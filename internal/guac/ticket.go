// Package guac 生成 Guacamole 的一次性远程桌面会话票据。
//
// 方案选择：不用 Guacamole 的管理员 REST API。
// 那条路需要 guacamole-auth-jdbc + 一个数据库容器（建连接、建用户、授权、发共享链接），
// 而官方 guacamole/guacamole 镜像自带 guacamole-auth-json 扩展：
// 外部系统用共享密钥把一段 JSON「签名 + 加密」后交给 Guacamole，
// 它校验签名与过期时间通过后，直接按 JSON 里描述的参数建立连接。
//
// 于是 NAS 侧只需要 guacd + guacamole 两个容器，不引入数据库；
// wold 也不必持有 Guacamole 管理员口令，只需要一把与 Guacamole 共享的密钥。
//
// 载荷格式与算法以扩展自带的 doc/README.md 与 doc/encrypt-json.sh 为准：
//
//	JSON → HMAC/SHA-256（二进制签名前置）→ AES-128-CBC（全零 IV，PKCS#7 填充）→ base64
//
// 注意：base64 里含 + 与 =，放进 URL 前必须转义。
package guac

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 语义错误。上层据此给不同的提示，不解析错误字符串。
var (
	ErrNotConfigured = errors.New("未配置 Guacamole（WOL_GUAC_URL / WOL_GUAC_SECRET）")
	ErrBadSecret     = errors.New("WOL_GUAC_SECRET 必须是恰好 32 个十六进制字符（0-9/a-f，即 128 位 = 16 字节的 AES 密钥）")
)

// DefaultTTL 是票据有效期。
// 60 秒是"点完按钮、浏览器跳过去"所需的时间量级，再长就等于把入口公开挂着了。
const DefaultTTL = 60 * time.Second

// Config 是 Guacamole 接入参数。
type Config struct {
	// BaseURL 是 Guacamole 的访问前缀，例如 https://nas.example.com/guacamole。
	// 末尾斜杠会被去掉。
	BaseURL string
	// SecretKey 是 32 位十六进制字符串（16 字节），与 guacamole.properties 的
	// json-secret-key 必须是同一个值。
	SecretKey string
	// TTL 是票据有效期，0 表示 DefaultTTL。
	TTL time.Duration
}

// ParseConfig 校验并规整配置。URL 或密钥为空时返回 ErrNotConfigured：
// 调用方据此把功能降级（接口返回明确错误），而不是启动失败。
func ParseConfig(baseURL, secret string, ttl time.Duration) (Config, error) {
	baseURL = strings.TrimSpace(baseURL)
	secret = strings.TrimSpace(secret)
	if baseURL == "" && secret == "" {
		return Config{}, ErrNotConfigured
	}
	if baseURL == "" || secret == "" {
		return Config{}, ErrNotConfigured
	}
	if _, err := hex.DecodeString(secret); err != nil || len(secret) != 32 {
		return Config{}, ErrBadSecret
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return Config{}, fmt.Errorf("WOL_GUAC_URL 必须以 http:// 或 https:// 开头，当前为 %q", baseURL)
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return Config{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		SecretKey: strings.ToLower(secret),
		TTL:       ttl,
	}, nil
}

// Session 描述一次要授予的远程桌面连接。
type Session struct {
	// Username 是 Guacamole 里显示的会话用户名，用设备名即可（仅展示用途）。
	Username string
	// ConnectionName 是连接名，会显示在 Guacamole 客户端标题栏。
	ConnectionName string
	Host           string
	Port           int
	ExpiresAt      time.Time
}

// rdpPayload 是交给 Guacamole 的 JSON 结构。
// 字段顺序即序列化顺序：签名是对这段字节做的，顺序必须稳定。
type rdpPayload struct {
	Username    string             `json:"username"`
	Expires     int64              `json:"expires"`
	Connections map[string]rdpConn `json:"connections"`
}

type rdpConn struct {
	Protocol   string            `json:"protocol"`
	Parameters map[string]string `json:"parameters"`
}

// Encode 生成可直接放进 URL 的 data 参数（已 base64，未做 URL 转义）。
func (c Config) Encode(s Session) (string, error) {
	key, err := hex.DecodeString(c.SecretKey)
	if err != nil {
		return "", ErrBadSecret
	}

	port := s.Port
	if port <= 0 {
		port = 3389
	}

	// 不传 username/password：让 Guacamole 直接呈现 Windows 的登录界面，
	// 由使用者自己输凭据。服务端不保存、也不转发任何 Windows 账号口令。
	conn := rdpConn{
		Protocol: "rdp",
		Parameters: map[string]string{
			"hostname":         s.Host,
			"port":             strconv.Itoa(port),
			"security":         "nla",
			"ignore-cert":      "true",
			"resize-method":    "display-update",
			"disable-audio":    "true",
			"enable-wallpaper": "false",
		},
	}

	name := s.ConnectionName
	if name == "" {
		name = s.Username
	}
	payload := rdpPayload{
		Username:    s.Username,
		Expires:     s.ExpiresAt.UnixMilli(),
		Connections: map[string]rdpConn{name: conn},
	}

	// encoding/json 对 map 的键排序，连接名只有一个，顺序确定。
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化 Guacamole 载荷失败: %w", err)
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(plain)
	signed := append(mac.Sum(nil), plain...)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化 AES 失败: %w", err)
	}
	padded := pkcs7Pad(signed, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, padded)

	return base64.StdEncoding.EncodeToString(out), nil
}

// ClientURL 把 data 拼成 Guacamole 的**直连**地址。
//
// 路由用 #/client/<连接名> 而不是首页 #/：实测（1.6.0）auth-json 只做认证 +
// 注入连接，落点是主页，还得手动点连接；带 client 路由时认证完立即发起连接。
// data 放在 # 之后的查询串里：Guacamole 前端从 location 里取，不经过服务端日志。
// 连接名做路径转义，中文名与特殊字符都安全。
func (c Config) ClientURL(data, connName string) string {
	return c.BaseURL + "/#/client/" + url.PathEscape(connName) + "?data=" + url.QueryEscape(data)
}

// pkcs7Pad 按块大小补齐。PKCS#5 与 PKCS#7 在 16 字节分组下是同一件事。
func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	return append(b, bytes.Repeat([]byte{byte(n)}, n)...)
}

// Decode 仅供测试与排障使用：把 data 还原成 JSON 字节，便于断言内容。
func (c Config) Decode(data string) ([]byte, error) {
	key, err := hex.DecodeString(c.SecretKey)
	if err != nil {
		return nil, ErrBadSecret
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, errors.New("密文长度不是分组整数倍")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, raw)

	// 去填充
	n := int(out[len(out)-1])
	if n <= 0 || n > aes.BlockSize || n > len(out) {
		return nil, errors.New("填充非法")
	}
	out = out[:len(out)-n]
	if len(out) < sha256.Size {
		return nil, errors.New("明文过短")
	}
	sig, body := out[:sha256.Size], out[sha256.Size:]

	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("签名不匹配")
	}
	return body, nil
}
