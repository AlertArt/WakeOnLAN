// 本文件实现 F5 的 HTTP 面：登录、登出、令牌校验中间件。
// 契约见 docs/E4-contract.md。此处的关键约束是「不改业务响应」——
// 鉴权只是在外面加一层，E1–E3 的响应体一个字段都不动。
package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"wol/internal/auth"
)

const bearerPrefix = "Bearer "

type loginRequest struct {
	Password string `json:"password"`
}

// loginResponse 是登录成功的响应，字段白名单恰好 2 个键。
type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON，或包含契约之外的字段")
		return
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_json", "请求体只能包含一个 JSON 对象")
		return
	}

	ip := clientIP(r)
	tok, err := s.auth.Login(ip, req.Password)
	switch {
	case errors.Is(err, auth.ErrLocked):
		s.logger.Warn("登录被限流", "ip", ip)
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "尝试次数过多，请稍后再试")
	case errors.Is(err, auth.ErrBadPassword):
		// 只记录来源与结果，绝不记录口令内容。
		s.logger.Warn("登录失败", "ip", ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "口令错误")
	case err != nil:
		s.logger.Error("签发令牌失败", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "服务内部错误")
	default:
		s.logger.Info("登录成功", "ip", ip)
		writeJSON(w, http.StatusOK, loginResponse{
			Token:     tok.Value,
			ExpiresAt: tok.ExpiresAt.Format(time.RFC3339),
		})
	}
}

// handleLogout 吊销当前令牌，幂等且不返回响应体。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := bearerToken(r); ok {
		s.auth.Revoke(token)
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireAuth 校验 Authorization 头。令牌只从请求头读取，
// 不接受 Cookie 与查询参数，避免令牌进入访问日志与 Referer。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}

		switch err := s.auth.Validate(token); {
		case errors.Is(err, auth.ErrExpired):
			// 与「无效」区分开，前端据此跳转登录页（E5 验收 5-3）。
			writeError(w, http.StatusUnauthorized, "token_expired", "登录已过期，请重新登录")
		case err != nil:
			writeError(w, http.StatusUnauthorized, "unauthorized", "登录状态无效，请重新登录")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// bearerToken 从 Authorization 头取出令牌。
// 前缀大小写不敏感，前缀后允许空白；其余格式一律视为未提供。
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if len(header) < len(bearerPrefix) {
		return "", false
	}
	if !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(bearerPrefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// clientIP 取 TCP 对端地址，用于失败计数。
// 刻意不读 X-Forwarded-For：那是客户端可控的头，加一个就能绕过锁定。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
