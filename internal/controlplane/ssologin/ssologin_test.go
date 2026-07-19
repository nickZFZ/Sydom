package ssologin_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/ssologin"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestResolver_DecryptsSecretAndMatches(t *testing.T) {
	db := dbtest.SetupSchema(t)
	mk := bytes.Repeat([]byte{9}, 32)
	enc, err := crypto.Encrypt(mk, []byte("topsecret"))
	require.NoError(t, err)

	var tid, opID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('A') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled)
		VALUES ($1,'https://idp','cid',$2,true)`, tid, enc)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, tid)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`INSERT INTO admin_operator (principal, secret_enc, email, status)
		VALUES ('alice','\xbb'::bytea,'alice@acme.com',1) RETURNING id`).Scan(&opID))
	_, err = db.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, tid, opID)
	require.NoError(t, err)

	r, err := ssologin.NewResolver(db, mk)
	require.NoError(t, err)
	ctx := context.Background()

	idp, ok, err := r.ResolveIdPByDomain(ctx, "acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "topsecret", idp.ClientSecret, "resolver 须解密回明文供 token 交换")
	require.Equal(t, tid, idp.TenantID)
	require.True(t, idp.Enabled)

	byT, ok, err := r.ResolveIdPByTenant(ctx, tid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "topsecret", byT.ClientSecret)

	p, ok, err := r.MatchOperatorForLogin(ctx, tid, "alice@acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "alice", p)

	// 未配域→ok=false，无 secret 泄露。
	_, ok, err = r.ResolveIdPByDomain(ctx, "nope.com")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestNewResolver_RejectsShortKey(t *testing.T) {
	_, err := ssologin.NewResolver(nil, []byte("short"))
	require.Error(t, err, "主密钥长度不足须 fail-close")
}
