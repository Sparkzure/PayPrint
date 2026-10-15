package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Sparkzure/PayPrint/server/internal/backup"
	"github.com/Sparkzure/PayPrint/server/internal/config"
	"github.com/Sparkzure/PayPrint/server/internal/database"
	"github.com/Sparkzure/PayPrint/server/internal/domain"
	"github.com/Sparkzure/PayPrint/server/internal/httpapi"
	"github.com/Sparkzure/PayPrint/server/internal/ipp"
	"github.com/Sparkzure/PayPrint/server/internal/notifier"
	"github.com/Sparkzure/PayPrint/server/internal/service"
	"github.com/Sparkzure/PayPrint/server/internal/store"
	"github.com/Sparkzure/PayPrint/server/internal/webui"
)

var (
	version   = "dev"
	gitCommit = "unknown"
	builtAt   = "unknown"
)

func main() {
	configPath := flag.String("config", "",
		"配置文件路径；默认为二进制同目录下的 "+config.FileName+"，不存在则按默认值生成")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, warnings, err := config.Load(configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	log.Info("已装载配置", "config", cfg.Path, "dir", cfg.Dir)
	for _, warning := range warnings {
		log.Warn(warning)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restored, err := backup.ApplyStagedRestore(
		cfg.DBPath, cfg.StorageDir,
		filepath.Join(filepath.Dir(cfg.DBPath), "restore"),
	)
	if err != nil {
		return fmt.Errorf("应用待还原的备份: %w", err)
	}
	if restored {
		log.Info("已应用待还原的备份，旧数据保留为 *.pre-restore")
	}

	db, err := database.Open(cfg.DBPath, database.Options{})
	if err != nil {
		return err
	}
	defer db.Close()

	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	migrationVersion, _ := database.MigrationVersion(db)

	svc := service.New(&service.Deps{
		DB:       db,
		Q:        store.New(db),
		Cfg:      cfg,
		IPP:      ipp.NewClient(0, cfg.IPP.UserName),
		Triggers: buildTriggers(),
		Queriers: buildQueriers(cfg),
		Log:      log,
	})

	if err := svc.Settings.Reload(ctx); err != nil {
		log.Warn("读取运行时配置失败，先按内置默认值运行", "error", err)
	}

	if err := svc.Printers.Bootstrap(ctx); err != nil {
		log.Warn("登记默认打印机失败，可在管理面板里手工添加", "error", err)
	}
	log.Info("启动不依赖打印机：打印机未就绪不影响服务运行，打印机地址可在管理面板修改并测试")

	app := httpapi.NewServer(svc, cfg, log)

	app.OnShutdown = func(reason string) {
		log.Warn("收到管理面板的退出请求，开始优雅关闭", "reason", reason)
		stop()
	}
	srv := app.Router()
	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: srv,

		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       90 * time.Second,
	}

	runtime := svc.Runtime()
	log.Info("PayPrint 启动",
		"version", version,
		"commit", gitCommit,
		"built_at", builtAt,
		"env", cfg.Env,
		"config", cfg.Path,
		"addr", cfg.HTTPAddr,
		"db", cfg.DBPath,
		"migration_version", migrationVersion,
		"worker_enabled", cfg.Worker.Enabled,
		"open_registration", runtime.OpenRegistration,

		"auto_release_on_payment", runtime.AutoReleaseOnPayment,
		"require_payment_verification", runtime.RequirePaymentVerification,
		"release_ttl", runtime.ReleaseTTL,
		"order_ttl", runtime.OrderTTL,
		"payment_sources", svc.Triggers.Names(),
	)
	if !webui.Built() {

		log.Warn("这个二进制里没有前端产物，浏览器打开会是 404；用 scripts/web.ps1 下载后重新构建（CI 产物自带）")
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	if cfg.Worker.Enabled {
		go svc.RunWorkers(ctx, log)
	} else {
		log.Warn("后台任务循环已关闭：打印任务不会自动投递")
	}

	select {
	case err := <-serverErr:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	case <-ctx.Done():
		log.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("优雅关闭超时", "error", err)
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}

func buildTriggers() *notifier.Registry {
	registry := notifier.NewRegistry()
	registry.Register(notifier.WeChatAndroidTrigger{})

	registry.Register(notifier.CollectorAPITrigger{})
	return registry
}

func buildQueriers(cfg *config.Config) *notifier.Queriers {
	queriers := notifier.NewQueriers(notifier.NopQuerier{})
	if cfg.WeChat.Enabled {
		queriers.Register(string(domain.ChannelWeChat), notifier.NewWeChatOrderQuerier(notifier.WeChatV2Config{
			AppID:    cfg.WeChat.AppID,
			MchID:    cfg.WeChat.MchID,
			APIKey:   cfg.WeChat.APIKey,
			QueryURL: cfg.WeChat.QueryURL,
			Timeout:  cfg.WeChat.Timeout,
		}))
	}
	return queriers
}
