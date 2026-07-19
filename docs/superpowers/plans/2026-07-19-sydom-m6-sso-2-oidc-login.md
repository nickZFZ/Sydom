# M6-sso-2：OIDC 登录流（企业 SSO 第二片）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。子代理对每个任务遵循 superpowers:test-driven-development。

**目标：** 让租户 operator 经其租户 OIDC IdP 登录 Sydom Console——发起→授权码流→ID Token 验签→**事前登录严格身份映射**→复用既有 `sessions.Create`。OIDC RP 纯 stdlib 手写，授权求值核心零触碰。

**架构：** ①独立 `internal/oidc` 包（无状态纯函数 RP：discovery/authURL/exchange/JWKS/verify，注入 `*http.Client`+`now`，离线可测）；②新 leaf 包 `internal/controlplane/ssologin`（生产 resolver，持 db+masterKey，解密 client_secret，INV-1 secret 留控制面）；③Console BFF 编排（`oidc.go`：`handleSSOStart`/`handleOIDCCallback` + Redis 一时态 + 登录页），认证成功后汇聚到既有 `h.sessions.Create(principal)`+cookie；④operator email 开通（迁移 000025 + proto additive + handler）。授权判定/casbin/adminauthz/authz.go 除 ruleTable +1 外一字不改。

**技术栈：** Go stdlib（`crypto/rsa`·`ecdsa`·`sha256`·`crypto`·`elliptic`·`math/big`·`encoding/json`·`base64`·`net/http`·`net/url`），Postgres（testcontainers via `internal/dbtest`），Redis（`go-redis/v9`，`dbtest.StartRedis`），buf/protoc-gen-go（proto 生成），`stretchr/testify`。

---

## 关键既有事实（实现者必读，勿凭猜）

- **登录汇聚点** `internal/controlplane/console/auth.go` `handleLoginPost`：认证成功后 `id,_,err := h.sessions.Create(r.Context(), principal)` → `http.SetCookie(... Name: sessionCookieName, HttpOnly:true, Secure:h.cookieSecure, SameSite:StrictMode)` → 302。**SSO 回调复用同一段。** `sessionCookieName = "sydom_console_session"`。
- **`Handler` struct** 定义在 `auth.go:22-32`：字段 `srv *mgmt.AdminServer`、`enf *adminauthz.Enforcer`、`db *sql.DB`、`resolver secretResolver`、`sessions *RedisStore`、`logger *slog.Logger`、`cookieSecure bool`、`templates pageSet`。
- **`RedisStore`**（`session.go`）：`Create(ctx, principal) (id, csrf string, err)`；键前缀 `console:sess:`；`randToken()`=32B CSPRNG→`base64.RawURLEncoding`（复用它做 state/nonce/verifier）。
- **`NewHandler`**（`handler.go:16`）当前签名：`NewHandler(srv *mgmt.AdminServer, resolver secretResolver, enf *adminauthz.Enforcer, db *sql.DB, sessions *RedisStore, logger *slog.Logger, cookieSecure bool) http.Handler`。**唯一非测试调用方** = `internal/controlplane/app/run.go:166`；测试调用方 = `console/handler_test.go:47`（`newConsole` helper）。二者本片都要改。
- **`crypto`**（`internal/crypto/aesgcm.go`）：`KeySize=32`、`Encrypt(key, plaintext []byte)`、`Decrypt(key, blob []byte)`、`ErrKeySize`。
- **M6-sso-1 store**（`internal/controlplane/store/tenant_idp.go`）：表 `tenant_idp(tenant_id UNIQUE, issuer, client_id, client_secret_enc BYTEA, enabled)` + `tenant_idp_domain(tenant_id, domain UNIQUE)`；域小写化存储。
- **`admin_operator`** 列：`id`、`principal`、`secret_enc BYTEA`、`status int16`（默认 1=active）。`tenant_membership(tenant_id, operator_id, tier)` PK `(tenant_id, operator_id)`。
- **mgmt 错误助手**（已存在，`mgmt` 包内）：`isUniqueViolation(err) bool`、`isForeignKeyViolation(err) bool`、`auditJSON(map) `、`adminauthz.InsertAdminAudit(...)`、`cp.OperatorFromContext(ctx)`。
- **ruleTable**（`internal/controlplane/mgmt/authz.go:46`）：`CreateOperator` = `{"admin","create",false,scopeSystem}`。本片仅追加一行 `SetOperatorEmail`。
- **proto**：`api/proto/sydom/admin/v1/admin.proto`；生成到 `gen/sydom/admin/v1/`。`CreateOperatorRequest{string principal=1}`、`WriteResponse{uint64 version=1; bool changed=2}`。生成命令 `make proto-gen`；破坏性门 `make proto-breaking`。
- **REST**（`internal/controlplane/restgw/routes.go`）：`route{method,pattern,fullMethod,decode,invoke}`；已有 `POST /v1/operators`→CreateOperator（757-767）、`PUT /v1/operators/{operator_id}/status`→SetOperatorStatus（768-783）；`decodeBody(body, m)`、`r.PathValue(key)`、`pathInt64`。
- **测试基建**：`dbtest.MigratedDSN(t)`/`dbtest.SetupSchema(t)`（testcontainers postgres:17-alpine，Docker 已就绪）、`dbtest.StartRedis(t)`；`internal/db` 迁移测试用 `startPostgres(t)`+`RunMigrations(dsn, migrationsSource)`+`MigrateDown(...)`+`tableExists(t,db,name)`。

## 文件结构

**新建：**
- `db/migrations/000025_operator_email.up.sql` / `.down.sql` — admin_operator 加 email。
- `internal/db/operator_email_migration_test.go` — 迁移测试（UNIQUE + 多 NULL + down）。
- `internal/oidc/oidc.go` — `ProviderConfig`、`Discover`、`AuthCodeURL`、`Exchange`、`PKCEChallenge`。
- `internal/oidc/jwks.go` — `JWKS`、`ParseJWKS`、`FetchJWKS`。
- `internal/oidc/verify.go` — `VerifyParams`、`IDTokenClaims`、`VerifyIDToken`、sentinel 错误。
- `internal/oidc/oidc_test.go` — Discover/AuthCodeURL/Exchange/FetchJWKS（httptest mock）。
- `internal/oidc/verify_test.go` — 签发测试 token + JWKS，正路径 + 全部负路径（逐条）。
- `internal/controlplane/store/idp_login.go` — `IdPLoginRow`、`IdPLoginByDomain`、`IdPLoginByTenant`、`OperatorEmailMatch`。
- `internal/controlplane/store/idp_login_test.go` — 域路由/租户成员匹配（含跨租户/停用/未验证否定）。
- `internal/controlplane/ssologin/ssologin.go` — `IdPLogin`、`Resolver`（db+masterKey，解密）。
- `internal/controlplane/ssologin/ssologin_test.go` — 解密回显 + fail-close。
- `internal/controlplane/console/oidcstate.go` — `oidcState`、`RedisStore.PutOIDCState`/`TakeOIDCState`（GETDEL 一次性）。
- `internal/controlplane/console/oidc.go` — `handleSSOStart`、`handleOIDCCallback`、`ssoFail`、`isSafeReturnTo`。
- `internal/controlplane/console/oidc_test.go` — mock IdP 端到端 + 全部安全不变量否定用例。

**修改：**
- `internal/controlplane/console/auth.go` — `Handler` struct 加 4 字段 + 私有接口 `idpLoginResolver`/`operatorMatcher`。
- `internal/controlplane/console/handler.go` — `NewHandler` 加 `sso SSODeps` 参数 + 注册 2 条 SSO 路由 + 填 Handler 新字段。
- `internal/controlplane/console/templates/login.html` — 加 email 先行 SSO 表单。
- `internal/controlplane/console/handler_test.go` — `newConsole` 适配新签名（默认零 SSODeps）。
- `api/proto/sydom/admin/v1/admin.proto` — `CreateOperatorRequest` 加 `email`；新增 `SetOperatorEmail` rpc + `SetOperatorEmailRequest`。
- `internal/controlplane/mgmt/admin_ops.go` — `CreateOperator` 落 email；新增 `SetOperatorEmail` handler。
- `internal/controlplane/mgmt/authz.go` — ruleTable 追加 `SetOperatorEmail` 一行。
- `internal/controlplane/restgw/routes.go` — 加 `PUT /v1/operators/{principal}/email` → SetOperatorEmail。
- `internal/controlplane/app/config.go` — 加 `ConsoleBaseURL`（`SYDOM_CONSOLE_BASE_URL` / yaml `console_base_url`）。
- `internal/controlplane/app/run.go` — 装配 `ssologin.NewResolver` + `oidcHTTPClient` + `consoleBaseURL`，传入 `console.NewHandler`。

**零触碰（机器 diff 核验，除 authz.go +1 行外必须 0 diff）：** `casbin/`、`internal/controlplane/adminauthz/`、`internal/sidecar/kernel/`、`internal/sidecar/dataperm/`（若存在）、`internal/controlplane/mgmt/authz.go` 的 `AuthorizeRule`/`Enforce` 逻辑。

---

## 任务 1：迁移 000025（admin_operator.email）

**文件：**
- 创建：`db/migrations/000025_operator_email.up.sql`、`db/migrations/000025_operator_email.down.sql`
- 测试：`internal/db/operator_email_migration_test.go`

- [ ] **步骤 1：写迁移测试（先失败）** — 建 `internal/db/operator_email_migration_test.go`：

```go
package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000025_OperatorEmail(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 存量 operator 无 email（NULL）不受影响。
	var o1 int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op1','\xab'::bytea) RETURNING id`).Scan(&o1))
	require.NoError(t, db.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc, email) VALUES ('op2','\xab'::bytea,'a@acme.com') RETURNING id`).Scan(new(int64)))

	// 全局唯一：第二 operator 抢同 email→冲突。
	_, err = db.Exec(`UPDATE admin_operator SET email='a@acme.com' WHERE id=$1`, o1)
	require.Error(t, err, "admin_operator.email 全局唯一应拒重复")

	// 多个 NULL 共存（Postgres UNIQUE 允许多 NULL）。
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op3','\xab'::bytea)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op4','\xab'::bytea)`)
	require.NoError(t, err, "多个 NULL email 应共存")

	// down：列被删。
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	_, err = db.Exec(`SELECT email FROM admin_operator LIMIT 1`)
	require.Error(t, err, "down 后 email 列应不存在")
}
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/db/ -run TestMigration000025 -v`；预期 FAIL（无 000025 迁移文件 / email 列不存在）。
- [ ] **步骤 3：写迁移 up** — `db/migrations/000025_operator_email.up.sql`：

```sql
-- M6-sso-2：operator 关联 email，供 OIDC 登录严格映射（email_verified 匹配）。
-- nullable + 全局 UNIQUE（一 email→一 operator）；非 SSO operator 为 NULL；小写化由应用层保证。
ALTER TABLE admin_operator ADD COLUMN email VARCHAR(320) UNIQUE;
```

- [ ] **步骤 4：写迁移 down** — `db/migrations/000025_operator_email.down.sql`：

```sql
ALTER TABLE admin_operator DROP COLUMN email;
```

- [ ] **步骤 5：运行确认通过** — `go test ./internal/db/ -run TestMigration000025 -v`；预期 PASS。
- [ ] **步骤 6：Commit**

```bash
git add db/migrations/000025_operator_email.up.sql db/migrations/000025_operator_email.down.sql internal/db/operator_email_migration_test.go
git commit -m "feat(db): 迁移 000025 admin_operator 加 email（nullable+UNIQUE）（M6-sso-2 T1）"
```

---

## 任务 2：`internal/oidc` 纯 stdlib OIDC RP

**文件：** 创建 `internal/oidc/oidc.go`、`internal/oidc/jwks.go`、`internal/oidc/verify.go`、`internal/oidc/verify_test.go`、`internal/oidc/oidc_test.go`。

> 本任务 security-critical。**先写 `verify_test.go` 的负路径**，再写实现。测试用测试 RSA 私钥签 ID Token（RS256），构造含对应公钥的 JWKS。

- [ ] **步骤 1：写验签测试（先失败）** — `internal/oidc/verify_test.go`：

```go
package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// signRS256 用 key 签一份 JWT（header{alg,kid} + payload），返回紧凑串。alg 可覆盖以测负路径。
func signRS256(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(hdr) + "." + enc(claims)
	if alg == "none" {
		return signingInput + "."
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksFor 构造只含一把 RSA 公钥（kid）的 JWKS。
func jwksFor(t *testing.T, kid string, pub *rsa.PublicKey) JWKS {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	doc := map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": kid, "n": n, "e": e}}}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	ks, err := ParseJWKS(b)
	require.NoError(t, err)
	return ks
}

func goodClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://idp.example", "sub": "u-1", "aud": "client-x",
		"email": "alice@acme.com", "email_verified": true,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "N",
	}
}

func params() VerifyParams {
	return VerifyParams{Issuer: "https://idp.example", ClientID: "client-x", Nonce: "N"}
}

func TestVerifyIDToken_Good(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	raw := signRS256(t, key, "k1", "RS256", goodClaims(now))
	c, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.NoError(t, err)
	require.Equal(t, "alice@acme.com", c.Email)
	require.True(t, c.EmailVerified)
	require.Equal(t, "u-1", c.Sub)
}

func TestVerifyIDToken_Negatives(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	ks := jwksFor(t, "k1", &key.PublicKey)

	cases := []struct {
		name  string
		build func() string
		keys  JWKS
	}{
		{"tampered-signature", func() string {
			raw := signRS256(t, key, "k1", "RS256", goodClaims(now))
			return raw[:len(raw)-2] + "AA" // 改签名尾字节
		}, ks},
		{"wrong-signing-key", func() string { return signRS256(t, other, "k1", "RS256", goodClaims(now)) }, ks},
		{"alg-none", func() string { return signRS256(t, key, "k1", "none", goodClaims(now)) }, ks},
		{"alg-hs256", func() string { return signRS256(t, key, "k1", "HS256", goodClaims(now)) }, ks},
		{"unknown-kid", func() string { return signRS256(t, key, "k9", "RS256", goodClaims(now)) }, ks},
		{"wrong-aud", func() string {
			c := goodClaims(now)
			c["aud"] = "someone-else"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"wrong-iss", func() string {
			c := goodClaims(now)
			c["iss"] = "https://evil"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"wrong-nonce", func() string {
			c := goodClaims(now)
			c["nonce"] = "X"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"expired", func() string {
			c := goodClaims(now)
			c["exp"] = now.Add(-2 * time.Hour).Unix()
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyIDToken(tc.build(), tc.keys, params(), now)
			require.Error(t, err, "负路径必须拒绝")
		})
	}
}

func TestVerifyIDToken_UnknownKIDSentinel(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	raw := signRS256(t, key, "k9", "RS256", goodClaims(now))
	_, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.True(t, errors.Is(err, ErrUnknownKID), "kid 未命中须暴露哨兵供调用方刷新 JWKS 重试")
}

// aud 兼容 string 与 []string。
func TestVerifyIDToken_AudArray(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	c := goodClaims(now)
	c["aud"] = []string{"other", "client-x"}
	raw := signRS256(t, key, "k1", "RS256", c)
	_, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.NoError(t, err)
}
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/oidc/ -run TestVerify -v`；预期编译失败（类型/函数未定义）。
- [ ] **步骤 3：写 `verify.go`**：

```go
package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// leewaySeconds 是 exp/iat 容许的时钟偏移（≤60s，spec §5.2）。
const leewaySeconds = 60

var (
	// ErrUnknownKID：JWKS 无匹配 kid。调用方可刷新 JWKS 重试一次后再拒。
	ErrUnknownKID = errors.New("oidc: unknown kid")
	// ErrUnsupportedAlg：alg 非 RS256/ES256（含拒 none/HS*）。
	ErrUnsupportedAlg = errors.New("oidc: unsupported alg")
	// ErrBadSignature：签名不通过。
	ErrBadSignature = errors.New("oidc: bad signature")
)

// VerifyParams 是验签断言的期望值。
type VerifyParams struct{ Issuer, ClientID, Nonce string }

// IDTokenClaims 是校验通过后返回的声明子集。
type IDTokenClaims struct {
	Iss, Sub, Email string
	EmailVerified   bool
	Exp, Iat        int64
	Aud             []string
	Nonce           string
}

// audience 兼容 OIDC aud 既可为 string 也可为 []string。
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// VerifyIDToken 逐条固定的验签算法（spec §5.2）：alg 白名单→kid+kty 绑定→验签→声明校验。
func VerifyIDToken(rawIDToken string, keys JWKS, p VerifyParams, now time.Time) (IDTokenClaims, error) {
	parts := strings.Split(rawIDToken, ".")
	if len(parts) != 3 {
		return IDTokenClaims{}, errors.New("oidc: malformed jwt")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: header decode: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: header parse: %w", err)
	}
	// 2. alg 白名单：仅 RS256/ES256；none/HS* 显式拒。
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return IDTokenClaims{}, ErrUnsupportedAlg
	}
	// 3. 按 kid 选公钥。
	key, ok := keys.keys[hdr.Kid]
	if !ok {
		return IDTokenClaims{}, ErrUnknownKID
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return IDTokenClaims{}, ErrBadSignature
	}
	// 4. 验签于 header.payload 原文；kty 须与 alg 族匹配。
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch hdr.Alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return IDTokenClaims{}, ErrUnsupportedAlg // kid 对应 EC，与 RS256 不匹配
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			return IDTokenClaims{}, ErrBadSignature
		}
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return IDTokenClaims{}, ErrUnsupportedAlg
		}
		if len(sig) != 64 {
			return IDTokenClaims{}, ErrBadSignature
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return IDTokenClaims{}, ErrBadSignature
		}
	}
	// 5. 解并校验声明。
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: payload decode: %w", err)
	}
	var rc struct {
		Iss           string   `json:"iss"`
		Sub           string   `json:"sub"`
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Exp           int64    `json:"exp"`
		Iat           int64    `json:"iat"`
		Aud           audience `json:"aud"`
		Nonce         string   `json:"nonce"`
	}
	if err := json.Unmarshal(payloadBytes, &rc); err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: payload parse: %w", err)
	}
	if rc.Iss != p.Issuer {
		return IDTokenClaims{}, errors.New("oidc: issuer mismatch")
	}
	if !contains(rc.Aud, p.ClientID) {
		return IDTokenClaims{}, errors.New("oidc: audience mismatch")
	}
	nowUnix := now.Unix()
	if nowUnix > rc.Exp+leewaySeconds {
		return IDTokenClaims{}, errors.New("oidc: token expired")
	}
	if rc.Iat != 0 && rc.Iat > nowUnix+leewaySeconds {
		return IDTokenClaims{}, errors.New("oidc: token issued in future")
	}
	if rc.Nonce != p.Nonce {
		return IDTokenClaims{}, errors.New("oidc: nonce mismatch")
	}
	return IDTokenClaims{
		Iss: rc.Iss, Sub: rc.Sub, Email: rc.Email, EmailVerified: rc.EmailVerified,
		Exp: rc.Exp, Iat: rc.Iat, Aud: []string(rc.Aud), Nonce: rc.Nonce,
	}, nil
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **步骤 4：写 `jwks.go`**：

```go
package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
)

// JWKS 是 kid→公钥（*rsa.PublicKey | *ecdsa.PublicKey）的只读映射。
type JWKS struct{ keys map[string]any }

type jwk struct {
	Kty, Kid, Crv, N, E, X, Y string
}

// ParseJWKS 解析 JWKS 文档；仅收 RSA 与 EC(P-256)，其余跳过。
func ParseJWKS(b []byte) (JWKS, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return JWKS{}, fmt.Errorf("oidc: parse jwks: %w", err)
	}
	out := JWKS{keys: make(map[string]any, len(doc.Keys))}
	for _, k := range doc.Keys {
		switch k.Kty {
		case "RSA":
			nb, err := base64.RawURLEncoding.DecodeString(k.N)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk n: %w", err)
			}
			eb, err := base64.RawURLEncoding.DecodeString(k.E)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk e: %w", err)
			}
			out.keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(nb),
				E: int(new(big.Int).SetBytes(eb).Int64()),
			}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			xb, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk x: %w", err)
			}
			yb, err := base64.RawURLEncoding.DecodeString(k.Y)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk y: %w", err)
			}
			out.keys[k.Kid] = &ecdsa.PublicKey{
				Curve: elliptic.P256(),
				X:     new(big.Int).SetBytes(xb),
				Y:     new(big.Int).SetBytes(yb),
			}
		}
	}
	return out, nil
}

// FetchJWKS GET jwksURI 并解析。
func FetchJWKS(ctx context.Context, hc *http.Client, jwksURI string) (JWKS, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return JWKS{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return JWKS{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return JWKS{}, fmt.Errorf("oidc: jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return JWKS{}, err
	}
	return ParseJWKS(body)
}
```

- [ ] **步骤 5：写 `oidc.go`**：

```go
// Package oidc 实现纯 stdlib 手写 OIDC Relying Party 原语：
// discovery / auth-code URL / token 交换 / JWKS 解析 / ID Token 验签。
// 无外部依赖、无隐式全局状态；HTTP 客户端与时钟均注入，完全离线可测。
package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProviderConfig 是 discovery 得到的端点集合。
type ProviderConfig struct {
	Issuer, AuthorizationEndpoint, TokenEndpoint, JWKSURI string
}

// Discover 拉取 issuer 的 openid-configuration，校验 issuer 字段防 mix-up。
func Discover(ctx context.Context, hc *http.Client, issuer string) (ProviderConfig, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ProviderConfig{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ProviderConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery status %d", resp.StatusCode)
	}
	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery decode: %w", err)
	}
	if doc.Issuer != issuer {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery issuer mismatch")
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery missing endpoints")
	}
	return ProviderConfig{
		Issuer: doc.Issuer, AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint: doc.TokenEndpoint, JWKSURI: doc.JWKSURI,
	}, nil
}

// AuthCodeURL 构造授权码流跳转 URL（PKCE S256）。
func AuthCodeURL(p ProviderConfig, clientID, redirectURI, state, nonce, codeChallenge string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("scope", "openid email")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("state", state)
	v.Set("nonce", nonce)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(p.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return p.AuthorizationEndpoint + sep + v.Encode()
}

// PKCEChallenge 返回 base64url(sha256(verifier))。
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Exchange 用授权码换 token，取 id_token。客户端认证=client_secret_basic。
func Exchange(ctx context.Context, hc *http.Client, p ProviderConfig,
	clientID, clientSecret, redirectURI, code, codeVerifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: token status %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("oidc: token decode: %w", err)
	}
	if tok.IDToken == "" {
		return "", fmt.Errorf("oidc: token response missing id_token")
	}
	return tok.IDToken, nil
}
```

- [ ] **步骤 6：写 `oidc_test.go`（httptest mock discovery/exchange/jwks）**：

```go
package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscover_IssuerMismatchRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://evil","authorization_endpoint":"a","token_endpoint":"t","jwks_uri":"j"}`))
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), srv.Client(), srv.URL)
	require.Error(t, err, "issuer 字段与请求不符须拒（防 mix-up）")
}

func TestDiscover_Good(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"))
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","authorization_endpoint":"` + srv.URL + `/auth","token_endpoint":"` + srv.URL + `/token","jwks_uri":"` + srv.URL + `/jwks"}`))
	}))
	defer srv.Close()
	pc, err := Discover(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/token", pc.TokenEndpoint)
}

func TestExchange_BasicAuthAndIDToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "cid", u)
		require.Equal(t, "sec", pw)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		require.Equal(t, "the-code", r.Form.Get("code"))
		require.Equal(t, "v3rifier", r.Form.Get("code_verifier"))
		_, _ = w.Write([]byte(`{"id_token":"HEADER.PAYLOAD.SIG"}`))
	}))
	defer srv.Close()
	raw, err := Exchange(context.Background(), srv.Client(),
		ProviderConfig{TokenEndpoint: srv.URL}, "cid", "sec", "https://rp/cb", "the-code", "v3rifier")
	require.NoError(t, err)
	require.Equal(t, "HEADER.PAYLOAD.SIG", raw)
}

func TestAuthCodeURL_Fields(t *testing.T) {
	got := AuthCodeURL(ProviderConfig{AuthorizationEndpoint: "https://idp/auth"},
		"cid", "https://rp/cb", "st", "no", PKCEChallenge("v"))
	for _, want := range []string{"response_type=code", "scope=openid+email", "client_id=cid",
		"code_challenge_method=S256", "state=st", "nonce=no"} {
		require.Contains(t, got, want)
	}
}
```

- [ ] **步骤 7：运行确认全绿** — `go test ./internal/oidc/ -v`；预期 PASS（含全部负路径）。
- [ ] **步骤 8：变异自检（有齿证明，改后须复原）** — 临时把 `verify.go` 的 alg 白名单改成也接受 `"none"`：`go test ./internal/oidc/ -run TestVerifyIDToken_Negatives/alg-none`；预期该子测试转 FAIL（证明测试能捕获）。**复原代码**，重跑确认恢复 PASS。
- [ ] **步骤 9：Commit**

```bash
git add internal/oidc/
git commit -m "feat(oidc): 纯 stdlib OIDC RP（discovery/authURL/exchange/JWKS/RS256+ES256 验签）+ 逐条负路径（M6-sso-2 T2）"
```

---

## 任务 3：operator email 开通（proto + handler + ruleTable + REST）

**文件：** 修改 `api/proto/.../admin.proto`、`mgmt/admin_ops.go`、`mgmt/authz.go`、`restgw/routes.go`；测试 `mgmt/admin_ops_test.go`（或新增 `mgmt/operator_email_test.go`）。

- [ ] **步骤 1：改 proto** — `api/proto/sydom/admin/v1/admin.proto`：
  - `CreateOperatorRequest` 加字段：`string email = 2;`（`// 可选，OIDC 登录映射锚；空=NULL`）。
  - rpc 区（`CreateOperator` 附近）加：`rpc SetOperatorEmail(SetOperatorEmailRequest) returns (WriteResponse);`
  - 新消息：

```proto
message SetOperatorEmailRequest {
  string principal = 1;
  string email = 2; // 空=清除
}
```

- [ ] **步骤 2：生成代码** — `make proto-gen`（若报缺工具：先 `make proto-tools` 再 `make proto-gen`）。确认 `gen/sydom/admin/v1/admin.pb.go` 出现 `GetEmail()`、`SetOperatorEmailRequest`，`admin_grpc.pb.go` 出现 `SetOperatorEmail`。
- [ ] **步骤 3：过破坏性门** — `make proto-breaking`；预期 PASS（纯 additive）。
- [ ] **步骤 4：写 handler 测试（先失败）** — 新增 `internal/controlplane/mgmt/operator_email_test.go`：

```go
package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperatorEmail_CreateThenSetAndConflicts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")

	// CreateOperator 带 email（小写化落库）。
	_, err := srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-a", Email: "A@Acme.com"})
	require.NoError(t, err)
	var got string
	require.NoError(t, db.QueryRow(`SELECT email FROM admin_operator WHERE principal='op-a'`).Scan(&got))
	require.Equal(t, "a@acme.com", got)

	// SetOperatorEmail 改到新 email。
	_, err = srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-b"})
	require.NoError(t, err)
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: "b@acme.com"})
	require.NoError(t, err)

	// email 冲突→AlreadyExists。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: "a@acme.com"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 未知 operator→NotFound。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "ghost", Email: "x@acme.com"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// 空 email 清除（NULL）。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: ""})
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='op-b' AND email IS NULL`).Scan(&n))
	require.Equal(t, 1, n)
}
```

- [ ] **步骤 5：运行确认失败** — `go test ./internal/controlplane/mgmt/ -run TestOperatorEmail -v`；预期编译失败（`SetOperatorEmail` 未定义）。
- [ ] **步骤 6：改 `CreateOperator` 落 email** — `mgmt/admin_ops.go`，在 `InsertOperator` 成功后（`id` 已得、`BumpPolicyVersion` 之前）插入：

```go
	if email := strings.ToLower(strings.TrimSpace(r.Email)); email != "" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admin_operator SET email=$2 WHERE id=$1`, id, email); err != nil {
			if isUniqueViolation(err) {
				return nil, status.Error(codes.AlreadyExists, "email already used")
			}
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
	}
```
（文件顶部若无 `strings` 导入则补上。**不改 `adminauthz.InsertOperator`**——零触碰 adminauthz。）

- [ ] **步骤 7：写 `SetOperatorEmail` handler** — 追加到 `mgmt/admin_ops.go`：

```go
// SetOperatorEmail 设置/清除 operator 的 email（OIDC 登录锚）。email 不参与 casbin 策略，故不 bump 版本。
// 冲突→AlreadyExists；未知 operator→NotFound；空 email→清除（NULL）。
func (s *AdminServer) SetOperatorEmail(ctx context.Context, r *adminv1.SetOperatorEmailRequest) (*adminv1.WriteResponse, error) {
	email := strings.ToLower(strings.TrimSpace(r.Email))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	var res sql.Result
	if email == "" {
		res, err = tx.ExecContext(ctx, `UPDATE admin_operator SET email=NULL WHERE principal=$1`, r.Principal)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE admin_operator SET email=$2 WHERE principal=$1`, r.Principal, email)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "email already used")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if n == 0 {
		return nil, status.Error(codes.NotFound, "unknown operator")
	}
	// 审计：记 principal + 是否设置（email 是身份锚，非 secret；仍不记 secret）。
	if err := adminauthz.InsertAdminAudit(ctx, tx, sql.NullInt64{}, cp.OperatorFromContext(ctx),
		"set_email", "operator", r.Principal,
		auditJSON(map[string]any{"email_set": email != ""}), sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Version: uint64(ver), Changed: false}, nil
}
```

- [ ] **步骤 8：ruleTable +1** — `mgmt/authz.go`，在 `CreateOperator` 行下追加：

```go
	"/sydom.admin.v1.AdminService/SetOperatorEmail":         {"admin", "update", false, scopeSystem},
```

- [ ] **步骤 9：REST 路由** — `restgw/routes.go`，紧邻 `SetOperatorStatus` 路由后追加：

```go
		{"PUT", "/v1/operators/{principal}/email", pfx + "SetOperatorEmail",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.SetOperatorEmailRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				m.Principal = r.PathValue("principal")
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SetOperatorEmail(ctx, m.(*adminv1.SetOperatorEmailRequest))
			}},
```

- [ ] **步骤 10：运行确认通过** — `go test ./internal/controlplane/mgmt/ -run TestOperatorEmail -v`；预期 PASS。再 `go build ./...` 确认 restgw 编译。
- [ ] **步骤 11：Commit**

```bash
git add api/proto/ gen/ internal/controlplane/mgmt/ internal/controlplane/restgw/routes.go
git commit -m "feat(mgmt): CreateOperator 加 email + SetOperatorEmail RPC/REST（scopeSystem，additive）（M6-sso-2 T3）"
```

---

## 任务 4：Console 登录编排（store + ssologin + 一时态 + 编排 + 装配）

**文件：** 创建 `store/idp_login.go`(+test)、`ssologin/ssologin.go`(+test)、`console/oidcstate.go`、`console/oidc.go`(+`oidc_test.go`)；改 `console/auth.go`、`console/handler.go`、`console/handler_test.go`、`console/templates/login.html`、`app/config.go`、`app/run.go`。

### 4A：store 查询

- [ ] **步骤 1：写 store 测试（先失败）** — `internal/controlplane/store/idp_login_test.go`：

```go
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

	// 停用（status=2）→ 不命中。
	_, err = db.Exec(`UPDATE admin_operator SET status=2 WHERE id=$1`, opID)
	require.NoError(t, err)
	_, ok, err = store.OperatorEmailMatch(ctx, db, tA, "alice@acme.com")
	require.NoError(t, err)
	require.False(t, ok)
}
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/store/ -run TestIdPLogin -v`；预期编译失败。
- [ ] **步骤 3：写 `store/idp_login.go`**：

```go
package store

import (
	"context"
	"database/sql"
	"errors"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// IdPLoginRow 是登录编排所需的 IdP 配置行（client_secret 仍为密文，解密在 ssologin/控制面）。
type IdPLoginRow struct {
	TenantID        int64
	Issuer          string
	ClientID        string
	ClientSecretEnc []byte
	Enabled         bool
}

const idpLoginCols = `tenant_id, issuer, client_id, client_secret_enc, enabled`

func scanIdPLogin(row *sql.Row) (IdPLoginRow, bool, error) {
	var r IdPLoginRow
	err := row.Scan(&r.TenantID, &r.Issuer, &r.ClientID, &r.ClientSecretEnc, &r.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return IdPLoginRow{}, false, nil
	}
	if err != nil {
		return IdPLoginRow{}, false, err
	}
	return r, true, nil
}

// IdPLoginByDomain 按 email 域路由到租户 IdP（域全局唯一）。未命中→ok=false。
func IdPLoginByDomain(ctx context.Context, ex cp.DBTX, domain string) (IdPLoginRow, bool, error) {
	return scanIdPLogin(ex.QueryRowContext(ctx,
		`SELECT `+idpLoginCols+` FROM tenant_idp ti
		 JOIN tenant_idp_domain d ON d.tenant_id = ti.tenant_id
		 WHERE d.domain = $1`, domain))
}

// IdPLoginByTenant 按 tenantID 取 IdP（回调复用一时态 tenantID，避免信任回调参数）。
func IdPLoginByTenant(ctx context.Context, ex cp.DBTX, tenantID int64) (IdPLoginRow, bool, error) {
	return scanIdPLogin(ex.QueryRowContext(ctx,
		`SELECT `+idpLoginCols+` FROM tenant_idp WHERE tenant_id = $1`, tenantID))
}

// OperatorEmailMatch 严格映射：email 匹配的 active operator 且为 tenantID 有效成员 → principal。
// 任一不满足→ok=false（fail-close，无枚举 oracle）。email UNIQUE + 成员 PK 保至多一行。
func OperatorEmailMatch(ctx context.Context, ex cp.DBTX, tenantID int64, email string) (string, bool, error) {
	var principal string
	err := ex.QueryRowContext(ctx,
		`SELECT o.principal FROM admin_operator o
		 JOIN tenant_membership m ON m.operator_id = o.id
		 WHERE o.email = $1 AND o.status = 1 AND m.tenant_id = $2`, email, tenantID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return principal, true, nil
}
```

- [ ] **步骤 4：运行确认通过** — `go test ./internal/controlplane/store/ -run TestIdPLogin -v`；预期 PASS。

### 4B：ssologin 生产 resolver

- [ ] **步骤 5：写 ssologin 测试（先失败）** — `internal/controlplane/ssologin/ssologin_test.go`：

```go
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

	byT, ok, err := r.ResolveIdPByTenant(ctx, tid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "topsecret", byT.ClientSecret)

	p, ok, err := r.MatchOperatorForLogin(ctx, tid, "alice@acme.com")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "alice", p)
}
```

- [ ] **步骤 6：写 `ssologin/ssologin.go`**：

```go
// Package ssologin 是 SSO 登录的生产 resolver：把「域/租户→IdP 登录配置（解密后 client_secret）」
// 与「email→严格映射 operator」封装在控制面（持 masterKey）。INV-1：secret 解密不出本包。
package ssologin

import (
	"context"
	"database/sql"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// IdPLogin 是登录编排消费的 IdP 配置（含解密后的 ClientSecret 明文，仅进程内用）。
type IdPLogin struct {
	TenantID     int64
	Issuer       string
	ClientID     string
	ClientSecret string
	Enabled      bool
}

// Resolver 持 db+masterKey，实现 console 的窄接口（结构性满足）。
type Resolver struct {
	db        *sql.DB
	masterKey []byte
}

// NewResolver 构造，校验主密钥长度（fail-close）。
func NewResolver(db *sql.DB, masterKey []byte) (*Resolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, crypto.ErrKeySize
	}
	k := make([]byte, len(masterKey))
	copy(k, masterKey)
	return &Resolver{db: db, masterKey: k}, nil
}

func (r *Resolver) decrypt(row store.IdPLoginRow) (IdPLogin, error) {
	plain, err := crypto.Decrypt(r.masterKey, row.ClientSecretEnc)
	if err != nil {
		return IdPLogin{}, err
	}
	return IdPLogin{
		TenantID: row.TenantID, Issuer: row.Issuer, ClientID: row.ClientID,
		ClientSecret: string(plain), Enabled: row.Enabled,
	}, nil
}

// ResolveIdPByDomain 按 email 域路由 + 解密。
func (r *Resolver) ResolveIdPByDomain(ctx context.Context, domain string) (IdPLogin, bool, error) {
	row, ok, err := store.IdPLoginByDomain(ctx, r.db, domain)
	if err != nil || !ok {
		return IdPLogin{}, ok, err
	}
	out, err := r.decrypt(row)
	return out, err == nil, err
}

// ResolveIdPByTenant 按 tenantID 取 + 解密（回调用）。
func (r *Resolver) ResolveIdPByTenant(ctx context.Context, tenantID int64) (IdPLogin, bool, error) {
	row, ok, err := store.IdPLoginByTenant(ctx, r.db, tenantID)
	if err != nil || !ok {
		return IdPLogin{}, ok, err
	}
	out, err := r.decrypt(row)
	return out, err == nil, err
}

// MatchOperatorForLogin 委托 store 严格映射。
func (r *Resolver) MatchOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error) {
	return store.OperatorEmailMatch(ctx, r.db, tenantID, email)
}
```

- [ ] **步骤 7：运行确认通过** — `go test ./internal/controlplane/ssologin/ -v`；预期 PASS。

### 4C：Console 一时态 + 编排 + 装配

- [ ] **步骤 8：写 `console/oidcstate.go`** — Redis 一时态（一次性 GETDEL）：

```go
package console

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// oidcState 是发起→回调间的一时态（10min TTL，回调 GETDEL 一次性）。绝不含 secret/issuer。
type oidcState struct {
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	TenantID int64  `json:"tenant_id"`
	ReturnTo string `json:"return_to"`
}

func (s *RedisStore) oidcStateKey(state string) string { return "console:oidcstate:" + state }

// PutOIDCState 写一时态。
func (s *RedisStore) PutOIDCState(ctx context.Context, state string, v oidcState, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.oidcStateKey(state), raw, ttl).Err()
}

// TakeOIDCState 原子 GETDEL：命中即删（一次性，防重放）。未命中→ok=false。
func (s *RedisStore) TakeOIDCState(ctx context.Context, state string) (oidcState, bool, error) {
	if state == "" {
		return oidcState{}, false, nil
	}
	raw, err := s.rdb.GetDel(ctx, s.oidcStateKey(state)).Bytes()
	if errors.Is(err, redis.Nil) {
		return oidcState{}, false, nil
	}
	if err != nil {
		return oidcState{}, false, err
	}
	var v oidcState
	if err := json.Unmarshal(raw, &v); err != nil {
		return oidcState{}, false, err
	}
	return v, true, nil
}
```

- [ ] **步骤 9：改 `console/auth.go`** — 加窄接口 + Handler 字段：

```go
// idpLoginResolver 是发起/回调解析 IdP 登录配置的窄接口（生产由 *ssologin.Resolver 满足）。
type idpLoginResolver interface {
	ResolveIdPByDomain(ctx context.Context, domain string) (ssologin.IdPLogin, bool, error)
	ResolveIdPByTenant(ctx context.Context, tenantID int64) (ssologin.IdPLogin, bool, error)
}

// operatorMatcher 是 email→严格映射 operator 的窄接口。
type operatorMatcher interface {
	MatchOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error)
}
```
并在 `Handler` struct 加 4 字段：

```go
	idpResolver    idpLoginResolver
	operatorMatch  operatorMatcher
	oidcHTTP       *http.Client
	consoleBaseURL string
```
（`auth.go` 顶部导入 `"github.com/nickZFZ/Sydom/internal/controlplane/ssologin"`。）

- [ ] **步骤 10：写 `console/oidc.go`** 编排：

```go
package console

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nickZFZ/Sydom/internal/oidc"
)

const oidcCallbackPath = "/auth/oidc/callback"

// ssoFail 统一通用失败（无枚举 oracle：不区分域未配/验签失败/映射失败）。
func (h *Handler) ssoFail(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "login.html", http.StatusUnauthorized, map[string]any{"Error": "SSO 登录失败"})
}

// handleSSOStart：email 先行 → 域路由 → discovery → 生成 state/nonce/PKCE → 存一时态 → 302 到 IdP。
func (h *Handler) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		h.ssoFail(w, r)
		return
	}
	if h.consoleBaseURL == "" || h.idpResolver == nil {
		h.ssoFail(w, r) // fail-close：无 redirect_uri 基址 / 未装配 SSO
		return
	}
	idp, ok, err := h.idpResolver.ResolveIdPByDomain(r.Context(), email[at+1:])
	if err != nil || !ok || !idp.Enabled {
		h.ssoFail(w, r)
		return
	}
	pc, err := oidc.Discover(r.Context(), h.oidcHTTP, idp.Issuer)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	state, err1 := randToken()
	nonce, err2 := randToken()
	verifier, err3 := randToken()
	if err1 != nil || err2 != nil || err3 != nil {
		h.ssoFail(w, r)
		return
	}
	if err := h.sessions.PutOIDCState(r.Context(), state,
		oidcState{Nonce: nonce, Verifier: verifier, TenantID: idp.TenantID, ReturnTo: "/"},
		10*time.Minute); err != nil {
		h.ssoFail(w, r)
		return
	}
	authURL := oidc.AuthCodeURL(pc, idp.ClientID, h.consoleBaseURL+oidcCallbackPath,
		state, nonce, oidc.PKCEChallenge(verifier))
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleOIDCCallback：state 一次性 → 按一时态 tenantID 重取 IdP → 换 token → 验签 → 严格映射 → 会话。
func (h *Handler) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" {
		h.ssoFail(w, r)
		return
	}
	st, ok, err := h.sessions.TakeOIDCState(r.Context(), q.Get("state"))
	if err != nil || !ok {
		h.ssoFail(w, r) // CSRF + 一次性：未知/过期/重放
		return
	}
	if h.consoleBaseURL == "" || h.idpResolver == nil {
		h.ssoFail(w, r)
		return
	}
	idp, ok, err := h.idpResolver.ResolveIdPByTenant(r.Context(), st.TenantID)
	if err != nil || !ok || !idp.Enabled { // 期间被停用→拒
		h.ssoFail(w, r)
		return
	}
	pc, err := oidc.Discover(r.Context(), h.oidcHTTP, idp.Issuer)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	raw, err := oidc.Exchange(r.Context(), h.oidcHTTP, pc, idp.ClientID, idp.ClientSecret,
		h.consoleBaseURL+oidcCallbackPath, q.Get("code"), st.Verifier)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	vp := oidc.VerifyParams{Issuer: idp.Issuer, ClientID: idp.ClientID, Nonce: st.Nonce}
	jwks, err := oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	claims, err := oidc.VerifyIDToken(raw, jwks, vp, time.Now())
	if errors.Is(err, oidc.ErrUnknownKID) { // kid 未知→刷新 JWKS 重试一次
		if jwks, err = oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI); err == nil {
			claims, err = oidc.VerifyIDToken(raw, jwks, vp, time.Now())
		}
	}
	if err != nil || !claims.EmailVerified {
		h.ssoFail(w, r)
		return
	}
	principal, ok, err := h.operatorMatch.MatchOperatorForLogin(r.Context(), st.TenantID, strings.ToLower(claims.Email))
	if err != nil || !ok {
		h.ssoFail(w, r) // fail-close：非成员/停用/未知 email/跨租户
		return
	}
	id, _, err := h.sessions.Create(r.Context(), principal)
	if err != nil {
		h.renderError(w, r, codeInternal, "会话创建失败", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: id, Path: "/",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	returnTo := st.ReturnTo
	if !isSafeReturnTo(returnTo) {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// isSafeReturnTo 仅允许本站相对路径（防开放重定向）。
func isSafeReturnTo(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.Contains(p, "\\")
}
```
（注：`codeInternal` 与 `renderError` 已在 console 包存在，见 `errors.go`/`handleLoginPost` 用法；若常量名不同，与 `handleLoginPost` 保持一致。）

- [ ] **步骤 11：改 `console/handler.go`** — `NewHandler` 加 SSO 依赖 + 路由。定义并追加参数 `sso SSODeps`：

```go
// SSODeps 是 Console SSO 登录的注入依赖（run.go 装配；生产实现在 ssologin，持 db+masterKey）。
type SSODeps struct {
	Resolver       idpLoginResolver
	Matcher        operatorMatcher
	HTTPClient     *http.Client
	ConsoleBaseURL string
}
```
`NewHandler` 签名末尾加 `sso SSODeps`；构造 `h` 时填：
```go
	h := &Handler{srv: srv, resolver: resolver, enf: enf, db: db,
		sessions: sessions, logger: logger, cookieSecure: cookieSecure, templates: mustTemplates(),
		idpResolver: sso.Resolver, operatorMatch: sso.Matcher,
		oidcHTTP: sso.HTTPClient, consoleBaseURL: sso.ConsoleBaseURL}
```
在 `POST /login` 路由后加：
```go
	mux.HandleFunc("POST /login/sso", h.handleSSOStart)
	mux.HandleFunc("GET /auth/oidc/callback", h.handleOIDCCallback)
```

- [ ] **步骤 12：改 `console/handler_test.go`** — `newConsole` 的 `NewHandler(...)` 调用末尾加 `SSODeps{}`（默认无 SSO，既有测试不受影响）。**若** end-to-end 测试需要注入 mock，`newConsole` 可改成接受可选 `SSODeps`（见步骤 14）。
- [ ] **步骤 13：改 `login.html`** — 在既有 principal/secret 表单前加 email 先行 SSO 表单（保留密码表单作回退）：

```html
    <form method="post" action="/login/sso" class="stacked-form">
      <div class="form-field"><label for="sso_email">企业邮箱（SSO）</label>
        <input id="sso_email" name="email" type="email" autocomplete="email"></div>
      <button type="submit" class="btn btn-primary">用企业账号登录</button>
    </form>
    <div class="login-sep">或使用 Principal/Secret</div>
```

- [ ] **步骤 14：写端到端测试 `console/oidc_test.go`（mock IdP）** — 起 httptest IdP（discovery+JWKS+token endpoint 签真 RS256 token），驱动发起→回调→会话建立，并逐条断言安全不变量。要点（实现者据 `handler_test.go` 的 `newConsole`/`dbtest` 基建落地）：

```go
package console

import (
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

	"github.com/nickZFZ/Sydom/internal/controlplane/ssologin"
	"github.com/nickZFZ/Sydom/internal/crypto" // 包名 crypto：crypto.Encrypt(mk, plaintext)
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// mockIdP 起一个签发 RS256 ID Token 的最小 OIDC provider。
type mockIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	kid   string
	sub   string
	email string
	verif bool
	nonce string // 由回调注入的期望 nonce（测试从 authURL 提取后回填）
}

// ...（helper：/.well-known/openid-configuration 回 issuer=srv.URL + 三端点；
//      /jwks 回含 kid 的 RSA JWKS；/token 校验 basic auth + 返回签好的 id_token）
```
测试用例（每条独立，均经真实 Redis+DB）：
1. **正路径**：alice@acme.com 是 tenant A 的 active owner → 发起得 302 到 IdP authURL（提取 state/nonce）→ 请求回调（code+state）→ 得会话 cookie，`GET /` 200（已登录）。
2. **state 重放**：用同一 state 二次回调 → `ssoFail`（GETDEL 一次性）。
3. **跨租户**：email 属 A 但 operator 只在 B 有成员 → 拒。
4. **停用**：operator status=2 → 拒。
5. **未知 email**：IdP 返回 bob@acme.com（无对应 operator）→ 拒。
6. **email_verified=false** → 拒。
7. **IdP disabled**（回调前 `UPDATE tenant_idp SET enabled=false`）→ 拒。
8. **缺 consoleBaseURL**（SSODeps.ConsoleBaseURL="" 的 handler）发起 → `ssoFail`（fail-close）。

- [ ] **步骤 15：运行 console 全绿** — `go test ./internal/controlplane/console/ -v`；预期全绿（含既有测试 + 新 oidc_test 全部用例）。
- [ ] **步骤 16：改 `app/config.go`** — 加 `ConsoleBaseURL`（遵循既有 `firstNonEmpty(getenv(...), fc....)` env-override 模式）：
  - `Config` struct 加 `ConsoleBaseURL string`；
  - `fileConfig`（fc）加 `ConsoleBaseURL string \`yaml:"console_base_url"\``；
  - Config 字面量里加 `ConsoleBaseURL: firstNonEmpty(getenv("SYDOM_CONSOLE_BASE_URL"), fc.ConsoleBaseURL),`（放在 `ConsoleCookieInsecure` 行附近）。
  - 非必填（缺省空串）：空值不阻断启动，仅令 SSO 路由 fail-close（编排层步骤 10 已处理）。
- [ ] **步骤 17：改 `app/run.go`** — 装配（`console.NewHandler` 调用处，约 166 行）：

```go
	ssoResolver, err := ssologin.NewResolver(db, cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("sso resolver: %w", err)
	}
	oidcHTTP := &http.Client{Timeout: 10 * time.Second}
```
并把 `console.NewHandler(...)` 末尾加：
```go
		, console.SSODeps{Resolver: ssoResolver, Matcher: ssoResolver,
			HTTPClient: oidcHTTP, ConsoleBaseURL: cfg.ConsoleBaseURL})
```
（导入 `ssologin`；`net/http`/`time` 已在 run.go 导入。ssoResolver 同时满足两接口，故 Resolver 与 Matcher 同一实例。）

- [ ] **步骤 18：编译 + 装配自检** — `go build ./...`；`go test ./internal/controlplane/app/ ./internal/deploycfg/ -run Config -v`（若有配置测试）；预期通过。
- [ ] **步骤 19：变异自检（有齿证明，改后须复原）** — 临时删除 `store/idp_login.go` `OperatorEmailMatch` 中的 `m.tenant_id = $2` 条件（去掉租户成员绑定）：`go test ./internal/controlplane/console/ -run TestOIDC -v`（跨租户用例）；预期跨租户用例转 FAIL。**复原**后重跑恢复 PASS。
- [ ] **步骤 20：Commit**

```bash
git add internal/controlplane/store/idp_login.go internal/controlplane/store/idp_login_test.go \
        internal/controlplane/ssologin/ internal/controlplane/console/ \
        internal/controlplane/app/config.go internal/controlplane/app/run.go
git commit -m "feat(console): OIDC 登录编排（域路由/一时态/验签/严格映射→复用会话）+ 生产 resolver（M6-sso-2 T4）"
```

---

## 任务 5：全局验证 + 变异 + 零触碰核验

**文件：** 无新增；仅验证与（如需）修补。

- [ ] **步骤 1：全仓测试** — `go test ./...`；预期全绿（含 oidc/store/ssologin/console/mgmt/db 各包）。记录包数。
- [ ] **步骤 2：proto 破坏性门** — `make proto-breaking`；预期 PASS。
- [ ] **步骤 3：零触碰机器 diff** — 对本片起点基线核验授权求值核心零改（仅 authz.go +1 ruleTable 行）：

```bash
BASE=$(git merge-base HEAD origin/main)   # 或本片首个提交的父
git diff --stat "$BASE"..HEAD -- casbin/ \
  internal/controlplane/adminauthz/ \
  internal/sidecar/kernel/ internal/sidecar/dataperm/ 2>/dev/null
git diff "$BASE"..HEAD -- internal/controlplane/mgmt/authz.go
```
预期：前者**零输出**（无文件变更）；后者仅 `ruleTable` 追加 `SetOperatorEmail` 一行。若任一目录出现意外改动，回查并还原。

- [ ] **步骤 4：综合变异复核（抽查，改后复原）** — 逐一临时改、跑、复原，确认「测试有齿」：
  - 撤 `verify.go` alg 白名单（接受 `none`）→ `internal/oidc` none-alg 用例 FAIL。
  - 撤 `email_verified` 校验（`handleOIDCCallback` 去掉 `!claims.EmailVerified`）→ console email_verified 用例 FAIL。
  - 撤 `TakeOIDCState` 的 GETDEL 改为 GET（不删）→ console state-重放 用例 FAIL。
  每条确认 FAIL 后**立即复原**并重跑 PASS。
- [ ] **步骤 5：`go vet`** — `go vet ./internal/oidc/ ./internal/controlplane/ssologin/ ./internal/controlplane/store/ ./internal/controlplane/console/ ./internal/controlplane/mgmt/`；预期无告警。
- [ ] **步骤 6：Commit（如步骤 3/4 有修补）** — 否则本任务无独立提交。

```bash
# 仅当有修补时
git add -A && git commit -m "test(m6-sso-2): 零触碰核验 + 变异有齿复核修补（M6-sso-2 T5）"
```

---

## 自检（规格覆盖 / 占位符 / 类型一致）

**规格覆盖度（对照 spec §2 目标 / §9 不变量）：**
- §2.1 `internal/oidc` 独立离线 RP → 任务 2 ✓（注入 hc+now，httptest+测试密钥全离线）。
- §2.2 Console 编排（域路由→授权码→回调验签→严格映射→复用会话）→ 任务 4C ✓。
- §2.3 operator email 开通（CreateOperator email + SetOperatorEmail）→ 任务 3 ✓。
- §2.4 迁移 000025 → 任务 1 ✓。
- §2.5 fail-close + 零触碰 + 有齿测试 → 任务 4/5 ✓。
- §9 验签白名单/kty 绑定 → 任务 2；声明校验 → 任务 2；email_verified → 任务 4C 步骤 10；严格映射跨租户 → 任务 4A `OperatorEmailMatch`；state 一次性 → 任务 4C `TakeOIDCState`；IdP disabled → 任务 4C 两处 `!Enabled`；secret 不外泄 → ssologin 解密进程内 + 一时态不含 secret；开放重定向 → `isSafeReturnTo`；零触碰 → 任务 5 步骤 3。**全覆盖。**

**占位符扫描：** 任务 4 步骤 14 的 mockIdP helper 以注释描述而非完整代码——这是**唯一**结构性留白，因它是测试脚手架（实现者据既有 `handler_test.go` 基建落地），但已列出全部 8 条断言用例与签发要点，非行为逻辑留白。其余步骤均含可编译代码。实现者若需，mockIdP 可直接复用任务 2 的 `signRS256`/`jwksFor` 思路（RS256 + kid）。

**类型一致：** `IdPLogin`（ssologin）字段 `TenantID/Issuer/ClientID/ClientSecret/Enabled` 在 console 编排中一致引用；`oidcState` 字段 `Nonce/Verifier/TenantID/ReturnTo` 在 Put/Take 与编排间一致；`VerifyParams{Issuer,ClientID,Nonce}`、`IDTokenClaims{Email,EmailVerified,Sub}`、`ErrUnknownKID` 哨兵在验签与编排间一致；`store.IdPLoginRow` → `ssologin.decrypt` → `IdPLogin` 转换链一致。

---

## 落地顺序 / 依赖

任务 1→2→3→4→5 顺序执行。任务 2（oidc）与任务 3（operator email）无相互依赖，可互换；任务 4 依赖 1（email 列）+2（oidc 包）+3（若端到端测试建 operator 带 email，可用 SQL 直插避开对 3 的强依赖，但用 handler 更真实）。任务 5 依赖全部。**每任务独立 commit，本地 FF，push origin 待用户定。**
