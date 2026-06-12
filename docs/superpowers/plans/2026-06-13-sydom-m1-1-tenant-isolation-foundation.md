# M1.1 · 租户隔离基座（鉴权核心 + 跨租户安全矩阵）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在控制面 admin 鉴权核心中引入「租户域」作为 app 域之上的严格包含层——让租户管理员能管理本租户名下所有 app 的业务策略、跨租户在鉴权层物理 403，并用一张跨租户安全矩阵测试实证隔离正确性。

**架构：** 既有 admin 鉴权是 casbin RBAC-with-domain，授权域只有 `"*"`（super-admin/system）或 app 域字符串（`DomainOfAppID`=app_id）。本切片**纯增量**地加一层租户域 `"t:<tenant_id>"`：请求增加 `tdom` 字段，matcher 增加租户域析取项；`AuthorizeRule` 对带 app_id 的 RPC 解析出 app 所属租户域并一并传入。租户域是既有 `admin_subject_role.domain` / `admin_role_grant.domain`（VARCHAR(64)）上的纯约定，**无需 DB 迁移**。app 直绑模型与数据面（Sidecar）完全不动。

**技术栈：** Go 1.x、casbin v3.10.0、PostgreSQL（testcontainers 集成测试）、gRPC。

---

## 0. 背景与锁定决策

本计划是「司域生产就绪路线图」（`docs/superpowers/specs/2026-06-13-sydom-production-readiness-roadmap.md`，commit `d989045`，目前在 `worktree-feat+sp3-console` 分支；合 main 前路径以该 spec 为准）里程碑 **M1（多租户基座 + 端到端最薄业务旅程）** 的第一个子项目。M1 经范围检查拆为 5 子项目：

- **M1.1 — 租户隔离基座（本计划）**：鉴权核心的租户边界 + 租户管理员 bootstrap + 跨租户安全矩阵。
- M1.2 — 自助账户最小集（注册 + 首管理员 + 邀请成员）+ tenant-scoped 读（含 ListApplications/CreateApplication 租户化）。
- M1.3 — 最小业务功能 +「某人能做什么」有效权限视图。
- M1.4 — 一条业务语言薄运营台旅程。
- M1.5 — 最小可托管运维底座（TLS / 健康探针 / 可部署）。

**锁定决策（本轮用户确认）：**

1. **租户隔离路线 = 路线 A（租户域作为 app 域之上的包含层）。** 请求加 `tdom`，matcher 扩展，`AuthorizeRule` 解析 appDom + tenantDom。app 域模型 / 数据面 / 27 RPC 主体不动；tenant 作为严格包含层并入**同一** enforcer / ruleTable，最小爆炸半径。
2. **本次产出 = M1.1。** 后续 M1.2–M1.5 各自再起独立计划。

**贯穿一致性约束（carry-forward，来自既有工程范式）：**

- **一份授权真相**：租户边界并入既有 `AuthorizeRule` / `ruleTable`，**绝不**在 console / restgw 另起一套租户判定逻辑。
- **fail-close**：app 不存在 / tenantOf 查询失败 → 一律 `PermissionDenied`，绝不放行、绝不借 NotFound/FailedPrecondition 差异泄露 app 存在性。
- **casbin 论断已回源核实**（见 §3）。
- 跨包改签名后 `go vet ./...` 全仓兜底。

---

## 1. 关键实查结论（写代码前已核实，勿重新推导）

| 事实 | 出处 | 影响 |
|---|---|---|
| admin 鉴权域只有 `"*"` 或 app_id 字符串，**无租户域** | `internal/controlplane/adminauthz/enforcer.go:18-29` modelText | 租户域是全新增量 |
| `tenant` 表已存在；`application.tenant_id` FK 已存在 | `db/migrations/000001`,`000002` | **M1.1 无需迁移**，tenantOf 直查 application |
| `admin_subject_role.domain` / `admin_role_grant.domain` 为 VARCHAR(64)，接受任意字符串 | `db/migrations/000013` | `"t:<id>"`（≤21 字符）直接入列，无 schema 改动 |
| `adminauthz.Enforcer` 唯一生产调用方是 `mgmt.AuthorizeRule`；另有 `adminauthz/enforcer_test.go` | grep `.Enforce(` | Enforce 4→5 元爆炸半径仅此两处 |
| `AuthorizeRule` 有 13 处调用方（console×11 + restgw×1 + gRPC 拦截器×1） | grep `AuthorizeRule` | **保持 AuthorizeRule 签名不变** → 这 13 处零改动（见 §2 设计） |
| Sidecar `engine.Enforce`（数据面）是独立类型、独立模型 | `internal/sidecar/kernel/engine.go:62` | 数据面**不受影响**，租户层纯属 admin 面 |
| `EnsureRootOperator` 幂等播种 super-admin@* 的范式 | `internal/controlplane/adminauthz/operator.go:53-95` | `EnsureTenantAdmin` 镜像此范式 |
| 测试基建：`dbtest.SetupSchema(t)`（testcontainers PG，跑全量迁移）、`dbtest.SeedApp(t,db)`（建 'acme' 租户+1 app，返回 appID，**只能调一次**） | `internal/dbtest/dbtest.go` | 多租户矩阵需新增参数化 `SeedAppInTenant` |
| `crypto.KeySize=32`、`crypto.ErrKeySize`、`crypto.Encrypt` 已导出 | `internal/crypto/aesgcm.go` | bootstrap / 测试构造主密钥用 `bytes.Repeat([]byte{0x..}, crypto.KeySize)` |

---

## 2. 设计要点（锁定分解，逐任务依此实现）

**租户域字符串约定：** `TenantDomain(tenantID) = "t:" + strconv(tenantID)`。app 域是纯数字串，租户域带 `t:` 前缀，**天然不冲突**。

**模型变更（adminauthz modelText）：** 请求由 4 token 增至 5 token，policy 仍 4 token（grant 不加列），matcher 增加租户域析取。

**`AuthorizeRule` 签名保持不变（关键）：** tenantOf 解析挂在已持有 `db` 的 `*adminauthz.Enforcer` 上（新方法 `TenantDomainOf`）。`AuthorizeRule(ctx, enf, fullMethod, principal, req)` 只改函数体、不改签名 → console / restgw 13 处调用方零改动。仅 `Enforce` 签名变（唯一生产调用方就是 AuthorizeRule 自身）。

**租户管理员角色 = 单条通配 grant `(t:<id>, *, *)`**（镜像 super-admin 的 `(*,*,*)`，但锚定在租户域）。经 matcher，该通配**只**命中 app-scoped 操作；system RPC 在 `*` 域、不被 `t:` 域命中，故租户管理员止步于本租户业务策略，碰不到 SaaS 级 operator/admin-role 管理与 CreateApplication。DRY 且未来新增 app-scoped 资源自动覆盖。

**为何路线 A 是纯增量（向后兼容）：** matcher 第一析取项 `g(r.sub,p.sub,r.dom)` 保留——既有「直绑 app 域」的 operator（如现有测试里 alice@"7"）继续命中 r.dom 路径放行；既有 super-admin@`*` 继续命中第三析取项 `g(r.sub,p.sub,"*")`。租户域是**叠加**的第二析取项，不删除任何旧路径。

### 文件结构（创建 / 修改 + 职责）

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/controlplane/adminauthz/enforcer.go` | 修改 | modelText 加 `tdom` + matcher 析取；`Enforce` 4→5 元；新增 `TenantDomain` 函数 + `(*Enforcer).TenantDomainOf` 方法 |
| `internal/controlplane/adminauthz/enforcer_test.go` | 修改 | 既有调用 4→5 元；新增租户域 matcher 单测 + `TenantDomainOf` 单测 |
| `internal/controlplane/mgmt/authz.go` | 修改 | `AuthorizeRule` 函数体：解析 tdom（system→`"*"`；app-scoped→`TenantDomainOf`，miss→`PermissionDenied`），调 5 元 Enforce。**签名不变** |
| `internal/dbtest/dbtest.go` | 修改 | 新增 `SeedAppInTenant(t, conn, tenantName, domain, appKey) (tenantID, appID int64)` 参数化多租户播种 |
| `internal/controlplane/adminauthz/operator.go` | 修改 | 新增 `EnsureTenantAdmin`（幂等 bootstrap 租户管理员）+ 内部 `ensureOperatorTx` |
| `internal/controlplane/adminauthz/operator_test.go` | 修改 | 新增 `EnsureTenantAdmin` 单测（幂等 + 绑定生效） |
| `internal/controlplane/mgmt/tenant_authz_test.go` | 创建 | 跨租户安全矩阵测试（M1.1 退风险验收 artifact） |

**非目标（M1.1 边界，明确不做，留给后续）：**

- ListApplications / CreateApplication 的租户化（枚举读 + 租户管理员自建 app）→ M1.2。M1.1 中这两个 RPC 维持 `system:true`（`*` 域），租户管理员调用得 403——**安全无泄露**（fail-close，仅是暂缺能力）。
- 租户自助注册 / 邀请成员 / owner/admin/member 分层 → M1.2。M1.1 用 `EnsureTenantAdmin`（bootstrap/seeder 路径，非自助 RPC）产出矩阵的真实主体。
- 数据面（Sidecar）租户化 → 不在 M1 范围（数据面已按 app 域隔离）。
- DB 迁移 → 无（见 §1）。

---

## 3. casbin v3.10.0 行为核实（已回源，勿再推测）

依据 `casbin/enforcer.go`：

- **L743-745**：`rTokens` 按 `model["r"][rType].Tokens` 的名字建索引；请求 token 名即 matcher 里的 `r.<name>`。→ 在 `request_definition` 加 `tdom` 即得可用的 `r.tdom`（L1060 按名解析）。
- **L788-792**：`if len(model["r"][rType].Tokens) != len(rvals)` → 报 `"invalid request size: expected N, got M"`。→ 模型变 5 token 后，**所有** `Enforce` 调用必须严格传 5 个 rvals（`sub, dom, tdom, res, act`），缺一即 error。
- `g(r.sub, p.sub, r.dom)` 与新增的 `g(r.sub, p.sub, r.tdom)` 是**同一个** 3 元 `g` 角色函数传不同 domain 值；casbin `DomainManager` 按域键管理角色管理器，同一 g 多次传不同域是标准用法（既有 matcher 已有 `g(...,r.dom) || g(...,"*")` 双调用）。

结论：模型加 `tdom` + matcher 多析取 + Enforce 5 元，casbin 原生支持；唯一硬约束是 rvals 数必须等于 token 数。

---

## 任务 1：鉴权核心加租户域（adminauthz 模型 + Enforce 5 元 + TenantDomain/TenantDomainOf + mgmt AuthorizeRule 解析 tdom）

> 原子任务：Enforce 签名变更是不可分割的——一改模型 token 数，4 元 Enforce 即触发 "invalid request size"。故 adminauthz 改动与其唯一生产调用方 `AuthorizeRule` 必须同一提交落地，结束时全仓可编译、相关包测试全绿。

**文件：**
- 修改：`internal/controlplane/adminauthz/enforcer.go`
- 修改：`internal/controlplane/adminauthz/enforcer_test.go`
- 修改：`internal/controlplane/mgmt/authz.go:62-84`（仅函数体）

- [ ] **步骤 1：先写失败的 adminauthz 单测（租户域 matcher + TenantDomainOf）**

把 `internal/controlplane/adminauthz/enforcer_test.go` 中 `TestEnforcer_Matrix` 与 `TestEnforcer_ReloadOnVersionBump` 的所有 `Enforce` 调用由 4 元改 5 元（插入 `tdom` 参数），并新增两个测试。改后**完整文件**如下：

```go
package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestEnforcer_Matrix(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))
	sid, _ := adminauthz.InsertOperator(ctx, db, "bob", []byte("x"))
	var superID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&superID))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, sid, superID, "*"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// alice 直绑 app 域 "7"：r.dom 路径放行（tdom 在此无关，传 ""）。
	allow, err := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.NoError(t, err)
	require.True(t, allow)
	deny, _ := enf.Enforce(ctx, "alice", "9", "", "role", "create")
	require.False(t, deny, "跨 app 域必须拒绝")
	deny2, _ := enf.Enforce(ctx, "alice", "7", "", "application", "create")
	require.False(t, deny2, "未授予的资源必须拒绝")

	for _, dom := range []string{"7", "9", "*"} {
		ok, _ := enf.Enforce(ctx, "bob", dom, "", "application", "create")
		require.True(t, ok, "super-admin 在域 %s 应放行", dom)
	}

	no, _ := enf.Enforce(ctx, "ghost", "7", "", "role", "create")
	require.False(t, no)

	empty1, _ := enf.Enforce(ctx, "", "7", "", "role", "create")
	require.False(t, empty1, "空 principal 必须拒绝")
	empty2, _ := enf.Enforce(ctx, "alice", "", "", "role", "create")
	require.False(t, empty2, "空 domain 必须拒绝")
}

func TestEnforcer_ReloadOnVersionBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	ok, _ := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.False(t, ok)

	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	ok2, _ := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.True(t, ok2, "版本 bump 后应重载并放行")
}

// 新增：租户域作为 app 域之上的包含层——租户管理员在 t:<id> 的通配 grant
// 覆盖其名下任意 app；跨租户必须拒绝。
func TestEnforcer_TenantDomainContainment(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "tadmin-5", "n")
	// 通配 grant 锚定在租户域 t:5。
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "t:5", "*", "*"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "t:5"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// 本租户名下任意 app 域（"42"）+ 租户域 t:5 → 放行（经 r.tdom 析取项）。
	ok, err := enf.Enforce(ctx, "alice", "42", "t:5", "role", "create")
	require.NoError(t, err)
	require.True(t, ok, "租户管理员对本租户 app 应放行")
	ok2, _ := enf.Enforce(ctx, "alice", "100", "t:5", "data_policy", "update")
	require.True(t, ok2, "通配 grant 覆盖本租户全部 app-scoped 资源/动作")

	// 跨租户：app 域 "99" + 租户域 t:7 → 拒绝（无任何析取项命中）。
	deny, _ := enf.Enforce(ctx, "alice", "99", "t:7", "role", "create")
	require.False(t, deny, "跨租户必须拒绝")
	// system 域（"*"）→ 租户管理员的 t:5 通配不命中，拒绝。
	denySys, _ := enf.Enforce(ctx, "alice", "*", "*", "admin", "create")
	require.False(t, denySys, "租户管理员不得触达 SaaS 级 system 域")
}

func TestEnforcer_TenantDomainOf(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db) // 建 'acme' 租户 + 1 app
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	td, err := enf.TenantDomainOf(ctx, appID)
	require.NoError(t, err)
	require.Equal(t, adminauthz.TenantDomain(tenantID), td)

	_, err = enf.TenantDomainOf(ctx, 999999) // 不存在 → fail-close error
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestEnforcer' 2>&1 | head -30`
预期：编译失败——`Enforce` 实参数量不符、`TenantDomainOf`/`TenantDomain` undefined。

- [ ] **步骤 3：改 adminauthz/enforcer.go（模型 + Enforce 5 元 + TenantDomain/TenantDomainOf）**

a) 把 `modelText`（当前 enforcer.go:18-29）整体替换为：

```go
// modelText 是 admin 鉴权的 RBAC-with-domain 模型。
// 租户域设计：tdom 是 app 所属租户的域（"t:<tenant_id>"），作为 app 域之上的包含层。
//   - g(r.sub,p.sub,r.dom) —— app 直绑（既有路径，向后兼容）；
//   - g(r.sub,p.sub,r.tdom) —— 租户管理员经租户域命中其名下所有 app（新增包含层）；
//   - g(r.sub,p.sub,"*") —— super-admin 在 * 域的兜底；
//   - p.dom/p.res/p.act == "*" —— 通配 grant（super-admin 的 * 行、租户管理员的 t:<id> 行）。
const modelText = `
[request_definition]
r = sub, dom, tdom, res, act
[policy_definition]
p = sub, dom, res, act
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = (g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, r.tdom) || g(r.sub, p.sub, "*")) && (p.dom == r.dom || p.dom == r.tdom || p.dom == "*") && (p.res == r.res || p.res == "*") && (p.act == r.act || p.act == "*")
`
```

b) 把 `Enforce` 方法（当前 enforcer.go:96-110）的签名与末行改为 5 元：

```go
// Enforce 鉴权：先比对 admin_policy_version，若变化则重载策略，再求值。
// 必须传 5 个值（sub, dom, tdom, res, act），与 5-token 请求定义匹配，
// 否则 casbin 报 "invalid request size"。fail-close 由调用方据 err 拒绝。
func (en *Enforcer) Enforce(ctx context.Context, sub, dom, tdom, res, act string) (bool, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	cur, err := ReadPolicyVersion(ctx, en.db)
	if err != nil {
		return false, err
	}
	if cur != en.loadedV {
		if err := en.e.LoadPolicy(); err != nil {
			return false, fmt.Errorf("adminauthz: reload policy: %w", err)
		}
		en.loadedV = cur
	}
	return en.e.Enforce(sub, dom, tdom, res, act)
}
```

c) 在文件末尾追加 `TenantDomain` + `TenantDomainOf`：

```go
// TenantDomain 把 tenant_id 转成租户域字符串。"t:" 前缀与纯数字 app 域天然不冲突。
func TenantDomain(tenantID int64) string { return "t:" + strconv.FormatInt(tenantID, 10) }

// TenantDomainOf 查 app 所属租户并返回其租户域。app 不存在/查询失败 → error
// （fail-close：调用方据此拒绝；绝不放行，也绝不借差异泄露 app 存在性）。
func (en *Enforcer) TenantDomainOf(ctx context.Context, appID int64) (string, error) {
	var tenantID int64
	if err := en.db.QueryRowContext(ctx,
		`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID); err != nil {
		return "", fmt.Errorf("adminauthz: tenant of app %d: %w", appID, err)
	}
	return TenantDomain(tenantID), nil
}
```

d) 在 import 块加入 `"strconv"`（与既有 `"context"`、`"database/sql"`、`"fmt"`、`"sync"` 并列）。

- [ ] **步骤 4：运行 adminauthz 测试验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -count=1 2>&1 | tail -20`
预期：PASS（含新增 `TestEnforcer_TenantDomainContainment`、`TestEnforcer_TenantDomainOf`）。

- [ ] **步骤 5：改 mgmt/authz.go 的 AuthorizeRule 函数体（解析 tdom，签名不变）**

把 `AuthorizeRule`（当前 authz.go:65-84）函数体替换为：

```go
func AuthorizeRule(ctx context.Context, enf *adminauthz.Enforcer, fullMethod, principal string, req any) (context.Context, error) {
	rule, known := ruleTable[fullMethod]
	if !known {
		return nil, status.Error(codes.PermissionDenied, "unknown method")
	}
	domain, tdom := "*", "*"
	if !rule.system {
		g, ok := req.(appIDGetter)
		if !ok {
			return nil, status.Error(codes.Internal, "request missing app_id")
		}
		appID := int64(g.GetAppId())
		domain = DomainOfAppID(appID)
		td, err := enf.TenantDomainOf(ctx, appID)
		if err != nil {
			// app 不存在/查询失败：fail-close 为 PermissionDenied，不泄露存在性差异。
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}
		tdom = td
	}
	allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action)
	// TODO(observability): Enforce 内部错误（DB/策略加载故障）当前与"权限不足"一并 fail-close 为 PermissionDenied；接入日志/metric 后在此区分并记录。
	if err != nil || !allow {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return cp.WithOperator(ctx, principal), nil
}
```

并把函数上方注释（authz.go:62-64）首句更新为：

```go
// AuthorizeRule 据 ruleTable[fullMethod] 计算授权域：system→("*","*")；否则取 req 的
// app_id 算 app 域，并查其所属租户得租户域 tdom（app 不存在→fail-close 拒绝），
// 调 enf.Enforce（5 元）。gRPC 拦截器、REST 网关、Console 共用，ruleTable 为唯一真相源。
```

- [ ] **步骤 6：全仓编译 + mgmt 测试验证通过**

运行：`go build ./... && go test ./internal/controlplane/mgmt/ -count=1 2>&1 | tail -25`
预期：build 无错；mgmt 测试全绿（既有 `TestAuthorizeRule_AppDomainAndDeny`、拦截器测试不受影响——它们用 `dbtest.SeedApp` 建了真实 app，tenantOf 能解析；`AppId:999` 的 deny 用例因 tenantOf miss 仍返回 `PermissionDenied`，断言不变）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/adminauthz/enforcer.go internal/controlplane/adminauthz/enforcer_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(adminauthz): 鉴权核心加租户域包含层(请求加tdom/matcher析取/Enforce5元/TenantDomainOf)

AuthorizeRule 签名不变，console/restgw 调用方零改动；tenantOf 挂在持db的Enforcer上。
纯增量：app直绑与super-admin旧路径保留，租户域为叠加析取项。casbin v3.10.0回源核实。

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：dbtest 参数化多租户播种 helper

**文件：**
- 修改：`internal/dbtest/dbtest.go`

- [ ] **步骤 1：先写失败的 helper 自测（验证返回非零 tenantID/appID 且同库可多次调）**

在 `internal/dbtest/dbtest.go` **所属包外**新建 `internal/dbtest/seed_test.go`（dbtest 当前可能无测试；此自测确保 helper 可用且支持多租户）：

```go
package dbtest_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestSeedAppInTenant_MultiTenant(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-a", "app-a", "AK_a")
	tB, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "app-b", "AK_b")
	require.NotZero(t, tA)
	require.NotZero(t, appA)
	require.NotEqual(t, tA, tB, "两个租户必须不同")
	require.NotEqual(t, appA, appB, "两个应用必须不同")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/dbtest/ -run TestSeedAppInTenant_MultiTenant 2>&1 | head -15`
预期：编译失败——`SeedAppInTenant` undefined。

- [ ] **步骤 3：实现 SeedAppInTenant**

在 `internal/dbtest/dbtest.go` 末尾追加（紧随既有 `SeedApp` 之后）：

```go
// SeedAppInTenant 建一个租户 + 其下一个应用，返回 (tenantID, appID)。
// 与 SeedApp 不同：参数化 name/domain/app_key，支持同库播种多租户多应用（跨租户隔离测试用）。
func SeedAppInTenant(t *testing.T, conn *sql.DB, tenantName, domain, appKey string) (int64, int64) {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, conn.QueryRow(
		`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, tenantName).Scan(&tenantID))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1, $2, $3, $4, '\xab'::bytea) RETURNING id`,
		tenantID, domain, tenantName+"-app", appKey).Scan(&appID))
	return tenantID, appID
}
```

（`sql`、`testing`、`require` 已在 dbtest.go import；无需新增。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/dbtest/ -run TestSeedAppInTenant_MultiTenant -count=1 2>&1 | tail -10`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/dbtest/dbtest.go internal/dbtest/seed_test.go
git commit -m "test(dbtest): 新增参数化 SeedAppInTenant 多租户播种 helper

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：EnsureTenantAdmin 租户管理员 bootstrap

**文件：**
- 修改：`internal/controlplane/adminauthz/operator.go`
- 修改：`internal/controlplane/adminauthz/operator_test.go`

- [ ] **步骤 1：先写失败的单测**

在 `internal/controlplane/adminauthz/operator_test.go` 末尾追加（若该测试文件不存在或包名不同，新建 `operator_ensure_tenant_test.go`，包 `adminauthz_test`）：

```go
func TestEnsureTenantAdmin_BindsAndIsIdempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tID, appID := dbtest.SeedAppInTenant(t, db, "acme", "order", "AK_o")

	// 首次：建 operator + 租户角色 + 绑定。
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("s3cr3t")))
	// 再次：幂等，不报错、不重复。
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("s3cr3t")))

	var opCount, bindCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='alice'`).Scan(&opCount))
	require.Equal(t, 1, opCount, "幂等：operator 不重复")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='alice' AND sr.domain=$1`, adminauthz.TenantDomain(tID)).Scan(&bindCount))
	require.Equal(t, 1, bindCount, "幂等：租户域绑定不重复")

	// 绑定生效：alice 经 AuthorizeRule 应能管理本租户 app（这里直接用 enforcer 验证）。
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	ok, err := enf.Enforce(ctx, "alice", adminauthz.TenantDomain(tID), adminauthz.TenantDomain(tID), "role", "create")
	require.NoError(t, err)
	require.True(t, ok)
	_ = appID
}
```

确保该测试文件 import 含：`bytes`、`context`、`testing`、`github.com/nickZFZ/Sydom/internal/controlplane/adminauthz`、`github.com/nickZFZ/Sydom/internal/crypto`、`github.com/nickZFZ/Sydom/internal/dbtest`、`github.com/stretchr/testify/require`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run TestEnsureTenantAdmin 2>&1 | head -15`
预期：编译失败——`EnsureTenantAdmin` undefined。

- [ ] **步骤 3：实现 EnsureTenantAdmin + ensureOperatorTx**

在 `internal/controlplane/adminauthz/operator.go` 末尾追加（`EnsureRootOperator` 之后）：

```go
// ensureOperatorTx 幂等取/建 operator，返回 id。事务内调用。
func ensureOperatorTx(ctx context.Context, tx *sql.Tx, masterKey []byte, principal string, secret []byte) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM admin_operator WHERE principal=$1`, principal).Scan(&id)
	if err == nil {
		return id, nil // 已存在：不覆盖凭据
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("adminauthz: find operator: %w", err)
	}
	enc, err := crypto.Encrypt(masterKey, secret)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: encrypt secret: %w", err)
	}
	return InsertOperator(ctx, tx, principal, enc)
}

// EnsureTenantAdmin 幂等播种某租户的租户管理员：
//   - operator(principal) 不存在则建（masterKey 加密初始 secret）；已存在不覆盖凭据；
//   - 租户专属角色（code=tenant-admin-<tenantID>）在 t:<tenantID> 域授单条通配 (*,*)；
//   - 绑定 operator → 角色 @ t:<tenantID>；
//   - bump 版本触发 enforcer 重载。
//
// 通配 (t:<id>,*,*) 经 matcher 仅命中 app-scoped 操作（system RPC 在 * 域，不被 t: 域命中），
// 故租户管理员止步于本租户业务策略，碰不到 SaaS 级 operator/admin-role 管理与 CreateApplication。
func EnsureTenantAdmin(ctx context.Context, db *sql.DB, masterKey []byte, tenantID int64, principal string, secret []byte) error {
	if len(masterKey) != crypto.KeySize {
		return crypto.ErrKeySize
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauthz: begin: %w", err)
	}
	defer tx.Rollback()

	opID, err := ensureOperatorTx(ctx, tx, masterKey, principal, secret)
	if err != nil {
		return err
	}

	code := fmt.Sprintf("tenant-admin-%d", tenantID)
	var roleID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM admin_role WHERE code=$1`, code).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		roleID, err = InsertRole(ctx, tx, code, fmt.Sprintf("租户%d管理员", tenantID))
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("adminauthz: find tenant-admin role: %w", err)
	}

	dom := TenantDomain(tenantID)
	if err := InsertRoleGrant(ctx, tx, roleID, dom, "*", "*"); err != nil {
		return err
	}
	if err := InsertSubjectRole(ctx, tx, opID, roleID, dom); err != nil {
		return err
	}
	if err := BumpPolicyVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauthz: commit tenant admin: %w", err)
	}
	return nil
}
```

确认 `operator.go` 的 import 已含 `"errors"`、`"database/sql"`、`"fmt"`、`"github.com/nickZFZ/Sydom/internal/crypto"`（`EnsureRootOperator` 已用这些；`InsertRole`/`InsertRoleGrant`/`InsertSubjectRole`/`BumpPolicyVersion`/`TenantDomain` 同包，直接可用）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run TestEnsureTenantAdmin -count=1 2>&1 | tail -15`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/operator.go internal/controlplane/adminauthz/operator_ensure_tenant_test.go
git commit -m "feat(adminauthz): EnsureTenantAdmin 幂等播种租户管理员(t:<id>域通配grant+绑定)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：跨租户安全矩阵测试（M1.1 退风险验收 artifact）

> 这是 M1.1 的验收物：经**共用** `AuthorizeRule` 核心，实证「租户管理员管本租户、跨租户 403、root 全放行、租户管理员碰不到 system 域」。

**文件：**
- 创建：`internal/controlplane/mgmt/tenant_authz_test.go`

- [ ] **步骤 1：写矩阵测试**

```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAuthorizeRule_CrossTenantIsolation 是 M1.1 退风险验收矩阵：
// 经共用 AuthorizeRule，证明租户隔离在鉴权核心层正确。
func TestAuthorizeRule_CrossTenantIsolation(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-a", "app-a", "AK_a")
	tB, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "app-b", "AK_b")

	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tB, "bob", []byte("sb")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk, "root", []byte("sr")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// code 返回 AuthorizeRule 的 gRPC 状态码（nil→codes.OK）。
	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createRole = "/sydom.admin.v1.AdminService/CreateRole"
		listRoles  = "/sydom.admin.v1.AdminService/ListRoles"
		createOp   = "/sydom.admin.v1.AdminService/CreateOperator"
	)
	roleReq := func(appID int64) *adminv1.CreateRoleRequest { return &adminv1.CreateRoleRequest{AppId: uint64(appID)} }
	listReq := func(appID int64) *adminv1.ListRolesRequest { return &adminv1.ListRolesRequest{AppId: uint64(appID)} }

	// alice = 租户 A 管理员：本租户放行，跨租户 403（写 + 读）。
	require.Equal(t, codes.OK, code("alice", createRole, roleReq(appA)))
	require.Equal(t, codes.PermissionDenied, code("alice", createRole, roleReq(appB)), "alice 写跨租户 app 必须 403")
	require.Equal(t, codes.OK, code("alice", listRoles, listReq(appA)))
	require.Equal(t, codes.PermissionDenied, code("alice", listRoles, listReq(appB)), "alice 读跨租户 app 必须 403")

	// bob = 租户 B 管理员：对称。
	require.Equal(t, codes.OK, code("bob", createRole, roleReq(appB)))
	require.Equal(t, codes.PermissionDenied, code("bob", createRole, roleReq(appA)), "bob 写跨租户 app 必须 403")

	// root = super-admin：两租户均放行。
	require.Equal(t, codes.OK, code("root", createRole, roleReq(appA)))
	require.Equal(t, codes.OK, code("root", createRole, roleReq(appB)))

	// 租户管理员碰不到 SaaS 级 system RPC；root 可以。
	require.Equal(t, codes.PermissionDenied, code("alice", createOp, &adminv1.CreateOperatorRequest{Principal: "x"}), "租户管理员不得创建 operator")
	require.Equal(t, codes.OK, code("root", createOp, &adminv1.CreateOperatorRequest{Principal: "x"}))
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAuthorizeRule_CrossTenantIsolation -count=1 -v 2>&1 | tail -30`
预期：PASS，矩阵全绿。
（若某项 FAIL，**勿改测试断言迁就实现**——回到任务 1/3 用 systematic-debugging 定位根因；安全矩阵是真相。）

- [ ] **步骤 3：Commit**

```bash
git add internal/controlplane/mgmt/tenant_authz_test.go
git commit -m "test(mgmt): 跨租户安全矩阵(租户管理员管本租户/跨租户403/root全放行/system域闸)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：全仓兜底验证 + 收尾

> carry-forward §120：跨包改签名后 `go vet ./...` 全仓兜底。

**文件：** 无新增（仅验证 + 可选进度文档）

- [ ] **步骤 1：全仓 vet + 全量测试**

运行：
```bash
go vet ./... 2>&1 | tail -20
go build ./... 2>&1 | tail -5
go test ./... -count=1 2>&1 | tail -40
```
预期：`go vet` 无输出（干净）；`go build` 无错；`go test ./...` 全绿（重点确认 `console`、`restgw`、`mgmt`、`adminauthz` 包均 PASS——它们经 `AuthorizeRule` 串到新核心，验证 13 处调用方在签名不变下行为正确）。

- [ ] **步骤 2：若有失败，按 systematic-debugging 定位修复后重跑**

不放过任何 FAIL/vet 告警；修复直到全绿。每修一处单独审视是实现 bug 还是测试需随新语义更新（如有 console/restgw 测试假设「租户管理员能调 system RPC」之类，需核对是否属 M1.1 语义变更——按本计划非目标，租户管理员对 system RPC 得 403 是预期）。

- [ ] **步骤 3：更新里程碑进度记录（可选但推荐）**

若团队维护进度文档（参考 memory `project_detailed_design_progress`），追加一行：M1.1 租户隔离基座已落地（鉴权核心 tdom 包含层 + EnsureTenantAdmin + 跨租户安全矩阵全绿，无迁移、数据面不动）。

- [ ] **步骤 4：最终 commit（仅当步骤 3 改了文档）**

```bash
git add -A
git commit -m "docs: 记录 M1.1 租户隔离基座落地

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 自检（规格覆盖 / 占位符 / 类型一致性）

**规格覆盖度（对照路线图 M1 §86-89 中属于 M1.1 的部分）：**

| M1 范围项 | M1.1 是否覆盖 | 任务 |
|---|---|---|
| 租户实体 + 隔离边界（租户拥有多 app） | ✅ 鉴权核心租户域包含层 | 任务 1 |
| 「运营方 root ↔ 租户管理员」分层 | ✅ root=super-admin@*，租户管理员=tenant-admin@t:<id> | 任务 1 + 3 |
| 读写全 tenant-scoped（带 app_id 的 RPC） | ✅ 写 11 + 读 6 + SetApplicationStatus 经 matcher 自动租户化 | 任务 1 + 4 |
| 跨租户 403，安全矩阵扩到租户级 | ✅ 退风险验收 artifact | 任务 4 |
| 自助账户最小集（注册/邀请） | ⛔ 非目标 → M1.2 | — |
| 最小功能（人→角色 + 有效权限视图） | ⛔ 非目标 → M1.3 | — |
| 薄运营台旅程 | ⛔ 非目标 → M1.4 | — |
| 最小可托管运维底座 | ⛔ 非目标 → M1.5 | — |
| ListApplications/CreateApplication 租户化（枚举读/自建 app） | ⛔ 非目标 → M1.2（M1.1 维持 system，租户管理员 403 无泄露） | — |

M1.1 验收（路线图「跨租户 403 安全矩阵全绿」之鉴权核心部分）：由任务 4 矩阵测试给出。「非技术人完成把 Alice 设为销售经理」属 M1.3/M1.4 UX，不在 M1.1。

**占位符扫描：** 全计划无 TODO/待定/「类似任务 N」；每个代码步骤均含完整可粘贴代码与精确命令、预期输出。（authz.go 内保留的 `TODO(observability)` 是**既有**代码注释，非本计划缺口。）

**类型一致性：**
- `Enforce(ctx, sub, dom, tdom, res, act)` —— 任务 1 定义，任务 1/3/4 调用一致（5 元）。
- `TenantDomain(tenantID int64) string` / `(*Enforcer).TenantDomainOf(ctx, appID int64) (string, error)` —— 任务 1 定义，任务 1/3/4 引用一致。
- `EnsureTenantAdmin(ctx, db, masterKey, tenantID, principal, secret)` —— 任务 3 定义，任务 4 调用一致。
- `SeedAppInTenant(t, conn, tenantName, domain, appKey) (int64, int64)` —— 任务 2 定义，任务 3/4 调用一致（返回 tenantID, appID 两值）。
- `AuthorizeRule(ctx, enf, fullMethod, principal, req)` —— 签名**不变**，仅函数体改；13 处既有调用方零改动。
- casbin 约束：5-token 请求 ↔ 5 rvals，全计划所有 Enforce 调用一致传 5 值。

---

## 执行交接

计划已完成并保存到 `docs/superpowers/plans/2026-06-13-sydom-m1-1-tenant-isolation-foundation.md`。两种执行方式：

1. **子代理驱动（推荐）** —— 每个任务调度一个新子代理，任务间两阶段审查，快速迭代（必需子技能：superpowers:subagent-driven-development）。鉴于本切片是安全敏感的鉴权核心改动，强烈建议任务 1、4 后追加整体安全评审。
2. **内联执行** —— 当前会话用 superpowers:executing-plans 批量执行并设检查点。

建议先用 `using-git-worktrees` 为 M1.1 起隔离 worktree（off `main`）再执行。
