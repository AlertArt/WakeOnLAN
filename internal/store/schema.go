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
	metrics_json     TEXT    NOT NULL DEFAULT '',
	metrics_at       TEXT    NOT NULL DEFAULT '',
	rdp_enabled      INTEGER NOT NULL DEFAULT 0,
	rdp_blank_before TEXT    NOT NULL DEFAULT '',
	rdp_blank_allowed INTEGER NOT NULL DEFAULT 0,
	created_at       TEXT    NOT NULL,
	updated_at       TEXT    NOT NULL
);

-- MAC 归一化后唯一，防止同一网卡被重复添加。
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_mac ON device (mac);
`

// F10 的指令队列。独立建表而不是塞进 device：指令是事件流，设备是实体。
const schemaCommand = `
CREATE TABLE IF NOT EXISTS device_command (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id      INTEGER NOT NULL,
	type           TEXT    NOT NULL,
	delay_seconds  INTEGER NOT NULL DEFAULT 60,
	force          INTEGER NOT NULL DEFAULT 0,
	status         TEXT    NOT NULL DEFAULT 'pending',
	message        TEXT    NOT NULL DEFAULT '',
	allow_blank_password INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT    NOT NULL,
	delivered_at   TEXT    NOT NULL DEFAULT '',
	finished_at    TEXT    NOT NULL DEFAULT '',
	expires_at     TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_device_command_status ON device_command (device_id, status);
`

// Migrate 创建业务表并补齐新增列，幂等，可重复调用。
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, schemaDevice); err != nil {
		return fmt.Errorf("初始化 device 表失败: %w", err)
	}
	if _, err := d.sql.ExecContext(ctx, schemaCommand); err != nil {
		return fmt.Errorf("初始化 device_command 表失败: %w", err)
	}

	// 老库缺列时补列。用默认值填充，不重写已有行。
	// rdp_blank_before 用空串表示"我们没动过这条策略"，而不是用 0/1——
	// 必须能把"原本就是 0"与"从没碰过"区分开，否则关闭 RDP 时会替用户写一个他没设过的值。
	added := []struct{ table, name, ddl string }{
		{"device", "probe_status", `ALTER TABLE device ADD COLUMN probe_status TEXT NOT NULL DEFAULT 'pending'`},
		{"device", "probe_message", `ALTER TABLE device ADD COLUMN probe_message TEXT NOT NULL DEFAULT ''`},
		{"device", "probe_checked_at", `ALTER TABLE device ADD COLUMN probe_checked_at TEXT NOT NULL DEFAULT ''`},
		{"device", "metrics_json", `ALTER TABLE device ADD COLUMN metrics_json TEXT NOT NULL DEFAULT ''`},
		{"device", "metrics_at", `ALTER TABLE device ADD COLUMN metrics_at TEXT NOT NULL DEFAULT ''`},
		{"device", "rdp_enabled", `ALTER TABLE device ADD COLUMN rdp_enabled INTEGER NOT NULL DEFAULT 0`},
		{"device", "rdp_blank_before", `ALTER TABLE device ADD COLUMN rdp_blank_before TEXT NOT NULL DEFAULT ''`},
		{"device", "rdp_blank_allowed", `ALTER TABLE device ADD COLUMN rdp_blank_allowed INTEGER NOT NULL DEFAULT 0`},
		{"device_command", "allow_blank_password", `ALTER TABLE device_command ADD COLUMN allow_blank_password INTEGER NOT NULL DEFAULT 0`},
	}
	for _, col := range added {
		exists, err := d.hasColumn(ctx, col.table, col.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := d.sql.ExecContext(ctx, col.ddl); err != nil {
			return fmt.Errorf("为 %s 表补列 %s 失败: %w", col.table, col.name, err)
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
