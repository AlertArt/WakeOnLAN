package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"wol/internal/guac"
	"wol/internal/store"
)

const testGuacKey = "4c0b569e4c96df157eee1b65dd0e4d41"

// newRDPTestServer 构造带登录态的路由，并装配远程桌面能力。
// 设备 1 是"在线 + 已启用 RDP + 有 IP"的理想目标，其余用例按需改字段。
func newRDPTestServer(t *testing.T) (http.Handler, *fakeStatusStore) {
	t.Helper()
	h, status, _ := newRDPTestServerWith(t, true)
	return h, status
}

func newRDPTestServerWith(t *testing.T, enableRDP bool) (http.Handler, *fakeStatusStore, *Server) {
	t.Helper()

	fs := newFakeDeviceStore()
	fs.items = []store.Device{
		{ID: 1, Name: "书房主机", MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9, ProbeType: "heartbeat", ProbeValue: testHeartbeatSecret},
		{ID: 2, Name: "Ping 设备", MAC: "AA:BB:CC:DD:EE:02", BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9, ProbeType: "ping", ProbeValue: "192.168.1.10"},
	}
	fs.nextID = 3

	metrics, _ := json.Marshal(store.Metrics{CPU: 12, MemUsed: 1024, MemTotal: 8192, IP: "192.168.1.10"})

	status := newFakeStatusStore()
	status.items = []store.ProbeStatus{
		{
			DeviceID: 1, Status: store.StatusOnline, ProbeType: "heartbeat",
			CheckedAt: time.Now(), RDPEnabled: true,
			MetricsJSON: string(metrics), MetricsAt: time.Now(),
		},
		{DeviceID: 2, Status: store.StatusOnline, ProbeType: "ping", CheckedAt: time.Now()},
	}

	srv := New(fakePinger{}, fs, &fakeWaker{}, &fakeChecker{}, status, newFakeCommandStore(),
		testAuthn, newTestLogger(), "../../web")
	if enableRDP {
		cfg, err := guac.ParseConfig("https://nas.example.com/guacamole", testGuacKey, 0)
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		srv.EnableRDP(cfg, 3389)
	}
	return withTestToken(srv.Routes()), status, srv
}

func postSession(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	h.ServeHTTP(rec, req)
	return rec
}

func getTicket(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var e apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("解析错误响应失败: %v，body = %s", err, rec.Body.String())
	}
	return e
}

// 正常路径：200 + 相对 URL + 60 秒。
func TestRDPSessionAccepted(t *testing.T) {
	h, _ := newRDPTestServer(t)

	rec := postSession(h, "/api/devices/1/rdp-session")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200，body = %s", rec.Code, rec.Body.String())
	}

	var resp rdpSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !strings.HasPrefix(resp.URL, "/rdp/") || len(resp.URL) != len("/rdp/")+32 {
		t.Fatalf("url = %q，期望 /rdp/<32 位十六进制>", resp.URL)
	}
	if resp.ExpiresIn != 60 {
		t.Fatalf("expires_in = %d，want 60", resp.ExpiresIn)
	}
	if resp.ExpiresAt == "" {
		t.Fatal("expires_at 为空")
	}
}

// 拒绝 1：未配置 Guacamole → 503，而不是 500。
func TestRDPSessionNotConfigured(t *testing.T) {
	h, _, _ := newRDPTestServerWith(t, false)

	rec := postSession(h, "/api/devices/1/rdp-session")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if e := decodeErr(t, rec); e.Error != "guac_not_configured" {
		t.Fatalf("error = %q", e.Error)
	}
}

// 拒绝 2：非心跳设备没有下发通道，也没有可信的 IP。
func TestRDPSessionNotHeartbeat(t *testing.T) {
	h, _ := newRDPTestServer(t)

	rec := postSession(h, "/api/devices/2/rdp-session")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400，body = %s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Error != "not_heartbeat" {
		t.Fatalf("error = %q", e.Error)
	}
}

// 拒绝 3：设备离线。
func TestRDPSessionOffline(t *testing.T) {
	h, status := newRDPTestServer(t)
	status.items[0].CheckedAt = time.Now().Add(-10 * time.Minute)
	status.items[0].Status = store.StatusTimeout

	rec := postSession(h, "/api/devices/1/rdp-session")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409，body = %s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Error != "device_offline" {
		t.Fatalf("error = %q", e.Error)
	}
}

// 拒绝 4：RDP 未启用（服务端只认客户端回执，不猜）。
func TestRDPSessionRDPDisabled(t *testing.T) {
	h, status := newRDPTestServer(t)
	status.items[0].RDPEnabled = false

	rec := postSession(h, "/api/devices/1/rdp-session")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409，body = %s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Error != "rdp_disabled" {
		t.Fatalf("error = %q", e.Error)
	}
}

// 拒绝 5：没有 IP 就无法填 hostname，宁可明确报错也不要连到空地址。
func TestRDPSessionWithoutIP(t *testing.T) {
	h, status := newRDPTestServer(t)
	status.items[0].MetricsJSON = `{"cpu":5,"mem_used":100,"mem_total":200}`

	rec := postSession(h, "/api/devices/1/rdp-session")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409，body = %s", rec.Code, rec.Body.String())
	}
	if e := decodeErr(t, rec); e.Error != "no_ip" {
		t.Fatalf("error = %q", e.Error)
	}
}

// 票据兑换：302 到 Guacamole，参数在 fragment 里。
func TestRDPTicketRedirects(t *testing.T) {
	h, _ := newRDPTestServer(t)

	var resp rdpSessionResponse
	rec := postSession(h, "/api/devices/1/rdp-session")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	rec = getTicket(h, resp.URL)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302，body = %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	// 必须是直连路由：auth-json 认证完不会自动连线，落点带 client 路径才会立即发起会话。
	if !strings.HasPrefix(loc, "https://nas.example.com/guacamole/#/client/") {
		t.Fatalf("Location = %q", loc)
	}
	// 载荷必须已经 URL 转义：base64 的 + / = 在查询串里有特殊含义。
	if strings.Contains(loc, "+") || strings.Contains(loc, "?data=ey") && strings.Contains(loc, "==") {
		t.Fatalf("Location 中的载荷未转义：%q", loc)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q，应禁止缓存", cc)
	}
}

// 连接名必须是纯 ASCII：Guacamole 前端用 btoa 编码连接标识，非 Latin1 字符
// （如中文设备名）会让整页抛 InvalidCharacterError，主页「全部连接」渲染不出来。
func TestRDPPayloadConnectionNameASCII(t *testing.T) {
	h, _ := newRDPTestServer(t)

	var resp rdpSessionResponse
	rec := postSession(h, "/api/devices/1/rdp-session")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	rec = getTicket(h, resp.URL)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}

	// Location 形如 …/#/client/<名字>?data=<载荷>；数据在 fragment 里，手动拆出来。
	loc := rec.Header().Get("Location")
	i := strings.Index(loc, "?data=")
	if i < 0 {
		t.Fatalf("Location 缺少 data 参数：%q", loc)
	}
	data, err := url.QueryUnescape(loc[i+len("?data="):])
	if err != nil {
		t.Fatalf("载荷转义非法: %v", err)
	}

	cfg, err := guac.ParseConfig("https://nas.example.com/guacamole", testGuacKey, 0)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	plain, err := cfg.Decode(data)
	if err != nil {
		t.Fatalf("载荷解不开: %v", err)
	}

	var payload struct {
		Username    string `json:"username"`
		Connections map[string]struct {
			Protocol string `json:"protocol"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("载荷不是合法 JSON: %v，明文 %q", err, plain)
	}
	if payload.Username != "wol-1" {
		t.Errorf("username = %q，应为 wol-1（设备 1 的稳定标识）", payload.Username)
	}
	for name, conn := range payload.Connections {
		if !isASCII(name) {
			t.Errorf("连接名 %q 含非 ASCII 字符，会让 Guacamole 前端 btoa 崩溃", name)
		}
		if name != "wol-1" {
			t.Errorf("连接名 = %q，应为 wol-1（不取设备名「书房主机」）", name)
		}
		if conn.Protocol != "rdp" {
			t.Errorf("protocol = %q，应为 rdp", conn.Protocol)
		}
	}
	if len(payload.Connections) != 1 {
		t.Errorf("连接数 = %d，应为 1", len(payload.Connections))
	}
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// 同一张票据只能用一次：第二次 410。
func TestRDPTicketSingleUse(t *testing.T) {
	h, _ := newRDPTestServer(t)

	var resp rdpSessionResponse
	rec := postSession(h, "/api/devices/1/rdp-session")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if got := getTicket(h, resp.URL); got.Code != http.StatusFound {
		t.Fatalf("首次兑换 status = %d", got.Code)
	}
	second := getTicket(h, resp.URL)
	if second.Code != http.StatusGone {
		t.Fatalf("二次兑换 status = %d，want 410", second.Code)
	}
	if !strings.Contains(second.Body.String(), "已失效") {
		t.Fatalf("410 页面缺少说明文字：%s", second.Body.String())
	}
}

// 伪造/过期票据一律 410，且不泄露任何会话参数。
func TestRDPTicketUnknown(t *testing.T) {
	h, _ := newRDPTestServer(t)

	rec := getTicket(h, "/rdp/0123456789abcdef0123456789abcdef")
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d，want 410", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "guacamole") {
		t.Fatal("410 页面不应包含 Guacamole 地址")
	}
}

// 兑换票据不需要登录态：票据本身就是凭据。
func TestRDPTicketWithoutToken(t *testing.T) {
	fs := newFakeDeviceStore()
	status := newFakeStatusStore()
	srv := New(fakePinger{}, fs, &fakeWaker{}, &fakeChecker{}, status, newFakeCommandStore(),
		testAuthn, newTestLogger(), "../../web")
	cfg, err := guac.ParseConfig("https://nas.example.com/guacamole", testGuacKey, 0)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	srv.EnableRDP(cfg, 3389)

	ticket, _, err := srv.rdpTickets.Issue("payload", "测试机")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rec := getTicket(srv.Routes(), "/rdp/"+ticket)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d，want 302（不需要登录）", rec.Code)
	}
}

// 访问日志不能把票据原样写进去。
func TestLogPathMasked(t *testing.T) {
	if got := logPath("/rdp/abc123"); got != "/rdp/<ticket>" {
		t.Fatalf("logPath = %q", got)
	}
	if got := logPath("/api/devices/1/rdp-session"); got != "/api/devices/1/rdp-session" {
		t.Fatalf("普通路径被误改：%q", got)
	}
	if got := logPath("/rdp/"); got != "/rdp/<ticket>" {
		t.Fatalf("logPath = %q", got)
	}
}
