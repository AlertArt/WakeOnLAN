package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wol/internal/auth"
	"wol/internal/store"
)

// authFixture 用独立的 Authenticator，避免限流与锁定的状态跨用例互相污染。
type authFixture struct {
	handler http.Handler
	authn   *auth.Authenticator
	devices *fakeDeviceStore
}

func newAuthFixture(t *testing.T, ttl time.Duration) authFixture {
	t.Helper()

	a, err := auth.New(testPassword, ttl)
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}

	fs := newFakeDeviceStore()
	fs.items = append(fs.items, store.Device{
		ID:            1,
		Name:          "书房主机",
		MAC:           "AA:BB:CC:DD:EE:FF",
		BroadcastAddr: "192.168.1.255",
		BroadcastPort: 9,
		ProbeType:     "ping",
		ProbeValue:    "192.168.1.10",
	})
	fs.nextID = 2

	h := New(fakePinger{}, fs, &fakeWaker{}, &fakeChecker{}, newFakeStatusStore(), a, newTestLogger(), "../../web").Routes()
	return authFixture{handler: h, authn: a, devices: fs}
}

// send 发送一个请求。token 为空表示不带 Authorization 头。
func (f authFixture) send(method, path, body, token string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	var req *http.Request
	if reader == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// login 登录并返回令牌，失败即终止用例。
func (f authFixture) login(t *testing.T) string {
	t.Helper()
	rec := f.send(http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", rec.Code, rec.Body)
	}
	var body loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("登录响应不是合法 JSON: %v", err)
	}
	return body.Token
}

// 4-1：不带令牌访问受保护接口，一律 401，且不得泄露任何设备信息。
func TestProtectedRoutesRejectMissingToken(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/devices"},
		{http.MethodPost, "/api/devices"},
		{http.MethodPut, "/api/devices/1"},
		{http.MethodDelete, "/api/devices/1"},
		{http.MethodPost, "/api/devices/1/wake"},
		{http.MethodPost, "/api/devices/1/check"},
		{http.MethodGet, "/api/status"},
		{http.MethodPost, "/api/logout"},
	}

	for _, c := range cases {
		rec := f.send(c.method, c.path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 状态码 = %d, want 401", c.method, c.path, rec.Code)
		}
		assertErrorCode(t, rec.Body.Bytes(), "unauthorized")

		// 关键：响应里不能出现设备 MAC、名称等任何业务数据。
		if body := rec.Body.String(); strings.Contains(body, "AA:BB:CC") || strings.Contains(body, "书房主机") {
			t.Fatalf("%s %s 泄露了设备信息: %s", c.method, c.path, body)
		}
	}
}

// 健康检查必须保持公开，否则容器探活会被锁死（E4 禁止事项 8）。
func TestHealthStaysPublic(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	rec := f.send(http.MethodGet, "/api/health", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health 状态码 = %d, want 200", rec.Code)
	}
}

// 登录页必须能在未登录时加载出来。
func TestStaticAssetsStayPublic(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	rec := f.send(http.MethodGet, "/", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("静态首页状态码 = %d, want 200", rec.Code)
	}
}

// 4-2：正确口令登录，响应字段与契约一致。
func TestLoginIssuesToken(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	rec := f.send(http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", rec.Code, rec.Body)
	}
	assertKeys(t, rec.Body.Bytes(), []string{"expires_at", "token"})

	var body loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(body.Token) != 43 {
		t.Fatalf("令牌长度 = %d, want 43", len(body.Token))
	}
	if _, err := time.Parse(time.RFC3339, body.ExpiresAt); err != nil {
		t.Fatalf("expires_at 不是 RFC3339: %q", body.ExpiresAt)
	}
	if err := f.authn.Validate(body.Token); err != nil {
		t.Fatalf("签发的令牌无法通过校验: %v", err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	rec := f.send(http.MethodPost, "/api/login", `{"password":"wrong"}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "invalid_credentials")
}

func TestLoginRejectsMalformedBody(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	for _, body := range []string{"", "not-json", `{"password":"x","role":"admin"}`} {
		rec := f.send(http.MethodPost, "/api/login", body, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%q 状态码 = %d, want 400", body, rec.Code)
		}
		assertErrorCode(t, rec.Body.Bytes(), "invalid_json")
	}
}

// 4-3：用令牌访问受保护接口，行为与 E1 一致。
func TestTokenGrantsAccess(t *testing.T) {
	f := newAuthFixture(t, time.Hour)
	token := f.login(t)

	rec := f.send(http.MethodGet, "/api/devices", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200, body=%s", rec.Code, rec.Body)
	}

	var list []deviceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应不是列表: %v", err)
	}
	if len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("设备列表 = %+v", list)
	}

	// 逐项核对字段白名单，确认鉴权没有改变 E1 的响应形状。
	var items []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("响应不是数组: %v", err)
	}
	for _, item := range items {
		assertContractKeys(t, item)
	}
}

func TestTokenHeaderFormats(t *testing.T) {
	f := newAuthFixture(t, time.Hour)
	token := f.login(t)

	// 小写 bearer 前缀应可用（前缀大小写不敏感）。
	req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	req.Header.Set("Authorization", "bearer "+token)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("小写 bearer 状态码 = %d, want 200", rec.Code)
	}

	// 其他方案、空令牌、以及把令牌放查询参数，都必须拒绝。
	for _, header := range []string{"Basic " + token, "Bearer", "Bearer   "} {
		req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization=%q 状态码 = %d, want 401", header, rec.Code)
		}
	}

	rec = f.send(http.MethodGet, "/api/devices?token="+token, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("查询参数中的令牌状态码 = %d, want 401", rec.Code)
	}
}

// 4-5：令牌过期后返回可识别的过期标识。
func TestExpiredTokenReportsExpired(t *testing.T) {
	f := newAuthFixture(t, 20*time.Millisecond)
	token := f.login(t)

	time.Sleep(80 * time.Millisecond)

	rec := f.send(http.MethodGet, "/api/devices", "", token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), "token_expired")
}

// 4-6：登出后旧令牌失效。
func TestLogoutInvalidatesToken(t *testing.T) {
	f := newAuthFixture(t, time.Hour)
	token := f.login(t)

	rec := f.send(http.MethodPost, "/api/logout", "", token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("登出状态码 = %d, want 204, body=%s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 不应带响应体: %s", rec.Body)
	}

	replay := f.send(http.MethodGet, "/api/devices", "", token)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("登出后复用令牌状态码 = %d, want 401", replay.Code)
	}
	assertErrorCode(t, replay.Body.Bytes(), "unauthorized")
}

// 4-4：连续 5 次错误口令后，第 6 次返回限流错误，且锁定期内正确口令也被拒。
func TestLoginLockedAfterFiveFailures(t *testing.T) {
	f := newAuthFixture(t, time.Hour)

	for i := 1; i <= auth.MaxFailures; i++ {
		rec := f.send(http.MethodPost, "/api/login", `{"password":"wrong"}`, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次状态码 = %d, want 401", i, rec.Code)
		}
	}

	rec := f.send(http.MethodPost, "/api/login", `{"password":"`+testPassword+`"}`, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次状态码 = %d, want 429, body=%s", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec.Body.Bytes(), "too_many_attempts")
	if !strings.Contains(rec.Body.String(), "稍后再试") {
		t.Fatalf("限流提示应包含「稍后再试」: %s", rec.Body)
	}
}

// 令牌不能混用：A 的令牌不能校验 B 的认证器。
func TestTokenBoundToIssuer(t *testing.T) {
	f := newAuthFixture(t, time.Hour)
	other := newAuthFixture(t, time.Hour)

	token := f.login(t)

	rec := other.send(http.MethodGet, "/api/devices", "", token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("跨认证器令牌状态码 = %d, want 401", rec.Code)
	}
}
