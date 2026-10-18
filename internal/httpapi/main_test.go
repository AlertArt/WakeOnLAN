package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"wol/internal/auth"
)

const testPassword = "test-password-1234"

var (
	// testAuthn 是全部用例共享的认证器，保证中间件走的是真实实现。
	testAuthn *auth.Authenticator
	// testTokenValue 是预先签发的有效令牌。
	testTokenValue string
)

// TestMain 统一构造认证器与令牌。放在 TestMain 而不是各 fixture 里，
// 是为了让 E1–E3 的既有用例只改一行构造代码就能继续跑。
func TestMain(m *testing.M) {
	a, err := auth.New(testPassword, time.Hour)
	if err != nil {
		panic(err)
	}
	tok, err := a.Login("127.0.0.1", testPassword)
	if err != nil {
		panic(err)
	}
	testAuthn = a
	testTokenValue = tok.Value
	os.Exit(m.Run())
}

// withTestToken 模拟「已登录的客户端」：给请求补上 Authorization 头。
// 注意它不绕过鉴权——中间件照常执行，只是令牌是真的。
func withTestToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		clone.Header.Set("Authorization", "Bearer "+testTokenValue)
		next.ServeHTTP(w, clone)
	})
}

// newTestLogger 返回丢弃输出的 logger，避免测试刷屏。
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
