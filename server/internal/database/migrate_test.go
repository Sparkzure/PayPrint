package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigrationUpgradeFromOperatorModel(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"), Options{})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	defer db.Close()

	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("设置 dialect: %v", err)
	}
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.UpToContext(ctx, db, "migrations", 6); err != nil {
		t.Fatalf("升级到 00006: %v", err)
	}

	mustExec(t, db, `INSERT INTO printers (id, code, name, cups_uri, location, status,
		supports_color, supports_duplex, paper_sizes, price_bw_fen, price_color_fen,
		max_copies, created_at, updated_at)
		VALUES ('prn-a', 'A', 'A 点', 'ipp://x/ipp/print', '', 'online', 0, 0, '["A4"]', 100, 0, 10, 1, 1)`)
	mustExec(t, db, `INSERT INTO users (id, username, username_ci, password_hash, role,
		status, created_at, updated_at)
		VALUES ('u-op', 'site-a', 'site-a', 'x', 'operator', 'active', 1, 1)`)
	mustExec(t, db, `INSERT INTO users (id, username, username_ci, password_hash, role,
		status, created_at, updated_at)
		VALUES ('u-user', 'alice', 'alice', 'x', 'user', 'active', 1, 1)`)
	mustExec(t, db, `INSERT INTO devices (id, fingerprint, public_key, status, user_id,
		bound_at, label, platform, app_version, first_seen_at, last_seen_at, created_at,
		updated_at, collector_printer_id)
		VALUES ('dev-op', 'fp-op', 'pk', 'active', 'u-op', 1, '收款机', '', '', 1, 1, 1, 1, 'prn-a')`)
	mustExec(t, db, `INSERT INTO sessions (id, user_id, device_id, token_hash, issued_at,
		expires_at, revoked_at, last_used_at)
		VALUES ('sess-1', 'u-op', 'dev-op', 'hash-1', 1, 9999999999, 0, 1)`)

	mustExec(t, db, `INSERT INTO users (id, username, username_ci, password_hash, role,
		status, created_at, updated_at)
		VALUES ('u-admin', 'owner', 'owner', 'x', 'admin', 'active', 1, 1)`)
	mustExec(t, db, `INSERT INTO devices (id, fingerprint, public_key, status, user_id,
		bound_at, label, platform, app_version, first_seen_at, last_seen_at, created_at,
		updated_at, collector_printer_id)
		VALUES ('dev-admin', 'fp-admin', 'pk-admin', 'active', 'u-admin', 1, '运维机',
		'', '', 1, 1, 1, 1, NULL)`)

	mustExec(t, db, `INSERT INTO sessions (id, user_id, device_id, token_hash, issued_at,
		expires_at, revoked_at, last_used_at)
		VALUES ('sess-admin-dev', 'u-admin', 'dev-admin', 'hash-3', 1, 9999999999, 0, 1)`)
	mustExec(t, db, `INSERT INTO device_nonces (fingerprint, nonce, created_at, expires_at)
		VALUES ('fp-admin', 'nonce-admin', 1, 9999999999)`)

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("升级到最新版本: %v", err)
	}

	var role string
	if err := db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = 'u-op'`).Scan(&role); err != nil {
		t.Fatalf("读取用户角色: %v", err)
	}
	if role != "user" {
		t.Fatalf("运营账号应转为普通用户，实际 %q", role)
	}

	var deviceID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT device_id FROM sessions WHERE id = 'sess-1'`).
		Scan(&deviceID); err != nil {
		t.Fatalf("读取会话: %v", err)
	}
	if !deviceID.Valid || deviceID.String != "dev-op" {
		t.Fatalf("已有会话的设备绑定不应丢失，实际 %+v", deviceID)
	}

	if hasColumn(t, db, "devices", "collector_printer_id") {
		t.Fatal("devices.collector_printer_id 仍然存在，迁移没有生效")
	}

	mustExec(t, db, `INSERT INTO sessions (id, user_id, device_id, token_hash, issued_at,
		expires_at, revoked_at, last_used_at)
		VALUES ('sess-admin', 'u-user', NULL, 'hash-2', 1, 9999999999, 0, 1)`)
	mustExec(t, db, `INSERT INTO collector_credentials (id, printer_id, label, token_hash, created_at)
		VALUES ('cred-1', 'prn-a', 'A 点收款机', 'cred-hash-1', 1)`)

	if _, err := db.ExecContext(ctx, `INSERT INTO collector_credentials
		(id, printer_id, label, token_hash, created_at)
		VALUES ('cred-2', 'prn-missing', '', 'cred-hash-2', 1)`); err == nil {
		t.Fatal("收款凭据指向不存在的打印机时应当被外键拒绝")
	}

	var adminStatus, adminFingerprint, adminKey string
	var adminUserID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT status, fingerprint, user_id, public_key FROM devices
		WHERE id = 'dev-admin'`).Scan(&adminStatus, &adminFingerprint, &adminUserID, &adminKey); err != nil {
		t.Fatalf("读取管理员设备: %v", err)
	}
	if adminStatus != "released" || adminUserID.Valid || adminKey != "" {
		t.Fatalf("管理员设备应已释放，实际 status=%q user_id=%+v public_key=%q",
			adminStatus, adminUserID, adminKey)
	}
	if adminFingerprint != "released:dev-admin" {
		t.Fatalf("释放必须让出指纹（否则这台机器永远只能登录、不能再注册），实际 %q",
			adminFingerprint)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM sessions WHERE device_id = 'dev-admin'`); n != 0 {
		t.Fatalf("释放应当清空该设备的会话，仍有 %d 条", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM device_nonces WHERE fingerprint = 'fp-admin'`); n != 0 {
		t.Fatalf("释放应当清空该指纹的重放账本，仍有 %d 条", n)
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM sessions WHERE id = 'sess-1'`); n != 1 {
		t.Fatalf("成员设备的会话不应被迁移删除，实际 %d 条", n)
	}

	var memberUserID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM devices WHERE id = 'dev-op'`).
		Scan(&memberUserID); err != nil {
		t.Fatalf("读取成员设备: %v", err)
	}
	if !memberUserID.Valid || memberUserID.String != "u-op" {
		t.Fatalf("成员账号的设备绑定不应被迁移改动，实际 %+v", memberUserID)
	}
}

func TestMigrationUpgradeBackfillsOrderAudits(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "audit-upgrade.db"), Options{})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	defer db.Close()

	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("设置 dialect: %v", err)
	}
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())

	if err := goose.UpToContext(ctx, db, "migrations", 13); err != nil {
		t.Fatalf("升级到 00013: %v", err)
	}

	mustExec(t, db, `INSERT INTO printers (id, code, name, cups_uri, created_at, updated_at)
		VALUES ('prn-1', 'P01', '测试机', 'ipp://127.0.0.1:631/ipp/print', 1, 1)`)
	mustExec(t, db, `INSERT INTO users (id, username, username_ci, password_hash,
		display_name, role, status, created_at, updated_at)
		VALUES ('u-1', 'alice', 'alice', 'x', 'Alice', 'user', 'active', 1, 1)`)
	mustExec(t, db, `INSERT INTO devices (id, fingerprint, public_key, status, user_id,
		label, platform, app_version, first_seen_at, last_seen_at, created_at, updated_at)
		VALUES ('dev-1', 'fp-old', 'pk', 'active', 'u-1', '测试机', '', '', 1, 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO print_files (id, user_id, original_name, size_bytes,
		sha256, storage_path, page_count, created_at)
		VALUES ('file-1', 'u-1', '报告.pdf', 1024, 'sha', '/tmp/报告.pdf', 2, 1)`)
	mustExec(t, db, `INSERT INTO orders (id, order_no, user_id, device_id, printer_id,
		file_id, status, amount_fen, title, params_json, cost_json, billable_sides,
		sheets_per_copy, created_at, updated_at, expires_at)
		VALUES ('order-1', 'OC1', 'u-1', 'dev-1', 'prn-1', 'file-1', 'completed', 600,
		'报告.pdf', '{"copies":3,"color_mode":"color","duplex":"long_edge"}', '{}',
		2, 2, 100, 100, 200)`)
	mustExec(t, db, `INSERT INTO payment_events (id, event_id, source, channel, order_id,
		order_no, device_id, amount_fen, status, reported_at, client_ts, created_at)
		VALUES ('evt-1', 'evt-1', 'wechat_android', 'wechat', 'order-1', 'OC1', 'dev-1',
		600, 'accepted', 100, 100, 100)`)

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("升级到最新版本: %v", err)
	}

	if n := countRows(t, db, `SELECT COUNT(*) FROM orders`); n != 1 {
		t.Fatalf("升级不应丢订单，实际剩 %d 条", n)
	}
	if hasColumn(t, db, "orders", "device_id") {
		t.Fatal("orders.device_id 仍然存在，迁移没有生效")
	}
	if hasColumn(t, db, "payment_events", "device_id") {
		t.Fatal("payment_events.device_id 仍然存在，迁移没有生效")
	}

	var (
		orderNo, username, deviceLabel, fingerprint, fileName, colorMode, duplex string
		copies, totalPages, amountFen                                            int64
	)
	if err := db.QueryRowContext(ctx, `SELECT order_no, username, device_label,
		device_fingerprint, file_name, color_mode, duplex, copies, total_pages, amount_fen
		FROM order_audits WHERE order_id = 'order-1'`).Scan(
		&orderNo, &username, &deviceLabel, &fingerprint, &fileName, &colorMode, &duplex,
		&copies, &totalPages, &amountFen); err != nil {
		t.Fatalf("读取回填的审计记录: %v", err)
	}
	if orderNo != "OC1" || username != "alice" {
		t.Fatalf("账号字段不正确: order_no=%q username=%q", orderNo, username)
	}
	if deviceLabel != "测试机" || fingerprint != "fp-old" {
		t.Fatalf("设备快照不正确: label=%q fingerprint=%q", deviceLabel, fingerprint)
	}
	if fileName != "报告.pdf" || colorMode != "color" || duplex != "long_edge" {
		t.Fatalf("打印内容不正确: file=%q color=%q duplex=%q", fileName, colorMode, duplex)
	}
	if copies != 3 || totalPages != 6 || amountFen != 600 {
		t.Fatalf("打印类型/金额不正确: copies=%d total_pages=%d amount=%d",
			copies, totalPages, amountFen)
	}

	var userID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM payment_events WHERE id = 'evt-1'`).
		Scan(&userID); err != nil {
		t.Fatalf("读取到账事件的账号: %v", err)
	}
	if !userID.Valid || userID.String != "u-1" {
		t.Fatalf("到账事件应当带上订单的账号，实际 %+v", userID)
	}

	if countRows(t, db, `SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'audit_logs'`) != 0 {
		t.Fatal("audit_logs 仍然存在，迁移没有生效")
	}
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("执行 SQL 失败: %v\n%s", err, query)
	}
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("统计失败: %v\n%s", err, query)
	}
	return n
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("读取表结构: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描列名: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列名: %v", err)
	}
	return false
}
