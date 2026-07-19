# M6-sso-3 OIDC JIT 自动开通 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。子代理对每个任务遵循 superpowers:test-driven-development。

**目标：** 租户 owner 开启 `jit_enabled` 后，其 IdP 域下 email_verified 的**全新**用户首登 Console 时自动开通为**零权限成员**并复用会话；开关默认关时 M6-sso-2 事前严格映射一字不变。

**架构：** ①迁移 000026 `tenant_idp.jit_enabled`（默认 false）；②供应原语 `ssologin.ProvisionOperatorForLogin`（持 masterKey，仅全新 email 建 operator〔principal=email，随机 secret〕+membership〔TierMember〕+审计，**不 bump 策略版本**〔零 casbin 绑定〕）；③回调 `console/oidc.go` 严格映射未命中 + `idp.JITEnabled` → 尝试 JIT，否则/失败一律通用 401；④开关搭现有 `ConfigureTenantIdp`（additive proto），**`authz.go`/授权求值核心全零改**。

**技术栈：** Go stdlib（`crypto/rand`·`encoding/json`·`database/sql`），Postgres（testcontainers via `internal/dbtest`），Redis（`go-redis/v9`，`dbtest.StartRedis`），buf/protoc-gen-go，`stretchr/testify`。

---

## 关键既有事实（实现者必读，勿凭猜）

- **`admin_operator`**（`db/migrations/000013_admin_schema.up.sql`）：`principal VARCHAR(128) NOT NULL UNIQUE`、`secret_enc BYTEA NOT NULL`、`status SMALLINT NOT NULL DEFAULT 1`。**principal 128 字符上限**——email>128 的 INSERT 失败→fail-close（罕见，安全）。M6-sso-2 迁移 000025 加了 `email VARCHAR(320) UNIQUE`。
- **`tenant_idp`**（M6-sso-1，迁移 000024）：`tenant_id UNIQUE, issuer, client_id, client_secret_enc BYTEA, enabled BOOLEAN, created_at, updated_at`。
- **`tenant_membership`**：PK `(tenant_id, operator_id)`；`tier SMALLINT`。`adminauthz.TierMember int16 = 3`（此前「预留不签发」，本片首次签发）。
- **`store/tenant_idp.go`**（M6-sso-1）：`type TenantIdp struct{Configured bool; Issuer, ClientID string; Domains []string; Enabled bool}`；`UpsertTenantIdpTx(ctx, tx cp.DBTX, tenantID int64, issuer, clientID string, secretEnc []byte, domains []string, enabled bool) error`（INSERT tenant_idp + 替换域）；`TenantIdpOf(ctx, ex, tenantID) (TenantIdp, error)`（SELECT issuer, client_id, enabled；无配置→Configured=false）。
- **`store/idp_login.go`**（M6-sso-2）：`type IdPLoginRow struct{TenantID int64; Issuer, ClientID string; ClientSecretEnc []byte; Enabled bool}`；`const idpLoginCols = "ti.tenant_id, ti.issuer, ti.client_id, ti.client_secret_enc, ti.enabled"`；`scanIdPLogin(*sql.Row)(IdPLoginRow,bool,error)`；`IdPLoginByDomain`/`IdPLoginByTenant`（均用 idpLoginCols，别名 `ti`）；`OperatorEmailMatch(ctx, ex, tenantID, email)(string,bool,error)`。
- **`ssologin/ssologin.go`**（M6-sso-2）：`type IdPLogin struct{TenantID int64; Issuer, ClientID, ClientSecret string; Enabled bool}`；`type Resolver struct{db *sql.DB; masterKey []byte}`；`NewResolver(db, masterKey)`；`decrypt(row store.IdPLoginRow)(IdPLogin,error)`；`ResolveIdPByDomain`/`ResolveIdPByTenant`/`MatchOperatorForLogin`。
- **`console/oidc.go handleOIDCCallback`**（M6-sso-2）：`idp, ok, err := h.idpResolver.ResolveIdPByTenant(ctx, st.TenantID)` 已取 idp；之后 `principal, ok, err := h.operatorMatch.MatchOperatorForLogin(ctx, st.TenantID, lower(claims.Email))`；`if err != nil || !ok { h.ssoFail(w, r); return }`。`operatorMatcher` 私有接口在 `console/auth.go`。
- **`console/oidc_test.go`**（M6-sso-2）：`newMockIdP(t)`（签 RS256）、`newConsoleSSO(t, baseURL)(*httptest.Server,*sql.DB,[]byte)`、`seedIdP(t, db, mk, idpURL string, enabled bool) int64`、`seedOperator(t, db, principal, email string, status int16, membershipTenant int64)`、`ssoClient(t)`、`start(t,c,ts,email)(state,nonce)`、`callback(t,c,ts,code,state)*http.Response`、`hasSessionCookie(resp)bool`。
- **`adminauthz`**：`InsertMembership(ctx, q cp.DBTX, tenantID, operatorID int64, tier int16)(bool,error)`；`InsertAdminAudit(ctx, ex cp.DBTX, tenantID sql.NullInt64, operator, action, entityType, entityID string, diff []byte, adminVersion sql.NullInt64) error`；`TierMember int16`。
- **`crypto`**（`internal/crypto`，包名 `crypto`）：`KeySize=32`、`Encrypt(key, plaintext []byte)([]byte,error)`、`Decrypt`、`ErrKeySize`。
- **proto**：`ConfigureTenantIdpRequest` 字段 tenant_id=1..enabled=6；`GetTenantIdpResponse` configured=1..enabled=5。`make proto-gen`/`make proto-breaking`。`mgmt/sso.go` handler `ConfigureTenantIdp` 调 `store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId), r.Issuer, r.ClientId, enc, r.Domains, r.Enabled)`；`GetTenantIdp` 从 `store.TenantIdpOf` 映射响应。
- **迁移测试基建**（`internal/db`）：`startPostgres(t)`、`RunMigrations(dsn, migrationsSource)`、`MigrateDown(dsn, migrationsSource)`。
- **零触碰**：`casbin/`·`internal/controlplane/adminauthz/`（**只调公有函数不改源**）·`internal/sidecar/kernel/`·`internal/sidecar/dataperm/`·`mgmt/authz.go` 本片**全零 diff**。

## 文件结构

**新建：** `db/migrations/000026_tenant_idp_jit.up.sql`/`.down.sql`；`internal/db/tenant_idp_jit_migration_test.go`。
**修改：** `store/idp_login.go`(+test)、`store/tenant_idp.go`(+test)、`ssologin/ssologin.go`(+test)、`console/auth.go`、`console/oidc.go`、`console/oidc_test.go`、`api/proto/.../admin.proto`、`mgmt/sso.go`、`mgmt/sso_test.go`。**无 `authz.go` 改动。**

---

## 任务 1：迁移 000026（tenant_idp.jit_enabled）

**文件：** 创建 `db/migrations/000026_tenant_idp_jit.up.sql`、`.down.sql`；测试 `internal/db/tenant_idp_jit_migration_test.go`。

- [ ] **步骤 1：写迁移测试（先失败）**：

```go
package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000026_TenantIdpJIT(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('jit') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://i','cid','\xaa'::bytea)`, tid)
	require.NoError(t, err)

	// 默认 false（向后兼容）。
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&jit))
	require.False(t, jit, "jit_enabled 默认应 false")

	_, err = db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tid)
	require.NoError(t, err)

	require.NoError(t, MigrateDown(dsn, migrationsSource))
	_, err = db.Exec(`SELECT jit_enabled FROM tenant_idp LIMIT 1`)
	require.Error(t, err, "down 后 jit_enabled 列应不存在")
}
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/db/ -run TestMigration000026 -v`；预期 FAIL（列不存在）。
- [ ] **步骤 3：写 up** — `db/migrations/000026_tenant_idp_jit.up.sql`：

```sql
-- M6-sso-3：每租户 JIT 开关。默认 false=保留事前登录严格映射（向后兼容）。
ALTER TABLE tenant_idp ADD COLUMN jit_enabled BOOLEAN NOT NULL DEFAULT false;
```

- [ ] **步骤 4：写 down** — `db/migrations/000026_tenant_idp_jit.down.sql`：

```sql
ALTER TABLE tenant_idp DROP COLUMN jit_enabled;
```

- [ ] **步骤 5：运行确认通过** — `go test ./internal/db/ -run TestMigration000026 -v`；预期 PASS。
- [ ] **步骤 6：Commit**

```bash
git add db/migrations/000026_tenant_idp_jit.up.sql db/migrations/000026_tenant_idp_jit.down.sql internal/db/tenant_idp_jit_migration_test.go
git commit -m "feat(db): 迁移 000026 tenant_idp 加 jit_enabled（默认 false）（M6-sso-3 T1）"
```

---

## 任务 2：store + ssologin 供应原语

**文件：** 修改 `store/idp_login.go`（登录读路径带 jit_enabled + `InsertJITOperatorTx`）+ `ssologin/ssologin.go`（`IdPLogin.JITEnabled` + `ProvisionOperatorForLogin`）；测试 `store/idp_login_test.go`（追加）、`ssologin/ssologin_test.go`（追加）。

- [ ] **步骤 1：写 ssologin 测试（先失败）** — 追加到 `internal/controlplane/ssologin/ssologin_test.go`：

```go
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
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/ssologin/ -run TestProvision`；预期编译失败（方法未定义）。
- [ ] **步骤 3：store 加 `InsertJITOperatorTx`** — 追加到 `internal/controlplane/store/idp_login.go`：

```go
// InsertJITOperatorTx 建一个 SSO JIT operator（principal=email，status=1），返回 id。
// 事务内调用；principal/email UNIQUE 违例或 email>128 长度违例由调用方按 fail-close 处理。
func InsertJITOperatorTx(ctx context.Context, tx cp.DBTX, email string, secretEnc []byte) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO admin_operator (principal, secret_enc, email, status) VALUES ($1,$2,$1,1) RETURNING id`,
		email, secretEnc).Scan(&id)
	return id, err
}
```
并在登录读路径带出 jit_enabled：把 `idpLoginCols` 改为
```go
const idpLoginCols = `ti.tenant_id, ti.issuer, ti.client_id, ti.client_secret_enc, ti.enabled, ti.jit_enabled`
```
`IdPLoginRow` 加字段 `JITEnabled bool`；`scanIdPLogin` 的 Scan 末尾加 `&r.JITEnabled`：
```go
	err := row.Scan(&r.TenantID, &r.Issuer, &r.ClientID, &r.ClientSecretEnc, &r.Enabled, &r.JITEnabled)
```

- [ ] **步骤 4：ssologin `IdPLogin.JITEnabled` + `ProvisionOperatorForLogin`** — 修改 `internal/controlplane/ssologin/ssologin.go`：
  - `IdPLogin` 加字段 `JITEnabled bool`；`decrypt` 返回时加 `JITEnabled: row.JITEnabled`。
  - 顶部导入加 `"crypto/rand"`、`"encoding/json"`、`"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"`。
  - 追加方法：

```go
// ProvisionOperatorForLogin 仅在 email 完全未知时 JIT 开通零权限成员：
// 建 operator(principal=email, 随机 secret, status=1) + membership(TierMember) + 审计，不 bump 策略版本。
// 既有 email（含既有非成员）→ ok=false（fail-close，防跨租户账户接管）。
func (r *Resolver) ProvisionOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM admin_operator WHERE email=$1)`, email).Scan(&exists); err != nil {
		return "", false, err
	}
	if exists {
		return "", false, nil // 既有 email → 不 JIT
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", false, err
	}
	enc, err := crypto.Encrypt(r.masterKey, secret)
	if err != nil {
		return "", false, err
	}
	opID, err := store.InsertJITOperatorTx(ctx, tx, email, enc)
	if err != nil {
		return "", false, err // UNIQUE 竞态 / email>128 长度 → fail-close
	}
	if _, err := adminauthz.InsertMembership(ctx, tx, tenantID, opID, adminauthz.TierMember); err != nil {
		return "", false, err
	}
	diff, err := json.Marshal(map[string]any{"tenant_id": tenantID, "via": "sso_jit"})
	if err != nil {
		return "", false, err
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: tenantID, Valid: true}, "sso_jit", "jit_provision", "operator", email,
		diff, sql.NullInt64{}); err != nil {
		return "", false, err
	}
	// 不 BumpPolicyVersion：零 casbin 绑定=零策略变更，enforcer 无需重载。
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return email, true, nil
}
```
（`sql` 已在 ssologin 导入；`store`/`crypto` 已导入。）

- [ ] **步骤 5：运行 ssologin 通过** — `go test ./internal/controlplane/ssologin/`；预期 PASS。
- [ ] **步骤 6：store 测试带 jit 读字段** — 追加到 `internal/controlplane/store/idp_login_test.go` 一个断言（在既有 `TestIdPLoginByDomainAndOperatorMatch` 内，seed 后）：

```go
	// jit_enabled 读出（默认 false）。
	require.False(t, row.JITEnabled)
	_, err = db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tA)
	require.NoError(t, err)
	row2, ok2, err := store.IdPLoginByTenant(ctx, db, tA)
	require.NoError(t, err)
	require.True(t, ok2)
	require.True(t, row2.JITEnabled)
```

- [ ] **步骤 7：运行 store 通过** — `go test ./internal/controlplane/store/ -run TestIdPLogin`；预期 PASS。
- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/store/idp_login.go internal/controlplane/store/idp_login_test.go internal/controlplane/ssologin/
git commit -m "feat(ssologin): ProvisionOperatorForLogin（仅全新 email 建零权限成员）+ 读路径带 jit_enabled（M6-sso-3 T2）"
```

---

## 任务 3：回调 JIT 分支 + mock IdP 端到端

**文件：** 修改 `console/auth.go`（接口 +1 法）、`console/oidc.go`（回调 JIT 分支）；测试 `console/oidc_test.go`（追加 4 场景）。

- [ ] **步骤 1：写端到端测试（先失败）** — 追加到 `internal/controlplane/console/oidc_test.go`：

```go
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
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/console/ -run TestOIDCLogin_JIT`；预期 FAIL（JITProvisions 得 401，因回调无 JIT 分支）。
- [ ] **步骤 3：接口 +1 法** — `console/auth.go` 的 `operatorMatcher` 接口加：

```go
	ProvisionOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error)
```

- [ ] **步骤 4：回调 JIT 分支** — `console/oidc.go handleOIDCCallback`，把严格映射块改为：

```go
	principal, ok, err := h.operatorMatch.MatchOperatorForLogin(r.Context(), st.TenantID, strings.ToLower(claims.Email))
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	if !ok {
		// 严格映射未命中：租户显式开 JIT 则尝试自动开通（仅全新 email）。
		if idp.JITEnabled {
			principal, ok, err = h.operatorMatch.ProvisionOperatorForLogin(r.Context(), st.TenantID, strings.ToLower(claims.Email))
		}
		if err != nil || !ok {
			h.ssoFail(w, r) // JIT 关 / 既有 email / 竞态 → 通用 401（无枚举 oracle）
			return
		}
	}
```
（`idp` 变量已由上文 `ResolveIdPByTenant` 取得；`IdPLogin.JITEnabled` 由 T2 带出。）

- [ ] **步骤 5：运行确认通过** — `go test ./internal/controlplane/console/ -run TestOIDCLogin_JIT -v`；预期 4 场景全 PASS。
- [ ] **步骤 6：变异自检（有齿，改后复原）** — ① 临时删 `idp.JITEnabled` 门（改成 `if true {`）→ `go test -run TestOIDCLogin_JITDisabledRejectsUnknown`；预期红。复原。② 临时删 `ProvisionOperatorForLogin` 的 `if exists { return ... }`（ssologin.go）→ `go test -run TestOIDCLogin_JITRejectsExistingNonMember`；预期红。复原。各复原后重跑 PASS。
- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/auth.go internal/controlplane/console/oidc.go internal/controlplane/console/oidc_test.go
git commit -m "feat(console): OIDC 回调 JIT 分支（jit_enabled 开+全新 email→零权限开通）（M6-sso-3 T3）"
```

---

## 任务 4：开关配置面（additive proto + store + handler）

**文件：** 修改 `api/proto/.../admin.proto`、`store/tenant_idp.go`(+test)、`mgmt/sso.go`、`mgmt/sso_test.go`。

- [ ] **步骤 1：改 proto** — `api/proto/sydom/admin/v1/admin.proto`：
  - `ConfigureTenantIdpRequest` 末尾加 `bool jit_enabled = 7; // M6-sso-3：开启则该 IdP 域全新 email 首登 JIT 零权限开通`。
  - `GetTenantIdpResponse` 末尾加 `bool jit_enabled = 6;`。

- [ ] **步骤 2：生成 + 破坏性门** — `make proto-gen`（缺工具先 `make proto-tools`）；`make proto-breaking`（预期 PASS，纯 additive）。确认 `GetJitEnabled()` 生成。
- [ ] **步骤 3：写 mgmt 测试（先失败）** — 追加到 `internal/controlplane/mgmt/sso_test.go`：

```go
func TestConfigureTenantIdp_JITRoundtrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('jit-t') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)

	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.True(t, got.JitEnabled, "GetTenantIdp 须回显 jit_enabled")

	var jit bool
	require.NoError(t, db.QueryRow(`SELECT jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&jit))
	require.True(t, jit)
}
```

- [ ] **步骤 4：运行确认失败** — `go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp_JIT`；预期 FAIL（未透传，got.JitEnabled=false）。
- [ ] **步骤 5：store roundtrip jit_enabled** — `store/tenant_idp.go`：
  - `TenantIdp` struct 加 `JITEnabled bool`。
  - `UpsertTenantIdpTx` 签名末尾加 `jitEnabled bool`；INSERT 列加 `jit_enabled`、VALUES 加 `$6`（并把后续 domains 逻辑不变）、`ON CONFLICT DO UPDATE SET` 加 `jit_enabled=EXCLUDED.jit_enabled`：

```go
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled, jit_enabled)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   issuer=EXCLUDED.issuer, client_id=EXCLUDED.client_id,
		   client_secret_enc=EXCLUDED.client_secret_enc, enabled=EXCLUDED.enabled,
		   jit_enabled=EXCLUDED.jit_enabled, updated_at=now()`,
		tenantID, issuer, clientID, secretEnc, enabled, jitEnabled); err != nil {
		return err
	}
```
  - `TenantIdpOf` 的 SELECT 加 `jit_enabled`，Scan 加 `&t.JITEnabled`：

```go
	err := ex.QueryRowContext(ctx,
		`SELECT issuer, client_id, enabled, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tenantID).
		Scan(&t.Issuer, &t.ClientID, &t.Enabled, &t.JITEnabled)
```

- [ ] **步骤 6：handler 透传** — `mgmt/sso.go`：
  - `ConfigureTenantIdp` 的 `store.UpsertTenantIdpTx(...)` 调用末尾加 `r.JitEnabled`（`store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId), r.Issuer, r.ClientId, enc, r.Domains, r.Enabled, r.JitEnabled)`）。审计 diff 的 map 加 `"jit_enabled": r.JitEnabled`。
  - `GetTenantIdp` 的响应加 `JitEnabled: t.JITEnabled`。

- [ ] **步骤 7：store 单测（先失败→通过）** — 追加到 `internal/controlplane/store/tenant_idp_test.go` 一个 roundtrip 断言（Upsert 传 jitEnabled=true → TenantIdpOf 读回 JITEnabled=true）。运行 `go test ./internal/controlplane/store/`。
- [ ] **步骤 7b：更新既有 UpsertTenantIdpTx 调用方** — 新增 `jitEnabled` 参数破坏三处既有调用，均须补末位实参：
  - `mgmt/sso.go:38`（生产，步骤 6 已改为传 `r.JitEnabled`）；
  - `store/tenant_idp_test.go:19` 与 `:34`（两处测试直调 `UpsertTenantIdpTx`）末尾各补一个 `bool`（如 `false`，与该测试断言无关）。
  `go build ./...` 只抓生产调用方；测试调用方须 `go vet ./internal/controlplane/store/` 或 `go test` 编译时才暴露——务必一并改。
- [ ] **步骤 8：运行 mgmt + store 通过** — `go test ./internal/controlplane/store/ ./internal/controlplane/mgmt/ -run 'TestConfigureTenantIdp|TestTenantIdp|TestUpsert'`；预期 PASS。再 `go build ./...`。
- [ ] **步骤 9：Commit**

```bash
git add api/proto/ gen/ internal/controlplane/store/tenant_idp.go internal/controlplane/store/tenant_idp_test.go internal/controlplane/mgmt/sso.go internal/controlplane/mgmt/sso_test.go
git commit -m "feat(mgmt): ConfigureTenantIdp/GetTenantIdp jit_enabled 开关（additive，scopeTenant 自助）（M6-sso-3 T4）"
```

---

## 任务 5：全局验证 + 变异 + 零触碰核验

- [ ] **步骤 1：全仓测试** — `go test ./...`；预期全绿。
- [ ] **步骤 2：proto 破坏性门** — `make proto-breaking`；预期 PASS。
- [ ] **步骤 3：零触碰机器 diff** —

```bash
BASE=origin/main   # 本片起点基线
git diff --stat "$BASE"..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/
git diff "$BASE"..HEAD -- internal/controlplane/mgmt/authz.go
```
预期：二者**均空输出**（本片授权求值核心 + authz.go 全零改；供应仅调 adminauthz 公有函数不改源）。

- [ ] **步骤 4：综合变异复核（改后复原）** — ① 撤 console `idp.JITEnabled` 门→ JIT-关 测试红；② 撤 `ProvisionOperatorForLogin` 的 `if exists` 检查→ 既有非成员 测试红。各复原后 PASS。
- [ ] **步骤 5：`go vet`** — `go vet ./internal/controlplane/ssologin/ ./internal/controlplane/store/ ./internal/controlplane/console/ ./internal/controlplane/mgmt/`；预期无告警。
- [ ] **步骤 6：Commit（仅当步骤 3/4 有修补，否则无独立提交）**。

---

## 自检（规格覆盖 / 占位符 / 类型一致）

**规格覆盖度（对照 spec §2/§8）：** §2.1 迁移 000026→T1；§2.2 ProvisionOperatorForLogin→T2；§2.3 回调 JIT 分支→T3；§2.4 开关配置面→T4；§2.5 fail-close+零触碰+有齿→T3/T5。§8 门控（jit_enabled）→T3/T4；仅全新 email→T2 `if exists`；最小权限（零绑定）→T2/T3 断言；无枚举 oracle→T3 统一 401；零策略变更（不 bump）→T2；零触碰→T5。**全覆盖。**

**占位符扫描：** 无 TODO/待定；每步含可编译代码或精确命令。

**类型一致：** `IdPLoginRow.JITEnabled`/`IdPLogin.JITEnabled`/`TenantIdp.JITEnabled`（手写 Go 统一 `JITEnabled`）；proto 生成 `JitEnabled`/`GetJitEnabled()`；`ProvisionOperatorForLogin(ctx, tenantID int64, email string)(string,bool,error)` 在 ssologin 定义、console `operatorMatcher` 接口引用、handleOIDCCallback 调用三处签名一致；`adminauthz.TierMember`(int16=3)、`InsertMembership`/`InsertAdminAudit` 签名与既有一致。

## 落地顺序 / 依赖

T1→T2→T3→T4→T5。T3 依赖 T2（`IdPLogin.JITEnabled`+`ProvisionOperatorForLogin`）；T3 端到端 seed `jit_enabled` 用直接 SQL UPDATE（不依赖 T4 的 proto 开关）。T4 独立（配置面），可与 T3 互换但建议在后。每任务独立 commit，本地 FF，push origin 待用户定。
