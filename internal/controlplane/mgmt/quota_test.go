package mgmt_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newQuotaFixture 建 schema + 一个空租户（0 应用、默认 free 套餐），返回 server / operator ctx /
// tenantID / db 句柄（mgmt_test 外部包无法访问 AdminServer.db，故单独返回 db 供断言）。
func newQuotaFixture(t *testing.T) (*mgmt.AdminServer, context.Context, int64, *sql.DB) {
	t.Helper()
	db := dbtest.SetupSchema(t)
	var tenantID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('quota-t') RETURNING id`).Scan(&tenantID))
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	return srv, ctx, tenantID, db
}

func createApp(ctx context.Context, s *mgmt.AdminServer, tenantID int64, i int) error {
	_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: uint64(tenantID), Domain: fmt.Sprintf("d%d", i), Name: "n", AppKey: fmt.Sprintf("ak_%d", i)})
	return err
}

// free 套餐限 3：建到第 3 个成功、第 4 个 ResourceExhausted（fail-close）。
func TestCreateApplication_QuotaFailClose(t *testing.T) {
	s, ctx, tenantID, _ := newQuotaFixture(t)
	for i := 0; i < 3; i++ {
		require.NoErrorf(t, createApp(ctx, s, tenantID, i), "第 %d 个应用应成功（free 限 3）", i+1)
	}
	err := createApp(ctx, s, tenantID, 3)
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "第 4 个应超配额 fail-close")
}

// 数据驱动：把 plan 限额 UPDATE 到 5，则第 4/5 个也成功。
func TestCreateApplication_QuotaDataDriven(t *testing.T) {
	s, ctx, tenantID, db := newQuotaFixture(t)
	_, err := db.Exec(`UPDATE plan SET max_applications=5 WHERE id=(SELECT plan_id FROM tenant WHERE id=$1)`, tenantID)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoErrorf(t, createApp(ctx, s, tenantID, i), "限额提到 5 后第 %d 个应成功", i+1)
	}
}

// 8 并发建应用于 free(3) 租户：行锁串行 → 恰 3 成功、5 ResourceExhausted、DB 恰 3。
func TestCreateApplication_QuotaConcurrent(t *testing.T) {
	s, ctx, tenantID, db := newQuotaFixture(t)
	const N = 8
	var wg sync.WaitGroup
	codesCh := make(chan codes.Code, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codesCh <- status.Code(createApp(ctx, s, tenantID, i))
		}(i)
	}
	wg.Wait()
	close(codesCh)
	var ok, exhausted int
	for c := range codesCh {
		switch c {
		case codes.OK:
			ok++
		case codes.ResourceExhausted:
			exhausted++
		}
	}
	require.Equal(t, 3, ok, "free(3) 下恰 3 成功")
	require.Equal(t, N-3, exhausted, "其余超配额 ResourceExhausted")
	var total int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&total))
	require.Equal(t, 3, total, "DB 应用数恰 3（行锁串行无超限）")
}
