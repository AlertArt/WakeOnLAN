package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wol/internal/probe"
	"wol/internal/store"
)

// assertErrorCode 校验错误响应的码值，并确认只有 error/message 两个键。
func assertErrorCode(t *testing.T, raw []byte, want string) {
	t.Helper()
	var e apiError
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("响应不是合法错误对象: %v (%s)", err, raw)
	}
	if e.Error != want {
		t.Fatalf("error = %q, want %q", e.Error, want)
	}
	if e.Message == "" {
		t.Fatalf("错误响应缺少 message: %s", raw)
	}
}

// fakeChecker 记录触发参数，供断言「用的是库里的校验配置」。
type fakeChecker struct {
	calls      int
	id         int64
	probeType  string
	probeValue string
	err        error
}

func (f *fakeChecker) Start(id int64, probeType, probeValue string) error {
	f.calls++
	f.id, f.probeType, f.probeValue = id, probeType, probeValue
	return f.err
}

// fakeStatusStore 提供状态查询。
type fakeStatusStore struct {
	items []store.ProbeStatus
	err   error
}

func (f *fakeStatusStore) ListProbeStatus(context.Context) ([]store.ProbeStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func newFakeStatusStore() *fakeStatusStore { return &fakeStatusStore{items: []store.ProbeStatus{}} }

// 触发校验与状态查询的字段白名单，按字母序。
var (
	checkContractKeys  = []string{"started_at", "status"}
	statusContractKeys = []string{"checked_at", "device_id", "message", "status"}
)

type checkFixture struct {
	handler http.Handler
	devices *fakeDeviceStore
	checker *fakeChecker
	status  *fakeStatusStore
}

func newCheckTestServer(t *testing.T) checkFixture {
	t.Helper()
	fs := newFakeDeviceStore()
	fs.items = append(fs.items,
		store.Device{
			ID:            1,
			Name:          "书房主机",
			MAC:           "AA:BB:CC:DD:EE:FF",
			BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9,
			ProbeType:     "tcp",
			ProbeValue:    "192.168.1.10:22",
		},
		store.Device{
			ID:            2,
			Name:          "未配置校验",
			MAC:           "AA:BB:CC:DD:EE:02",
			BroadcastAddr: "192.168.1.255",
			BroadcastPort: 9,
			ProbeType:     "none",
			ProbeValue:    "",
		},
	)
	fs.nextID = 3

	checker := &fakeChecker{}
	status := newFakeStatusStore()
	h := withTestToken(New(fakePinger{}, fs, &fakeWaker{}, checker, status, testAuthn, newTestLogger(), "../../web").Routes())
	return checkFixture{handler: h, devices: fs, checker: checker, status: status}
}

func TestStartCheckAccepted(t *testing.T) {
	f := newCheckTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/devices/1/check", nil)
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("状态码 = %d, want 202, body=%s", rec.Code, rec.Body)
	}
	assertKeys(t, rec.Body.Bytes(), checkContractKeys)

	var body checkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.Status != store.StatusChecking {
		t.Fatalf("status = %q, want %q", body.Status, store.StatusChecking)
	}
	if _, err := time.Parse(time.RFC3339, body.StartedAt); err != nil {
		t.Fatalf("started_at 不是 RFC3339: %v", err)
	}

	// 校验目标必须来自设备记录，不由客户端提供。
	if f.checker.calls != 1 {
		t.Fatalf("校验启动次数 = %d, want 1", f.checker.calls)
	}
	if f.checker.id != 1 || f.checker.probeType != "tcp" || f.checker.probeValue != "192.168.1.10:22" {
		t.Fatalf("传入探测参数不符: id=%d type=%q value=%q",
			f.checker.id, f.checker.probeType, f.checker.probeValue)
	}
}

func TestStartCheckRejectsUnconfiguredProbe(t *testing.T) {
	f := newCheckTestServer(t)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/2/check", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_probe")
	if f.checker.calls != 0 {
		t.Fatalf("未配置校验时不应启动任务，实际启动 %d 次", f.checker.calls)
	}
}

func TestStartCheckRejectsInvalidProbeValue(t *testing.T) {
	f := newCheckTestServer(t)
	// 直接把非法目标写进假存储，模拟早期数据或手工改库。
	f.devices.items[0].ProbeValue = "not-a-target"

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/1/check", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_probe")
	if f.checker.calls != 0 {
		t.Fatalf("目标非法时不应启动任务，实际启动 %d 次", f.checker.calls)
	}
}

func TestStartCheckConflictWhenRunning(t *testing.T) {
	f := newCheckTestServer(t)
	f.checker.err = probe.ErrInProgress

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/1/check", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("状态码 = %d, want 409, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "check_in_progress")
}

func TestStartCheckNotFound(t *testing.T) {
	f := newCheckTestServer(t)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/999/check", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "not_found")
}

func TestStartCheckRejectsBadID(t *testing.T) {
	f := newCheckTestServer(t)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/abc/check", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_id")
}

func TestStartCheckStatusWriteFailure(t *testing.T) {
	f := newCheckTestServer(t)
	f.checker.err = errors.New("数据库写入失败")

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/devices/1/check", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "status_write_failed")
	// 内部错误细节不得回显。
	if strings.Contains(rec.Body.String(), "数据库写入失败") {
		t.Fatalf("响应泄露了内部错误: %s", rec.Body)
	}
}

func TestListStatusContract(t *testing.T) {
	f := newCheckTestServer(t)
	checked := time.Date(2026, 10, 18, 16, 30, 5, 0, time.UTC)
	f.status.items = []store.ProbeStatus{
		{DeviceID: 1, Status: store.StatusOnline, Message: "", CheckedAt: checked},
		{DeviceID: 2, Status: store.StatusPending, Message: "", CheckedAt: time.Time{}},
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", rec.Code, rec.Body)
	}

	var list []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是数组: %v (%s)", err, rec.Body)
	}
	if len(list) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(list))
	}
	for _, item := range list {
		assertKeys(t, item, statusContractKeys)
	}

	var first probeStatusResponse
	_ = json.Unmarshal(list[0], &first)
	if first.DeviceID != 1 || first.Status != store.StatusOnline || first.Message != "" {
		t.Fatalf("首项不符: %+v", first)
	}
	if first.CheckedAt != checked.Format(time.RFC3339) {
		t.Fatalf("checked_at = %q", first.CheckedAt)
	}

	var second probeStatusResponse
	_ = json.Unmarshal(list[1], &second)
	if second.CheckedAt != "" {
		t.Fatalf("从未校验的设备 checked_at 应为空串，实际 %q", second.CheckedAt)
	}
}

func TestListStatusEmptyIsArray(t *testing.T) {
	f := newCheckTestServer(t)

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("空列表应返回 []，实际 %q", strings.TrimSpace(rec.Body.String()))
	}
}

func TestListStatusStoreFailure(t *testing.T) {
	f := newCheckTestServer(t)
	f.status.err = errors.New("查询失败")

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "internal")
}

func TestCheckRejectsWrongMethod(t *testing.T) {
	f := newCheckTestServer(t)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest(method, "/api/devices/1/check", nil))
		if rec.Code == http.StatusAccepted {
			t.Fatalf("%s 不应被接受", method)
		}
	}
}
