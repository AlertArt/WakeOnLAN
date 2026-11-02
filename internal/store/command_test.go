package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func sampleCommand(deviceID int64) Command {
	return Command{DeviceID: deviceID, Type: CommandShutdown, DelaySeconds: 60}
}

// 指令写入后是 pending，且带过期时间。
func TestCreateCommandPending(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	cmd, err := db.CreateCommand(ctx, sampleCommand(1))
	if err != nil {
		t.Fatalf("CreateCommand() error = %v", err)
	}
	if cmd.ID != 1 || cmd.Status != CommandStatusPending {
		t.Fatalf("cmd = %+v, want id=1 status=pending", cmd)
	}
	if cmd.DelaySeconds != 60 {
		t.Fatalf("delay = %d, want 60", cmd.DelaySeconds)
	}
	if cmd.ExpiresAt.Sub(cmd.CreatedAt) != CommandTTL {
		t.Fatalf("expires_at - created_at = %v, want %v", cmd.ExpiresAt.Sub(cmd.CreatedAt), CommandTTL)
	}
}

// 同一设备不允许并发指令：关机与重启不能排队打架。
func TestCreateCommandRejectsSecond(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	if _, err := db.CreateCommand(ctx, sampleCommand(1)); err != nil {
		t.Fatalf("第一次 CreateCommand() error = %v", err)
	}

	_, err := db.CreateCommand(ctx, Command{DeviceID: 1, Type: CommandRestart, DelaySeconds: 30})
	if !errors.Is(err, ErrCommandPending) {
		t.Fatalf("error = %v, want ErrCommandPending", err)
	}

	// 不同设备互不影响。
	if _, err := db.CreateCommand(ctx, sampleCommand(2)); err != nil {
		t.Fatalf("另一台设备的 CreateCommand() error = %v", err)
	}
}

// 取走即 delivered，且不会被第二次下发。
func TestTakePendingCommandsMarksDelivered(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	cmd, _ := db.CreateCommand(ctx, sampleCommand(1))

	list, err := db.TakePendingCommands(ctx, 1, time.Now())
	if err != nil {
		t.Fatalf("TakePendingCommands() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != cmd.ID || list[0].Status != CommandStatusDelivered {
		t.Fatalf("list = %+v, want 一条 delivered 指令", list)
	}
	if list[0].DeliveredAt.IsZero() {
		t.Fatal("delivered_at 未回填")
	}

	again, err := db.TakePendingCommands(ctx, 1, time.Now())
	if err != nil {
		t.Fatalf("第二次 TakePendingCommands() error = %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("第二次取到 %d 条，want 0", len(again))
	}
}

// 回执把 delivered 推进到 done/failed，且只能回执一次。
func TestAckCommand(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	cmd, _ := db.CreateCommand(ctx, sampleCommand(1))
	if _, err := db.TakePendingCommands(ctx, 1, time.Now()); err != nil {
		t.Fatalf("TakePendingCommands() error = %v", err)
	}

	acked, err := db.AckCommand(ctx, 1, cmd.ID, true, "已执行 shutdown /s /t 60")
	if err != nil {
		t.Fatalf("AckCommand() error = %v", err)
	}
	// 回执返回最新快照：handler 要靠 Type 判断是不是 RDP 指令。
	if acked.Type != cmd.Type || acked.Status != CommandStatusDone {
		t.Fatalf("acked = %+v, want type %s + done", acked, cmd.Type)
	}

	list, err := db.ListCommands(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListCommands() error = %v", err)
	}
	if len(list) != 1 || list[0].Status != CommandStatusDone || list[0].Message == "" {
		t.Fatalf("list = %+v, want done + message", list)
	}
	if list[0].FinishedAt.IsZero() {
		t.Fatal("finished_at 未回填")
	}

	// 重复回执按"不存在"处理，避免客户端重试覆盖结果。
	if _, err := db.AckCommand(ctx, 1, cmd.ID, false, "again"); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("重复回执 error = %v, want ErrCommandNotFound", err)
	}

	// 换个设备 ID 回执同样拒绝（防止越权）。
	if _, err := db.AckCommand(ctx, 2, cmd.ID, false, "wrong device"); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("跨设备回执 error = %v, want ErrCommandNotFound", err)
	}
}

// 超过 TTL 未取走的 pending 指令自动过期，不再下发。
func TestExpiredCommandIsNotDelivered(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	cmd, _ := db.CreateCommand(ctx, sampleCommand(1))

	// 直接改过期时间，避免测试等 5 分钟。
	future := time.Now().Add(CommandTTL + time.Minute)
	if _, err := db.ExpireStale(ctx, future); err != nil {
		t.Fatalf("ExpireStale() error = %v", err)
	}

	list, err := db.ListCommands(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListCommands() error = %v", err)
	}
	if list[0].Status != CommandStatusExpired {
		t.Fatalf("status = %q, want expired", list[0].Status)
	}

	got, err := db.TakePendingCommands(ctx, 1, future)
	if err != nil {
		t.Fatalf("TakePendingCommands() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("过期指令仍被下发 %d 条", len(got))
	}

	// 过期后可以重新下发
	if _, err := db.CreateCommand(ctx, sampleCommand(1)); err != nil {
		t.Fatalf("过期后 CreateCommand() error = %v", err)
	}
	_ = cmd
}

// 跨设备列表要带上设备名，供审计使用。
func TestListAllCommandsIncludesDeviceName(t *testing.T) {
	db := newMigratedDB(t)
	ctx := context.Background()

	dv, err := db.CreateDevice(ctx, sampleDevice("AA:BB:CC:DD:EE:07"))
	if err != nil {
		t.Fatalf("CreateDevice() error = %v", err)
	}
	if _, err := db.CreateCommand(ctx, sampleCommand(dv.ID)); err != nil {
		t.Fatalf("CreateCommand() error = %v", err)
	}

	list, err := db.ListAllCommands(ctx, 10)
	if err != nil {
		t.Fatalf("ListAllCommands() error = %v", err)
	}
	if len(list) != 1 || list[0].DeviceName != "书房主机" {
		t.Fatalf("list = %+v, want DeviceName=书房主机", list)
	}
}
