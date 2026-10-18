package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"wol/internal/store"
	"wol/internal/wol"
)

// fakeWaker 记录被调用时的参数，供测试断言「发包用的是库里的配置」。
type fakeWaker struct {
	calls int
	mac   string
	addr  string
	port  int
	err   error
}

func (f *fakeWaker) Wake(mac, broadcastAddr string, port int) (wol.Result, error) {
	f.calls++
	f.mac, f.addr, f.port = mac, broadcastAddr, port
	if f.err != nil {
		return wol.Result{}, f.err
	}
	return wol.Result{SentAt: time.Date(2026, 10, 18, 10, 52, 31, 0, time.UTC)}, nil
}

// 唤醒响应的字段白名单，按字母序，用于逐键比对。
var wakeContractKeys = []string{"sent", "sent_at"}

func newWakeTestServer(t *testing.T, w *fakeWaker) (http.Handler, *fakeDeviceStore) {
	t.Helper()
	fs := newFakeDeviceStore()
	// 预置一台设备，广播地址与端口刻意与默认值不同，便于确认配置真的被读取。
	fs.items = append(fs.items, store.Device{
		ID:            1,
		Name:          "书房主机",
		MAC:           "AA:BB:CC:DD:EE:FF",
		BroadcastAddr: "192.168.1.255",
		BroadcastPort: 7,
		ProbeType:     "ping",
		ProbeValue:    "192.168.1.10",
	})
	fs.nextID = 2
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(fakePinger{}, fs, w, logger, "../../web").Routes(), fs
}

func assertKeys(t *testing.T, raw []byte, want []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v (%s)", err, raw)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("响应字段不符\n got: %v\nwant: %v", got, want)
	}
}

func TestWakeReturns202(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	rec := do(h, http.MethodPost, "/api/devices/1/wake", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	assertKeys(t, rec.Body.Bytes(), wakeContractKeys)

	var body wakeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !body.Sent {
		t.Fatal("sent 应为 true")
	}
	if body.SentAt == "" {
		t.Fatal("sent_at 为空")
	}
	if _, err := time.Parse(time.RFC3339, body.SentAt); err != nil {
		t.Fatalf("sent_at 不是 RFC3339: %q", body.SentAt)
	}
}

// 发包参数必须来自设备记录，而不是请求体。
func TestWakeUsesStoredDeviceConfig(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	// 请求体里塞入干扰配置，应当被完全忽略。
	body := `{"mac":"11:22:33:44:55:66","broadcast_addr":"10.0.0.255","broadcast_port":1234}`
	rec := do(h, http.MethodPost, "/api/devices/1/wake", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}

	if w.calls != 1 {
		t.Fatalf("发包调用次数 = %d, want 1", w.calls)
	}
	if w.mac != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("mac = %q, want 库内值 AA:BB:CC:DD:EE:FF", w.mac)
	}
	if w.addr != "192.168.1.255" || w.port != 7 {
		t.Fatalf("addr:port = %s:%d, want 192.168.1.255:7", w.addr, w.port)
	}
}

func TestWakeDeviceNotFound(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	rec := do(h, http.MethodPost, "/api/devices/999/wake", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "not_found" {
		t.Fatalf("error = %q, want not_found", e.Error)
	}
	if w.calls != 0 {
		t.Fatalf("设备不存在时不应发包，实际调用 %d 次", w.calls)
	}
}

func TestWakeInvalidID(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	for _, path := range []string{"/api/devices/0/wake", "/api/devices/abc/wake", "/api/devices/-2/wake"} {
		rec := do(h, http.MethodPost, path, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
		var e apiError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error != "invalid_id" {
			t.Fatalf("%s error = %q, want invalid_id", path, e.Error)
		}
	}
	if w.calls != 0 {
		t.Fatalf("ID 非法时不应发包，实际调用 %d 次", w.calls)
	}
}

// 发送失败返回 500 send_failed，且不回显底层错误内容。
func TestWakeSendFailureIsSanitized(t *testing.T) {
	w := &fakeWaker{err: errors.New("WSANO_DATA: 无路由到主机 192.168.1.255")}
	h, _ := newWakeTestServer(t, w)

	rec := do(h, http.MethodPost, "/api/devices/1/wake", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "send_failed" {
		t.Fatalf("error = %q, want send_failed", e.Error)
	}
	if strings.Contains(rec.Body.String(), "WSANO_DATA") {
		t.Fatalf("响应泄露了底层系统错误: %s", rec.Body.String())
	}
}

// 只发一次：重复点击是两次独立请求，接口本身不放大调用次数。
func TestWakeCallsSenderOncePerRequest(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	do(h, http.MethodPost, "/api/devices/1/wake", "")
	if w.calls != 1 {
		t.Fatalf("单次请求的发包次数 = %d, want 1", w.calls)
	}
}

func TestWakeRejectsWrongMethod(t *testing.T) {
	w := &fakeWaker{}
	h, _ := newWakeTestServer(t, w)

	rec := do(h, http.MethodGet, "/api/devices/1/wake", "")
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 或 404", rec.Code)
	}
	if w.calls != 0 {
		t.Fatal("方法不匹配时不应发包")
	}
}
