package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const FileName = "payprint.conf"

func DefaultPath() string {
	exe, err := os.Executable()
	if err != nil {

		return FileName
	}
	dir := filepath.Dir(exe)
	if isEphemeralDir(dir) {
		return FileName
	}
	return filepath.Join(dir, FileName)
}

func isEphemeralDir(dir string) bool {
	dir = filepath.Clean(dir)
	lower := strings.ToLower(dir)

	if strings.Contains(lower, "go-build") {
		return true
	}
	for _, root := range []string{os.TempDir(), os.Getenv("GOTMPDIR")} {
		if root == "" {
			continue
		}
		prefix := strings.ToLower(filepath.Clean(root) + string(os.PathSeparator))
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

type Config struct {
	Path string
	Dir  string

	Env      string
	HTTPAddr string

	DBPath     string
	StorageDir string
	LogLevel   string

	SessionTTL time.Duration

	SignatureSkew time.Duration

	NonceTTL time.Duration

	MaxUploadBytes int64

	AdminToken string

	DefaultPrinterURI  string
	DefaultPrinterName string

	CORSOrigins []string

	IPP    IPPConfig
	Worker WorkerConfig
	WeChat WeChatConfig
}

type IPPConfig struct {
	UserName string

	JobFormat string

	RetryBase time.Duration
	RetryMax  time.Duration
}

type WorkerConfig struct {
	Enabled  bool
	Interval time.Duration
	Batch    int

	Lease time.Duration
}

type WeChatConfig struct {
	Enabled   bool
	AppID     string
	MchID     string
	APIKey    string
	QueryURL  string
	Timeout   time.Duration
	MaxAttemp int
}

func Load(path string) (*Config, []string, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return nil, nil, err
	}

	cfg, err := defaults()
	if err != nil {
		return nil, nil, err
	}

	var warnings []string
	raw, err := os.ReadFile(resolved)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if writeErr := os.WriteFile(resolved, []byte(Template()), 0o600); writeErr != nil {
			warnings = append(warnings, fmt.Sprintf(
				"生成配置文件 %s 失败，按内置默认值运行：%v", resolved, writeErr))
		}
	case err != nil:
		return nil, nil, fmt.Errorf("读取配置文件 %s: %w", resolved, err)
	default:
		values, parseErr := parse(string(raw))
		if parseErr != nil {
			return nil, nil, fmt.Errorf("配置文件 %s: %w", resolved, parseErr)
		}
		unused, applyErr := apply(cfg, values)
		for _, key := range unused {
			if hint := renamedSection(key); hint != "" {
				warnings = append(warnings, fmt.Sprintf(
					"配置文件 %s 里有认不出的项 %q（已忽略）：%s", resolved, key, hint))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("配置文件 %s 里有认不出的项 %q（已忽略）", resolved, key))
		}
		if applyErr != nil {
			return nil, warnings, fmt.Errorf("配置文件 %s: %w", resolved, applyErr)
		}
	}

	cfg.Path = resolved
	cfg.Dir = filepath.Dir(resolved)
	cfg.DBPath = resolveAgainst(cfg.Dir, cfg.DBPath)
	cfg.StorageDir = resolveAgainst(cfg.Dir, cfg.StorageDir)

	if err := cfg.validate(); err != nil {
		return nil, warnings, fmt.Errorf("配置文件 %s: %w", resolved, err)
	}
	return cfg, warnings, nil
}

func renamedSection(key string) string {
	switch {
	case strings.HasPrefix(key, "cups."):
		return "该段已更名为 [printer]，请改段名，否则这一段的设置不会生效"
	case strings.HasPrefix(key, "wechat."):
		return "该段已更名为 [payment]，请改段名，否则这一段的设置不会生效"
	default:
		return ""
	}
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.DBPath) == "" {
		return errors.New("storage.db_path 不能为空")
	}

	if c.NonceTTL < 2*c.SignatureSkew {
		return fmt.Errorf("security.nonce_ttl(%s) 必须 >= 2 * security.signature_skew(%s)",
			c.NonceTTL, c.SignatureSkew)
	}
	if c.WeChat.Enabled {
		if c.WeChat.AppID == "" || c.WeChat.MchID == "" || c.WeChat.APIKey == "" {
			return errors.New("payment.query_enabled 打开时必须同时填 payment.app_id / payment.mch_id / payment.api_key")
		}
	}
	return nil
}

func resolvePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		abs, err := filepath.Abs(DefaultPath())
		if err != nil {
			return "", fmt.Errorf("解析默认配置文件路径: %w", err)
		}
		return abs, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析配置文件路径 %s: %w", path, err)
	}
	return abs, nil
}

func resolveAgainst(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

type values struct {
	m    map[string]string
	used map[string]bool
	err  error
}

func (v *values) raw(key string) (string, bool) {
	raw, ok := v.m[key]
	if ok {
		v.used[key] = true
	}
	return raw, ok
}

func (v *values) fail(key, raw, want string) {
	if v.err == nil {
		v.err = fmt.Errorf("%s = %q 不是合法的%s", key, raw, want)
	}
}

func (v *values) str(key string, dst *string) {
	if raw, ok := v.raw(key); ok {
		*dst = raw
	}
}

func (v *values) dur(key string, dst *time.Duration) {
	raw, ok := v.raw(key)
	if !ok {
		return
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		v.fail(key, raw, "时长（例如 15m / 30s / 720h）")
		return
	}
	*dst = parsed
}

func (v *values) int(key string, dst *int) {
	raw, ok := v.raw(key)
	if !ok {
		return
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		v.fail(key, raw, "整数")
		return
	}
	*dst = parsed
}

func (v *values) i64(key string, dst *int64) {
	raw, ok := v.raw(key)
	if !ok {
		return
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		v.fail(key, raw, "整数")
		return
	}
	*dst = parsed
}

func (v *values) boolean(key string, dst *bool) {
	raw, ok := v.raw(key)
	if !ok {
		return
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		v.fail(key, raw, "布尔值（true / false）")
		return
	}
	*dst = parsed
}

func (v *values) list(key string, dst *[]string) {
	raw, ok := v.raw(key)
	if !ok {
		return
	}
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	*dst = out
}

func (v *values) unused() []string {
	var out []string
	for key := range v.m {
		if !v.used[key] {
			out = append(out, key)
		}
	}
	return out
}

func apply(cfg *Config, vals map[string]string) ([]string, error) {
	v := &values{m: vals, used: make(map[string]bool, len(vals))}

	v.str("server.env", &cfg.Env)
	v.str("server.http_addr", &cfg.HTTPAddr)
	v.str("server.log_level", &cfg.LogLevel)
	v.list("server.cors_origins", &cfg.CORSOrigins)

	v.str("storage.db_path", &cfg.DBPath)
	v.str("storage.storage_dir", &cfg.StorageDir)

	v.dur("security.session_ttl", &cfg.SessionTTL)
	v.dur("security.signature_skew", &cfg.SignatureSkew)
	v.dur("security.nonce_ttl", &cfg.NonceTTL)
	v.i64("security.max_upload_bytes", &cfg.MaxUploadBytes)
	v.str("security.admin_token", &cfg.AdminToken)

	v.str("printer.default_printer_uri", &cfg.DefaultPrinterURI)
	v.str("printer.default_printer_name", &cfg.DefaultPrinterName)
	v.str("printer.user_name", &cfg.IPP.UserName)
	v.str("printer.job_format", &cfg.IPP.JobFormat)
	v.dur("printer.retry_base", &cfg.IPP.RetryBase)
	v.dur("printer.retry_max", &cfg.IPP.RetryMax)

	v.boolean("worker.enabled", &cfg.Worker.Enabled)
	v.dur("worker.interval", &cfg.Worker.Interval)
	v.int("worker.batch", &cfg.Worker.Batch)
	v.dur("worker.lease", &cfg.Worker.Lease)

	v.boolean("payment.query_enabled", &cfg.WeChat.Enabled)
	v.str("payment.app_id", &cfg.WeChat.AppID)
	v.str("payment.mch_id", &cfg.WeChat.MchID)
	v.str("payment.api_key", &cfg.WeChat.APIKey)
	v.str("payment.query_url", &cfg.WeChat.QueryURL)
	v.dur("payment.timeout", &cfg.WeChat.Timeout)
	v.int("payment.max_attempts", &cfg.WeChat.MaxAttemp)

	return v.unused(), v.err
}

func defaults() (*Config, error) {
	vals, err := parse(Template())
	if err != nil {
		return nil, fmt.Errorf("内置配置模板有误: %w", err)
	}
	cfg := &Config{}
	if _, err := apply(cfg, vals); err != nil {
		return nil, fmt.Errorf("内置配置模板有误: %w", err)
	}
	return cfg, nil
}

func parse(text string) (map[string]string, error) {
	out := make(map[string]string)
	section := ""

	for i, line := range strings.Split(text, "\n") {
		lineNo := i + 1
		line = strings.TrimSpace(stripComment(line))
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("第 %d 行：段名少了收尾的 ]（%q）", lineNo, line)
			}
			section = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")))
			if section == "" {
				return nil, fmt.Errorf("第 %d 行：段名不能为空", lineNo)
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("第 %d 行：应为 key = value 或 [段]（%q）", lineNo, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("第 %d 行：键不能为空", lineNo)
		}
		if section != "" {
			key = section + "." + key
		}
		out[key] = unquote(strings.TrimSpace(value))
	}
	return out, nil
}

func stripComment(line string) string {
	for i, r := range line {
		if r != '#' && r != ';' {
			continue
		}
		if i == 0 {
			return ""
		}
		if prev := line[i-1]; prev == ' ' || prev == '\t' {
			return line[:i]
		}
	}
	return line
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func Template() string {
	return `# payprint 配置文件
#
# 首次启动时若本文件不存在，服务会按这里的默认值生成一份再继续启动。
# 改完重启进程生效；运行中可在管理面板修改的那些项不在这里（它们在数据库里，改完即生效）。
#
# 语法：
#   [段]                分段
#   key = value         键值；# 或 ; 之后是注释；值两端成对的引号会被去掉
#   删掉一行            该项回到默认值（列在此文件里的这些即为默认值）
#
# 路径：storage 段里的相对路径以本文件所在目录为基准解析，
# 因此"二进制 + 本文件 + data/ 目录"可以整体搬走，与进程的工作目录无关。

[server]
# 仅用于日志标识。
env = production
# HTTP 监听地址。只暴露给局域网，不要直接对公网开放。
http_addr = :8080
# debug / info / warn / error
log_level = info
# 允许跨域的前端来源（逗号分隔）。同一台机器上由本进程提供前端时留空即可；
# 开发时前端跑在 vite（5173），需要这两项。
cors_origins = http://localhost:5173,http://127.0.0.1:5173

[storage]
# SQLite 数据库文件。
db_path = data/payprint.db
# 上传的文件（每个账号一个子目录）。与数据库一起备份即可。
storage_dir = data/files

[security]
# 会话有效期。
session_ttl = 720h
# 设备签名允许的时间戳偏移窗口。
signature_skew = 5m
# nonce 保留时长，必须 >= 2 * signature_skew（启动时校验，不满足直接拒绝启动）。
nonce_ttl = 15m
# 单个上传文件的字节上限（默认 50 MiB）。
max_upload_bytes = 52428800
# 运维令牌：给脚本、CI 与紧急救援用。留空则这条通道关闭（已有管理员仍可从面板登录，
# 空库后的第一个注册账号会自动成为管理员）。
# 生成方式：openssl rand -base64 32
admin_token =

[printer]
# 打印机地址（IPP）。只在首次启动、打印机表为空时用来种一台待配置的打印机。
# 无线打印机填 ipp://<打印机IP>/ipp/print；打印机不支持网络时先搭一台 CUPS 网关，
# 填 ipp://<网关IP>:631/printers/<队列名>。
# 上线后在管理面板「打印机」页维护，改完可以点「测试连接」当场验证。
default_printer_uri = ipp://127.0.0.1:631/ipp/print
default_printer_name = 默认打印机
# 上报给打印机的 requesting-user-name。
user_name = payprint
# 文档格式，目前只支持 PDF。
job_format = application/pdf
# 投递失败时的指数退避区间。
retry_base = 30s
retry_max = 10m

[worker]
# 后台任务循环（打印投递、状态回查、超时回收）。多实例部署时只让一个实例为 true。
enabled = true
interval = 5s
batch = 20
# 任务租约：认领后超过这个时长仍未写终态的任务会被判为"孤儿"（进程在投递途中退出）
# 并自动回收重投。别调得比一次 IPP 投递的耗时还短，否则慢但活着的 worker 会被抢。
lease = 2m

[payment]
# 到账核验的官方兜底查单（目前实现的是微信 v2 查单）。
# 打开后必须同时提供 app_id / mch_id / api_key，否则启动失败；
# 关闭时明确返回 unknown，绝不会把"没查过"当成"已确认"。
query_enabled = false
app_id =
mch_id =
api_key =
query_url = https://api.mch.weixin.qq.com/pay/orderquery
timeout = 10s
max_attempts = 5
`
}
