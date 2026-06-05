# 司域 · 控制面进程装配 (cmd/sydom-controlplane) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把已实现的控制面库层装配成可执行二进制 `cmd/sydom-controlplane`：YAML+env 配置、连 DB/Redis、幂等播种 root operator、起 AdminService 与 PolicySync 两个 gRPC 服务、跑 relay/dispatch 后台协程、信号优雅关闭。

**架构：** 薄 `main` + 可测 `internal/controlplane/app` 包。`Run(ctx, cfg, adminLis, syncLis, logger) error` 接受注入的监听器（测试用 `:0`、main 用配置地址），装配全部组件、用 `sync.WaitGroup` 协调 4 个协程、`<-ctx.Done()` 后 `GracefulStop`。fail-close 启动：配置无效/连接失败/root 播种失败即非零退出。

**技术栈：** Go 1.26、`gopkg.in/yaml.v3`（go.mod 已含 indirect，提为 direct）、`lib/pq`、`go-redis/v9`、`log/slog`（stdlib）、testcontainers（集成测试）。零新模块。

---

## 关键事实（动手前必读，均已回源核实）

**装配链签名：**
- `sql.Open("postgres", dsn)`（驱动 `_ "github.com/lib/pq"`）；`db.PingContext(ctx)`。
- `redis.NewClient(&redis.Options{Addr})`；`rdb.Ping(ctx).Err()`。
- `secret.NewResolver(db, masterKey) (*Resolver, error)` —— **PolicySync 用**（解 app 凭据）。
- `adminauthz.NewOperatorResolver(db, masterKey) (*OperatorResolver, error)` —— **AdminService 用**（解 operator 凭据）。**两个 resolver 不同主体，绝不可复用同一个。**
- `adminauthz.EnsureRootOperator(ctx, db, masterKey, principal string, secret []byte) error`（幂等：已存在返 nil；否则建 operator + 绑 super-admin 域 `*` + bump version）。**必须在 `NewEnforcer` 之前调**——否则 enforcer 构造期加载不到 root 的 super-admin 绑定，root 调 RPC 会被元-RBAC 拒。
- `adminauthz.NewEnforcer(db) (*Enforcer, error)`（构造期从 DB 加载策略）。
- `policy.NewPolicyManager(db, outbox.NewSink()) *PolicyManager`（sink 写事务内落 outbox）。
- `mgmt.NewAdminServer(db, mgr, masterKey) *AdminServer`；`mgmt.NewGRPCServer(srv, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db) *grpc.Server`。
- `policysync.NewServer(db, policysync.Config{HeartbeatInterval}) *Server`；`policysync.NewGRPCServer(srv, res auth.SecretResolver) *grpc.Server`；`(*Server).RunDispatchLoop(ctx, sub) error`。
- `outbox.RunRelayLoop(ctx, db, pub, poll) error`；`broadcast.NewRedisPublisher(rdb)` / `NewRedisSubscriber(rdb)`。
- **`RunRelayLoop` 与 `RunDispatchLoop` 在 ctx 取消时都返回 `context.Canceled`** —— 优雅关闭的预期值，错误收集时必须 `errors.Is(err, context.Canceled)` 过滤。
- `grpc.Server.Serve(lis)` 在 `GracefulStop()` 后返回 `nil`。

**测试事实：**
- `dbtest.StartPostgres(t) string`（返 DSN，无迁移）；`dbtest.SetupSchema(t) *sql.DB`（起容器+迁移，**不暴露 DSN**）；`dbtest.StartRedis(t) string`（返 addr）。Run 要按 DSN 自开连接池，故需新增 `dbtest.MigratedDSN(t) string`（Task 2）。
- `crypto.KeySize == 32`。
- `auth.NewPerRPCCredentials(principal, secret []byte, secure bool)`。
- `adminv1.NewAdminServiceClient(conn)`；`ListApplicationsRequest{}`（空）；root（super-admin）可调，迁移后无 application 时返回空列表 + nil error。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/controlplane/app/config.go` | `Config` + `LoadConfig(path, getenv)`（YAML + env 覆盖密钥 + 校验） |
| `internal/controlplane/app/config_test.go` | `LoadConfig` 纯单测（无 Docker） |
| `internal/controlplane/app/run.go` | `Run(ctx, cfg, adminLis, syncLis, logger) error` + `Main() int` |
| `internal/controlplane/app/run_test.go` | `Run` 集成测试（testcontainers PG+Redis） |
| `internal/dbtest/dbtest.go`（改） | 新增 `MigratedDSN(t) string` |
| `cmd/sydom-controlplane/main.go` | 极薄：`os.Exit(app.Main())` |

---

## 任务 1：Config + LoadConfig

**文件：**
- 创建：`internal/controlplane/app/config.go`
- 测试：`internal/controlplane/app/config_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/app/config_test.go`：

```go
package app_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"SYDOM_MASTER_KEY":  base64.StdEncoding.EncodeToString(make([]byte, crypto.KeySize)),
		"SYDOM_ROOT_SECRET": "root-secret",
	}
}

const fullYAML = `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
heartbeat_interval: "10s"
relay_poll_interval: "2s"
`

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "postgres://localhost/sydom", cfg.DatabaseDSN)
	require.Equal(t, ":8081", cfg.AdminAddr)
	require.Equal(t, ":8082", cfg.SyncAddr)
	require.Equal(t, "root@sydom", cfg.RootPrincipal)
	require.Equal(t, 10*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, 2*time.Second, cfg.RelayPollInterval)
	require.Len(t, cfg.MasterKey, crypto.KeySize)
	require.Equal(t, []byte("root-secret"), cfg.RootSecret)
}

func TestLoadConfig_EnvOverridesDSN(t *testing.T) {
	env := validEnv()
	env["SYDOM_DATABASE_DSN"] = "postgres://override/db"
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, "postgres://override/db", cfg.DatabaseDSN)
}

func TestLoadConfig_MasterKeyWrongSize(t *testing.T) {
	env := validEnv()
	env["SYDOM_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingMasterKey(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_MASTER_KEY")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRootSecret(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_ROOT_SECRET")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRequiredAddr(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
sync_addr: ":8082"
root_principal: "root@sydom"
` // 缺 admin_addr
	_, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_IntervalDefaults(t *testing.T) {
	yaml := `
database_dsn: "postgres://localhost/sydom"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
` // 无间隔
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
	require.Equal(t, time.Second, cfg.RelayPollInterval)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/app/ -run TestLoadConfig`
预期：编译失败 `undefined: app.LoadConfig`（包尚不存在）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/app/config.go`：

```go
// Package app 装配控制面进程：加载配置、连 DB/Redis、起 AdminService/PolicySync、跑 relay/dispatch、优雅关闭。
package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nickZFZ/Sydom/internal/crypto"
	"gopkg.in/yaml.v3"
)

// Config 是控制面进程运行参数。非敏感项来自 YAML，敏感项（MasterKey/RootSecret）只来自 env。
type Config struct {
	DatabaseDSN       string
	RedisAddr         string
	AdminAddr         string
	SyncAddr          string
	RootPrincipal     string
	HeartbeatInterval time.Duration
	RelayPollInterval time.Duration

	MasterKey  []byte // env SYDOM_MASTER_KEY（base64，解码须 32 字节）
	RootSecret []byte // env SYDOM_ROOT_SECRET（原始字节）
}

type fileConfig struct {
	DatabaseDSN       string `yaml:"database_dsn"`
	RedisAddr         string `yaml:"redis_addr"`
	AdminAddr         string `yaml:"admin_addr"`
	SyncAddr          string `yaml:"sync_addr"`
	RootPrincipal     string `yaml:"root_principal"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
	RelayPollInterval string `yaml:"relay_poll_interval"`
}

// LoadConfig 读 YAML + env 覆盖密钥/可选项 + 校验（任一不满足 fail-close 返错）。
// getenv 注入便于测试（生产传 os.Getenv）。
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg := Config{
		DatabaseDSN:   firstNonEmpty(getenv("SYDOM_DATABASE_DSN"), fc.DatabaseDSN),
		RedisAddr:     firstNonEmpty(getenv("SYDOM_REDIS_ADDR"), fc.RedisAddr),
		AdminAddr:     fc.AdminAddr,
		SyncAddr:      fc.SyncAddr,
		RootPrincipal: fc.RootPrincipal,
	}
	if cfg.HeartbeatInterval, err = parseDurationDefault(fc.HeartbeatInterval, 30*time.Second); err != nil {
		return Config{}, fmt.Errorf("heartbeat_interval: %w", err)
	}
	if cfg.RelayPollInterval, err = parseDurationDefault(fc.RelayPollInterval, time.Second); err != nil {
		return Config{}, fmt.Errorf("relay_poll_interval: %w", err)
	}

	mk, err := base64.StdEncoding.DecodeString(getenv("SYDOM_MASTER_KEY"))
	if err != nil {
		return Config{}, fmt.Errorf("decode SYDOM_MASTER_KEY: %w", err)
	}
	cfg.MasterKey = mk
	cfg.RootSecret = []byte(getenv("SYDOM_ROOT_SECRET"))

	if len(cfg.MasterKey) != crypto.KeySize {
		return Config{}, fmt.Errorf("SYDOM_MASTER_KEY must decode to %d bytes, got %d", crypto.KeySize, len(cfg.MasterKey))
	}
	if len(cfg.RootSecret) == 0 {
		return Config{}, errors.New("SYDOM_ROOT_SECRET required")
	}
	for _, f := range []struct{ name, val string }{
		{"database_dsn", cfg.DatabaseDSN},
		{"redis_addr", cfg.RedisAddr},
		{"admin_addr", cfg.AdminAddr},
		{"sync_addr", cfg.SyncAddr},
		{"root_principal", cfg.RootPrincipal},
	} {
		if f.val == "" {
			return Config{}, fmt.Errorf("%s required", f.name)
		}
	}
	return cfg, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func parseDurationDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	return time.ParseDuration(s)
}
```

- [ ] **步骤 4：把 yaml.v3 提为直接依赖并验证通过**

运行：`go mod tidy && go test ./internal/controlplane/app/ -run TestLoadConfig -v`
预期：tidy 把 `gopkg.in/yaml.v3` 的 `// indirect` 去掉（无新模块下载）；7 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/config.go internal/controlplane/app/config_test.go go.mod go.sum
git commit -m "feat(controlplane/app): Config + LoadConfig（YAML+env，cmd 装配任务 1/5）"
```

---

## 任务 2：dbtest.MigratedDSN 测试 helper

**文件：**
- 修改：`internal/dbtest/dbtest.go`（在 `SetupSchema` 之后新增）

`SetupSchema` 起容器+迁移但只返回 `*sql.DB`、不暴露 DSN；Run 集成测试要按 DSN 自开连接池，故加一个返回「已迁移 DSN」的姊妹 helper，复用 `SetupSchema` 同款内部（`db.RunMigrations`/`migrationsSource`）。纯测试基建（additive，不改 `SetupSchema`），由 Task 3 真正驱动。

- [ ] **步骤 1：新增 MigratedDSN**

在 `internal/dbtest/dbtest.go` 的 `SetupSchema` 函数之后追加：

```go
// MigratedDSN 起 PG 容器、跑全量迁移，返回 DSN（供需按 DSN 自开连接池的被测代码，如 cmd 装配）。
func MigratedDSN(t *testing.T) string {
	t.Helper()
	dsn := StartPostgres(t)
	require.NoError(t, db.RunMigrations(dsn, migrationsSource()))
	return dsn
}
```

- [ ] **步骤 2：验证编译/vet 通过**

运行：`go vet ./internal/dbtest/`
预期：无输出（`MigratedDSN` 引用的 `StartPostgres`/`db.RunMigrations`/`migrationsSource` 均同包已有符号）。

- [ ] **步骤 3：Commit**

```bash
git add internal/dbtest/dbtest.go
git commit -m "test(dbtest): MigratedDSN 暴露已迁移库 DSN（cmd 装配任务 2/5）"
```

---

## 任务 3：Run + Main 装配 + 集成测试

**文件：**
- 创建：`internal/controlplane/app/run.go`
- 测试：`internal/controlplane/app/run_test.go`

Run 是单体装配函数，由一个 testcontainers 集成测试驱动（验证装配链贯通 + 优雅关闭），不拆微步。`Main()` 为薄 glue（flag/signal/listener/Run），随 run.go 一并实现、由二进制构建（Task 4）兜底。

- [ ] **步骤 1：编写失败的集成测试**

`internal/controlplane/app/run_test.go`：

```go
package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRun_WiringEndToEnd(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)

	mk := make([]byte, crypto.KeySize)
	for i := range mk {
		mk[i] = 0x2a
	}
	rootSecret := []byte("root-secret")
	cfg := app.Config{
		DatabaseDSN:       dsn,
		RedisAddr:         redisAddr,
		RootPrincipal:     "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond,
		RelayPollInterval: 20 * time.Millisecond,
		MasterKey:         mk,
		RootSecret:        rootSecret,
	}

	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, logger) }()

	conn, err := grpc.NewClient(adminLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(cfg.RootPrincipal, rootSecret, false)))
	require.NoError(t, err)
	defer conn.Close()
	cli := adminv1.NewAdminServiceClient(conn)

	// 装配链贯通：root 凭据经 operator 认证 + 元-RBAC 调 ListApplications 成功。
	require.Eventually(t, func() bool {
		_, err := cli.ListApplications(context.Background(), &adminv1.ListApplicationsRequest{})
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "装配后 root 应能调通 AdminService")

	// 优雅关闭：取消 ctx，Run 在超时内干净返回 nil。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回")
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/app/ -run TestRun_WiringEndToEnd`
预期：编译失败 `undefined: app.Run`。

- [ ] **步骤 3：编写实现**

`internal/controlplane/app/run.go`：

```go
package app

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// Run 装配并运行控制面，阻塞至 ctx 取消后优雅关闭。adminLis/syncLis 由调用方注入（测试用 :0）。
func Run(ctx context.Context, cfg Config, adminLis, syncLis net.Listener, logger *slog.Logger) error {
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
	adminSrv := mgmt.NewGRPCServer(mgmt.NewAdminServer(db, mgr, cfg.MasterKey), operatorResolver, enforcer, db)
	syncCore := policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval})
	syncSrv := policysync.NewGRPCServer(syncCore, appResolver)
	pub := broadcast.NewRedisPublisher(rdb)
	sub := broadcast.NewRedisSubscriber(rdb)

	logger.Info("control plane starting",
		"admin_addr", adminLis.Addr().String(),
		"sync_addr", syncLis.Addr().String())

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
	launch("admin-serve", func() error { return adminSrv.Serve(adminLis) })
	launch("sync-serve", func() error { return syncSrv.Serve(syncLis) })
	launch("relay", func() error { return outbox.RunRelayLoop(runCtx, db, pub, cfg.RelayPollInterval) })
	launch("dispatch", func() error { return syncCore.RunDispatchLoop(runCtx, sub) })

	<-runCtx.Done()
	logger.Info("control plane shutting down")
	adminSrv.GracefulStop()
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, cfg, adminLis, syncLis, logger); err != nil {
		logger.Error("run", "err", err)
		return 1
	}
	return 0
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/app/ -run TestRun_WiringEndToEnd -v`（需 Docker：起 PG+Redis 容器）
预期：PASS —— ListApplications 经 root 凭据调通、ctx 取消后 Run 返回 nil。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/run.go internal/controlplane/app/run_test.go
git commit -m "feat(controlplane/app): Run 装配 + 优雅关闭 + 集成测试（cmd 装配任务 3/5）"
```

---

## 任务 4：cmd/sydom-controlplane 入口二进制

**文件：**
- 创建：`cmd/sydom-controlplane/main.go`

- [ ] **步骤 1：编写薄入口**

`cmd/sydom-controlplane/main.go`：

```go
package main

import (
	"os"

	"github.com/nickZFZ/Sydom/internal/controlplane/app"
)

func main() {
	os.Exit(app.Main())
}
```

- [ ] **步骤 2：验证二进制可构建**

运行：`go build -o /tmp/sydom-controlplane ./cmd/sydom-controlplane/ && echo BUILD_OK`
预期：输出 `BUILD_OK`（链接成功，产出可执行文件）。

- [ ] **步骤 3：验证配置缺失即非零退出（不连 DB）**

运行：`/tmp/sydom-controlplane -config /nonexistent.yaml; echo "exit=$?"`
预期：`exit=1`（LoadConfig 读不到文件 → fail-close，stderr 有 `load config` 日志）。

- [ ] **步骤 4：Commit**

```bash
git add cmd/sydom-controlplane/main.go
git commit -m "feat(cmd): sydom-controlplane 入口二进制（cmd 装配任务 4/5）"
```

---

## 任务 5：全量验证与收尾

**文件：** 无新增；全仓验证。

- [ ] **步骤 1：app 包集成测试（含 -race）**

运行：`go test -race ./internal/controlplane/app/...`（需 Docker）
预期：`ok`，无 DATA RACE（4 协程并发 + 关闭协调）。

- [ ] **步骤 2：vet + 全仓 build + 回归**

运行：`go vet ./internal/controlplane/app/... ./cmd/... && go build ./... && go test ./internal/controlplane/...`
预期：vet 无输出；build 成功（含 cmd）；控制面各包测试 `ok`。

- [ ] **步骤 3：补 config.yaml 示例（运维参考）**

创建 `cmd/sydom-controlplane/config.example.yaml`：

```yaml
database_dsn: "postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
heartbeat_interval: "30s"
relay_poll_interval: "1s"
# 敏感项只走环境变量（不写本文件）：
#   SYDOM_MASTER_KEY  = base64(32 字节 AES-256 主密钥)
#   SYDOM_ROOT_SECRET = root operator 初始 HMAC 密钥
# DB 须先 make migrate-up
```

```bash
git add cmd/sydom-controlplane/config.example.yaml
git commit -m "docs(cmd): 控制面配置示例 + 启动前置说明（cmd 装配任务 5/5）"
```

- [ ] **步骤 4：收尾**

调用 finishing-a-development-branch 技能决定集成方式（合并/PR/清理）。
更新进度记忆 `project_detailed_design_progress.md`：cmd/sydom-controlplane 已实现；下一步 cmd/sydom-sidecar（数据面二进制）。

---

## 自检结果

**1. 规格覆盖度**（对照 spec 各节）：
- §2 决策 1（先控制面 cmd）→ 全计划。✅
- §2 决策 2 + §4 配置（YAML+env 密钥）→ Task 1 `config.go` + 7 个 LoadConfig 单测（覆盖解析/env 覆盖/主密钥长度/缺失/地址缺失/间隔默认）。✅
- §2 决策 3 + §5 两端口/两 resolver → Task 3 `Run`（`operatorResolver` 给 admin、`appResolver` 给 sync、两监听器）。✅
- §2 决策 4 + §3（薄 main + 可测 app + 注入监听器）→ Task 3（Run 接监听器）+ Task 4（薄 main）。✅
- §2 决策 5（DB 假定预迁移）→ Task 5 config 示例注释 + 不含自动迁移。✅
- §2 决策 6 + §4 校验（fail-close 启动）→ Task 1 校验单测 + Task 3 各 `return fmt.Errorf` + Task 4 步骤 3（缺配置非零退出）。✅
- §5 装配数据流（含 EnsureRootOperator 先于 NewEnforcer）→ Task 3 `Run` 实现按序。✅
- §6 优雅关闭（WaitGroup + context.Canceled 过滤 + GracefulStop）→ Task 3 `Run` 的 launch/关闭段 + 集成测试关闭断言。✅
- §7 日志（slog）→ Task 3 `Run`/`Main` 的 logger。✅
- §8 测试（LoadConfig 单测 + Run 集成 + MigratedDSN）→ Task 1 + Task 2 + Task 3。✅
- §9 不在范围（sidecar/迁移/可观测扩展）→ 未触及。✅

**2. 占位符扫描**：无 TODO/待定；每个代码步骤完整可编译，命令/预期具体。Main() 不单测是经论证取舍（薄 glue，由二进制构建 + 缺配置退出码兜底），非占位。✅

**3. 类型一致性**：`Config`/`LoadConfig`（Task 1 定义，Task 3 `Run`/`Main` 消费）；`MigratedDSN`（Task 2 定义，Task 3 测试消费）；`Run(ctx,cfg,adminLis,syncLis,logger)`/`Main()`（Task 3 定义，Task 4 `main` 消费）。装配引用的 `secret.NewResolver`/`adminauthz.NewOperatorResolver`/`NewEnforcer`/`EnsureRootOperator`/`policy.NewPolicyManager`/`outbox.NewSink`/`mgmt.NewAdminServer`/`mgmt.NewGRPCServer`/`policysync.NewServer`/`NewGRPCServer`/`RunDispatchLoop`/`outbox.RunRelayLoop`/`broadcast.NewRedisPublisher`/`NewRedisSubscriber` 均已回源核实签名；`RunRelayLoop`/`RunDispatchLoop` 返回 `context.Canceled` 已在 `launch` 过滤。✅
