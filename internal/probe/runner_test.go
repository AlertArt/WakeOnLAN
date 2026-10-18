package probe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// statusEvent 记录一次状态写入，用于断言状态迁移顺序。
type statusEvent struct {
	deviceID int64
	status   string
	message  string
}

type fakeStatus struct {
	mu       sync.Mutex
	events   []statusEvent
	terminal chan statusEvent
	startErr error
}

func newFakeStatus() *fakeStatus {
	return &fakeStatus{terminal: make(chan statusEvent, 8)}
}

func (f *fakeStatus) record(ev statusEvent) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}

func (f *fakeStatus) snapshot() []statusEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]statusEvent, len(f.events))
	copy(out, f.events)
	return out
}

func (f *fakeStatus) MarkChecking(_ context.Context, deviceID int64) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.record(statusEvent{deviceID: deviceID, status: "checking"})
	return nil
}

func (f *fakeStatus) MarkOnline(_ context.Context, deviceID int64) error {
	ev := statusEvent{deviceID: deviceID, status: "online"}
	f.record(ev)
	f.terminal <- ev
	return nil
}

func (f *fakeStatus) MarkTimeout(_ context.Context, deviceID int64, message string) error {
	ev := statusEvent{deviceID: deviceID, status: "timeout", message: message}
	f.record(ev)
	f.terminal <- ev
	return nil
}

func (f *fakeStatus) MarkFailed(_ context.Context, deviceID int64, message string) error {
	ev := statusEvent{deviceID: deviceID, status: "failed", message: message}
	f.record(ev)
	f.terminal <- ev
	return nil
}

func (f *fakeStatus) waitTerminal(t *testing.T, d time.Duration) statusEvent {
	t.Helper()
	select {
	case ev := <-f.terminal:
		return ev
	case <-time.After(d):
		t.Fatalf("等待终态超时，已记录事件：%+v", f.snapshot())
		return statusEvent{}
	}
}

func newTestRunner(t *testing.T, st StatusWriter) *Runner {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRunner(context.Background(), st, logger)
}

// 关闭的端口，用于构造「一直探测不到」的目标。
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestRunnerMarksOnlineOnFirstAttempt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	st := newFakeStatus()
	r := newTestRunner(t, st)

	if err := r.Start(1, "tcp", ln.Addr().String()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// 202 返回后立刻可读到的第一个状态必须是 checking。
	events := st.snapshot()
	if len(events) == 0 || events[0].status != "checking" {
		t.Fatalf("首个状态应为 checking，实际 %+v", events)
	}

	ev := st.waitTerminal(t, 2*time.Second)
	if ev.status != "online" || ev.message != "" {
		t.Fatalf("终态 = %+v, want online 且无 message", ev)
	}
	if r.Running(1) {
		t.Fatalf("终态后不应仍标记为进行中")
	}
}

func TestRunnerMarksTimeoutAfterMaxWait(t *testing.T) {
	st := newFakeStatus()
	r := newTestRunner(t, st)
	r.maxWait = 1 * time.Second
	r.interval = 200 * time.Millisecond

	start := time.Now()
	if err := r.Start(2, "tcp", closedAddr(t)); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	ev := st.waitTerminal(t, 3*time.Second)
	elapsed := time.Since(start)

	if ev.status != "timeout" {
		t.Fatalf("终态 = %q, want timeout", ev.status)
	}
	if ev.message != "1 秒内未探测到设备上线" {
		t.Fatalf("message = %q", ev.message)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("耗时 %v，未等满上限就判定超时", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("耗时 %v，明显超过上限", elapsed)
	}
}

func TestRunnerRejectsDuplicateStart(t *testing.T) {
	st := newFakeStatus()
	r := newTestRunner(t, st)
	r.maxWait = 600 * time.Millisecond
	r.interval = 100 * time.Millisecond

	addr := closedAddr(t)
	if err := r.Start(3, "tcp", addr); err != nil {
		t.Fatalf("首次 Start() error = %v", err)
	}
	if err := r.Start(3, "tcp", addr); !errors.Is(err, ErrInProgress) {
		t.Fatalf("第二次 Start() error = %v, want ErrInProgress", err)
	}

	// 只允许出现一次 checking：第二次调用不得产生任何写入。
	st.waitTerminal(t, 3*time.Second)
	count := 0
	for _, ev := range st.snapshot() {
		if ev.status == "checking" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("checking 写入次数 = %d, want 1（无任务叠加）", count)
	}
}

func TestRunnerFailsImmediatelyWhenToolMissing(t *testing.T) {
	old := pingBinary
	pingBinary = "wol-no-such-ping-binary"
	defer func() { pingBinary = old }()

	st := newFakeStatus()
	r := newTestRunner(t, st)
	r.maxWait = 5 * time.Second
	r.interval = 1 * time.Second

	start := time.Now()
	if err := r.Start(4, "ping", "127.0.0.1"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	ev := st.waitTerminal(t, 2*time.Second)
	if ev.status != "failed" {
		t.Fatalf("终态 = %q, want failed", ev.status)
	}
	if !strings.Contains(ev.message, "探测命令") || !strings.Contains(ev.message, "不存在") {
		t.Fatalf("message = %q，未说明命令缺失", ev.message)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("工具缺失应立即结束，实际耗时 %v", elapsed)
	}
}

func TestRunnerFailsOnInvalidTarget(t *testing.T) {
	st := newFakeStatus()
	r := newTestRunner(t, st)

	if err := r.Start(5, "tcp", "not-a-target"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	ev := st.waitTerminal(t, time.Second)
	if ev.status != "failed" {
		t.Fatalf("终态 = %q, want failed", ev.status)
	}
	if ev.message != "校验目标非法，请检查设备的校验设置" {
		t.Fatalf("message = %q", ev.message)
	}
}

func TestRunnerAllowsRestartAfterTerminal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()

	st := newFakeStatus()
	r := newTestRunner(t, st)

	for i := 0; i < 2; i++ {
		if err := r.Start(6, "tcp", ln.Addr().String()); err != nil {
			t.Fatalf("第 %d 次 Start() error = %v", i+1, err)
		}
		ev := st.waitTerminal(t, 2*time.Second)
		if ev.status != "online" {
			t.Fatalf("第 %d 次终态 = %q", i+1, ev.status)
		}
	}
}

func TestRunnerStartReportsWriteFailure(t *testing.T) {
	st := newFakeStatus()
	st.startErr = errors.New("磁盘只读")
	r := newTestRunner(t, st)

	err := r.Start(7, "tcp", "127.0.0.1:22")
	if err == nil {
		t.Fatalf("写入失败时 Start() 应返回错误")
	}
	// 启动失败必须释放占位，否则该设备会永久无法再触发。
	if r.Running(7) {
		t.Fatalf("启动失败后仍占用任务位")
	}
}

// 进程退出（ctx 取消）时不写终态，避免把「服务关闭」误记成「设备超时」。
func TestRunnerSilentOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st := newFakeStatus()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(ctx, st, logger)
	r.maxWait = 5 * time.Second
	r.interval = 100 * time.Millisecond

	if err := r.Start(8, "tcp", closedAddr(t)); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case ev := <-st.terminal:
		t.Fatalf("关闭时不应写终态，实际写入 %+v", ev)
	case <-time.After(500 * time.Millisecond):
	}

	if r.Running(8) {
		t.Fatalf("关闭后任务位应释放")
	}
}
