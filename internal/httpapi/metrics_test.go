package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"wol/internal/store"
)

// 带合法 metrics 的心跳：指标被钳制后落库，心跳本身照常 200。
func TestHeartbeatSavesMetrics(t *testing.T) {
	h, _, _, _ := newHeartbeatServerWithCommands(t)
	token := withTestToken(h)

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret + `","metrics":{
		"cpu":12.5,"mem_used":6442,"mem_total":16384,
		"disk_used":233,"disk_total":476,
		"net_up":1024,"net_down":8192,
		"ip":"192.168.1.66","uptime_s":86400,
		"hostname":"OFFICE-PC","os":"Windows 10 企业版"}}`
	rec := do(token, http.MethodPost, "/api/heartbeat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

// 畸形指标值被钳制而非丢弃，心跳不失败——指标只是展示数据。
func TestHeartbeatSanitizesBadMetrics(t *testing.T) {
	h, _, status, _ := newHeartbeatServerWithCommands(t)
	token := withTestToken(h)

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret + `","metrics":{
		"cpu":-99,"mem_total":null,
		"net_down":1e15,
		"ip":42,"hostname":{"evil":true},
		"unknown_key":"ignored"}}`
	rec := do(token, http.MethodPost, "/api/heartbeat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（坏指标不应影响心跳）, body=%s", rec.Code, rec.Body.String())
	}
	// 类型错/负数归零，超上限钳到 1e12；全零的无效字段（ip/hostname/mem_total）被丢弃。
	m, ok := status.metrics[1]
	if !ok {
		t.Fatalf("含有效字段的指标应落库")
	}
	if m.CPU != 0 || m.NetDown != 1e12 || m.IP != "" || m.Hostname != "" {
		t.Errorf("钳制结果不符: %+v", m)
	}
}

// 部分坏部分好：好的字段保留并落库。
func TestHeartbeatSavesPartialMetrics(t *testing.T) {
	h, _, status, _ := newHeartbeatServerWithCommands(t)
	token := withTestToken(h)

	body := `{"mac":"AA:BB:CC:DD:EE:FF","secret":"` + testHeartbeatSecret + `","metrics":{
		"cpu":55,"ip":"10.0.0.9","os":"Windows 10 企业版","net_down":"bad"}}`
	rec := do(token, http.MethodPost, "/api/heartbeat", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	m, ok := status.metrics[1]
	if !ok {
		t.Fatalf("指标未落库")
	}
	if m.CPU != 55 || m.IP != "10.0.0.9" || m.OS != "Windows 10 企业版" {
		t.Errorf("落库内容不符: %+v", m)
	}
	if m.NetDown != 0 {
		t.Errorf("坏类型 net_down = %v, want 0", m.NetDown)
	}
}

// 不带 metrics 的旧客户端：行为完全不变（状态接口也不出现 metrics 键）。
func TestStatusOmitsMetricsForLegacyClients(t *testing.T) {
	h, _, _, _ := newHeartbeatServerWithCommands(t)
	token := withTestToken(h)

	rec := do(token, http.MethodPost, "/api/heartbeat", `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("心跳 status = %d, want 200", rec.Code)
	}

	rec = do(token, http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析状态失败: %v", err)
	}
	for _, item := range list {
		if _, ok := item["metrics"]; ok {
			t.Fatalf("老客户端不应出现 metrics 键: %v", item)
		}
		if _, ok := item["metrics_at"]; ok {
			t.Fatalf("老客户端不应出现 metrics_at 键: %v", item)
		}
	}
}

// 落库过的指标通过 /api/status 原样透出，并带 metrics_at。
func TestStatusReturnsMetrics(t *testing.T) {
	h, _, status, _ := newHeartbeatServerWithCommands(t)
	token := withTestToken(h)

	// fake 状态库预置这台设备，SaveMetrics 才能同步出可读回的 items。
	status.items = append(status.items, store.ProbeStatus{DeviceID: 1, Status: store.StatusOnline})

	do(token, http.MethodPost, "/api/heartbeat", `{"mac":"AA:BB:CC:DD:EE:FF","secret":"`+testHeartbeatSecret+`","metrics":{"cpu":33,"mem_total":16384}}`)

	rec := do(token, http.MethodGet, "/api/status", "")
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析状态失败: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("状态为空")
	}
	var raw json.RawMessage
	var found bool
	for _, item := range list {
		if m, ok := item["metrics"]; ok {
			raw, found = m, true
			break
		}
	}
	if !found {
		t.Fatalf("带指标的设备未透出 metrics 键")
	}
	var m store.Metrics
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("metrics 不是合法 JSON: %v", err)
	}
	if m.CPU != 33 || m.MemTotal != 16384 {
		t.Errorf("metrics 内容不符: %+v", m)
	}
}
