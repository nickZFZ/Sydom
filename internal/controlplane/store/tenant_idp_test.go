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
		"https://issuer", "cid", []byte("enc1"), []string{"Acme.com", "acme.co.uk"}, true, true))
	require.NoError(t, tx.Commit())

	got, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.True(t, got.Configured)
	require.Equal(t, "https://issuer", got.Issuer)
	require.Equal(t, "cid", got.ClientID)
	require.True(t, got.Enabled)
	require.True(t, got.JITEnabled, "jit_enabled roundtrip")
	require.ElementsMatch(t, []string{"acme.com", "acme.co.uk"}, got.Domains, "域应小写化")

	// 再次 upsert：覆盖 + 替换域。
	tx2, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertTenantIdpTx(context.Background(), tx2, tid,
		"https://issuer2", "cid2", []byte("enc2"), []string{"new.com"}, false, false))
	require.NoError(t, tx2.Commit())
	got2, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "https://issuer2", got2.Issuer)
	require.False(t, got2.Enabled)
	require.False(t, got2.JITEnabled, "jit_enabled 覆盖为 false")
	require.Equal(t, []string{"new.com"}, got2.Domains, "旧域应被替换")
}

func TestTenantIdpSecretEnc(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('se') RETURNING id`).Scan(&tid))
	// 无配置→ok=false。
	_, ok, err := store.TenantIdpSecretEnc(context.Background(), db, tid)
	require.NoError(t, err)
	require.False(t, ok)
	// 有配置→返密文。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://i','cid','\xdead'::bytea)`, tid)
	require.NoError(t, err)
	enc, ok, err := store.TenantIdpSecretEnc(context.Background(), db, tid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte{0xde, 0xad}, enc)
}

func TestTenantIdpOf_Unconfigured(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('t2') RETURNING id`).Scan(&tid))
	got, err := store.TenantIdpOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.False(t, got.Configured)
}
