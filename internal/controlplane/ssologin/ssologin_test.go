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

func TestProvisionOperatorForLogin(t *testing.T) {
	db := dbtest.SetupSchema(t)
	mk := bytes.Repeat([]byte{9}, 32)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('A') RETURNING id`).Scan(&tid))
	r, err := ssologin.NewResolver(db, mk)
	require.NoError(t, err)
	ctx := context.Background()

	// 全新 email → 建 operator(principal=email)+membership(TierMember)。
	p, ok, err := r.ProvisionOperatorForLogin(ctx, tid, "newbie@acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "newbie@acme.com", p)
	var status int16
	require.NoError(t, db.QueryRow(`SELECT status FROM admin_operator WHERE principal='newbie@acme.com'`).Scan(&status))
	require.Equal(t, int16(1), status)
	var tier int16
	require.NoError(t, db.QueryRow(`SELECT m.tier FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		WHERE o.principal='newbie@acme.com' AND m.tenant_id=$1`, tid).Scan(&tier))
	require.Equal(t, int16(3), tier, "TierMember")
	// 零 casbin 绑定。
	var binds int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		WHERE o.principal='newbie@acme.com'`).Scan(&binds))
	require.Equal(t, 0, binds, "JIT operator 零 casbin 授权")
	// secret 为密文（非空 bytea）。
	var enc []byte
	require.NoError(t, db.QueryRow(`SELECT secret_enc FROM admin_operator WHERE principal='newbie@acme.com'`).Scan(&enc))
	require.NotEmpty(t, enc)

	// 二次同 email → ok=false（既有不 JIT）。
	_, ok, err = r.ProvisionOperatorForLogin(ctx, tid, "newbie@acme.com")
	require.NoError(t, err)
	require.False(t, ok)

	// 既有非成员 email（另建 operator 但不入本租户）→ ok=false。
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc, email, status)
		VALUES ('preexist','\xbb'::bytea,'pre@acme.com',1)`)
	require.NoError(t, err)
	_, ok, err = r.ProvisionOperatorForLogin(ctx, tid, "pre@acme.com")
	require.NoError(t, err)
	require.False(t, ok, "既有 email 即便非成员也不 JIT")
}
