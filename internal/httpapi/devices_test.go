package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"wol/internal/store"
)

// 契约白名单：响应中只允许出现这 9 个键，多一个即判不通过（验收项 1-6）。
var contractKeys = []string{
	"broadcast_addr", "broadcast_port", "created_at", "id",
	"mac", "name", "probe_type", "probe_value", "updated_at",
}

const validBody = `{"name":"书房主机","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","broadcast_port":9,"probe_type":"ping","probe_value":"192.168.1.10"}`

func newDeviceTestServer(t *testing.T) (http.Handler, *fakeDeviceStore) {
	t.Helper()
	fs := newFakeDeviceStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(fakePinger{}, fs, logger, "../../web").Routes(), fs
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertContractKeys 逐键比对响应对象的键集合，多余或缺失都失败。
func assertContractKeys(t *testing.T, raw []byte) {
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
	if strings.Join(got, ",") != strings.Join(contractKeys, ",") {
		t.Fatalf("响应字段与契约不一致\n got: %v\nwant: %v", got, contractKeys)
	}
}

func TestCreateDevice(t *testing.T) {
	h, fs := newDeviceTestServer(t)

	rec := do(h, http.MethodPost, "/api/devices", validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}
	assertContractKeys(t, rec.Body.Bytes())

	var got deviceResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ID != 1 {
		t.Fatalf("id = %d, want 1", got.ID)
	}
	if got.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("mac 未归一化: %q", got.MAC)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatal("时间戳为空")
	}
	if len(fs.items) != 1 {
		t.Fatalf("存储中设备数 = %d, want 1", len(fs.items))
	}
}

func TestCreateDeviceAppliesDefaults(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	rec := do(h, http.MethodPost, "/api/devices",
		`{"name":"客厅主机","mac":"aabbccddeeff","broadcast_addr":"192.168.1.255"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rec.Code, rec.Body.String())
	}

	var got deviceResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.BroadcastPort != 9 {
		t.Fatalf("broadcast_port = %d, want 默认 9", got.BroadcastPort)
	}
	if got.ProbeType != "none" {
		t.Fatalf("probe_type = %q, want none", got.ProbeType)
	}
	if got.ProbeValue != "" {
		t.Fatalf("probe_value = %q, want 空串", got.ProbeValue)
	}
}

// 反例：非法输入必须 400 且不写库（验收项 1-3）。
func TestCreateDeviceRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		key  string
	}{
		{"MAC 少一段", `{"name":"a","mac":"aa:bb:cc:dd:ee","broadcast_addr":"192.168.1.255"}`, "invalid_mac"},
		{"MAC 含非法字符", `{"name":"a","mac":"aa:bb:cc:dd:ee:zz","broadcast_addr":"192.168.1.255"}`, "invalid_mac"},
		{"MAC 分隔符混用", `{"name":"a","mac":"aa:bb-cc:dd:ee:ff","broadcast_addr":"192.168.1.255"}`, "invalid_mac"},
		{"名称为空", `{"name":"   ","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255"}`, "invalid_name"},
		{"名称超长", `{"name":"` + strings.Repeat("名", 33) + `","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255"}`, "invalid_name"},
		{"广播地址非法", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1"}`, "invalid_broadcast_addr"},
		{"端口越界", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","broadcast_port":70000}`, "invalid_broadcast_port"},
		{"校验类型非法", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","probe_type":"arp"}`, "invalid_probe"},
		{"ping 缺少目标", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","probe_type":"ping"}`, "invalid_probe"},
		{"tcp 目标格式错误", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","probe_type":"tcp","probe_value":"192.168.1.10"}`, "invalid_probe"},
		{"请求体不是 JSON", `not-json`, "invalid_json"},
		{"含契约外字段", `{"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","deleted_at":"x"}`, "invalid_json"},
		{"客户端擅自传 id", `{"id":99,"name":"a","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255"}`, "invalid_json"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, fs := newDeviceTestServer(t)
			rec := do(h, http.MethodPost, "/api/devices", c.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
			}

			var e apiError
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("错误响应不是 JSON: %v", err)
			}
			if e.Error != c.key {
				t.Fatalf("error = %q, want %q", e.Error, c.key)
			}
			if e.Message == "" {
				t.Fatal("错误响应缺少 message")
			}
			if len(fs.items) != 0 {
				t.Fatalf("非法输入不应写库，实际写入 %d 条", len(fs.items))
			}
		})
	}
}

// 反例：重复 MAC 必须 409 且不产生第二条记录（验收项 1-4）。
func TestCreateDeviceDuplicateMAC(t *testing.T) {
	h, fs := newDeviceTestServer(t)

	if rec := do(h, http.MethodPost, "/api/devices", validBody); rec.Code != http.StatusCreated {
		t.Fatalf("首次创建失败: %d %s", rec.Code, rec.Body.String())
	}

	// 换大小写与分隔符写同一个 MAC，仍应识别为重复。
	dup := `{"name":"另一台","mac":"AA-BB-CC-DD-EE-FF","broadcast_addr":"192.168.1.255"}`
	rec := do(h, http.MethodPost, "/api/devices", dup)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}

	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "duplicate_mac" {
		t.Fatalf("error = %q, want duplicate_mac", e.Error)
	}
	if len(fs.items) != 1 {
		t.Fatalf("设备数 = %d, want 1", len(fs.items))
	}
}

func TestCreateDeviceLimitExceeded(t *testing.T) {
	h, fs := newDeviceTestServer(t)
	for i := 0; i < store.MaxDevices; i++ {
		mac := "AA:BB:CC:DD:" + string(rune('A'+i/26)) + string(rune('A'+i%26))
		fs.items = append(fs.items, store.Device{ID: int64(i + 1), Name: "x", MAC: mac})
	}

	rec := do(h, http.MethodPost, "/api/devices", validBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "limit_exceeded" {
		t.Fatalf("error = %q, want limit_exceeded", e.Error)
	}
}

func TestListDevicesEmptyIsArray(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	rec := do(h, http.MethodGet, "/api/devices", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("空列表应返回 []，实际 %q", body)
	}
}

func TestListDevicesOrderAndShape(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	for i, mac := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"} {
		body := `{"name":"设备` + string(rune('A'+i)) + `","mac":"` + mac + `","broadcast_addr":"192.168.1.255"}`
		if rec := do(h, http.MethodPost, "/api/devices", body); rec.Code != http.StatusCreated {
			t.Fatalf("创建失败: %s", rec.Body.String())
		}
	}

	rec := do(h, http.MethodGet, "/api/devices", "")
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是数组: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("设备数 = %d, want 2", len(list))
	}
	for _, item := range list {
		raw, _ := json.Marshal(item)
		assertContractKeys(t, raw)
	}

	var items []deviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("解析设备数组失败: %v", err)
	}
	if items[0].ID >= items[1].ID {
		t.Fatalf("未按 id 升序返回: %d, %d", items[0].ID, items[1].ID)
	}
}

func TestUpdateDevice(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	created := do(h, http.MethodPost, "/api/devices", validBody)
	var before deviceResponse
	_ = json.Unmarshal(created.Body.Bytes(), &before)

	body := `{"name":"书房主机（改名）","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255","broadcast_port":7}`
	rec := do(h, http.MethodPut, "/api/devices/1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	assertContractKeys(t, rec.Body.Bytes())

	var after deviceResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if after.Name != "书房主机（改名）" {
		t.Fatalf("name = %q", after.Name)
	}
	if after.BroadcastPort != 7 {
		t.Fatalf("broadcast_port = %d, want 7", after.BroadcastPort)
	}
	if after.CreatedAt != before.CreatedAt {
		t.Fatalf("created_at 不应变化: %q -> %q", before.CreatedAt, after.CreatedAt)
	}
}

// 反例：修改同一设备的 MAC 不应被判重复。
func TestUpdateDeviceSameMACIsNotDuplicate(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	do(h, http.MethodPost, "/api/devices", validBody)
	rec := do(h, http.MethodPut, "/api/devices/1",
		`{"name":"改名","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

// 反例：修改到别的设备已占用的 MAC 必须 409。
func TestUpdateDeviceDuplicateMACOtherDevice(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	do(h, http.MethodPost, "/api/devices", validBody)
	do(h, http.MethodPost, "/api/devices",
		`{"name":"第二台","mac":"aa:bb:cc:dd:ee:02","broadcast_addr":"192.168.1.255"}`)

	rec := do(h, http.MethodPut, "/api/devices/2",
		`{"name":"第二台","mac":"aa:bb:cc:dd:ee:ff","broadcast_addr":"192.168.1.255"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rec.Code, rec.Body.String())
	}
}

// 反例：修改不存在的设备返回 404。
func TestUpdateDeviceNotFound(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	rec := do(h, http.MethodPut, "/api/devices/999", validBody)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

// 反例：非法 ID 返回 400。
func TestDeviceInvalidID(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	for _, path := range []string{"/api/devices/0", "/api/devices/-1", "/api/devices/abc"} {
		rec := do(h, http.MethodPut, path, validBody)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
		var e apiError
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error != "invalid_id" {
			t.Fatalf("%s error = %q, want invalid_id", path, e.Error)
		}
	}
}

func TestDeleteDevice(t *testing.T) {
	h, fs := newDeviceTestServer(t)

	do(h, http.MethodPost, "/api/devices", validBody)

	rec := do(h, http.MethodDelete, "/api/devices/1", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 不应有响应体，实际 %q", rec.Body.String())
	}
	if len(fs.items) != 0 {
		t.Fatalf("删除后仍有 %d 条记录", len(fs.items))
	}
}

// 反例：删除不存在的设备返回 404 且服务不崩溃（验收项 1-5）。
func TestDeleteDeviceNotFound(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	rec := do(h, http.MethodDelete, "/api/devices/42", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "not_found" {
		t.Fatalf("error = %q, want not_found", e.Error)
	}
}

// 存储层抛出未识别错误时返回 500，且不回显内部细节。
func TestDeviceStoreErrorHidesDetail(t *testing.T) {
	fs := newFakeDeviceStore()
	fs.err = errBoom
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(fakePinger{}, fs, logger, "../../web").Routes()

	rec := do(h, http.MethodGet, "/api/devices", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("响应泄露内部错误: %s", rec.Body.String())
	}
	var e apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error != "internal" {
		t.Fatalf("error = %q, want internal", e.Error)
	}
}

// 契约自检：健康检查接口的响应结构未被 E1 改动。
func TestHealthResponseStillStable(t *testing.T) {
	h, _ := newDeviceTestServer(t)

	rec := do(h, http.MethodGet, "/api/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if len(m) != 3 {
		t.Fatalf("健康检查字段数 = %d, want 3 (%v)", len(m), m)
	}
	for _, k := range []string{"status", "db", "time"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("健康检查缺少字段 %q", k)
		}
	}
}
