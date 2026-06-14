# M1.4 薄运营台业务语言旅程 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 交付独立的业务向运营台（`/ops/` 前缀），让非技术业务管理员用业务语言完成「把 Alice 设为销售经理并看她能做什么」+ 新建业务角色（命名 + 勾能力），底层零旁路复用全部既有 RPC + `AuthorizeRule`。

**架构：** Console BFF 内新增 `/ops/apps/{app_id}/...` 业务区（人员页 + 业务角色页），业务语言映射层把 role.name/permission.name/data_policy.description 翻译给非技术用户、绝不露原语。新增 `CreateBusinessRole` 原子复合 RPC（单 `runVersionedWrite` 事务建角色 + 批量授权）。接通既有 `data_policy.description` 列（无迁移）作数据策略业务简记。

**技术栈：** Go、casbin v3.10.0、buf/protobuf、gRPC、net/http（REST + Console BFF）、html/template、PostgreSQL、testify、dbtest。

**规格：** `docs/superpowers/specs/2026-06-14-sydom-m1-4-business-ops-console-design.md`

---

## 文件结构

**新建：**
- `internal/controlplane/console/routes_ops.go` — 运营台 handler（registerOps + 人员/业务角色页 + 业务语言映射）。
- `internal/controlplane/console/routes_ops_test.go` — 运营台测试。
- `internal/controlplane/console/templates/ops_layout.html` — 运营台独立 layout/nav（业务语言）。
- `internal/controlplane/console/templates/ops_people.html` / `ops_person.html` / `ops_roles.html` / `ops_role_new.html` / `ops_role.html` — 运营台模板。
- `internal/controlplane/mgmt/business_role.go` — gRPC handler `CreateBusinessRole`。
- `internal/controlplane/mgmt/business_role_test.go` — handler + 鉴权矩阵测试。

**修改：**
- `api/proto/sydom/admin/v1/admin.proto` — `CreateBusinessRole` RPC+消息；`UpsertDataPolicyRequest`/`DataPolicySummary` 加 description。
- `internal/controlplane/types.go` — `cp.DataPolicy` +Description。
- `internal/controlplane/store/store.go` — `UpsertDataPolicy` 写 description。
- `internal/controlplane/store/read.go` — `ReadAppDataPolicies` 读 description。
- `internal/controlplane/policy/manager.go` — `CreateBusinessRole` 原子复合方法。
- `internal/controlplane/mgmt/server.go` — `UpsertDataPolicy` handler 透传 description。
- `internal/controlplane/mgmt/admin_reads.go` — `ListDataPolicies` 读 description。
- `internal/controlplane/mgmt/authz.go` — ruleTable +`CreateBusinessRole`。
- `internal/controlplane/restgw/routes.go` — appRoutes +`CreateBusinessRole`。
- `internal/controlplane/console/handler.go` — 注册 `h.registerOps(mux)`。
- `internal/controlplane/console/routes_datapolicy.go` — upsertDataPolicy decode +description。
- `internal/controlplane/console/templates/datapolicies.html` — 表单加「业务说明」输入。

---

## 任务 1：proto — CreateBusinessRole + data_policy description

**文件：** 修改 `api/proto/sydom/admin/v1/admin.proto`

- [ ] **步骤 1：service 加 RPC**

在 `rpc BindOperatorRole(...)` 那段管理员写分组之后（或紧邻 CreateRole 所在业务写分组）插入：
```proto
  // —— 业务语言运营台（M1.4）——
  rpc CreateBusinessRole(CreateBusinessRoleRequest) returns (CreateBusinessRoleResponse);
```

- [ ] **步骤 2：文件末尾加消息**
```proto
// CreateBusinessRole：业务语言建角色（命名 + 勾能力），后端原子建角色+批量授权。code 系统生成。
message CreateBusinessRoleRequest {
  uint64 app_id = 1;
  string name = 2;
  repeated int64 permission_ids = 3;
}
message CreateBusinessRoleResponse {
  int64 role_id = 1;
  uint64 version = 2;
  bool changed = 3;
}
```

- [ ] **步骤 3：给 data_policy 两消息加 description（接通既有 DB 列）**

把 `UpsertDataPolicyRequest` 末尾加字段（现有最后是 `string effect = 7;`）：
```proto
  string description = 8; // 业务说明（运营台简记，纯元数据，不参与求值）
```
把 `DataPolicySummary` 末尾加字段（现有最后是 `uint64 version = 7;`）：
```proto
  string description = 8;
```

- [ ] **步骤 4：regen + 编译 + 漂移检测**

运行：`make proto-gen && go build ./gen/... && make proto-check`
预期：生成 `CreateBusinessRoleRequest/Response`、两 data_policy 消息含 `Description`；`git diff --exit-code gen/` 零差异。

- [ ] **步骤 5：Commit**
```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M1.4 CreateBusinessRole RPC + data_policy description 字段"
```

---

## 任务 2：data_policy.description 全链接通（cp/store/mgmt，无迁移）

**文件：** 修改 `internal/controlplane/types.go`、`store/store.go`、`store/read.go`、`mgmt/server.go`、`mgmt/admin_reads.go`
**测试：** `internal/controlplane/store/datapolicy_description_test.go`

- [ ] **步骤 1：写失败的 store 往返测试**

创建 `internal/controlplane/store/datapolicy_description_test.go`：
```go
package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestUpsertDataPolicy_DescriptionRoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	id, created, err := store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "sales", Resource: "orders",
		Condition: `{"op":"EQ","field":"region","value":"east"}`, Effect: "allow",
		Description: "仅限本人区域的订单",
	}, 1)
	require.NoError(t, err)
	require.True(t, created)

	dps, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, dps, 1)
	require.Equal(t, int64(id), dps[0].ID)
	require.Equal(t, "仅限本人区域的订单", dps[0].Description)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run TestUpsertDataPolicy_DescriptionRoundTrip`
预期：FAIL（`cp.DataPolicy` 无 Description 字段，编译错）。

- [ ] **步骤 3：cp.DataPolicy 加字段**

在 `internal/controlplane/types.go` 的 `DataPolicy` 结构体（在 `Effect string` 后）加：
```go
	Description string // 业务说明（运营台简记）；纯元数据，不参与投影/求值
```

- [ ] **步骤 4：store 写 description**

修改 `internal/controlplane/store/store.go` 的 `UpsertDataPolicy`：
INSERT 语句改为含 description：
```go
		err = ex.QueryRowContext(ctx, `
			INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, description, version)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8) RETURNING id`,
			appID, p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, p.Description, version).Scan(&id)
```
UPDATE 语句加 `description=$N`（重排参数）：
```go
	res, err := ex.ExecContext(ctx, `
		UPDATE data_policy SET subject_type=$1, subject_id=$2, resource=$3, condition=$4::jsonb,
		       effect=$5, description=$6, version=$7, updated_at=now()
		WHERE app_id=$8 AND id=$9`,
		p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, p.Description, version, appID, p.ID)
```

- [ ] **步骤 5：store 读 description**

修改 `internal/controlplane/store/read.go` 的 `ReadAppDataPolicies`：SELECT 加 `description`，scan 加 `&p.Description`。注意 description 列可空（NULL）→ 用 `COALESCE(description,'')`：
```go
	rows, err := q.QueryContext(ctx,
		`SELECT id, subject_type, subject_id, resource, condition, effect, COALESCE(description,'') FROM data_policy WHERE app_id=$1`, appID)
```
并在 scan 行加 `&p.Description`（按 SELECT 顺序，置于 effect 之后）。读实际代码核对 scan 变量顺序。

- [ ] **步骤 6：运行 store 测试验证通过**

运行：`go test ./internal/controlplane/store/ -run TestUpsertDataPolicy_DescriptionRoundTrip`
预期：PASS。

- [ ] **步骤 7：mgmt 透传 description（写 + 读）**

`internal/controlplane/mgmt/server.go` 的 `UpsertDataPolicy` handler，构造 `cp.DataPolicy{...}` 时加 `Description: r.Description`：
```go
	d, err := s.mgr.UpsertDataPolicy(ctx, int64(r.AppId), cp.DataPolicy{
		ID: r.Id, SubjectType: r.SubjectType, SubjectID: r.SubjectId, Resource: r.Resource,
		Condition: r.Condition, Effect: eff, Description: r.Description,
	})
```
`internal/controlplane/mgmt/admin_reads.go` 的 `ListDataPolicies`：两处 SQL（无过滤 / resource 过滤）SELECT 加 `COALESCE(description,'')`，scan 目标加 `&x.Description`（置于 `&ver` 之前，按 SELECT 顺序）。读实际代码核对两个分支 SQL 与 scan。

- [ ] **步骤 8：运行全链测试 + 包测试**

运行：`go test ./internal/controlplane/store/... ./internal/controlplane/mgmt/ -count=1 2>&1 | tail -5`
预期：全 PASS（既有 data policy 测试 + 新 description 往返）。

- [ ] **步骤 9：Commit**
```bash
git add internal/controlplane/types.go internal/controlplane/store/ internal/controlplane/mgmt/server.go internal/controlplane/mgmt/admin_reads.go
git commit -m "feat(datapolicy): 接通既有 description 列全链(cp/store/mgmt 读写, 纯元数据不入求值)"
```

---

## 任务 3：PolicyManager.CreateBusinessRole 原子复合

**文件：** 修改 `internal/controlplane/policy/manager.go`
**测试：** `internal/controlplane/policy/manager_business_role_test.go`

- [ ] **步骤 1：写失败的原子测试**

创建 `internal/controlplane/policy/manager_business_role_test.go`：
```go
package policy_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCreateBusinessRole_AtomicRoleAndGrants(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	mgr := policy.NewPolicyManager(db, nil)

	// 先建两个权限点（建模台职责，这里直接用 PolicyManager 播种）。
	p1, _, err := mgr.UpsertPermission(ctx, appID, "p_read", "orders", "read", "p", "查看订单")
	require.NoError(t, err)
	p2, _, err := mgr.UpsertPermission(ctx, appID, "p_export", "orders", "export", "p", "导出订单")
	require.NoError(t, err)

	roleID, d, err := mgr.CreateBusinessRole(ctx, appID, "销售经理", []int64{p1, p2})
	require.NoError(t, err)
	require.NotZero(t, roleID)
	require.NotNil(t, d) // 有授权 → 产生 casbin_rule → Delta 非空

	// 角色 + 两条授权都在（同事务原子落库）。
	rules, err := store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	var pRows int
	for _, r := range rules {
		if r.Ptype == "p" {
			pRows++
		}
	}
	require.Equal(t, 2, pRows) // 两条 p 行（read/export）
}

func TestCreateBusinessRole_EmptyCapabilitiesOK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)

	roleID, _, err := mgr.CreateBusinessRole(context.Background(), appID, "空角色", nil)
	require.NoError(t, err)
	require.NotZero(t, roleID) // 无能力的空角色合法
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestCreateBusinessRole`
预期：FAIL（`CreateBusinessRole` 未定义）。

- [ ] **步骤 3：实现 CreateBusinessRole + code 生成**

在 `internal/controlplane/policy/manager.go` 加（紧邻 CreateRole 后）。先确保文件 import `crypto/rand`、`encoding/hex`：
```go
// generateRoleCode 生成系统内部唯一角色 code（业务管理员永不见/不填）。
// 纯随机避免中文 name slug 的复杂度；唯一性由 uq_role_app_code 兜底。
func generateRoleCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "br-" + hex.EncodeToString(b), nil
}

// CreateBusinessRole 业务语言建角色：单事务内建角色 + 批量授权（原子，杜绝半授权空角色）。
func (m *PolicyManager) CreateBusinessRole(ctx context.Context, appID int64, name string, permIDs []int64) (roleID int64, d *cp.Delta, err error) {
	code, err := generateRoleCode()
	if err != nil {
		return 0, nil, fmt.Errorf("policy: gen role code: %w", err)
	}
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "create_business_role", entityType: "role", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.InsertRole(ctx, tx, appID, code, name)
			if e != nil {
				return nil, e
			}
			roleID = id
			for _, pid := range permIDs {
				if e := store.InsertRolePermission(ctx, tx, appID, id, pid, cp.EffectAllow); e != nil {
					return nil, e
				}
			}
			return nil, nil
		},
	})
	return roleID, d, err
}
```
注：`rand`/`hex` 若包内已 import 则复用；核对 `internal/controlplane/policy/manager.go` 顶部 import。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policy/ -run TestCreateBusinessRole -v`
预期：2 测试 PASS。

- [ ] **步骤 5：Commit**
```bash
git add internal/controlplane/policy/
git commit -m "feat(policy): CreateBusinessRole 原子复合(单事务建角色+批量授权, code 系统生成)"
```

---

## 任务 4：mgmt CreateBusinessRole handler + ruleTable + 鉴权矩阵

**文件：** 创建 `internal/controlplane/mgmt/business_role.go`；修改 `authz.go`
**测试：** `internal/controlplane/mgmt/business_role_test.go`

- [ ] **步骤 1：ruleTable 新增**

`internal/controlplane/mgmt/authz.go` 的 ruleTable，在 `CreateRole` 那条业务写分组内加：
```go
	"/sydom.admin.v1.AdminService/CreateBusinessRole":      {"role", "create", true, scopeApp},
```
（加后运行 `gofmt -w internal/controlplane/mgmt/authz.go` 修列对齐。）

- [ ] **步骤 2：写失败的 handler + 鉴权矩阵测试**

创建 `internal/controlplane/mgmt/business_role_test.go`（对齐 `effective_test.go` 真实 harness `accountsSrv`/`adminauthz.EnsureTenantAdmin`/`NewEnforcer`/`AuthorizeRule`）：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateBusinessRole_EmptyNameRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	_, err := srv.CreateBusinessRole(context.Background(),
		&adminv1.CreateBusinessRoleRequest{AppId: uint64(appID), Name: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateBusinessRole_CrossTenant403(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/CreateBusinessRole"
	req := &adminv1.CreateBusinessRoleRequest{AppId: uint64(appB), Name: "x"}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
```
（如需 `mgmt` import，对齐 `effective_test.go` 既有 import；`accountsSrv` 来自 `accounts_test.go`。）

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestCreateBusinessRole`
预期：FAIL（handler 未定义）。

- [ ] **步骤 4：实现 handler**

创建 `internal/controlplane/mgmt/business_role.go`：
```go
package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateBusinessRole 业务语言建角色：原子建角色 + 批量授权（下沉 PolicyManager 单事务）。
func (s *AdminServer) CreateBusinessRole(ctx context.Context, r *adminv1.CreateBusinessRoleRequest) (*adminv1.CreateBusinessRoleResponse, error) {
	if r.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	roleID, d, err := s.mgr.CreateBusinessRole(ctx, int64(r.AppId), r.Name, r.PermissionIds)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create business role: %v", err)
	}
	resp := &adminv1.CreateBusinessRoleResponse{RoleId: roleID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}
```
核对 `AdminServer` 有字段 `mgr *policy.PolicyManager`（读 server.go；UpsertDataPolicy handler 用 `s.mgr`）。

- [ ] **步骤 5：运行验证通过 + 全包**

运行：`go test ./internal/controlplane/mgmt/ -run TestCreateBusinessRole -v && go test ./internal/controlplane/mgmt/ -count=1 2>&1 | tail -3`
预期：新测试 PASS；全包绿。

- [ ] **步骤 6：Commit**
```bash
git add internal/controlplane/mgmt/business_role.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/business_role_test.go
git commit -m "feat(mgmt): CreateBusinessRole handler + ruleTable scopeApp + 跨租户矩阵"
```

---

## 任务 5：REST 路由 CreateBusinessRole

**文件：** 修改 `internal/controlplane/restgw/routes.go`
**测试：** `internal/controlplane/restgw/routes_business_role_test.go`

- [ ] **步骤 1：写失败的路由测试**

创建 `internal/controlplane/restgw/routes_business_role_test.go`（对齐 `routes_accounts_test.go` 真实 harness `newTestGW`/`rootClient`/`c.do`，POST + JSON body）：
```go
package restgw_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestREST_CreateBusinessRole_POST(t *testing.T) {
	h := newRESTHarness(t) // 对齐 routes_accounts_test.go 真实 harness
	// 需先有权限点；harness 若无播种 helper，可只断言路由存在 + 鉴权放行下非 404/405。
	resp := h.do(h.signed(t, "POST", "/v1/apps/1/business-roles", `{"name":"销售经理","permission_ids":[]}`))
	require.NotEqual(t, http.StatusNotFound, resp.StatusCode)
	require.NotEqual(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
```
（harness 名以本包实际为准；重点断言路由注册 + 绑定 CreateBusinessRole。）

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/restgw/ -run TestREST_CreateBusinessRole`
预期：FAIL（404）。

- [ ] **步骤 3：appRoutes 追加路由**

`internal/controlplane/restgw/routes.go` 的 `appRoutes()` 返回切片内追加（紧邻 CreateRole 路由后）：
```go
		{"POST", "/v1/apps/{app_id}/business-roles", pfx + "CreateBusinessRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateBusinessRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id // path 权威覆写
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateBusinessRole(ctx, m.(*adminv1.CreateBusinessRoleRequest))
			}},
```
若包内有路由计数断言，同步 +1，并更新 appRoutes/allRoutes 注释计数。

- [ ] **步骤 4：运行验证通过 + 全包**

运行：`go test ./internal/controlplane/restgw/... -count=1 2>&1 | tail -3`
预期：全 PASS。

- [ ] **步骤 5：Commit**
```bash
git add internal/controlplane/restgw/
git commit -m "feat(restgw): POST /v1/apps/{app_id}/business-roles 路由(path 权威覆写)"
```

---

## 任务 6：建模台数据策略表单加「业务说明」

**文件：** 修改 `internal/controlplane/console/routes_datapolicy.go`、`templates/datapolicies.html`
**测试：** `internal/controlplane/console/routes_datapolicy_test.go`（追加用例；若无则新建）

- [ ] **步骤 1：写失败的表单往返测试**

在 console 测试中新增（对齐 `handler_test.go` 真实 harness `newConsole`/`loginAndCSRF`/`readBody`）：
```go
func TestDataPolicyForm_DescriptionPersisted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{
		"csrf_token": {csrf}, "id": {"0"}, "subject_type": {"role"}, "subject_id": {"sales"},
		"resource": {"orders"}, "effect": {"allow"},
		"condition":   {`{"op":"EQ","field":"region","value":"east"}`},
		"description": {"仅限本人区域的订单"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)

	// 列表页应回显业务说明。
	page, _ := c.Get(ts.URL + fmt.Sprintf("/apps/%d/data-policies", appID))
	require.Contains(t, readBody(t, page), "仅限本人区域的订单")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestDataPolicyForm_Description`
预期：FAIL（description 未透传/未显示）。

- [ ] **步骤 3：upsertDataPolicy decode 加 description**

`internal/controlplane/console/routes_datapolicy.go` 的 `upsertDataPolicy`，返回的 `&adminv1.UpsertDataPolicyRequest{...}` 加：
```go
				Description: r.FormValue("description"),
```

- [ ] **步骤 4：模板加输入 + 列显示**

`internal/controlplane/console/templates/datapolicies.html`：
表单（`condition` textarea 后）加：
```html
<input name="description" placeholder="业务说明（运营台显示，如：仅限本人区域的订单）">
```
列表 `<table>` 加一列表头「业务说明」+ 行 `<td>{{.Description}}</td>`（核对 ListDataPolicies 已回带 description→`DataPolicySummary.Description`，任务 2 已接通）。读模板实际结构对齐表头/行列数。

- [ ] **步骤 5：运行验证通过 + 全 console**

运行：`go test ./internal/controlplane/console/... -count=1 2>&1 | tail -3`
预期：全 PASS。

- [ ] **步骤 6：Commit**
```bash
git add internal/controlplane/console/routes_datapolicy.go internal/controlplane/console/templates/datapolicies.html internal/controlplane/console/*_test.go
git commit -m "feat(console): 建模台数据策略表单加业务说明(description)输入+回显"
```

---

## 任务 7：运营台 人员旅程 + 业务语言映射

**文件：** 创建 `internal/controlplane/console/routes_ops.go`、`templates/ops_layout.html`、`ops_people.html`、`ops_person.html`；修改 `handler.go`
**测试：** `internal/controlplane/console/routes_ops_test.go`

- [ ] **步骤 1：注册运营台路由**

`internal/controlplane/console/handler.go` 的 `New` 内（registerAccounts 后）加 `h.registerOps(mux)`。

- [ ] **步骤 2：写失败的人员旅程测试**

创建 `internal/controlplane/console/routes_ops_test.go`（对齐 `handler_test.go` 真实 harness）：
```go
package console_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestOps_PersonView_BusinessLanguage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建模台播种：权限点（带业务名）+ 业务角色（含能力）+ 把人绑到角色。
	mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "p_read", "orders", "read", "查看订单")
	// 经运营台建业务角色 or 直接 mgmt；此处用既有建模台 helper 建角色+授权+绑定 alice。
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "sales", "销售经理")
	mustGrant(t, c, ts, csrf, uint64(appID), roleID, "orders", "read")
	mustBind(t, c, ts, csrf, uint64(appID), "alice", roleID)

	page, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/people/view?user_id=alice", appID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	body := readBody(t, page)
	require.Contains(t, body, "销售经理") // 业务角色名
	require.Contains(t, body, "查看订单") // 能力业务名（非 orders:read）
	require.NotContains(t, body, "orders:read") // 不漏技术原语
}

func TestOps_PersonView_DegradeNoEnumerate(t *testing.T) {
	ts, store, db := newConsole(t)
	_ = dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	page, err := c.Get(ts.URL + "/ops/apps/9999999999/people/view?user_id=alice")
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, page.StatusCode)
	require.False(t, strings.Contains(readBody(t, page), "查看订单"))
}
```
> 测试用的 `mustCreatePermission`/`mustGrant`/`mustBind` helper：若 console 测试包已有同类（见 `handler_test.go`/`routes_effperm_test.go` 的 `mustCreateRole`）则复用/补齐；否则在测试文件内用 `c.PostForm` 打既有建模台路由实现（薄封装）。

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestOps_Person`
预期：FAIL（路由/handler/模板未定义）。

- [ ] **步骤 4：实现 registerOps + 人员 handler + 业务语言映射**

创建 `internal/controlplane/console/routes_ops.go`：
```go
package console

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

const opsSvc = "/sydom.admin.v1.AdminService/"

// registerOps 注册业务向运营台路由（业务语言、隐藏技术原语）。
func (h *Handler) registerOps(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/people", h.opsPeople)
	mux.HandleFunc("GET /ops/apps/{app_id}/people/view", h.opsPersonView)
	// 业务角色页路由在任务 8 注册。
}

// capName 是 (resource,action)→业务名 映射；缺 name 回退 resource:action。
type capName map[[2]string]string

// permNameMap 取 app 权限点的业务名映射（经 AuthorizeRule，越权即返回错误→上层降级）。
func (h *Handler) permNameMap(ctx context.Context, principal string, appID uint64) (capName, error) {
	msg := &adminv1.ListPermissionsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, opsSvc+"ListPermissions", principal, msg)
	if err != nil {
		return nil, err
	}
	resp, err := h.srv.ListPermissions(actx, msg)
	if err != nil {
		return nil, err
	}
	m := capName{}
	for _, p := range resp.Permissions {
		if p.Name != "" {
			m[[2]string{p.Resource, p.Action}] = p.Name
		}
	}
	return m, nil
}

func (m capName) label(resource, action string) string {
	if n, ok := m[[2]string{resource, action}]; ok {
		return n
	}
	return resource + ":" + action // 回退（不漏更深原语如 role_id/eft）
}

// opsPeople：人员列表（来自已有绑定的去重 user_id）+ 录入入口。
func (h *Handler) opsPeople(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListUserBindings", err)
		return
	}
	msg := &adminv1.ListUserBindingsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, opsSvc+"ListUserBindings", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListUserBindings", err)
		return
	}
	resp, err := h.srv.ListUserBindings(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListUserBindings", err)
		return
	}
	seen := map[string]bool{}
	var people []string
	for _, b := range resp.Bindings {
		if !seen[b.UserId] {
			seen[b.UserId] = true
			people = append(people, b.UserId)
		}
	}
	h.renderPage(w, r, "ops_people.html", http.StatusOK, map[string]any{
		"AppID": appID, "People": people, "CSRF": sess.CSRF, "OpsNav": "people"})
}

// capView 是某人能力的业务展示行。
type capView struct{ Capability string }

// opsPersonView：某人「能做什么」业务视图——角色名 + 能力业务名 + 数据策略业务简记。
func (h *Handler) opsPersonView(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"GetEffectivePermissions", err)
		return
	}
	userID := r.FormValue("user_id")

	caps, err := h.permNameMap(r.Context(), principal, appID)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListPermissions", err)
		return
	}

	data := map[string]any{"AppID": appID, "UserID": userID, "CSRF": sess.CSRF, "OpsNav": "people"}

	if userID != "" {
		// 有效权限（功能 + 角色 + 数据预览）。
		effMsg := &adminv1.GetEffectivePermissionsRequest{AppId: appID, UserId: userID}
		ectx, err := mgmt.AuthorizeRule(r.Context(), h.enf, opsSvc+"GetEffectivePermissions", principal, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, opsSvc+"GetEffectivePermissions", err)
			return
		}
		eff, err := h.srv.GetEffectivePermissions(ectx, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, opsSvc+"GetEffectivePermissions", err)
			return
		}
		var capRows []capView
		for _, p := range eff.Permissions {
			capRows = append(capRows, capView{Capability: caps.label(p.Resource, p.Action)})
		}
		// 数据策略业务简记：取该 app 数据策略，过滤适用于本人（user 或其角色），按 resource 汇描述。
		notes := h.dataScopeNotes(r.Context(), principal, appID, userID, eff.Roles)

		// 当前角色（业务名）。
		bindMsg := &adminv1.ListUserBindingsRequest{AppId: appID, UserId: userID}
		bctx, _ := mgmt.AuthorizeRule(r.Context(), h.enf, opsSvc+"ListUserBindings", principal, bindMsg)
		bindResp, _ := h.srv.ListUserBindings(bctx, bindMsg)

		data["Queried"] = true
		data["Roles"] = eff.Roles
		data["Capabilities"] = capRows
		data["DataNotes"] = notes
		data["Bindings"] = bindResp.GetBindings()
	}
	h.renderPage(w, r, "ops_person.html", http.StatusOK, data)
}

// dataScopeNote 是某 resource 的数据范围业务简记。
type dataScopeNote struct {
	Resource string
	Note     string
}

// dataScopeNotes 取适用于本人（subject=user:userID 或其角色 code）的数据策略，按 resource 汇业务简记。
// 角色 code 集来自有效权限的 Roles（隐式角色闭包，与 dataperm 主体判定同源）。
func (h *Handler) dataScopeNotes(ctx context.Context, principal string, appID uint64, userID string, roles []string) []dataScopeNote {
	msg := &adminv1.ListDataPoliciesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, opsSvc+"ListDataPolicies", principal, msg)
	if err != nil {
		return nil // 取不到就不展示数据范围（不阻塞功能视图）
	}
	resp, err := h.srv.ListDataPolicies(actx, msg)
	if err != nil {
		return nil
	}
	roleSet := map[string]bool{}
	for _, r := range roles {
		roleSet[r] = true
	}
	byRes := map[string][]string{}
	var order []string
	for _, dp := range resp.DataPolicies {
		applies := (dp.SubjectType == "user" && dp.SubjectId == userID) ||
			(dp.SubjectType == "role" && roleSet[dp.SubjectId])
		if !applies {
			continue
		}
		note := dp.Description
		if note == "" {
			note = "受限范围（详见建模台）" // 绝不回退到原始谓词
		}
		if _, ok := byRes[dp.Resource]; !ok {
			order = append(order, dp.Resource)
		}
		byRes[dp.Resource] = append(byRes[dp.Resource], note)
	}
	var out []dataScopeNote
	for _, res := range order {
		out = append(out, dataScopeNote{Resource: res, Note: joinNotes(byRes[res])})
	}
	return out
}

func joinNotes(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += "；"
		}
		out += n
	}
	return out
}
```

- [ ] **步骤 5：实现模板**

创建 `internal/controlplane/console/templates/ops_layout.html`（运营台独立 layout，业务语言 nav；对齐既有 `layout.html` 的 `{{define}}` 结构——读 layout.html 核对块名 `title`/`content` 与外层 HTML 骨架是否共用。若 console 用单一 layout.html define content，则 ops 模板同样 `{{define "content"}}` 并加运营台 nav 片段）。最小可用：
```html
{{define "opsnav"}}
<aside class="appnav"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/people" {{if eq .OpsNav "people"}}class="active"{{end}}>人员</a>
<a href="/ops/apps/{{.AppID}}/roles" {{if eq .OpsNav "roles"}}class="active"{{end}}>业务角色</a>
</aside>{{end}}
```
创建 `ops_people.html`：
```html
{{define "content"}}
{{template "opsnav" .}}
<h2>人员</h2>
<form method="GET" action="/ops/apps/{{.AppID}}/people/view">
  <label>查看某人能做什么：<input name="user_id" placeholder="用户标识" required></label>
  <button type="submit">查看</button>
</form>
<ul>
  {{range .People}}<li><a href="/ops/apps/{{$.AppID}}/people/view?user_id={{.}}">{{.}}</a></li>
  {{else}}<li>（暂无已分配人员）</li>{{end}}
</ul>
{{end}}
```
创建 `ops_person.html`：
```html
{{define "content"}}
{{template "opsnav" .}}
<h2>{{if .Queried}}{{.UserID}} 能做什么{{else}}查看某人能做什么{{end}}</h2>
<form method="GET" action="/ops/apps/{{.AppID}}/people/view">
  <input name="user_id" value="{{.UserID}}" placeholder="用户标识" required><button>查看</button>
</form>
{{if .Queried}}
<h3>业务角色</h3>
<p>{{range .Roles}}<span class="tag">{{.}}</span> {{else}}（未分配角色）{{end}}</p>
<h3>能做的事</h3>
<ul>{{range .Capabilities}}<li>{{.Capability}}</li>{{else}}<li>（无）</li>{{end}}</ul>
<h3>数据范围</h3>
<ul>{{range .DataNotes}}<li><b>{{.Resource}}</b>：{{.Note}}</li>{{else}}<li>全部数据</li>{{end}}</ul>
{{end}}
{{end}}
```
注：运营台模板**绝不**渲染 role_id/code/eft/谓词。若 console 渲染机制要求每页模板在 `render.go` 注册/解析，按既有范式补登记（读 `render.go` 看是否按文件名自动解析）。

- [ ] **步骤 6：运行验证通过 + 全 console**

运行：`go test ./internal/controlplane/console/... -count=1 2>&1 | tail -4`
预期：人员旅程测试 + 既有全 PASS。

- [ ] **步骤 7：Commit**
```bash
git add internal/controlplane/console/
git commit -m "feat(console): 运营台人员旅程 + 业务语言映射(角色名/能力名/数据简记, 不漏原语)"
```

---

## 任务 8：运营台 业务角色旅程（建角色 + 分配 + 编辑能力）

**文件：** 修改 `routes_ops.go`；创建 `templates/ops_roles.html`、`ops_role_new.html`、`ops_role.html`
**测试：** 追加到 `routes_ops_test.go`

- [ ] **步骤 1：注册业务角色 + 分配路由**

`routes_ops.go` 的 `registerOps` 加：
```go
	mux.HandleFunc("GET /ops/apps/{app_id}/roles", h.opsRoles)
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/new", h.opsRoleNewForm)
	mux.HandleFunc("POST /ops/apps/{app_id}/roles", h.opsCreateRole)
	mux.HandleFunc("POST /ops/apps/{app_id}/people/assign", h.opsAssignRole)
	mux.HandleFunc("POST /ops/apps/{app_id}/people/unassign", h.opsUnassignRole)
```

- [ ] **步骤 2：写失败的建角色 + 分配测试**

追加到 `routes_ops_test.go`：
```go
func TestOps_CreateBusinessRole_Then_Assign(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	permID := mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "p_read", "orders", "read", "查看订单")

	// 运营台建业务角色（名称 + 勾能力）。
	form := url.Values{"csrf_token": {csrf}, "name": {"销售经理"}, "permission_ids": {fmt.Sprint(permID)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/ops/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)

	// 角色列表应显示业务名「销售经理」。
	page, _ := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/roles", appID))
	require.Contains(t, readBody(t, page), "销售经理")
}
```
> `mustCreatePermission` 返回 permID（经建模台 UpsertPermission 路由 + 查 DB 取 id，或复用既有 helper）。

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestOps_CreateBusinessRole`
预期：FAIL。

- [ ] **步骤 4：实现业务角色 handler**

`routes_ops.go` 加：
```go
// opsRoles：业务角色列表（按业务名，隐藏 code/role_id）。
func (h *Handler) opsRoles(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListRoles", err)
		return
	}
	msg := &adminv1.ListRolesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, opsSvc+"ListRoles", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListRoles", err)
		return
	}
	resp, err := h.srv.ListRoles(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListRoles", err)
		return
	}
	h.renderPage(w, r, "ops_roles.html", http.StatusOK, map[string]any{
		"AppID": appID, "Roles": resp.Roles, "CSRF": sess.CSRF, "OpsNav": "roles"})
}

// opsRoleNewForm：新建业务角色表单（名称 + 能力复选框，复选项=权限点业务名）。
func (h *Handler) opsRoleNewForm(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListPermissions", err)
		return
	}
	msg := &adminv1.ListPermissionsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, opsSvc+"ListPermissions", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListPermissions", err)
		return
	}
	resp, err := h.srv.ListPermissions(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, opsSvc+"ListPermissions", err)
		return
	}
	h.renderPage(w, r, "ops_role_new.html", http.StatusOK, map[string]any{
		"AppID": appID, "Permissions": resp.Permissions, "CSRF": sess.CSRF, "OpsNav": "roles"})
}

// opsCreateRole：建业务角色（doWrite + CreateBusinessRole，原子）。
func (h *Handler) opsCreateRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, opsSvc+"CreateBusinessRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			var permIDs []int64
			for _, s := range r.Form["permission_ids"] {
				id, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, status.Errorf(codes.InvalidArgument, "invalid permission_id %q", s)
				}
				permIDs = append(permIDs, id)
			}
			return &adminv1.CreateBusinessRoleRequest{AppId: appID, Name: r.FormValue("name"), PermissionIds: permIDs}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.CreateBusinessRole(ctx, m.(*adminv1.CreateBusinessRoleRequest))
		},
		func(r *http.Request) string { return fmt.Sprintf("/ops/apps/%s/roles", r.PathValue("app_id")) })
}

// opsAssignRole / opsUnassignRole：分配/移除业务角色（doWrite + Bind/UnbindUserRole，复用 decodeUserRoleRequest），回人员视图。
func (h *Handler) opsAssignRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, opsSvc+"BindUserRole", decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		}, opsPersonRedirect)
}

func (h *Handler) opsUnassignRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, opsSvc+"UnbindUserRole", decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		}, opsPersonRedirect)
}

func opsPersonRedirect(r *http.Request) string {
	return fmt.Sprintf("/ops/apps/%s/people/view?user_id=%s",
		r.PathValue("app_id"), url.QueryEscape(r.FormValue("user_id")))
}
```
顶部 import 补 `fmt`、`net/url`、`strconv`、`google.golang.org/grpc/codes`、`google.golang.org/grpc/status`、`google.golang.org/protobuf/proto`（核对 `decodeUserRoleRequest` 在 console 包内已存在——M1.3 任务 6 抽出）。

- [ ] **步骤 5：实现模板**

`ops_roles.html`：
```html
{{define "content"}}
{{template "opsnav" .}}
<h2>业务角色</h2>
<a class="btn-primary" href="/ops/apps/{{.AppID}}/roles/new">+ 新建业务角色</a>
<ul>{{range .Roles}}<li>{{.Name}}</li>{{else}}<li>（暂无业务角色）</li>{{end}}</ul>
{{end}}
```
`ops_role_new.html`：
```html
{{define "content"}}
{{template "opsnav" .}}
<h2>新建业务角色</h2>
<form method="POST" action="/ops/apps/{{.AppID}}/roles">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <label>角色名称 <input name="name" placeholder="如：销售经理" required></label>
  <fieldset><legend>这个角色能做什么</legend>
    {{range .Permissions}}<label><input type="checkbox" name="permission_ids" value="{{.PermissionId}}"> {{if .Name}}{{.Name}}{{else}}{{.Resource}}:{{.Action}}{{end}}</label><br>{{else}}<p>（暂无可选能力，请先在建模台定义权限点）</p>{{end}}
  </fieldset>
  <button type="submit">创建</button>
</form>
{{end}}
```
（`PermissionSummary` 字段名 `PermissionId`/`Name`/`Resource`/`Action` 照 gen 核对。）

- [ ] **步骤 6：人员视图加分配/移除表单（编辑闭环）**

`ops_person.html` 在「业务角色」区加：当前绑定的移除表单 + 分配新角色表单（select 来自 ListRoles 业务名）。这要求 opsPersonView 也取 ListRoles 传入模板。在 opsPersonView 的 `if userID != ""` 块补取 ListRoles 并 `data["AssignableRoles"]=rolesResp.Roles`，模板加：
```html
<form method="POST" action="/ops/apps/{{.AppID}}/people/assign">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <input type="hidden" name="user_id" value="{{.UserID}}">
  <select name="role_id">{{range .AssignableRoles}}<option value="{{.RoleId}}">{{.Name}}</option>{{end}}</select>
  <button>分配角色</button>
</form>
```
（移除表单：遍历 `.Bindings` 出 role_id，POST `/ops/apps/{{.AppID}}/people/unassign`，隐藏 csrf+user_id+role_id。）

- [ ] **步骤 7：运行验证通过 + 全 console**

运行：`go test ./internal/controlplane/console/... -count=1 2>&1 | tail -4`
预期：建角色 + 人员旅程全 PASS。

- [ ] **步骤 8：Commit**
```bash
git add internal/controlplane/console/
git commit -m "feat(console): 运营台业务角色旅程(建角色勾能力+分配/移除, CreateBusinessRole 原子)"
```

---

## 任务 9：整体验证 + 安全评审

- [ ] **步骤 1：全仓测试** — `go test ./...` 预期 0 FAIL。
- [ ] **步骤 2：全仓 vet + gofmt** — `go vet ./...` 无输出；`gofmt -l` 本次文件干净；`make proto-check` 无漂移。
- [ ] **步骤 3：OP 不变量逐条核验（file:line 证据）**：
  - OP-1 一份真相：运营台经 `AuthorizeRule`/`ruleTable`，无自有授权；`git diff -- internal/controlplane/adminauthz/` 为空。
  - OP-2 建角色原子：`CreateBusinessRole` 单 `runVersionedWrite`，`TestCreateBusinessRole_AtomicRoleAndGrants` 守门。
  - OP-3 不漏原语：`grep -n "role_id\|RoleId\|\.Code\|:read\|ptype\|eft" internal/controlplane/console/templates/ops_*.html` 应无技术原语外露（能力用 name，回退 resource:action 是设计内的最深回退）。
  - OP-4 数据求值零影响：description 不入 sync proto；`go test ./internal/sidecar/... ./internal/controlplane/effperm/` 全绿。
  - OP-5 租户隔离：`TestOps_PersonView_DegradeNoEnumerate` + `TestCreateBusinessRole_CrossTenant403`。
  - OP-6 secret：`grep -rn secret_enc internal/controlplane/console/routes_ops.go internal/controlplane/mgmt/business_role.go` 无命中。
  - OP-7 CSRF：运营台写动作（建角色/分配/移除）全走 `doWrite`。
- [ ] **步骤 4：opus 整体安全评审** 子代理：复核 OP-1..OP-7 + 业务语言不漏原语 + 跨租户矩阵 + 谓词绝不外露。
- [ ] **步骤 5：收尾 Commit**（如评审有修补）。

---

## 自检结果

**1. 规格覆盖度：** 规格 §4 运营台架构→任务 7/8；§5 业务语言映射→任务 7（permNameMap/label/dataScopeNotes）；§6 CreateBusinessRole→任务 1+3+4；§7 description 接通→任务 1+2+6；§8 鉴权→任务 4/7/8（全经 AuthorizeRule）；§9 三面 parity→任务 4(gRPC)+5(REST)+7/8(Console)；§11 不变量→任务 9；§12 测试→各任务 TDD。无遗漏。

**2. 占位符扫描：** 测试 helper 处标注「对齐/复用既有 X_test.go」并指明确切来源（handler_test.go/effective_test.go/routes_accounts_test.go），非占位；模板登记/harness 名以本包实际为准的提示给了核对锚点。无 TODO/待定。

**3. 类型一致性：** `cp.DataPolicy.Description`（任务 2）↔ proto `description`（任务 1）↔ mgmt 透传（任务 2）一致；`CreateBusinessRole(appID,name,permIDs)` PolicyManager（任务 3）↔ mgmt handler（任务 4）↔ proto `CreateBusinessRoleRequest{AppId,Name,PermissionIds}`（任务 1）↔ REST/Console 调用（任务 5/8）签名一致；`capName`/`label`/`dataScopeNote`/`decodeUserRoleRequest`（M1.3 既有）跨任务 7/8 一致；`PermissionSummary.PermissionId/Name/Resource/Action`、`RoleSummary.RoleId/Name`、`UserBindingSummary.UserId/RoleId` 按 gen 核对。无漂移。
