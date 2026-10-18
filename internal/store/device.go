// 本文件实现 device 表的读写方法。
// 只负责持久化与语义错误映射，不做业务校验（校验在 httpapi 层完成），
// 也不感知 HTTP 概念（状态码、路径参数）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 语义错误。上层据此映射 HTTP 状态码，不解析错误字符串。
var (
	ErrNotFound      = errors.New("设备不存在")
	ErrDuplicateMAC  = errors.New("MAC 已存在")
	ErrLimitExceeded = errors.New("设备数已达上限")
)

// MaxDevices 一期不做分页，用总量上限替代。
const MaxDevices = 100

// Device 是设备的持久化模型。probe_* 字段由 E3 消费，E1 只负责存取。
type Device struct {
	ID            int64
	Name          string
	MAC           string
	BroadcastAddr string
	BroadcastPort int
	ProbeType     string
	ProbeValue    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const deviceColumns = `id, name, mac, broadcast_addr, broadcast_port, probe_type, probe_value, created_at, updated_at`

// ListDevices 按 id 升序返回全部设备。无数据时返回空切片而非 nil，
// 保证 JSON 序列化为 [] 而不是 null。
func (d *DB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+deviceColumns+` FROM device ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询设备列表失败: %w", err)
	}
	defer rows.Close()

	list := make([]Device, 0, 16)
	for rows.Next() {
		dv, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, dv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历设备列表失败: %w", err)
	}
	return list, nil
}

// GetDevice 按 id 查询单条，不存在时返回 ErrNotFound。
func (d *DB) GetDevice(ctx context.Context, id int64) (Device, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM device WHERE id = ?`, id)
	dv, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return dv, err
}

// CountDevices 返回当前设备总数，供上限判断。
func (d *DB) CountDevices(ctx context.Context) (int, error) {
	var n int
	if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM device`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计设备数失败: %w", err)
	}
	return n, nil
}

// CreateDevice 插入设备并回填 ID 与时间戳。
// 调用方保证 MAC 已归一化、字段已校验。
func (d *DB) CreateDevice(ctx context.Context, dv Device) (Device, error) {
	n, err := d.CountDevices(ctx)
	if err != nil {
		return Device{}, err
	}
	if n >= MaxDevices {
		return Device{}, ErrLimitExceeded
	}

	now := time.Now()
	dv.CreatedAt = now
	dv.UpdatedAt = now

	res, err := d.sql.ExecContext(ctx,
		`INSERT INTO device (name, mac, broadcast_addr, broadcast_port, probe_type, probe_value, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		dv.Name, dv.MAC, dv.BroadcastAddr, dv.BroadcastPort, dv.ProbeType, dv.ProbeValue,
		formatTime(dv.CreatedAt), formatTime(dv.UpdatedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Device{}, ErrDuplicateMAC
		}
		return Device{}, fmt.Errorf("新增设备失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return Device{}, fmt.Errorf("读取新增设备 ID 失败: %w", err)
	}
	dv.ID = id

	// 回读一次，保证返回值与落盘值逐字段一致（含时间戳精度），
	// 否则调用方拿到的纳秒精度时间与库里秒级时间不等。
	return d.getDeviceOrFail(ctx, id)
}

// UpdateDevice 全量覆盖指定设备，保留 created_at，刷新 updated_at。
func (d *DB) UpdateDevice(ctx context.Context, dv Device) (Device, error) {
	old, err := d.GetDevice(ctx, dv.ID)
	if err != nil {
		return Device{}, err
	}

	now := time.Now()
	res, err := d.sql.ExecContext(ctx,
		`UPDATE device
		    SET name = ?, mac = ?, broadcast_addr = ?, broadcast_port = ?,
		        probe_type = ?, probe_value = ?, updated_at = ?
		  WHERE id = ?`,
		dv.Name, dv.MAC, dv.BroadcastAddr, dv.BroadcastPort, dv.ProbeType, dv.ProbeValue,
		formatTime(now), dv.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Device{}, ErrDuplicateMAC
		}
		return Device{}, fmt.Errorf("修改设备失败: %w", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return Device{}, ErrNotFound
	}

	dv.CreatedAt = old.CreatedAt
	dv.UpdatedAt = now

	// 同样回读，避免返回值与库里秒级时间戳出现精度差异。
	return d.getDeviceOrFail(ctx, dv.ID)
}

// getDeviceOrFail 读取刚写入的记录。刚写入成功却读不到，属于存储异常，按普通错误上报。
func (d *DB) getDeviceOrFail(ctx context.Context, id int64) (Device, error) {
	dv, err := d.GetDevice(ctx, id)
	if err != nil {
		return Device{}, fmt.Errorf("回读设备 %d 失败: %w", id, err)
	}
	return dv, nil
}

// DeleteDevice 物理删除，不存在时返回 ErrNotFound。
func (d *DB) DeleteDevice(ctx context.Context, id int64) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM device WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除设备失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("读取删除结果失败: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// scanner 兼容 *sql.Row 与 *sql.Rows。
type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(s scanner) (Device, error) {
	var dv Device
	var created, updated string
	err := s.Scan(&dv.ID, &dv.Name, &dv.MAC, &dv.BroadcastAddr, &dv.BroadcastPort,
		&dv.ProbeType, &dv.ProbeValue, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, sql.ErrNoRows
		}
		return Device{}, fmt.Errorf("读取设备记录失败: %w", err)
	}

	if dv.CreatedAt, err = parseTime(created); err != nil {
		return Device{}, err
	}
	if dv.UpdatedAt, err = parseTime(updated); err != nil {
		return Device{}, err
	}
	return dv, nil
}

func formatTime(t time.Time) string { return t.Format(time.RFC3339) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("解析时间戳 %q 失败: %w", s, err)
	}
	return t, nil
}

// isUniqueViolation 判断是否为唯一约束冲突。
// modernc.org/sqlite 的错误信息含 "UNIQUE constraint failed"，
// 此处只依赖该稳定片段，不引入驱动的错误码常量。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
