package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Sparkzure/PayPrint/server/internal/database"
)

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(path, database.Options{MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("迁移数据库: %v", err)
	}
	return db
}

func migrationOf(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	v, err := database.MigrationVersion(db)
	if err != nil {
		t.Fatalf("读取数据库版本: %v", err)
	}
	return v
}

func TestBackupRoundTrip(t *testing.T) {
	ctx := context.Background()
	dirA := t.TempDir()
	db := openTestDB(t, filepath.Join(dirA, "data.db"))
	version := migrationOf(t, db)

	storageA := filepath.Join(dirA, "files")
	if err := os.MkdirAll(filepath.Join(storageA, "user-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageA, "user-1", "doc.pdf"), []byte("pdf-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO users (
		id, username, username_ci, password_hash, phone, role, status,
		created_at, updated_at, ban_reason, banned_at, banned_by
	) VALUES ('u1', 'alice', 'alice', 'x', '', 'user', 'banned', 1000, 1000, '恶意占用打印资源', 1200, 'owner')`); err != nil {
		t.Fatalf("种入数据: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteBackup(ctx, db, version, storageA, &buf); err != nil {
		t.Fatalf("导出备份: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("导出的备份不应为空")
	}

	dirB := t.TempDir()
	targetDB := filepath.Join(dirB, "data.db")
	targetFiles := filepath.Join(dirB, "files")
	stageDir := filepath.Join(dirB, "restore")

	manifest, err := Stage(bytes.NewReader(buf.Bytes()), stageDir, version)
	if err != nil {
		t.Fatalf("暂存备份: %v", err)
	}
	if manifest.MigrationVersion != version || manifest.Files != 1 {
		t.Fatalf("清单不正确：%+v", manifest)
	}

	applied, err := ApplyStagedRestore(targetDB, targetFiles, stageDir)
	if err != nil {
		t.Fatalf("应用备份: %v", err)
	}
	if !applied {
		t.Fatal("应当应用了备份")
	}

	restored := openTestDB(t, targetDB)
	var username, banReason, bannedBy, status string
	if err := restored.QueryRowContext(ctx,
		`SELECT username, ban_reason, banned_by, status FROM users WHERE id = 'u1'`,
	).Scan(&username, &banReason, &bannedBy, &status); err != nil {
		t.Fatalf("还原后的库读不到账号: %v", err)
	}
	if username != "alice" || banReason != "恶意占用打印资源" || bannedBy != "owner" || status != "banned" {
		t.Fatalf("还原后的数据不正确：%s / %s / %s / %s", username, banReason, bannedBy, status)
	}

	content, err := os.ReadFile(filepath.Join(targetFiles, "user-1", "doc.pdf"))
	if err != nil {
		t.Fatalf("还原后的上传文件缺失: %v", err)
	}
	if string(content) != "pdf-bytes" {
		t.Fatal("还原后的文件内容不正确")
	}

	if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
		t.Fatal("应用后应清理暂存目录")
	}
}

func TestApplyIsNoopWithoutStagedBackup(t *testing.T) {
	dir := t.TempDir()
	applied, err := ApplyStagedRestore(
		filepath.Join(dir, "data.db"), filepath.Join(dir, "files"), filepath.Join(dir, "restore"))
	if err != nil {
		t.Fatalf("空操作不应报错: %v", err)
	}
	if applied {
		t.Fatal("没有暂存备份却报告已应用")
	}
}

func addZipBytes(t *testing.T, zw *zip.Writer, name string, content []byte) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
}

func TestStageRejects(t *testing.T) {
	dir := t.TempDir()
	db := openTestDB(t, filepath.Join(dir, "data.db"))
	version := migrationOf(t, db)
	stageDir := filepath.Join(dir, "restore")

	t.Run("不是 zip", func(t *testing.T) {
		if _, err := Stage(strings.NewReader("hello"), stageDir, version); err == nil {
			t.Fatal("应当拒绝非 zip 内容")
		}
	})

	t.Run("缺少数据库文件", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := writeJSONEntry(zw, manifestName, Manifest{Format: FormatV1, MigrationVersion: version}); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(bytes.NewReader(buf.Bytes()), stageDir, version); err == nil {
			t.Fatal("应当拒绝缺数据库的备份")
		}
	})

	t.Run("版本比当前服务新", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := writeJSONEntry(zw, manifestName, Manifest{Format: FormatV1, MigrationVersion: version + 100}); err != nil {
			t.Fatal(err)
		}
		addZipBytes(t, zw, dbName, []byte("fake"))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(bytes.NewReader(buf.Bytes()), stageDir, version); err == nil {
			t.Fatal("应当拒绝结构更新的备份")
		}
	})

	t.Run("包里有白名单之外的条目（zip-slip）", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := writeJSONEntry(zw, manifestName, Manifest{Format: FormatV1, MigrationVersion: version}); err != nil {
			t.Fatal(err)
		}
		addZipBytes(t, zw, dbName, []byte("fake"))
		addZipBytes(t, zw, "../outside.txt", []byte("evil"))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(bytes.NewReader(buf.Bytes()), stageDir, version); err == nil {
			t.Fatal("应当拒绝含意外条目的备份")
		}

		if _, err := os.Stat(filepath.Join(dir, "outside.txt")); err == nil {
			t.Fatal("zip-slip 得手了：文件被写出了暂存目录")
		}
	})
}
