// 本文件实现 F3 的两个接口：触发校验（异步）与查询状态。
// 校验本身跑在 probe.Runner 的进程内 goroutine 里，handler 只负责校验请求与写回执。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wol/internal/probe"
	"wol/internal/store"
)

// Checker 抽象校验任务的启动能力，由 probe.Runner 实现。
type Checker interface {
	Start(deviceID int64, probeType, probeValue string) error
}

// StatusStore 抽象状态查询与写入能力，由 store.DB 实现。
// MarkOnline 同时供心跳上报复用：收到心跳即视为"已上线"，最近结论时间=现在。
// SaveMetrics 供 F11 心跳附带指标落库。
type StatusStore interface {
	ListProbeStatus(ctx context.Context) ([]store.ProbeStatus, error)
	MarkOnline(ctx context.Context, deviceID int64) error
	SaveMetrics(ctx context.Context, deviceID int64, m *store.Metrics, at time.Time) error
	SetRDPEnabled(ctx context.Context, deviceID int64, enabled bool) error
}

// checkResponse 是触发接口的回执，只允许这两个键（见 docs/E3-contract.md）。
type checkResponse struct {
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

// probeStatusResponse 是状态查询的单项形状。
// metrics/metrics_at 是 F11 追加的可选键：无指标的设备（老客户端）不出现这两个键。
type probeStatusResponse struct {
	DeviceID  int64           `json:"device_id"`
	Status    string          `json:"status"`
	Message   string          `json:"message"`
	CheckedAt string          `json:"checked_at"`
	Metrics   json.RawMessage `json:"metrics,omitempty"`
	MetricsAt string          `json:"metrics_at,omitempty"`
	// RDPEnabled 是 M2 追加的键：目标机是否已启用远程桌面。
	// 只由客户端 enable_rdp/disable_rdp 的回执改写，恒存在（老设备为 false）。
	RDPEnabled bool `json:"rdp_enabled"`
}

func (s *Server) handleStartCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	dv, err := s.devices.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, "查询待校验设备", err)
		return
	}

	// 心跳设备不接受主动探测：它的上线结论只能由客户端上报产生。
	if dv.ProbeType == probeHeartbeat {
		writeError(w, http.StatusBadRequest, "heartbeat_mode",
			"该设备使用心跳校验，无法主动探测；唤醒后请等待客户端上报，最长 3 分钟")
		return
	}

	// 配置不可执行时直接拒绝，不启动任务：让它跑满 120 秒再报失败是浪费。
	if dv.ProbeType == "none" || dv.ProbeValue == "" {
		writeError(w, http.StatusBadRequest, "invalid_probe", "该设备未配置校验目标，请先设置校验方式与目标")
		return
	}
	if err := probe.ValidateTarget(dv.ProbeType, dv.ProbeValue); err != nil {
		s.logger.Warn("校验目标非法", "id", id, "type", dv.ProbeType, "value", dv.ProbeValue, "err", err)
		writeError(w, http.StatusBadRequest, "invalid_probe", "校验目标非法，请检查设备的校验设置")
		return
	}

	startedAt := time.Now()
	if err := s.checker.Start(id, dv.ProbeType, dv.ProbeValue); err != nil {
		switch {
		case errors.Is(err, probe.ErrInProgress):
			writeError(w, http.StatusConflict, "check_in_progress", "该设备正在校验中，请等待本次结果")
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "设备不存在")
		default:
			s.logger.Error("启动校验失败", "id", id, "err", err)
			writeError(w, http.StatusInternalServerError, "status_write_failed", "写入校验状态失败")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, checkResponse{
		Status:    store.StatusChecking,
		StartedAt: startedAt.Format(time.RFC3339),
	})
}

func (s *Server) handleListStatus(w http.ResponseWriter, r *http.Request) {
	list, err := s.statuses.ListProbeStatus(r.Context())
	if err != nil {
		s.logger.Error("查询校验状态失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}

	out := make([]probeStatusResponse, 0, len(list))
	now := time.Now()
	for _, item := range list {
		resp := probeStatusResponse{
			DeviceID:   item.DeviceID,
			Status:     item.Status,
			Message:    item.Message,
			RDPEnabled: item.RDPEnabled,
		}
		if !item.CheckedAt.IsZero() {
			resp.CheckedAt = item.CheckedAt.Format(time.RFC3339)
		}
		if item.MetricsJSON != "" && json.Valid([]byte(item.MetricsJSON)) {
			resp.Metrics = json.RawMessage(item.MetricsJSON)
			resp.MetricsAt = item.MetricsAt.Format(time.RFC3339)
		}
		// 心跳设备的状态由「最近一次心跳距今多久」推算，落库值只反映最后一次上报。
		// 这样无需后台清扫任务：读的时候晚一秒就知道它已经离线了。
		if item.ProbeType == probeHeartbeat {
			resp.Status, resp.Message = heartbeatStatus(item.CheckedAt, now)
		}
		out = append(out, resp)
	}

	writeJSON(w, http.StatusOK, out)
}
