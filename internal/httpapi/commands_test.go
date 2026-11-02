package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wol/internal/store"
)

// fakeCommandStore 是内存版指令队列，行为与 store.DB 的语义保持一致：
// 同一设备只允许一条未完成指令，取走即 delivered，回执只能一次。
type fakeCommandStore struct {
	items     []store.Command
	nextID    int64
	createErr error
	ackErr    error
	listErr   error
	// taken 记录被下发过的指令 ID，供心跳用例断言。
	taken []int64
}

func newFakeCommandStore() *fakeCommandStore {
	return &fakeCommandStore{items: []store.Command{}, nextID: 1}
}

func (f *fakeCommandStore) CreateCommand(_ context.Context, cmd store.Command) (store.Command, error) {
	if f.createErr != nil {
		return store.Command{}, f.createErr
	}
	for _, item := range f.items {
		if item.DeviceID == cmd.DeviceID &&
			(item.Status == store.CommandStatusPending || item.Status == store.CommandStatusDelivered) {
			return store.Command{}, store.ErrCommandPending
		}
	}
	now := time.Now()
	cmd.ID = f.nextID
	f.nextID++
	cmd.Status = store.CommandStatusPending
	cmd.CreatedAt = now
	cmd.ExpiresAt = now.Add(store.CommandTTL)
	f.items = append(f.items, cmd)
	return cmd, nil
}

func (f *fakeCommandStore) TakePendingCommands(_ context.Context, deviceID int64, now time.Time) ([]store.Command, error) {
	out := make([]store.Command, 0, 1)
	for i := range f.items {
		if f.items[i].DeviceID != deviceID || f.items[i].Status != store.CommandStatusPending {
			continue
		}
		f.items[i].Status = store.CommandStatusDelivered
		f.items[i].DeliveredAt = now
		f.taken = append(f.taken, f.items[i].ID)
		out = append(out, f.items[i])
	}
	return out, nil
}

func (f *fakeCommandStore) AckCommand(_ context.Context, deviceID, commandID int64, ok bool, message string) error {
	if f.ackErr != nil {
		return f.ackErr
	}
	for i := range f.items {
		if f.items[i].ID != commandID || f.items[i].DeviceID != deviceID ||
			f.items[i].Status != store.CommandStatusDelivered {
			continue
		}
		if ok {
			f.items[i].Status = store.CommandStatusDone
		} else {
			f.items[i].Status = store.CommandStatusFailed
		}
		f.items[i].Message = message
		f.items[i].FinishedAt = time.Now()
		return nil
	}
	return store.ErrCommandNotFound
}

func (f *fakeCommandStore) ListCommands(_ context.Context, deviceID int64, limit int) ([]store.Command, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.Command, 0, limit)
	for i := len(f.items) - 1; i >= 0 && len(out) < limit; i-- {
		if f.items[i].DeviceID == deviceID {
			out = append(out, f.items[i])
		}
	}
	return out, nil
}

func (f *fakeCommandStore) ListAllCommands(_ context.Context, limit int) ([]store.Command, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.Command, 0, limit)
	for i := len(f.items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, f.items[i])
	}
	return out, nil
}

func (f *fakeCommandStore) HasOpenCommand(_ context.Context, deviceID int64) (bool, error) {
	for _, item := range f.items {
		if item.DeviceID == deviceID &&
			(item.Status == store.CommandStatusPending || item.Status == store.CommandStatusDelivered) {
			return true, nil
		}
	}
	return false, nil
}

// newCommandTestServer 构造带登录态的路由，并预置一台在线、一台离线的心跳设备。
func newCommandTestServer(t *testing.T) (http.Handler, *fakeCommandStore, *fakeStatusStore) {
	t.Helper()

	fs := newFakeDeviceStore()
	fs.items = []store.Device{
		{ID: 1, Name: "书房主机", MAC: "AA:BB:CC:DD:EE:FF", BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9, ProbeType: "heartbeat", ProbeValue: testHeartbeatSecret},
		{ID: 2, Name: "Ping 设备", MAC: "AA:BB:CC:DD:EE:02", BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9, ProbeType: "ping", ProbeValue: "192.168.1.10"},
	}
	fs.nextID = 3

	status := newFakeStatusStore()
	status.items = []store.ProbeStatus{
		{DeviceID: 1, Status: store.StatusOnline, ProbeType: "heartbeat", CheckedAt: time.Now()},
		{DeviceID: 2, Status: store.StatusOnline, ProbeType: "ping", CheckedAt: time.Now()},
	}

	commands := newFakeCommandStore()
	h := withTestToken(New(fakePinger{}, fs, &fakeWaker{}, &fakeChecker{}, status, commands, testAuthn,
		newTestLogger(), "../../web").Routes())
	return h, commands, status
}

func postCommand(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// 正常下发：201 + pending + 默认延时 60。
func TestCreateCommandAccepted(t *testing.T) {
	h, commands, _ := newCommandTestServer(t)

	rec := postCommand(h, "/api/devices/1/commands", `{"type":"shutdown"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}

	var resp commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Type != store.CommandShutdown || resp.Status != store.CommandStatusPending {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.DelaySeconds != 60 {
		t.Fatalf("delay = %d, want 60", resp.DelaySeconds)
	}
	if len(commands.items) != 1 {
		t.Fatalf("写入指令数 = %d, want 1", len(commands.items))
	}
}

// 非心跳设备没有下发通道，必须显式拒绝。
func TestCreateCommandRejectsNonHeartbeat(t *testing.T) {
	h, _, _ := newCommandTestServer(t)

	rec := postCommand(h, "/api/devices/2/commands", `{"type":"shutdown"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not_heartbeat") {
		t.Fatalf("body = %s, want not_heartbeat", rec.Body.String())
	}
}

// 离线设备不排队，避免"幽灵关机"。
func TestCreateCommandRejectsOfflineDevice(t *testing.T) {
	h, _, status := newCommandTestServer(t)
	// 把心跳时间推到阈值之外：设备已离线。
	status.items[0].CheckedAt = time.Now().Add(-10 * time.Minute)

	rec := postCommand(h, "/api/devices/1/commands", `{"type":"restart"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "device_offline") {
		t.Fatalf("body = %s, want device_offline", rec.Body.String())
	}
}

// 同设备第二条指令被拒。
func TestCreateCommandRejectsSecond(t *testing.T) {
	h, _, _ := newCommandTestServer(t)

	if rec := postCommand(h, "/api/devices/1/commands", `{"type":"shutdown"}`); rec.Code != http.StatusCreated {
		t.Fatalf("第一条 status = %d", rec.Code)
	}
	rec := postCommand(h, "/api/devices/1/commands", `{"type":"restart"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "command_pending") {
		t.Fatalf("status = %d body = %s, want 409 command_pending", rec.Code, rec.Body.String())
	}
}

// 参数校验：类型、延时、未知字段。
func TestCreateCommandValidation(t *testing.T) {
	h, _, _ := newCommandTestServer(t)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"未知类型", `{"type":"format"}`, http.StatusBadRequest},
		{"延时过小", `{"type":"shutdown","delay_seconds":5}`, http.StatusBadRequest},
		{"延时过大", `{"type":"shutdown","delay_seconds":601}`, http.StatusBadRequest},
		{"多余字段", `{"type":"shutdown","oops":1}`, http.StatusBadRequest},
		{"非法 JSON", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postCommand(h, "/api/devices/1/commands", tc.body)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

// 设备不存在 → 404；未登录 → 401。
func TestCreateCommandNotFoundAndAuth(t *testing.T) {
	h, _, _ := newCommandTestServer(t)
	if rec := postCommand(h, "/api/devices/99/commands", `{"type":"shutdown"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	// 不带令牌的路由，验证鉴权真的生效。
	plain := New(fakePinger{}, newFakeDeviceStore(), &fakeWaker{}, &fakeChecker{},
		newFakeStatusStore(), newFakeCommandStore(), testAuthn, newTestLogger(), "../../web").Routes()
	rec := postCommand(plain, "/api/devices/1/commands", `{"type":"shutdown"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// 心跳捎带指令并声明能力；回执把 delivered 推到 done。
func TestHeartbeatCarriesCommandsAndHandlesAcks(t *testing.T) {
	h, _, _, commands := newHeartbeatServerWithCommands(t)

	// 先直接塞一条待下发指令，模拟已经下发过。
	_, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandShutdown, DelaySeconds: 60,
	})
	if err != nil {
		t.Fatalf("CreateCommand() error = %v", err)
	}

	rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Features) != 1 || resp.Features[0] != "commands" {
		t.Fatalf("features = %v, want [commands]", resp.Features)
	}
	if len(resp.Commands) != 1 || resp.Commands[0].Type != store.CommandShutdown {
		t.Fatalf("commands = %+v, want 一条 shutdown", resp.Commands)
	}

	// 同一指令不会二次下发。
	rec = postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	var again heartbeatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if len(again.Commands) != 0 {
		t.Fatalf("第二次心跳仍下发 %d 条", len(again.Commands))
	}

	// 回执后状态变 done。
	ackBody := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":1,"ok":true,"message":"已执行 shutdown /s /t 60"}]}`
	if rec := postHeartbeat(h, ackBody); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if commands.items[0].Status != store.CommandStatusDone {
		t.Fatalf("status = %q, want done", commands.items[0].Status)
	}
}

// 未知指令 ID 的回执不能拖垮心跳。
func TestHeartbeatIgnoresUnknownAck(t *testing.T) {
	h, _, _ := newHeartbeatServer(t)

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":999,"ok":true,"message":"x"}]}`
	rec := postHeartbeat(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 列表接口：带设备名、limit 生效。
func TestListCommands(t *testing.T) {
	h, commands, _ := newCommandTestServer(t)
	_, _ = commands.CreateCommand(context.Background(), store.Command{DeviceID: 1, Type: store.CommandShutdown, DelaySeconds: 60})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/commands?limit=5", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp []commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp) != 1 || resp[0].DeviceID != 1 {
		t.Fatalf("resp = %+v", resp)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices/1/commands", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("设备指令列表 status = %d", rec.Code)
	}
}
