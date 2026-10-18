package wol

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

// listenLoopback 在回环地址上开一个随机端口，返回监听器与端口号。
// 用真实 UDP 套接字接收，替代抓包，验证「实际发出去的字节」。
func listenLoopback(t *testing.T) (*net.UDPConn, int) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("监听回环端口失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, conn.LocalAddr().(*net.UDPAddr).Port
}

// recvOne 读取一个报文，返回内容与来源。
func recvOne(t *testing.T, conn *net.UDPConn) ([]byte, *net.UDPAddr) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("设置读超时失败: %v", err)
	}
	buf := make([]byte, 2048)
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("未收到报文: %v", err)
	}
	return buf[:n], from
}

// 2-3：真实发出一次，接收端拿到的必须是 102 字节，且与构造结果逐字节相同。
func TestSendDeliversExactPacket(t *testing.T) {
	conn, port := listenLoopback(t)

	packet, err := BuildPacketFromString("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("构造包失败: %v", err)
	}

	if err := Send(packet, "127.0.0.1", port); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	got, _ := recvOne(t, conn)
	if len(got) != PacketLen {
		t.Fatalf("收到 %d 字节, want %d", len(got), PacketLen)
	}
	if !bytes.Equal(got, packet) {
		t.Fatalf("收到的字节与构造结果不同:\n got %X\nwant %X", got, packet)
	}

	// 同步头与 16 段 MAC：对线上真实收到的字节再验一遍。
	for i := 0; i < 6; i++ {
		if got[i] != 0xFF {
			t.Fatalf("线上报文同步头第 %d 字节 = 0x%02X", i, got[i])
		}
	}
	wantMAC := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for seg := 0; seg < 16; seg++ {
		if !bytes.Equal(got[6+seg*6:12+seg*6], wantMAC) {
			t.Fatalf("线上报文第 %d 段 MAC 不符", seg+1)
		}
	}
}

// 发一次只到一个包：接收端不应收到第二个报文。
func TestSendSendsExactlyOnce(t *testing.T) {
	conn, port := listenLoopback(t)

	packet, _ := BuildPacketFromString("AA:BB:CC:DD:EE:FF")
	if err := Send(packet, "127.0.0.1", port); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	recvOne(t, conn)

	if err := conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
		t.Fatalf("设置读超时失败: %v", err)
	}
	buf := make([]byte, 2048)
	if n, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("收到多余的第 2 个报文（%d 字节），说明发生了重发", n)
	}
}

// 2-4：参数非法时返回明确错误，不 panic。
func TestSendRejectsBadArguments(t *testing.T) {
	valid, _ := BuildPacketFromString("AA:BB:CC:DD:EE:FF")

	cases := []struct {
		name   string
		packet []byte
		addr   string
		port   int
		want   error
	}{
		{"空包", nil, "127.0.0.1", 9, ErrInvalidPacket},
		{"包过短", valid[:101], "127.0.0.1", 9, ErrInvalidPacket},
		{"包过长", append(valid, 0x00), "127.0.0.1", 9, ErrInvalidPacket},
		{"地址为空", valid, "", 9, ErrInvalidAddr},
		{"地址非 IP", valid, "host.local", 9, ErrInvalidAddr},
		{"地址不完整", valid, "192.168.1", 9, ErrInvalidAddr},
		{"IPv6 地址", valid, "::1", 9, ErrInvalidAddr},
		{"端口为 0", valid, "127.0.0.1", 0, ErrInvalidPort},
		{"端口为负", valid, "127.0.0.1", -1, ErrInvalidPort},
		{"端口越界", valid, "127.0.0.1", 65536, ErrInvalidPort},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Send(c.packet, c.addr, c.port)
			if err == nil {
				t.Fatal("应当返回错误")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("错误类型 = %v, want %v", err, c.want)
			}
		})
	}
}

// 向广播地址发送不应返回参数错误（能否真正到达取决于网络环境，不在本测试判定范围内）。
func TestSendAcceptsBroadcastAddress(t *testing.T) {
	packet, _ := BuildPacketFromString("AA:BB:CC:DD:EE:FF")

	for _, addr := range []string{"255.255.255.255", "192.168.1.255"} {
		if err := Send(packet, addr, DefaultBroadcastPort); err != nil {
			if errors.Is(err, ErrInvalidAddr) || errors.Is(err, ErrInvalidPort) || errors.Is(err, ErrInvalidPacket) {
				t.Fatalf("%s 被误判为参数非法: %v", addr, err)
			}
			// 套接字层错误（如无路由）在本机环境可接受，说明确实尝试发送了。
			t.Logf("%s 发送返回套接字错误（环境相关）: %v", addr, err)
		}
	}
}
