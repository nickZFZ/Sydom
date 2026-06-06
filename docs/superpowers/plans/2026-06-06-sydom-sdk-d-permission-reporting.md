# 司域 SDK ⑤-D：权限点上报 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法跟踪进度。

**目标：** 业务 app 经 SDK→Sidecar→控制面 上报权限点，app 凭据认证、`source=auto` 幂等 upsert、不覆盖人工配置。

**架构：** SDK 显式注册收集 → 调本地 `AuthService.ReportPermissions`（新增 RPC）→ Sidecar `authz` 委托 `syncclient` 经已认证 `PolicySync` 连接转发 → 控制面 `PolicySync.ReportPermissions`（新增 RPC）→ `PolicyManager.ReportPermissions` 走 `runVersionedWrite`（纯目录无 bump）→ `store.UpsertAutoPermission` 冲突保留 manual。

**技术栈：** Go 1.26.3；buf v1.34.0（proto regen）；testcontainers PG（store/policy/policysync/E2E 任务需 Docker）；bufconn（syncclient/authz/SDK 任务）。

**跨层类型链（贯穿一致）：** `sydom.Permission`→`authv1.PermissionPoint`→`syncclient.PermissionPoint`→`syncv1.PermissionPoint`→`cp.PermissionPoint`→`store.UpsertAutoPermission(逐字段)`。计数 `ReportResult{Upserted,Skipped int}` 在 cp/syncclient/sydom 各定义一份同形。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `api/proto/.../sync/v1/policy_sync.proto`（+gen） | PolicySync 加 ReportPermissions RPC + 3 message |
| `api/proto/.../auth/v1/auth.proto`（+gen） | AuthService 加 ReportPermissions RPC + 3 message |
| `internal/controlplane/store/store.go` | +UpsertAutoPermission |
| `internal/controlplane/types.go` | +PermissionPoint、ReportResult |
| `internal/controlplane/policy/manager.go` | +PolicyManager.ReportPermissions |
| `internal/controlplane/policysync/server.go` | +Server.ReportPermissions + permissionReporter 接口；NewServer 签名加 reporter |
| `internal/controlplane/app/run.go` | NewServer 调用点传入 mgr |
| `internal/sidecar/syncclient/client.go` | +SyncClient.ReportPermissions + PermissionPoint/ReportResult 域类型 |
| `internal/sidecar/authz/server.go` | +Server.ReportPermissions + PermissionRelay 接口；NewServer/NewGRPCServer 签名加 relay |
| `internal/sidecar/app/run.go` | NewGRPCServer 调用点传入 syncCli |
| `sdk/go/sydom/permission.go` | Permission/ReportResult/Client.ReportPermissions/Registry/PermissionReporter |

---

### 任务 1：proto 加 ReportPermissions（两服务）+ regen

**文件：**
- 修改：`api/proto/sydom/sync/v1/policy_sync.proto`、`api/proto/sydom/auth/v1/auth.proto`
- 生成：`gen/sydom/sync/v1/*`、`gen/sydom/auth/v1/*`

- [ ] **步骤 1：改 sync.v1 proto**

在 `api/proto/sydom/sync/v1/policy_sync.proto` 的 `service PolicySync { ... }` 内、`PullSnapshot` 之后加一行：

```proto
  // ReportPermissions 批量上报权限点目录（app 凭据，幂等 upsert，source=auto）。
  rpc ReportPermissions(ReportPermissionsRequest) returns (ReportPermissionsResponse);
```

在文件末尾追加：

```proto
// ReportPermissionsRequest 批量上报权限点。app_id 由认证凭据强制，不在请求体。
message ReportPermissionsRequest {
  repeated PermissionPoint permissions = 1;
}

// PermissionPoint 是一条权限点目录元数据（功能权限定义，非授权）。
message PermissionPoint {
  string code = 1;
  string resource = 2;
  string action = 3;
  string type = 4;
  string name = 5;
  string description = 6;
}

// ReportPermissionsResponse 返回写入统计。
message ReportPermissionsResponse {
  uint32 upserted = 1; // 新增或刷新（source=auto）的条数
  uint32 skipped = 2;  // 命中 source=manual 行被保留而跳过的条数
}
```

- [ ] **步骤 2：改 auth.v1 proto**

在 `api/proto/sydom/auth/v1/auth.proto` 的 `service AuthService { ... }` 内、`FilterSQL` 之后加一行：

```proto
  // ReportPermissions 把业务进程的权限点上报中继到控制面（Sidecar 转发，域由 Sidecar pin）。
  rpc ReportPermissions(ReportPermissionsRequest) returns (ReportPermissionsResponse);
```

文件末尾追加（与 sync.v1 同形，不同 package 不冲突）：

```proto
// ReportPermissionsRequest 批量上报权限点（本地：无 app_id，Sidecar pin 域）。
message ReportPermissionsRequest {
  repeated PermissionPoint permissions = 1;
}

message PermissionPoint {
  string code = 1;
  string resource = 2;
  string action = 3;
  string type = 4;
  string name = 5;
  string description = 6;
}

message ReportPermissionsResponse {
  uint32 upserted = 1;
  uint32 skipped = 2;
}
```

- [ ] **步骤 3：regen + 编译**

运行：`make proto-tools && make proto-gen && go build ./...`
预期：`buf lint` 过、生成代码更新、全仓编译通过。若 `make proto-tools` 已装过可省，但 `make proto-gen` 必跑。

- [ ] **步骤 4：漂移检查**

运行：`git add -A && make proto-check`
预期：`proto-check`（= proto-gen + `git diff --exit-code gen/`）无差异（生成代码与 proto 同步且已暂存）。

- [ ] **步骤 5：Commit**

```bash
git add api/ gen/
git commit -m "feat(proto): PolicySync/AuthService 加 ReportPermissions RPC（⑤-D 任务 1）"
```
提交信息末尾另起一行：`Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`

---

### 任务 2：store.UpsertAutoPermission（冲突保留 manual）

**文件：**
- 修改：`internal/controlplane/store/store.go`
- 测试：`internal/controlplane/store/store_test.go`

- [ ] **步骤 1：编写失败的测试**（追加到 `store_test.go`，需 Docker）

```go
func TestUpsertAutoPermission_InsertAndRefresh(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 新增 → applied，source=auto
	applied, err := store.UpsertAutoPermission(ctx, db, appID, "p.read", "order", "read", "api", "读订单", "")
	require.NoError(t, err)
	require.True(t, applied)
	var src, name string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source, name FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.read").Scan(&src, &name))
	require.Equal(t, "auto", src)
	require.Equal(t, "读订单", name)

	// 同 code 再报（已是 auto）→ applied，字段刷新
	applied, err = store.UpsertAutoPermission(ctx, db, appID, "p.read", "order", "read", "api", "读订单V2", "desc")
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT name FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.read").Scan(&name))
	require.Equal(t, "读订单V2", name)
}

func TestUpsertAutoPermission_NeverClobbersManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 预置一条人工权限点（source=manual）
	_, err := db.ExecContext(ctx,
		`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		 VALUES ($1,$2,$3,$4,$5,$6,'manual')`,
		appID, "p.manual", "order", "write", "api", "人工写订单")
	require.NoError(t, err)

	// auto 上报同 code → skipped，manual 行原样保留
	applied, err := store.UpsertAutoPermission(ctx, db, appID, "p.manual", "CHANGED", "CHANGED", "x", "篡改", "x")
	require.NoError(t, err)
	require.False(t, applied)
	var src, resource, name string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source, resource, name FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.manual").Scan(&src, &resource, &name))
	require.Equal(t, "manual", src)
	require.Equal(t, "order", resource) // 未被篡改
	require.Equal(t, "人工写订单", name)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run TestUpsertAutoPermission -v`
预期：编译失败（`store.UpsertAutoPermission` 未定义）。

- [ ] **步骤 3：实现**（追加到 `store.go`；确保文件已 import `database/sql` 与 `errors`，没有则加）

```go
// UpsertAutoPermission 上报式幂等写权限点：新增标 source='auto'；冲突时仅当现有行
// source='auto' 才覆盖（DO UPDATE ... WHERE），命中 manual 行原样保留（§8 不覆盖人工配置）。
// 返回 applied：true=新增或刷新了 auto 行；false=命中 manual 行被跳过（非错误）。
func UpsertAutoPermission(ctx context.Context, ex cp.DBTX, appID int64, code, resource, action, permType, name, description string) (bool, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO permission (app_id, code, resource, action, type, name, description, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'auto')
		ON CONFLICT (app_id, code) DO UPDATE SET
			resource=EXCLUDED.resource, action=EXCLUDED.action, type=EXCLUDED.type,
			name=EXCLUDED.name, description=EXCLUDED.description, updated_at=now()
		WHERE permission.source='auto'
		RETURNING id`, appID, code, resource, action, permType, name, description).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // 命中 manual 行，DO UPDATE 的 WHERE 为假，零行返回 → 跳过
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -run TestUpsertAutoPermission -v`
预期：两个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/store.go internal/controlplane/store/store_test.go
git commit -m "feat(cp): store.UpsertAutoPermission — 冲突保留 manual（⑤-D 任务 2）"
```
末尾加 Co-Authored-By 行。

---

### 任务 3：cp 类型 + PolicyManager.ReportPermissions

**文件：**
- 修改：`internal/controlplane/types.go`、`internal/controlplane/policy/manager.go`
- 测试：`internal/controlplane/policy/manager_test.go`

- [ ] **步骤 1：编写失败的测试**（追加到 `manager_test.go`，需 Docker）

```go
func TestReportPermissions_CatalogOnly_NoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	res, err := mgr.ReportPermissions(ctx, appID, []cp.PermissionPoint{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.write", Resource: "order", Action: "write", Type: "api", Name: "写"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Upserted)
	require.Equal(t, 0, res.Skipped)

	// 纯目录上报：无授权 → 无投影 diff → 版本不 bump
	var v int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v))
	require.Equal(t, int64(0), v)
}

func TestReportPermissions_MixAutoManual_Counts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		VALUES ($1,'p.m','order','write','api','人工','manual')`, appID)
	require.NoError(t, err)
	mgr := policy.NewPolicyManager(db, nil)

	res, err := mgr.ReportPermissions(context.Background(), appID, []cp.PermissionPoint{
		{Code: "p.a", Resource: "order", Action: "read", Type: "api", Name: "自动"},
		{Code: "p.m", Resource: "x", Action: "x", Type: "x", Name: "篡改"}, // 命中 manual → skipped
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Upserted)
	require.Equal(t, 1, res.Skipped)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestReportPermissions -v`
预期：编译失败（`cp.PermissionPoint`/`PolicyManager.ReportPermissions` 未定义）。

- [ ] **步骤 3：实现 cp 类型**（追加到 `internal/controlplane/types.go`）

```go
// PermissionPoint 是一条上报的权限点目录元数据。
type PermissionPoint struct {
	Code        string
	Resource    string
	Action      string
	Type        string
	Name        string
	Description string
}

// ReportResult 是一次权限点上报的写入统计。
type ReportResult struct {
	Upserted int // 新增或刷新（source=auto）
	Skipped  int // 命中 manual 行被保留
}
```

- [ ] **步骤 4：实现 PolicyManager.ReportPermissions**（追加到 `manager.go`）

```go
// ReportPermissions 批量上报权限点（app 凭据来源，全标 source=auto）。
// 单批走 runVersionedWrite：纯目录上报无投影 diff → 不 bump、不广播；若上报改了
// "已被授权权限点"的 resource/action 致投影变化 → 照常 bump+广播（一致性要求）。
// 命中 manual 行的条目被跳过、计入 Skipped，绝不覆盖人工配置。
func (m *PolicyManager) ReportPermissions(ctx context.Context, appID int64, points []cp.PermissionPoint) (cp.ReportResult, error) {
	var res cp.ReportResult
	ctx = cp.WithOperator(ctx, "auto-report") // bump 路径的 audit actor
	_, err := m.runVersionedWrite(ctx, appID, writeOp{
		action:     "report_permissions",
		entityType: "permission",
		entityID:   "",
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			for _, p := range points {
				applied, e := store.UpsertAutoPermission(ctx, tx, appID,
					p.Code, p.Resource, p.Action, p.Type, p.Name, p.Description)
				if e != nil {
					return nil, e
				}
				if applied {
					res.Upserted++
				} else {
					res.Skipped++
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return cp.ReportResult{}, err
	}
	return res, nil
}
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/policy/ -run TestReportPermissions -v`
预期：两个测试 PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/types.go internal/controlplane/policy/manager.go internal/controlplane/policy/manager_test.go
git commit -m "feat(cp): PolicyManager.ReportPermissions — 批量 auto 上报走 runVersionedWrite（⑤-D 任务 3）"
```
末尾加 Co-Authored-By 行。

---

### 任务 4：policysync.Server.ReportPermissions + 接线

**文件：**
- 修改：`internal/controlplane/policysync/server.go`、`internal/controlplane/app/run.go`
- 测试：`internal/controlplane/policysync/server_test.go`

- [ ] **步骤 1：编写失败的测试**（追加到 `server_test.go`，需 Docker）

```go
type stubReporter struct {
	gotAppID int64
	gotN     int
	res      cp.ReportResult
}

func (s *stubReporter) ReportPermissions(_ context.Context, appID int64, pts []cp.PermissionPoint) (cp.ReportResult, error) {
	s.gotAppID = appID
	s.gotN = len(pts)
	return s.res, nil
}

func TestReportPermissions_ResolvesAppIDAndDelegates(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	stub := &stubReporter{res: cp.ReportResult{Upserted: 2}}
	srv := policysync.NewServer(db, policysync.Config{}, stub)

	ctx := auth.WithAppID(context.Background(), dbtest.SeedAppKey) // 模拟认证后的 app_key
	resp, err := srv.ReportPermissions(ctx, &syncv1.ReportPermissionsRequest{
		Permissions: []*syncv1.PermissionPoint{
			{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
			{Code: "p.write", Resource: "order", Action: "write", Type: "api", Name: "写"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, appID, stub.gotAppID) // app_id 由凭据强制解析
	require.Equal(t, 2, stub.gotN)
	require.Equal(t, uint32(2), resp.GetUpserted())
}

func TestReportPermissions_RejectsEmptyCode(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	srv := policysync.NewServer(db, policysync.Config{}, &stubReporter{})
	ctx := auth.WithAppID(context.Background(), dbtest.SeedAppKey)
	_, err := srv.ReportPermissions(ctx, &syncv1.ReportPermissionsRequest{
		Permissions: []*syncv1.PermissionPoint{{Code: "", Resource: "o", Action: "r"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestReportPermissions_Unauthenticated(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := policysync.NewServer(db, policysync.Config{}, &stubReporter{})
	_, err := srv.ReportPermissions(context.Background(), &syncv1.ReportPermissionsRequest{}) // 无 app_id
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policysync/ -run TestReportPermissions -v`
预期：编译失败（`NewServer` 签名不匹配 / `Server.ReportPermissions` 未定义）。

- [ ] **步骤 3：改 server.go**

`Server` 结构加 `reporter` 字段，`NewServer` 签名加 `reporter`，并加处理器。先加 import：`cp "github.com/nickZFZ/Sydom/internal/controlplane"`。

把 `Server` 结构改为：

```go
type Server struct {
	syncv1.UnimplementedPolicySyncServer
	db       *sql.DB
	hub      *Hub
	cfg      Config
	reporter permissionReporter
}

// permissionReporter 是 Server 对策略管理器的窄依赖（*policy.PolicyManager 满足）。
type permissionReporter interface {
	ReportPermissions(ctx context.Context, appID int64, points []cp.PermissionPoint) (cp.ReportResult, error)
}
```

`NewServer` 改为：

```go
func NewServer(db *sql.DB, cfg Config, reporter permissionReporter) *Server {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.BufSize <= 0 {
		cfg.BufSize = 256
	}
	return &Server{db: db, hub: NewHub(cfg.BufSize), cfg: cfg, reporter: reporter}
}
```

文件末尾加处理器：

```go
// ReportPermissions 接收权限点批量上报：app_id 由凭据强制解析，校验后委托 reporter。
func (s *Server) ReportPermissions(ctx context.Context, req *syncv1.ReportPermissionsRequest) (*syncv1.ReportPermissionsResponse, error) {
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing app identity")
	}
	points := make([]cp.PermissionPoint, 0, len(req.GetPermissions()))
	for _, p := range req.GetPermissions() {
		if p.GetCode() == "" || p.GetResource() == "" || p.GetAction() == "" {
			return nil, status.Error(codes.InvalidArgument, "permission code/resource/action 不可为空")
		}
		points = append(points, cp.PermissionPoint{
			Code: p.GetCode(), Resource: p.GetResource(), Action: p.GetAction(),
			Type: p.GetType(), Name: p.GetName(), Description: p.GetDescription(),
		})
	}
	appID, err := store.ResolveAppIDByKey(ctx, s.db, appKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	res, err := s.reporter.ReportPermissions(ctx, appID, points)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "report permissions: %v", err)
	}
	return &syncv1.ReportPermissionsResponse{
		Upserted: uint32(res.Upserted), Skipped: uint32(res.Skipped),
	}, nil
}
```

- [ ] **步骤 4：改 controlplane run.go 接线**

`internal/controlplane/app/run.go` 第 63 行 `syncCore := policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval})` 改为传入已构造的 `mgr`：

```go
	syncCore := policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval}, mgr)
```

（`mgr := policy.NewPolicyManager(db, outbox.NewSink())` 在第 61 行已存在，作用域可用。）

- [ ] **步骤 5：运行验证通过 + 全仓编译**

运行：`go test ./internal/controlplane/policysync/ -run TestReportPermissions -v && go build ./...`
预期：三个测试 PASS、全仓编译过（run.go 接线正确）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/policysync/server.go internal/controlplane/policysync/server_test.go internal/controlplane/app/run.go
git commit -m "feat(cp): PolicySync.ReportPermissions 处理器 + 接线（⑤-D 任务 4）"
```
末尾加 Co-Authored-By 行。

---

### 任务 5：syncclient.SyncClient.ReportPermissions（中继到 CP）

**文件：**
- 修改：`internal/sidecar/syncclient/client.go`
- 测试：`internal/sidecar/syncclient/client_test.go`

- [ ] **步骤 1：编写失败的测试**（追加到 `client_test.go`）

> `client_test.go` 已有 `startFake(t, *fakeServer)` 起 bufconn `PolicySync`。`fakeServer` 需能响应 `ReportPermissions`——给它加一个可编程字段并实现该方法。若 `fakeServer` 内嵌 `syncv1.UnimplementedPolicySyncServer`，加方法即可。

```go
func TestReportPermissions_RelaysToControlPlane(t *testing.T) {
	f := &fakeServer{} // 见下：给 fakeServer 加 reportResp / gotReport 字段
	f.reportResp = &syncv1.ReportPermissionsResponse{Upserted: 2, Skipped: 1}
	c, _, _ := startFake(t, f)

	res, err := c.ReportPermissions(context.Background(), []syncclient.PermissionPoint{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.write", Resource: "order", Action: "write", Type: "api", Name: "写"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Upserted)
	require.Equal(t, 1, res.Skipped)
	require.Len(t, f.gotReport, 2)
	require.Equal(t, "p.read", f.gotReport[0].GetCode())
}
```

给 `fakeServer` 加字段与方法（在测试文件内）：

```go
// 字段加到 fakeServer 结构：
//   reportResp *syncv1.ReportPermissionsResponse
//   gotReport  []*syncv1.PermissionPoint

func (f *fakeServer) ReportPermissions(_ context.Context, req *syncv1.ReportPermissionsRequest) (*syncv1.ReportPermissionsResponse, error) {
	f.gotReport = req.GetPermissions()
	if f.reportResp != nil {
		return f.reportResp, nil
	}
	return &syncv1.ReportPermissionsResponse{}, nil
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run TestReportPermissions -v`
预期：编译失败（`syncclient.PermissionPoint`/`SyncClient.ReportPermissions` 未定义）。

- [ ] **步骤 3：实现**（追加到 `client.go`）

```go
// PermissionPoint 是上报给控制面的一条权限点目录元数据（域中性，不含 app_id）。
type PermissionPoint struct {
	Code        string
	Resource    string
	Action      string
	Type        string
	Name        string
	Description string
}

// ReportResult 是上报写入统计。
type ReportResult struct {
	Upserted int
	Skipped  int
}

// ReportPermissions 经已认证的 PolicySync 连接把权限点上报到控制面（HMAC 凭据已在连接上）。
func (c *SyncClient) ReportPermissions(ctx context.Context, points []PermissionPoint) (ReportResult, error) {
	in := &syncv1.ReportPermissionsRequest{Permissions: make([]*syncv1.PermissionPoint, len(points))}
	for i, p := range points {
		in.Permissions[i] = &syncv1.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action,
			Type: p.Type, Name: p.Name, Description: p.Description,
		}
	}
	resp, err := c.client.ReportPermissions(ctx, in)
	if err != nil {
		return ReportResult{}, err
	}
	return ReportResult{Upserted: int(resp.GetUpserted()), Skipped: int(resp.GetSkipped())}, nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestReportPermissions -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/client.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar): SyncClient.ReportPermissions — 中继上报到控制面（⑤-D 任务 5）"
```
末尾加 Co-Authored-By 行。

---

### 任务 6：authz.Server.ReportPermissions + 接线

**文件：**
- 修改：`internal/sidecar/authz/server.go`、`internal/sidecar/app/run.go`
- 测试：`internal/sidecar/authz/server_test.go`

- [ ] **步骤 1：编写失败的测试**（追加到 `server_test.go`）

```go
type stubRelay struct {
	got []syncclient.PermissionPoint
	res syncclient.ReportResult
	err error
}

func (s *stubRelay) ReportPermissions(_ context.Context, pts []syncclient.PermissionPoint) (syncclient.ReportResult, error) {
	s.got = pts
	return s.res, s.err
}

func TestServer_ReportPermissions_TranslatesAndDelegates(t *testing.T) {
	relay := &stubRelay{res: syncclient.ReportResult{Upserted: 2, Skipped: 1}}
	srv := authz.NewServer(nil, relay) // ReportPermissions 不碰 Authorizer，a 可为 nil
	resp, err := srv.ReportPermissions(context.Background(), &authv1.ReportPermissionsRequest{
		Permissions: []*authv1.PermissionPoint{
			{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
			{Code: "p.write", Resource: "order", Action: "write"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.GetUpserted())
	require.Equal(t, uint32(1), resp.GetSkipped())
	require.Len(t, relay.got, 2)
	require.Equal(t, "p.read", relay.got[0].Code)
}

func TestServer_ReportPermissions_RelayErrorPropagates(t *testing.T) {
	relay := &stubRelay{err: errors.New("cp down")}
	srv := authz.NewServer(nil, relay)
	_, err := srv.ReportPermissions(context.Background(), &authv1.ReportPermissionsRequest{
		Permissions: []*authv1.PermissionPoint{{Code: "c", Resource: "r", Action: "a"}},
	})
	require.Error(t, err) // fail-soft：错误回传，业务自定处理
}
```

> 注：`server_test.go` 需 import `"errors"`、`"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"`（若尚无）。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/authz/ -run TestServer_ReportPermissions -v`
预期：编译失败（`NewServer` 签名不匹配 / `Server.ReportPermissions` 未定义）。

- [ ] **步骤 3：改 server.go**

加 import：`"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"`。

`Server` 结构与构造函数改为带 relay：

```go
// Server 把 Authorizer 适配为 gRPC AuthService，并中继权限点上报到控制面。
type Server struct {
	authv1.UnimplementedAuthServiceServer
	a     *Authorizer
	relay PermissionRelay
}

// PermissionRelay 是 Server 对上报中继的窄依赖（*syncclient.SyncClient 满足）。
type PermissionRelay interface {
	ReportPermissions(ctx context.Context, points []syncclient.PermissionPoint) (syncclient.ReportResult, error)
}

// NewServer 包装 Authorizer 为 gRPC handler，relay 转发权限点上报。
func NewServer(a *Authorizer, relay PermissionRelay) *Server { return &Server{a: a, relay: relay} }

// NewGRPCServer 装配带 AuthService 的 grpc.Server（供 cmd 监听本地端点）。
func NewGRPCServer(a *Authorizer, relay PermissionRelay) *grpc.Server {
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a, relay))
	return g
}
```

文件末尾加处理器：

```go
// ReportPermissions 把业务进程的权限点上报译为域中性点、委托 relay 转发到控制面。
// 上报是 fail-soft 的目录元数据写入：失败返回 error 交业务自定处理，不影响鉴权。
func (s *Server) ReportPermissions(ctx context.Context, req *authv1.ReportPermissionsRequest) (*authv1.ReportPermissionsResponse, error) {
	points := make([]syncclient.PermissionPoint, len(req.GetPermissions()))
	for i, p := range req.GetPermissions() {
		points[i] = syncclient.PermissionPoint{
			Code: p.GetCode(), Resource: p.GetResource(), Action: p.GetAction(),
			Type: p.GetType(), Name: p.GetName(), Description: p.GetDescription(),
		}
	}
	res, err := s.relay.ReportPermissions(ctx, points)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "report permissions: %v", err)
	}
	return &authv1.ReportPermissionsResponse{
		Upserted: uint32(res.Upserted), Skipped: uint32(res.Skipped),
	}, nil
}
```

- [ ] **步骤 4：改 sidecar run.go 接线**

`internal/sidecar/app/run.go` 第 42 行 `authSrv := authz.NewGRPCServer(authzr)` 改为：

```go
	authSrv := authz.NewGRPCServer(authzr, syncCli)
```

（`syncCli` 在第 30 行已构造，满足 `authz.PermissionRelay`。）

- [ ] **步骤 5：运行验证通过 + 全仓编译**

运行：`go test ./internal/sidecar/authz/ -run TestServer_ReportPermissions -v && go build ./...`
预期：两测试 PASS、全仓编译过。

- [ ] **步骤 6：Commit**

```bash
git add internal/sidecar/authz/server.go internal/sidecar/authz/server_test.go internal/sidecar/app/run.go
git commit -m "feat(sidecar): authz.ReportPermissions 本地入口 + relay 接线（⑤-D 任务 6）"
```
末尾加 Co-Authored-By 行。

---

### 任务 7：SDK 权限点注册与上报

**文件：**
- 创建：`sdk/go/sydom/permission.go`、`sdk/go/sydom/permission_test.go`

- [ ] **步骤 1：编写失败的测试**

`permission_test.go`（bufconn fake AuthService；可复用 `client_test.go` 已有的 `startFake`/`fakeAuth` 模式——给其加 ReportPermissions 响应）：

```go
package sydom_test

import (
	"context"
	"sync"
	"testing"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

func TestClient_ReportPermissions(t *testing.T) {
	// startReportFake：起 bufconn，AuthService.ReportPermissions 返回固定计数并记录入参。
	got, client := startReportFake(t, &authv1.ReportPermissionsResponse{Upserted: 2, Skipped: 1})
	res, err := client.ReportPermissions(context.Background(), []sydom.Permission{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.write", Resource: "order", Action: "write"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Upserted != 2 || res.Skipped != 1 {
		t.Fatalf("got %+v", res)
	}
	if len(*got) != 2 || (*got)[0].GetCode() != "p.read" {
		t.Fatalf("server got %v", *got)
	}
}

func TestRegistry_RegisterThenReport(t *testing.T) {
	var mu sync.Mutex
	var captured []sydom.Permission
	stub := reporterFunc(func(_ context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
		mu.Lock()
		defer mu.Unlock()
		captured = ps
		return sydom.ReportResult{Upserted: len(ps)}, nil
	})

	reg := sydom.NewRegistry()
	reg.Register(sydom.Permission{Code: "p.a", Resource: "order", Action: "read"})
	reg.Register(sydom.Permission{Code: "p.b", Resource: "order", Action: "write"})
	res, err := reg.Report(context.Background(), stub)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Upserted != 2 || len(captured) != 2 {
		t.Fatalf("got res=%+v captured=%d", res, len(captured))
	}
}

// reporterFunc 把函数适配为 sydom.PermissionReporter。
type reporterFunc func(context.Context, []sydom.Permission) (sydom.ReportResult, error)

func (f reporterFunc) ReportPermissions(ctx context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
	return f(ctx, ps)
}
```

`startReportFake` 辅助（加到 `permission_test.go`，参照 `client_test.go` 的 bufconn 模式）：

```go
// startReportFake 起一个仅实现 ReportPermissions 的 bufconn AuthService，返回收到的入参指针与拨号好的 Client。
func startReportFake(t *testing.T, resp *authv1.ReportPermissionsResponse) (*[]*authv1.PermissionPoint, *sydom.Client) {
	t.Helper()
	var got []*authv1.PermissionPoint
	fake := &reportOnlyAuth{resp: resp, got: &got}
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, fake)
	lis := bufconn.Listen(1024 * 1024)
	go g.Serve(lis)
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	client, err := sydom.New("bufnet", sydom.WithConn(conn))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &got, client
}

type reportOnlyAuth struct {
	authv1.UnimplementedAuthServiceServer
	resp *authv1.ReportPermissionsResponse
	got  *[]*authv1.PermissionPoint
}

func (a *reportOnlyAuth) ReportPermissions(_ context.Context, req *authv1.ReportPermissionsRequest) (*authv1.ReportPermissionsResponse, error) {
	*a.got = req.GetPermissions()
	return a.resp, nil
}
```

> 该测试文件需 import：`net`、`google.golang.org/grpc`、`google.golang.org/grpc/credentials/insecure`、`google.golang.org/grpc/test/bufconn`。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./sdk/go/sydom/ -run 'TestClient_ReportPermissions|TestRegistry' -v`
预期：编译失败（`sydom.Permission`/`Client.ReportPermissions`/`Registry` 未定义）。

- [ ] **步骤 3：实现**（`sdk/go/sydom/permission.go`）

```go
package sydom

import (
	"context"
	"sync"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
)

// Permission 是一条要上报的权限点目录元数据（功能权限定义）。
type Permission struct {
	Code        string
	Resource    string
	Action      string
	Type        string
	Name        string
	Description string
}

// ReportResult 是一次上报的写入统计。
type ReportResult struct {
	Upserted int // 新增或刷新（source=auto）
	Skipped  int // 命中人工配置（source=manual）被保留
}

// PermissionReporter 是 Registry 对上报端的窄依赖；*Client 自动满足。
type PermissionReporter interface {
	ReportPermissions(ctx context.Context, perms []Permission) (ReportResult, error)
}

// ReportPermissions 把权限点上报到本地 Sidecar（Sidecar 中继到控制面）。
// 上报是目录元数据、非鉴权决策：失败返回 error（codes.Unavailable→ErrUnavailable），
// 业务通常记日志后继续，不应因上报失败阻塞启动（fail-soft）。
func (c *Client) ReportPermissions(ctx context.Context, perms []Permission) (ReportResult, error) {
	in := &authv1.ReportPermissionsRequest{Permissions: make([]*authv1.PermissionPoint, len(perms))}
	for i, p := range perms {
		in.Permissions[i] = &authv1.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action,
			Type: p.Type, Name: p.Name, Description: p.Description,
		}
	}
	resp, err := c.cli.ReportPermissions(ctx, in)
	if err != nil {
		return ReportResult{}, mapErr(err)
	}
	return ReportResult{Upserted: int(resp.GetUpserted()), Skipped: int(resp.GetSkipped())}, nil
}

// Registry 在进程内收集权限点，供启动时一次性上报。并发安全。
type Registry struct {
	mu    sync.Mutex
	perms []Permission
}

// NewRegistry 新建空注册表。
func NewRegistry() *Registry { return &Registry{} }

// Register 登记一条权限点（可在 init/启动期多处调用）。
func (r *Registry) Register(p Permission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perms = append(r.perms, p)
}

// Report 把已登记的权限点一次性上报。空集为 no-op，返回零值结果。
func (r *Registry) Report(ctx context.Context, reporter PermissionReporter) (ReportResult, error) {
	r.mu.Lock()
	snapshot := append([]Permission(nil), r.perms...)
	r.mu.Unlock()
	if len(snapshot) == 0 {
		return ReportResult{}, nil
	}
	return reporter.ReportPermissions(ctx, snapshot)
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./sdk/go/sydom/ -run 'TestClient_ReportPermissions|TestRegistry' -race -v`
预期：PASS（Registry 并发安全经 -race）。

- [ ] **步骤 5：Commit**

```bash
git add sdk/go/sydom/permission.go sdk/go/sydom/permission_test.go
git commit -m "feat(sdk): 权限点注册与上报 — Permission/Registry/Client.ReportPermissions（⑤-D 任务 7）"
```
末尾加 Co-Authored-By 行。

---

### 任务 8：真链路 E2E（SDK→Sidecar→控制面贯通）

**文件：**
- 测试：`sdk/go/sydom/report_e2e_test.go`（新建，需 Docker）

- [ ] **步骤 1：编写 E2E 测试**

> 组合既有测试基建：CP 用 testcontainers + 认证 PolicySync（参照 `internal/controlplane/policysync/server_test.go` 的 `res.EncryptSecret`+`UPDATE app_secret_enc`+`policysync.NewGRPCServer(srv, res)` 装配，srv 传真 `policy.NewPolicyManager(db,nil)`）；Sidecar 用真 `syncclient.New`（bufconn 拨 CP）+ 真 `authz.NewGRPCServer(authzr, syncCli)`（bufconn 本地）；SDK 用 `sydom.New(WithConn 本地 authz bufconn)`。断言落库 source='auto' + 计数。

```go
func TestReportPermissions_EndToEnd(t *testing.T) {
	ctx := context.Background()
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// CP：装 app 凭据
	plain := []byte("e2e-secret-0123456789")
	res, err := secret.NewResolver(db, masterKeyForTest()) // 见下：32 字节 master key
	require.NoError(t, err)
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	mgr := policy.NewPolicyManager(db, nil)
	cpSrv := policysync.NewGRPCServer(policysync.NewServer(db, policysync.Config{}, mgr), res)
	cpLis := bufconn.Listen(1024 * 1024)
	go cpSrv.Serve(cpLis)
	t.Cleanup(cpSrv.Stop)

	// Sidecar：syncCli 经 bufconn 拨 CP（HMAC 凭据），authz 本地 bufconn
	engine, err := kernel.New("dom-e2e", nil, dataperm.NewTable())
	require.NoError(t, err)
	syncCli, err := syncclient.New(syncclient.Config{
		Endpoint: "bufnet", AppID: dbtest.SeedAppKey, Secret: plain, Secure: false,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return cpLis.Dial() })},
	}, engine)
	require.NoError(t, err)
	t.Cleanup(func() { syncCli.Close() })
	authzr := authz.New(engine, dataperm.NewFilter(engine, dataperm.NewTable()), syncCli, authz.Config{})
	sideSrv := authz.NewGRPCServer(authzr, syncCli)
	sideLis := bufconn.Listen(1024 * 1024)
	go sideSrv.Serve(sideLis)
	t.Cleanup(sideSrv.Stop)

	// SDK → 本地 Sidecar
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return sideLis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	client, err := sydom.New("bufnet", sydom.WithConn(conn))
	require.NoError(t, err)

	// 预置一条 manual，验证 auto 不覆盖
	_, err = db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		VALUES ($1,'p.manual','order','write','api','人工','manual')`, appID)
	require.NoError(t, err)

	out, err := client.ReportPermissions(ctx, []sydom.Permission{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.manual", Resource: "x", Action: "x", Type: "x", Name: "篡改"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, out.Upserted)
	require.Equal(t, 1, out.Skipped)

	// 落库核验：p.read 是 auto；p.manual 仍 manual 未被篡改
	var src, name string
	require.NoError(t, db.QueryRow(`SELECT source FROM permission WHERE app_id=$1 AND code='p.read'`, appID).Scan(&src))
	require.Equal(t, "auto", src)
	require.NoError(t, db.QueryRow(`SELECT source, name FROM permission WHERE app_id=$1 AND code='p.manual'`, appID).Scan(&src, &name))
	require.Equal(t, "manual", src)
	require.Equal(t, "人工", name)
}
```

> `masterKeyForTest()` 返回 32 字节 master key——参照 `policysync/server_test.go` 中构造 `secret.NewResolver` 用的同款 key 来源（如该测试用 `bytes.Repeat([]byte("k"), 32)` 之类，照搬其常量/构造方式，确保 `EncryptSecret` 与 `policysync.NewGRPCServer` 解析一致）。import 需含 dbtest、secret、policy、policysync、kernel、dataperm、syncclient、authz、grpc、bufconn、insecure、net。

- [ ] **步骤 2：运行**

运行：`go test ./sdk/go/sydom/ -run TestReportPermissions_EndToEnd -v`
预期：PASS——SDK 上报经 Sidecar 中继落库控制面，source='auto'，manual 不被覆盖，计数 1/1。

- [ ] **步骤 3：gofmt + Commit**

```bash
gofmt -w sdk/go/sydom/report_e2e_test.go
git add sdk/go/sydom/report_e2e_test.go
git commit -m "test(sdk): 权限点上报真链路 E2E（SDK→Sidecar→控制面贯通）（⑤-D 任务 8）"
```
末尾加 Co-Authored-By 行。

---

### 任务 9：全量验证 + 收尾

**文件：** 无（仅验证）

- [ ] **步骤 1：gofmt + vet + build**

运行：`gofmt -l sdk/ internal/ && go vet ./... && go build ./...`
预期：gofmt 无输出、vet 与 build 通过。

- [ ] **步骤 2：proto 漂移**

运行：`make proto-check`
预期：无差异。

- [ ] **步骤 3：相关包测试（含竞态；需 Docker）**

运行：
```bash
go test ./sdk/... -race
go test ./internal/controlplane/store/ ./internal/controlplane/policy/ ./internal/controlplane/policysync/ -count=1
go test ./internal/sidecar/syncclient/ ./internal/sidecar/authz/ -race
```
预期：全 ok。（全量 `go test ./...` 需 Docker 起 PG/Redis，按既有惯例跑受影响包即可。）

- [ ] **步骤 4：收尾**

进入 finishing-a-development-branch 流程（验证 → 选项 → 合并/PR）。

---

## 自检

**1. 规格覆盖度**（对照 `2026-06-06-sydom-sdk-d-permission-reporting-design.md`）：
- §4.1 proto（sync.v1 + auth.v1 各 +RPC+3 message）→ 任务 1 ✓
- §4.2 store.UpsertAutoPermission（auto 不覆盖 manual）→ 任务 2 ✓
- §4.2 cp.PermissionPoint/ReportResult + policy.ReportPermissions（runVersionedWrite 复用）→ 任务 3 ✓
- §4.2 policysync.Server.ReportPermissions（app_id 凭据强制 + 校验 + 委托）+ 控制面 run.go 接线 → 任务 4 ✓
- §4.3 syncclient.ReportPermissions（中继）→ 任务 5 ✓
- §4.3 authz.ReportPermissions（本地入口 + relay）+ sidecar run.go 接线 → 任务 6 ✓
- §4.4 SDK Permission/ReportResult/Client.ReportPermissions/Registry/PermissionReporter → 任务 7 ✓
- §6 测试策略（store/policy/中继/SDK/E2E）→ 任务 2/3/5/7 + 任务 8 E2E ✓
- §2 D4 auto 不覆盖 manual → 任务 2/3/8；D5 no-bump → 任务 3；D6 fail-soft → 任务 6/7（错误回传）；D7 app_id 凭据强制 → 任务 4/8 ✓
- §7 改动面（10 文件）→ 任务 1-7 全覆盖 ✓

**2. 占位符扫描**：无 TODO/待定。两处「参照既有测试基建」（任务 8 的 master key 与 CP 认证装配、任务 5/7 的 bufconn fake 扩展）是指向同包既有 helper 的明确指引（`server_test.go`/`client_test.go` 已有完整模式），非占位——E2E 认证装配照搬既有 server_test 模式以免重复 150 行样板且避免 HMAC 装配漂移。

**3. 类型一致性**：跨层类型链一致——`sydom.Permission`/`syncclient.PermissionPoint`/`cp.PermissionPoint` 字段同名（Code/Resource/Action/Type/Name/Description）；`ReportResult{Upserted,Skipped int}` 在 cp/syncclient/sydom 三处同形；proto `PermissionPoint`/`ReportPermissionsResponse{upserted,skipped uint32}` 两 package 同形；`policysync.permissionReporter`(appID int64,[]cp.PermissionPoint) 与 `policy.PolicyManager.ReportPermissions` 签名一致；`authz.PermissionRelay`([]syncclient.PermissionPoint) 与 `syncclient.SyncClient.ReportPermissions` 一致；`sydom.PermissionReporter`([]Permission) 与 `*Client.ReportPermissions` 一致；`NewServer`/`NewGRPCServer` 改签名处（policysync 任务4、authz 任务6）对应 run.go 接线（任务4、任务6）同步更新。
