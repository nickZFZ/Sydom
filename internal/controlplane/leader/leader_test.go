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

// onElected 若不经 ctx 而快速返回，Run 必须在重新参选前退避（约每 poll 一轮），否则会
// busy-loop 疯抢 advisory lock 打爆 PG。断言固定窗口内的调用次数受 poll 上界约束（有齿：
// 撤掉退避则次数暴涨至成百上千）。
func TestRun_BackoffOnFastReturn(t *testing.T) {
	db := dbtest.SetupSchema(t)
	const key int64 = 918273646
	const poll = 30 * time.Millisecond

	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = leader.Run(ctx, db, key, poll, func(context.Context) error {
			atomic.AddInt32(&calls, 1)
			return nil // 立即返回（非经 ctx 取消）
		})
		close(done)
	}()

	time.Sleep(10 * poll)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后返回")
	}

	n := atomic.LoadInt32(&calls)
	if n == 0 {
		t.Fatal("onElected 从未被调用（选主未生效）")
	}
	// 10*poll 窗口、每轮 ~poll → 约 10 次量级；给足调度余量放宽到 40。无退避则会是成百上千次。
	if n > 40 {
		t.Fatalf("onElected 被调用 %d 次，疑似 busy-loop（重新参选退避失效）", n)
	}
}
