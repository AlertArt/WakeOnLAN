// 本文件实现校验任务的生命周期：启动、轮询、写终态。
// 任务完全在进程内：一个 map 记录进行中的设备，一个 goroutine 跑一轮轮询。
// 进程退出任务即消失，不做持久化、不做恢复、不做排队。
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// MaxWait 是一轮校验的总时长上限。超过即判定「超时未响应」。
	MaxWait = 120 * time.Second
	// Interval 是轮询间隔。
	Interval = 5 * time.Second
)

// ErrInProgress 表示该设备已有一轮校验在进行，本次不启动新任务。
var ErrInProgress = errors.New("该设备正在校验中")

// StatusWriter 是 Runner 对状态存储的最小依赖，按语义方法暴露。
// 这样 probe 包不需要知道状态字符串，也不需要 import 存储层。
type StatusWriter interface {
	MarkChecking(ctx context.Context, deviceID int64) error
	MarkOnline(ctx context.Context, deviceID int64) error
	MarkTimeout(ctx context.Context, deviceID int64, message string) error
	MarkFailed(ctx context.Context, deviceID int64, message string) error
}

// Runner 管理进行中的校验任务。
// 零值不可用，必须经 NewRunner 构造。
type Runner struct {
	ctx    context.Context
	store  StatusWriter
	logger *slog.Logger

	// interval 与 maxWait 由常量初始化，仅测试中压缩。
	interval time.Duration
	maxWait  time.Duration

	mu     sync.Mutex
	active map[int64]struct{}
}

// NewRunner 构造 Runner。ctx 取消时，进行中的轮询会静默退出、不写状态，
// 交由下次启动的 MarkInterrupted 清理。
func NewRunner(ctx context.Context, st StatusWriter, logger *slog.Logger) *Runner {
	return &Runner{
		ctx:      ctx,
		store:    st,
		logger:   logger,
		interval: Interval,
		maxWait:  MaxWait,
		active:   make(map[int64]struct{}),
	}
}

// Start 启动一轮校验，立即返回。
// 该设备已有任务时返回 ErrInProgress，且不产生任何副作用。
func (r *Runner) Start(deviceID int64, probeType, probeValue string) error {
	r.mu.Lock()
	if _, running := r.active[deviceID]; running {
		r.mu.Unlock()
		return ErrInProgress
	}
	r.active[deviceID] = struct{}{}
	r.mu.Unlock()

	// 先同步写入「校验中」再起 goroutine：
	// 调用方拿到 202 后立刻查询，必须读到 checking，而不是上一轮的旧终态。
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := r.store.MarkChecking(writeCtx, deviceID)
	cancel()
	if err != nil {
		r.release(deviceID)
		return fmt.Errorf("写入校验中状态失败: %w", err)
	}

	go r.loop(deviceID, probeType, probeValue)
	return nil
}

// loop 跑完一轮校验，必然以某个终态结束（或进程退出）。
func (r *Runner) loop(deviceID int64, probeType, probeValue string) {
	defer r.release(deviceID)

	deadline := time.Now().Add(r.maxWait)
	for {
		// 到点即判定超时，不再发起新的探测，保证总时长不超过上限。
		if !time.Now().Before(deadline) {
			r.timeout(deviceID)
			return
		}

		err := r.probeOnce(probeType, probeValue)
		if r.ctx.Err() != nil {
			// 进程正在退出：不写状态，标记为 checking 的残留在下次启动时清理。
			return
		}

		switch {
		case err == nil:
			r.write(deviceID, "online", func(ctx context.Context) error {
				return r.store.MarkOnline(ctx, deviceID)
			})
			return
		case errors.Is(err, ErrToolMissing):
			r.logger.Warn("校验无法进行，探测命令缺失", "id", deviceID, "err", err)
			r.write(deviceID, "failed", func(ctx context.Context) error {
				return r.store.MarkFailed(ctx, deviceID, fmt.Sprintf("探测命令 %s 不存在，无法校验", pingBinary))
			})
			return
		case errors.Is(err, ErrInvalidTarget):
			r.logger.Warn("校验无法进行，目标配置非法", "id", deviceID, "err", err)
			r.write(deviceID, "failed", func(ctx context.Context) error {
				return r.store.MarkFailed(ctx, deviceID, "校验目标非法，请检查设备的校验设置")
			})
			return
		}

		// 本轮未响应：等到下一轮。最后一轮只睡到截止时刻，不超时多跑。
		wait := r.interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait > 0 {
			select {
			case <-r.ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

func (r *Runner) probeOnce(probeType, probeValue string) error {
	ctx, cancel := context.WithTimeout(r.ctx, AttemptTimeout)
	defer cancel()
	return Probe(ctx, probeType, probeValue)
}

func (r *Runner) timeout(deviceID int64) {
	r.write(deviceID, "timeout", func(ctx context.Context) error {
		return r.store.MarkTimeout(ctx, deviceID,
			fmt.Sprintf("%d 秒内未探测到设备上线", int(r.maxWait.Seconds())))
	})
}

// write 用独立超时写终态：即便服务正在关闭，也要尽量把结论落盘。
// 写失败只记日志，不改变已判定的事实。
func (r *Runner) write(deviceID int64, status string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fn(ctx); err != nil {
		r.logger.Error("写入校验结果失败", "id", deviceID, "status", status, "err", err)
	}
}

func (r *Runner) release(deviceID int64) {
	r.mu.Lock()
	delete(r.active, deviceID)
	r.mu.Unlock()
}

// Running 报告该设备是否正在校验，供测试与排障使用。
func (r *Runner) Running(deviceID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[deviceID]
	return ok
}
