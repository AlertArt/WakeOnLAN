package wol

import (
	"bytes"
	"errors"
	"testing"
)

// 2-1：包结构必须恒为 102 字节，前 6 字节全 0xFF，其后 16 段完全相同且等于目标 MAC。
func TestBuildPacketStructure(t *testing.T) {
	mac, err := ParseMAC("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("ParseMAC() error = %v", err)
	}

	p := BuildPacket(mac)

	if len(p) != PacketLen {
		t.Fatalf("包长 = %d, want %d", len(p), PacketLen)
	}
	if len(p) != 102 {
		t.Fatalf("包长 = %d, want 102（协议常量被改动？）", len(p))
	}

	for i := 0; i < 6; i++ {
		if p[i] != 0xFF {
			t.Fatalf("同步头第 %d 字节 = 0x%02X, want 0xFF", i, p[i])
		}
	}

	want := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	for seg := 0; seg < 16; seg++ {
		got := p[6+seg*6 : 6+seg*6+6]
		if !bytes.Equal(got, want) {
			t.Fatalf("第 %d 段 MAC = %X, want %X", seg+1, got, want)
		}
	}
}

// 2-2：大写冒号、小写短横线、12 位裸十六进制三种写法，构造结果必须完全一致。
func TestBuildPacketAcceptsThreeMACForms(t *testing.T) {
	forms := []string{
		"AA:BB:CC:DD:EE:FF",
		"aa-bb-cc-dd-ee-ff",
		"aabbccddeeff",
		"  AA-BB-CC-DD-EE-FF  ",
		"AA:bb:CC:dd:EE:ff",
	}

	base, err := BuildPacketFromString(forms[0])
	if err != nil {
		t.Fatalf("基准写法解析失败: %v", err)
	}

	for _, f := range forms[1:] {
		got, err := BuildPacketFromString(f)
		if err != nil {
			t.Fatalf("%q 解析失败: %v", f, err)
		}
		if !bytes.Equal(got, base) {
			t.Fatalf("%q 构造结果与基准不一致:\n got %X\nwant %X", f, got, base)
		}
	}
}

func TestParseMACString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF"},
		{"AA-BB-CC-DD-EE-FF", "AA:BB:CC:DD:EE:FF"},
		{"aabbccddeeff", "AA:BB:CC:DD:EE:FF"},
		{"00:00:00:00:00:00", "00:00:00:00:00:00"},
		{"ff:ff:ff:ff:ff:ff", "FF:FF:FF:FF:FF:FF"},
	}
	for _, c := range cases {
		m, err := ParseMAC(c.in)
		if err != nil {
			t.Fatalf("ParseMAC(%q) error = %v", c.in, err)
		}
		if got := m.String(); got != c.want {
			t.Fatalf("ParseMAC(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseMACRejectsInvalid(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"aa:bb:cc:dd:ee",       // 少一段
		"aa:bb:cc:dd:ee:ff:00", // 多一段
		"aa:bb-cc:dd:ee:ff",    // 分隔符混用
		"aa:bb:cc:dd:ee:zz",    // 非十六进制
		"aabbccddee",           // 长度不足
		"aabbccddeeff00",       // 长度超出
		"aa::bb:cc:dd:ee:ff",   // 空段
		"AA:BB:CC:DD:EE:F",     // 段长不足
		"aaaa:bb:cc:dd:ee:ff",  // 段长超出
		"aa:bb:cc:dd:ee:ff;rm", // 注入式脏输入
	}

	for _, s := range bad {
		if _, err := ParseMAC(s); err == nil {
			t.Fatalf("ParseMAC(%q) 应当返回错误", s)
		} else if !errors.Is(err, ErrInvalidMAC) {
			t.Fatalf("ParseMAC(%q) 错误类型不对: %v", s, err)
		}
	}
}

// 纯函数性：同一输入反复调用必须得到相同字节，且不共享底层数组。
func TestBuildPacketIsPure(t *testing.T) {
	mac, _ := ParseMAC("AA:BB:CC:DD:EE:FF")

	a := BuildPacket(mac)
	b := BuildPacket(mac)
	if !bytes.Equal(a, b) {
		t.Fatal("同一输入两次构造结果不同")
	}

	if len(a) > 0 && len(b) > 0 {
		a[0] = 0x00
		if b[0] == 0x00 {
			t.Fatal("两次调用共享了底层数组，修改其一影响了另一")
		}
	}
}
