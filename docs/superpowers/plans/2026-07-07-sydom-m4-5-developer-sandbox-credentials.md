# M4.5 开发者自助闭环（凭据总览 + 数据权限沙箱）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 Web Console 新增两薄件——开发者「接入凭据总览」（`/developer` 一 section）+「数据权限沙箱」（专页 `/apps/{id}/data-sandbox`，输入 subject/resource/attrs 看数据面同一渲染器产出的参数化 WHERE+args）——各由一条新增只读 RPC 支撑，复用既有 `effperm`+`dataperm`，不造第二套决策。

**架构：** 两条新增 `scopeApp` read RPC，三面 parity（gRPC/REST/Console）。`GetApplication` 复用既有 `ApplicationSummary`（无 secret 字段）读一行 app 元数据。`PreviewDataFilter` 在 `effperm` 新增导出 `PreviewFilter`，内部 `buildEngine`(app 快照) + `dataperm.NewFilter(eng,table).FilterSQL(...)`（**数据面同一函数，零触碰**）。Console BFF 渲染，无新 JS。

**技术栈：** Go、buf（proto-gen）、gRPC、html/template、net/http、testify、testcontainers、axe-core 4.10.2。

**规格：** `docs/superpowers/specs/2026-07-07-sydom-m4-5-developer-sandbox-credentials-design.md`（BASE=main `94a615d`；含 SD-1..7）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**；实现者不读本 plan（由控制者派发任务文本）。**授权决策核心（casbin/adminauthz/kernel）+ 数据面（sidecar/dataperm、sidecar/authz）内容零触碰**：仅 `effperm` 加一导出函数、`mgmt`/`restgw` 加 handler/route、`authz.go` 加 2 条 read ruleTable 项、proto 加 2 条 read RPC。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` | +2 RPC（`GetApplication`/`PreviewDataFilter`）+ 4 message | 1,2 |
| `gen/sydom/admin/v1/*` | `make proto-gen` 重生成（不手改） | 1,2 |
| `internal/controlplane/mgmt/admin_ops.go` | +`GetApplication` handler（读一行 application，无 secret） | 1 |
| `internal/controlplane/mgmt/authz.go` | +2 条 ruleTable read 项 | 1,2 |
| `internal/controlplane/mgmt/admin_ops_test.go` | `GetApplication` 单测（含无 secret 断言） | 1 |
| `internal/controlplane/effperm/preview.go` | +导出 `PreviewFilter`（buildEngine+dataperm.FilterSQL 复用） | 2 |
| `internal/controlplane/effperm/preview_test.go` | `PreviewFilter` 单测（sql+args 精确、反向验证有齿） | 2 |
| `internal/controlplane/mgmt/preview_filter.go` | +`PreviewDataFilter` handler（只读 tx 薄包 effperm.PreviewFilter） | 2 |
| `internal/controlplane/mgmt/preview_filter_test.go` | `PreviewDataFilter` RPC 契约测 | 2 |
| `internal/controlplane/restgw/routes.go` | +2 route（GET application、POST data-filter/preview） | 3 |
| `internal/controlplane/restgw/routes_m45_test.go` | 两 route parity 测 | 3 |
| `internal/controlplane/console/routes_developer.go` | `/developer` handler 增调 `GetApplication` | 4 |
| `internal/controlplane/console/templates/developer.html` | +「接入凭据」section | 4 |
| `internal/controlplane/console/routes_developer_test.go` | 凭据 section 测（app_key 现、无 secret） | 4 |
| `internal/controlplane/console/routes_data_sandbox.go` | `registerDataSandbox` + `dataSandbox` handler | 5 |
| `internal/controlplane/console/templates/data_sandbox.html` | 沙箱表单 + 结果 | 5 |
| `internal/controlplane/console/routes_data_sandbox_test.go` | 沙箱页测 | 5 |
| `internal/controlplane/console/templates/_appnav.html` | +「沙箱」tab | 5 |
| `internal/controlplane/console/handler.go` | +`h.registerDataSandbox(mux)` | 5 |
| `docs/superpowers/2026-07-07-m4-5-developer-sandbox-walkthrough.md` | 走查记录 | 6 |

**关键决策：** `PreviewFilter` 放 `effperm`（已拥有快照装配 `buildEngine`），mgmt handler 薄包，杜绝第二套。`GetApplication` 复用 `ApplicationSummary`（类型层无 secret）。功能 Check 沙箱不做——链接既有 `/decision`。

---

## 任务 1：`GetApplication` 只读 RPC（凭据总览数据源）

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`、`internal/controlplane/mgmt/admin_ops.go`、`internal/controlplane/mgmt/authz.go`
- 创建：`internal/controlplane/mgmt/admin_ops_test.go`（若已存在则追加）
- 重生成：`gen/sydom/admin/v1/*`（`make proto-gen`）

参考既有：`ApplicationSummary`（admin.proto:189，字段 `app_id/domain/name/app_key/status/current_version`，**无 secret**）；`ListApplications`（admin_ops.go:120，读 application 表范式）；ruleTable（authz.go:45）；`SetApplicationStatus` ruleTable 项 `{"application","update",false,scopeApp}`。

- [ ] **步骤 1：proto 加 RPC + message**

在 `admin.proto` 的 service 块（ListApplications rpc 附近）加：
```proto
  rpc GetApplication(GetApplicationRequest) returns (GetApplicationResponse);
```
在 messages 区（`ListApplicationsResponse` 之后）加：
```proto
message GetApplicationRequest { uint64 app_id = 1; }
message GetApplicationResponse { ApplicationSummary application = 1; }
```

- [ ] **步骤 2：重生成 gen 代码**

运行：`make proto-gen`
预期：`gen/sydom/admin/v1/*.go` 更新，含 `GetApplicationRequest`/`GetApplicationResponse` 与 `AdminServiceServer` 接口新增 `GetApplication` 方法（此时 mgmt.AdminServer 未实现 → 编译失败，符合 TDD 红）。

- [ ] **步骤 3：写失败测试 `admin_ops_test.go`（追加）**

镜像既有 mgmt 测试装配（`newAdminServer`/dbtest 播种 app，见 admin_ops_test.go 现有用例；helper 名以实际为准）。断言读回字段 + **绝不含 secret**：
```go
func TestAdminServer_GetApplication(t *testing.T) {
	s, db := newAdminServer(t) // 以既有 helper 为准
	appID := dbtest.SeedApp(t, db)
	resp, err := s.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.Equal(t, uint64(appID), resp.Application.AppId)
	require.NotEmpty(t, resp.Application.AppKey)
	require.NotEmpty(t, resp.Application.Domain)
	// SD-1：response 绝不含 secret（ApplicationSummary 无 secret 字段，双保险断言序列化无 secret 字面）。
	require.NotContains(t, resp.String(), "secret")
}

func TestAdminServer_GetApplication_NotFound(t *testing.T) {
	s, _ := newAdminServer(t)
	_, err := s.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```
> 若 mgmt 测试没有 `newAdminServer` helper，改用该文件里既有的 AdminServer 装配方式（读 admin_ops_test.go 顶部）。`dbtest.SeedApp(t, db) int64`。

- [ ] **步骤 4：运行确认失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminServer_GetApplication -v`
预期：编译失败（`GetApplication` 未实现）或 FAIL。

- [ ] **步骤 5：实现 `GetApplication`（admin_ops.go 追加）**

```go
// GetApplication 读单个 app 的非敏感元数据（app 域只读）。绝不返回任何 secret：
// app_secret_hash 永不进 ApplicationSummary（该 message 无 secret 字段）。
func (s *AdminServer) GetApplication(ctx context.Context, r *adminv1.GetApplicationRequest) (*adminv1.GetApplicationResponse, error) {
	var out adminv1.ApplicationSummary
	err := s.db.QueryRowContext(ctx,
		`SELECT id, domain, name, app_key, status, current_version FROM application WHERE id = $1`,
		int64(r.AppId),
	).Scan(&out.AppId, &out.Domain, &out.Name, &out.AppKey, &out.Status, &out.CurrentVersion)
	if err == sql.ErrNoRows {
		return nil, status.Errorf(codes.NotFound, "application %d not found", r.AppId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get application: %v", err)
	}
	return &adminv1.GetApplicationResponse{Application: &out}, nil
}
```
> `status` 的类型（`out.Status` 是 uint32，DB status 是 SMALLINT）：Scan 到 uint32 可行（pg SMALLINT→int16→uint32 目标，用中间 int16 变量再赋值更稳；以编译通过为准，必要时 `var st int16` 再 `out.Status = uint32(st)`）。确保 `import "database/sql"`、`"google.golang.org/grpc/codes"`、`"google.golang.org/grpc/status"` 就位。

- [ ] **步骤 6：运行确认通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminServer_GetApplication -v`
预期：两子测试 PASS。

- [ ] **步骤 7：加 ruleTable 项（authz.go）**

在 `ruleTable` map 里加（与 application 族并列）：
```go
	"/sydom.admin.v1.AdminService/GetApplication": {"application", "read", false, scopeApp},
```

- [ ] **步骤 8：验证 + gofmt + Commit**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAdminServer_GetApplication|TestRuleTable|TestAuthz' -v`（既有 ruleTable 全覆盖测试应自动纳入新项并 PASS；若无此测试忽略该 -run 分支）。
运行：`gofmt -l internal/controlplane/mgmt/`（空）。
运行：`git diff internal/controlplane/adminauthz/ internal/kernel/ | wc -l`（0，决策核心零触碰）。
```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/admin_ops_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(mgmt): M4.5 GetApplication 只读 RPC(复用 ApplicationSummary 无 secret 字段+scopeApp read,供开发者凭据总览)"
```
> 提交尾部空行后加 `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`。**禁 --amend**。

---

## 任务 2：`effperm.PreviewFilter` + `PreviewDataFilter` 只读 RPC（沙箱数据源）

**文件：**
- 创建：`internal/controlplane/effperm/preview.go`、`internal/controlplane/effperm/preview_test.go`
- 创建：`internal/controlplane/mgmt/preview_filter.go`、`internal/controlplane/mgmt/preview_filter_test.go`
- 修改：`api/proto/sydom/admin/v1/admin.proto`、`internal/controlplane/mgmt/authz.go`；重生成 `gen/`

参考既有：`effperm.buildEngine(ctx,tx,appID) (*kernel.Engine,*dataperm.Table,[]cp.Rule,[]cp.DataPolicy,string,error)`（effperm.go:95）；`dataperm.NewFilter(roles RoleResolver, table *Table) *Filter`（filter.go:19，`*kernel.Engine` 满足 RoleResolver）；`dataperm.Filter.FilterSQL(user,dom,resource string, attrs map[string]any) (SQLResult,error)`（render_sql.go:15，`SQLResult{SQL string; Args []any}`，无过滤→空 SQL、deny-all→"1=0"）；`dataperm.ErrMissingVar`；mgmt `ExplainDecision`（decision.go:15，只读 tx + effperm 调用范式）。

- [ ] **步骤 1：写失败测试 `effperm/preview_test.go`**

镜像 effperm 既有测试装配（种子 casbin 规则 + 数据策略；见 effperm_test.go 的 `mustCasbinP`/seed helper，名以实际为准）。断言 sql+args 精确：
```go
package effperm

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestPreviewFilter_RendersParameterizedSQL(t *testing.T) {
	ctx := context.Background()
	db := dbtest.MigratedDB(t) // 以既有 helper 为准
	appID := dbtest.SeedApp(t, db)
	// 播种：user alice 有 role viewer；viewer 对 order 有数据策略 dept = $user.dept（allow）。
	seedRoleAndDataPolicy(t, db, appID, "alice", "viewer", "order", `{"op":"EQ","field":"dept","value":"$user.dept"}`)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	res, err := PreviewFilter(ctx, tx, appID, "alice", "order", map[string]any{"dept": "shanghai"})
	require.NoError(t, err)
	require.Equal(t, "dept = ?", res.SQL)
	require.Equal(t, []any{"shanghai"}, res.Args)
}

func TestPreviewFilter_MissingVar(t *testing.T) {
	ctx := context.Background()
	db := dbtest.MigratedDB(t)
	appID := dbtest.SeedApp(t, db)
	seedRoleAndDataPolicy(t, db, appID, "alice", "viewer", "order", `{"op":"EQ","field":"dept","value":"$user.dept"}`)
	tx, _ := db.BeginTx(ctx, nil)
	defer tx.Rollback()
	_, err := PreviewFilter(ctx, tx, appID, "alice", "order", map[string]any{}) // 缺 dept
	require.ErrorIs(t, err, dataperm.ErrMissingVar)
}
```
> `seedRoleAndDataPolicy` 用 effperm_test.go 既有播种 helper 组合实现（casbin g/p 规则 + data_policy 行）；条件 JSON 用 canonical 大写算子（M4.3 起文法）。`dbtest.MigratedDB`/`SeedApp` 名以实际为准。**若播种细节不清就停下问控制者**（勿臆造 schema）。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/effperm/ -run TestPreviewFilter -v`
预期：FAIL（`PreviewFilter` 未定义）。

- [ ] **步骤 3：实现 `effperm/preview.go`**

```go
package effperm

import (
	"context"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
)

// PreviewFilter 在只读 tx 内对 (appID, subject, resource, attrs) 渲染数据权限的参数化 SQL 片段。
// 复用与 Sidecar 数据面完全同源的渲染器：buildEngine 建 app 快照 → dataperm.NewFilter → FilterSQL，
// 杜绝第二套渲染/决策。任一步失败 fail-close 透传 error（含 ErrMissingVar），绝不返回空 SQL 冒充「无限制」。
func PreviewFilter(ctx context.Context, tx cp.DBTX, appID int64, subject, resource string, attrs map[string]any) (dataperm.SQLResult, error) {
	eng, table, _, _, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return dataperm.SQLResult{}, err
	}
	f := dataperm.NewFilter(eng, table) // *kernel.Engine 满足 dataperm.RoleResolver
	return f.FilterSQL(subject, domain, resource, attrs)
}
```
> `cp` 别名与 `DBTX` 类型以 effperm.go 里既有 import 为准（effperm.go 已用 `cp cp.DBTX`）。

- [ ] **步骤 4：运行确认通过**

运行：`go test ./internal/controlplane/effperm/ -run TestPreviewFilter -v`
预期：两子测试 PASS。

- [ ] **步骤 5：反向验证（证明单测有齿）**

临时把 `preview.go` 的 `f.FilterSQL(subject, domain, resource, attrs)` 改成 `f.FilterSQL(subject, domain, "nonexistent", attrs)`（渲染不存在资源 → 空 SQL）→ 重跑 `TestPreviewFilter_RendersParameterizedSQL` → 确认 **FAIL**（期望 "dept = ?" 得空）；还原 → PASS。**汇报贴 FAIL→PASS 证据**。

- [ ] **步骤 6：proto 加 `PreviewDataFilter` RPC + message**

service 块加：
```proto
  rpc PreviewDataFilter(PreviewDataFilterRequest) returns (PreviewDataFilterResponse);
```
messages 区加：
```proto
message PreviewDataFilterRequest {
  uint64 app_id = 1;
  string subject = 2;
  string resource = 3;
  map<string, string> attrs = 4;
}
message PreviewDataFilterResponse {
  string sql = 1;
  repeated string args = 2;
}
```
运行：`make proto-gen`（重生成 gen）。

- [ ] **步骤 7：写失败测试 `mgmt/preview_filter_test.go`**

```go
func TestAdminServer_PreviewDataFilter(t *testing.T) {
	s, db := newAdminServer(t) // 以既有 helper 为准
	appID := dbtest.SeedApp(t, db)
	seedRoleAndDataPolicy(t, db, appID, "alice", "viewer", "order", `{"op":"EQ","field":"dept","value":"$user.dept"}`)
	resp, err := s.PreviewDataFilter(context.Background(), &adminv1.PreviewDataFilterRequest{
		AppId: uint64(appID), Subject: "alice", Resource: "order", Attrs: map[string]string{"dept": "shanghai"},
	})
	require.NoError(t, err)
	require.Equal(t, "dept = ?", resp.Sql)
	require.Equal(t, []string{"shanghai"}, resp.Args)
}
```
> mgmt 测试的播种 helper 可能与 effperm 的不同包不可直接复用——用 mgmt 测试既有的 casbin/data_policy 播种方式（读 preview 相邻测试文件）；不确定就停下问控制者。

- [ ] **步骤 8：运行确认失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminServer_PreviewDataFilter -v` → FAIL（未实现）。

- [ ] **步骤 9：实现 `mgmt/preview_filter.go`**

```go
package mgmt

import (
	"context"
	"database/sql"
	"fmt"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PreviewDataFilter 预览数据权限渲染出的参数化 SQL 片段（app 域只读）。
// 鉴权由 AuthorizeRule(scopeApp read) 前置；本 handler 在只读 tx 内复用 effperm.PreviewFilter
// （与 Sidecar 数据面同源，零第二套渲染）。缺变量等 → InvalidArgument（报错而非误导性 SQL）。
func (s *AdminServer) PreviewDataFilter(ctx context.Context, r *adminv1.PreviewDataFilterRequest) (*adminv1.PreviewDataFilterResponse, error) {
	if r.Subject == "" || r.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "subject and resource required")
	}
	attrs := make(map[string]any, len(r.Attrs))
	for k, v := range r.Attrs {
		attrs[k] = v
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := effperm.PreviewFilter(ctx, tx, int64(r.AppId), r.Subject, r.Resource, attrs)
	if err != nil {
		if errorsIsMissingVar(err) {
			return nil, status.Errorf(codes.InvalidArgument, "数据策略引用了未提供的属性：%v", err)
		}
		return nil, status.Errorf(codes.Internal, "preview data filter: %v", err)
	}
	args := make([]string, len(res.Args))
	for i, a := range res.Args {
		args[i] = fmt.Sprintf("%v", a)
	}
	return &adminv1.PreviewDataFilterResponse{Sql: res.SQL, Args: args}, nil
}

func errorsIsMissingVar(err error) bool { return errors.Is(err, dataperm.ErrMissingVar) }
```
> 需 `import "errors"`。若 `errorsIsMissingVar` 与既有重名则内联 `errors.Is`。`dataperm.ErrMissingVar` 是导出哨兵（filter.go）。

- [ ] **步骤 10：运行确认通过 + 零触碰核验 + gofmt**

运行：`go test ./internal/controlplane/mgmt/ ./internal/controlplane/effperm/ -run 'TestAdminServer_PreviewDataFilter|TestPreviewFilter' -v`（PASS）。
运行零触碰硬核验：
```bash
git diff internal/sidecar/dataperm/ internal/sidecar/authz/ internal/kernel/ internal/controlplane/adminauthz/ | wc -l   # 期望 0
```
运行：`gofmt -l internal/controlplane/`（空）。

- [ ] **步骤 11：加 ruleTable 项 + Commit**

authz.go 加：
```go
	"/sydom.admin.v1.AdminService/PreviewDataFilter": {"effective_permission", "read", false, scopeApp},
```
```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ internal/controlplane/effperm/preview.go internal/controlplane/effperm/preview_test.go internal/controlplane/mgmt/preview_filter.go internal/controlplane/mgmt/preview_filter_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(effperm+mgmt): M4.5 PreviewDataFilter 只读 RPC(复用 buildEngine+dataperm.FilterSQL 数据面同源零触碰,scopeApp read,反向验证有齿)"
```

---

## 任务 3：REST parity（两 route）

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`
- 创建：`internal/controlplane/restgw/routes_m45_test.go`

参考既有：`route{method,pattern,fullMethod}` + `allRoutes()`（routes.go）；一条既有 GET 读 route（如 `ExplainDecision` 的 `GET /v1/apps/{app_id}/decision`）作为「解码 path/query → AuthorizeRule → srv 调用 → JSON 编码」的镜像范式；application 管理族既有 route（如 `SetApplicationStatus` 的 `/v1/applications/{app_id}/status`）。

- [ ] **步骤 1：写失败测试 `routes_m45_test.go`**

镜像 restgw 既有 parity 测（见 routes_test.go：起网关、登录/带 app 凭据、打 REST、断言与 gRPC 同结果 + 同 ruleTable 鉴权）。覆盖：
- `GET /v1/applications/{app_id}` → 200，body 含 app_key，**不含 secret**。
- `POST /v1/apps/{app_id}/data-filter/preview`（JSON body `{subject,resource,attrs}`）→ 200，body 含 `sql`/`args`。
- 两 route 未授权 → 403/401（复用既有 parity 断言范式）。
> 具体装配以 routes_test.go 既有用例为准照搬改端点；不确定就停下问控制者。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/restgw/ -run TestM45 -v` → FAIL（route 未注册 → 404）。

- [ ] **步骤 3：实现两 route（routes.go 的 `allRoutes()` 返回切片里加两条）**

镜像既有 GET/POST route 结构（`{method, pattern, fullMethod, handler闭包}`）：
- GET route：`{"GET", "/v1/applications/{app_id}", pfx + "GetApplication", <闭包>}`——闭包解 path `app_id` 组 `GetApplicationRequest`，走既有 AuthorizeRule+srv.GetApplication+JSON 编码（照搬相邻 GET route 闭包骨架）。
- POST route：`{"POST", "/v1/apps/{app_id}/data-filter/preview", pfx + "PreviewDataFilter", <闭包>}`——闭包解 path `app_id` + decode JSON body 到 `PreviewDataFilterRequest`（body 覆写 app_id 为 path 值，遵既有 path 权威覆写纪律），走 AuthorizeRule+srv.PreviewDataFilter+JSON 编码。
> `pfx` 是既有 FullMethod 前缀常量。闭包骨架逐字镜像相邻同 method route，仅换 message 类型与 srv 方法名。**path app_id 权威覆写 body**（与既有写 route 一致）。

- [ ] **步骤 4：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/controlplane/restgw/ -run TestM45 -v`（PASS）+ `go test ./internal/controlplane/restgw/ -count=1`（全绿，含既有 allRoutes 覆盖测试自动纳入两新 route）。
运行：`gofmt -l internal/controlplane/restgw/`（空）。
```bash
git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_m45_test.go
git commit -m "feat(restgw): M4.5 GetApplication+PreviewDataFilter REST parity(GET /v1/applications/{id}+POST /v1/apps/{id}/data-filter/preview,path 权威覆写,同 ruleTable)"
```

---

## 任务 4：Console 件① 接入凭据总览（/developer 一 section）

**文件：**
- 修改：`internal/controlplane/console/routes_developer.go`、`internal/controlplane/console/templates/developer.html`、`internal/controlplane/console/routes_developer_test.go`

参考既有：`routes_developer.go`（M4.4，`developer` handler 现状：requireSession→pathUint64→renderPage）；`routes_decision.go`（AuthorizeRule+srv 调用范式）；`RotateApplicationSecret` 既有入口 `/apps/{id}/rotate-secret`；`developer.html`（M4.4 四 section 外壳）。

- [ ] **步骤 1：写失败测试（routes_developer_test.go 追加）**

```go
func TestConsole_DeveloperPage_ShowsCredentials(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "接入凭据")
	require.Contains(t, body, `id="credentials"`)
	// app_key 应展示（读回自 GetApplication）。种子 app 的 app_key 前缀断言以 SeedApp 实际为准，
	// 退化断言：凭据 section 含 "app_key" 标签且含 domain。
	require.Contains(t, body, "app_key")
	// SD-1：绝不渲染 secret。
	require.NotContains(t, body, "app_secret")
	require.NotContains(t, body, "rotesecret") // 无 secret 明文
}
```
> 若 `SeedApp` 的 app_key 值可知，加 `require.Contains(t, body, <那个 app_key>)` 更有齿；不可知则保留标签断言。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_DeveloperPage_ShowsCredentials -v` → FAIL（无凭据 section）。

- [ ] **步骤 3：handler 增调 GetApplication（routes_developer.go 的 `developer`）**

在 `developer` handler 里 pathUint64 之后、renderPage 之前加（复用 AuthorizeRule + srv.GetApplication）：
```go
	appMsg := &adminv1.GetApplicationRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetApplication", principal, appMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetApplication", err)
		return
	}
	appResp, err := h.srv.GetApplication(actx, appMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetApplication", err)
		return
	}
```
并把 `"App": appResp.Application` 加进 renderPage 的 data map。`developer` handler 需能拿到 `principal`——把 `_, sess, ok := h.requireSession(...)` 改为 `principal, sess, ok := h.requireSession(...)`（principal 现已使用）。确保 import `adminv1`、`mgmt`。`svc` 是既有 FullMethod 前缀常量。

- [ ] **步骤 4：模板加「接入凭据」section（developer.html，放 quickstart 之前或之后）**

```html
<section id="credentials" aria-labelledby="h-credentials" class="developer-doc">
<h2 id="h-credentials">接入凭据</h2>
<p>把下列值注入你的 Sidecar 环境（<strong>secret 仅在轮换时一次性展示，绝不在此显示</strong>）。</p>
<table class="table"><tbody>
<tr><th scope="row">App ID</th><td><code>{{.App.AppId}}</code></td></tr>
<tr><th scope="row">App Key</th><td><code>{{.App.AppKey}}</code></td></tr>
<tr><th scope="row">Domain</th><td><code>{{.App.Domain}}</code></td></tr>
<tr><th scope="row">Sidecar 端点（默认约定）</th><td><code>127.0.0.1:8090</code></td></tr>
</tbody></table>
<p><a href="/apps/{{.AppID}}/rotate-secret">轮换凭据（生成新 secret，一次性展示）</a></p>
</section>
```
> `html/template` 自动转义。无 `<script>`。类名沿用既有 `table`/`developer-doc`。

- [ ] **步骤 5：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/controlplane/console/ -run TestConsole_DeveloperPage -v`（含既有 M4.4 用例 + 新凭据用例全 PASS）。
运行：`gofmt -l internal/controlplane/console/`（空）。
```bash
git add internal/controlplane/console/routes_developer.go internal/controlplane/console/templates/developer.html internal/controlplane/console/routes_developer_test.go
git commit -m "feat(console): M4.5 /developer 接入凭据 section(GetApplication 读 app_key/domain,绝不渲染 secret,轮换入口复用既有,无新 JS)"
```

---

## 任务 5：Console 件② 数据权限沙箱专页

**文件：**
- 创建：`internal/controlplane/console/routes_data_sandbox.go`、`internal/controlplane/console/routes_data_sandbox_test.go`、`internal/controlplane/console/templates/data_sandbox.html`
- 修改：`internal/controlplane/console/handler.go`、`internal/controlplane/console/templates/_appnav.html`

参考既有：`routes_decision.go`（GET 回填读页范式，逐字镜像）；`handler.go`（register 范式，M4.4 加过 `h.registerDeveloper`）；`_appnav.html`（tab 范式）；`data_sandbox` 需要解析 attrs textarea。

- [ ] **步骤 1：写失败测试 `routes_data_sandbox_test.go`**

```go
func TestConsole_DataSandbox_RendersForm(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/data-sandbox")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Equal(t, 1, strings.Count(body, "<h1"))
	require.Contains(t, body, "数据权限沙箱")
	require.Contains(t, body, `name="subject"`)
	require.Contains(t, body, `name="resource"`)
	require.Contains(t, body, `name="attrs"`)
}

func TestConsole_DataSandbox_PreviewsSQL(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	seedRoleAndDataPolicy(t, db, appID, "alice", "viewer", "order", `{"op":"EQ","field":"dept","value":"$user.dept"}`) // 以既有 helper 为准
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/data-sandbox?subject=alice&resource=order&attrs=dept%3Dshanghai")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Contains(t, body, "dept = ?")   // 渲染出的 WHERE
	require.Contains(t, body, "shanghai")   // arg
}

func TestConsole_DataSandbox_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/data-sandbox")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
```
> 沙箱播种 helper 同任务 2 说明（casbin+data_policy）；不确定就停下问控制者。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_DataSandbox -v` → FAIL（路由未注册）。

- [ ] **步骤 3：实现 handler + 注册（routes_data_sandbox.go）**

镜像 `routes_decision.go`：
```go
package console

import (
	"net/http"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerDataSandbox 注册数据权限沙箱专页（建模台开发者区，只读）。
func (h *Handler) registerDataSandbox(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/data-sandbox", h.dataSandbox)
}

// dataSandbox：数据权限沙箱页（读）。GET ?subject=&resource=&attrs= 三者齐备时调 PreviewDataFilter
// 渲染参数化 WHERE+args；否则只渲表单。attrs 为 "key=value 每行" 文本，服务端解析（无新 JS）。
func (h *Handler) dataSandbox(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
		return
	}
	subject := r.FormValue("subject")
	resource := r.FormValue("resource")
	attrsRaw := r.FormValue("attrs")
	data := map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "datasandbox",
		"Subject": subject, "Resource": resource, "AttrsRaw": attrsRaw, "CSRF": sess.CSRF,
	}
	if subject != "" && resource != "" {
		msg := &adminv1.PreviewDataFilterRequest{
			AppId: appID, Subject: subject, Resource: resource, Attrs: parseAttrs(attrsRaw),
		}
		ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"PreviewDataFilter", principal, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
			return
		}
		resp, err := h.srv.PreviewDataFilter(ctx, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
			return
		}
		data["Queried"] = true
		data["SQL"] = resp.Sql
		data["Args"] = resp.Args
	}
	h.renderPage(w, r, "data_sandbox.html", http.StatusOK, data)
}

// parseAttrs 把 "key=value 每行" 文本解析为 map（空行/无 = 的行跳过；只切首个 =）。
func parseAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}
```
在 `handler.go` 的 `NewHandler` 里、`h.registerDeveloper(mux)` 之后加：`h.registerDataSandbox(mux)   // M4.5 数据权限沙箱`。

- [ ] **步骤 4：模板 `templates/data_sandbox.html`（镜像 decision.html 外壳）**

```html
{{define "title"}}数据权限沙箱 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section class="developer-doc">
<nav class="breadcrumb" aria-label="面包屑">建模台 · 数据权限沙箱</nav>
<h1>数据权限沙箱</h1>
<p>输入主体、资源与属性，预览数据面 <code>FilterSQL</code> 会渲染出的参数化 WHERE 片段（与 SDK 接入所得一致）。功能权限「试一试」见 <a href="/apps/{{.AppID}}/decision">决策解释</a>。</p>
<form method="get" action="/apps/{{.AppID}}/data-sandbox" class="stack-form">
<label for="subject">主体（user）</label>
<input id="subject" name="subject" value="{{.Subject}}" required>
<label for="resource">资源（resource）</label>
<input id="resource" name="resource" value="{{.Resource}}" required>
<label for="attrs">属性（每行 key=value）</label>
<textarea id="attrs" name="attrs" rows="4" placeholder="dept=shanghai">{{.AttrsRaw}}</textarea>
<button class="btn btn-primary">预览</button>
</form>
{{if .Queried}}
<section aria-labelledby="h-result">
<h2 id="h-result">渲染结果</h2>
{{if .SQL}}
<p>WHERE 片段（参数化）：</p>
<pre><code>{{.SQL}}</code></pre>
<p>参数（args，按序绑定，值绝不进 SQL 文本）：</p>
<ul>{{range .Args}}<li><code>{{.}}</code></li>{{end}}</ul>
{{else}}
<p role="status">无行级限制：该主体对该资源全部可见。</p>
{{end}}
</section>
{{end}}
</section></div>{{end}}
```
> 无 `<script>`。类名沿用设计系统（`workspace`/`developer-doc`/`table`/`btn`/`stack-form` 若不存在则复用既有表单类，以 decision.html 为准）。

- [ ] **步骤 5：_appnav 加「沙箱」tab（放「开发者」后）**

```html
<a href="/apps/{{.AppID}}/data-sandbox" {{if eq .Tab "datasandbox"}}aria-current="page"{{end}}>沙箱</a>
```

- [ ] **步骤 6：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_DataSandbox|TestConsole_DeveloperPage' -v`（PASS）。
运行：`gofmt -l internal/controlplane/console/`（空）。
```bash
git add internal/controlplane/console/routes_data_sandbox.go internal/controlplane/console/routes_data_sandbox_test.go internal/controlplane/console/templates/data_sandbox.html internal/controlplane/console/handler.go internal/controlplane/console/templates/_appnav.html
git commit -m "feat(console): M4.5 数据权限沙箱专页(GET 回填复用 PreviewDataFilter,attrs 每行 key=value 服务端解析,_appnav 沙箱 tab,无新 JS)"
```

---

## 任务 6：整体核验 SD-1..7 + 真实浏览器 axe 走查 + 最终评审 + FF

**文件：** 无代码改动（除走查涌现修复）；产出 `docs/superpowers/2026-07-07-m4-5-developer-sandbox-walkthrough.md`。

- [ ] **步骤 1：SD-2/SD-6 零触碰核验**

```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- internal/sidecar/dataperm/ internal/sidecar/authz/ internal/kernel/ internal/controlplane/adminauthz/ casbin/ | wc -l   # 期望 0（数据面 eval + 决策核心零触碰）
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | grep -c '^+.*ruleTable\|^+.*AdminService'   # 仅 +2 read 项
```
预期：数据面/决策核心 diff=0；authz.go 仅 +2 read ruleTable 项。

- [ ] **步骤 2：全量验证**

```bash
make proto-gen          # 幂等：gen 与 proto 同步，无未提交 diff
gofmt -l internal/      # 空
go vet ./...            # 干净
go test ./...           # 0 FAIL（含 mgmt/effperm/restgw/console 新测试）
```
> `make proto-gen` 后 `git status` 应干净（gen 已在任务 1/2 提交）。

- [ ] **步骤 3：真实浏览器 axe 走查（SD-7）**

复用 M4.4 走查脚手架范式（build-tag `walkthrough` 复用 `newConsole`+dbtest、会话 TTL `time.Hour`、播种 app + 一条数据策略、打印 URL、阻塞待 SIGTERM）+ 系统 Chrome via Playwright MCP（`--prefer-offline @0.0.77`）+ axe-core 4.10.2（jsdelivr）。走查：
- `/apps/1/developer`（含新凭据 section）：axe 0 违规 + 单 h1 + 凭据 section 展示 app_key + **DOM 全文无 secret**。
- `/apps/1/data-sandbox`：axe 0 违规 + 单 h1 + breadcrumb + 表单控件 aria-label；端到端搭 subject/resource/attrs → 提交 → 渲染 WHERE+args。
记录到 walkthrough.md 并 commit。**走查纪律**：停后台按确切 PID；脚手架走查后删除未提交。

- [ ] **步骤 4：最终整体评审**

派子代理（或控制者 inline）逐条核验 SD-1..7：SD-1 无 secret（凭据/沙箱路径 diff+浏览器扫）、SD-2 单源零触碰数据面 eval（diff 证明）、SD-3 参数化（值进 args）、SD-4 只读 fail-close、SD-5 无新 JS、SD-6 authz 仅 +2 read、SD-7 axe 0。产出 READY 或阻断清单。

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加 M4.5 节 + M4 收官；`MEMORY.md` M4 索引下标 M4.5 ✅、M4 完结。

- [ ] **步骤 6：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m4-5-developer-sandbox
```
核实 main==feature tip；push origin 与否问用户；清理 worktree。

---

## 自检（写完计划后，对照规格）

**1. 规格覆盖度：**
- §3.1 GetApplication → 任务 1 ✅；§3.2 PreviewDataFilter → 任务 2 ✅
- §4.1 凭据 section → 任务 4 ✅；§4.2 沙箱专页 → 任务 5 ✅
- §REST parity → 任务 3 ✅
- SD-1 无 secret（任务 1/4）、SD-2 单源零触碰（任务 2/6）、SD-3 参数化（任务 2/5）、SD-4 只读 fail-close（任务 2/5）、SD-5 无新 JS（任务 4/5）、SD-6 authz +2（任务 1/2/6）、SD-7 axe（任务 6）✅
- §7 测试策略 → 各任务 TDD + 任务 6 ✅；§8 任务分解 → 6 任务 ✅

**2. 占位符扫描：** 无 TODO；每步含实际代码/命令/预期。播种 helper 名标注「以既有为准/不确定就停下问控制者」是刻意的（effperm/mgmt/console 各包测试装配不同，实现者须对齐真实 helper，勿臆造 schema）。

**3. 类型一致性：**
- `effperm.PreviewFilter(ctx,tx,appID,subject,resource,attrs) (dataperm.SQLResult,error)`（任务 2）→ mgmt handler 一致引用 ✅
- `PreviewDataFilterRequest{app_id,subject,resource,attrs map<string,string>}` / `Response{sql,args []string}`（任务 2 proto）→ 任务 3 REST、任务 5 Console 一致 ✅
- `GetApplicationResponse{application ApplicationSummary}`（任务 1）→ 任务 4 模板 `.App.AppId/.AppKey/.Domain` 一致 ✅
- `parseAttrs` map[string]string（任务 5）→ `PreviewDataFilterRequest.Attrs` 一致 ✅

对照无缺口。
