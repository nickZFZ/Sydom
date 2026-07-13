package mgmt_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memberSrv 建 schema + 一个租户（owner 已是 1 个成员），返回 server / tenantID。
func memberSrv(t *testing.T) (*mgmt.AdminServer, uint64) {
	t.Helper()
	db := dbtest.SetupSchema(t)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	reg, err := srv.RegisterTenant(context.Background(),
		&adminv1.RegisterTenantRequest{TenantName: "mq", OwnerPrincipal: "mqowner"})
	require.NoError(t, err)
	return srv, reg.TenantId
}

// free 成员限 3：owner(1) + 邀请 2 成功、第 3 个邀请 ResourceExhausted（fail-close）。
func TestInviteMember_QuotaFailClose(t *testing.T) {
	srv, tid := memberSrv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // owner + 2 = 3 = free 限
		_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId: tid, Principal: fmt.Sprintf("m%d", i)})
		require.NoErrorf(t, err, "第 %d 个邀请应成功（free 成员限 3，含 owner）", i+1)
	}
	_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: tid, Principal: "overflow"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "满员后新邀请应 ResourceExhausted")
}

// Order-B 正确性：满员时重复邀请【已有】成员 → AlreadyExists（非 ResourceExhausted）。
func TestInviteMember_AtLimitReinviteExisting(t *testing.T) {
	srv, tid := memberSrv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId: tid, Principal: fmt.Sprintf("m%d", i)})
		require.NoError(t, err)
	}
	// 现已满员（owner + m0 + m1 = 3）。重复邀请已有成员 m0：
	_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: tid, Principal: "m0"})
	require.Equal(t, codes.AlreadyExists, status.Code(err),
		"满员时重复邀请已有成员应 AlreadyExists（!inserted 短路先于配额门）")
}

// 8 并发邀请于 free(3) 租户（owner 已占 1）：行锁串行 → 恰 2 成功、其余 ResourceExhausted。
func TestInviteMember_QuotaConcurrent(t *testing.T) {
	srv, tid := memberSrv(t)
	const N = 8
	var wg sync.WaitGroup
	codesCh := make(chan codes.Code, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := srv.InviteMember(context.Background(),
				&adminv1.InviteMemberRequest{TenantId: tid, Principal: fmt.Sprintf("c%d", i)})
			codesCh <- status.Code(err)
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
	require.Equal(t, 2, ok, "owner 占 1，free(3) 下恰 2 邀请成功")
	require.Equal(t, N-2, exhausted, "其余超配额 ResourceExhausted")
}
