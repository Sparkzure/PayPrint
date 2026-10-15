package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Options struct {
	MaxOpenConns int
}

func Open(path string, opts Options) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录 %s: %w", dir, err)
		}
	}

	dsn := buildDSN(path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}

	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 8
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)

	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	return db, nil
}

func buildDSN(path string) string {
	p := filepath.ToSlash(path)

	p = strings.NewReplacer("?", "%3f", "#", "%23").Replace(p)

	params := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}
	return "file:" + p + "?" + strings.Join(params, "&")
}
