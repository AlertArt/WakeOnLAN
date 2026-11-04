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
// ProbeType 供调用方区分「主动探测」与「心跳上报」：后者的结论由最近心跳时间推算。
// MetricsJSON 是 F11 心跳附带指标的原始 JSON（可能为空），MetricsAt 为其上报时间。
// RDPEnabled 是目标机 RDP 是否可用的结论：只由客户端 enable_rdp/disable_rdp 的回执改写，
// 服务端不猜（M2 契约）。
//
// RDPBlankAllowed 表示这台机的「空密码账户禁止网络登录」策略当前是否**由我们**放开了。
// RDPBlankBefore 是放开之前的原值：” 表示没动过，'0'/'1' 是原值，'absent' 表示原本没有这个键。
// 两者一起构成可逆性：客户端重装、换机后仍能按原值恢复，不会一律写回 1。
type ProbeStatus struct {
	DeviceID        int64
	Status          string
	Message         string
	ProbeType       string
	CheckedAt       time.Time
	MetricsJSON     string
	MetricsAt       time.Time
	RDPEnabled      bool
	RDPBlankAllowed bool
	RDPBlankBefore  string
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
		`SELECT id, probe_status, probe_message, probe_type, probe_checked_at,
		        metrics_json, metrics_at, rdp_enabled, rdp_blank_allowed, rdp_blank_before
		   FROM device ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("查询校验状态失败: %w", err)
	}
	defer rows.Close()

	list := make([]ProbeStatus, 0, 16)
	for rows.Next() {
		var (
			ps       ProbeStatus
			checked  string
			metrics  string
			metAt    string
			rdp      int
			blankOn  int
			blankRaw string
		)
		if err := rows.Scan(&ps.DeviceID, &ps.Status, &ps.Message, &ps.ProbeType, &checked,
			&metrics, &metAt, &rdp, &blankOn, &blankRaw); err != nil {
			return nil, fmt.Errorf("读取校验状态失败: %w", err)
		}
		ps.RDPEnabled = rdp != 0
		ps.RDPBlankAllowed = blankOn != 0
		ps.RDPBlankBefore = blankRaw
		if checked != "" {
			t, err := parseTime(checked)
			if err != nil {
				return nil, err
			}
			ps.CheckedAt = t
		}
		ps.MetricsJSON = metrics
		if metAt != "" {
			t, err := parseTime(metAt)
			if err != nil {
				return nil, fmt.Errorf("读取指标时间失败: %w", err)
			}
			ps.MetricsAt = t
		}
		list = append(list, ps)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历校验状态失败: %w", err)
	}
	return list, nil
}

// SetRDPEnabled 记录目标机 RDP 是否可用。
// 只由客户端 enable_rdp/disable_rdp 的回执调用：服务端既没有能力也没有理由去猜。
func (d *DB) SetRDPEnabled(ctx context.Context, deviceID int64, enabled bool) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE device SET rdp_enabled = ? WHERE id = ?`, boolToInt(enabled), deviceID)
	if err != nil {
		return fmt.Errorf("写入 RDP 状态失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRDPBlankPolicy 记录「空密码账户禁止网络登录」的放行状态与原始策略值。
//
// 只由客户端 enable_rdp/disable_rdp 的回执调用，语义与 SetRDPEnabled 一致：
// 服务端没有能力读到目标机的策略，也就不去猜。
// allowed=false 时 before 必须传空串：关闭即视为"我们不再持有这条策略"，
// 下一次启用重新读当时的原值，避免拿一份过期快照去覆盖用户的后续修改。
func (d *DB) SetRDPBlankPolicy(ctx context.Context, deviceID int64, allowed bool, before string) error {
	if !allowed {
		before = ""
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE device SET rdp_blank_allowed = ?, rdp_blank_before = ? WHERE id = ?`,
		boolToInt(allowed), before, deviceID)
	if err != nil {
		return fmt.Errorf("写入空密码策略状态失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
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
