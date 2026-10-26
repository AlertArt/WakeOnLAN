// Package probe 实现唤醒结果校验（F3）：通过 ICMP 或 TCP 端口拨号判断目标是否已上线。
// 本包不依赖数据库与 HTTP，可脱离本服务单独调用。
package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// AttemptTimeout 是单次探测的超时上限。
// 它必须小于轮询间隔（5 秒），否则上一轮还没结束就要开始下一轮，会造成探测叠加。
const AttemptTimeout = 3 * time.Second

// 探测错误的三种语义。上层据此决定「继续轮询」还是「立即结束」。
var (
	// ErrUnreachable 表示本轮没有响应，目标可能还在启动，继续轮询有意义。
	ErrUnreachable = errors.New("目标本轮未响应")
	// ErrToolMissing 表示探测手段本身不可用（如容器内没有 ping），继续轮询也不会成功。
	ErrToolMissing = errors.New("探测命令不存在")
	// ErrInvalidTarget 表示设备的校验目标配置非法，属于数据问题而非网络问题。
	ErrInvalidTarget = errors.New("校验目标非法")
)

// pingBinary 是调用的 ping 可执行文件名。
// 只在测试中替换，不对外暴露配置项（E3 禁止新增配置）。
var pingBinary = "ping"

// ValidateTarget 校验设备的校验设置是否可执行。
// 与 Probe 共用同一套规则，避免「接口放行但探测必失败」的不一致。
func ValidateTarget(probeType, value string) error {
	switch probeType {
	case "ping":
		return validatePingTarget(value)
	case "tcp":
		return validateTCPTarget(value)
	case "heartbeat":
		// 心跳没有"探测目标"，probe_value 在这里存的是设备的上报密钥。
		return ValidateHeartbeatSecret(value)
	default:
		return fmt.Errorf("%w: 不支持的校验类型 %q", ErrInvalidTarget, probeType)
	}
}

// Probe 执行一次探测。
// 返回 nil 表示在线；ErrUnreachable 表示本轮未响应；
// ErrToolMissing / ErrInvalidTarget 表示终态失败，继续轮询没有意义。
func Probe(ctx context.Context, probeType, value string) error {
	if err := ValidateTarget(probeType, value); err != nil {
		return err
	}
	if probeType == "heartbeat" {
		// 心跳由客户端上报，服务端没有可执行的探测动作。
		// 上层（/api/devices/{id}/check）已拒绝该类型，这里是最后一道防线。
		return fmt.Errorf("%w: 心跳模式不发起主动探测", ErrInvalidTarget)
	}
	if probeType == "ping" {
		return probeICMP(ctx, value)
	}
	return probeTCP(ctx, value)
}

// validatePingTarget 只接受 IPv4 字面量。
// 不接受主机名与任意字符串：前者会引入 DNS 依赖与不确定的解析耗时，
// 后者可能被当成 ping 的参数（如 "-t"）而改变命令语义。
func validatePingTarget(value string) error {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("%w: ping 目标必须是 IPv4 地址", ErrInvalidTarget)
	}
	return nil
}

// validateTCPTarget 要求 host:port 且端口在 1-65535 之间。
func validateTCPTarget(value string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%w: tcp 目标必须是 主机:端口", ErrInvalidTarget)
	}
	if host == "" {
		return fmt.Errorf("%w: tcp 目标缺少主机", ErrInvalidTarget)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: tcp 端口必须在 1-65535 之间", ErrInvalidTarget)
	}
	return nil
}

func probeICMP(ctx context.Context, host string) error {
	path, err := exec.LookPath(pingBinary)
	if err != nil {
		// 这里返回 ErrToolMissing 而不是 ErrUnreachable：
		// 没有 ping 命令时轮询 120 秒也不会成功，应立即给出结论。
		return fmt.Errorf("%w: %s", ErrToolMissing, pingBinary)
	}

	out, err := exec.CommandContext(ctx, path, pingArgs(host)...).CombinedOutput()
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%w: %s", ErrUnreachable, lastLine(out))
	}
	// 找到了可执行文件却无法启动（权限、损坏），同样属于探测手段不可用。
	return fmt.Errorf("%w: %v", ErrToolMissing, err)
}

// pingArgs 按操作系统给出「只发一个包、超时与本包一致」的参数。
func pingArgs(host string) []string {
	if runtime.GOOS == "windows" {
		return []string{"-n", "1", "-w", strconv.Itoa(int(AttemptTimeout / time.Millisecond)), host}
	}
	return []string{"-c", "1", "-W", strconv.Itoa(int(AttemptTimeout / time.Second)), host}
}

func probeTCP(ctx context.Context, addr string) error {
	dialer := net.Dialer{Timeout: AttemptTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	_ = conn.Close()
	return nil
}

// lastLine 取输出最后一行，仅用于日志排查，不进入接口响应。
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}
