// 本文件提供供上层（HTTP 层）调用的最小门面。
// 刻意只暴露一个 Wake 方法：上层不需要知道包怎么构造、socket 怎么建。
package wol

import (
	"fmt"
	"time"
)

// Service 是 F2 对外的唯一入口。
// 它无可配置字段——重试、并发、超时都不在本期范围内。
type Service struct{}

// NewService 构造 Service。
func NewService() *Service { return &Service{} }

// Result 描述一次发送动作的结果。
// SentAt 是包交给内核的时刻，不代表设备已开机（那是 F3 的职责）。
type Result struct {
	SentAt time.Time
}

// Wake 解析 MAC、构造魔术包并发往指定广播地址与端口。
// 失败时返回带上下文的错误，由调用方决定如何呈现。
func (s *Service) Wake(mac, broadcastAddr string, port int) (Result, error) {
	packet, err := BuildPacketFromString(mac)
	if err != nil {
		return Result{}, fmt.Errorf("构造魔术包失败: %w", err)
	}

	if err := Send(packet, broadcastAddr, port); err != nil {
		return Result{}, err
	}

	return Result{SentAt: time.Now()}, nil
}
