# M5.4a 控制面多副本安全（Relay 选主）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面 outbox relay 加 PostgreSQL 会话级 advisory-lock 选主，只让一个副本 drain，使控制面可安全跑 N 副本；补集成测试证明恰好一次投递 / failover 连续 / 并发写安全，并加 leader 可观测 gauge。

**架构：** 新增无依赖小包 `internal/controlplane/leader`（`Run(ctx,db,key,poll,onElected)` 用 `pg_try_advisory_lock` 选主、专用连接持锁、失锁取消领导期子 ctx 后重新参选）。`internal/controlplane/app/run.go` 把 `launch("relay", RunRelayLoop(...))` 改为 `leader.Run(...)` 包住**原样** `RunRelayLoop`（drain 逻辑逐字不改），并在领导期回调里 set/clear `obs` 的 `sydom_relay_leader` gauge。授权求值核心零触碰。

**技术栈：** Go、`database/sql` + PostgreSQL advisory lock、`internal/dbtest`（testcontainers PG）、`prometheus/client_golang`、既有 `broadcast.Publisher`/`outbox.RunRelayLoop`。

**BASE：** `feat/m5-4a-multi-replica-safety` @ `59818ff`（含设计规格 commit）；规格 `docs/superpowers/specs/2026-07-12-sydom-m5-4a-control-plane-multi-replica-safety-design.md`。

**零触碰铁律：** `git diff 272a806..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/` 必须为空。`internal/controlplane/outbox/relay.go` 内容 diff 必须为 0（只新增测试文件，不改 drain 逻辑）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/obs/metrics.go`（修改） | 加 `relayLeader prometheus.Gauge` 字段 + `New()` 注册 `sydom_relay_leader` + nil-safe `SetRelayLeader(bool)` 设置器（镜像既有 `connected`/`SetConnected`）。 |
| `internal/obs/relay_leader_test.go`（新增） | 断言 `SetRelayLeader(true/false)` 反映到 `/metrics` 的 `sydom_relay_leader` 序列 + nil 接收者安全。 |
| `internal/controlplane/leader/leader.go`（新增） | advisory-lock 选主：`Run(ctx,db,key,poll,onElected)`；专用连接 `pg_try_advisory_lock`、领导期健康检查、显式 `pg_advisory_unlock` 释放。仅依赖标准库 + `database/sql`。 |
| `internal/controlplane/leader/leader_test.go`（新增） | 争锁恒单 leader + 取消先当选者→另一实例接管（failover）。 |
| `internal/controlplane/outbox/relay_leader_test.go`（新增） | relay 经 `leader.Run` 包裹的集成测试：恰好一次投递（含撤门变异实验证有齿）+ failover 连续 drain 积压无丢。 |
| `internal/controlplane/store/lockversion_test.go`（新增） | 并发写经 `LockAppVersion` 行锁串行、版本单调无丢（M54A-4）。 |
| `internal/controlplane/app/run.go`（修改） | `launch("relay", …)` 由裸 `RunRelayLoop` 改为 `leader.Run(runCtx, db, relayLeaderKey, cfg.RelayPollInterval, onElected)`，`onElected` 里 set/clear gauge 后跑原样 `RunRelayLoop`；定义 `relayLeaderKey` 常量；import `leader`。 |

> `internal/controlplane/outbox/relay.go` 的 `RunRelayLoop`/`drainOnce` **不改一字**。

---

## 任务 1：obs `sydom_relay_leader` gauge（TDD）

**文件：**
- 修改：`internal/obs/metrics.go`
- 测试：`internal/obs/relay_leader_test.go`（新增）

- [ ] **步骤 1：写失败的测试**

新增 `internal/obs/relay_leader_test.go`：

```go
package obs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/obs"
)

func scrapeRelay(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rr.Body.String()
}

func TestSetRelayLeader_Gauge(t *testing.T) {
	m := obs.New()

	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 0") {
		t.Fatalf("默认应为 sydom_relay_leader 0，实测:\n%s", body)
	}

	m.SetRelayLeader(true)
	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 1") {
		t.Fatalf("SetRelayLeader(true) 后应为 sydom_relay_leader 1，实测:\n%s", body)
	}

	m.SetRelayLeader(false)
	if body := scrapeRelay(t, m.Handler()); !strings.Contains(body, "sydom_relay_leader 0") {
		t.Fatalf("SetRelayLeader(false) 后应回 sydom_relay_leader 0，实测:\n%s", body)
	}

	var nilM *obs.Metrics
	nilM.SetRelayLeader(true) // nil 接收者不得 panic
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/obs/ -run TestSetRelayLeader_Gauge -v`
预期：编译失败 `m.SetRelayLeader undefined`。

- [ ] **步骤 3：实现 gauge**

在 `internal/obs/metrics.go` 的 `Metrics` struct 末尾（`connected prometheus.Gauge` 之后）加字段：

```go
	relayLeader prometheus.Gauge
```

在 `New()` 的字面量里（`connected: …` 之后）加：

```go
		relayLeader: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sydom_relay_leader", Help: "本控制面副本是否为 outbox relay leader(0/1)"}),
```

把 `reg.MustRegister(...)` 那行的参数追加 `m.relayLeader`：

```go
	reg.MustRegister(m.grpcReqs, m.grpcDur, m.httpReqs, m.httpDur, m.authzDec,
		m.checkDur, m.cacheHits, m.cacheMiss, m.snapApplied, m.connected, m.relayLeader)
```

在文件的类型化助手区（`SetConnected` 附近）加 nil-safe 设置器：

```go
// SetRelayLeader 标记本副本是否为 outbox relay leader。nil 接收者安全。
func (m *Metrics) SetRelayLeader(isLeader bool) {
	if m == nil {
		return
	}
	var v float64
	if isLeader {
		v = 1
	}
	m.relayLeader.Set(v)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/obs/ -run TestSetRelayLeader_Gauge -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/obs/metrics.go internal/obs/relay_leader_test.go
git commit -m "feat(obs): M5.4a 加 sydom_relay_leader gauge(nil-safe SetRelayLeader,镜像 connected 模式)"
```

---

## 任务 2：`leader` 选主包（TDD）

**文件：**
- 创建：`internal/controlplane/leader/leader.go`
- 测试：`internal/controlplane/leader/leader_test.go`

- [ ] **步骤 1：写失败的测试**

新增 `internal/controlplane/leader/leader_test.go`：

```go
package leader_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/leader"
	"github.com/nickZFZ/Sydom/internal/dbtest"
)

// 争锁下任一时刻恒只有一个 leader；取消先当选者后另一实例接管。
func TestRun_SingleLeaderAndFailover(t *testing.T) {
	db := dbtest.SetupSchema(t)
	const key int64 = 918273645
	const poll = 40 * time.Millisecond

	var concurrent int32 // 当前在跑的 onElected 数
	var violated int32   // 是否出现过 >1
	started := make(chan int, 2)

	mk := func(id int) func(context.Context) error {
		return func(lctx context.Context) error {
			if atomic.AddInt32(&concurrent, 1) != 1 {
				atomic.StoreInt32(&violated, 1)
			}
			select {
			case started <- id:
			default:
			}
			<-lctx.Done()
			atomic.AddInt32(&concurrent, -1)
			return lctx.Err()
		}
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = leader.Run(ctx1, db, key, poll, mk(1)) }()
	go func() { _ = leader.Run(ctx2, db, key, poll, mk(2)) }()

	var first int
	select {
	case first = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("无实例当选")
	}
	time.Sleep(6 * poll)
	if atomic.LoadInt32(&violated) != 0 {
		t.Fatal("出现过并发 leader >1（advisory lock 未生效）")
	}

	// 取消先当选者 → 另一实例应接管
	if first == 1 {
		cancel1()
	} else {
		cancel2()
	}
	select {
	case second := <-started:
		if second == first {
			t.Fatalf("接管者不应是被取消的实例 %d", first)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failover 超时：无实例接管")
	}
	time.Sleep(4 * poll)
	if atomic.LoadInt32(&violated) != 0 {
		t.Fatal("failover 后出现过并发 leader >1")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/leader/ -run TestRun_SingleLeaderAndFailover -v`
预期：编译失败 `package leader is not in std`（包尚不存在）/ `undefined: leader.Run`。

- [ ] **步骤 3：实现 `leader.go`**

新增 `internal/controlplane/leader/leader.go`：

```go
// Package leader 用 PostgreSQL 会话级 advisory lock 选举单一 leader：
// 抢到锁的副本运行 onElected；进程/连接死时 PG 自动释放会话锁，另一副本接管。
package leader

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Run 参选 key 对应的领导权，阻塞至 ctx 取消。
// 抢到锁后以「领导期有效」的子 ctx 调用 onElected；onElected 返回或锁连接失效则结束本次
// 领导期、释放锁并重新参选。onElected 返回非 context.Canceled 错误时，Run 以该错误返回（致命）。
// poll 为参选轮询与领导期健康检查间隔。
func Run(ctx context.Context, db *sql.DB, key int64, poll time.Duration, onElected func(leaderCtx context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := db.Conn(ctx)
		if err != nil {
			if !wait(ctx, poll) {
				return ctx.Err()
			}
			continue
		}
		got, err := tryLock(ctx, conn, key)
		if err != nil || !got {
			conn.Close() // 未持锁，归还连接
			if !wait(ctx, poll) {
				return ctx.Err()
			}
			continue
		}
		// 成为 leader
		err = lead(ctx, conn, poll, onElected)
		release(conn, key) // 显式解锁再关闭：会话锁不会因 Close() 归还池而释放
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err // onElected 致命错误
		}
		// 失去领导权（连接失效）→ 重新参选
	}
}

// lead 持锁运行 onElected；后台健康检查发现锁连接失效则取消领导期子 ctx。
func lead(ctx context.Context, conn *sql.Conn, poll time.Duration, onElected func(context.Context) error) error {
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-t.C:
				if err := conn.PingContext(leaderCtx); err != nil {
					cancel() // 连接失效 → PG 已释放会话锁 → 放弃领导权
					return
				}
			}
		}
	}()
	return onElected(leaderCtx)
}

func tryLock(ctx context.Context, conn *sql.Conn, key int64) (bool, error) {
	var got bool
	err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got)
	return got, err
}

// release 尽力显式解锁再关闭连接。会话级 advisory lock 不会因 (*sql.Conn).Close() 归还池
// 而释放（会话仍活）——必须显式 unlock，否则残锁使其它副本永远抢不到。连接已死则 unlock
// 失败无妨（PG 已随会话释放）。用 Background 确保 ctx 已取消时仍尝试解锁。
func release(conn *sql.Conn, key int64) {
	_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	conn.Close()
}

// wait 睡 d，或 ctx 取消时提前返回 false。
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/leader/ -run TestRun_SingleLeaderAndFailover -v`
预期：PASS（需 Docker 起 PG，dbtest 自动管理）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/leader/
git commit -m "feat(cp): M5.4a 新增 leader 选主包(pg_try_advisory_lock 专用连接持锁+健康检查+显式解锁,failover 自动接管)"
```

---

## 任务 3：relay 经 leader 包裹的集成测试（恰好一次 + failover，有齿）

**文件：**
- 测试：`internal/controlplane/outbox/relay_leader_test.go`（新增；不改 `relay.go`）

- [ ] **步骤 1：写测试（恰好一次 + failover 连续）**

新增 `internal/controlplane/outbox/relay_leader_test.go`：

```go
package outbox_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/leader"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"google.golang.org/protobuf/proto"
)

// recordingPub 记录每次 Publish 的 delta 版本；delay 拉宽 drain 窗口以稳定暴露多 drainer 竞态。
type recordingPub struct {
	mu    sync.Mutex
	seen  []uint64
	delay time.Duration
}

func (p *recordingPub) Publish(_ context.Context, _ int64, d *syncv1.Delta) error {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.mu.Lock()
	p.seen = append(p.seen, d.Version)
	p.mu.Unlock()
	return nil
}

func (p *recordingPub) versions() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint64, len(p.seen))
	copy(out, p.seen)
	return out
}

func insertOutbox(t *testing.T, db *sql.DB, appID int64, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		blob, err := proto.Marshal(&syncv1.Delta{Version: uint64(i)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO policy_outbox(app_id, version, delta_proto) VALUES($1,$2,$3)`,
			appID, i, blob); err != nil {
			t.Fatal(err)
		}
	}
}

func countPublished(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM policy_outbox WHERE published_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", d)
}

// 两个 relay 争锁：leader 门保证每条 delta 恰好发布一次。
func TestRelayUnderLeader_ExactlyOnce(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	insertOutbox(t, db, appID, 5)

	pub := &recordingPub{delay: 10 * time.Millisecond}
	const key int64 = 918273600
	const poll = 25 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 2; i++ {
		go func() {
			_ = leader.Run(ctx, db, key, poll, func(lctx context.Context) error {
				return outbox.RunRelayLoop(lctx, db, pub, poll)
			})
		}()
	}
	waitUntil(t, 3*time.Second, func() bool { return countPublished(t, db) == 5 })
	time.Sleep(4 * poll) // 给潜在的重复投递留出暴露窗口
	cancel()

	if got := len(pub.versions()); got != 5 {
		t.Fatalf("恰好一次投递：应发布 5 条，实测 %d（>5=多 leader 无协调重复投递）", got)
	}
}

// 杀 leader → 另一实例接管 drain 积压：无丢（全 published + 版本集完整）。
func TestRelayUnderLeader_FailoverContinuity(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	insertOutbox(t, db, appID, 12)

	pub := &recordingPub{delay: 15 * time.Millisecond}
	const key int64 = 918273601
	const poll = 25 * time.Millisecond
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = leader.Run(ctx1, db, key, poll, func(l context.Context) error { return outbox.RunRelayLoop(l, db, pub, poll) }) }()
	go func() { _ = leader.Run(ctx2, db, key, poll, func(l context.Context) error { return outbox.RunRelayLoop(l, db, pub, poll) }) }()

	waitUntil(t, 3*time.Second, func() bool { return countPublished(t, db) >= 2 })
	cancel1() // 杀先当选 leader
	waitUntil(t, 5*time.Second, func() bool { return countPublished(t, db) == 12 })
	cancel2()

	// 无丢：12 条全 published，且版本集恰为 {1..12}（failover 边界允许 ≤1 条重复，故只查完整性）。
	seen := map[uint64]bool{}
	for _, v := range pub.versions() {
		seen[v] = true
	}
	for i := uint64(1); i <= 12; i++ {
		if !seen[i] {
			t.Fatalf("failover 丢失版本 %d（seen=%v）", i, pub.versions())
		}
	}
	if got := countPublished(t, db); got != 12 {
		t.Fatalf("failover 后应全 published 12，实测 %d", got)
	}
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./internal/controlplane/outbox/ -run 'TestRelayUnderLeader' -v`
预期：两测试均 PASS。

- [ ] **步骤 3：变异实验证「恰好一次」测试有齿**

临时把 `TestRelayUnderLeader_ExactlyOnce` 里两个 goroutine 的
`leader.Run(ctx, db, key, poll, func(lctx …) { return outbox.RunRelayLoop(lctx, db, pub, poll) })`
改为**直接** `outbox.RunRelayLoop(ctx, db, pub, poll)`（撤掉 leader 门）。

运行：`go test ./internal/controlplane/outbox/ -run TestRelayUnderLeader_ExactlyOnce -v`
预期：**FAIL**——`len(pub.versions()) > 5`（两 drainer 无协调重复投递）。观察到 FAIL 后**还原**为 `leader.Run` 包裹，再次运行确认 PASS。这证明测试对「多 leader 重复投递」有齿。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/outbox/relay_leader_test.go
git commit -m "test(cp): M5.4a relay 经 leader 恰好一次投递(撤门变异实验证有齿)+failover 连续 drain 积压无丢"
```

---

## 任务 4：并发写安全（LockAppVersion 行锁串行）—— M54A-4

**文件：**
- 测试：`internal/controlplane/store/lockversion_test.go`（新增）

- [ ] **步骤 1：写测试**

新增 `internal/controlplane/store/lockversion_test.go`：

```go
package store_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
)

func currentVersion(t *testing.T, db *sql.DB, appID int64) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// N 个并发 writer 各在事务内 LockAppVersion→+1→Bump→commit：行锁串行化，最终版本 = 初始 + N，无丢。
func TestLockAppVersion_SerializesConcurrentWriters(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	before := currentVersion(t, db, appID)

	const N = 20
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				errs <- err
				return
			}
			cur, err := store.LockAppVersion(context.Background(), tx, appID)
			if err != nil {
				tx.Rollback()
				errs <- err
				return
			}
			if err := store.BumpAppVersion(context.Background(), tx, appID, cur+1); err != nil {
				tx.Rollback()
				errs <- err
				return
			}
			errs <- tx.Commit()
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("writer 出错: %v", e)
		}
	}
	if got := currentVersion(t, db, appID); got != before+N {
		t.Fatalf("并发 %d 写应串行到版本 %d，实测 %d（碰撞/丢失=行锁失效）", N, before+N, got)
	}
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./internal/controlplane/store/ -run TestLockAppVersion_SerializesConcurrentWriters -v`
预期：PASS（20 并发写串行 → 版本恰 +20）。

- [ ] **步骤 3：Commit**

```bash
git add internal/controlplane/store/lockversion_test.go
git commit -m "test(cp): M5.4a 并发写经 LockAppVersion 行锁串行(20 writer→版本+20 无碰撞,证跨副本写安全)"
```

---

## 任务 5：接线 run.go（relay 经 leader + gauge）+ 验收

**文件：**
- 修改：`internal/controlplane/app/run.go`

- [ ] **步骤 1：改接线**

在 `internal/controlplane/app/run.go` 的 import 块加：

```go
	"github.com/nickZFZ/Sydom/internal/controlplane/leader"
```

在文件顶部（package 声明后的 const/var 区，或紧邻 run 函数外）加常量：

```go
// relayLeaderKey 是 outbox relay 选主的固定 advisory-lock key（同 PG 实例内专属本用途）。
const relayLeaderKey int64 = 0x53444f42 // "SDOB"
```

把原来的第 130 行：

```go
	launch("relay", func() error { return outbox.RunRelayLoop(runCtx, db, pub, cfg.RelayPollInterval) })
```

替换为：

```go
	launch("relay", func() error {
		return leader.Run(runCtx, db, relayLeaderKey, cfg.RelayPollInterval,
			func(lctx context.Context) error {
				m.SetRelayLeader(true)
				defer m.SetRelayLeader(false)
				return outbox.RunRelayLoop(lctx, db, pub, cfg.RelayPollInterval)
			})
	})
```

（`m`、`db`、`pub`、`runCtx`、`cfg.RelayPollInterval` 均在此作用域已有；`context` 已 import。）

- [ ] **步骤 2：编译 + 全量测试**

运行：`go build ./... && go test ./... 2>&1 | tail -20`
预期：build 无错；`go test ./...` EXIT 0（含新增 leader/outbox/store/obs 测试）。

- [ ] **步骤 3：M54A-5 relay 逻辑零改核验**

运行：`git diff 272a806..HEAD -- internal/controlplane/outbox/relay.go`
预期：**空输出**（drain 逻辑逐字未改，只新增了测试文件）。

- [ ] **步骤 4：M54A-1 零触碰授权核心核验**

运行：`git diff --numstat 272a806..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz`
预期：**空输出**。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/run.go
git commit -m "feat(cp): M5.4a relay 经 leader.Run 选主(仅 leader 副本 drain,onElected set/clear sydom_relay_leader gauge)+固定 advisory-lock key"
```

---

## 自检

**1. 规格覆盖度：**
- §3.1/3.3 机制 + 组件 → 任务 2（leader 包）+ 任务 5（run.go 接线）。
- §3.4 leader gauge → 任务 1（obs）+ 任务 5（onElected set/clear）。
- §4 决策（会话级 try-lock / 专用连接 / 显式解锁 / drain 零改）→ 任务 2 `leader.go` + 任务 5 步骤 3。
- §5 fail-close / failover 语义 → 任务 3 failover 连续测试。
- §6 验证（争锁单 leader / 恰好一次〔变异实验〕/ failover / 并发写）→ 任务 2/3/4。
- §7 M54A-1..7 → M54A-1 任务5步4、M54A-2 任务3步1+3、M54A-3 任务3、M54A-4 任务4、M54A-5 任务5步3、M54A-6 任务1、M54A-7 任务5步2。
- 全覆盖。

**2. 占位符扫描：** 每步含实际代码/命令与预期输出；无 TODO/待定/伪代码。变异实验（任务3步3）给出确切改法与还原。

**3. 类型一致性：** `leader.Run(ctx, db, key int64, poll time.Duration, onElected func(context.Context) error) error` 在任务2定义、任务3/5 一致调用；`obs.Metrics.SetRelayLeader(bool)` 任务1定义、任务5调用；`outbox.RunRelayLoop(ctx, *sql.DB, broadcast.Publisher, time.Duration) error` 既有签名，`recordingPub` 实现 `broadcast.Publisher`（`Publish(context.Context, int64, *syncv1.Delta) error`）；`syncv1.Delta.Version uint64`、`store.LockAppVersion(ctx, cp.DBTX, int64)(int64,error)`/`store.BumpAppVersion(ctx, cp.DBTX, int64, int64)error` 均与实查一致。
