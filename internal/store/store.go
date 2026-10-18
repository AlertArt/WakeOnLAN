// Package store 负责 SQLite 持久化。E0 阶段只建立连接与最小自检，
// 设备表结构（F1）由 E1 在此包内新增，禁止在骨架阶段预建业务表。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，免 CGO，便于交叉编译到 arm64
)

// DB 包裹 *sql.DB，后续业务查询方法挂在此类型上。
type DB struct {
	sql *sql.DB
}

// Open 打开（或创建）数据目录下的 SQLite 文件，并完成连通性自检。
func Open(ctx context.Context, dataDir, fileName string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	path := filepath.Join(dataDir, fileName)
	q := url.Values{}
	// WAL 提升读写并发；busy_timeout 避免瞬时锁冲突直接报错。
	q.Set("_pragma", "busy_timeout(5000)")
	q.Set("_pragma", "journal_mode(WAL)")
	dsn := "file:" + path + "?" + q.Encode()

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// 单机单用户场景，连接数固定为 1，避免 SQLite 写锁竞争。
	sqlDB.SetMaxOpenConns(1)
	// 连接空闲即释放，避免文件句柄长期占用（Windows 下会影响目录清理）。
	sqlDB.SetMaxIdleConns(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("数据库连通性自检失败: %w", err)
	}

	return &DB{sql: sqlDB}, nil
}

// Ping 供健康检查使用。
func (d *DB) Ping(ctx context.Context) error {
	return d.sql.PingContext(ctx)
}

// Close 释放连接。
func (d *DB) Close() error {
	return d.sql.Close()
}
