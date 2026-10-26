package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wol/internal/store"
)

// 心跳密钥示例：只含字母、数字与连字符，长度 20。
const testHeartbeatSecret = "hb-secret-0123456789"

// newHeartbeatServer 构造一个**不带令牌**的路由：
// 心跳接口本就公开，这样能顺带证明它不需要登录态。
func newHeartbeatServer(t *testing.T) (http.Handler, *fakeDeviceStore, *fakeStatusStore) {
	t.Helper()

	fs := newFakeDeviceStore()
	fs.items = []store.Device{
		{
			ID:            1,
			Name:          "书房主机",
			MAC:           "AA:BB:CC:DD:EE:FF",
			BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9,
			ProbeType:     "heartbeat",
			ProbeValue:    testHeartbeatSecret,
		},
		{
			ID:            2,
			Name:          "Ping 设备",
			MAC:           "AA:BB:CC:DD:EE:02",
			BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9,
			ProbeType:     "ping",
			ProbeValue:    "192.168.1.10",
		},
	}
	fs.nextID = 3

	status := newFakeStatusStore()
	h := New(fakePinger{}, fs, &fakeWaker{}, &fakeChecker{}, status, testAuthn, newTestLogger(), "../../web").Routes()
	return h, fs, status
}

func postHeartbeat(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/heartbeat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestHeartbeatAccepted(t *testing.T) {
	h, _, status := newHeartbeatServer(t)

	// MAC 用小写连字符写法，验证服务端会自行归一化。
	rec := postHeartbeat(h, `{"mac":"aa-bb-cc-dd-ee-ff","secret":"`+testHeartbeatSecret+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", rec.Code, rec.Body)
	}
	assertKeys(t, rec.Body.Bytes(), []string{"interval_seconds", "status", "timeout_seconds"})

	var body heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.Status != store.StatusOnline {
		t.Fatalf("status = %q, want %q", body.Status, store.StatusOnline)
	}
	if body.IntervalSeconds != 60 || body.TimeoutSeconds != 180 {
		t.Fatalf("间隔/超时 = %d/%d, want 60/180", body.IntervalSeconds, body.TimeoutSeconds)
	}
	if len(status.onlineIDs) != 1 || status.onlineIDs[0] != 1 {
		t.Fatalf("应把设备 1 标记为已上线，实际 %v", status.onlineIDs)
	}
}

func TestHeartbeatRejectsWrongSecret(t *testing.T) {
	h, _, status := newHeartbeatServer(t)

	rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"wrong-secret-0123456"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "heartbeat_rejected")
	if len(status.onlineIDs) != 0 {
		t.Fatalf("密钥错误时不得写入在线状态，实际 %v", status.onlineIDs)
	}
}

func TestHeartbeatRejectsUnknownMACAndNonHeartbeat(t *testing.T) {
	h, _, status := newHeartbeatServer(t)

	// 未知 MAC 与非心跳设备必须给出同一个错误码，避免被用来枚举设备。
	for _, body := range []string{
		`{"mac":"11:22:33:44:55:66","secret":"` + testHeartbeatSecret + `"}`,
		`{"mac":"AA:BB:CC:DD:EE:02","secret":"` + testHeartbeatSecret + `"}`,
	} {
		rec := postHeartbeat(h, body)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("body=%s 状态码 = %d, want 401", body, rec.Code)
		}
		assertErrorCode(t, rec.Body.Bytes(), "heartbeat_rejected")
	}
	if len(status.onlineIDs) != 0 {
		t.Fatalf("拒绝请求时不得写入在线状态，实际 %v", status.onlineIDs)
	}
}

func TestHeartbeatRejectsMalformedRequest(t *testing.T) {
	h, _, _ := newHeartbeatServer(t)

	cases := []struct {
		body string
		code string
	}{
		{`{"mac":"AA:BB:CC:DD:EE:FF"}`, "invalid_heartbeat"}, // 缺 secret
		{`{"mac":"not-a-mac","secret":"` + testHeartbeatSecret + `"}`, "invalid_heartbeat"},
		{`{"mac":"AA:BB:CC:DD:EE:FF","secret":"x","extra":1}`, "invalid_json"}, // 未知字段
		{`not json`, "invalid_json"},
	}
	for _, c := range cases {
		rec := postHeartbeat(h, c.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s 状态码 = %d, want 400", c.body, rec.Code)
		}
		assertErrorCode(t, rec.Body.Bytes(), c.code)
	}
}

func TestHeartbeatWriteFailure(t *testing.T) {
	h, _, status := newHeartbeatServer(t)
	status.markErr = errBoom

	rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal")
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("响应泄露了内部错误: %s", rec.Body)
	}
}

// 心跳设备不接受主动探测：/check 必须给出可操作的说明，而不是跑满 120 秒。
func TestStartCheckRejectsHeartbeatMode(t *testing.T) {
	h, _, _ := newHeartbeatServer(t)

	rec := httptest.NewRecorder()
	withTestToken(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/1/check", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "heartbeat_mode")
}

// 状态接口对心跳设备按「距上次心跳多久」推算：从未上报=pending，超时=timeout。
func TestListStatusDerivesHeartbeatState(t *testing.T) {
	h, _, status := newHeartbeatServer(t)
	now := time.Now()
	status.items = []store.ProbeStatus{
		{DeviceID: 1, Status: store.StatusPending, ProbeType: "heartbeat"},
		{DeviceID: 2, Status: store.StatusOnline, ProbeType: "heartbeat", CheckedAt: now.Add(-10 * time.Second)},
		{DeviceID: 3, Status: store.StatusOnline, ProbeType: "heartbeat", CheckedAt: now.Add(-5 * time.Minute)},
	}

	rec := httptest.NewRecorder()
	withTestToken(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", rec.Code, rec.Body)
	}

	var list []probeStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是数组: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("条目数 = %d, want 3", len(list))
	}

	if list[0].Status != store.StatusPending || !strings.Contains(list[0].Message, "尚未收到心跳") {
		t.Fatalf("从未上报应为 pending 并提示启动客户端，实际 %+v", list[0])
	}
	if list[1].Status != store.StatusOnline || list[1].Message != "" {
		t.Fatalf("10 秒前的心跳应判在线，实际 %+v", list[1])
	}
	if list[2].Status != store.StatusTimeout || !strings.Contains(list[2].Message, "心跳超时") {
		t.Fatalf("5 分钟前的心跳应判超时，实际 %+v", list[2])
	}
}

// 设备写入路径必须接受 heartbeat 类型，并拒绝过短的密钥。
func TestCreateHeartbeatDevice(t *testing.T) {
	h, _, _ := newHeartbeatServer(t)

	bad := `{"name":"新机","mac":"AA:BB:CC:DD:EE:88","broadcast_addr":"192.168.1.255","probe_type":"heartbeat","probe_value":"short"}`
	rec := httptest.NewRecorder()
	withTestToken(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("过短密钥状态码 = %d, want 400, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_probe")

	good := `{"name":"新机","mac":"AA:BB:CC:DD:EE:88","broadcast_addr":"192.168.1.255","probe_type":"heartbeat","probe_value":"` + testHeartbeatSecret + `"}`
	rec = httptest.NewRecorder()
	withTestToken(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices", strings.NewReader(good)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("合法心跳设备状态码 = %d, want 201, body=%s", rec.Code, rec.Body)
	}
}
