// Command wold 是局域网唤醒系统的服务端进程入口。
// 职责仅限：加载配置 → 初始化存储 → 启动 HTTP 服务 → 优雅退出。
// 业务逻辑一律放在 internal/ 下的对应包里。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"wol/internal/config"
	"wol/internal/httpapi"
	"wol/internal/probe"
	"wol/internal/store"
	"wol/internal/wol"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DataDir, "wol.db")
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return err
	}

	// 上次进程可能在轮询中途退出，此处把残留的「校验中」重置为「待校验」（3-4）。
	if n, err := db.MarkInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		logger.Info("已重置上次中断的校验状态", "count", n)
	}

	waker := wol.NewService()
	runner := probe.NewRunner(ctx, db, logger)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           httpapi.New(db, db, waker, runner, db, logger, "web").Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务启动", "addr", srv.Addr, "data_dir", cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，开始优雅关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
