// F11 心跳轻量指标：每台设备只保留最新一份快照，不做历史曲线。
// 存储为 device 表上的两个补充列（metrics_json / metrics_at），
// 与校验状态一样走 ensureColumns 幂等迁移，老数据文件升级无感。
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// Metrics 是客户端随心跳上报的轻量指标。
// 单位约定：mem_* 为 MB，disk_* 为 GB，net_* 为字节/秒，uptime_s 为秒。
// 任何字段缺失都允许（客户端单字段降级），零值表示"这次没采到"。
type Metrics struct {
	CPU       float64 `json:"cpu"`                // 0–100
	MemUsed   float64 `json:"mem_used"`           // MB
	MemTotal  float64 `json:"mem_total"`          // MB
	DiskUsed  float64 `json:"disk_used"`          // GB
	DiskTotal float64 `json:"disk_total"`         // GB
	NetUp     float64 `json:"net_up"`             // B/s
	NetDown   float64 `json:"net_down"`           // B/s
	IP        string  `json:"ip,omitempty"`       // 主用网卡 IPv4
	UptimeS   int64   `json:"uptime_s,omitempty"` // 开机时长
	Hostname  string  `json:"hostname,omitempty"` // ≤64 字符
	OS        string  `json:"os,omitempty"`       // ≤64 字符
}

// 字符串字段的截断上限：指标是给网页展示的，不是日志，超长只会污染版面。
const metricsStringLimit = 64

// metrics 数值上限：合理范围内钳制，防异常数据撑爆版面或被当攻击载荷。
const (
	metricsNumMax = 1e12
	metricsMemMax = 1 << 24 // 16 TB（MB 计）
)

// Clamp 原地修正非法值：NaN/Inf 归零、负数归零、超上限截断、字符串截断。
// 返回 false 表示整个快照没有一个可用字段，调用方可以直接丢弃。
func (m *Metrics) Clamp() bool {
	m.CPU = clampNum(m.CPU, 100)
	m.MemUsed = clampNum(m.MemUsed, metricsMemMax)
	m.MemTotal = clampNum(m.MemTotal, metricsMemMax)
	m.DiskUsed = clampNum(m.DiskUsed, metricsNumMax)
	m.DiskTotal = clampNum(m.DiskTotal, metricsNumMax)
	m.NetUp = clampNum(m.NetUp, metricsNumMax)
	m.NetDown = clampNum(m.NetDown, metricsNumMax)
	if m.MemUsed > m.MemTotal && m.MemTotal > 0 {
		m.MemUsed = m.MemTotal
	}
	if m.UptimeS < 0 || m.UptimeS > metricsNumMax {
		m.UptimeS = 0
	}
	m.IP = clampStr(m.IP)
	m.Hostname = clampStr(m.Hostname)
	m.OS = clampStr(m.OS)

	return m.CPU > 0 || m.MemTotal > 0 || m.DiskTotal > 0 ||
		m.NetUp > 0 || m.NetDown > 0 || m.IP != "" ||
		m.UptimeS > 0 || m.Hostname != "" || m.OS != ""
}

func clampNum(v, max float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	if v > max {
		return max
	}
	return v
}

func clampStr(s string) string {
	if len(s) > metricsStringLimit {
		r := []rune(s)
		if len(r) > metricsStringLimit {
			s = string(r[:metricsStringLimit])
		}
	}
	return s
}

// SaveMetrics 覆盖写入设备最新指标。m 为 nil 时清空该设备的指标。
// 指标写入失败由调用方决定是否影响心跳——本方法只负责落库。
func (d *DB) SaveMetrics(ctx context.Context, deviceID int64, m *Metrics, at time.Time) error {
	payload := ""
	if m != nil {
		b, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("序列化指标失败: %w", err)
		}
		payload = string(b)
	}

	res, err := d.sql.ExecContext(ctx,
		`UPDATE device SET metrics_json = ?, metrics_at = ? WHERE id = ?`,
		payload, formatTime(at), deviceID)
	if err != nil {
		return fmt.Errorf("写入设备指标失败: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// MetricsAt 由 ListProbeStatus 一并读出，见 status.go 的 ProbeStatus。
