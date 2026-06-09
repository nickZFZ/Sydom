package app

import (
	"context"
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
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/redis/go-redis/v9"
)

// Run 装配并运行控制面，阻塞至 ctx 取消后优雅关闭。adminLis/syncLis/restLis 由调用方注入（测试用 :0）。
// restLis 为 nil 时不起 REST 监听器，向后兼容。
func Run(ctx context.Context, cfg Config, adminLis, syncLis, restLis net.Listener, logger *slog.Logger) error {
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

	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	adminSrv := mgmt.NewAdminServer(db, mgr, cfg.MasterKey)
	grpcSrv := mgmt.NewGRPCServer(adminSrv, operatorResolver, enforcer, db)
	syncCore := policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval}, mgr)
	syncSrv := policysync.NewGRPCServer(syncCore, appResolver)
	pub := broadcast.NewRedisPublisher(rdb)
	sub := broadcast.NewRedisSubscriber(rdb)

	logger.Info("control plane starting",
		"admin_addr", adminLis.Addr().String(),
		"sync_addr", syncLis.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 5)
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
		restSrv = &http.Server{Handler: restgw.NewHandler(adminSrv, operatorResolver, enforcer, db, logger)}
		logger.Info("control plane REST enabled", "rest_addr", restLis.Addr().String())
		launch("rest-serve", func() error {
			if e := restSrv.Serve(restLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, cfg, adminLis, syncLis, restLis, logger); err != nil {
		logger.Error("run", "err", err)
		return 1
	}
	return 0
}
