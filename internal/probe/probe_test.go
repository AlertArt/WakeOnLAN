package probe

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"testing"
)

func TestProbeTCPOnline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	if err := Probe(context.Background(), "tcp", ln.Addr().String()); err != nil {
		t.Fatalf("在线目标应探测成功，实际 error = %v", err)
	}
}

func TestProbeTCPUnreachable(t *testing.T) {
	// 先占一个端口再关掉，确保该端口上没有任何监听。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	err = Probe(context.Background(), "tcp", addr)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
}

func TestProbePingLoopback(t *testing.T) {
	if _, err := exec.LookPath(pingBinary); err != nil {
		t.Skip("当前环境没有 ping，跳过")
	}
	if err := Probe(context.Background(), "ping", "127.0.0.1"); err != nil {
		t.Fatalf("回环地址应探测成功，实际 error = %v", err)
	}
}

func TestProbePingToolMissing(t *testing.T) {
	old := pingBinary
	pingBinary = "wol-no-such-ping-binary"
	defer func() { pingBinary = old }()

	err := Probe(context.Background(), "ping", "127.0.0.1")
	if !errors.Is(err, ErrToolMissing) {
		t.Fatalf("error = %v, want ErrToolMissing", err)
	}
	// 终态错误必须能被上层识别为「立即结束」，不能被当成「继续轮询」。
	if errors.Is(err, ErrUnreachable) {
		t.Fatalf("工具缺失不应被判定为未响应: %v", err)
	}
}

func TestValidateTargetAccepts(t *testing.T) {
	cases := []struct{ probeType, value string }{
		{"ping", "192.168.1.10"},
		{"ping", " 10.0.0.1 "},
		{"tcp", "192.168.1.10:22"},
		{"tcp", "127.0.0.1:3389"},
	}
	for _, c := range cases {
		if err := ValidateTarget(c.probeType, c.value); err != nil {
			t.Errorf("ValidateTarget(%q, %q) 应通过，实际 %v", c.probeType, c.value, err)
		}
	}
}

func TestValidateTargetRejects(t *testing.T) {
	cases := []struct{ name, probeType, value string }{
		{"类型为 none", "none", ""},
		{"类型为空", "", ""},
		{"未知类型", "icmp", "192.168.1.10"},
		{"ping 缺值", "ping", ""},
		{"ping 非 IPv4", "ping", "not-an-ip"},
		{"ping 主机名", "ping", "example.com"},
		{"ping IPv6", "ping", "::1"},
		{"tcp 缺值", "tcp", ""},
		{"tcp 缺端口", "tcp", "192.168.1.10"},
		{"tcp 端口为零", "tcp", "192.168.1.10:0"},
		{"tcp 端口越界", "tcp", "192.168.1.10:70000"},
		{"tcp 缺主机", "tcp", ":22"},
	}
	for _, c := range cases {
		err := ValidateTarget(c.probeType, c.value)
		if !errors.Is(err, ErrInvalidTarget) {
			t.Errorf("%s: error = %v, want ErrInvalidTarget", c.name, err)
		}
	}
}

// 探测不得修改入参，也不得因多次调用产生状态（纯函数式）。
func TestProbeIsRepeatable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	for i := 0; i < 3; i++ {
		if err := Probe(context.Background(), "tcp", addr); err != nil {
			t.Fatalf("第 %d 次探测失败: %v", i+1, err)
		}
	}
}
