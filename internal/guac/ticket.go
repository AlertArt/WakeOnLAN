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

	// 不传 username/password：凭据由使用者在会话内的 Windows 登录界面输入。
	// 服务端不保存、也不转发任何 Windows 账号口令。
	//
	// security 用 tls 而不是 nla：NLA 要求在连接建立前给出凭据，guacd 会对
	// 没有凭据的连接弹「用户名/密码」提示框，而 guacd 等待用户应答的超时
	// 写死为 15 秒（GUACAMOLE-2233，1.6.0 尚不可配置）——人工输入口令几乎
	// 必然超时，前端表现为「远程桌面服务器拒绝了本连接」。目标机配合关闭
	// NLA（enable_rdp 设 UserAuthentication=0）后，TLS 模式在会话内呈现
	// 真正的 Windows 登录界面，没有时间限制。
	conn := rdpConn{
		Protocol: "rdp",
		Parameters: map[string]string{
			"hostname":         s.Host,
			"port":             strconv.Itoa(port),
			"security":         "tls",
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

// guacDataSource 是 guacamole-auth-json 这个认证扩展在 Guacamole 内部的标识，
// 也就是连接所属的「数据源」名。它由扩展自己的 AuthenticationProvider 标识决定，
// 与 guacamole.properties 无关，固定为 json。
const guacDataSource = "json"

// ClientURL 把 data 拼成 Guacamole 的**直连**地址。
//
// 路由用 #/client/<标识> 而不是首页 #/：auth-json 只做认证 + 注入连接，
// 落在首页还得手动点一下；带 client 路由时认证完立即发起会话。
// data 放在 # 之后的查询串里：前端从 location 里取，不经过服务端日志。
//
// 路径里的「标识」不是连接名，而是 Guacamole 的 ClientIdentifier
// （见 guacamole-client 的 app/navigation/types/ClientIdentifier.js）：
//
//	base64url( 连接标识 \0 类型 \0 数据源 )   // 类型 c = connection
//
// 只写连接名也能跑：前端解不出来时会退化成「连接名 + 当前登录用户的数据源」。
// 但那是靠猜——路由解析早于认证完成时，前端还拿不到数据源，会退回 "default"，
// 于是隧道创建按 "default" 去找连接，服务端返回 204，前端报
// 「请求的连接不存在，请先检查连接的名字再重试」。所以这里必须写全三项。
func (c Config) ClientURL(data, connName string) string {
	return c.BaseURL + "/#/client/" + clientIdentifier(connName) + "?data=" + url.QueryEscape(data)
}

// clientIdentifier 生成 Guacamole 的连接标识：base64url，去掉 = 填充。
// base64url 的字符集（A-Za-z0-9-_）在路径段里不需要再转义。
func clientIdentifier(connName string) string {
	raw := connName + "\x00" + "c" + "\x00" + guacDataSource
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
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
