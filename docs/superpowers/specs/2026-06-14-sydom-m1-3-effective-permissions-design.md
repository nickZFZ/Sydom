# 司域 (Sydom) M1.3 — 人→角色 + 「某人能做什么」有效权限视图 设计

> 类型：单功能 spec（M1 多租户基座第 3 子项目）。
> 日期：2026-06-14
> 前置：M1.1 租户隔离基座（`437b28d`）、M1.2 自助账户最小集 + tenant-scoped 读（`48ecae8`）均已并入 main。
> 路线图定位：`2026-06-13-sydom-production-readiness-roadmap.md` §4 M1 «最小功能（把人分配到角色 + 「某人能做什么」有效权限视图）»。

## 1. 背景与目标

司域的 app 域 RBAC（终端用户→业务角色→功能权限 + 数据权限）已端到端可运行，但管理面**只有正向 List，零反查**：无法回答「**user X 到底能做什么**」。M1.3 补上这条最小但核心的能力，并把「把人分配到角色」与「看 TA 能做什么」串成一条薄业务旅程（运营台旅程雏形）。

**目标**：给定一个 app 内的 user，算出并展示：
1. **功能权限**——TA 经角色闭包、deny 覆盖聚合后的最终 (resource, action) 允许集；
2. **数据策略符号预览**——每个已配 resource 上 TA 会被套的行过滤谓词（`$user.xxx` 保留符号形态）；
3. **角色**——TA 的隐式角色闭包（含继承）。

**红线（继承自既有工程范式）**：一致性优先于简化。算出的「能做什么」必须**逐字节等于 Sidecar 实际会放行的**——故不另起一套决策逻辑，而是**在控制面内瞬态复用整条 Sidecar 求值栈**（`kernel.Engine` + `dataperm`），喂数据用与 Sidecar 快照**同源**的 `store.ReadAppRules` / `store.ReadAppDataPolicies`。

## 2. 范围

**纳入**：
- 新增 1 个只读 RPC `GetEffectivePermissions`（app 域、tenant-scoped）。
- 控制面瞬态求值器：从 DB 物化策略建临时 `kernel.Engine`，算功能允许集 + 角色闭包。
- `dataperm` 新增**符号渲染**路径（不解析 `$user.xxx`），与 Sidecar 现有路径行为隔离。
- 三面 full-parity：gRPC RPC + REST 路由 + Console 用户为中心页面（绑定增删复用既有 `BindUserRole`/`UnbindUserRole`）。

**不纳入（明确边界，避免范围蔓延）**：
- **per-permission「由哪个角色授予」/「为什么 allow/deny」**——属 M2 explain。
- **denied 集回传**（「为什么不能」对照）——M2 边界，本期严守最小，只回 allow 集。
- **数据策略 condition 实值求值 / what-if 代入**——已定**符号谓词**口径（系统不存用户属性、不持有客户业务数据，忠实上限是渲染谓词而非真实行）；实值预演属 M3 决策模拟器。
- **分页 / 搜索 / 排序**——M2。
- **新「person/user」一等身份目录**——YAGNI；user 仍是 app 域内的 subject 字符串，复用既有绑定语义。
- **求值结果缓存**——Beta 量级每请求瞬态构建可接受；缓存 / 常驻引擎留 M2 规模化。

## 3. 方案选型与决策

「有效权限怎么算」三选：

| | 方案 | 一致性 | 成本 | 裁决 |
|---|---|---|---|---|
| **E1** | 瞬态复用 `kernel.Engine` + `dataperm`，`store.ReadAppRules/ReadAppDataPolicies` 喂数据，请求级构建后即弃 | **逐字节等于 Sidecar**（同一引擎+model+dataperm，零决策逻辑重写） | 每请求建引擎 + 灌全 app 物化策略 | ✅ **采用** |
| E2 | 递归 SQL CTE 自算角色闭包 + deny 覆盖 + 数据策略主体匹配 | 在 SQL 里二次实现决策语义，闭包/deny/合并任一处偏离即不一致 | 轻 | ✗ 违背「一份授权真相·绝无旁路」 |
| E3 | 控制面常驻 app 域引擎（像 Sidecar 长期加载 + 订阅同步） | 高 | 重，多养一份引擎 + 缓存一致性负担 | ✗ M1.3 过度 |

**决策：E1**。与项目「一致性优先 / 一份真相」红线契合——求值器与 Sidecar 收到的快照同源（`PullSnapshot` 也正是 `store.ReadAppRules` + `store.ReadAppDataPolicies`），算出来就是 Sidecar 会放行的。

## 4. 架构与复用接缝

```
GetEffectivePermissions(app_id, user_id)
  │
  ├─ AuthorizeRule(scopeApp)            ← M1.1 既有，跨租户 403 / app 存在性 fail-close 自然兜住
  │
  └─ 求值核心（新增包 internal/controlplane/effperm）
       ├─ domain := application.domain（单一来源；构造+enforce+渲染全程同值）
       ├─ rules := store.ReadAppRules(tx, appID)          ([]cp.Rule)
       ├─ dps   := store.ReadAppDataPolicies(tx, appID)   ([]cp.DataPolicy)
       │     （与 PullSnapshot 同两函数 → 与 Sidecar 快照同源）
       ├─ table := dataperm.NewTable(); 灌 dps（cp.DataPolicy→kernel.DataPolicy）
       ├─ eng   := kernel.New(domain, nil, table)
       ├─ eng.ApplySnapshot(kernel.Snapshot{Rules: cp→kernel, DataPolicies: cp→kernel})
       ├─ filter := dataperm.NewFilter(eng, table)   （eng 满足 RoleResolver）
       │
       ├─ roles        := eng.GetImplicitRolesForUser(user, domain)
       ├─ permissions  := 候选(obj,act)=p 行去重 → eng.BatchEnforce → 收 allow 集
       └─ data_previews:= 每 distinct resource → filter.FilterSymbolic(user, domain, resource)
```

**类型转换（机械、无歧义）**：
- `cp.Rule{Ptype, V[6]string}` ≅ `kernel.Rule{Ptype, V[6]string}`：逐字段拷贝。
- `cp.DataPolicy{ID int64, SubjectType, SubjectID, Resource, Condition, Effect}` → `kernel.DataPolicy{ID uint64, …}`：ID `int64→uint64`，余直拷。
- Snapshot 的 `Version` 在瞬态求值中不参与决策，置 DB 当前版本或 1 皆可（仅令 `ready=true`）。

**复用清单（全部既有、不改语义）**：
- `internal/controlplane/store`：`ReadAppRules` / `ReadAppDataPolicies`（只读，可在只读 tx 内调，保证 rules/dps 一致快照）。
- `internal/sidecar/kernel`：`New` / `ApplySnapshot` / `Enforce` / `BatchEnforce` / `GetImplicitRolesForUser`（已接 casbin + kernel model）。
- `internal/sidecar/dataperm`：`NewTable` / `NewFilter` / 既有 `buildPlan` 的主体匹配 + allow/deny 合并。

**唯一新增到 dataperm 的能力**：符号渲染（§6）。

**分层观察（非阻塞）**：mgmt/effperm 将 import `internal/sidecar/kernel`、`internal/sidecar/dataperm`。二者本质是纯求值引擎（无状态 / pin 单域），位置偏 `sidecar/` 命名空间属历史归类。复用即本设计目的（一份真相）；是否未来抽到中性 `internal/policyeval` 留作 backlog，不在 M1.3 动，避免无关重构。

## 5. 求值核心（新增 `internal/controlplane/effperm`）

新包 `effperm` 封装瞬态求值，对 mgmt 暴露一个纯函数式入口：

```go
// Result 是一次有效权限求值结果（领域类型，proto 在 mgmt 层映射）。
type Result struct {
    Roles       []string
    Permissions []Perm          // {Resource, Action}，已 deny 覆盖
    DataViews   []DataView      // {Resource, Match, Predicate}
}

// Compute 在调用方提供的只读 tx 内，对 (appID, user) 做瞬态求值。
// 内部自读 application.domain 作为引擎单一域来源（handler 无需感知 domain）。
// 失败一律返回 error（fail-close），绝不返回空 Result 冒充「无权限」。
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error)
```

**功能允许集算法**：
1. `rules := store.ReadAppRules(tx, appID)`；候选集 = 所有 `Ptype=="p"` 行的 `(V[2], V[3])`（obj, act）去重。
2. `eng.BatchEnforce([[user, domain, obj, act], …])` 一次跑完（跑真实 matcher + `e = some(allow) && !some(deny)`，deny 覆盖天然正确）。
3. 收 `true` 的候选为 `Permissions`。
   - 选 `BatchEnforce` 而非逐条 `Enforce`：单次调用、候选集有界（Beta 量级小）；其绕缓存对瞬态引擎无影响。

**角色闭包**：`eng.GetImplicitRolesForUser(user, domain)` 直接得隐式角色（含继承）。

**就绪性**：`ApplySnapshot` 成功即 `ready=true`；空策略 app（无 p/g 行）→ 候选集空 → `Permissions` 空（合法的「无功能权限」，非错误）。

## 6. dataperm 符号渲染扩展

现 `buildPlan`（filter.go）在命中循环内即 `resolveVars`（把 `$user.xxx` 解析为 attrs 实值，缺键 `ErrMissingVar` fail-close），再 allow(OR)/deny(AND NOT) 合并。符号预览需要**合并后但变量未解析**的树。

**重构（抽公共核、零行为漂移）**：
- 抽 `selectAndMerge(user, dom, resource) (mode, rawTree)`：做「主体匹配 + 中毒 fail-close + allow/deny 合并」，**不解析变量**，产出含 `$user.xxx` 的原始合并树。
- 既有 `FilterSQL` / `FilterRaw` 改为：`selectAndMerge` → 对 rawTree 整树 `resolveVars` → 原渲染。变量在合并前逐叶解析 vs 合并后整树解析，结果等价（合并为结构操作，不触叶值）；`ErrMissingVar` 仍 fail-close（落在 resolve 步）。
- 新增 `FilterSymbolic(user, dom, resource) (SymbolicResult, error)`：`selectAndMerge` → `renderSymbolic(rawTree)`。

```go
type SymbolicResult struct {
    Match     string // all | none | conditional（复用既有 MatchAll/None/Conditional）
    Predicate string // 仅 conditional：人类可读谓词，$user.xxx 与字面量内联
}
```

- `renderSymbolic`：复用 `sqlComparator` 的算子映射，叶子输出 `field <op> value`；`value` 为 `$user.xxx` 时原样输出 `$user.xxx`，标量/数组字面量直接内联（**仅展示用，不进任何 SQL，无注入面**）。AND/OR/NOT/IN/BETWEEN/IS NULL 与 `renderSQL` 同构，仅占位符换为可读值。

**行为隔离闸**：所有既有 dataperm 测试（`FilterSQL`/`FilterRaw`/`ErrMissingVar`/中毒 fail-close）必须保持全绿，作为「Sidecar 路径零漂移」的验收门。新增 `FilterSymbolic` 专项测试覆盖符号输出 + all/none/conditional 三态 + 角色主体命中。

## 7. 新 RPC 契约

`api/proto/sydom/admin/v1/admin.proto` 新增（读面分组，与既有 List RPC 并列）：

```proto
rpc GetEffectivePermissions(GetEffectivePermissionsRequest) returns (GetEffectivePermissionsResponse);

message GetEffectivePermissionsRequest {
  uint64 app_id  = 1;
  string user_id = 2;
}
message GetEffectivePermissionsResponse {
  repeated string              roles         = 1; // 隐式角色闭包（含继承）
  repeated EffectivePermission permissions   = 2; // deny 覆盖后的功能允许集
  repeated DataPolicyPreview   data_previews = 3; // 每 resource 的符号行过滤
}
message EffectivePermission { string resource = 1; string action = 2; }
message DataPolicyPreview {
  string resource  = 1;
  string match     = 2; // all | none | conditional
  string predicate = 3; // 仅 match=conditional 非空
}
```

mgmt handler `GetEffectivePermissions`：开只读 tx → `effperm.Compute(ctx, tx, appID, userID)`（域读取在 Compute 内）→ 映射 `Result`→proto。`user_id` 为空 → `InvalidArgument`（反查必须指定主体）。

## 8. 鉴权 / 租户隔离（M1.1 matcher 一字不改）

`ruleTable` 新增一条：

```go
"/sydom.admin.v1.AdminService/GetEffectivePermissions": {"effective_permission", "read", false, scopeApp},
```

- `scopeApp` → 域取 `app_id`、tenant 包含层经 `TenantDomainOf`。
- **跨租户 403、app 不存在 fail-close（不泄露存在性差异）** 全部由既有 `AuthorizeRule` 自然兜住，与所有 app 读完全一致——M1.3 不新增任何租户判定逻辑。
- 非写读：不入 `CheckStatusWrite`（停用 app 仍可查有效权限，对齐「读不受 status 写拦截」既定口径）。

## 9. 「人→角色」业务旅程 + Console 页面

**绑定零新机制**：`BindUserRole` / `UnbindUserRole` 已存在、已 tenant-scoped（`scopeApp`）、SP3 Console 已 parity。

**Console 新增用户为中心页面**（服务端 BFF，与既有 console 同范式）：
- 入口：选 app + 输入/选 user。
- 角色区：列 user 当前直绑角色（`ListUserBindings?user_id=`），可增（`BindUserRole`）/删（`UnbindUserRole`）；角色下拉来自 `ListRoles`。
- 有效权限区：「看 TA 能做什么」→ `GetEffectivePermissions` → 渲染功能允许集（resource/action 表）+ 角色闭包 + 数据策略符号谓词（每 resource 一行：`all` 显示「全部行」、`none` 显示「无行」、`conditional` 显示谓词串）。
- 把「设为销售经理」与「看能做什么」收敛到一条页面旅程（运营台旅程雏形；完整业务语言抽象层属 M3）。
- 沿用既有 console 纪律：会话不含 secret、跨域 403、降级无枚举、CSRF。

## 10. 错误处理 / fail-close

- `effperm.Compute` 任一步失败（域读取 / store 读 / `ApplySnapshot` 越域 / 中毒数据策略 / `BatchEnforce` / `GetImplicitRolesForUser` / `FilterSymbolic`）→ 返回 error，handler 映射为 `Internal`（或越域类→既已被 AuthorizeRule 前置拦截）。**绝不**把计算失败静默降级为空 `Result`（空集会被读作「啥也不能做」，掩盖故障）。
- 中毒数据策略命中 → `dataperm` 既有 fail-close 透传（绝不静默丢 deny）。
- 空策略 app（无规则）→ 合法空结果（`roles`/`permissions`/`data_previews` 皆空），非错误。
- 错误细节回传客户端沿用既有模式（`Internal` 携 `%v`）；统一脱敏待接入可观测性（与 M1.1/M1.2 同非阻塞 TODO）。

## 11. 不变量（验收逐条核验，file:line 证据）

- **EP-1 一致性**：`effperm` 求值与 Sidecar 同源同引擎——`store.ReadAppRules/ReadAppDataPolicies` + `kernel.Engine` + `dataperm`，无第二套决策逻辑。
- **EP-2 deny 覆盖忠实**：功能允许集经真实 `BatchEnforce`（`e = some(allow) && !some(deny)`），非 SQL 重算。
- **EP-3 fail-close**：计算失败一律 error，绝不空集冒充无权限。
- **EP-4 租户隔离零旁路**：`scopeApp` + `AuthorizeRule`，跨租户 403 与既有 app 读同源；M1.1 matcher 一字未改。
- **EP-5 Sidecar 路径零漂移**：dataperm 重构后所有既有测试全绿。
- **EP-6 符号口径**：数据预览仅渲染谓词（`$user.xxx` 符号化），不代入实值、不接触客户数据，无越权信息面。
- **EP-7 secret 不泄露**：新页面 / RPC 不触 secret_enc（沿用既有读面物理隔离）。

## 12. 测试策略

- **effperm 单测**（`internal/controlplane/effperm`，dbtest 播种）：
  - 直绑角色 → 功能允许集正确；
  - 继承闭包（child 继承 parent，权限上卷）正确；
  - **deny 覆盖**：同 (obj,act) 既有 allow 又有 deny → 不在允许集；
  - 数据策略符号预览：user 主体 / 角色主体命中、all/none/conditional 三态、`$user.xxx` 符号保留；
  - fail-close：注入中毒数据策略 → error 非空集；空策略 app → 合法空结果。
- **dataperm 重构门**：既有全测试保持绿 + `FilterSymbolic` 专项。
- **鉴权矩阵**（扩 `tenant_authz_test` / `account_isolation_test` 风格）：本租户管理员可查本租户 app 的有效权限；跨租户 → 403；root 全放行；停用 app 仍可读。
- **三面 parity**：gRPC handler 测 + REST 路由测 + Console 页面测（含降级无枚举）。
- **casbin 回源核实**（实现前，记忆铁律）：`BatchEnforce`、`GetImplicitRolesForUser`（含 domain 入参语义）、effect `some(allow)&&!some(deny)` 在 v3.10.0 的确切行为——读源码核实后再断言，不凭推测。

## 13. 子项目任务分解（交 writing-plans 细化）

1. proto 新增 `GetEffectivePermissions` + 消息 + 重新生成。
2. dataperm 抽 `selectAndMerge` + `FilterSymbolic`（既有测试零漂移门）。
3. `internal/controlplane/effperm` 求值核心 + 单测（cp→kernel 转换、功能集、闭包、deny 覆盖、符号预览、fail-close）。
4. mgmt handler `GetEffectivePermissions` + `ruleTable` 条目 + 鉴权矩阵测。
5. REST 路由 + 测。
6. Console 用户为中心页面 + 测。
7. 整体安全评审（EP-1..EP-7 逐条 file:line）+ 全仓 `go test ./...` + `go vet ./...`。

## 14. 假设与未决

- **假设**：`application.domain` 是该 app 的 casbin 域字符串（projection 即取 `app.domain`）；effperm 全程以此单值构造引擎 + enforce + 渲染，**不依赖** `domain == str(app_id)`，内部自洽。
- **假设**：Beta 量级下每请求瞬态建引擎可接受；规模化缓存留 M2。
- **未决（非阻塞）**：`kernel`/`dataperm` 是否抽到中性 `internal/policyeval`——留 backlog。
