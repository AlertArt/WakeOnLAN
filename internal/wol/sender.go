// 本文件负责把构造好的包通过 UDP 发出去。
// 只发一次，不重试、不排队、不并发——这是 F2 的边界，也是 E2 的反向验收项。
package wol

import (
	"errors"
	"fmt"
	"net"
)

var (
	// ErrInvalidPacket 表示待发送的包长度不符合魔术包规范。
	ErrInvalidPacket = errors.New("待发送的包不是合法的魔术包")
	// ErrInvalidAddr 表示广播地址不是合法 IPv4。
	ErrInvalidAddr = errors.New("广播地址非法")
	// ErrInvalidPort 表示端口越界。
	ErrInvalidPort = errors.New("广播端口非法")
)

// Send 把 packet 以 UDP 广播方式发往 addr:port，发送后立即返回，不等待任何响应。
//
// 说明：UDP 是无连接的，内核不会因为「目标主机不可达」立即报错。
// 因此本函数能明确报告的失败只有两类——参数非法、本机无法创建/写入套接字，
// 与任务单约定的失败场景一致。
func Send(packet []byte, addr string, port int) error {
	if len(packet) != PacketLen {
		return fmt.Errorf("%w: 长度应为 %d 字节，实际 %d 字节", ErrInvalidPacket, PacketLen, len(packet))
	}

	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("%w: %q 不是 IPv4 地址", ErrInvalidAddr, addr)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: %d 超出 1-65535", ErrInvalidPort, port)
	}

	// Go 对 AF_INET 的 UDP 套接字默认开启 SO_BROADCAST，可直接发往广播地址。
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip.To4(), Port: port})
	if err != nil {
		return fmt.Errorf("创建 UDP 套接字失败: %w", err)
	}
	defer conn.Close()

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("发送魔术包失败: %w", err)
	}
	return nil
}
