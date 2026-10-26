package probe

import (
	"strings"
	"testing"
	"time"
)

func TestValidateHeartbeatSecret(t *testing.T) {
	good := []string{
		"0123456789abcdef",
		"hb-secret-0123456789",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_",
	}
	for _, s := range good {
		if err := ValidateHeartbeatSecret(s); err != nil {
			t.Fatalf("ValidateHeartbeatSecret(%q) = %v, want nil", s, err)
		}
	}

	bad := []string{
		"",
		"short",
		"0123456789abcde",       // 15 位
		strings.Repeat("a", 65), // 65 位
		"0123456789abcdef!",     // 非法字符
		"0123456789 abcdef",     // 空格
	}
	for _, s := range bad {
		err := ValidateHeartbeatSecret(s)
		if err == nil {
			t.Fatalf("ValidateHeartbeatSecret(%q) = nil, want error", s)
		}
		if !strings.Contains(err.Error(), "心跳密钥") {
			t.Fatalf("错误信息应说明密钥问题，实际 %v", err)
		}
	}
}

func TestValidateTargetAcceptsHeartbeat(t *testing.T) {
	if err := ValidateTarget("heartbeat", "hb-secret-0123456789"); err != nil {
		t.Fatalf("ValidateTarget(heartbeat) = %v, want nil", err)
	}
	if err := ValidateTarget("heartbeat", ""); err == nil {
		t.Fatal("空密钥应被拒绝")
	}
}

// 心跳没有主动探测动作，误调用时必须立刻失败，而不是去拨一个空地址。
func TestProbeRejectsHeartbeat(t *testing.T) {
	err := Probe(t.Context(), "heartbeat", "hb-secret-0123456789")
	if err == nil {
		t.Fatal("Probe(heartbeat) 应返回错误")
	}
	if !strings.Contains(err.Error(), "不发起主动探测") {
		t.Fatalf("错误信息不符: %v", err)
	}
}

func TestHeartbeatStale(t *testing.T) {
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		checkedAt time.Time
		want      bool
	}{
		{"从未上报", time.Time{}, true},
		{"刚刚", now.Add(-time.Second), false},
		{"2 分钟前", now.Add(-2 * time.Minute), false},
		{"恰好 3 分钟", now.Add(-HeartbeatTimeout), false},
		{"3 分零 1 秒前", now.Add(-HeartbeatTimeout - time.Second), true},
		{"1 小时前", now.Add(-time.Hour), true},
	}
	for _, c := range cases {
		if got := HeartbeatStale(c.checkedAt, now); got != c.want {
			t.Fatalf("%s: HeartbeatStale = %v, want %v", c.name, got, c.want)
		}
	}
}
