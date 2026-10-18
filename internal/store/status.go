// 本文件实现 E3 的校验状态存储：只保留最近一次结果，不保存历史。
// 状态字符串是契约的一部分，取值见 docs/E3-contract.md，禁止在此新增取值。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 五个状态取值，与 docs/E3-contract.md 一一对应。
const (
	StatusPending  = "pending"
	StatusChecking = "checking"
	StatusOnline   = "online"
	StatusTimeout  = "timeout"
	StatusFailed   = "failed"
)

// 状态说明的固定文案。探测命令的原始输出不允许进入这里。
const (
	msgInterrupted = "服务重启，上次校验中断"
)

// ProbeStatus 是一台设备的状态快照。
// CheckedAt 为零值表示从未产生过终态结论。
type ProbeStatus struct {
	DeviceID  int64
	Status    string
	Message   string
	CheckedAt time.Time
}

// MarkChecking 标记校验开始。不刷新 checked_at：
// 那是「最近一次结论」的时间，校验中不应覆盖上一次的结果时间。
func (d *DB) MarkChecking(ctx context.Context, deviceID int64) error {
	return d.setProbeStatus(ctx, deviceID, StatusChecking, "", time.Time{})
}

// MarkOnline 标记已上线，message 恒为空串。
func (d *DB) MarkOnline(ctx context.Context, deviceID int64) error {
	return d.setProbeStatus(ctx, deviceID, StatusOnline, "", time.Now())
}

// MarkTimeout 标记超时未响应。
func (d *DB) MarkTimeout(ctx context.Context, deviceID int64, message string) error {
	return d.setProbeStatus(ctx, deviceID, StatusTimeout, message, time.Now())
}

// MarkFailed 标记校验失败（探测手段不可用或目标配置非法）。
func (d *DB) MarkFailed(ctx context.Context, deviceID int64, message string) error {
	return d.setProbeStatus(ctx, deviceID, StatusFailed, message, time.Now())
}

func (d *DB) setProbeStatus(ctx context.Context, deviceID int64, status, message string, checkedAt time.Time) error {
	var (
		res sql.Result
		err error
	)
	if checkedAt.IsZero() {
		res, err = d.sql.ExecContext(ctx,
			`UPDATE device SET probe_status = ?, probe_message = ? WHERE id = ?`,
			status, message, deviceID)
	} else {
		res, err = d.sql.ExecContext(ctx,
			`UPDATE device SET probe_status = ?, probe_message = ?, probe_checked_at = ? WHERE id = ?`,
			status, message, formatTime(checkedAt), deviceID)
	}
	if err != nil {
		return fmt.Errorf("写入校验状态失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListProbeStatus 返回全部设备的状态，按 id 升序。
// 无设备时返回空切片而非 nil，保证 JSON 序列化为 []。
func (d *DB) ListProbeStatus(ctx context.Context) ([]ProbeStatus, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, probe_status, probe_message, probe_checked_at FROM device ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询校验状态失败: %w", err)
	}
	defer rows.Close()

	list := make([]ProbeStatus, 0, 16)
	for rows.Next() {
		var (
			ps      ProbeStatus
			checked string
		)
		if err := rows.Scan(&ps.DeviceID, &ps.Status, &ps.Message, &checked); err != nil {
			return nil, fmt.Errorf("读取校验状态失败: %w", err)
		}
		if checked != "" {
			t, err := parseTime(checked)
			if err != nil {
				return nil, err
			}
			ps.CheckedAt = t
		}
		list = append(list, ps)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历校验状态失败: %w", err)
	}
	return list, nil
}

// MarkInterrupted 把遗留的 checking 重置为 pending，返回处理的行数。
// 供进程启动时调用：上次进程在轮询中途退出，状态会永久停在「校验中」，
// 这一步是 3-4「重启后状态不卡死」的实现基础。
func (d *DB) MarkInterrupted(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE device SET probe_status = ?, probe_message = ? WHERE probe_status = ?`,
		StatusPending, msgInterrupted, StatusChecking)
	if err != nil {
		return 0, fmt.Errorf("重置中断的校验状态失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取重置结果失败: %w", err)
	}
	return n, nil
}

// 保证 *DB 满足 probe.StatusWriter，编译期即可发现签名漂移。
var _ interface {
	MarkChecking(ctx context.Context, deviceID int64) error
	MarkOnline(ctx context.Context, deviceID int64) error
	MarkTimeout(ctx context.Context, deviceID int64, message string) error
	MarkFailed(ctx context.Context, deviceID int64, message string) error
} = (*DB)(nil)
