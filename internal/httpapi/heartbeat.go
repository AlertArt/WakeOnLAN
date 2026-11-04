// 本文件实现 F9 的心跳上报接口与状态推算。
//
// 设计要点：
//   - 心跳是**公开**接口（目标机上的客户端没有管理口令），靠每台设备独立的密钥鉴权；
//   - 密钥复用设备记录的 probe_value，不新增表、不新增列、不改设备资源形状；
//   - 收到心跳只做一件事：把 probe_status 置为 online、probe_checked_at 置为现在；
//   - 离线不靠后台任务判断，而是在 GET /api/status 时按「距上次心跳多久」推算。
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"wol/internal/probe"
	"wol/internal/store"
)

// heartbeatRequest 是心跳上报的请求体。
// acks 是 F10 追加的可选字段：客户端执行完关机/重启后在这里回执。
// metrics 是 F11 追加的可选字段：客户端随心跳携带的轻量指标。
// 旧客户端不带它们；客户端只在上报时见过对应 features 才会发，避免旧服务端 400。
type heartbeatRequest struct {
	MAC     string                     `json:"mac"`
	Secret  string                     `json:"secret"`
	Acks    []heartbeatAck             `json:"acks,omitempty"`
	Metrics map[string]json.RawMessage `json:"metrics,omitempty"`
}

// heartbeatAck 是一条指令的执行结果。
// rdp_enabled 是 M2 追加的可选字段：客户端执行完 enable_rdp/disable_rdp 后，
// 把「改完之后 RDP 到底是不是可用的」一并报上来，服务端据此更新设备状态。
// 老客户端不带这个键，指令类型也不是 RDP 时服务端不会碰它。
type heartbeatAck struct {
	ID         int64  `json:"id"`
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	RDPEnabled *bool  `json:"rdp_enabled,omitempty"`
	// RDPBlankAllowed / RDPBlankBefore 是「空密码网络登录」放行状态的回执：
	// 客户端改完策略后把"现在是否放开"和"改之前的原值"一起报上来。
	// 只带 allowed 不带 before 的回执按"没有原值可恢复"处理，宁可少恢复也不乱写。
	RDPBlankAllowed *bool   `json:"rdp_blank_allowed,omitempty"`
	RDPBlankBefore  *string `json:"rdp_blank_before,omitempty"`
}

// heartbeatResponse 是心跳回执。interval_seconds 让客户端不必自己写死间隔；
// features 用于能力协商，commands 是 F10 的待执行指令（可能为空）。
type heartbeatResponse struct {
	Status          string             `json:"status"`
	IntervalSeconds int                `json:"interval_seconds"`
	TimeoutSeconds  int                `json:"timeout_seconds"`
	Features        []string           `json:"features"`
	Commands        []heartbeatCommand `json:"commands,omitempty"`
}

// heartbeatCommand 是下发到客户端的最小指令描述，不含任何服务端内部字段。
type heartbeatCommand struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	DelaySeconds int    `json:"delay_seconds"`
	Force        bool   `json:"force"`
	ExpiresAt    string `json:"expires_at"`
	// AllowBlankPassword 只在用户勾选了安全降级时为 true；省略即为不放行。
	AllowBlankPassword bool `json:"allow_blank_password,omitempty"`
	// BlankPolicyBefore 只随 disable_rdp 下发：服务端保存的策略原值，
	// 让客户端能"改回原样"而不是一律写 1。'' 表示我们没动过，无需恢复。
	BlankPolicyBefore string `json:"blank_policy_before,omitempty"`
}

// heartbeatFeatures 声明本服务端支持的能力。
var heartbeatFeatures = []string{"commands", "metrics"}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体应为 {\"mac\":\"...\",\"secret\":\"...\"}")
		return
	}

	mac, ok := normalizeMAC(req.MAC)
	secret := strings.TrimSpace(req.Secret)
	if !ok || secret == "" {
		writeError(w, http.StatusBadRequest, "invalid_heartbeat", "mac 必须是合法 MAC，secret 不能为空")
		return
	}

	dv, found, err := s.findDeviceByMAC(r.Context(), mac)
	if err != nil {
		s.logger.Error("查询心跳设备失败", "mac", mac, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}

	// 三种失败合并成同一个响应：不区分「MAC 不存在」「不是心跳设备」「密钥错误」，
	// 避免同网段的人拿它枚举有哪些设备。
	reject := !found || dv.ProbeType != probeHeartbeat ||
		subtle.ConstantTimeCompare([]byte(dv.ProbeValue), []byte(secret)) != 1
	if reject {
		writeError(w, http.StatusUnauthorized, "heartbeat_rejected", "MAC 或心跳密钥不正确")
		return
	}

	if err := s.statuses.MarkOnline(r.Context(), dv.ID); err != nil {
		s.logger.Error("写入心跳状态失败", "id", dv.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}

	s.applyAcks(r.Context(), dv.ID, req.Acks)

	// 指标落库失败不影响心跳：上线状态与指令比一帧指标重要。
	if req.Metrics != nil {
		if m := parseMetrics(req.Metrics); m != nil {
			if err := s.statuses.SaveMetrics(r.Context(), dv.ID, m, time.Now()); err != nil {
				s.logger.Warn("写入设备指标失败", "id", dv.ID, "err", err)
			}
		}
	}

	// 取走待执行指令：置为 delivered，过期未取走的置为 expired。
	// 取指令失败不该影响心跳本身——设备上线状态比指令重要。
	pending, err := s.commands.TakePendingCommands(r.Context(), dv.ID, time.Now())
	if err != nil {
		s.logger.Error("读取待下发指令失败", "id", dv.ID, "err", err)
		pending = nil
	}

	resp := heartbeatResponse{
		Status:          store.StatusOnline,
		IntervalSeconds: int(probe.HeartbeatInterval.Seconds()),
		TimeoutSeconds:  int(probe.HeartbeatTimeout.Seconds()),
		Features:        heartbeatFeatures,
	}
	if len(pending) > 0 {
		// 关闭远程桌面时要把「空密码网络登录」恢复成原值，原值存在设备行上。
		// 只在真有 disable_rdp 时才查，正常心跳不付这份开销。
		blankBefore := ""
		for _, cmd := range pending {
			if cmd.Type == store.CommandDisableRDP {
				blankBefore = s.rdpBlankBefore(r.Context(), dv.ID)
				break
			}
		}

		resp.Commands = make([]heartbeatCommand, 0, len(pending))
		for _, cmd := range pending {
			item := heartbeatCommand{
				ID:           cmd.ID,
				Type:         cmd.Type,
				DelaySeconds: cmd.DelaySeconds,
				Force:        cmd.Force,
				ExpiresAt:    cmd.ExpiresAt.UTC().Format(time.RFC3339),
			}
			if cmd.Type == store.CommandEnableRDP {
				item.AllowBlankPassword = cmd.AllowBlankPassword
			}
			if cmd.Type == store.CommandDisableRDP {
				item.BlankPolicyBefore = blankBefore
			}
			resp.Commands = append(resp.Commands, item)
			s.logger.Info("指令已随心跳下发", "device_id", dv.ID, "command", cmd.Type, "command_id", cmd.ID)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// applyAcks 记录客户端回执。
// 重复回执（网络重试）与伪造 ID 都只记日志，不让整次心跳失败：
// 关节点的回执丢掉会导致设备状态卡住，比丢一条审计更糟。
func (s *Server) applyAcks(ctx context.Context, deviceID int64, acks []heartbeatAck) {
	for _, ack := range acks {
		if ack.ID <= 0 {
			continue
		}
		cmd, err := s.commands.AckCommand(ctx, deviceID, ack.ID, ack.OK, ack.Message)
		switch {
		case err == nil:
			s.logger.Info("指令执行回执", "device_id", deviceID, "command_id", ack.ID,
				"command", cmd.Type, "ok", ack.OK)
			s.applyRDPState(ctx, deviceID, cmd.Type, ack)
			s.applyRDPBlankPolicy(ctx, deviceID, cmd.Type, ack)
		case errors.Is(err, store.ErrCommandNotFound):
			s.logger.Warn("忽略未知指令回执", "device_id", deviceID, "command_id", ack.ID)
		default:
			s.logger.Error("写入指令回执失败", "device_id", deviceID, "command_id", ack.ID, "err", err)
		}
	}
}

// applyRDPState 用 RDP 类指令的回执更新 device.rdp_enabled。
//
// 只认"客户端明确带上了 rdp_enabled 且执行成功"的回执：
// 失败时可能是 reg 写不进去，此时保持原状态比猜测更安全；
// 非 RDP 指令的回执即便带了该字段也一律忽略，避免被旧数据或伪造包改状态。
func (s *Server) applyRDPState(ctx context.Context, deviceID int64, commandType string, ack heartbeatAck) {
	if commandType != store.CommandEnableRDP && commandType != store.CommandDisableRDP {
		return
	}
	if !ack.OK || ack.RDPEnabled == nil {
		return
	}
	if err := s.statuses.SetRDPEnabled(ctx, deviceID, *ack.RDPEnabled); err != nil {
		s.logger.Error("更新 RDP 状态失败", "device_id", deviceID, "err", err)
		return
	}
	s.logger.Info("RDP 状态已更新", "device_id", deviceID, "rdp_enabled", *ack.RDPEnabled)
}

// applyRDPBlankPolicy 记录「空密码账户禁止网络登录」的放行状态。
//
// 只在客户端明确回传 rdp_blank_allowed 时才动状态：
//   - 启用时客户端会带 allowed=true 与 before（原值快照）；
//   - 关闭时客户端恢复完原值带 allowed=false；
//   - 老客户端不带这两个键，保持原状态——若我们之前确实放开过，
//     状态就该继续显示"已放开"，因为目标机上也确实还开着。
func (s *Server) applyRDPBlankPolicy(ctx context.Context, deviceID int64, commandType string, ack heartbeatAck) {
	if commandType != store.CommandEnableRDP && commandType != store.CommandDisableRDP {
		return
	}
	if !ack.OK || ack.RDPBlankAllowed == nil {
		// 授权了却没回传结果：多半是目标机还跑着不认识这个键的旧客户端。
		// 不猜状态，但要留一条能查的线索，否则页面会一直显示"未放开"而没人知道为什么。
		if ack.OK && commandType == store.CommandEnableRDP {
			s.logger.Warn("客户端未回传空密码策略状态（可能是旧版本客户端）", "device_id", deviceID)
		}
		return
	}

	before := ""
	if *ack.RDPBlankAllowed && ack.RDPBlankBefore != nil {
		before = normalizeBlankBefore(*ack.RDPBlankBefore)
	}
	if err := s.statuses.SetRDPBlankPolicy(ctx, deviceID, *ack.RDPBlankAllowed, before); err != nil {
		s.logger.Error("更新空密码策略状态失败", "device_id", deviceID, "err", err)
		return
	}
	s.logger.Info("空密码策略状态已更新", "device_id", deviceID,
		"allowed", *ack.RDPBlankAllowed, "before", before)
}

// normalizeBlankBefore 只接受三种取值：”（没动过）、'0'、'1'、'absent'（原本无此键）。
// 其它内容一律当空处理：这份值最终会被回写给目标机，不能是客户端随口传的字符串。
func normalizeBlankBefore(raw string) string {
	switch raw {
	case "0", "1", "absent":
		return raw
	default:
		return ""
	}
}

// rdpBlankBefore 读取设备记录的空密码策略原值，供 disable_rdp 精确恢复。
func (s *Server) rdpBlankBefore(ctx context.Context, deviceID int64) string {
	list, err := s.statuses.ListProbeStatus(ctx)
	if err != nil {
		s.logger.Error("读取空密码策略原值失败", "device_id", deviceID, "err", err)
		return ""
	}
	for _, item := range list {
		if item.DeviceID == deviceID {
			return item.RDPBlankBefore
		}
	}
	return ""
}

// parseMetrics 把客户端发来的宽松 JSON 对象解析成结构化指标。
// 宽松策略：未知键忽略、类型不对的字段丢弃、数值钳制（Clamp）——
// 指标只是展示用途，任何坏字段都不值得让一次心跳失败。
// 返回 nil 表示没有可用字段，无需落库。
func parseMetrics(raw map[string]json.RawMessage) *store.Metrics {
	var m store.Metrics
	m.CPU = metricNum(raw, "cpu", 100)
	m.MemUsed = metricNum(raw, "mem_used", 1<<24)
	m.MemTotal = metricNum(raw, "mem_total", 1<<24)
	m.DiskUsed = metricNum(raw, "disk_used", 1e12)
	m.DiskTotal = metricNum(raw, "disk_total", 1e12)
	m.NetUp = metricNum(raw, "net_up", 1e12)
	m.NetDown = metricNum(raw, "net_down", 1e12)
	m.UptimeS = int64(metricNum(raw, "uptime_s", 1e12))
	m.IP = metricStr(raw, "ip")
	m.Hostname = metricStr(raw, "hostname")
	m.OS = metricStr(raw, "os")

	if !m.Clamp() {
		return nil
	}
	return &m
}

// metricNum 读取数值字段：类型不对或缺失返回 0，超上限截断。
func metricNum(raw map[string]json.RawMessage, key string, max float64) float64 {
	v, ok := raw[key]
	if !ok {
		return 0
	}
	var f float64
	if err := json.Unmarshal(v, &f); err != nil {
		return 0
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	if f > max {
		return max
	}
	return f
}

// metricStr 读取字符串字段：类型不对返回空串。
func metricStr(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

// findDeviceByMAC 在设备列表里按 MAC 查找。
// 设备上限 100 台，线性扫描的代价可以忽略，也就不必为此扩大 DeviceStore 接口。
func (s *Server) findDeviceByMAC(ctx context.Context, mac string) (store.Device, bool, error) {
	list, err := s.devices.ListDevices(ctx)
	if err != nil {
		return store.Device{}, false, err
	}
	for _, dv := range list {
		if dv.MAC == mac {
			return dv, true, nil
		}
	}
	return store.Device{}, false, nil
}

// heartbeatStatus 把「最近一次心跳时间」换算成状态与说明。
// 从未上报过是 pending（没有结论），超过阈值是 timeout（明确离线）。
func heartbeatStatus(checkedAt, now time.Time) (string, string) {
	if checkedAt.IsZero() {
		return store.StatusPending, "尚未收到心跳，请确认目标机上的客户端已启动"
	}
	if probe.HeartbeatStale(checkedAt, now) {
		return store.StatusTimeout, fmt.Sprintf("心跳超时：最近一次心跳在 %s前，请确认目标机上的客户端正在运行",
			humanAgo(now.Sub(checkedAt)))
	}
	return store.StatusOnline, ""
}

// humanAgo 输出中文相对时长，避免依赖容器时区（NAS 容器的 Local 通常是 UTC）。
func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
}
