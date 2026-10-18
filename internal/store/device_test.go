package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// newMigratedDB 返回一个已建表的临时库，测试结束自动关闭。
func newMigratedDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), t.TempDir(), "wol.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return db
}

func sampleDevice(mac string) Device {
	return Device{
		Name:          "书房主机",
		MAC:           mac,
		BroadcastAddr: "192.168.1.255",
		BroadcastPort: 9,
		ProbeType:     "ping",
		ProbeValue:    "192.168.1.10",
	}
}

// Migrate 必须幂等：重复调用不报错（服务重启会再次执行）。
func TestMigrateIsIdempotent(t *testing.T) {
	db := newMigratedDB(t)
	for i := 0; i < 2; i++ {
		if err := db.Migrate(context.Background()); err != nil {
			t.Fatalf("第 %d 次 Migrate() error = %v", i+2, err)
		}
	}
}

func TestCreateAndGetDevice(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	created, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01"))
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}
	if created.ID != 1 {
		t.Fatalf("id = %d, want 1", created.ID)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("时间戳未回填")
	}

	got, err := db.GetDevice(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDevice() error = %v", err)
	}
	if got.Name != created.Name || got.MAC != created.MAC || got.BroadcastAddr != created.BroadcastAddr {
		t.Fatalf("读回内容不一致: %+v vs %+v", got, created)
	}
	if got.ProbeType != "ping" || got.ProbeValue != "192.168.1.10" {
		t.Fatalf("probe 字段丢失: %+v", got)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("created_at 精度丢失: %v vs %v", got.CreatedAt, created.CreatedAt)
	}
}

// 反例：查询不存在的 id 返回 ErrNotFound。
func TestGetDeviceNotFound(t *testing.T) {
	db := newMigratedDB(t)

	if _, err := db.GetDevice(context.Background(), 12345); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListDevicesEmpty(t *testing.T) {
	db := newMigratedDB(t)

	list, err := db.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if list == nil {
		t.Fatal("空列表应返回空切片而非 nil，否则 JSON 会序列化为 null")
	}
	if len(list) != 0 {
		t.Fatalf("len = %d, want 0", len(list))
	}
}

func TestListDevicesOrderedByID(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	for _, mac := range []string{"AA:BB:CC:DD:EE:03", "AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"} {
		if _, err := db.CreateDevice(ctx, sampleDevice(mac)); err != nil {
			t.Fatalf("CreateDevice() error = %v", err)
		}
	}

	list, err := db.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].ID >= list[i].ID {
			t.Fatalf("未按 id 升序: %d >= %d", list[i-1].ID, list[i].ID)
		}
	}
}

// 反例：唯一索引必须拒绝重复 MAC。
func TestCreateDeviceDuplicateMAC(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	if _, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01")); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if _, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01")); !errors.Is(err, ErrDuplicateMAC) {
		t.Fatalf("err = %v, want ErrDuplicateMAC", err)
	}

	if n, _ := db.CountDevices(ctx); n != 1 {
		t.Fatalf("设备数 = %d, want 1", n)
	}
}

func TestUpdateDevice(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	created, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01"))

	changed := created
	changed.Name = "改名后的主机"
	changed.BroadcastPort = 7
	changed.ProbeType = "none"
	changed.ProbeValue = ""

	updated, err := db.UpdateDevice(ctx, changed)
	if err != nil {
		t.Fatalf("UpdateDevice() error = %v", err)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("created_at 不应变化: %v -> %v", created.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("updated_at 未刷新: %v", updated.UpdatedAt)
	}

	got, _ := db.GetDevice(ctx, created.ID)
	if got.Name != "改名后的主机" || got.BroadcastPort != 7 || got.ProbeType != "none" || got.ProbeValue != "" {
		t.Fatalf("更新未落库: %+v", got)
	}
}

// 反例：更新不存在的设备返回 ErrNotFound。
func TestUpdateDeviceNotFound(t *testing.T) {
	db := newMigratedDB(t)

	dv := sampleDevice("AA:BB:CC:DD:EE:09")
	dv.ID = 999
	if _, err := db.UpdateDevice(context.Background(), dv); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 反例：把设备改成另一台已占用的 MAC 返回 ErrDuplicateMAC。
func TestUpdateDeviceDuplicateMAC(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	if _, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01")); err != nil {
		t.Fatalf("创建第一台失败: %v", err)
	}
	second, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:02"))
	if err != nil {
		t.Fatalf("创建第二台失败: %v", err)
	}

	second.MAC = "AA:BB:CC:DD:EE:01"
	if _, err := db.UpdateDevice(ctx, second); !errors.Is(err, ErrDuplicateMAC) {
		t.Fatalf("err = %v, want ErrDuplicateMAC", err)
	}
}

func TestDeleteDevice(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	created, _ := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:01"))
	if err := db.DeleteDevice(ctx, created.ID); err != nil {
		t.Fatalf("DeleteDevice() error = %v", err)
	}
	if _, err := db.GetDevice(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后仍可查到: %v", err)
	}
}

// 反例：删除不存在的设备返回 ErrNotFound，不 panic。
func TestDeleteDeviceNotFound(t *testing.T) {
	db := newMigratedDB(t)

	if err := db.DeleteDevice(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateDeviceLimit(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	for i := 0; i < MaxDevices; i++ {
		if _, err := db.CreateDevice(ctx, sampleDevice(fmt.Sprintf("AA:BB:CC:DD:%02X:%02X", i/256, i%256))); err != nil {
			t.Fatalf("第 %d 台创建失败: %v", i+1, err)
		}
	}
	if _, err := db.CreateDevice(ctx, sampleDevice("FF:FF:FF:FF:FF:FF")); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want ErrLimitExceeded", err)
	}
}
