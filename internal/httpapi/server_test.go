package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newTestServer(dbErr error) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(fakePinger{err: dbErr}, newFakeDeviceStore(), logger, "../../web").Routes()
}

func TestHealthOK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	newTestServer(nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Status != "ok" || body.DB != "ok" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestHealthDegradedWhenDBDown(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	newTestServer(errors.New("db down")).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHealthRejectsWrongMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	newTestServer(nil).ServeHTTP(rec, req)

	// Go 1.22+ ServeMux 对"方法不匹配"返回 405；部分实现返回 404 亦可接受，
	// 关键是不得落到正常业务分支。
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 405 或 404", rec.Code)
	}
}
