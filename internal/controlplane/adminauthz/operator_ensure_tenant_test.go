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

func TestEnsureTenantAdmin_BindsAndIsIdempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tID, appID := dbtest.SeedAppInTenant(t, db, "acme", "order", "AK_o")

	// 首次：建 operator + 租户角色 + 绑定。
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("s3cr3t")))
	// 再次：幂等，不报错、不重复。
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("s3cr3t")))

	var opCount, bindCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='alice'`).Scan(&opCount))
	require.Equal(t, 1, opCount, "幂等：operator 不重复")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='alice' AND sr.domain=$1`, adminauthz.TenantDomain(tID)).Scan(&bindCount))
	require.Equal(t, 1, bindCount, "幂等：租户域绑定不重复")

	// 绑定生效：alice 经 enforcer 应能管理本租户 app。
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	ok, err := enf.Enforce(ctx, "alice", adminauthz.TenantDomain(tID), adminauthz.TenantDomain(tID), "role", "create")
	require.NoError(t, err)
	require.True(t, ok)
	_ = appID
}
