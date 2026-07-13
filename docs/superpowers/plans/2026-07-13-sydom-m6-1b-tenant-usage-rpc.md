# M6.1b 用量计量 RPC `GetTenantUsage` 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 加 additive 只读 RPC `GetTenantUsage` 返租户套餐+应用用量/上限，gRPC+REST parity，scopeTenant 授权。

**架构：** proto 加 RPC+3 消息（additive→过 proto-breaking）；store 只读 `TenantUsageOf`（无锁）；mgmt handler；ruleTable scopeTenant 项；REST `GET /v1/tenant-usage`。零触碰授权求值核心。

**技术栈：** protobuf/buf、gRPC、`database/sql`、restgw。

**BASE：** `feat/m6-1b-tenant-usage-rpc` @ 含规格提交；规格 `docs/superpowers/specs/2026-07-13-sydom-m6-1b-tenant-usage-rpc-design.md`。

**零触碰铁律：** `git diff 73efd45..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth` 必须为空。

---

## 任务 1：proto 加 RPC + 消息 + 重新生成

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/...`（`buf generate`）

- [ ] **步骤 1：加 RPC + 消息**

在 `admin.proto` 的 `service AdminService {` 里、`rpc GetApplication(...)` 之后加一行：
```proto
  rpc GetTenantUsage(GetTenantUsageRequest) returns (GetTenantUsageResponse);
```
在文件消息定义区（`ListApplicationsResponse` 附近）加：
```proto
message GetTenantUsageRequest { uint64 tenant_id = 1; }
message ResourceUsage { uint32 used = 1; uint32 limit = 2; }
message GetTenantUsageResponse {
  string plan_name = 1;
  ResourceUsage applications = 2;
}
```

- [ ] **步骤 2：lint + 生成 + 兼容门**

运行：
```bash
make proto-lint && echo LINT-OK
PATH="$(go env GOPATH)/bin:$PATH" buf generate && echo GEN-OK
make proto-breaking && echo BREAKING-OK   # additive → 对 main 应 PASS
go build ./... && echo BUILD-OK
```
预期：LINT-OK、GEN-OK（gen/ 出现 GetTenantUsage 类型）、BREAKING-OK（additive 向后兼容）、BUILD-OK。

- [ ] **步骤 3：Commit（proto + gen 同提交，防漂移）**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M6.1b 加 additive GetTenantUsage RPC(GetTenantUsageRequest tenant_id+ResourceUsage used/limit+GetTenantUsageResponse plan_name/applications;buf generate 同步;additive 过 proto-breaking)"
```

---

## 任务 2：store 只读 `TenantUsageOf`

**文件：**
- 修改：`internal/controlplane/store/quota.go`
- 修改：`internal/controlplane/store/quota_test.go`

- [ ] **步骤 1：加 TenantUsage 类型 + TenantUsageOf**

在 `quota.go` 末尾加：
```go
// TenantUsage 是租户的套餐名 + 各资源用量/上限（本增量仅 applications）。
type TenantUsage struct {
	PlanName         string
	MaxApplications  int
	UsedApplications int
}

// TenantUsageOf 只读返租户套餐名 + 应用上限 + 当前应用数（无锁，读路径）。租户不存在→ErrNotFound。
func TenantUsageOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantUsage, error) {
	var u TenantUsage
	err := ex.QueryRowContext(ctx,
		`SELECT p.name, p.max_applications,
		        (SELECT count(*) FROM application a WHERE a.tenant_id = t.id)
		   FROM tenant t JOIN plan p ON p.id = t.plan_id WHERE t.id = $1`,
		tenantID).Scan(&u.PlanName, &u.MaxApplications, &u.UsedApplications)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantUsage{}, ErrNotFound
	}
	return u, err
}
```

- [ ] **步骤 2：测试**

在 `quota_test.go` 加：
```go
func TestTenantUsageOf(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	u, err := store.TenantUsageOf(context.Background(), db, tenantID)
	require.NoError(t, err)
	require.Equal(t, "free", u.PlanName)
	require.Equal(t, 3, u.MaxApplications)
	require.Equal(t, 1, u.UsedApplications, "seed 1 应用")

	_, err = store.TenantUsageOf(context.Background(), db, 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}
```
运行：`go test ./internal/controlplane/store/ -run TestTenantUsageOf -v` → PASS。

- [ ] **步骤 3：Commit**

```bash
git add internal/controlplane/store/quota.go internal/controlplane/store/quota_test.go
git commit -m "feat(cp): M6.1b store TenantUsageOf 只读(JOIN tenant×plan+子查询 count applications,无锁读路径,unknown→ErrNotFound)"
```

---

## 任务 3：mgmt handler + ruleTable

**文件：**
- 创建：`internal/controlplane/mgmt/tenant_usage.go`
- 修改：`internal/controlplane/mgmt/authz.go`
- 创建：`internal/controlplane/mgmt/tenant_usage_test.go`

- [ ] **步骤 1：handler**

`internal/controlplane/mgmt/tenant_usage.go`：
```go
package mgmt

import (
	"context"
	"errors"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTenantUsage 只读返租户套餐 + 应用用量/上限。授权由拦截器经 ruleTable(scopeTenant) 完成：
// 租户看自己、root 看全部、跨租户 PermissionDenied 早于本 handler。
func (s *AdminServer) GetTenantUsage(ctx context.Context, r *adminv1.GetTenantUsageRequest) (*adminv1.GetTenantUsageResponse, error) {
	u, err := store.TenantUsageOf(ctx, s.db, int64(r.TenantId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &adminv1.GetTenantUsageResponse{
		PlanName:     u.PlanName,
		Applications: &adminv1.ResourceUsage{Used: uint32(u.UsedApplications), Limit: uint32(u.MaxApplications)},
	}, nil
}
```

- [ ] **步骤 2：ruleTable 项**

在 `internal/controlplane/mgmt/authz.go` 的 ruleTable 里，`ListApplications` 项附近加：
```go
	"/sydom.admin.v1.AdminService/GetTenantUsage":              {"application", "read", false, scopeTenant},
```

- [ ] **步骤 3：测试**

`internal/controlplane/mgmt/tenant_usage_test.go`（`package mgmt_test`，复用任务 M6.1a 的 fixture 模式）：
```go
package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTenantUsage(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))
	s := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	resp, err := s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: uint64(tenantID)})
	require.NoError(t, err)
	require.Equal(t, "free", resp.PlanName)
	require.Equal(t, uint32(1), resp.Applications.Used)
	require.Equal(t, uint32(3), resp.Applications.Limit)

	// 建第二个应用 → used 递增
	_, err = s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: uint64(tenantID), Domain: "d2", Name: "n", AppKey: "ak2"})
	require.NoError(t, err)
	resp, err = s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: uint64(tenantID)})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Applications.Used)

	// 未知租户 NotFound
	_, err = s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```
运行：`go test ./internal/controlplane/mgmt/ -run TestGetTenantUsage -v` → PASS。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/mgmt/tenant_usage.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/tenant_usage_test.go
git commit -m "feat(cp): M6.1b GetTenantUsage handler(只读 TenantUsageOf,unknown 租户 NotFound)+ruleTable scopeTenant application:read(租户看自己 root 看全);建应用后 used 递增测试"
```

---

## 任务 4：REST 路由 + 最终验收

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`
- 修改/新增：restgw apidoc 或 routes 测试

- [ ] **步骤 1：REST 路由**

在 `internal/controlplane/restgw/routes.go` 的 applications 路由块里（`GetApplication` 路由之后、`}` 之前），加：
```go
		{"GET", "/v1/tenant-usage", pfx + "GetTenantUsage",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tid, err := queryInt64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetTenantUsageRequest{TenantId: uint64(tid)}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetTenantUsage(ctx, m.(*adminv1.GetTenantUsageRequest))
			}},
```

- [ ] **步骤 2：REST 派生断言**

若本包已有 route 覆盖测试（查 `routes_test.go`/`apidoc_test.go`），加一条断言 `allRoutes()`/`Routes()` 含 `GET /v1/tenant-usage`→`...GetTenantUsage`；否则新增最小测试：
```go
func TestRoute_TenantUsage(t *testing.T) {
	found := false
	for _, r := range Routes() { // restgw.Routes() 只读派生
		if r.Method == "GET" && r.Pattern == "/v1/tenant-usage" {
			require.Contains(t, r.FullMethod, "GetTenantUsage")
			found = true
		}
	}
	require.True(t, found, "应有 GET /v1/tenant-usage 路由")
}
```
（`package restgw`；`Routes()` 见 apidoc.go。）运行：`go test ./internal/controlplane/restgw/ -run TenantUsage -v` → PASS。

- [ ] **步骤 3：最终验收**

运行：
```bash
make proto-check && echo PROTO-CHECK-OK      # gen 无漂移
go build ./... && go test ./... 2>&1 | grep -E "FAIL|panic" | head; echo "GO-EXIT=${PIPESTATUS[1]}"
make proto-breaking && echo BREAKING-OK
git diff 73efd45..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth | head; echo "ZERO-TOUCH-DONE(空)"
```
预期：PROTO-CHECK-OK、`go test ./...` 无 FAIL（GO-EXIT=0）、BREAKING-OK、零触碰空。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/restgw/
git commit -m "feat(cp): M6.1b REST GET /v1/tenant-usage?tenant_id→GetTenantUsage(三面 parity;路由派生断言)"
```

---

## 自检

**1. 规格覆盖度：** §4.1 文件→任务1(proto)+2(store)+3(handler/authz)+4(REST)；§4.2/4.3/4.4/4.5→各任务；§5 验证→各任务测试+任务4验收；§6 M61B-1..7→M61B-1 任务4步3、M61B-2 任务1步2、M61B-3 任务2、M61B-4 任务3步3、M61B-5 任务3步2、M61B-6 任务4、M61B-7 任务4步3。全覆盖。

**2. 占位符扫描：** proto/store/handler/route 为实代码+命令+预期；测试用既有 fixture 模式（`mgmt.NewAdminServer`+`cp.WithOperator`+`mk()`）。

**3. 类型一致性：** `adminv1.GetTenantUsageRequest{TenantId uint64}`（→`GetTenantId()`）、`GetTenantUsageResponse{PlanName string; Applications *ResourceUsage}`、`ResourceUsage{Used,Limit uint32}`（proto 定义、handler/测试一致）；`store.TenantUsage{PlanName,MaxApplications,UsedApplications}`+`TenantUsageOf(ctx,cp.DBTX,int64)(TenantUsage,error)`（任务2 定义、任务3 调用）；`restgw.Routes()` 返 `RouteDoc{Method,Pattern,FullMethod}`（apidoc.go 既有）；ruleTable `scopeTenant`、`queryInt64`、`pfx` 均既有。
