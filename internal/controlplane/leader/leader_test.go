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
