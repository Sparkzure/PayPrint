package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Migrate(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("设置 goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("执行数据库迁移: %w", err)
	}
	return nil
}

func MigrationVersion(db *sql.DB) (int64, error) {
	v, err := goose.GetDBVersion(db)
	if err != nil {
		return 0, fmt.Errorf("读取迁移版本: %w", err)
	}
	return v, nil
}
