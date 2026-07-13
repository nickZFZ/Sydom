# M6.1b 用量计量可见性 RPC `GetTenantUsage` — 设计规格

> M6.1（用量计量 + 配额）第二增量。BASE=main `73efd45`（M6.1a 应用配额）。M6.1a 立了套餐+配额+强制；本增量补**计量可见性**——只读 RPC 返租户套餐 + 各资源用量/上限，供运营台/客户看「用了多少、还剩多少」。

## 1. 背景与目标

M6.1a 让超额 fail-close，但租户/运营看不到当前用量与上限。计费/配额产品需**用量可见**。目标：加只读 `GetTenantUsage(tenant_id)` → `{plan_name, applications:{used,limit}}`，gRPC + REST parity，tenant-scoped 授权（租户看自己、root 看全部），fail-close，零触碰授权求值核心。

**非目标（留后续）**：角色/数据策略/成员维度用量（本增量仅 applications，与 M6.1a 已强制维度对齐）；用量事件计量（Check/API 高频聚合）；Console 用量 UI（本增量只到 API 层，Console 消费留 UI 增量）；套餐管理/升级/计费。

## 2. 现状（实查）

- M6.1a：`plan(name,max_applications)` + `tenant.plan_id`；`store.TenantPlanLimits`（FOR UPDATE，写路径用）+`CountApplications`。
- authz：`scopeTenant`（域取自请求 `tenant_id`，0→"*" 否则 t:<id>）已存在；`ListApplications` 即 `{"application","read",false,scopeTenant}`。请求实现 `GetTenantId() uint64` 即被 scopeTenant 解析。
- proto：`api/proto/sydom/admin/v1/admin.proto`（`AdminService`）；`buf generate` 可用无漂移；新增 RPC 为 **additive → `make proto-breaking`（M6.5）通过**。
- REST：`routes.go` route{method,pattern,fullMethod,decode,invoke}；`queryInt64(r,"tenant_id")` 读查询参数（ListApplications 同款）。
- gRPC handler = `AdminServer` 方法；`store.ErrNotFound` 既有。

## 3. 方案

**A（选定）加 additive RPC + tenant-scoped 授权 + 只读 store 助手 + REST 路由。** 镜像 `ListApplications`（scopeTenant read）；新只读 `store.TenantUsage`（**不加 FOR UPDATE**，读路径无需锁）返套餐名+上限+用量。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `api/proto/sydom/admin/v1/admin.proto`（改） | 加 `GetTenantUsage` RPC + 3 消息（additive） |
| `gen/...`（改，`buf generate` 产） | 生成码同步 |
| `internal/controlplane/store/quota.go`（改） | 加只读 `TenantUsage(ctx,ex,tenantID)` |
| `internal/controlplane/mgmt/tenant_usage.go`（新） | `GetTenantUsage` handler |
| `internal/controlplane/mgmt/authz.go`（改） | ruleTable 加 GetTenantUsage → `{"application","read",false,scopeTenant}` |
| `internal/controlplane/restgw/routes.go`（改） | 加 `GET /v1/tenant-usage?tenant_id=` 路由 |
| 测试（新） | store TenantUsage + mgmt GetTenantUsage(含 NotFound) + REST 路由派生断言 |

### 4.2 proto（additive）

```proto
// service AdminService 内，applications 区加：
rpc GetTenantUsage(GetTenantUsageRequest) returns (GetTenantUsageResponse);

message GetTenantUsageRequest { uint64 tenant_id = 1; }
message ResourceUsage { uint32 used = 1; uint32 limit = 2; }
message GetTenantUsageResponse {
  string plan_name = 1;
  ResourceUsage applications = 2;
}
```
`tenant_id=1` → 生成 `GetTenantId()` → scopeTenant 解析。

### 4.3 store `TenantUsage`

```go
type TenantUsage struct {
	PlanName        string
	MaxApplications int
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
	if errors.Is(err, sql.ErrNoRows) { return TenantUsage{}, ErrNotFound }
	return u, err
}
```
（与 `PlanLimits` 并存；命名 `TenantUsageOf` 避免与 struct `TenantUsage` 撞。）

### 4.4 handler

```go
func (s *AdminServer) GetTenantUsage(ctx context.Context, r *adminv1.GetTenantUsageRequest) (*adminv1.GetTenantUsageResponse, error) {
	u, err := store.TenantUsageOf(ctx, s.db, int64(r.TenantId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil { return nil, status.Errorf(codes.Internal, "%v", err) }
	return &adminv1.GetTenantUsageResponse{
		PlanName: u.PlanName,
		Applications: &adminv1.ResourceUsage{Used: uint32(u.UsedApplications), Limit: uint32(u.MaxApplications)},
	}, nil
}
```
授权由拦截器经 ruleTable(scopeTenant) 完成（租户看自己、root 看全部、跨租户 PermissionDenied 早于 handler）；NotFound 仅 root/system 可达（tenant admin 跨租户先被 authz 拒）。

### 4.5 授权 + REST

- ruleTable：`"/sydom.admin.v1.AdminService/GetTenantUsage": {"application", "read", false, scopeTenant}`（复用 application:read，租户能读应用即能看用量）。
- REST：`{"GET","/v1/tenant-usage", pfx+"GetTenantUsage", decode: queryInt64 tenant_id → GetTenantUsageRequest, invoke: s.GetTenantUsage}`。

## 5. 验证

- **store**：`TenantUsageOf` 返正确 plan_name/limit/used（free=3、seed 1 app→used 1）；unknown→ErrNotFound。
- **handler**：GetTenantUsage 返 {free,used,limit}；建应用后 used 递增；未知租户 NotFound。
- **REST 派生**：`allRoutes()` 含 `GET /v1/tenant-usage`→GetTenantUsage（apidoc Routes 断言）。
- **proto 兼容**：`make proto-breaking`（对 main，additive）PASS；`make proto-check` 无漂移（gen 已提交）。
- **零触碰**：`git diff 73efd45..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/ internal/auth/`=空（authz.go 是 mgmt ruleTable 非授权求值核心）。
- `go test ./...` EXIT 0。

## 6. 验收标准（M61B-1..7）

- **M61B-1** 零触碰授权求值核心：上述 diff=空。
- **M61B-2** proto additive：`GetTenantUsage`+3 消息；`make proto-breaking` PASS（向后兼容）；`buf generate` 无漂移。
- **M61B-3** store `TenantUsageOf` 只读返 plan/limit/used + unknown ErrNotFound。
- **M61B-4** handler 返正确用量；建应用后 used+1；未知租户 NotFound。
- **M61B-5** authz scopeTenant：ruleTable 有 GetTenantUsage 项；请求 `GetTenantId()` 存在（tenant-scoped）。
- **M61B-6** REST parity：`GET /v1/tenant-usage` 在 allRoutes、映 GetTenantUsage。
- **M61B-7** `go test ./...` EXIT 0；`make proto-check` 绿。

## 7. 风险

- **gen 漂移**：`buf generate` 后须提交 gen/；`make proto-check` 兜底。
- **scopeTenant 复用 application:read**：语义合理（读应用=看用量）；若日后要独立 usage 权限再加 resource，本增量不引入新权限定义（免播种）。
- **只读无锁 vs 写路径锁**：`TenantUsageOf` 无 FOR UPDATE（读快照即可，用量是瞬时视图，无需与写串行）；与 `TenantPlanLimits`（写路径锁）职责分离。
