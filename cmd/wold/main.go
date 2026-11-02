// Command wold 是局域网唤醒系统的服务端进程入口。
// 职责仅限：加载配置 → 初始化存储 → 启动 HTTP 服务 → 优雅退出。
// 业务逻辑一律放在 internal/ 下的对应包里。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"wol/internal/auth"
	"wol/internal/config"
	"wol/internal/guac"
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

	authn, err := auth.New(cfg.AdminPassword, cfg.TokenTTL)
	if err != nil {
		return err
	}

	api := httpapi.New(db, db, waker, runner, db, db, authn, logger, "web")

	// M2 远程桌面：两项配置都为空表示不启用，接口会返回明确的 503 而不是 500。
	if cfg.GuacURL != "" {
		guacCfg, err := guac.ParseConfig(cfg.GuacURL, cfg.GuacSecret, 0)
		if err != nil {
			return fmt.Errorf("远程桌面配置有误: %w", err)
		}
		api.EnableRDP(guacCfg, cfg.RDPPort)
		// 只记地址与目标端口，密钥不进日志。
		logger.Info("远程桌面已启用", "guac_url", guacCfg.BaseURL, "rdp_port", cfg.RDPPort)
	} else {
		logger.Info("远程桌面未启用（未设置 WOL_GUAC_URL / WOL_GUAC_SECRET）")
	}

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务启动",
			"addr", srv.Addr,
			"data_dir", cfg.DataDir,
			"token_ttl", cfg.TokenTTL.String(), // 只记有效期，不记口令
		)
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
