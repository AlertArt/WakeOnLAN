package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wol/internal/store"
)

func TestNormalizeMAC(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF", true},
		{"AA:BB:CC:DD:EE:FF", "AA:BB:CC:DD:EE:FF", true},
		{"AA-BB-CC-DD-EE-FF", "AA:BB:CC:DD:EE:FF", true},
		{"aabbccddeeff", "AA:BB:CC:DD:EE:FF", true},
		{"AABBCCDDEEFF", "AA:BB:CC:DD:EE:FF", true},
		{"  aa:bb:cc:dd:ee:ff  ", "AA:BB:CC:DD:EE:FF", true},

		{"", "", false},
		{"aa:bb:cc:dd:ee", "", false},           // 少一段
		{"aa:bb:cc:dd:ee:ff:00", "", false},     // 多一段
		{"aa:bb-cc:dd:ee:ff", "", false},        // 分隔符混用
		{"aa:bb:cc:dd:ee:gg", "", false},        // 非十六进制
		{"aabbccddee", "", false},               // 长度不足
		{"aabbccddeeff00", "", false},           // 长度超出
		{"aa::bb:cc:dd:ee:ff", "", false},       // 空段
		{"AA:BB:CC:DD:EE:F", "", false},         // 段长不足
		{"aa:bb:cc:dd:ee:ff;rm -rf", "", false}, // 注入式脏输入
	}

	for _, c := range cases {
		got, ok := normalizeMAC(c.in)
		if ok != c.ok {
			t.Fatalf("normalizeMAC(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
		if ok && got != c.want {
			t.Fatalf("normalizeMAC(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsIPv4(t *testing.T) {
	good := []string{"192.168.1.255", "0.0.0.0", "255.255.255.255", "10.0.0.1"}
	bad := []string{"", "192.168.1", "999.1.1.1", "host.local", "::1", "fe80::1", "192.168.1.256"}

	for _, s := range good {
		if !isIPv4(s) {
			t.Fatalf("isIPv4(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isIPv4(s) {
			t.Fatalf("isIPv4(%q) = true, want false", s)
		}
	}
}

// fakeDeviceStore 是 DeviceStore 的内存实现，仅用于 handler 层测试。
// 语义错误与保存顺序刻意与 store 实现保持一致。
type fakeDeviceStore struct {
	items  []store.Device
	nextID int64
	err    error // 非 nil 时所有方法直接返回该错误，用于 500 分支
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{nextID: 1}
}

func (f *fakeDeviceStore) ListDevices(context.Context) ([]store.Device, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]store.Device, len(f.items))
	copy(out, f.items)
	return out, nil
}

func (f *fakeDeviceStore) GetDevice(_ context.Context, id int64) (store.Device, error) {
	if f.err != nil {
		return store.Device{}, f.err
	}
	for _, dv := range f.items {
		if dv.ID == id {
			return dv, nil
		}
	}
	return store.Device{}, store.ErrNotFound
}

func (f *fakeDeviceStore) CreateDevice(_ context.Context, dv store.Device) (store.Device, error) {
	if f.err != nil {
		return store.Device{}, f.err
	}
	if len(f.items) >= store.MaxDevices {
		return store.Device{}, store.ErrLimitExceeded
	}
	for _, x := range f.items {
		if strings.EqualFold(x.MAC, dv.MAC) {
			return store.Device{}, store.ErrDuplicateMAC
		}
	}
	now := time.Date(2026, 10, 17, 23, 40, 12, 0, time.UTC)
	dv.ID = f.nextID
	f.nextID++
	dv.CreatedAt, dv.UpdatedAt = now, now
	f.items = append(f.items, dv)
	return dv, nil
}

func (f *fakeDeviceStore) UpdateDevice(_ context.Context, dv store.Device) (store.Device, error) {
	if f.err != nil {
		return store.Device{}, f.err
	}
	for i, x := range f.items {
		if x.ID != dv.ID {
			continue
		}
		for _, y := range f.items {
			if y.ID != dv.ID && strings.EqualFold(y.MAC, dv.MAC) {
				return store.Device{}, store.ErrDuplicateMAC
			}
		}
		dv.CreatedAt = x.CreatedAt
		dv.UpdatedAt = x.UpdatedAt.Add(time.Minute)
		f.items[i] = dv
		return dv, nil
	}
	return store.Device{}, store.ErrNotFound
}

func (f *fakeDeviceStore) DeleteDevice(_ context.Context, id int64) error {
	if f.err != nil {
		return f.err
	}
	for i, x := range f.items {
		if x.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

var errBoom = errors.New("boom")
