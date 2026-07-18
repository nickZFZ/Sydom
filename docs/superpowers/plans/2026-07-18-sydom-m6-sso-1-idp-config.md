# M6-sso-1 每租户 OIDC IdP 配置地基 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 每租户可配置其 OIDC IdP（issuer/client_id/client_secret/email 域/启用），client_secret 加密存储读不泄露，email 域全局唯一，为下一片 OIDC 登录流铺路。

**架构：** 纯配置 CRUD + 加密，不触认证/登录路径。`tenant_idp`（一租户一 IdP）+ `tenant_idp_domain`（全局 UNIQUE 域）；`ConfigureTenantIdp`（租户 owner 自助 scopeTenant，加密 secret）+ `GetTenantIdp`（脱敏）。零触碰授权求值核心。

**技术栈：** Go 1.26、PostgreSQL（lib/pq）、golang-migrate、protobuf/buf、gRPC、internal/crypto（AES-256-GCM）、testcontainers。

规格：`docs/superpowers/specs/2026-07-18-sydom-m6-sso-1-idp-config-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `db/migrations/000024_tenant_idp.up.sql` | tenant_idp + tenant_idp_domain 表 | 创建 |
| `db/migrations/000024_tenant_idp.down.sql` | 对称回滚 | 创建 |
| `internal/db/tenant_idp_migration_test.go` | 迁移 + 域唯一约束 + down | 创建 |
| `internal/controlplane/store/tenant_idp.go` | TenantIdp 类型 + UpsertTenantIdpTx + TenantIdpOf | 创建 |
| `internal/controlplane/store/tenant_idp_test.go` | store TDD | 创建 |
| `api/proto/sydom/admin/v1/admin.proto` | ConfigureTenantIdp + GetTenantIdp + 消息 | 修改 |
| `gen/sydom/admin/v1/*.pb.go` | buf generate 产物 | 生成 |
| `internal/controlplane/mgmt/authz.go` | ruleTable 加 2 条目 | 修改 |
| `internal/controlplane/mgmt/sso.go` | ConfigureTenantIdp + GetTenantIdp handler | 创建 |
| `internal/controlplane/mgmt/sso_test.go` | handler TDD（配置/脱敏/域冲突/跨租户） | 创建 |
| `internal/controlplane/restgw/routes_accounts.go` | 两条 REST 路由 | 修改 |

---

## 任务 1：迁移 000024（tenant_idp + tenant_idp_domain）

**文件：**
- 创建：`db/migrations/000024_tenant_idp.up.sql`、`000024_tenant_idp.down.sql`
- 测试：`internal/db/tenant_idp_migration_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/db/tenant_idp_migration_test.go`（`package db`，用既有 `startPostgres`/`RunMigrations`/`MigrateDown`/`tableExists` harness）：
```go
package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000024_TenantIdp(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.True(t, tableExists(t, db, "tenant_idp"))
	require.True(t, tableExists(t, db, "tenant_idp_domain"))

	var t1, t2 int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-a') RETURNING id`).Scan(&t1))
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-b') RETURNING id`).Scan(&t2))

	// 一租户一 IdP：同租户第二条 tenant_idp→冲突。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://a','cid','\xab'::bytea)`, t1)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://a2','cid2','\xab'::bytea)`, t1)
	require.Error(t, err, "uq_tenant_idp_tenant 应拒同租户第二条 IdP")

	// 域全局唯一：不同租户抢同域→冲突。
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, t1)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, t2)
	require.Error(t, err, "uq_tenant_idp_domain 应拒跨租户同域")

	require.NoError(t, MigrateDown(dsn, migrationsSource))
	require.False(t, tableExists(t, db, "tenant_idp"))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestMigration000024 -count=1`
预期：FAIL——`tenant_idp` 表不存在。

- [ ] **步骤 3：编写迁移**

`db/migrations/000024_tenant_idp.up.sql`：
```sql
-- M6-sso-1：每租户 OIDC IdP 配置（一租户一 IdP）。client_secret 加密存储（AES-256-GCM）。
CREATE TABLE tenant_idp (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          BIGINT      NOT NULL REFERENCES tenant(id),
    issuer             TEXT        NOT NULL,
    client_id          TEXT        NOT NULL,
    client_secret_enc  BYTEA       NOT NULL,
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_idp_tenant UNIQUE (tenant_id)
);

-- email 域路由（下一片按域路由登录）；全局 UNIQUE 保「一域→一租户 IdP」不歧义。
CREATE TABLE tenant_idp_domain (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenant(id),
    domain     TEXT   NOT NULL,
    CONSTRAINT uq_tenant_idp_domain UNIQUE (domain)
);
```

`db/migrations/000024_tenant_idp.down.sql`：
```sql
DROP TABLE IF EXISTS tenant_idp_domain;
DROP TABLE IF EXISTS tenant_idp;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestMigration000024 -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000024_tenant_idp.up.sql db/migrations/000024_tenant_idp.down.sql internal/db/tenant_idp_migration_test.go
git commit -m "feat(db): 迁移 000024 tenant_idp + tenant_idp_domain（M6-sso-1）"
```

---

## 任务 2：store 层 —— TenantIdp + UpsertTenantIdpTx + TenantIdpOf

**文件：**
- 创建：`internal/controlplane/store/tenant_idp.go`
- 测试：`internal/controlplane/store/tenant_idp_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/store/tenant_idp_test.go`：
```go
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
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestUpsertTenantIdpTx|TestTenantIdpOf' -count=1`
预期：FAIL——`store.UpsertTenantIdpTx` / `store.TenantIdpOf` / `store.TenantIdp` 未定义。

- [ ] **步骤 3：实现**

`internal/controlplane/store/tenant_idp.go`：
```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// TenantIdp 是租户 OIDC IdP 配置的读视图（绝不含 client_secret——INV-1 不泄露）。
type TenantIdp struct {
	Configured bool
	Issuer     string
	ClientID   string
	Domains    []string
	Enabled    bool
}

// UpsertTenantIdpTx 在调用方事务内 upsert 本租户 IdP config + 替换其 email 域集合。
// domains 小写化写入；域被他租户占用→pq 23505（uq_tenant_idp_domain）由调用方映射。
func UpsertTenantIdpTx(ctx context.Context, tx cp.DBTX, tenantID int64,
	issuer, clientID string, secretEnc []byte, domains []string, enabled bool) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   issuer=EXCLUDED.issuer, client_id=EXCLUDED.client_id,
		   client_secret_enc=EXCLUDED.client_secret_enc, enabled=EXCLUDED.enabled, updated_at=now()`,
		tenantID, issuer, clientID, secretEnc, enabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM tenant_idp_domain WHERE tenant_id=$1`, tenantID); err != nil {
		return err
	}
	for _, d := range domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,$2)`,
			tenantID, strings.ToLower(strings.TrimSpace(d))); err != nil {
			return err // 域全局冲突→pq 23505
		}
	}
	return nil
}

// TenantIdpOf 读租户 IdP 元数据（不查 client_secret_enc）+ 聚合域。无配置→Configured=false。
func TenantIdpOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantIdp, error) {
	var t TenantIdp
	err := ex.QueryRowContext(ctx,
		`SELECT issuer, client_id, enabled FROM tenant_idp WHERE tenant_id=$1`, tenantID).
		Scan(&t.Issuer, &t.ClientID, &t.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantIdp{Configured: false}, nil
	}
	if err != nil {
		return TenantIdp{}, err
	}
	t.Configured = true
	rows, err := ex.QueryContext(ctx,
		`SELECT domain FROM tenant_idp_domain WHERE tenant_id=$1 ORDER BY domain`, tenantID)
	if err != nil {
		return TenantIdp{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return TenantIdp{}, err
		}
		t.Domains = append(t.Domains, d)
	}
	return t, rows.Err()
}
```
（`cp.DBTX` 接口已含 `QueryContext`/`QueryRowContext`/`ExecContext`〔`internal/controlplane/types.go:57-60`〕，`TenantIdpOf` 直接用无需扩展接口。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/store/ -run 'TestUpsertTenantIdpTx|TestTenantIdpOf' -count=1`
预期：PASS。再跑全 store 包：`go test ./internal/controlplane/store/ -count=1`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/tenant_idp.go internal/controlplane/store/tenant_idp_test.go
git commit -m "feat(store): TenantIdp + UpsertTenantIdpTx + TenantIdpOf（M6-sso-1）"
```

---

## 任务 3：proto —— ConfigureTenantIdp + GetTenantIdp

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/sydom/admin/v1/*.pb.go`

- [ ] **步骤 1：改 proto**

`api/proto/sydom/admin/v1/admin.proto`：
1. `AdminService` 加两个 RPC（与 ChangeTenantPlan 相邻）：
```proto
  // 企业 SSO：每租户 OIDC IdP 配置（M6-sso-1，scopeTenant 租户 owner 自助）。
  rpc ConfigureTenantIdp(ConfigureTenantIdpRequest) returns (ConfigureTenantIdpResponse);
  rpc GetTenantIdp(GetTenantIdpRequest) returns (GetTenantIdpResponse);
```
2. 加消息（放 ChangeTenantPlan 消息附近）：
```proto
message ConfigureTenantIdpRequest {
  uint64 tenant_id = 1;
  string issuer = 2;
  string client_id = 3;
  string client_secret = 4;    // 明文入参，服务端加密存储；GetTenantIdp 绝不回
  repeated string domains = 5; // email 域（服务端 lowercase）
  bool enabled = 6;
}
message ConfigureTenantIdpResponse {
  uint64 tenant_id = 1;
  bool enabled = 2;
}
message GetTenantIdpRequest { uint64 tenant_id = 1; }
message GetTenantIdpResponse {
  bool configured = 1;
  string issuer = 2;
  string client_id = 3;
  repeated string domains = 4;
  bool enabled = 5;
  // 绝不含 client_secret（INV-1）
}
```

- [ ] **步骤 2：生成 + 校验兼容**

运行：`make proto-gen && make proto-check && make proto-breaking`
预期：buf generate 无错、proto-check 无 drift、proto-breaking PASS（纯 additive）。

- [ ] **步骤 3：编译**

运行：`go build ./gen/... ./internal/...`
预期：EXIT 0。

- [ ] **步骤 4：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/sydom/admin/v1/
git commit -m "feat(proto): ConfigureTenantIdp + GetTenantIdp additive（M6-sso-1）"
```

---

## 任务 4：handler + ruleTable + REST

**文件：**
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable 两条目）
- 创建：`internal/controlplane/mgmt/sso.go`
- 测试：`internal/controlplane/mgmt/sso_test.go`
- 修改：`internal/controlplane/restgw/routes_accounts.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/sso_test.go`：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigureTenantIdp_EncryptsAndGetOmitsSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('sso-t') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s3cr3t", Domains: []string{"acme.com"}, Enabled: true,
	})
	require.NoError(t, err)

	// DB 里 client_secret 为密文，绝非明文。
	var enc []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc))
	require.NotContains(t, string(enc), "s3cr3t", "secret 须密文存储")

	// GetTenantIdp 回元数据但绝不含 secret（GetTenantIdpResponse 无 secret 字段，proto 保证）。
	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.True(t, got.Configured)
	require.Equal(t, "https://idp", got.Issuer)
	require.Equal(t, []string{"acme.com"}, got.Domains)
	require.True(t, got.Enabled)
}

func TestConfigureTenantIdp_DomainConflict_AlreadyExists(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var t1, t2 int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('a') RETURNING id`).Scan(&t1))
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('b') RETURNING id`).Scan(&t2))
	ctx := cp.WithOperator(context.Background(), "root")
	base := func(tid int64) *adminv1.ConfigureTenantIdpRequest {
		return &adminv1.ConfigureTenantIdpRequest{TenantId: uint64(tid), Issuer: "https://i",
			ClientId: "c", ClientSecret: "s", Domains: []string{"shared.com"}, Enabled: false}
	}
	_, err := srv.ConfigureTenantIdp(ctx, base(t1))
	require.NoError(t, err)
	_, err = srv.ConfigureTenantIdp(ctx, base(t2))
	require.Equal(t, codes.AlreadyExists, status.Code(err), "跨租户抢同域须 AlreadyExists")
}

func TestConfigureTenantIdp_MissingFields_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('c') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "", ClientId: "c", ClientSecret: "s", Domains: []string{"x.com"},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// 授权门：跨租户配置 IdP 须 PermissionDenied（scopeTenant）。
func TestConfigureTenantIdp_CrossTenant_Denied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	_ = appB
	var tB int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='tenant-b'`).Scan(&tB))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/ConfigureTenantIdp"
	req := &adminv1.ConfigureTenantIdpRequest{TenantId: uint64(tB), Issuer: "https://i", ClientId: "c", ClientSecret: "s", Domains: []string{"z.com"}}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "tenant-a 管理员配 tenant-b IdP 须拒")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp -count=1`
预期：FAIL——`srv.ConfigureTenantIdp` 未定义、ruleTable 无该 method。

- [ ] **步骤 3：实现**

`internal/controlplane/mgmt/authz.go`：ruleTable 加（与 ChangeTenantPlan 相邻）：
```go
	"/sydom.admin.v1.AdminService/ConfigureTenantIdp":        {"sso", "update", false, scopeTenant},
	"/sydom.admin.v1.AdminService/GetTenantIdp":              {"sso", "read", false, scopeTenant},
```

`internal/controlplane/mgmt/sso.go`（新建）：
```go
package mgmt

import (
	"context"
	"database/sql"
	"fmt"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConfigureTenantIdp upsert 本租户 OIDC IdP 配置（scopeTenant 租户 owner 自助）。
// client_secret 加密存储；域被他租户占用→AlreadyExists。
func (s *AdminServer) ConfigureTenantIdp(ctx context.Context, r *adminv1.ConfigureTenantIdpRequest) (*adminv1.ConfigureTenantIdpResponse, error) {
	if r.Issuer == "" || r.ClientId == "" || r.ClientSecret == "" || len(r.Domains) == 0 {
		return nil, status.Error(codes.InvalidArgument, "issuer, client_id, client_secret, domains required")
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(r.ClientSecret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId),
		r.Issuer, r.ClientId, enc, r.Domains, r.Enabled); err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "domain already claimed by another tenant")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	// 审计绝不含 client_secret（INV-1）。
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"configure_idp", "tenant_idp", fmt.Sprintf("%d", r.TenantId),
		auditJSON(map[string]any{"issuer": r.Issuer, "client_id": r.ClientId, "domains": r.Domains, "enabled": r.Enabled}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.ConfigureTenantIdpResponse{TenantId: r.TenantId, Enabled: r.Enabled}, nil
}

// GetTenantIdp 读本租户 IdP 元数据（脱敏，绝不回 client_secret）。
func (s *AdminServer) GetTenantIdp(ctx context.Context, r *adminv1.GetTenantIdpRequest) (*adminv1.GetTenantIdpResponse, error) {
	t, err := store.TenantIdpOf(ctx, s.db, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &adminv1.GetTenantIdpResponse{
		Configured: t.Configured, Issuer: t.Issuer, ClientId: t.ClientID,
		Domains: t.Domains, Enabled: t.Enabled,
	}, nil
}
```
（`isUniqueViolation` 已存在于 `accounts.go`，同包可用。）

`internal/controlplane/restgw/routes_accounts.go`：`accountRoutes()` 加两条：
```go
		{"PUT", "/v1/tenants/{tenant_id}/idp", pfx + "ConfigureTenantIdp",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.ConfigureTenantIdpRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				m.TenantId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ConfigureTenantIdp(ctx, m.(*adminv1.ConfigureTenantIdpRequest))
			}},
		{"GET", "/v1/tenants/{tenant_id}/idp", pfx + "GetTenantIdp",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetTenantIdpRequest{TenantId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetTenantIdp(ctx, m.(*adminv1.GetTenantIdpRequest))
			}},
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp -count=1`
预期：PASS。
运行：`go test ./internal/controlplane/mgmt/ ./internal/controlplane/restgw/ -count=1`
预期：PASS（apidoc/route 动态派生，无硬编码计数需改）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/sso.go internal/controlplane/mgmt/sso_test.go internal/controlplane/restgw/routes_accounts.go
git commit -m "feat(mgmt): ConfigureTenantIdp + GetTenantIdp（scopeTenant，加密+脱敏）+ REST（M6-sso-1）"
```

---

## 任务 5：全局验证 + 变异 + 零触碰

- [ ] **步骤 1：全仓测试 + 兼容门**

运行：
```bash
go build ./... && go vet ./...
go test ./... -count=1
make proto-breaking
```
预期：build/vet EXIT 0；`go test ./...` 全绿；proto-breaking PASS。

- [ ] **步骤 2：零触碰授权核心核验**

运行（基线=本片前最后提交，实现时用 `git rev-parse HEAD~<本片提交数>` 或 SSO spec 提交前的 hash）：
```bash
BASE=<M6-sso-1 前最后提交>
git diff --name-only $BASE -- ':!docs' | grep -iE 'casbin/|adminauthz/enforcer|/kernel/|dataperm|/authz\.go$'
```
预期：仅 `authz.go` 出现，且 diff 仅新增 2 行 ruleTable 配置条目（人工核确认无求值逻辑改动）。

- [ ] **步骤 3：变异实验证有齿（两处）**

变异 A（撤域唯一约束的映射）：临时把 ConfigureTenantIdp 的 `isUniqueViolation` 分支删掉（落 Internal）：
运行：`go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp_DomainConflict_AlreadyExists -count=1`
预期：FAIL（不再 AlreadyExists）。**还原**。

变异 B（撤 secret 加密）：临时把 handler 里 `enc` 改为 `[]byte(r.ClientSecret)`（明文存）：
运行：`go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp_EncryptsAndGetOmitsSecret -count=1`
预期：FAIL（DB 里含明文 "s3cr3t"）。**还原**。

- [ ] **步骤 4：确认工作树干净**

```bash
git status --short   # 变异均还原后应干净
```

---

## 自检记录

- **规格覆盖**：§4 数据模型→任务 1；§7 store→任务 2；§6 RPC→任务 3+4；§8 不变量→各任务测试 + 任务 5；§9 测试计划→逐任务 TDD + 任务 5 变异；§10 顺序→任务 1-5。
- **占位符**：无（proto tag 具体，新消息 tag 1 起）。
- **类型一致性**：`store.TenantIdp`（Configured/Issuer/ClientID/Domains/Enabled）在任务 2 定义、任务 4 handler 消费一致；`UpsertTenantIdpTx`/`TenantIdpOf` 签名跨任务一致；handler 用 `crypto.Encrypt`/`isUniqueViolation`/`InsertAdminAudit`/`auditJSON`（均既有，同包/同 crypto 包）。
- **接口核实**：`cp.DBTX` 已含 `QueryContext`（types.go:59），TenantIdpOf 直接用。
