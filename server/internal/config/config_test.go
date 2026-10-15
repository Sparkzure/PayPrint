package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTemplateIsDefaults(t *testing.T) {
	vals, err := parse(Template())
	if err != nil {
		t.Fatalf("解析模板: %v", err)
	}
	if unused, err := apply(&Config{}, vals); err != nil || len(unused) != 0 {
		t.Fatalf("模板里的项必须全部被认识: unused=%v err=%v", unused, err)
	}

	cfg, err := defaults()
	if err != nil {
		t.Fatalf("装载默认值: %v", err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("默认值必须能过校验: %v", err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBPath != "data/payprint.db" {
		t.Fatalf("默认值不对: addr=%q db=%q", cfg.HTTPAddr, cfg.DBPath)
	}
}

func TestDefaultPathLeavesEphemeralBuildDirs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到自身路径: %v", err)
	}
	if !isEphemeralDir(filepath.Dir(exe)) {
		t.Skipf("测试二进制不在临时目录里（%s），跳过", exe)
	}
	if got := DefaultPath(); got != FileName {
		t.Fatalf("临时构建目录下应退回工作目录: got %q want %q", got, FileName)
	}
}

func TestIsEphemeralDir(t *testing.T) {
	goBuild := filepath.Join(os.TempDir(), "go-build123", "b001", "exe")
	if !isEphemeralDir(goBuild) {
		t.Fatalf("go run 的构建目录应判为临时: %s", goBuild)
	}
	insideTemp := filepath.Join(os.TempDir(), "somewhere-else")
	if !isEphemeralDir(insideTemp) {
		t.Fatalf("临时目录下应判为临时: %s", insideTemp)
	}
	normal := filepath.Join(string(os.PathSeparator), "opt", "payprint")
	if isEphemeralDir(normal) {
		t.Fatalf("普通部署目录不该判为临时: %s", normal)
	}
}

func TestLoadGeneratesFileOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	cfg, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("首次装载失败: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("首次装载不该有警告: %v", warnings)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置文件没有被生成: %v", err)
	}
	if string(raw) != Template() {
		t.Fatal("生成的文件内容应当就是内置模板")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}

		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("生成的文件权限应为 0600，实际 %o", perm)
		}
	}

	wantDB := filepath.Join(dir, "data", "payprint.db")
	if cfg.DBPath != wantDB {
		t.Fatalf("db_path 应解析到配置文件旁边: got %q want %q", cfg.DBPath, wantDB)
	}
	if cfg.StorageDir != filepath.Join(dir, "data", "files") {
		t.Fatalf("storage_dir 应解析到配置文件旁边: got %q", cfg.StorageDir)
	}
	if cfg.Path != path {
		t.Fatalf("cfg.Path 应为传入路径: got %q want %q", cfg.Path, path)
	}
}

func TestLoadOverridesOnlyPresentKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	content := `
# 只改端口与日志级别，其余保持默认
[server]
http_addr = 127.0.0.1:9999
log_level = debug

[security]
; 值里的分号不是注释
nonce_ttl = 40m

[worker]
lease = 3m
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试配置: %v", err)
	}

	cfg, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("不该有警告: %v", warnings)
	}
	if cfg.HTTPAddr != "127.0.0.1:9999" || cfg.LogLevel != "debug" {
		t.Fatalf("server 段没生效: addr=%q level=%q", cfg.HTTPAddr, cfg.LogLevel)
	}
	if cfg.NonceTTL != 40*time.Minute || cfg.Worker.Lease != 3*time.Minute {
		t.Fatalf("时长没生效: nonce=%s lease=%s", cfg.NonceTTL, cfg.Worker.Lease)
	}

	if cfg.Worker.Enabled != true || cfg.Worker.Interval != 5*time.Second {
		t.Fatalf("没写的项应当保持默认: %+v", cfg.Worker)
	}
	if len(cfg.CORSOrigins) != 2 {
		t.Fatalf("没写的项应当保持默认: %v", cfg.CORSOrigins)
	}
}

func TestLoadWarnsOnUnknownKeyAndFailsOnBadValue(t *testing.T) {
	dir := t.TempDir()

	unknown := filepath.Join(dir, "unknown.conf")
	if err := os.WriteFile(unknown, []byte("[server]\nhttp_addr = :1\nnope = 1\n"), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	if _, warnings, err := Load(unknown); err != nil || len(warnings) != 1 {
		t.Fatalf("认不出的键应只警告: warnings=%v err=%v", warnings, err)
	}

	bad := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(bad, []byte("[server]\nhttp_addr = :1\n\n[security]\nnonce_ttl = 15\n"), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	_, _, err := Load(bad)
	if err == nil {
		t.Fatal("非法时长必须报错，不能静默按默认值跑")
	}

	if !strings.Contains(err.Error(), "security.nonce_ttl") {
		t.Fatalf("报错里应当指出键名: %v", err)
	}
}

func TestLoadRejectsNonceShorterThanWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	content := "[security]\nsignature_skew = 10m\nnonce_ttl = 15m\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("nonce_ttl < 2 * signature_skew 必须拒绝启动")
	}
}

func TestLoadRequiresPaymentCredentialsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("[payment]\nquery_enabled = true\n"), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("开了兜底查单却没给商户信息，必须拒绝启动")
	}
}

func TestLoadPointsOutRenamedSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	content := "[server]\nhttp_addr = :1\n\n[cups]\ndefault_printer_uri = ipp://192.168.1.50:631/printers/a\n\n[wechat]\nquery_enabled = false\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写配置: %v", err)
	}

	cfg, warnings, err := Load(path)
	if err != nil {
		t.Fatalf("旧段名不该导致启动失败: %v", err)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "[printer]") || !strings.Contains(joined, "[payment]") {
		t.Fatalf("警告里应当点出新段名: %v", warnings)
	}

	if cfg.DefaultPrinterURI == "ipp://192.168.1.50:631/printers/a" {
		t.Fatal("旧段名下的值不该被读取")
	}
}

func TestParseDetails(t *testing.T) {

	vals, err := parse("[server]\r\nhttp_addr = \":8080\"   # 端口\r\n\r\ntoken = a#b\n")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if vals["server.http_addr"] != ":8080" {
		t.Fatalf("引号与行尾注释没处理对: %q", vals["server.http_addr"])
	}
	if vals["server.token"] != "a#b" {
		t.Fatalf("值里的 # 不是注释: %q", vals["server.token"])
	}

	if _, err := parse("http_addr :8080"); err == nil {
		t.Fatal("缺少 = 应当报错")
	}
	if _, err := parse("[server\nhttp_addr = :1"); err == nil {
		t.Fatal("段名缺 ] 应当报错")
	}
}
