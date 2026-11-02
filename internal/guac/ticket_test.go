package guac

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 测试用密钥：32 位十六进制 = 16 字节，与 Guacamole 的 json-secret-key 同格式。
const testKey = "4c0b569e4c96df157eee1b65dd0e4d41"

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseConfig("https://nas.example.com/guacamole/", testKey, 0)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return cfg
}

// 载荷必须能被同密钥解开，且内容与入参一致：
// 这条测试同时锁住"签名 + 加密"的实现，防止换算法后静默失败。
func TestEncodeRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	expires := time.UnixMilli(1793617186000)

	data, err := cfg.Encode(Session{
		Username:       "wol-office",
		ConnectionName: "办公机",
		Host:           "192.168.1.10",
		Port:           3389,
		ExpiresAt:      expires,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	plain, err := cfg.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	var got struct {
		Username    string `json:"username"`
		Expires     int64  `json:"expires"`
		Connections map[string]struct {
			Protocol   string            `json:"protocol"`
			Parameters map[string]string `json:"parameters"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatalf("明文不是合法 JSON: %v", err)
	}

	if got.Username != "wol-office" {
		t.Errorf("username = %q", got.Username)
	}
	if got.Expires != expires.UnixMilli() {
		t.Errorf("expires = %d，期望 %d（毫秒时间戳）", got.Expires, expires.UnixMilli())
	}
	conn, ok := got.Connections["办公机"]
	if !ok {
		t.Fatalf("连接名不对：%v", got.Connections)
	}
	if conn.Protocol != "rdp" {
		t.Errorf("protocol = %q", conn.Protocol)
	}
	if conn.Parameters["hostname"] != "192.168.1.10" || conn.Parameters["port"] != "3389" {
		t.Errorf("主机参数不对：%v", conn.Parameters)
	}
	if conn.Parameters["security"] != "nla" {
		t.Errorf("security = %q，期望 nla", conn.Parameters["security"])
	}
	// 明确断言不透传任何凭据：Windows 账号由使用者在 Guacamole 的登录界面输入。
	if _, ok := conn.Parameters["username"]; ok {
		t.Error("不应该携带 Windows 用户名")
	}
	if _, ok := conn.Parameters["password"]; ok {
		t.Error("不应该携带 Windows 口令")
	}
}

// 换一个密钥就解不开：签名与加密都用同一把密钥，任何一侧不一致都必须失败。
func TestDecodeWithWrongKeyFails(t *testing.T) {
	cfg := testConfig(t)
	data, err := cfg.Encode(Session{Username: "u", ConnectionName: "c", Host: "10.0.0.1", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	other, err := ParseConfig("https://nas.example.com/guacamole", "00000000000000000000000000000000", 0)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if _, err := other.Decode(data); err == nil {
		t.Fatal("换密钥后仍然解开了，说明签名没有被校验")
	}
}

// 篡改密文必须被 HMAC 拦住，而不是解出一段乱码数据就放行。
func TestTamperedPayloadRejected(t *testing.T) {
	cfg := testConfig(t)
	data, err := cfg.Encode(Session{Username: "u", ConnectionName: "c", Host: "10.0.0.1", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	raw := []byte(data)
	// 改掉中间一个字符，仍保持合法 base64 字符集。
	if raw[10] == 'A' {
		raw[10] = 'B'
	} else {
		raw[10] = 'A'
	}
	if _, err := cfg.Decode(string(raw)); err == nil {
		t.Fatal("篡改后的载荷没有被拒绝")
	}
}

// URL 里的 base64 必须转义：+ 会被当成空格，= 会截断查询串。
func TestClientURL(t *testing.T) {
	cfg := testConfig(t)
	got := cfg.ClientURL("ab+c/d==")

	if !strings.HasPrefix(got, "https://nas.example.com/guacamole/#/?data=") {
		t.Fatalf("前缀不对：%s", got)
	}
	if strings.Contains(got, "ab+c/d==") {
		t.Errorf("base64 未转义：%s", got)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("生成的 URL 不合法：%v", err)
	}
	// 参数在 fragment 里，服务端收不到，也就不会进访问日志。
	if parsed.RawQuery != "" {
		t.Errorf("参数不应出现在服务端可见的查询串里：%s", parsed.RawQuery)
	}
	if !strings.Contains(got, "ab%2Bc%2Fd%3D%3D") {
		t.Errorf("转义结果不符合预期：%s", got)
	}
}

func TestParseConfig(t *testing.T) {
	t.Run("都为空表示未配置", func(t *testing.T) {
		if _, err := ParseConfig("", "", 0); err != ErrNotConfigured {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("只配一半也算未配置", func(t *testing.T) {
		if _, err := ParseConfig("https://nas/guacamole", "", 0); err != ErrNotConfigured {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("密钥长度不对", func(t *testing.T) {
		if _, err := ParseConfig("https://nas/guacamole", "abc", 0); err != ErrBadSecret {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("密钥含非十六进制字符", func(t *testing.T) {
		if _, err := ParseConfig("https://nas/guacamole", strings.Repeat("z", 32), 0); err != ErrBadSecret {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("URL 缺协议", func(t *testing.T) {
		if _, err := ParseConfig("nas/guacamole", testKey, 0); err == nil {
			t.Fatal("应该报错")
		}
	})
	t.Run("末尾斜杠被去掉", func(t *testing.T) {
		cfg, err := ParseConfig("https://nas/guacamole///", testKey, 0)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if cfg.BaseURL != "https://nas/guacamole" {
			t.Errorf("BaseURL = %q", cfg.BaseURL)
		}
	})
	t.Run("默认 TTL 60 秒", func(t *testing.T) {
		cfg, err := ParseConfig("https://nas/guacamole", testKey, 0)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if cfg.TTL != DefaultTTL {
			t.Errorf("TTL = %v，期望 %v", cfg.TTL, DefaultTTL)
		}
		if DefaultTTL != 60*time.Second {
			t.Errorf("DefaultTTL = %v，契约是 60 秒", DefaultTTL)
		}
	})
}
