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
	"fmt"
	"net/http"
	"strings"
	"time"

	"wol/internal/probe"
	"wol/internal/store"
)

// heartbeatRequest 是心跳上报的请求体，只接受这两个键。
type heartbeatRequest struct {
	MAC    string `json:"mac"`
	Secret string `json:"secret"`
}

// heartbeatResponse 是心跳回执。interval_seconds 让客户端不必自己写死间隔。
type heartbeatResponse struct {
	Status          string `json:"status"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

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

	writeJSON(w, http.StatusOK, heartbeatResponse{
		Status:          store.StatusOnline,
		IntervalSeconds: int(probe.HeartbeatInterval.Seconds()),
		TimeoutSeconds:  int(probe.HeartbeatTimeout.Seconds()),
	})
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
