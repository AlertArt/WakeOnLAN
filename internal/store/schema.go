// 本文件承载 device 表的建表语句与初始化。
// 建表语句为 E1 冻结项：字段名与顺序即 docs/E1-contract.md 的持久化映射，
// 后续环节如需加列，必须走迁移函数并同步更新契约文档。
package store

import (
	"context"
	"fmt"
)

const schemaDevice = `
CREATE TABLE IF NOT EXISTS device (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT    NOT NULL,
	mac            TEXT    NOT NULL,
	broadcast_addr TEXT    NOT NULL,
	broadcast_port INTEGER NOT NULL DEFAULT 9,
	probe_type     TEXT    NOT NULL DEFAULT 'none',
	probe_value    TEXT    NOT NULL DEFAULT '',
	created_at     TEXT    NOT NULL,
	updated_at     TEXT    NOT NULL
);

-- MAC 归一化后唯一，防止同一网卡被重复添加。
CREATE UNIQUE INDEX IF NOT EXISTS idx_device_mac ON device (mac);
`

// Migrate 创建业务表，幂等，可重复调用。
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, schemaDevice); err != nil {
		return fmt.Errorf("初始化 device 表失败: %w", err)
	}
	return nil
}
