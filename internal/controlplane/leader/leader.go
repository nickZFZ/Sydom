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
