# M2.4 List 分页·搜索·排序·过滤 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给全部 11 个 List RPC 统一加 offset 分页（带总数）+ 子串搜索 + 白名单排序 + 结构化过滤，三面 parity（gRPC + REST + Console）。

**架构：** 共享 `ListPage{limit,offset,sort,order,q}` proto 子消息嵌入 11 个请求、响应加 `total`。List 查询当前**内联在 mgmt handler**（`s.db.QueryContext`，非 store 包）——故共享分页助手放 **mgmt 包**（`mgmt/listpage.go`），handler 就地改造（动态 WHERE + 白名单 ORDER BY + ILIKE 搜索 + COUNT 总数）。排序/搜索白名单服务端逐 List 维护（防 SQL 列注入）。鉴权 `AuthorizeRule`/`ruleTable`/matcher 零改；分页绝不拓宽 scope。

**技术栈：** Go、PostgreSQL、protobuf（buf）、net/http（restgw）、html/template（console）、testcontainers（internal/dbtest）。

**基准：** spec `docs/superpowers/specs/2026-06-19-sydom-m2-4-list-pagination-search-sort-filter-design.md`。本计划在 off-main worktree 执行。

**关键不变量（LS-1..LS-7，贯穿全程）：** LS-1 共享 ListPage+clampLimit 同 M2.3、11×3 面统一 / LS-2 sort/order 仅白名单映射为受控 SQL 标识符·q 参数化 ILIKE·limit/offset 钳制 / LS-3 租户隔离零旁路·adminauthz matcher 一字未改·enforcer.go diff=0·ruleTable 零改 / LS-4 COUNT(*) 同 WHERE total 准确 / LS-5 既有调用方同步更新无静默截断 / LS-6 读纯净不 bump / LS-7 sidecar 零触碰。

---

## 共享约定（全任务类型一致，务必逐字对齐）

**clampLimit**：已存在于 `internal/controlplane/mgmt/audit.go`（`func clampLimit(n uint32) int`：0→50，>200→200）。直接复用，不重定义。

**mgmt/listpage.go 三助手（任务 2 建）**：
```go
// resolveOrder 校验 sort（外部列名）against allowed（外部名→受控 SQL 列名白名单），
// order∈{asc,desc}；非法一律回退 defaultCol/ASC。返回受控 "col ASC|DESC"（绝不含用户原串）。
func resolveOrder(sort, order string, allowed map[string]string, defaultCol string) string
// pageOf 取 ListPage（nil 安全）→ clamped limit + 非负 offset。
func pageOf(p *adminv1.ListPage) (limit, offset int)
// searchClause 为白名单列生成 "(c1 ILIKE $n OR c2 ILIKE $n ...)" + 参数 "%q%"；
// q 空或无列 → ("", nil)。argPos 是该参数的占位序号（调用方传 len(args)+1）。
func searchClause(q string, cols []string, argPos int) (string, any)
```

**handler 改造范式（动态 WHERE，参照 M2.3 QueryAppAudit 的 add 闭包）**：每个 handler 用 `conds []string` + `args []any` + `add(cond, val)` 闭包累积既有 scope/过滤，再追加 search（特殊：多列共享一个 arg），最后 `ORDER BY <resolveOrder> LIMIT $ OFFSET $`，并发 `COUNT(*)` 同 WHERE（不含 ORDER/LIMIT）。

**proto 标量约定**：新增过滤字段默认零值=不过滤；`page` nil 安全（pageOf 处理）；`total` = COUNT(*)。

---

## 任务 1：proto — ListPage + 11 请求加 page/过滤 + 11 响应加 total

**文件：** 修改 `api/proto/sydom/admin/v1/admin.proto`

- [ ] **步骤 1：加 ListPage 共享消息**（追加到文件末尾审计消息之后）
```proto

// —— List 分页信封（M2.4，11 个 List 请求共用）——
message ListPage {
  uint32 limit  = 1;  // 0→默认 50，上限 200（clampLimit）
  uint32 offset = 2;
  string sort   = 3;  // 列名，服务端白名单校验；空/非法→默认列
  string order  = 4;  // "asc"|"desc"；空/非法→默认
  string q      = 5;  // 子串搜索，服务端白名单字段 ILIKE
}
```

- [ ] **步骤 2：11 个请求加 `ListPage page` + 新过滤字段**

逐条修改（字段号顺延，保留既有字段）：
```proto
message ListRolesRequest { uint64 app_id = 1; ListPage page = 2; }
message ListPermissionsRequest { uint64 app_id = 1; ListPage page = 2; string source = 3; }      // source 过滤 manual|auto
message ListGrantsRequest { uint64 app_id = 1; int64 role_id = 2; ListPage page = 3; }
message ListRoleInheritancesRequest { uint64 app_id = 1; ListPage page = 2; }
message ListUserBindingsRequest { uint64 app_id = 1; string user_id = 2; ListPage page = 3; }
message ListDataPoliciesRequest { uint64 app_id = 1; string resource = 2; ListPage page = 3; string effect = 4; } // effect 过滤 allow|deny
message ListOperatorsRequest { ListPage page = 1; int32 status = 2; }                            // status 过滤；0=不过滤(状态用 1/2，0 表缺省)
message ListAdminRolesRequest { ListPage page = 1; }
message ListApplicationsRequest { uint64 tenant_id = 1; ListPage page = 2; int32 status = 3; }   // status 过滤
message ListMembersRequest { uint64 tenant_id = 1; ListPage page = 2; int32 tier = 3; }          // tier 过滤
message ListMyTenantsRequest { ListPage page = 1; }
```
> 注意 `ListOperatorsRequest`/`ListAdminRolesRequest`/`ListMyTenantsRequest` 原为空 `{}`；status/tier 用 `int32`，0 表「不过滤」（DB 状态值为 1=active/2=disabled 等正数，tier 同理正数）。

- [ ] **步骤 3：11 个响应加 `uint32 total`**

逐条加 total（字段号取已有最大+1）：
```proto
message ListRolesResponse { repeated RoleSummary roles = 1; uint32 total = 2; }
message ListPermissionsResponse { repeated PermissionSummary permissions = 1; uint32 total = 2; }
message ListGrantsResponse { repeated GrantSummary grants = 1; uint32 total = 2; }
message ListRoleInheritancesResponse { repeated RoleInheritanceSummary inheritances = 1; uint32 total = 2; }
message ListUserBindingsResponse { repeated UserBindingSummary bindings = 1; uint32 total = 2; }
message ListDataPoliciesResponse { repeated DataPolicySummary data_policies = 1; uint32 total = 2; }
message ListOperatorsResponse { repeated OperatorSummary operators = 1; uint32 total = 2; }
message ListAdminRolesResponse { repeated AdminRoleSummary roles = 1; uint32 total = 2; }
message ListApplicationsResponse { ... ; uint32 total = N; }   // 在既有字段后加（看现有最大字段号+1）
message ListMembersResponse { repeated MemberSummary members = 1; uint32 total = 2; }
message ListMyTenantsResponse { repeated TenantMembershipSummary memberships = 1; bool is_operating_plane = 2; uint32 total = 3; }
```
（`ListApplicationsResponse` 先 grep 看现有字段号，total 取最大+1。）

- [ ] **步骤 4：生成 + 校验**

运行：`make proto-gen && make proto-check`，预期 buf lint 通过、生成 `ListPage`/各请求 `Page` 字段/各响应 `Total` 字段、proto-check exit 0（提交 gen/ 后）。再 `go build ./...`（既有 handler 仍能编译——新字段是 additive，旧代码不读 page/total 仍合法）。

- [ ] **步骤 5：Commit**
```bash
cd <worktree> && git add api/proto/sydom/admin/v1/admin.proto gen/ && \
git commit -m "feat(proto): M2.4 ListPage 信封 + 11 List 请求加 page/过滤 + 响应加 total"
```

---

## 任务 2：mgmt 共享分页助手 + 单测

**文件：** 创建 `internal/controlplane/mgmt/listpage.go`、`internal/controlplane/mgmt/listpage_test.go`

- [ ] **步骤 1：写失败测试 `listpage_test.go`（package mgmt，内部测试——需访问未导出函数）**

> 注意：用 `package mgmt`（非 mgmt_test），因 resolveOrder/searchClause/pageOf 未导出。检查是否已有 `package mgmt` 内部测试文件作伴；若全是 mgmt_test，新建内部测试文件即可（go 允许同目录两个测试包）。
```go
package mgmt

import (
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/stretchr/testify/require"
)

func TestResolveOrder_WhitelistAndInjection(t *testing.T) {
	allowed := map[string]string{"id": "id", "name": "name"}
	require.Equal(t, "name DESC", resolveOrder("name", "desc", allowed, "id"))
	require.Equal(t, "name ASC", resolveOrder("name", "", allowed, "id"))
	// 非法 sort（注入尝试）→ 回退默认列，绝不含用户串
	require.Equal(t, "id ASC", resolveOrder("id;DROP TABLE role", "asc", allowed, "id"))
	require.Equal(t, "id ASC", resolveOrder("unknown_col", "weird", allowed, "id"))
}

func TestPageOf_ClampAndNil(t *testing.T) {
	l, o := pageOf(nil)
	require.Equal(t, 50, l) // 默认
	require.Equal(t, 0, o)
	l, _ = pageOf(&adminv1.ListPage{Limit: 0})
	require.Equal(t, 50, l)
	l, _ = pageOf(&adminv1.ListPage{Limit: 1000})
	require.Equal(t, 200, l) // 上限
	_, o = pageOf(&adminv1.ListPage{Offset: 30})
	require.Equal(t, 30, o)
}

func TestSearchClause(t *testing.T) {
	cond, arg := searchClause("alice", []string{"code", "name"}, 3)
	require.Equal(t, "(code ILIKE $3 OR name ILIKE $3)", cond)
	require.Equal(t, "%alice%", arg)
	cond, arg = searchClause("", []string{"code"}, 3)
	require.Equal(t, "", cond)
	require.Nil(t, arg)
}
```

- [ ] **步骤 2：运行验证失败**：`cd <worktree> && go test ./internal/controlplane/mgmt/ -run "ResolveOrder|PageOf|SearchClause" -v` → 编译失败（未定义）。

- [ ] **步骤 3：实现 `listpage.go`**
```go
package mgmt

import (
	"strconv"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

// resolveOrder 把外部 sort 列名经 allowed 白名单映射为受控 SQL 列名，order→ASC|DESC。
// 非法 sort/order 一律回退 defaultCol/ASC。返回值永远是受控标识符，绝不含用户原始输入（防注入）。
func resolveOrder(sort, order string, allowed map[string]string, defaultCol string) string {
	col, ok := allowed[sort]
	if !ok {
		col = defaultCol
	}
	dir := "ASC"
	if strings.EqualFold(order, "desc") {
		dir = "DESC"
	}
	return col + " " + dir
}

// pageOf 取 ListPage（nil 安全）→ clamped limit（复用 clampLimit：0→50/上限200）+ 非负 offset。
func pageOf(p *adminv1.ListPage) (int, int) {
	if p == nil {
		return clampLimit(0), 0
	}
	off := int(p.Offset)
	if off < 0 {
		off = 0
	}
	return clampLimit(p.Limit), off
}

// searchClause 为白名单列生成 "(c1 ILIKE $n OR ...)" + 参数 "%q%"（所有列共享一个占位 $argPos）。
// q 空或无列 → ("", nil)。列名来自服务端白名单（非用户输入），无注入风险。
func searchClause(q string, cols []string, argPos int) (string, any) {
	if q == "" || len(cols) == 0 {
		return "", nil
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c + " ILIKE $" + strconv.Itoa(argPos)
	}
	return "(" + strings.Join(parts, " OR ") + ")", "%" + q + "%"
}
```

- [ ] **步骤 4：运行验证通过**：`go test ./internal/controlplane/mgmt/ -run "ResolveOrder|PageOf|SearchClause" -count=1 -v` → PASS。

- [ ] **步骤 5：Commit**
```bash
cd <worktree> && git add internal/controlplane/mgmt/listpage.go internal/controlplane/mgmt/listpage_test.go && \
git commit -m "feat(mgmt): 共享分页助手 resolveOrder/pageOf/searchClause(白名单防注入)"
```

---

## 任务 3：app 域 6 个 List handler 改造（admin_reads.go）

**文件：** 修改 `internal/controlplane/mgmt/admin_reads.go`；测试 `internal/controlplane/mgmt/admin_reads_pagination_test.go`（新建，package mgmt_test，用 dbtest + accountsSrv）

> **上下文**：6 个 handler 当前内联 `s.db.QueryContext(... ORDER BY id)`。逐个改为：动态 WHERE（既有 scope/过滤 + 可选 search）+ resolveOrder 白名单排序 + LIMIT/OFFSET + COUNT(*) 总数。下面给 **ListPermissions 完整 worked example**，其余 5 个按 §逐 List delta 表 套同一范式。

### 逐 List delta 表（app 域）
| handler | 表 | 基础 WHERE | 搜索列(q) | sort 白名单(外部→SQL，*默认) | 新过滤 | 响应字段 |
|---|---|---|---|---|---|---|
| ListRoles | role | app_id=$1 | code,name | id→id*, code→code, name→name | — | Roles/Total |
| ListPermissions | permission | app_id=$1 | code,name,resource,action | id*,code,resource,action,source | source(=$k 精确) | Permissions/Total |
| ListGrants | role_permission | app_id=$1 [+role_id] | （无） | id*,role_id | role_id(既有) | Grants/Total |
| ListRoleInheritances | role_inheritance | app_id=$1 | （无） | id* | — | Inheritances/Total |
| ListUserBindings | user_role_binding | app_id=$1 [+user_id] | user_id | id*,user_id | user_id(既有) | Bindings/Total |
| ListDataPolicies | data_policy | app_id=$1 [+resource] | resource,subject_id,description | id*,resource,effect | resource(既有),effect(=$k) | DataPolicies/Total |

- [ ] **步骤 1：写失败测试（覆盖 4 类 + 注入 + total）**

`admin_reads_pagination_test.go`（参照该包既有测试 accountsSrv/dbtest.SeedApp）：
```go
// ListPermissions：分页 + 搜索 + 排序 + total + source 过滤。
func TestListPermissions_PageSearchSortTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	for i := 0; i < 5; i++ {
		_, err := db.Exec(`INSERT INTO permission(app_id,code,resource,action,type,name,source)
			VALUES($1,$2,'order','read','api',$3,'manual')`,
			appID, fmt.Sprintf("p%d", i), fmt.Sprintf("权限%d", i))
		require.NoError(t, err)
	}
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	// 分页 limit=2 offset=0 → 2 行，total=5
	resp, err := s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 2, Offset: 0, Sort: "code", Order: "asc"}})
	require.NoError(t, err)
	require.Len(t, resp.Permissions, 2)
	require.Equal(t, uint32(5), resp.Total)
	require.Equal(t, "p0", resp.Permissions[0].Code)
	// 搜索 q=p1 → 1 行
	resp, err = s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Q: "p1"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	// 注入 sort → 回退默认、不报错、不破坏
	resp, err = s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Sort: "id;DROP TABLE permission"}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total) // 表未被破坏
}
```
（其余 5 个 handler 各加 1 个精简分页+total 测试，确认 total 准确、limit 生效；ListGrants/ListUserBindings/ListDataPolicies 额外断言既有过滤仍生效。）

- [ ] **步骤 2：运行验证失败**（编译失败：handler 还没读 Page/Total 字段）。

- [ ] **步骤 3：改 ListPermissions（worked example）**
```go
func (s *AdminServer) ListPermissions(ctx context.Context, r *adminv1.ListPermissionsRequest) (*adminv1.ListPermissionsResponse, error) {
	conds := []string{"app_id = $1"}
	args := []any{int64(r.AppId)}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+strconv.Itoa(len(args)))
	}
	if r.Source != "" {
		add("source =", r.Source)
	}
	if sc, arg := searchClause(r.Page.GetQ(), []string{"code", "name", "resource", "action"}, len(args)+1); sc != "" {
		args = append(args, arg)
		conds = append(conds, sc)
	}
	where := strings.Join(conds, " AND ")
	// total
	var total uint32
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM permission WHERE `+where, args...).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "count permissions: %v", err)
	}
	// page
	order := resolveOrder(r.Page.GetSort(), r.Page.GetOrder(),
		map[string]string{"id": "id", "code": "code", "resource": "resource", "action": "action", "source": "source"}, "id")
	limit, offset := pageOf(r.Page)
	args = append(args, limit, offset)
	q := `SELECT id, code, resource, action, type, name, source FROM permission WHERE ` + where +
		` ORDER BY ` + order + ` LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list permissions: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListPermissionsResponse{Total: total}
	for rows.Next() {
		var x adminv1.PermissionSummary
		if err := rows.Scan(&x.PermissionId, &x.Code, &x.Resource, &x.Action, &x.Ptype, &x.Name, &x.Source); err != nil {
			return nil, status.Errorf(codes.Internal, "scan permission: %v", err)
		}
		out.Permissions = append(out.Permissions, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows permission: %v", err)
	}
	return out, nil
}
```
顶部 import 加 `"strconv"`、`"strings"`（admin_reads.go 已有 context/sql/adminv1/codes/status）。`r.Page.GetQ()/GetSort()/GetOrder()` 用 proto getter（nil 安全）。

- [ ] **步骤 4：改其余 5 个 handler**（套同一范式，按 delta 表）
  - **ListRoles**：无新过滤；search cols=[code,name]；sort 白名单 {id,code,name}；SELECT 加既有 `COALESCE(description,'')`。
  - **ListGrants**：保留 `if r.RoleId != 0 { add("role_id =", r.RoleId) }`；无 search；sort {id,role_id}。
  - **ListRoleInheritances**：无过滤无 search；sort {id}。
  - **ListUserBindings**：保留 `if r.UserId != "" { add("user_id =", r.UserId) }`；search [user_id]；sort {id,user_id}。
  - **ListDataPolicies**：保留 `if r.Resource != "" { add("resource =", r.Resource) }`；加 `if r.Effect != "" { add("effect =", r.Effect) }`；search [resource,subject_id,description]；sort {id,resource,effect}；SELECT 保留 `condition::text`、`COALESCE(description,'')`、version。
  - 每个都设 `out.Total = total`。删除原来的 `if r.X==0 {...} else {...}` 双分支（统一走动态 WHERE）。

- [ ] **步骤 5：运行验证通过**：`go test ./internal/controlplane/mgmt/ -count=1 -v 2>&1 | tail -20` → 新分页测试 PASS；**既有 List 测试可能因默认 limit=50 截断而失败**——若既有测试 seed >50 行或断言全量，本任务**先不改既有测试**（留任务 9 统一处理向后兼容），但若既有测试 seed 少量（<50）则不受影响应仍绿。记录哪些既有测试受影响。

- [ ] **步骤 6：Commit**
```bash
cd <worktree> && git add internal/controlplane/mgmt/admin_reads.go internal/controlplane/mgmt/admin_reads_pagination_test.go && \
git commit -m "feat(mgmt): app 域 6 List 分页/搜索/排序/过滤 + total(白名单防注入)"
```

---

## 任务 4：system+tenant+self 5 个 List handler 改造

**文件：** 修改 `internal/controlplane/mgmt/admin_reads.go`（ListOperators/ListAdminRoles）、`internal/controlplane/mgmt/admin_ops.go`（ListApplications）、`internal/controlplane/mgmt/accounts.go`（ListMembers、ListMyTenants）；测试追加到 `admin_reads_pagination_test.go` 或新建 `account_pagination_test.go`

### 逐 List delta 表
| handler | 文件 | 表/来源 | 基础 WHERE | 搜索列 | sort 白名单(*默认) | 新过滤 |
|---|---|---|---|---|---|---|
| ListOperators | admin_reads.go | admin_operator | （无；全量）| principal | id*,principal,status | status(=$k，r.Status!=0) |
| ListAdminRoles | admin_reads.go | admin_role | （无）| code,name | id*,code | — |
| ListApplications | admin_ops.go | application | tenant_id 分支(见下) | name,domain,app_key | id*,name,domain,status | status(r.Status!=0) |
| ListMembers | accounts.go | tenant_membership⋈admin_operator | m.tenant_id=$1 | o.principal | o.id*(operator_id),o.principal,m.tier | tier(r.Tier!=0) |
| ListMyTenants | accounts.go | 内存(TenantsOfOperator) | — | tenant_name | tenant_id*,tenant_name | — |

- [ ] **步骤 1：写失败测试**（每个 handler 1 个分页+total 测试；ListApplications 测 status 过滤 + 超管 tenant_id=0 全量分页；ListMembers 测 tenant scope + tier 过滤；ListMyTenants 测内存分页/搜索）。参照任务 3 测试范式。ListOperators/ListApplications 直插多行，断言 limit/total。

- [ ] **步骤 2：运行验证失败**。

- [ ] **步骤 3：改 ListOperators / ListAdminRoles**（admin_reads.go，套任务 3 范式）
  - ListOperators：`conds` 起始为空（全量）→ `where` 可能为空，处理：`whereSQL := ""; if len(conds)>0 { whereSQL = " WHERE "+strings.Join(...) }`。SELECT `id,principal,status`（**secret_enc 绝不出**）。`if r.Status != 0 { add("status =", int16(r.Status)) }`。search [principal]。sort {id,principal,status}。COUNT 同 where。
  - ListAdminRoles：全量，search [code,name]，sort {id,code}。

- [ ] **步骤 4：改 ListApplications**（admin_ops.go）
  - 保留既有 tenant scope 语义：`tenant_id=0`→全量（超管），非 0→`tenant_id=$1`。用动态 WHERE：`if r.TenantId != 0 { add("tenant_id =", int64(r.TenantId)) }`。加 `if r.Status != 0 { add("status =", int16(r.Status)) }`。search [name,domain,app_key]。sort {id,name,domain,status}。SELECT `id,domain,name,app_key,status,current_version`。COUNT 同 where。设 Total。

- [ ] **步骤 5：改 ListMembers**（accounts.go）
  - 基础 `m.tenant_id = $1`（保留 tenant scope）。JOIN 不变（`tenant_membership m JOIN admin_operator o ON o.id=m.operator_id`）。`if r.Tier != 0 { add("m.tier =", int16(r.Tier)) }`。search [o.principal]。sort 白名单用带表别名的列：{operator_id→o.id, principal→o.principal, tier→m.tier}，默认 o.id。COUNT(*) 同 JOIN+where。SELECT `o.id,o.principal,m.tier,o.status`。设 Total。

- [ ] **步骤 6：改 ListMyTenants（内存分页）**（accounts.go）
  - 现状：`adminauthz.TenantsOfOperator(ctx, s.db, principal)` 返回 []Membership（含 TenantID/TenantName/Tier）。改为：取全量后**内存**应用：q 子串过滤 tenant_name（strings.Contains 不区分大小写用 strings.Contains(strings.ToLower(...),...))、sort（按 tenant_id 或 tenant_name，asc/desc）、total=过滤后长度、再 limit/offset 切片。用 pageOf 取 limit/offset。不引 SQL。is_operating_plane 保留。
  ```go
  // 伪结构（实现按既有 Membership 字段名）：
  ms := TenantsOfOperator(...)         // 全量
  if q != "" { ms = filterByName(ms, q) }
  sortMemberships(ms, sortCol, order)  // tenant_id|tenant_name
  total := len(ms)
  ms = pageSlice(ms, limit, offset)    // 边界安全切片
  ```

- [ ] **步骤 7：运行验证通过**：`go test ./internal/controlplane/mgmt/ -count=1 -v` → 新测试 PASS；记录受默认 limit 影响的既有测试（留任务 9）。

- [ ] **步骤 8：Commit**
```bash
cd <worktree> && git add internal/controlplane/mgmt/admin_reads.go internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/*_pagination_test.go && \
git commit -m "feat(mgmt): system+tenant+self 5 List 分页/搜索/排序/过滤 + total(ListMyTenants 内存)"
```

---

## 任务 5：REST 11 路由加 page/过滤 query 解析

**文件：** 修改 `internal/controlplane/restgw/routes.go`；测试 `internal/controlplane/restgw/routes_pagination_test.go`

> **上下文**：REST 路由的 decode 闭包构造请求 message。本任务给 11 个 List 路由的 decode 加 `?limit=&offset=&sort=&order=&q=` + 新过滤 query，填入 `&adminv1.ListPage{...}` 与请求字段。任务 8（M2.3）已加 `queryUint64`/`queryUint32` 助手。

- [ ] **步骤 1：加 ListPage 解析助手**（routes.go）
```go
// parseListPage 从 query 解析 ListPage（缺省零值）。
func parseListPage(r *http.Request) (*adminv1.ListPage, error) {
	limit, err := queryUint32(r, "limit")
	if err != nil {
		return nil, err
	}
	offset, err := queryUint32(r, "offset")
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	return &adminv1.ListPage{
		Limit: limit, Offset: offset,
		Sort: q.Get("sort"), Order: q.Get("order"), Q: q.Get("q"),
	}, nil
}
```

- [ ] **步骤 2：写失败测试**（参照 routes_effperm_test.go/routes_audit_test.go）：`TestREST_ListPermissions_Paginated`——种子 >2 行，GET `/v1/apps/{id}/permissions?limit=2&sort=code&q=...` → 200 且 JSON 含 `total` + `permissions` ≤2；越权 401。再选 1 个 system 域（如 operators）测一遍。

- [ ] **步骤 3：改 11 个 List 路由 decode**
  - app 域（appRoutes）6 个：在 decode 内 `pathUint64(app_id)` 后 `page, err := parseListPage(r)`，填 `m.Page = page`；带既有过滤的（grants role_id、bindings user_id、datapolicies resource）继续从 query 取；permissions 加 `Source: r.URL.Query().Get("source")`、datapolicies 加 `Effect: ...`。
  - ListOperators/ListAdminRoles（systemRoutes）：`&adminv1.ListOperatorsRequest{Page: page, Status: int32(queryInt64 status)}` 等。
  - ListApplications（applicationRoutes/systemRoutes 看现处）：加 page + status；tenant_id 既有。
  - ListMembers/ListMyTenants（accountRoutes）：加 page（+ tier for members）。

  worked example（ListPermissions 路由 decode）：
```go
func(r *http.Request, _ []byte) (proto.Message, error) {
	id, err := pathUint64(r, "app_id")
	if err != nil {
		return nil, err
	}
	page, err := parseListPage(r)
	if err != nil {
		return nil, err
	}
	return &adminv1.ListPermissionsRequest{AppId: id, Source: r.URL.Query().Get("source"), Page: page}, nil
},
```

- [ ] **步骤 4：运行验证通过**：`go test ./internal/controlplane/restgw/ -count=1 -v 2>&1 | tail -20`，新分页路由 PASS、既有路由零回归（REST 既有 List 测试若 seed 少量不受默认 limit 影响）。

- [ ] **步骤 5：Commit**
```bash
cd <worktree> && git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_pagination_test.go && \
git commit -m "feat(restgw): 11 List 路由加 page/过滤 query(parseListPage)"
```

---

## 任务 6：Console 共享分页/排序 partial + helper

**文件：** 创建 `internal/controlplane/console/templates/_pager.html`、`internal/controlplane/console/listparams.go`；测试在任务 7/8 覆盖

- [ ] **步骤 1：listparams.go——从请求解析分页/排序/搜索 + 构造模板数据**
```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

const consolePageSize = 50

// listPageFromReq 从 query 解析 ListPage（Console 固定 limit=consolePageSize，offset 由 ?page= 页码算）。
func listPageFromReq(r *http.Request) *adminv1.ListPage {
	page := 1
	if v, err := formUint64(r, "page"); err == nil && v >= 1 {
		page = int(v)
	}
	q := r.URL.Query()
	return &adminv1.ListPage{
		Limit: consolePageSize, Offset: uint32((page - 1) * consolePageSize),
		Sort: q.Get("sort"), Order: q.Get("order"), Q: q.Get("q"),
	}
}

// pagerData 构造分页条模板数据（当前页、是否有上下页、total、显示区间、保留的 query 串）。
func pagerData(r *http.Request, total uint32) map[string]any {
	page := 1
	if v, err := formUint64(r, "page"); err == nil && v >= 1 {
		page = int(v)
	}
	from := (page-1)*consolePageSize + 1
	to := page * consolePageSize
	if uint32(to) > total {
		to = int(total)
	}
	if total == 0 {
		from = 0
	}
	q := r.URL.Query()
	q.Del("page")
	return map[string]any{
		"Page": page, "Total": total, "From": from, "To": to,
		"HasPrev": page > 1, "HasNext": uint32(page*consolePageSize) < total,
		"PrevPage": page - 1, "NextPage": page + 1,
		"Query": q.Encode(), // 保留 sort/order/q/过滤
		"Sort": r.URL.Query().Get("sort"), "Order": r.URL.Query().Get("order"), "Q": r.URL.Query().Get("q"),
	}
}
```
（`formUint64` 任务 9-M2.3 已在 console/forms.go 加。）

- [ ] **步骤 2：_pager.html partial**
```html
{{define "pager"}}
<div class="pager">
  <span class="count">显示 {{.From}}-{{.To}} / 共 {{.Total}}</span>
  {{if .HasPrev}}<a href="?page={{.PrevPage}}&{{.Query}}">← 上一页</a>{{end}}
  {{if .HasNext}}<a href="?page={{.NextPage}}&{{.Query}}">下一页 →</a>{{end}}
</div>{{end}}
{{define "searchbox"}}
<form method="get" class="searchbox"><input name="q" value="{{.Q}}" placeholder="搜索">
{{if .Sort}}<input type="hidden" name="sort" value="{{.Sort}}">{{end}}
{{if .Order}}<input type="hidden" name="order" value="{{.Order}}">{{end}}
<button>搜索</button></form>{{end}}
```

- [ ] **步骤 3：编译校验**：`go build ./...`（partial 经 //go:embed 自动发现；listparams.go 编译）。无独立测试，任务 7/8 经页面测试覆盖。

- [ ] **步骤 4：Commit**
```bash
cd <worktree> && git add internal/controlplane/console/listparams.go internal/controlplane/console/templates/_pager.html && \
git commit -m "feat(console): 共享分页/搜索 partial + listparams 助手"
```

---

## 任务 7：Console app 域 6 List 页接分页/搜索/排序

**文件：** 修改 `internal/controlplane/console/routes_rbac.go`（listRoles/listPermissions/listGrants/listInheritances/listBindings）、`routes_datapolicy.go`（data policies 列表）、对应 6 个模板（roles/permissions/grants/inheritances/bindings/datapolicies.html）；测试 `routes_pagination_test.go`

> **上下文**：6 个读页当前 `msg := &adminv1.ListXRequest{AppId: appID}`。改为加 `Page: listPageFromReq(r)`（+保留既有 query 过滤），renderPage data 加 `"Pager": pagerData(r, resp.Total)` 与既有列表，模板加 `{{template "searchbox" .Pager}}` + 表格后 `{{template "pager" .Pager}}`、表头可点排序链接。

- [ ] **步骤 1：写失败测试**（参照 routes_decision_test.go/routes_audit_test.go）：`TestConsole_Permissions_Paginated`——带会话 + 种子 >2 行 + `?page=1` → 200 且 body 含「共 N」「下一页」。1 个代表页足够 + 1 个 system 页（任务 8）。

- [ ] **步骤 2：改 6 个 handler**（worked example listPermissions）
```go
func (h *Handler) listPermissions(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	msg := &adminv1.ListPermissionsRequest{AppId: appID, Source: r.URL.Query().Get("source"), Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListPermissions", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	resp, err := h.srv.ListPermissions(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	h.renderPage(w, r, "permissions.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "permissions", "Permissions": resp.Permissions,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}
```
其余 5 个同理（保留各自既有 query 过滤：grants 的 role_id、bindings 的 user_id、datapolicies 的 resource，从 r.FormValue/Query 取并填入请求）。

- [ ] **步骤 3：改 6 个模板**——在列表 `<table>` 前加 `{{template "searchbox" .Pager}}`，表格后加 `{{template "pager" .Pager}}`。表头排序链接示例（permissions code 列）：
```html
<th><a href="?sort=code&order=asc">Code ↑</a> <a href="?sort=code&order=desc">↓</a></th>
```
（至少给主键列/名称列加排序链接；保持简单。）

- [ ] **步骤 4：运行验证通过**：`go test ./internal/controlplane/console/ -count=1 -v 2>&1 | tail -20`，新测试 PASS、既有 console 页测试不回归（注意既有测试若 seed <50 不受默认 limit 影响；受影响的留任务 9）。

- [ ] **步骤 5：Commit**
```bash
cd <worktree> && git add internal/controlplane/console/routes_rbac.go internal/controlplane/console/routes_datapolicy.go internal/controlplane/console/templates/ internal/controlplane/console/routes_pagination_test.go && \
git commit -m "feat(console): app 域 6 List 页分页/搜索/排序(共用 pager partial)"
```

---

## 任务 8：Console system+tenant 5 List 页接分页

**文件：** 修改 `internal/controlplane/console/routes_system.go`（listOperators/listAdminRoles）、`routes_apps.go` 或相应（applications 列表页若有）、`routes_accounts.go`（membersList/tenantsList）；对应模板；测试追加

> **上下文**：套任务 7 范式。ListMyTenants（tenantsList）、ListMembers（membersList）、ListOperators、ListAdminRoles + applications 列表页（若 Console 有独立 applications 列表页则改；若无则跳过该页仅保留 RPC 层）。

- [ ] **步骤 1：先 grep 确认 Console 实际有哪些 system/tenant List 页**：`grep -rn "ListOperators\|ListAdminRoles\|ListApplications\|ListMembers\|ListMyTenants" internal/controlplane/console/*.go`，对每个**存在的**页 handler 套范式。

- [ ] **步骤 2：写失败测试**（1 个 system 页如 operators 分页 + total 渲染）。

- [ ] **步骤 3：改各存在的 handler**——加 `Page: listPageFromReq(r)`（+ status/tier 过滤从 query）、data 加 `"Pager": pagerData(r, resp.Total)`。

- [ ] **步骤 4：改对应模板**——加 searchbox + pager partial。

- [ ] **步骤 5：运行验证通过**：`go test ./internal/controlplane/console/ -count=1 -v`。

- [ ] **步骤 6：Commit**
```bash
cd <worktree> && git add internal/controlplane/console/ && \
git commit -m "feat(console): system+tenant List 页分页/搜索"
```

---

## 任务 9：向后兼容——更新受默认 limit 截断的既有调用方/测试

**文件：** 受影响的既有测试（mgmt/restgw/console 各包）、e2e（`examples/orderservice` 或 `internal/.../e2e`）、seeder/Console 若隐含全量假设

- [ ] **步骤 1：全量跑找受影响测试**：`cd <worktree> && go test ./... 2>&1 | grep -E "FAIL|--- FAIL" | tee /tmp/m24_fails.txt`。受影响来源：既有测试 seed >50 行后断言全量长度、或断言「列表含某行」但该行落在第 2 页。

- [ ] **步骤 2：逐个修复**——对每个 FAIL：
  - 若断言全量 count：改为断言 `resp.Total`（总数仍准）或显式传 `Page{Limit: 200}` 取足够大页。
  - 若断言「含某 X」但被分页挤出：传 `Page{Limit: 200}` 或按需 offset/搜索定位。
  - **不放宽** clampLimit 上限（200 是安全上限）；测试需要全量时显式传 Limit=200 且 seed ≤200。
  - e2e：若依赖 List 全量，改为传 Page 或断言 Total。

- [ ] **步骤 3：全量验证**：`go test ./... 2>&1 | grep -c FAIL` → 0。

- [ ] **步骤 4：Commit**
```bash
cd <worktree> && git add -A && \
git commit -m "test(m2.4): 既有调用方/e2e 适配默认分页(断言 Total 或显式 Limit)"
```

---

## 任务 10：全量验证 + LS-1..LS-7 评审 + 零触碰核验 + FF 合并

- [ ] **步骤 1：LS-3 零触碰核验**
```bash
git diff <base>..HEAD -- internal/controlplane/adminauthz/enforcer.go   # 预期空
git diff <base>..HEAD -- internal/controlplane/mgmt/authz.go            # 预期空(ruleTable 零改)
git diff <base>..HEAD -- internal/sidecar/                              # 预期空(LS-7)
```

- [ ] **步骤 2：格式/静态/proto**：`gofmt -l internal/`（空）、`go vet ./...`（净）、`make proto-check`（exit 0 无漂移）。

- [ ] **步骤 3：全量测试**：`go test ./...` → 0 FAIL（含 dbtest testcontainers + e2e）。

- [ ] **步骤 4：LS-1..LS-7 逐条核验并记录证据**
  - LS-1 共享 ListPage+clampLimit、11×3 统一；LS-2 注入测试（恶意 sort 回退默认、表未损）；LS-3 步骤1 零触碰 + 跨租户 List 仍隔离（既有 scope 测试不回归）；LS-4 total 测试（过滤+搜索下 total==实际）；LS-5 任务9 既有调用方全绿；LS-6 List 不 bump（读纯净）；LS-7 sidecar diff=0。

- [ ] **步骤 5：更新进度记忆**：`project_detailed_design_progress.md` 追加 M2.4 节 + `MEMORY.md` 索引指针（M2.4 完成 + M2 里程碑完结）。

- [ ] **步骤 6：FF 合并本地 main（不 push origin）**：worktree 全绿 + 评审 READY 后 FF 并入本地 main，清 worktree。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 ListPage+请求/响应→任务1 ✓；§4 逐 List 白名单→任务3/4 表 ✓；§5.1/5.2 共享助手（落 mgmt 非 store，因查询内联 mgmt）→任务2 + 各 handler ✓；§5.3 鉴权零改→任务10 核验 ✓；§5.4 REST→任务5、Console→任务6/7/8 ✓；§6 LS-1..LS-7→全程+任务10 ✓；§7 错误处理（非法 sort 回退、空结果非错误）→任务2/3 ✓；§8 测试策略→各任务 TDD ✓。

**与 spec 的偏差（已记录）：** spec §5.1 写「store 共享 orderClause」，实际查询内联在 mgmt handler→共享助手落 `mgmt/listpage.go`（更贴合现状、改动更小，shared-helper 意图不变）。

**类型一致性：** `ListPage`/`page`/`total` proto 字段、`resolveOrder(sort,order,allowed,defaultCol)string`/`pageOf(*ListPage)(int,int)`/`searchClause(q,cols,argPos)(string,any)`/`clampLimit(uint32)int`（复用）/`listPageFromReq`/`pagerData`/`parseListPage` 跨任务一致。

**占位符扫描：** worked example（ListPermissions handler / 路由 decode / Console handler）全代码；其余 10× 用精确逐 List delta 表（列/过滤/白名单/搜索列确切给出）——遵循 DRY，非占位。任务 8 先 grep 确认 Console 实际页集合（避免对不存在页臆造）。
