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
