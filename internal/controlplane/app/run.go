package app

import (
	"context"
	"crypto/tls"
	"database/sql"
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

	_ "github.com/lib/pq"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/controlplane/console"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/health"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/nickZFZ/Sydom/internal/secheaders"
	"github.com/nickZFZ/Sydom/internal/tlsconfig"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Run 装配并运行控制面，阻塞至 ctx 取消后优雅关闭。adminLis/syncLis/restLis/consoleLis 由调用方注入（测试用 :0）。
// restLis 为 nil 时不起 REST 监听器，consoleLis 为 nil 时不起 Console 监听器，均向后兼容。
func Run(ctx context.Context, cfg Config, adminLis, syncLis, restLis, consoleLis net.Listener, logger *slog.Logger) error {
	db, err := sql.Open("postgres", cfg.DatabaseDSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}

	appResolver, err := secret.NewResolver(db, cfg.MasterKey) // PolicySync：解 app 凭据
	if err != nil {
		return fmt.Errorf("app resolver: %w", err)
	}
	// 先播种 root（含 bump version），再构造 enforcer——使 enforcer 加载到 root 的 super-admin 绑定。
	if err := adminauthz.EnsureRootOperator(ctx, db, cfg.MasterKey, cfg.RootPrincipal, cfg.RootSecret); err != nil {
		return fmt.Errorf("ensure root operator: %w", err)
	}
	operatorResolver, err := adminauthz.NewOperatorResolver(db, cfg.MasterKey) // AdminService：解 operator 凭据
	if err != nil {
		return fmt.Errorf("operator resolver: %w", err)
	}
	enforcer, err := adminauthz.NewEnforcer(db)
	if err != nil {
		return fmt.Errorf("admin enforcer: %w", err)
	}

	srvTLS, err := tlsconfig.Server(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return fmt.Errorf("server tls: %w", err) // fail-close：半配置/证书不可读即拒绝启动
	}
	// M5.2b：仅 policysync（机对机策略同步）通道派生要求客户端证书的 mTLS 变体；
	// admin/REST/Console 继续用共享 srvTLS（人面不破）。sync_client_ca_file 空 → syncTLS==srvTLS，行为不变。
	syncTLS, err := tlsconfig.MutualServer(srvTLS, cfg.SyncClientCAFile)
	if err != nil {
		return fmt.Errorf("policysync mtls: %w", err) // fail-close：CA 设但无服务端 TLS / 无效 PEM 即拒绝启动
	}
	// baseOpts：admin/policysync 共享的非传输层 server option（拦截器在各 NewGRPCServer 内追加，
	// 当前为空）。两通道各自在此之上追加自己的传输凭据——唯一差异即用哪份 *tls.Config；
	// 绝不从头重建 opts，故任何将来加到 baseOpts 的共享 option 两通道都继承、不会被静默丢弃。
	var baseOpts []grpc.ServerOption
	grpcOpts := append(baseOpts[:len(baseOpts):len(baseOpts)], serverCreds(srvTLS)...)
	syncGrpcOpts := append(baseOpts[:len(baseOpts):len(baseOpts)], serverCreds(syncTLS)...)
	if srvTLS != nil {
		logger.Info("control plane TLS enabled")
	}
	if cfg.SyncClientCAFile != "" {
		logger.Info("policysync mTLS enabled (client cert required)")
	}

	m := obs.New() // 可观测性基座：RED 指标 + 访问日志 + /metrics（fail-open，绝不阻断主路径）

	// secure：部署已声明 HTTPS——复用 Console cookie 的同一信号（语义一致「部署在 HTTPS 后」）。
	// 门控 HSTS 下发（明文部署绝不发，防浏览器强制 HTTPS 锁死本地访问）。不新增 flag（YAGNI）。
	secure := !cfg.ConsoleCookieInsecure

	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	adminSrv := mgmt.NewAdminServer(db, mgr, cfg.MasterKey)
	grpcSrv := mgmt.NewGRPCServer(adminSrv, operatorResolver, enforcer, db, logger, m, grpcOpts...)
	syncCore := policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval}, mgr)
	syncSrv := policysync.NewGRPCServer(syncCore, appResolver, m, syncGrpcOpts...)
	pub := broadcast.NewRedisPublisher(rdb)
	sub := broadcast.NewRedisSubscriber(rdb)

	logger.Info("control plane starting",
		"admin_addr", adminLis.Addr().String(),
		"sync_addr", syncLis.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
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
	launch("admin-serve", func() error { return grpcSrv.Serve(adminLis) })
	launch("sync-serve", func() error { return syncSrv.Serve(syncLis) })
	launch("relay", func() error { return outbox.RunRelayLoop(runCtx, db, pub, cfg.RelayPollInterval) })
	launch("dispatch", func() error { return syncCore.RunDispatchLoop(runCtx, sub) })

	var restSrv *http.Server
	if restLis != nil {
		if srvTLS != nil {
			restLis = tls.NewListener(restLis, srvTLS)
		}
		restSrv = &http.Server{Handler: secheaders.API(secure)(m.HTTPMiddleware(logger, restgw.NewHandler(adminSrv, operatorResolver, enforcer, db, logger)))}
		logger.Info("control plane REST enabled", "rest_addr", restLis.Addr().String())
		launch("rest-serve", func() error {
			if e := restSrv.Serve(restLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}

	var consoleSrv *http.Server
	if consoleLis != nil {
		if srvTLS != nil {
			consoleLis = tls.NewListener(consoleLis, srvTLS)
		}
		store := console.NewRedisStore(rdb, cfg.ConsoleSessionTTL)
		consoleSrv = &http.Server{Handler: secheaders.Console(secure)(m.HTTPMiddleware(logger, console.NewHandler(
			adminSrv, operatorResolver, enforcer, db, store, logger, secure)))}
		logger.Info("control plane Console enabled", "console_addr", consoleLis.Addr().String())
		launch("console-serve", func() error {
			if e := consoleSrv.Serve(consoleLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}

	var healthSrv *http.Server
	if cfg.HealthAddr != "" {
		healthLis, lerr := net.Listen("tcp", cfg.HealthAddr)
		if lerr != nil {
			return fmt.Errorf("listen health: %w", lerr)
		}
		healthSrv = &http.Server{Handler: obs.OpsHandler(m, cpReadiness(db, rdb))} // /metrics + /healthz + /readyz
		logger.Info("control plane health enabled", "health_addr", cfg.HealthAddr)
		launch("health-serve", func() error {
			if e := healthSrv.Serve(healthLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}

	<-runCtx.Done()
	logger.Info("control plane shutting down")
	if restSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = restSrv.Shutdown(shutdownCtx)
	}
	if consoleSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = consoleSrv.Shutdown(shutdownCtx)
	}
	if healthSrv != nil {
		shutdownCtx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}
	grpcSrv.GracefulStop()
	syncSrv.GracefulStop()
	wg.Wait()
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
	adminLis, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		logger.Error("listen admin", "err", err)
		return 1
	}
	syncLis, err := net.Listen("tcp", cfg.SyncAddr)
	if err != nil {
		logger.Error("listen sync", "err", err)
		return 1
	}
	var restLis net.Listener
	if cfg.RESTAddr != "" {
		restLis, err = net.Listen("tcp", cfg.RESTAddr)
		if err != nil {
			logger.Error("listen rest", "err", err)
			return 1
		}
	}
	var consoleLis net.Listener
	if cfg.ConsoleAddr != "" {
		consoleLis, err = net.Listen("tcp", cfg.ConsoleAddr)
		if err != nil {
			logger.Error("listen console", "err", err)
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, cfg, adminLis, syncLis, restLis, consoleLis, logger); err != nil {
		logger.Error("run", "err", err)
		return 1
	}
	return 0
}

// serverCreds 把可选的 *tls.Config 包成 gRPC 传输凭据 server option 切片：
// nil（明文）→ 空切片，使调用方 append 后 opts 不含传输凭据。
func serverCreds(cfg *tls.Config) []grpc.ServerOption {
	if cfg == nil {
		return nil
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(cfg))}
}

// cpReadiness 构造控制面就绪 checker：DB Ping + Redis Ping 皆通即就绪（fail-close）。
func cpReadiness(db *sql.DB, rdb *redis.Client) health.Checker {
	return func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			return err
		}
		return rdb.Ping(pingCtx).Err()
	}
}
