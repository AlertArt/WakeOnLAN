package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// MaxFailures 是触发锁定的连续失败次数（4-4）。
	MaxFailures = 5
	// LockDuration 是锁定时长。
	LockDuration = 10 * time.Minute

	// tokenBytes 是令牌的随机字节数。32 字节经 base64url 编码后为 43 个字符。
	tokenBytes = 32
	// maxTrackedIPs 是失败计数表的容量上限，超过即整体清空。
	// 这不是功能，只是防止被大量伪造来源撑爆内存的兜底。
	maxTrackedIPs = 1024
)

var (
	// ErrBadPassword 表示口令错误。
	ErrBadPassword = errors.New("口令错误")
	// ErrLocked 表示该来源连续失败过多，处于锁定期。
	ErrLocked = errors.New("尝试次数过多")
	// ErrNoSession 表示令牌不存在、格式不对或已被吊销（含已登出）。
	ErrNoSession = errors.New("令牌无效")
	// ErrExpired 表示令牌存在但已过有效期。与 ErrNoSession 分开，
	// 前端据此区分"重新登录"与"登录已过期"（验收 4-5）。
	ErrExpired = errors.New("令牌已过期")
)

// Token 是一次登录的产物。
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// attempt 记录单个来源的失败情况。
type attempt struct {
	count       int
	lockedUntil time.Time
}

// Authenticator 持有口令摘要、有效期与会话表，并发安全。
type Authenticator struct {
	passwordHash [sha256.Size]byte
	ttl          time.Duration
	now          func() time.Time

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]time.Time // 令牌摘要 -> 到期时刻
	failures map[string]attempt              // 来源 IP -> 失败情况
}

// New 构造 Authenticator。口令为空或有效期非正数时拒绝，
// 避免"没配置口令"变成"口令为空也能登录"。
func New(password string, ttl time.Duration) (*Authenticator, error) {
	if password == "" {
		return nil, errors.New("登录口令为空")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("令牌有效期必须大于 0，当前为 %s", ttl)
	}
	return &Authenticator{
		passwordHash: sha256.Sum256([]byte(password)),
		ttl:          ttl,
		now:          time.Now,
		sessions:     make(map[[sha256.Size]byte]time.Time),
		failures:     make(map[string]attempt),
	}, nil
}

// TTL 返回令牌有效期，供启动日志使用（不输出任何令牌或口令）。
func (a *Authenticator) TTL() time.Duration { return a.ttl }

// Login 校验口令并签发令牌。ip 用于失败计数与锁定，由调用方从 TCP 对端地址取得，
// 不得使用可伪造的 X-Forwarded-For。
func (a *Authenticator) Login(ip, password string) (Token, error) {
	if a.locked(ip) {
		return Token{}, ErrLocked
	}
	if !a.match(password) {
		a.recordFailure(ip)
		return Token{}, ErrBadPassword
	}
	return a.issue(ip)
}

// Validate 校验令牌。返回值只有 nil、ErrNoSession、ErrExpired 三种。
func (a *Authenticator) Validate(token string) error {
	if token == "" {
		return ErrNoSession
	}
	key := sha256.Sum256([]byte(token))

	a.mu.Lock()
	defer a.mu.Unlock()

	exp, ok := a.sessions[key]
	if !ok {
		return ErrNoSession
	}
	// 刻意不在这里删除过期条目：若删除，同一令牌第二次访问会退化为 ErrNoSession，
	// 前端就分辨不出"过期"与"无效"。过期条目由登录时的 prune 清理。
	if !a.now().Before(exp) {
		return ErrExpired
	}
	return nil
}

// Revoke 吊销令牌，重复调用不报错（登出幂等）。
func (a *Authenticator) Revoke(token string) {
	if token == "" {
		return
	}
	key := sha256.Sum256([]byte(token))

	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, key)
}

// match 用固定时间比较口令摘要，避免按字节比较泄露长度与前缀。
func (a *Authenticator) match(password string) bool {
	sum := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(sum[:], a.passwordHash[:]) == 1
}

// locked 判断来源是否处于锁定期。锁定期已过则不算锁定，由 recordFailure 负责清零。
func (a *Authenticator) locked(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	att, ok := a.failures[ip]
	if !ok {
		return false
	}
	return a.now().Before(att.lockedUntil)
}

// recordFailure 累加失败次数，达到阈值即写入锁定。
func (a *Authenticator) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	att := a.failures[ip]

	// 上一轮锁定已过期：计数从头开始，否则用户会被永久锁死。
	if !att.lockedUntil.IsZero() && !now.Before(att.lockedUntil) {
		att = attempt{}
	}

	att.count++
	if att.count >= MaxFailures {
		att.lockedUntil = now.Add(LockDuration)
	}
	a.failures[ip] = att

	if len(a.failures) > maxTrackedIPs {
		a.failures = make(map[string]attempt)
	}
}

// issue 生成令牌并登记会话，同时清空该来源的失败计数。
func (a *Authenticator) issue(ip string) (Token, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, fmt.Errorf("生成令牌失败: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	key := sha256.Sum256([]byte(value))

	now := a.now()
	expires := now.Add(a.ttl)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.prune(now)
	delete(a.failures, ip)
	a.sessions[key] = expires

	return Token{Value: value, ExpiresAt: expires}, nil
}

// prune 清理已过期的会话。调用方必须已持有锁。
// 单用户场景下会话数极少，线性扫描足够。
func (a *Authenticator) prune(now time.Time) {
	for key, exp := range a.sessions {
		if !now.Before(exp) {
			delete(a.sessions, key)
		}
	}
}
