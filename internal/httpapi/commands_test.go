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

func (f *fakeCommandStore) AckCommand(_ context.Context, deviceID, commandID int64, ok bool, message string) (store.Command, error) {
	if f.ackErr != nil {
		return store.Command{}, f.ackErr
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
		return f.items[i], nil
	}
	return store.Command{}, store.ErrCommandNotFound
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
	if len(resp.Features) != 2 || resp.Features[0] != "commands" || resp.Features[1] != "metrics" {
		t.Fatalf("features = %v, want [commands metrics]", resp.Features)
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

// RDP 开关指令不带倒计时：写入 0，前端也不必给用户选"几秒后执行"。
func TestCreateRDPCommand(t *testing.T) {
	h, commands, _ := newCommandTestServer(t)

	rec := postCommand(h, "/api/devices/1/commands", `{"type":"enable_rdp"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201，body = %s", rec.Code, rec.Body.String())
	}

	var resp commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Type != store.CommandEnableRDP {
		t.Fatalf("type = %q", resp.Type)
	}
	if resp.DelaySeconds != 0 {
		t.Fatalf("delay_seconds = %d，RDP 指令应为 0", resp.DelaySeconds)
	}
	if commands.items[0].DelaySeconds != 0 {
		t.Fatalf("落库 delay = %d", commands.items[0].DelaySeconds)
	}

	// 同一设备同时只允许一条未完成指令，这里顺带锁住这条规则。
	if rec := postCommand(h, "/api/devices/1/commands", `{"type":"disable_rdp"}`); rec.Code != http.StatusConflict {
		t.Fatalf("第二条指令 status = %d，want 409，body = %s", rec.Code, rec.Body.String())
	}

	// disable_rdp 换一台干净的服务器同样受理。
	h2, commands2, _ := newCommandTestServer(t)
	if rec := postCommand(h2, "/api/devices/1/commands", `{"type":"disable_rdp"}`); rec.Code != http.StatusCreated {
		t.Fatalf("disable_rdp status = %d，body = %s", rec.Code, rec.Body.String())
	}
	if commands2.items[0].Type != store.CommandDisableRDP {
		t.Fatalf("落库 type = %q", commands2.items[0].Type)
	}
}

// 客户端回执自带的 rdp_enabled 是设备 RDP 状态的唯一来源。
func TestHeartbeatAckUpdatesRDPState(t *testing.T) {
	h, _, status, commands := newHeartbeatServerWithCommands(t)

	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandEnableRDP,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	// 第一次心跳把 pending 取走（→ delivered），回执只接受 delivered。
	if rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("取指令心跳 status = %d", rec.Code)
	}

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":1,"ok":true,"message":"RDP 已启用","rdp_enabled":true}]}`
	if rec := postHeartbeat(h, body); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d，body = %s", rec.Code, rec.Body.String())
	}
	if got, ok := status.rdp[1]; !ok || !got {
		t.Fatalf("rdp_enabled 未写入：%v", status.rdp)
	}
}

// 失败的回执、非 RDP 指令的回执都不改动 RDP 状态：
// 宁可保持原状态，也不要因为一条关机回执把 RDP 标成已启用。
func TestHeartbeatAckDoesNotGuessRDPState(t *testing.T) {
	h, _, status, commands := newHeartbeatServerWithCommands(t)

	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandShutdown, DelaySeconds: 60,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("取指令心跳 status = %d", rec.Code)
	}

	// 关机回执即便带了 rdp_enabled 也必须忽略。
	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":1,"ok":true,"message":"ok","rdp_enabled":true}]}`
	if rec := postHeartbeat(h, body); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d", rec.Code)
	}
	if len(status.rdp) != 0 {
		t.Fatalf("非 RDP 指令不应更新 RDP 状态：%v", status.rdp)
	}
}

// 空密码放行是显式授权：只有 enable_rdp 能带这个开关，其它指令带上必须报错，
// 不能静默忽略——用户会以为自己授权生效了，而这个开关是安全降级。
func TestCreateRDPCommandBlankPassword(t *testing.T) {
	h, commands, _ := newCommandTestServer(t)

	rec := postCommand(h, "/api/devices/1/commands",
		`{"type":"enable_rdp","allow_blank_password":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201，body = %s", rec.Code, rec.Body.String())
	}

	var resp commandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !resp.AllowBlankPassword {
		t.Fatalf("响应未回显 allow_blank_password：%+v", resp)
	}
	if !commands.items[0].AllowBlankPassword {
		t.Fatalf("落库未记录授权：%+v", commands.items[0])
	}

	// 默认不授权：省略即 false。
	h2, commands2, _ := newCommandTestServer(t)
	if rec := postCommand(h2, "/api/devices/1/commands", `{"type":"enable_rdp"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d，body = %s", rec.Code, rec.Body.String())
	}
	if commands2.items[0].AllowBlankPassword {
		t.Fatalf("未授权却落库为 true：%+v", commands2.items[0])
	}

	// 用在关闭指令上必须拒绝。
	h3, _, _ := newCommandTestServer(t)
	rec = postCommand(h3, "/api/devices/1/commands",
		`{"type":"disable_rdp","allow_blank_password":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("disable_rdp + 授权 status = %d，want 400，body = %s", rec.Code, rec.Body.String())
	}
}

// 授权开关要随心跳下发到客户端，未授权时这个键不出现（老客户端也不受影响）。
func TestHeartbeatDeliversBlankPasswordFlag(t *testing.T) {
	h, _, status, commands := newHeartbeatServerWithCommands(t)
	status.items = append(status.items, store.ProbeStatus{DeviceID: 1, ProbeType: "heartbeat"})

	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandEnableRDP, AllowBlankPassword: true,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}

	rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var resp heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析心跳响应失败: %v", err)
	}
	if len(resp.Commands) != 1 || !resp.Commands[0].AllowBlankPassword {
		t.Fatalf("授权未随指令下发：%+v", resp.Commands)
	}
	if !strings.Contains(rec.Body.String(), `"allow_blank_password":true`) {
		t.Fatalf("授权未编码进报文：%s", rec.Body.String())
	}

	// 未授权时省略该键：用原始 JSON 断言，避免结构体零值掩盖问题。
	hUn, _, statusUn, commandsUn := newHeartbeatServerWithCommands(t)
	statusUn.items = append(statusUn.items, store.ProbeStatus{DeviceID: 1, ProbeType: "heartbeat"})
	if _, err := commandsUn.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandEnableRDP,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	recUn := postHeartbeat(hUn, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	if strings.Contains(recUn.Body.String(), "allow_blank_password") {
		t.Fatalf("未授权不应下发 allow_blank_password：%s", recUn.Body.String())
	}

	// 关闭指令要带原值，客户端才能改回原样。
	h2, _, status2, commands2 := newHeartbeatServerWithCommands(t)
	status2.items = append(status2.items, store.ProbeStatus{
		DeviceID: 1, ProbeType: "heartbeat", RDPBlankAllowed: true, RDPBlankBefore: "1",
	})
	if _, err := commands2.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandDisableRDP,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	rec = postHeartbeat(h2, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	var resp2 heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("解析心跳响应失败: %v", err)
	}
	if len(resp2.Commands) != 1 || resp2.Commands[0].BlankPolicyBefore != "1" {
		t.Fatalf("关闭指令未携带策略原值：%+v", resp2.Commands)
	}
}

// 回执里的放行状态与原值要写进设备状态；恢复成功后清空原值。
func TestHeartbeatAckUpdatesBlankPolicy(t *testing.T) {
	h, _, status, commands := newHeartbeatServerWithCommands(t)
	status.items = append(status.items, store.ProbeStatus{DeviceID: 1, ProbeType: "heartbeat"})

	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandEnableRDP, AllowBlankPassword: true,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("取指令心跳 status = %d", rec.Code)
	}

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":1,"ok":true,"message":"已放开","rdp_enabled":true,` +
		`"rdp_blank_allowed":true,"rdp_blank_before":"1"}]}`
	if rec := postHeartbeat(h, body); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d，body = %s", rec.Code, rec.Body.String())
	}
	if !status.blankAllowed[1] || status.blankBefore[1] != "1" {
		t.Fatalf("放行状态未写入：allowed=%v before=%q", status.blankAllowed, status.blankBefore)
	}

	// 关闭 RDP 的回执：allowed=false 必须同时清空原值，否则下次会拿旧快照覆盖用户的新设置。
	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandDisableRDP,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("取指令心跳 status = %d", rec.Code)
	}
	body = `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":2,"ok":true,"message":"已恢复","rdp_enabled":false,"rdp_blank_allowed":false}]}`
	if rec := postHeartbeat(h, body); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d，body = %s", rec.Code, rec.Body.String())
	}
	if status.blankAllowed[1] || status.blankBefore[1] != "" {
		t.Fatalf("恢复后未清空：allowed=%v before=%q", status.blankAllowed, status.blankBefore)
	}
}

// 非法原值不许落库：这个字符串最终会被回写给目标机注册表。
func TestHeartbeatAckRejectsInvalidBlankBefore(t *testing.T) {
	h, _, status, commands := newHeartbeatServerWithCommands(t)
	status.items = append(status.items, store.ProbeStatus{DeviceID: 1, ProbeType: "heartbeat"})

	if _, err := commands.CreateCommand(context.Background(), store.Command{
		DeviceID: 1, Type: store.CommandEnableRDP, AllowBlankPassword: true,
	}); err != nil {
		t.Fatalf("CreateCommand: %v", err)
	}
	if rec := postHeartbeat(h, `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("取指令心跳 status = %d", rec.Code)
	}

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret +
		`","acks":[{"id":1,"ok":true,"message":"x","rdp_blank_allowed":true,"rdp_blank_before":"1; rm -rf /"}]}`
	if rec := postHeartbeat(h, body); rec.Code != http.StatusOK {
		t.Fatalf("回执心跳 status = %d", rec.Code)
	}
	if status.blankBefore[1] != "" {
		t.Fatalf("非法原值被落库：%q", status.blankBefore[1])
	}
}
