package store_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestIdPLoginByDomainAndOperatorMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tA, tB int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('A') RETURNING id`).Scan(&tA))
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('B') RETURNING id`).Scan(&tB))
	_, err := db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled)
		VALUES ($1,'https://idpA','cidA','\xaa'::bytea,true)`, tA)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, tA)
	require.NoError(t, err)

	// 域路由命中 A。
	row, ok, err := store.IdPLoginByDomain(ctx, db, "acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, tA, row.TenantID)
	require.Equal(t, "cidA", row.ClientID)
	require.True(t, row.Enabled)

	// 未知域→ok=false（无枚举 oracle 由调用方保证）。
	_, ok, err = store.IdPLoginByDomain(ctx, db, "nope.com")
	require.NoError(t, err)
	require.False(t, ok)

	// 按租户重取命中 A。
	byT, ok, err := store.IdPLoginByTenant(ctx, db, tA)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "cidA", byT.ClientID)

	// operator：active + tenant A 成员 → 命中。
	var opID int64
	require.NoError(t, db.QueryRow(`INSERT INTO admin_operator (principal, secret_enc, email, status)
		VALUES ('alice','\xbb'::bytea,'alice@acme.com',1) RETURNING id`).Scan(&opID))
	_, err = db.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, tA, opID)
	require.NoError(t, err)

	p, ok, err := store.OperatorEmailMatch(ctx, db, tA, "alice@acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "alice", p)

	// 跨租户：alice 非 B 成员 → 不命中（防冒充）。
	_, ok, err = store.OperatorEmailMatch(ctx, db, tB, "alice@acme.com")
	require.NoError(t, err)
	require.False(t, ok)

	// 未知 email → 不命中。
	_, ok, err = store.OperatorEmailMatch(ctx, db, tA, "ghost@acme.com")
	require.NoError(t, err)
	require.False(t, ok)

	// 停用（status=2）→ 不命中。
	_, err = db.Exec(`UPDATE admin_operator SET status=2 WHERE id=$1`, opID)
	require.NoError(t, err)
	_, ok, err = store.OperatorEmailMatch(ctx, db, tA, "alice@acme.com")
	require.NoError(t, err)
	require.False(t, ok)
}
