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

	"github.com/nickZFZ/Sydom/internal/health"
	"github.com/nickZFZ/Sydom/internal/sidecar/authz"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
	"github.com/nickZFZ/Sydom/internal/tlsconfig"
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
	scCfg, err := buildSyncConfig(cfg)
	if err != nil {
		return err
	}
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

	var healthSrv *http.Server
	if cfg.HealthAddr != "" {
		healthLis, lerr := net.Listen("tcp", cfg.HealthAddr)
		if lerr != nil {
			return fmt.Errorf("listen health: %w", lerr)
		}
		healthSrv = &http.Server{Handler: health.Handler(func(ctx context.Context) error { return authzr.Ready() })}
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
		cliTLS, err := tlsconfig.Client(cfg.ControlPlaneCAFile)
		if err != nil {
			return syncclient.Config{}, fmt.Errorf("control plane client tls: %w", err)
		}
		sc.Secure = true
		sc.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(cliTLS))}
	}
	return sc, nil
}
