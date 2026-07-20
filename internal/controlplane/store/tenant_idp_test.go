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

// 同一请求内重复域（大小写/空白差异，归一后相同）应被去重，而非撞全局 UNIQUE 报错。
// 先前逐条插入不去重，第二条归一后相同的域触发 uq_tenant_idp_domain（pq 23505），
// 经上层映射成「他租户占用」的 AlreadyExists——对「自重复」是误导。
func TestUpsertTenantIdpTx_DeduplicatesDomains(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('dedup') RETURNING id`).Scan(&tid))

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.UpsertTenantIdpTx(ctx, tx, tid,
		"https://issuer", "cid", []byte("enc"),
		[]string{"Acme.com", "acme.com", "  ACME.com  "}, true, false),
		"同请求内归一后相同的域应去重，不应撞 UNIQUE 报错")
	require.NoError(t, tx.Commit())

	got, err := store.TenantIdpOf(ctx, db, tid)
	require.NoError(t, err)
	require.Equal(t, []string{"acme.com"}, got.Domains, "重复域应折叠为单条")
}

func TestDeleteTenantIdpTx(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('d') RETURNING id`).Scan(&tid))
	// 无配置→false。
	tx0, _ := db.BeginTx(ctx, nil)
	del, err := store.DeleteTenantIdpTx(ctx, tx0, tid)
	require.NoError(t, err)
	require.False(t, del)
	require.NoError(t, tx0.Commit())
	// 配置后删→true + 域清空。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc) VALUES ($1,'https://i','c','\xaa'::bytea)`, tid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, tid)
	require.NoError(t, err)
	tx1, _ := db.BeginTx(ctx, nil)
	del, err = store.DeleteTenantIdpTx(ctx, tx1, tid)
	require.NoError(t, err)
	require.True(t, del)
	require.NoError(t, tx1.Commit())
	got, err := store.TenantIdpOf(ctx, db, tid)
	require.NoError(t, err)
	require.False(t, got.Configured)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&n))
	require.Equal(t, 0, n)
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
