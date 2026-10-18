package wol

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// Service.Wake 的端到端验证：给合法参数，接收端应收到正确魔术包，并回填发送时间。
func TestServiceWakeDeliversPacket(t *testing.T) {
	conn, port := listenLoopback(t)
	svc := NewService()

	before := time.Now()
	res, err := svc.Wake("aa-bb-cc-dd-ee-ff", "127.0.0.1", port)
	if err != nil {
		t.Fatalf("Wake() error = %v", err)
	}
	after := time.Now()

	got, _ := recvOne(t, conn)
	want, _ := BuildPacketFromString("AA:BB:CC:DD:EE:FF")
	if !bytes.Equal(got, want) {
		t.Fatalf("收到的包与预期不同:\n got %X\nwant %X", got, want)
	}

	if res.SentAt.Before(before) || res.SentAt.After(after) {
		t.Fatalf("SentAt = %v 落在调用区间 [%v, %v] 之外", res.SentAt, before, after)
	}
}

// 非法 MAC 应在构造阶段失败，不会尝试发送。
func TestServiceWakeRejectsBadMAC(t *testing.T) {
	conn, port := listenLoopback(t)
	svc := NewService()

	if _, err := svc.Wake("不是MAC", "127.0.0.1", port); err == nil {
		t.Fatal("应当返回错误")
	} else if !errors.Is(err, ErrInvalidMAC) {
		t.Fatalf("错误应可用 errors.Is 匹配 ErrInvalidMAC，实际 %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("设置读超时失败: %v", err)
	}
	buf := make([]byte, 2048)
	if n, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("MAC 非法却发出了 %d 字节报文", n)
	}
}

// 非法端口同样应在发送前拦截。
func TestServiceWakeRejectsBadPort(t *testing.T) {
	svc := NewService()

	if _, err := svc.Wake("AA:BB:CC:DD:EE:FF", "127.0.0.1", 0); !errors.Is(err, ErrInvalidPort) {
		t.Fatalf("err = %v, want ErrInvalidPort", err)
	}
}
