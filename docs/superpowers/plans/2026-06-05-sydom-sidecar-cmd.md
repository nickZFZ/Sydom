# 司域 · Sidecar 进程装配 (cmd/sydom-sidecar) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把已实现的 sidecar 库层（④-1~④-4）装配成可执行二进制 `cmd/sydom-sidecar`：YAML+env 配置、构造 `Table→Engine→Filter→SyncClient→Authorizer`、起 SyncClient 对账协程、监听本地 AuthService（loopback TCP）、信号优雅关闭。

**架构：** 薄 `main` + 可测 `internal/sidecar/app` 包。`Run(ctx, cfg, authLis, logger) error` 接受注入的监听器（测试用 `:0`、main 用配置地址），装配全部组件、用 `sync.WaitGroup` 协调 2 个协程（sync 对账 + auth 服务）、`<-ctx.Done()` 后 `GracefulStop`。fail-close 启动：配置无效即非零退出。Sidecar **不连 DB/Redis**，唯一外连是 gRPC 拨控制面 PolicySync。

**技术栈：** Go 1.26、`gopkg.in/yaml.v3`（控制面 cmd 已提为 direct）、`google.golang.org/grpc`、`log/slog`（stdlib）。**零新模块**，无 Docker（集成测试用纯内存真实 TCP fake PolicySync）。

---

## 关键事实（动手前必读，均已回源核实）

**装配链签名：**
- `dataperm.NewTable() *Table`（实现 `kernel.DataPolicyApplier`）。
- `kernel.New(domain string, c cache.Cache, applier kernel.DataPolicyApplier) (*Engine, error)`——`c=nil` 内建容量 1024 的有界 LRU；`applier` 传 `table`（路由数据策略）。
- `dataperm.NewFilter(roles dataperm.RoleResolver, table *dataperm.Table) *Filter`——`*kernel.Engine` 满足 `RoleResolver`（有 `GetImplicitRolesForUser`）。
- `syncclient.New(cfg syncclient.Config, engine *kernel.Engine) (*SyncClient, error)`；`syncclient.Config{Endpoint, AppID string, Secret []byte, Secure bool, DialOptions []grpc.DialOption, BackoffInitial, BackoffMax time.Duration}`。
- `authz.New(engine *kernel.Engine, filter *dataperm.Filter, fresh authz.Freshness, cfg authz.Config) *Authorizer`——`*syncclient.SyncClient` 满足 `Freshness`（有 `Ready()`/`LastSyncAt()`）；pin 域取自 `engine.Domain()`。`authz.Config{MaxStaleness time.Duration}`。
- `authz.NewGRPCServer(a *Authorizer) *grpc.Server`——内部 `authv1.RegisterAuthServiceServer`。
- `(*SyncClient).Run(ctx) error` —— **ctx 取消时返回 `nil`**（≠ 控制面 relay/dispatch 的 `context.Canceled`）；`(*SyncClient).Close() error`。
- `(*grpc.Server).Serve(lis)` 在 `GracefulStop()` 后返回 `nil`。

**命名陷阱：** `run.go` 同时用 stdlib `sync`（`sync.WaitGroup`）与 syncclient 实例——实例变量**必须**命名为 `syncCli`（不可叫 `sync`，否则遮蔽 `sync` 包）。

**配置语义（回源核实）：** `application` 表 `domain`（casbin 域）与 `app_key`（HMAC 标识+路由）是两个独立字段。`cfg.Domain → kernel.New 域`；`cfg.AppKey → syncclient.AppID`。错配 → 内核恒 `ErrForeignDomain` deny-all。HMAC `secret` 是原始字节（`auth.Sign(secret []byte,...)` 直接作 HMAC key），env `SYDOM_APP_SECRET` 按原始字节处理（镜像控制面 `SYDOM_ROOT_SECRET`）。

**proto 字段（回源核实，见 syncclient/authz 既有测试）：**
- `syncv1.Snapshot{Version uint64, Rules []*syncv1.PolicyRule, DataPolicies []*syncv1.DataPolicy}`；`syncv1.PolicyRule{Ptype string, Values []string}`；`syncv1.DataPolicy{Id uint64, SubjectType, SubjectId, Resource, Condition, Effect string}`。
- `authv1.CheckRequest{Subject, Object, Action string}` / `CheckResponse{Allowed bool}`；`authv1.FilterRequest{Subject, Resource string, Attrs *structpb.Struct}` / `FilterSQLResponse{Sql string, Args []*structpb.Value}`；客户端 `authv1.NewAuthServiceClient(conn)`。

**测试事实：**
- syncclient 既有 e2e 测试（`client_test.go` 的 `TestSyncClient_EndToEnd_DenyEffectReachesFilterSQL`）证明：快照含 g(alice→manager)+p(manager read order allow)+allow/deny 数据策略（域 `dom1`）→ `Check(alice,order,read)=true`、`FilterSQL(alice,order,{department:HR})` = `"(dept = ? AND NOT (status IN (?, ?)))"` / args `["HR","locked","void"]`。本计划集成测试复用同款快照，经 gRPC AuthService 验证。
- fake PolicySync 不装 HMAC 拦截器，任意 secret 均通过——无需 DB/Redis。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/sidecar/app/config.go` | `Config` + `LoadConfig(path, getenv)`（YAML + env 覆盖密钥 + 校验） |
| `internal/sidecar/app/config_test.go` | `LoadConfig` 纯单测（无 Docker） |
| `internal/sidecar/app/run.go` | `Run(ctx, cfg, authLis, logger) error` + `Main() int` |
| `internal/sidecar/app/run_test.go` | `Run` 集成测试（真实 TCP fake PolicySync，无 Docker） |
| `cmd/sydom-sidecar/main.go` | 极薄：`os.Exit(app.Main())` |
| `cmd/sydom-sidecar/config.example.yaml` | 运维参考配置示例 |

---

## 任务 1：Config + LoadConfig

**文件：**
- 创建：`internal/sidecar/app/config.go`
- 测试：`internal/sidecar/app/config_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/app/config_test.go`：

```go
package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/app"
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
	return map[string]string{"SYDOM_APP_SECRET": "app-secret"}
}

const fullYAML = `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "90s"
backoff_initial: "250ms"
backoff_max: "10s"
`

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, "localhost:8082", cfg.ControlPlaneAddr)
	require.Equal(t, "app-1", cfg.AppKey)
	require.Equal(t, "shop", cfg.Domain)
	require.Equal(t, "127.0.0.1:8090", cfg.AuthAddr)
	require.Equal(t, 90*time.Second, cfg.MaxStaleness)
	require.Equal(t, 250*time.Millisecond, cfg.BackoffInitial)
	require.Equal(t, 10*time.Second, cfg.BackoffMax)
	require.Equal(t, []byte("app-secret"), cfg.Secret)
}

func TestLoadConfig_EnvOverridesControlPlaneAddr(t *testing.T) {
	env := validEnv()
	env["SYDOM_CONTROL_PLANE_ADDR"] = "cp.internal:9000"
	cfg, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.NoError(t, err)
	require.Equal(t, "cp.internal:9000", cfg.ControlPlaneAddr)
}

func TestLoadConfig_MissingSecret(t *testing.T) {
	env := validEnv()
	delete(env, "SYDOM_APP_SECRET")
	_, err := app.LoadConfig(writeConfig(t, fullYAML), envFunc(env))
	require.Error(t, err)
}

func TestLoadConfig_MissingRequiredField(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
auth_addr: "127.0.0.1:8090"
` // 缺 domain
	_, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.Error(t, err)
}

func TestLoadConfig_Defaults(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
` // 无 max_staleness / 退避
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.MaxStaleness)
	require.Equal(t, 500*time.Millisecond, cfg.BackoffInitial)
	require.Equal(t, 30*time.Second, cfg.BackoffMax)
}

func TestLoadConfig_MaxStalenessZeroExplicit(t *testing.T) {
	yaml := `
control_plane_addr: "localhost:8082"
app_key: "app-1"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "0s"
`
	cfg, err := app.LoadConfig(writeConfig(t, yaml), envFunc(validEnv()))
	require.NoError(t, err)
	require.Equal(t, time.Duration(0), cfg.MaxStaleness)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/app/ -run TestLoadConfig`
预期：编译失败 `undefined: app.LoadConfig`（包尚不存在）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/app/config.go`：

```go
// Package app 装配 Sidecar 进程：加载配置、构造内核+数据权限+同步客户端+鉴权门面、
// 起对账协程、监听本地 AuthService、优雅关闭。
package app

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 Sidecar 进程运行参数。非敏感项来自 YAML，敏感项（Secret）只来自 env。
type Config struct {
	ControlPlaneAddr string        // 控制面 PolicySync 地址
	AppKey           string        // app_key：HMAC 认证标识 + 流路由（→ syncclient.AppID）
	Domain           string        // casbin 域（= application.domain，→ kernel.New 域）
	AuthAddr         string        // 本地 AuthService 监听地址（如 "127.0.0.1:8090"）
	MaxStaleness     time.Duration // 陈旧守卫上限（零值=关闭）
	BackoffInitial   time.Duration // syncclient 退避初值（零值用 500ms）
	BackoffMax       time.Duration // syncclient 退避上限（零值用 30s）

	Secret []byte // env SYDOM_APP_SECRET（HMAC 密钥，原始字节）
}

type fileConfig struct {
	ControlPlaneAddr string `yaml:"control_plane_addr"`
	AppKey           string `yaml:"app_key"`
	Domain           string `yaml:"domain"`
	AuthAddr         string `yaml:"auth_addr"`
	MaxStaleness     string `yaml:"max_staleness"`
	BackoffInitial   string `yaml:"backoff_initial"`
	BackoffMax       string `yaml:"backoff_max"`
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
		ControlPlaneAddr: firstNonEmpty(getenv("SYDOM_CONTROL_PLANE_ADDR"), fc.ControlPlaneAddr),
		AppKey:           fc.AppKey,
		Domain:           fc.Domain,
		AuthAddr:         fc.AuthAddr,
	}
	if cfg.MaxStaleness, err = parseDurationDefault(fc.MaxStaleness, 0); err != nil {
		return Config{}, fmt.Errorf("max_staleness: %w", err)
	}
	if cfg.BackoffInitial, err = parseDurationDefault(fc.BackoffInitial, 500*time.Millisecond); err != nil {
		return Config{}, fmt.Errorf("backoff_initial: %w", err)
	}
	if cfg.BackoffMax, err = parseDurationDefault(fc.BackoffMax, 30*time.Second); err != nil {
		return Config{}, fmt.Errorf("backoff_max: %w", err)
	}

	cfg.Secret = []byte(getenv("SYDOM_APP_SECRET"))

	if len(cfg.Secret) == 0 {
		return Config{}, errors.New("SYDOM_APP_SECRET required")
	}
	for _, f := range []struct{ name, val string }{
		{"control_plane_addr", cfg.ControlPlaneAddr},
		{"app_key", cfg.AppKey},
		{"domain", cfg.Domain},
		{"auth_addr", cfg.AuthAddr},
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

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/app/ -run TestLoadConfig -v`
预期：6 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/app/config.go internal/sidecar/app/config_test.go
git commit -m "$(printf 'feat(sidecar/app): Config + LoadConfig（YAML+env，sidecar cmd 任务 1/4）\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

## 任务 2：Run + Main 装配 + 集成测试

**文件：**
- 创建：`internal/sidecar/app/run.go`
- 测试：`internal/sidecar/app/run_test.go`

Run 是单体装配函数，由一个真实 TCP fake PolicySync 集成测试驱动（验证装配链贯通 + 优雅关闭），不拆微步。`Main()` 为薄 glue（flag/signal/listener/Run），随 run.go 一并实现、由二进制构建（Task 3）兜底。

- [ ] **步骤 1：编写失败的集成测试**

`internal/sidecar/app/run_test.go`：

```go
package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/app"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakePolicySync 是最小 PolicySync 服务端：PullSnapshot 返固定快照，Subscribe 长连保持。
type fakePolicySync struct {
	syncv1.UnimplementedPolicySyncServer
	snap *syncv1.Snapshot
}

func (f *fakePolicySync) PullSnapshot(context.Context, *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	return f.snap, nil
}

func (f *fakePolicySync) Subscribe(_ *syncv1.SubscribeRequest, s syncv1.PolicySync_SubscribeServer) error {
	<-s.Context().Done()
	return s.Context().Err()
}

// startFakeControlPlane 起真实 TCP 的 fake PolicySync，返回其监听地址。
func startFakeControlPlane(t *testing.T) string {
	t.Helper()
	snap := &syncv1.Snapshot{
		Version: 5,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
		},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{Id: 2, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}
	g := grpc.NewServer()
	syncv1.RegisterPolicySyncServer(g, &fakePolicySync{snap: snap})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return lis.Addr().String()
}

func TestRun_WiringEndToEnd(t *testing.T) {
	cpAddr := startFakeControlPlane(t)

	authLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cfg := app.Config{
		ControlPlaneAddr: cpAddr,
		AppKey:           "app-1",
		Domain:           "dom1",
		Secret:           []byte("secret"),
		MaxStaleness:     0,
		BackoffInitial:   time.Millisecond,
		BackoffMax:       5 * time.Millisecond,
		// AuthAddr 不设：Run 用注入的 authLis，不读 cfg.AuthAddr（仅 Main 用）。
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, authLis, logger) }()

	conn, err := grpc.NewClient(authLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	cli := authv1.NewAuthServiceClient(conn)

	// 装配链贯通：bootstrap 同步后 alice 经 manager 角色可 read order（就绪前返 Unavailable）。
	require.Eventually(t, func() bool {
		resp, err := cli.Check(context.Background(), &authv1.CheckRequest{
			Subject: "alice", Object: "order", Action: "read"})
		return err == nil && resp.GetAllowed()
	}, 10*time.Second, 50*time.Millisecond, "bootstrap 后 alice 应可 read order")

	// deny override 端到端贯通 FilterSQL。
	attrs, err := structpb.NewStruct(map[string]any{"department": "HR"})
	require.NoError(t, err)
	fres, err := cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: attrs})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", fres.GetSql())
	gotArgs := make([]any, len(fres.GetArgs()))
	for i, v := range fres.GetArgs() {
		gotArgs[i] = v.AsInterface()
	}
	require.Equal(t, []any{"HR", "locked", "void"}, gotArgs)

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

运行：`go test ./internal/sidecar/app/ -run TestRun_WiringEndToEnd`
预期：编译失败 `undefined: app.Run`。

- [ ] **步骤 3：编写实现**

`internal/sidecar/app/run.go`：

```go
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
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/app/ -run TestRun_WiringEndToEnd -v`
预期：PASS —— Check 经 bootstrap 同步后返 allowed=true、FilterSQL 反映 deny override、ctx 取消后 Run 返回 nil。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/app/run.go internal/sidecar/app/run_test.go
git commit -m "$(printf 'feat(sidecar/app): Run 装配 + 优雅关闭 + 集成测试（sidecar cmd 任务 2/4）\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

## 任务 3：cmd/sydom-sidecar 入口二进制

**文件：**
- 创建：`cmd/sydom-sidecar/main.go`

- [ ] **步骤 1：编写薄入口**

`cmd/sydom-sidecar/main.go`：

```go
package main

import (
	"os"

	"github.com/nickZFZ/Sydom/internal/sidecar/app"
)

func main() {
	os.Exit(app.Main())
}
```

- [ ] **步骤 2：验证二进制可构建**

运行：`go build -o /tmp/sydom-sidecar ./cmd/sydom-sidecar/ && echo BUILD_OK`
预期：输出 `BUILD_OK`（链接成功，产出可执行文件）。

- [ ] **步骤 3：验证配置缺失即非零退出**

运行：`/tmp/sydom-sidecar -config /nonexistent.yaml; echo "exit=$?"`
预期：`exit=1`（LoadConfig 读不到文件 → fail-close，stderr 有 `load config` 日志）。

- [ ] **步骤 4：Commit**

```bash
git add cmd/sydom-sidecar/main.go
git commit -m "$(printf 'feat(cmd): sydom-sidecar 入口二进制（sidecar cmd 任务 3/4）\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

---

## 任务 4：全量验证与收尾

**文件：** 创建 `cmd/sydom-sidecar/config.example.yaml`；全仓验证。

- [ ] **步骤 1：app 包测试（含 -race）**

运行：`go test -race ./internal/sidecar/app/...`
预期：`ok`，无 DATA RACE（对账写 vs 鉴权读、2 协程关闭协调并发）。

- [ ] **步骤 2：vet + 全仓 build + sidecar 回归**

运行：`go vet ./internal/sidecar/... ./cmd/... && go build ./... && go test ./internal/sidecar/...`
预期：vet 无输出；build 成功（含两个 cmd）；sidecar 各包测试 `ok`。

- [ ] **步骤 3：补 config.example.yaml（运维参考）**

创建 `cmd/sydom-sidecar/config.example.yaml`：

```yaml
control_plane_addr: "localhost:8082"
app_key: "app-prod-01"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "0s"        # 0=关闭陈旧守卫（!Ready 仍 fail-close）；生产建议显式设非零，如 "90s"
backoff_initial: "500ms"
backoff_max: "30s"
# 敏感项只走环境变量（不写本文件）：
#   SYDOM_APP_SECRET = 该 app 的 HMAC 密钥（原始字节，须与控制面建该 app 时设置的一致）
# 须先有可达的控制面 PolicySync（cmd/sydom-controlplane）
```

```bash
git add cmd/sydom-sidecar/config.example.yaml
git commit -m "$(printf 'docs(cmd): sidecar 配置示例 + 启动前置说明（sidecar cmd 任务 4/4）\n\nCo-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>')"
```

- [ ] **步骤 4：收尾**

调用 finishing-a-development-branch 技能决定集成方式（合并/PR/清理）。
更新进度记忆 `project_detailed_design_progress.md`：cmd/sydom-sidecar 已实现，两个二进制（控制面+数据面）装配齐备；下一步 ⑤ SDK 接口规范。

---

## 自检结果

**1. 规格覆盖度**（对照 spec 各节）：
- §2 决策 1（薄 main + 注入监听器）→ Task 2 `Run` 接 `authLis` + Task 3 薄 main。✅
- §2 决策 2 + §4 配置（YAML+env secret）→ Task 1 `config.go` + 6 个 LoadConfig 单测（解析/env 覆盖/secret 缺失/字段缺失/默认/max_staleness 显式 0）。✅
- §2 决策 3（domain≠app_key 双字段）→ Task 1 Config 两字段 + 两校验项；Task 2 `Run` 步骤 2（`cfg.Domain`→kernel）& 4（`cfg.AppKey`→syncclient）。✅
- §2 决策 4（max_staleness 默认 0）→ Task 1 `parseDurationDefault(fc.MaxStaleness, 0)` + `TestLoadConfig_Defaults`/`_MaxStalenessZeroExplicit`；Task 2 `authz.Config{MaxStaleness: cfg.MaxStaleness}`；Task 4 example 非零示例+注释。✅
- §2 决策 5（loopback TCP）→ Task 2 `Main` 的 `net.Listen("tcp", cfg.AuthAddr)` + 集成测试 `127.0.0.1:0`。✅
- §2 决策 6 + §4 校验（fail-close 启动）→ Task 1 校验单测 + Task 2 各 `return fmt.Errorf` + Task 3 步骤 3（缺配置非零退出）。✅
- §5 装配数据流（Table→Engine→Filter→SyncClient→Authorizer→gRPC）→ Task 2 `Run` 按序。✅
- §5 就绪前 fail-close → Task 2 集成测试 `require.Eventually`（就绪前 Check 返 err，就绪后 allowed）。✅
- §6 优雅关闭（WaitGroup + cascade cancel + GracefulStop + sync.Close）→ Task 2 `Run` launch/关闭段 + 集成测试关闭断言。✅
- §7 日志（slog）→ Task 2 `Run`/`Main` 的 logger。✅
- §8 测试（LoadConfig 单测 + Run 集成 + -race）→ Task 1 + Task 2 + Task 4 步骤 1。✅
- §9 不在范围（unix socket/mTLS/可观测/DB-Redis/多 app）→ 未触及。✅

**2. 占位符扫描**：无 TODO/待定；每个代码步骤完整可编译，命令/预期具体。`Main()` 不单测是经论证取舍（薄 glue，由二进制构建 + 缺配置退出码兜底），非占位。✅

**3. 类型一致性**：`Config`/`LoadConfig`（Task 1 定义，Task 2 `Run`/`Main` 消费）；`Run(ctx,cfg,authLis,logger)`/`Main()`（Task 2 定义，Task 3 `main` 消费）。装配引用的 `dataperm.NewTable`/`kernel.New`/`dataperm.NewFilter`/`syncclient.New`/`syncclient.Config`/`authz.New`/`authz.Config`/`authz.NewGRPCServer`/`(*SyncClient).Run`/`Close` 均已回源核实签名。命名陷阱（`syncCli` 避让 `sync` 包）在 Task 2 实现与「关键事实」均显式标注。`syncclient.Run` ctx 取消返回 `nil`（非 `context.Canceled`）已在 spec §6 与「关键事实」说明，`launch` 的 `context.Canceled` 过滤对它恒不命中但保持与控制面同构。proto 字段名（`syncv1.PolicyRule.Values`、`syncv1.DataPolicy.Effect`、`authv1.CheckRequest`/`FilterRequest`/`FilterSQLResponse`）均对照既有测试核实。✅
