package adminauthz_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestEnsureTenantAdmin_WritesMembershipAndBinding(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tID, _ := dbtest.SeedAppInTenant(t, db, "t-a", "app-a", "AK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("sa")))

	// membership(owner) 已写。
	ms, err := adminauthz.TenantsOfOperator(ctx, db, "alice")
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.Equal(t, tID, ms[0].TenantID)
	require.Equal(t, "t-a", ms[0].TenantName)
	require.Equal(t, adminauthz.TierOwner, ms[0].Tier)

	// casbin 绑定也在（I-1 锁步）：t:<id> 域存在 alice→tenant-admin-<id> 的 g 行。
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='alice' AND sr.domain=$1`, adminauthz.TenantDomain(tID)).Scan(&n))
	require.Equal(t, 1, n)

	// alice 非超管（运营平面标志 false）。
	op, err := adminauthz.IsOperatingPlane(ctx, db, "alice")
	require.NoError(t, err)
	require.False(t, op)
}

func TestInsertMembership_ReportsInserted(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tID, opID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('x') RETURNING id`).Scan(&tID))
	require.NoError(t, db.QueryRow(`INSERT INTO admin_operator(principal,secret_enc) VALUES('u','\xab'::bytea) RETURNING id`).Scan(&opID))

	ins, err := adminauthz.InsertMembership(ctx, db, tID, opID, adminauthz.TierAdmin)
	require.NoError(t, err)
	require.True(t, ins)

	ins, err = adminauthz.InsertMembership(ctx, db, tID, opID, adminauthz.TierAdmin) // 重复
	require.NoError(t, err)
	require.False(t, ins) // ON CONFLICT DO NOTHING → 未插入
}
