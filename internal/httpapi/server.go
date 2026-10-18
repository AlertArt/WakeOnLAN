// Package httpapi 组装路由与中间件。
// 接口清单与字段形状以 docs/E1-contract.md 为准，路由表集中在 server.go 维护。
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"wol/internal/store"
)

// Pinger 抽象"能否与依赖通信"，由 store.DB 实现，测试时可替换。
type Pinger interface {
	Ping(ctx context.Context) error
}

// DeviceStore 是设备持久化能力的接口，便于 handler 测试时注入假实现。
// 与 store.DB 的方法签名一一对应，不额外扩展。
type DeviceStore interface {
	ListDevices(ctx context.Context) ([]store.Device, error)
	GetDevice(ctx context.Context, id int64) (store.Device, error)
	CreateDevice(ctx context.Context, dv store.Device) (store.Device, error)
	UpdateDevice(ctx context.Context, dv store.Device) (store.Device, error)
	DeleteDevice(ctx context.Context, id int64) error
}

// Server 持有路由所需的依赖。
type Server struct {
	db      Pinger
	devices DeviceStore
	logger  *slog.Logger
	webDir  string
}

// New 构造 Server。
func New(db Pinger, devices DeviceStore, logger *slog.Logger, webDir string) *Server {
	return &Server{db: db, devices: devices, logger: logger, webDir: webDir}
}

// Routes 返回完整路由表。新增接口必须在此登记，便于一处审阅。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// 健康检查：Docker HEALTHCHECK 与 E7 验收 7-1 依赖此路径。
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// F1 设备管理：四条路由对应 docs/E1-contract.md 的四个接口。
	mux.HandleFunc("GET /api/devices", s.handleListDevices)
	mux.HandleFunc("POST /api/devices", s.handleCreateDevice)
	mux.HandleFunc("PUT /api/devices/{id}", s.handleUpdateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.handleDeleteDevice)

	// 静态资源：E0 仅一个占位页，前端页面由 E5 在此目录内新增。
	mux.Handle("/", http.FileServer(http.Dir(s.webDir)))

	return s.withRequestLog(mux)
}

type healthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Time   string `json:"time"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := healthResponse{Status: "ok", DB: "ok", Time: time.Now().Format(time.RFC3339)}
	code := http.StatusOK

	if err := s.db.Ping(ctx); err != nil {
		resp.Status = "degraded"
		resp.DB = "error"
		code = http.StatusServiceUnavailable
		s.logger.Warn("健康检查失败", "err", err)
	}

	writeJSON(w, code, resp)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// apiError 是所有失败响应的统一形状，见契约「错误码表」。
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, code int, key, msg string) {
	writeJSON(w, code, apiError{Error: key, Message: msg})
}

// withRequestLog 输出最小访问日志，不含请求体与口令等敏感信息。
func (s *Server) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}
