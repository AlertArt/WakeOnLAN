// 本文件实现魔术包的解析与构造，全部为纯函数：
// 同样的输入必然得到同样的 102 字节输出，不触碰网络、不读写任何外部状态。
package wol

import (
	"errors"
	"fmt"
	"strings"
)

// MAC 是解析后的 6 字节物理地址。
type MAC [6]byte

// 包结构常量。PacketLen 与 MagicHeader 定义在 doc.go。
const (
	// macLen 是单个 MAC 的长度。
	macLen = 6
	// macRepeat 是 MAC 在包中重复的次数，WOL 规范固定为 16。
	macRepeat = 16
)

// ErrInvalidMAC 表示输入无法解析为 MAC 地址。
var ErrInvalidMAC = errors.New("无法解析的 MAC 地址")

// String 返回大写冒号格式，便于日志与错误信息使用。
func (m MAC) String() string {
	var b strings.Builder
	b.Grow(17)
	for i, c := range m {
		if i > 0 {
			b.WriteByte(':')
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}

// ParseMAC 接受三种常见写法，返回 6 字节地址：
//
//	AA:BB:CC:DD:EE:FF
//	AA-BB-CC-DD-EE-FF
//	AABBCCDDEEFF
//
// 大小写不敏感；首尾空白被忽略；分隔符混用视为非法。
func ParseMAC(raw string) (MAC, error) {
	var m MAC
	s := strings.TrimSpace(raw)
	if s == "" {
		return m, ErrInvalidMAC
	}

	// 统一转成 12 位十六进制串，再按两位切分。
	var hexStr string
	hasColon := strings.Contains(s, ":")
	hasDash := strings.Contains(s, "-")

	switch {
	case hasColon && hasDash:
		return m, fmt.Errorf("%w: 分隔符不应混用", ErrInvalidMAC)
	case hasColon || hasDash:
		sep := ":"
		if hasDash {
			sep = "-"
		}
		parts := strings.Split(s, sep)
		if len(parts) != macLen {
			return m, fmt.Errorf("%w: 应为 6 段，实际 %d 段", ErrInvalidMAC, len(parts))
		}
		var b strings.Builder
		b.Grow(12)
		for _, p := range parts {
			if len(p) != 2 {
				return m, fmt.Errorf("%w: 每段应为 2 位十六进制", ErrInvalidMAC)
			}
			b.WriteString(p)
		}
		hexStr = b.String()
	default:
		hexStr = s
	}

	if len(hexStr) != macLen*2 {
		return m, fmt.Errorf("%w: 应为 12 位十六进制，实际 %d 位", ErrInvalidMAC, len(hexStr))
	}

	for i := 0; i < macLen; i++ {
		hi, ok := hexVal(hexStr[i*2])
		if !ok {
			return m, fmt.Errorf("%w: 含非十六进制字符 %q", ErrInvalidMAC, hexStr[i*2])
		}
		lo, ok := hexVal(hexStr[i*2+1])
		if !ok {
			return m, fmt.Errorf("%w: 含非十六进制字符 %q", ErrInvalidMAC, hexStr[i*2+1])
		}
		m[i] = hi<<4 | lo
	}
	return m, nil
}

// BuildPacket 构造魔术包：6 字节 0xFF + MAC 重复 16 次，共 102 字节。
// 纯函数，无错误分支——MAC 已是合法 6 字节，不存在构造失败的情况。
func BuildPacket(m MAC) []byte {
	p := make([]byte, 0, PacketLen)
	for i := 0; i < 6; i++ {
		p = append(p, MagicHeader)
	}
	for i := 0; i < macRepeat; i++ {
		p = append(p, m[:]...)
	}
	return p
}

// BuildPacketFromString 是 ParseMAC + BuildPacket 的组合，供上层一次调用。
func BuildPacketFromString(raw string) ([]byte, error) {
	m, err := ParseMAC(raw)
	if err != nil {
		return nil, err
	}
	return BuildPacket(m), nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
