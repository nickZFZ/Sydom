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

// leaderRecordingPub 记录每次 Publish 的 delta 版本；delay 拉宽 drain 窗口以稳定暴露多 drainer 竞态。
// 命名加 leader 前缀以避免与 relay_test.go 中同包已有的 recordingPub 类型撞名。
type leaderRecordingPub struct {
	mu    sync.Mutex
	seen  []uint64
	delay time.Duration
}

func (p *leaderRecordingPub) Publish(_ context.Context, _ int64, d *syncv1.Delta) error {
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	p.mu.Lock()
	p.seen = append(p.seen, d.Version)
	p.mu.Unlock()
	return nil
}

func (p *leaderRecordingPub) versions() []uint64 {
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

	pub := &leaderRecordingPub{delay: 10 * time.Millisecond}
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

	pub := &leaderRecordingPub{delay: 15 * time.Millisecond}
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
