package auth

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

const testPassword = "test-password-1234"

var base = time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)

// newTestAuth 返回一个时钟可控的实例，便于精确验证过期与锁定。
func newTestAuth(t *testing.T, ttl time.Duration) *Authenticator {
	t.Helper()
	a, err := New(testPassword, ttl)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a.now = func() time.Time { return base }
	return a
}

func TestNewRejectsEmptyPassword(t *testing.T) {
	if _, err := New("", time.Hour); err == nil {
		t.Fatal("空口令应当被拒绝")
	}
}

func TestNewRejectsBadTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := New(testPassword, ttl); err == nil {
			t.Fatalf("有效期 %s 应当被拒绝", ttl)
		}
	}
}

func TestLoginIssuesWellFormedToken(t *testing.T) {
	a := newTestAuth(t, 7*24*time.Hour)

	tok, err := a.Login("10.0.0.2", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if len(tok.Value) != 43 {
		t.Fatalf("令牌长度 = %d, want 43", len(tok.Value))
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok.Value)
	if err != nil {
		t.Fatalf("令牌不是 base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("令牌熵 = %d 字节, want 32", len(raw))
	}
	if want := base.Add(7 * 24 * time.Hour); !tok.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %s, want %s", tok.ExpiresAt, want)
	}
	if err := a.Validate(tok.Value); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("err = %v, want ErrBadPassword", err)
	}
	// 空口令同样按错误处理，不能绕开计数。
	if _, err := a.Login("10.0.0.2", ""); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("空口令 err = %v, want ErrBadPassword", err)
	}
}

func TestValidateRejectsUnknownAndEmptyToken(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	if err := a.Validate(""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("空令牌 err = %v, want ErrNoSession", err)
	}
	if err := a.Validate("not-a-real-token"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("未知令牌 err = %v, want ErrNoSession", err)
	}
}

func TestValidateReportsExpired(t *testing.T) {
	a := newTestAuth(t, time.Minute)
	tok, err := a.Login("10.0.0.2", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	a.now = func() time.Time { return base.Add(time.Minute) }

	// 连续两次都必须是 ErrExpired：若第一次就把条目删掉，第二次会退化为
	// ErrNoSession，前端将无法区分「过期」与「无效」。
	for i := 0; i < 2; i++ {
		if err := a.Validate(tok.Value); !errors.Is(err, ErrExpired) {
			t.Fatalf("第 %d 次 err = %v, want ErrExpired", i+1, err)
		}
	}
}

func TestRevokeInvalidatesToken(t *testing.T) {
	a := newTestAuth(t, time.Hour)
	tok, err := a.Login("10.0.0.2", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	a.Revoke(tok.Value)

	if err := a.Validate(tok.Value); !errors.Is(err, ErrNoSession) {
		t.Fatalf("吊销后 err = %v, want ErrNoSession", err)
	}
	// 重复吊销不应报错（登出幂等）。
	a.Revoke(tok.Value)
}

func TestLockAfterFiveFailures(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	for i := 1; i <= MaxFailures; i++ {
		if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("第 %d 次 err = %v, want ErrBadPassword", i, err)
		}
	}

	// 第 6 次：即使口令正确也拒绝。
	if _, err := a.Login("10.0.0.2", testPassword); !errors.Is(err, ErrLocked) {
		t.Fatalf("锁定期内正确口令 err = %v, want ErrLocked", err)
	}

	// 锁定按来源隔离：另一个 IP 不受影响。
	if _, err := a.Login("10.0.0.3", testPassword); err != nil {
		t.Fatalf("其他来源 err = %v, want nil", err)
	}
}

func TestLockExpiresAndCountResets(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	for i := 0; i < MaxFailures; i++ {
		if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("第 %d 次 err = %v", i+1, err)
		}
	}

	// 锁定到期后可以再试，且计数从头开始：此刻 4 次失败不应触发锁定。
	a.now = func() time.Time { return base.Add(LockDuration + time.Second) }
	for i := 0; i < MaxFailures-1; i++ {
		if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("解锁后第 %d 次 err = %v", i+1, err)
		}
	}
	if _, err := a.Login("10.0.0.2", testPassword); err != nil {
		t.Fatalf("解锁后正确口令 err = %v, want nil", err)
	}
}

func TestSuccessResetsFailureCount(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	for i := 0; i < MaxFailures-1; i++ {
		if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("第 %d 次 err = %v", i+1, err)
		}
	}
	if _, err := a.Login("10.0.0.2", testPassword); err != nil {
		t.Fatalf("正确口令 err = %v, want nil", err)
	}

	// 计数已清零，再错 4 次仍不应锁定。
	for i := 0; i < MaxFailures-1; i++ {
		if _, err := a.Login("10.0.0.2", "wrong"); !errors.Is(err, ErrBadPassword) {
			t.Fatalf("重置后第 %d 次 err = %v", i+1, err)
		}
	}
	if _, err := a.Login("10.0.0.2", testPassword); err != nil {
		t.Fatalf("未达阈值就锁定，err = %v", err)
	}
}

func TestTokensAreDistinct(t *testing.T) {
	a := newTestAuth(t, time.Hour)

	first, err := a.Login("10.0.0.2", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	second, err := a.Login("10.0.0.3", testPassword)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if first.Value == second.Value {
		t.Fatal("两次登录签发了相同令牌")
	}
	// 吊销其中一个不影响另一个。
	a.Revoke(first.Value)
	if err := a.Validate(second.Value); err != nil {
		t.Fatalf("另一枚令牌 err = %v, want nil", err)
	}
}
