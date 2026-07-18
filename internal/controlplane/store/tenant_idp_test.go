package store_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestUpsertTenantIdpTx_UpsertAndDomains(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('t1') RETURNING id`).Scan(&tid))

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertTenantIdpTx(context.Background(), tx, tid,
		"https://issuer", "cid", []byte("enc1"), []string{"Acme.com", "acme.co.uk"}, true))
	require.NoError(t, tx.Commit())

	got, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.True(t, got.Configured)
	require.Equal(t, "https://issuer", got.Issuer)
	require.Equal(t, "cid", got.ClientID)
	require.True(t, got.Enabled)
	require.ElementsMatch(t, []string{"acme.com", "acme.co.uk"}, got.Domains, "域应小写化")

	// 再次 upsert：覆盖 + 替换域。
	tx2, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertTenantIdpTx(context.Background(), tx2, tid,
		"https://issuer2", "cid2", []byte("enc2"), []string{"new.com"}, false))
	require.NoError(t, tx2.Commit())
	got2, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "https://issuer2", got2.Issuer)
	require.False(t, got2.Enabled)
	require.Equal(t, []string{"new.com"}, got2.Domains, "旧域应被替换")
}

func TestTenantIdpOf_Unconfigured(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('t2') RETURNING id`).Scan(&tid))
	got, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.False(t, got.Configured)
}
