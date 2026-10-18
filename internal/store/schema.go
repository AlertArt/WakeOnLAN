// 本文件承载 device 表的建表语句与迁移。
// 字段名与顺序即 docs/E1-contract.md 的持久化映射；
// E3 追加的三个校验状态列通过 ensureColumns 做幂等迁移，不改动已有字段。
package store

import (
	"context"
	"fmt"
	"strings"
)

const schemaDevice = `
CREATE TABLE IF NOT EXISTS device (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	name             TEXT    NOT NULL,
	mac              TEXT    NOT NULL,
	broadcast_addr   TEXT    NOT NULL,
	broadcast_port   INTEGER NOT NULL DEFAULT 9,
	probe_type       TEXT    NOT NULL DEFAULT 'none',
	probe_value      TEXT    NOT NULL DEFAULT '',
	probe_status     TEXT    NOT NULL DEFAULT 'pending',
	probe_message    TEXT    NOT NULL DEFAULT '',
	probe_checked_at TEXT    NOT NULL DEFAULT '',
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL
);

-- MAC 归一化后唯一，防止同一网卡被重复添加。
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_mac ON device (mac);
`

// Migrate 创建业务表并补齐新增列，幂等，可重复调用。
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, schemaDevice); err != nil {
		return fmt.Errorf("初始化 device 表失败: %w", err)
	}

	// E1 建库时没有这三列，老数据文件需要补列。用默认值填充，不重写已有行。
	added := []struct{ name, ddl string }{
		{"probe_status", `ALTER TABLE device ADD COLUMN probe_status TEXT NOT NULL DEFAULT 'pending'`},
		{"probe_message", `ALTER TABLE device ADD COLUMN probe_message TEXT NOT NULL DEFAULT ''`},
		{"probe_checked_at", `ALTER TABLE device ADD COLUMN probe_checked_at TEXT NOT NULL DEFAULT ''`},
	}
	for _, col := range added {
		exists, err := d.hasColumn(ctx, "device", col.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := d.sql.ExecContext(ctx, col.ddl); err != nil {
			return fmt.Errorf("为 device 表补列 %s 失败: %w", col.name, err)
		}
	}
	return nil
}

// hasColumn 查询表上是否已存在某列。
// 注意：连接池上限为 1，调用方必须保证 rows 已关闭后再执行 ALTER。
func (d *DB) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("读取表结构失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, fmt.Errorf("读取列名失败: %w", err)
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("遍历表结构失败: %w", err)
	}
	return false, nil
}
