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
	"net/http"
	"strings"
	"time"

	"wol/internal/probe"
	"wol/internal/store"
)

// heartbeatRequest 是心跳上报的请求体。
// acks 是 F10 追加的可选字段：客户端执行完关机/重启后在这里回执。
// 旧客户端不带它，旧服务端会因未知字段拒绝——所以客户端只在见过 features 后才发。
type heartbeatRequest struct {
	MAC    string         `json:"mac"`
	Secret string         `json:"secret"`
	Acks   []heartbeatAck `json:"acks,omitempty"`
}

// heartbeatAck 是一条指令的执行结果。
type heartbeatAck struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
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
}

// heartbeatFeatures 声明本服务端支持的能力。
var heartbeatFeatures = []string{"commands"}

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
		resp.Commands = make([]heartbeatCommand, 0, len(pending))
		for _, cmd := range pending {
			resp.Commands = append(resp.Commands, heartbeatCommand{
				ID:           cmd.ID,
				Type:         cmd.Type,
				DelaySeconds: cmd.DelaySeconds,
				Force:        cmd.Force,
				ExpiresAt:    cmd.ExpiresAt.UTC().Format(time.RFC3339),
			})
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
		err := s.commands.AckCommand(ctx, deviceID, ack.ID, ack.OK, ack.Message)
		switch {
		case err == nil:
			s.logger.Info("指令执行回执", "device_id", deviceID, "command_id", ack.ID, "ok", ack.OK)
		case errors.Is(err, store.ErrCommandNotFound):
			s.logger.Warn("忽略未知指令回执", "device_id", deviceID, "command_id", ack.ID)
		default:
			s.logger.Error("写入指令回执失败", "device_id", deviceID, "command_id", ack.ID, "err", err)
		}
	}
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
