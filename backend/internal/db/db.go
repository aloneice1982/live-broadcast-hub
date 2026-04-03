package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// Open 打开 SQLite 连接并执行 schema 迁移
func Open(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_foreign_keys=on&_timeout=5000", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite 单写者模型：设置最大连接数
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	// 读取同目录下的 schema.sql
	schemaPath := filepath.Join(filepath.Dir(os.Args[0]), "db", "schema.sql")
	// 开发环境 fallback
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		schemaPath = "./db/schema.sql"
	}

	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}

	if _, err = db.Exec(string(schema)); err != nil {
		return err
	}

	// 补列迁移：CREATE TABLE IF NOT EXISTS 不会为已有表添加新列
	// 这里逐列执行 ALTER TABLE，忽略"duplicate column"错误（已有列则跳过）
	for _, stmt := range []string{
		`ALTER TABLE stream_sources ADD COLUMN match_datetime TEXT`,
		`ALTER TABLE stream_sources ADD COLUMN round TEXT`,
		`ALTER TABLE stream_sources ADD COLUMN channel TEXT`,
		`ALTER TABLE stream_sources ADD COLUMN remark TEXT`,
		`ALTER TABLE promotional_videos ADD COLUMN thumbnail_path TEXT`,
		`ALTER TABLE promotional_videos ADD COLUMN display_name TEXT`,
	} {
		db.Exec(stmt) //nolint:errcheck — duplicate column error is expected on fresh DBs
	}
	return nil
}
