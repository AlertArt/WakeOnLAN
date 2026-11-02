// 本文件实现 M2 的远程桌面入口：签发一次性票据 + 票据兑换跳转。
//
// 两段式设计的原因：
//   - 票据（wold 自己发的 32 位随机串）放在 wold 的地址上，用于实现「一次性」——
//     Guacamole 的 auth-json 载荷一旦发出去就只受过期时间约束，无法限制次数；
//   - 兑换时 wold 才把签好名的 Guacamole 载荷拼进跳转地址，载荷只在浏览器 fragment 里，
//     不进服务端访问日志（fragment 不会被发送）。
//
// 因此：票据 60 秒过期且只能兑换一次；兑换出来的 Guacamole 地址同样 60 秒过期。
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"wol/internal/guac"
	"wol/internal/store"
)

// rdpSessionTTL 是票据有效期，与 Guacamole 载荷里的 expires 保持一致。
const rdpSessionTTL = guac.DefaultTTL

// rdpSessionResponse 是 POST /api/devices/{id}/rdp-session 的响应。
// url 是相对路径：wold 不知道自己被反代到哪个域名，交给浏览器按当前源解析。
type rdpSessionResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Server) handleRDPSession(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	dv, err := s.devices.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, "查询远程桌面设备", err)
		return
	}

	if dv.ProbeType != probeHeartbeat {
		writeError(w, http.StatusBadRequest, "not_heartbeat",
			"该设备不是心跳校验方式，无法建立远程桌面；请先把校验方式改为「心跳上报」并在目标机运行客户端")
		return
	}

	online, err := s.deviceOnline(r.Context(), id)
	if err != nil {
		s.logger.Error("查询设备在线状态失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}
	if !online {
		writeError(w, http.StatusConflict, "device_offline",
			"设备当前不在线（最近没有心跳），无法建立远程桌面")
		return
	}

	ps, found, err := s.deviceStatus(r.Context(), id)
	if err != nil {
		s.logger.Error("查询设备状态失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "device_offline", "设备当前不在线（最近没有心跳）")
		return
	}

	// RDP 是否可用只认客户端回执，服务端不猜：没启用就明确告诉用户该点哪个按钮。
	if !ps.RDPEnabled {
		writeError(w, http.StatusConflict, "rdp_disabled",
			"目标机尚未启用远程桌面：请在卡片上点「启用远程桌面」，等回执成功后再试")
		return
	}

	if s.guacCfg.BaseURL == "" {
		writeError(w, http.StatusServiceUnavailable, "guac_not_configured",
			"未配置 Guacamole：请在部署环境变量里设置 WOL_GUAC_URL 与 WOL_GUAC_SECRET")
		return
	}

	m := parseMetricsJSON(ps.MetricsJSON)
	if m == nil || m.IP == "" {
		writeError(w, http.StatusConflict, "no_ip",
			"还没有收到目标机的网卡地址（心跳里没有 ip），请确认客户端版本 ≥ 2.0.0 且最近上报成功")
		return
	}

	expiresAt := time.Now().Add(rdpSessionTTL)
	data, err := s.guacCfg.Encode(guac.Session{
		Username:       fmt.Sprintf("wol-%d", dv.ID),
		ConnectionName: dv.Name,
		Host:           m.IP,
		Port:           s.rdpPort,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		s.logger.Error("生成远程桌面载荷失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "生成远程桌面会话失败")
		return
	}

	ticket, expires, err := s.rdpTickets.Issue(data)
	if err != nil {
		s.logger.Error("签发票据失败", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "生成远程桌面会话失败")
		return
	}

	s.logger.Info("已签发远程桌面票据", "device_id", id, "ip", m.IP)
	writeJSON(w, http.StatusOK, rdpSessionResponse{
		URL:       "/rdp/" + ticket,
		ExpiresIn: int(rdpSessionTTL.Seconds()),
		ExpiresAt: expires.UTC().Format(time.RFC3339),
	})
}

// handleRDPTicket 兑换票据并跳转到 Guacamole。
// 这个接口不需要登录：票据本身就是凭据（128 位随机、60 秒、一次性）。
func (s *Server) handleRDPTicket(w http.ResponseWriter, r *http.Request) {
	ticket := r.PathValue("ticket")
	t, ok := s.rdpTickets.Consume(ticket)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(expiredTicketPage))
		return
	}

	// 跳转目标带一次性载荷，任何缓存都可能让第二个请求复用它。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, s.guacCfg.ClientURL(t.Data), http.StatusFound)
}

// expiredTicketPage 是票据失效时的说明页。
// 不复用前端页面：这一步可能发生在任何设备上，越简单越可靠。
const expiredTicketPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>会话票据已失效</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
     font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#f5f6f8;color:#1f2328}
.box{max-width:420px;padding:28px 24px;background:#fff;border-radius:12px;
     box-shadow:0 2px 12px rgba(0,0,0,.08);text-align:center}
h1{font-size:18px;margin:0 0 12px}
p{margin:0 0 18px;line-height:1.6;color:#57606a;font-size:14px}
a{display:inline-block;padding:9px 18px;border-radius:8px;background:#1f6feb;color:#fff;
  text-decoration:none;font-size:14px}
</style></head>
<body><div class="box">
<h1>会话票据已失效</h1>
<p>远程桌面票据 60 秒内有效，且只能使用一次。<br>请回到 WakeOnLAN 页面重新点击「远程桌面」。</p>
<a href="/">返回设备列表</a>
</div></body></html>`

// deviceStatus 读取单台设备的状态快照。
func (s *Server) deviceStatus(ctx context.Context, deviceID int64) (store.ProbeStatus, bool, error) {
	list, err := s.statuses.ListProbeStatus(ctx)
	if err != nil {
		return store.ProbeStatus{}, false, err
	}
	for _, item := range list {
		if item.DeviceID == deviceID {
			return item, true, nil
		}
	}
	return store.ProbeStatus{}, false, nil
}

// parseMetricsJSON 解析落库的指标 JSON，坏数据按"没有指标"处理。
func parseMetricsJSON(raw string) *store.Metrics {
	if raw == "" {
		return nil
	}
	var m store.Metrics
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return &m
}
