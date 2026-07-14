package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/nickZFZ/Sydom/internal/sidecar/authz"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
	"github.com/nickZFZ/Sydom/internal/tlsconfig"
)

// Run 装配并运行 Sidecar，阻塞至 ctx 取消后优雅关闭。authLis 由调用方注入（测试用 :0）。
func Run(ctx context.Context, cfg Config, authLis net.Listener, logger *slog.Logger) error {
	m := obs.New() // 可观测性基座：RED 指标 + 判定计数 + 缓存命中率 + /metrics（fail-open，绝不阻断主路径）

	table := dataperm.NewTable()
	// 注入指标 cache 装饰器（内建 1024 LRU 之外仅计命中/未命中，决策逻辑零改）。
	engine, err := kernel.New(cfg.Domain, obs.NewMetricsCache(kernel.NewBoundedCache(1024), m), table)
	if err != nil {
		return fmt.Errorf("new engine: %w", err)
	}
	filter := dataperm.NewFilter(engine, table)
	// 变量名用 syncCli（不可叫 sync）——否则遮蔽下方 sync.WaitGroup 的 sync 包。
	scCfg, err := buildSyncConfig(cfg)
	if err != nil {
		return err
	}
	scCfg.OnSnapshotApplied = m.SnapshotApplied // M5.1c: 全量快照 apply 计入 sydom_sidecar_snapshot_applied_total
	syncCli, err := syncclient.New(scCfg, engine)
	if err != nil {
		return fmt.Errorf("new sync client: %w", err)
	}
	authzr := authz.New(engine, filter, syncCli, authz.Config{MaxStaleness: cfg.MaxStaleness})
	srvTLS, err := tlsconfig.Server(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("server tls: %w", err)
	}
	var grpcOpts []grpc.ServerOption
	if srvTLS != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(srvTLS)))
		logger.Info("sidecar TLS enabled")
	}
	// 数据面 gRPC 无鉴权链，obs 拦截器经 opts 即最外层；decisionInterceptor 只读响应记判定计数（不改判定分发）。
	grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(m.UnaryServerInterceptor(logger), decisionInterceptor(m)))
	authSrv := authz.NewGRPCServer(authzr, syncCli, grpcOpts...)

	logger.Info("sidecar starting",
		"auth_addr", authLis.Addr().String(),
		"control_plane_addr", cfg.ControlPlaneAddr,
		"domain", cfg.Domain,
		"app_key", cfg.AppKey)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
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
	// connected gauge：轮询 syncCli 连接态刷 sydom_sidecar_connected（app 层非侵入，syncclient 内部零改；fail-open 不返 error）。
	launch("connected-gauge", func() error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		m.SetConnected(syncCli.Connected()) // 立即采一次，不等首个 tick
		for {
			select {
			case <-runCtx.Done():
				return nil
			case <-ticker.C:
				m.SetConnected(syncCli.Connected())
			}
		}
	})
	// TODO(M5.x): sydom_sidecar_snapshot_applied_total 待 syncclient 暴露快照事件 hook 后接入
	// （快照 apply 在 syncclient.Run 内部，无干净的 app 层 hook；不改 syncclient 内部逻辑）。

	var healthSrv *http.Server
	if cfg.HealthAddr != "" {
		healthLis, lerr := net.Listen("tcp", cfg.HealthAddr)
		if lerr != nil {
			return fmt.Errorf("listen health: %w", lerr)
		}
		healthSrv = &http.Server{Handler: obs.OpsHandler(m, func(ctx context.Context) error { return authzr.Ready() })} // /metrics + /healthz + /readyz
		logger.Info("sidecar health enabled", "health_addr", cfg.HealthAddr)
		launch("health-serve", func() error {
			if e := healthSrv.Serve(healthLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}

	<-runCtx.Done()
	logger.Info("sidecar shutting down")
	if healthSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}
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

// buildSyncConfig 由 app Config 组装 syncclient.Config；ControlPlaneTLS 开时
// 置 Secure=true 并注入 TLS 传输凭据——使 HMAC RequireTransportSecurity()=true，明文不可承载凭据。
func buildSyncConfig(cfg Config) (syncclient.Config, error) {
	sc := syncclient.Config{
		Endpoint:       cfg.ControlPlaneAddr,
		AppID:          cfg.AppKey,
		Secret:         cfg.Secret,
		Secure:         false,
		BackoffInitial: cfg.BackoffInitial,
		BackoffMax:     cfg.BackoffMax,
	}
	if cfg.ControlPlaneTLS {
		cliTLS, err := tlsconfig.MutualClient(cfg.ControlPlaneCAFile, cfg.ControlPlaneClientCertFile, cfg.ControlPlaneClientKeyFile)
		if err != nil {
			return syncclient.Config{}, fmt.Errorf("control plane client tls: %w", err)
		}
		sc.Secure = true
		sc.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(cliTLS))}
	}
	return sc, nil
}
