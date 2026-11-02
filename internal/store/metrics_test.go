package store

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func mustCreateDevice(t *testing.T, db *DB, mac string) int64 {
	t.Helper()
	dv := sampleDevice(mac)
	dv.ProbeType = "heartbeat"
	dv.ProbeValue = "secret-123"
	created, err := db.CreateDevice(context.Background(), dv)
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}
	return created.ID
}

// Clamp 修正非法值：负数、NaN、超上限、超长字符串。
func TestMetricsClamp(t *testing.T) {
	m := Metrics{
		CPU:      -5,
		MemTotal: math.NaN(),
		DiskUsed: 1e15,
		MemUsed:  8192,
		IP:       "192.168.1.66",
		UptimeS:  -1,
	}
	for i := 0; i < 100; i++ {
		m.Hostname += "名"
	}

	ok := m.Clamp()
	if !ok {
		t.Fatalf("Clamp() = false, want true（MemUsed/IP/Hostname 仍在）")
	}
	if m.CPU != 0 {
		t.Errorf("CPU = %v, want 0（负数归零）", m.CPU)
	}
	if m.MemTotal != 0 {
		t.Errorf("MemTotal = %v, want 0（NaN 归零）", m.MemTotal)
	}
	if m.DiskUsed > 1e12 {
		t.Errorf("DiskUsed = %v, 超出上限未截断", m.DiskUsed)
	}
	if m.UptimeS != 0 {
		t.Errorf("UptimeS = %d, want 0", m.UptimeS)
	}
	if len([]rune(m.Hostname)) > metricsStringLimit {
		t.Errorf("Hostname 长度 %d, 未截断到 %d", len([]rune(m.Hostname)), metricsStringLimit)
	}
}

// 全空快照应被判为不可用，调用方可以直接丢弃。
func TestMetricsClampAllEmpty(t *testing.T) {
	if (&Metrics{}).Clamp() {
		t.Fatalf("Clamp() = true for zero Metrics, want false")
	}
}

// SaveMetrics + ListProbeStatus 往返：写入后能原样读回，时间为 RFC3339。
func TestSaveMetricsRoundTrip(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()
	id := mustCreateDevice(t, db, "AA:BB:CC:00:00:01")

	in := Metrics{
		CPU: 12.5, MemUsed: 6442, MemTotal: 16384,
		DiskUsed: 233, DiskTotal: 476,
		NetUp: 1024, NetDown: 8192,
		IP: "192.168.1.66", UptimeS: 86400,
		Hostname: "OFFICE-PC", OS: "Windows 10 企业版",
	}
	at := time.Now().Truncate(time.Second)
	if err := db.SaveMetrics(ctx, id, &in, at); err != nil {
		t.Fatalf("SaveMetrics() error = %v", err)
	}

	list, err := db.ListProbeStatus(ctx)
	if err != nil {
		t.Fatalf("ListProbeStatus() error = %v", err)
	}
	if len(list) != 1 || list[0].MetricsJSON == "" {
		t.Fatalf("指标未读回: %+v", list)
	}
	var out Metrics
	if err := json.Unmarshal([]byte(list[0].MetricsJSON), &out); err != nil {
		t.Fatalf("metrics_json 不是合法 JSON: %v", err)
	}
	if out != in {
		t.Errorf("往返不一致\n got: %+v\nwant: %+v", out, in)
	}
	if !list[0].MetricsAt.Equal(at) {
		t.Errorf("MetricsAt = %v, want %v", list[0].MetricsAt, at)
	}
}

// 二次写入覆盖前一次；SaveMetrics 对不存在设备返回 ErrNotFound。
func TestSaveMetricsOverwriteAndNotFound(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()
	id := mustCreateDevice(t, db, "AA:BB:CC:00:00:02")

	if err := db.SaveMetrics(ctx, id, &Metrics{CPU: 1}, time.Now()); err != nil {
		t.Fatalf("第一次 SaveMetrics() error = %v", err)
	}
	if err := db.SaveMetrics(ctx, id, &Metrics{CPU: 99}, time.Now()); err != nil {
		t.Fatalf("第二次 SaveMetrics() error = %v", err)
	}
	list, _ := db.ListProbeStatus(ctx)
	var out Metrics
	_ = json.Unmarshal([]byte(list[0].MetricsJSON), &out)
	if out.CPU != 99 {
		t.Errorf("CPU = %v, want 99（应被覆盖）", out.CPU)
	}

	if err := db.SaveMetrics(ctx, 9999, &Metrics{CPU: 1}, time.Now()); err != ErrNotFound {
		t.Errorf("SaveMetrics(9999) = %v, want ErrNotFound", err)
	}
}
