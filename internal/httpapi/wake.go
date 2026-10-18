// 本文件实现 F2 的触发接口：按设备 ID 取出配置，发一次魔术包。
// 只报告「包已发出」，不判断设备是否上线（属 E3）。
package httpapi

import (
	"net/http"
	"time"

	"wol/internal/wol"
)

// Waker 抽象发包能力，由 wol.Service 实现，测试时可替换为假实现。
type Waker interface {
	Wake(mac, broadcastAddr string, port int) (wol.Result, error)
}

// wakeResponse 是 docs/E2-contract.md 冻结的响应形状，只允许这两个键。
type wakeResponse struct {
	Sent   bool   `json:"sent"`
	SentAt string `json:"sent_at"`
}

func (s *Server) handleWakeDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	dv, err := s.devices.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, "查询待唤醒设备", err)
		return
	}

	res, err := s.waker.Wake(dv.MAC, dv.BroadcastAddr, dv.BroadcastPort)
	if err != nil {
		// 发送失败通常是本机网络不可用或广播地址在当前网段不可路由，
		// 回显给用户的说明保持可操作，细节只进日志。
		s.logger.Error("发送魔术包失败",
			"id", id, "mac", dv.MAC, "addr", dv.BroadcastAddr, "port", dv.BroadcastPort, "err", err)
		writeError(w, http.StatusInternalServerError, "send_failed",
			"魔术包发送失败，请检查本机网络与广播地址是否可达")
		return
	}

	writeJSON(w, http.StatusAccepted, wakeResponse{
		Sent:   true,
		SentAt: res.SentAt.Format(time.RFC3339),
	})
}
