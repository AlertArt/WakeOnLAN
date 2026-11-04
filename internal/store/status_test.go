package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func getStatus(t *testing.T, db *DB, deviceID int64) ProbeStatus {
	t.Helper()
	list, err := db.ListProbeStatus(context.Background())
	if err != nil {
		t.Fatalf("ListProbeStatus() error = %v", err)
	}
	for _, item := range list {
		if item.DeviceID == deviceID {
			return item
		}
	}
	t.Fatalf("未找到设备 %d 的状态", deviceID)
	return ProbeStatus{}
}

// 新建设备默认是「待校验」，且没有结论时间。
func TestNewDeviceDefaultsToPending(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01"))
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	st := getStatus(t, db, dv.ID)
	if st.Status != StatusPending {
		t.Fatalf("status = %q, want %q", st.Status, StatusPending)
	}
	if st.Message != "" {
		t.Fatalf("message = %q, want 空串", st.Message)
	}
	if !st.CheckedAt.IsZero() {
		t.Fatalf("checked_at = %v, want 零值", st.CheckedAt)
	}
}

func TestMarkOnlineSetsCheckedAt(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:02"))
	before := time.Now().Add(-time.Second)

	if err := db.MarkOnline(ctx, dv.ID); err != nil {
		t.Fatalf("MarkOnline() error = %v", err)
	}

	st := getStatus(t, db, dv.ID)
	if st.Status != StatusOnline {
		t.Fatalf("status = %q", st.Status)
	}
	if st.Message != "" {
		t.Fatalf("online 的 message 必须为空，实际 %q", st.Message)
	}
	if st.CheckedAt.Before(before) {
		t.Fatalf("checked_at = %v，未被刷新", st.CheckedAt)
	}
}

// 校验中不得覆盖「最近一次结论」的时间。
func TestMarkCheckingKeepsPreviousCheckedAt(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:03"))
	if err := db.MarkTimeout(ctx, dv.ID, "120 秒内未探测到设备上线"); err != nil {
		t.Fatalf("MarkTimeout() error = %v", err)
	}
	first := getStatus(t, db, dv.ID).CheckedAt

	time.Sleep(1100 * time.Millisecond) // 时间戳精度为秒，跨秒才能看出是否被覆盖
	if err := db.MarkChecking(ctx, dv.ID); err != nil {
		t.Fatalf("MarkChecking() error = %v", err)
	}

	st := getStatus(t, db, dv.ID)
	if st.Status != StatusChecking {
		t.Fatalf("status = %q", st.Status)
	}
	if !st.CheckedAt.Equal(first) {
		t.Fatalf("checked_at 被覆盖: %v -> %v", first, st.CheckedAt)
	}
}

func TestMarkFailedKeepsMessage(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:04"))
	msg := "探测命令 ping 不存在，无法校验"
	if err := db.MarkFailed(ctx, dv.ID, msg); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}

	st := getStatus(t, db, dv.ID)
	if st.Status != StatusFailed || st.Message != msg {
		t.Fatalf("status=%q message=%q", st.Status, st.Message)
	}
}

func TestSetStatusOnMissingDevice(t *testing.T) {
	db := newMigratedDB(t)

	err := db.MarkOnline(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

// 3-4 的存储侧基础：残留的 checking 必须在启动时被重置，否则永久卡死。
func TestMarkInterruptedResetsChecking(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:05"))
	if err := db.MarkChecking(ctx, dv.ID); err != nil {
		t.Fatalf("MarkChecking() error = %v", err)
	}

	n, err := db.MarkInterrupted(ctx)
	if err != nil {
		t.Fatalf("MarkInterrupted() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("重置行数 = %d, want 1", n)
	}

	st := getStatus(t, db, dv.ID)
	if st.Status != StatusPending {
		t.Fatalf("status = %q, want pending", st.Status)
	}
	if st.Message != "服务重启，上次校验中断" {
		t.Fatalf("message = %q", st.Message)
	}

	// 幂等：没有 checking 时再调用不报错、不产生行数。
	n, err = db.MarkInterrupted(ctx)
	if err != nil || n != 0 {
		t.Fatalf("第二次 MarkInterrupted() = (%d, %v), want (0, nil)", n, err)
	}
}

// E1 时代的数据文件没有状态列，迁移后必须补列且老数据可读。
func TestMigrateAddsStatusColumnsToOldDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir(), "old.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 刻意使用 E1 的建表语句，不含 probe_status 等三列。
	_, err = db.sql.ExecContext(ctx, `
CREATE TABLE device (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT    NOT NULL,
	mac            TEXT    NOT NULL,
	broadcast_addr TEXT    NOT NULL,
	broadcast_port INTEGER NOT NULL DEFAULT 9,
	probe_type     TEXT    NOT NULL DEFAULT 'none',
	probe_value    TEXT    NOT NULL DEFAULT '',
	created_at     TEXT    NOT NULL,
	updated_at     TEXT    NOT NULL
)`)
	if err != nil {
		t.Fatalf("构造旧表失败: %v", err)
	}
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO device (name, mac, broadcast_addr, broadcast_port, probe_type, probe_value, created_at, updated_at)
		 VALUES ('老设备','AA:BB:CC:DD:EE:06','192.168.1.255',9,'ping','192.168.1.10','2026-10-17T23:40:12+08:00','2026-10-17T23:40:12+08:00')`)
	if err != nil {
		t.Fatalf("写入旧数据失败: %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	list, err := db.ListProbeStatus(ctx)
	if err != nil {
		t.Fatalf("ListProbeStatus() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("状态条数 = %d, want 1", len(list))
	}
	if list[0].Status != StatusPending {
		t.Fatalf("老行补列后 status = %q, want pending", list[0].Status)
	}

	// 迁移后仍可正常写状态。
	if err := db.MarkOnline(ctx, list[0].DeviceID); err != nil {
		t.Fatalf("迁移后 MarkOnline() error = %v", err)
	}
}

// 空密码策略的写入必须能精确还原：allowed=false 时原值要一起清掉。
// 保留旧快照会在下一次关闭 RDP 时把用户后来的设置覆盖掉。
func TestSetRDPBlankPolicy(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:31"))
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	// 默认没动过：allowed=false、原值为空。
	st := getStatus(t, db, dv.ID)
	if st.RDPBlankAllowed || st.RDPBlankBefore != "" {
		t.Fatalf("新设备默认 = allowed:%v before:%q，应为 false/空", st.RDPBlankAllowed, st.RDPBlankBefore)
	}

	if err := db.SetRDPBlankPolicy(ctx, dv.ID, true, "1"); err != nil {
		t.Fatalf("SetRDPBlankPolicy(true) error = %v", err)
	}
	st = getStatus(t, db, dv.ID)
	if !st.RDPBlankAllowed || st.RDPBlankBefore != "1" {
		t.Fatalf("放行后 = allowed:%v before:%q", st.RDPBlankAllowed, st.RDPBlankBefore)
	}

	// 关闭时即便传了原值也要清空：我们不再持有这条策略。
	if err := db.SetRDPBlankPolicy(ctx, dv.ID, false, "1"); err != nil {
		t.Fatalf("SetRDPBlankPolicy(false) error = %v", err)
	}
	st = getStatus(t, db, dv.ID)
	if st.RDPBlankAllowed || st.RDPBlankBefore != "" {
		t.Fatalf("恢复后 = allowed:%v before:%q，应清空", st.RDPBlankAllowed, st.RDPBlankBefore)
	}

	if err := db.SetRDPBlankPolicy(ctx, 99999, true, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的设备 error = %v, want ErrNotFound", err)
	}
}

// 老库迁移后两个新列要有安全的默认值；重复迁移不报错（服务每次启动都会跑）。
func TestMigrateBlankPolicyDefaultsAndIdempotent(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:32"))
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("第二次 Migrate() error = %v", err)
	}

	st := getStatus(t, db, dv.ID)
	if st.RDPBlankAllowed || st.RDPBlankBefore != "" {
		t.Fatalf("迁移默认值 = allowed:%v before:%q", st.RDPBlankAllowed, st.RDPBlankBefore)
	}
}
