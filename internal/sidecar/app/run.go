package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/nickZFZ/Sydom/internal/sidecar/authz"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
)

// Run 装配并运行 Sidecar，阻塞至 ctx 取消后优雅关闭。authLis 由调用方注入（测试用 :0）。
func Run(ctx context.Context, cfg Config, authLis net.Listener, logger *slog.Logger) error {
	table := dataperm.NewTable()
	engine, err := kernel.New(cfg.Domain, nil, table) // table 作 DataPolicyApplier；nil→内建 1024 LRU
	if err != nil {
		return fmt.Errorf("new engine: %w", err)
	}
	filter := dataperm.NewFilter(engine, table)
	// 变量名用 syncCli（不可叫 sync）——否则遮蔽下方 sync.WaitGroup 的 sync 包。
	syncCli, err := syncclient.New(syncclient.Config{
		Endpoint:       cfg.ControlPlaneAddr,
		AppID:          cfg.AppKey,
		Secret:         cfg.Secret,
		Secure:         false,
		BackoffInitial: cfg.BackoffInitial,
		BackoffMax:     cfg.BackoffMax,
	}, engine)
	if err != nil {
		return fmt.Errorf("new sync client: %w", err)
	}
	authzr := authz.New(engine, filter, syncCli, authz.Config{MaxStaleness: cfg.MaxStaleness})
	authSrv := authz.NewGRPCServer(authzr)

	logger.Info("sidecar starting",
		"auth_addr", authLis.Addr().String(),
		"control_plane_addr", cfg.ControlPlaneAddr,
		"domain", cfg.Domain,
		"app_key", cfg.AppKey)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	launch := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel() // 任一协程结束 → 触发整体关闭（级联）
			if e := fn(); e != nil && !errors.Is(e, context.Canceled) {
				errCh <- fmt.Errorf("%s: %w", name, e)
			}
		}()
	}
	launch("auth-serve", func() error { return authSrv.Serve(authLis) })
	launch("sync", func() error { return syncCli.Run(runCtx) }) // ctx 取消返回 nil

	<-runCtx.Done()
	logger.Info("sidecar shutting down")
	authSrv.GracefulStop()
	wg.Wait()
	if e := syncCli.Close(); e != nil {
		logger.Warn("close sync client", "err", e)
	}
	close(errCh)
	if e, ok := <-errCh; ok {
		return e
	}
	return nil
}

// Main 是进程入口逻辑：解析 -config、装信号 ctx、建监听器、调 Run，返回退出码。
func Main() int {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := LoadConfig(*configPath, os.Getenv)
	if err != nil {
		logger.Error("load config", "err", err)
		return 1
	}
	authLis, err := net.Listen("tcp", cfg.AuthAddr)
	if err != nil {
		logger.Error("listen auth", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, cfg, authLis, logger); err != nil {
		logger.Error("run", "err", err)
		return 1
	}
	return 0
}
