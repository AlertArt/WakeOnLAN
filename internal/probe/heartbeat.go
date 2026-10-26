// 本文件实现心跳在线校验（F9）的规则与常量。
//
// 心跳是"被动探测"：服务端不主动发包，由目标机上的客户端定时调用
// POST /api/heartbeat 上报。因此它不依赖 ping（Windows 默认丢弃入站 ICMP）、
// 也不需要目标机开放任何端口，只要目标机开着并且客户端在跑。
package probe

import (
	"fmt"
	"regexp"
	"time"
)

const (
	// HeartbeatInterval 是建议的上报间隔，也是客户端默认值。
	// 服务端在心跳响应里返回该值，客户端不必自己写死。
	HeartbeatInterval = 60 * time.Second

	// HeartbeatTimeout 是判定离线的阈值。
	// 取 3 倍间隔：允许连续丢失一到两次上报而不误判为离线。
	HeartbeatTimeout = 180 * time.Second

	// 心跳密钥（即设备的 probe_value）长度范围。
	// 密钥用于防止同网段的其他程序冒名上报，过短则形同虚设。
	MinHeartbeatSecretLen = 16
	MaxHeartbeatSecretLen = 64
)

// 密钥字符集：只允许 URL 安全字符，避免在命令行与任务计划里转义踩坑。
var heartbeatSecretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateHeartbeatSecret 校验心跳密钥。密钥就是 probe_value，与设备一起保存。
func ValidateHeartbeatSecret(secret string) error {
	if len(secret) < MinHeartbeatSecretLen || len(secret) > MaxHeartbeatSecretLen {
		return fmt.Errorf("%w: 心跳密钥需为 %d–%d 个字符", ErrInvalidTarget, MinHeartbeatSecretLen, MaxHeartbeatSecretLen)
	}
	if !heartbeatSecretPattern.MatchString(secret) {
		return fmt.Errorf("%w: 心跳密钥只能包含字母、数字、下划线和连字符", ErrInvalidTarget)
	}
	return nil
}

// HeartbeatStale 判断最近一次心跳是否已过期。
// checkedAt 为零值表示从未收到过心跳，按过期处理（调用方据此区分文案）。
func HeartbeatStale(checkedAt, now time.Time) bool {
	if checkedAt.IsZero() {
		return true
	}
	return now.Sub(checkedAt) > HeartbeatTimeout
}
