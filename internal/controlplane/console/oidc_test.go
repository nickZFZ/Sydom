package console

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/ssologin"
	appcrypto "github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// —— mock IdP：签发 RS256 ID Token 的最小 OIDC provider ——

type mockIdP struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	kid           string
	clientID      string
	nonce         string // 测试从 authURL 提取后回填
	email         string
	emailVerified bool
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	m := &mockIdP{key: key, kid: "test-kid", emailVerified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 m.srv.URL,
			"authorization_endpoint": m.srv.URL + "/auth",
			"token_endpoint":         m.srv.URL + "/token",
			"jwks_uri":               m.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(m.key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(m.key.PublicKey.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{"kty": "RSA", "kid": m.kid, "n": n, "e": e}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": m.signToken()})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockIdP) signToken() string {
	now := time.Now()
	hdr := map[string]any{"alg": "RS256", "typ": "JWT", "kid": m.kid}
	claims := map[string]any{
		"iss": m.srv.URL, "sub": "u-1", "aud": m.clientID,
		"email": m.email, "email_verified": m.emailVerified,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": m.nonce,
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(hdr) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// —— Console + SSO 装配（真实 ssologin.Resolver + 真实 Redis/DB） ——

func newConsoleSSO(t *testing.T, baseURL string) (*httptest.Server, *sql.DB, []byte) {
	t.Helper()
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mk := bytes.Repeat([]byte{7}, 32)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk, "root@sydom", []byte("rootsecret")))
	resolver, err := adminauthz.NewOperatorResolver(db, mk)
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	rdb := redis.NewClient(&redis.Options{Addr: dbtest.StartRedis(t)})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewRedisStore(rdb, time.Minute)

	ssoResolver, err := ssologin.NewResolver(db, mk)
	require.NoError(t, err)
	sso := SSODeps{
		Resolver: ssoResolver, Matcher: ssoResolver,
		HTTPClient: &http.Client{Timeout: 5 * time.Second}, ConsoleBaseURL: baseURL,
	}
	ts := httptest.NewServer(NewHandler(srv, resolver, enf, db, store, testLogger(t), false, sso))
	t.Cleanup(ts.Close)
	return ts, db, mk
}

// seedIdP 建 tenant + 启用 IdP（issuer=idpURL, cid/sec）+ 域 acme.com，返回 tenantID。
func seedIdP(t *testing.T, db *sql.DB, mk []byte, idpURL string, enabled bool) int64 {
	t.Helper()
	enc, err := appcrypto.Encrypt(mk, []byte("sec"))
	require.NoError(t, err)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled)
		VALUES ($1,$2,'cid',$3,$4)`, tid, idpURL, enc, enabled)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, tid)
	require.NoError(t, err)
	return tid
}

// seedOperator 建 operator（email/status）并绑定为 membershipTenant 的成员。
func seedOperator(t *testing.T, db *sql.DB, principal, email string, status int16, membershipTenant int64) {
	t.Helper()
	var opID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc, email, status) VALUES ($1,'\xbb'::bytea,$2,$3) RETURNING id`,
		principal, email, status).Scan(&opID))
	_, err := db.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, membershipTenant, opID)
	require.NoError(t, err)
}

func ssoClient(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// start 发起 SSO，返回 (state, nonce)。
func start(t *testing.T, c *http.Client, ts *httptest.Server, email string) (string, string) {
	t.Helper()
	resp, err := c.PostForm(ts.URL+"/login/sso", url.Values{"email": {email}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "发起须 302 到 IdP")
	u, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return u.Query().Get("state"), u.Query().Get("nonce")
}

func callback(t *testing.T, c *http.Client, ts *httptest.Server, code, state string) *http.Response {
	t.Helper()
	resp, err := c.Get(ts.URL + "/auth/oidc/callback?code=" + code + "&state=" + url.QueryEscape(state))
	require.NoError(t, err)
	return resp
}

func hasSessionCookie(resp *http.Response) bool {
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookieName && ck.Value != "" {
			return true
		}
	}
	return false
}

// —— 正路径：alice 是 tenant A 的 active 成员 → SSO 登录建立会话 ——

func TestOIDCLogin_EndToEnd(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid)

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "alice@acme.com")
	require.NotEmpty(t, state)
	require.NotEmpty(t, nonce)

	idp.nonce, idp.email, idp.emailVerified = nonce, "alice@acme.com", true
	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	require.True(t, hasSessionCookie(resp), "回调成功须下发会话 cookie")
}

// —— state 一次性：重放同一 state 第二次→拒 ——

func TestOIDCLogin_StateReplayRejected(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid)

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "alice@acme.com")
	idp.nonce, idp.email = nonce, "alice@acme.com"

	resp1 := callback(t, c, ts, "code123", state)
	resp1.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp1.StatusCode)

	// 同 state 再来一次（重放）→ GETDEL 已消费 → 401。
	resp2 := callback(t, c, ts, "code123", state)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp2.StatusCode, "state 重放须拒")
}

// —— 跨租户冒充：email 属 A，但 operator 仅在 B 有成员 → 拒 ——

func TestOIDCLogin_CrossTenantRejected(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tidA := seedIdP(t, db, mk, idp.srv.URL, true)
	// bob 有 A 所属域的 email，但只是租户 B 的成员。
	var tidB int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('other') RETURNING id`).Scan(&tidB))
	seedOperator(t, db, "bob", "bob@acme.com", 1, tidB)
	_ = tidA

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "bob@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "bob@acme.com", true

	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "跨租户 email 须拒（防冒充）")
	require.False(t, hasSessionCookie(resp))
}

// —— email_verified=false → 拒 ——

func TestOIDCLogin_EmailNotVerifiedRejected(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid)

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "alice@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "alice@acme.com", false

	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "email_verified=false 须拒")
}

// —— IdP disabled → 发起即拒 ——

func TestOIDCLogin_IdPDisabledRejected(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, false) // disabled
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid)

	c := ssoClient(t)
	resp, err := c.PostForm(ts.URL+"/login/sso", url.Values{"email": {"alice@acme.com"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "IdP disabled 须拒发起")
}

// —— 缺 consoleBaseURL → fail-close ——

func TestOIDCLogin_MissingBaseURLFailClose(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "") // 无 base URL
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid)

	c := ssoClient(t)
	resp, err := c.PostForm(ts.URL+"/login/sso", url.Values{"email": {"alice@acme.com"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "缺 consoleBaseURL 须 fail-close")
}

// —— M6-sso-3 JIT 自动开通 ——

// JIT 开 + 全新 email → 自动开通零权限成员 + 会话。
func TestOIDCLogin_JITProvisionsNewMember(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	_, err := db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tid)
	require.NoError(t, err)

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "newbie@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "newbie@acme.com", true
	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.True(t, hasSessionCookie(resp), "JIT 开通后须建会话")

	// operator + membership 存在，零 casbin 绑定。
	var tier int16
	require.NoError(t, db.QueryRow(`SELECT m.tier FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		WHERE o.principal='newbie@acme.com' AND m.tenant_id=$1`, tid).Scan(&tier))
	require.Equal(t, int16(3), tier)
	var binds int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		WHERE o.principal='newbie@acme.com'`).Scan(&binds))
	require.Equal(t, 0, binds, "JIT 成员零权限")
}

// JIT 关（默认）+ 全新 email → 401（回归守卫：严格映射不变）。
func TestOIDCLogin_JITDisabledRejectsUnknown(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	seedIdP(t, db, mk, idp.srv.URL, true) // jit_enabled 默认 false

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "newbie@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "newbie@acme.com", true
	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "JIT 关时全新 email 须拒")
}

// JIT 开 + 既有非成员 email → 401（跨租户防护）。
func TestOIDCLogin_JITRejectsExistingNonMember(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	_, err := db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tid)
	require.NoError(t, err)
	// bob 已有 operator 但非本租户成员（无 membership）。
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc, email, status)
		VALUES ('bob','\xbb'::bytea,'bob@acme.com',1)`)
	require.NoError(t, err)

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "bob@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "bob@acme.com", true
	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "既有非成员 email 即便 JIT 开也须拒")
}

// JIT 开 + 既有成员 email → 严格映射胜（不重复开通）。
func TestOIDCLogin_JITExistingMemberStrictWins(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	_, err := db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tid)
	require.NoError(t, err)
	seedOperator(t, db, "alice", "alice@acme.com", 1, tid) // 既有成员

	c := ssoClient(t)
	state, nonce := start(t, c, ts, "alice@acme.com")
	idp.nonce, idp.email, idp.emailVerified = nonce, "alice@acme.com", true
	resp := callback(t, c, ts, "code123", state)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.True(t, hasSessionCookie(resp))
	// 不重复开通：alice 仍只有一条 membership。
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		WHERE o.principal='alice' AND m.tenant_id=$1`, tid).Scan(&n))
	require.Equal(t, 1, n)
}
