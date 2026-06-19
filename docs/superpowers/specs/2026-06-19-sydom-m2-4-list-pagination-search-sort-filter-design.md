# 司域 (Sydom) M2.4 设计 · List 分页·搜索·排序·过滤

> 里程碑：**M2 · 授权产品功能纵深（operate / understand）** 的第四个（收尾）子项目（M2.4）。
> M2 拆 4 子项目：M2.1 撤权对称（已交付）/ M2.2 决策可解释性（已交付）/ M2.3 审计查询 + 变更历史（已交付）/ **M2.4 List 分页·搜索·排序·过滤**。本文仅覆盖 M2.4，经 brainstorm 收敛为「11 个 List RPC 统一接共享分页信封 + 服务端白名单排序/搜索 + offset 分页带总数，三面 parity」。

## 1. 背景与目标

控制面有 11 个只读 List RPC（SP1 读面 + M1.2 账户层），当前**全部无分页/排序/搜索**，仅个别带单一结构化过滤（`ListGrants.role_id`、`ListDataPolicies.resource`、`ListUserBindings.user_id`、`ListApplications.tenant_id`、`ListMembers.tenant_id`），一律返回**全量**。多数表会无界增长（权限点经 SDK 自动上报、用户绑定一行一 user-role、超管视角的应用/操作员跨租户全量），全量返回在生产下是性能与可用性隐患。

**目标**：给全部 11 个 List RPC 统一加 **offset 分页（带总数）+ 子串搜索 + 白名单排序 + 结构化过滤**，三面 parity（gRPC + REST + Console），机制共享一次、各 List 高度统一接入。受众：所有使用 List 的管理/运营/接入方。

## 2. 范围与 YAGNI 边界

**纳入（M2.4 必须项）**：
- **共享分页信封** `ListPage{limit,offset,sort,order,q}` 嵌入 11 个 List 请求；每个响应加 `uint32 total`。
- **11 个 List RPC 统一接入**：offset/limit 分页 + `COUNT(*)` 总数 + 子串搜索 q（白名单字段 ILIKE）+ 白名单排序（sort/order）+ 保留并适度扩充结构化过滤。
- **三面 parity**：gRPC + REST（query 参数）+ Console（搜索框 + 可排序表头 + 分页条 + 既有过滤）。
- **向后兼容更新**：既有调用方（Console/REST/e2e）同步更新到分页（缺省 clampLimit 截断到 50 的全量调用方都补 UI 控件/断言）。

**4 处已定决策（brainstorm 收敛，记录于此供审查）**：
- **(a) 覆盖面 = 全部 11 个 List 统一**（一致性优先）：否决「仅无界子集」与「分两阶段」（都留三面行为不一致）。
- **(b) 分页机制 = offset/limit + 总数**：管理配置列表人工浏览、需任意列排序 + 页码 + 「显示 m-n / 共 total」，offset 天然契合。否决 keyset（与任意排序冲突、难出总数/页码——M2.3 审计是 append-only 高频流故用 keyset，访问模式不同）与「offset 主 + keyset 可选」（两套机制，YAGNI）。
- **(c) 缺省分页 = clampLimit(0→50，上限 200)**，与 M2.3 审计一致：limit=0→默认 50、超 200 钳到 200。防无界查询（忘设 limit 也不会拉全表）。代价：截断既有全量调用方 → 必须同步更新 Console 分页 + e2e 断言。否决 limit=0→全量（无界防护不彻底）。
- **(d) 信封 = 共享 `ListPage` 子消息 + 响应 `total`**：DRY、一致、一处改全部生效。排序/搜索白名单在**服务端逐 List 维护**（不入 proto），防 SQL 列注入。否决「每请求平铺 5 字段」（proto 冲刷大、改动散落）与「通用 ListRequest 单 RPC」（丢类型安全、大重写、YAGNI）。

**不纳入（移出 M2.4，留后续）**：
- **游标 / keyset 分页**——offset 已选；审计的 keyset 各自保留。
- **跨列全文检索引擎 / 模糊排序**——q 仅白名单字段 ILIKE 子串。
- **保存的视图 / 筛选器预设 / 导出**——独立特性。

## 3. 共享信封（`api/proto/sydom/admin/v1/admin.proto`）

```proto
message ListPage {
  uint32 limit  = 1;  // 0→默认 50，上限 200（clampLimit，与 M2.3 一致）
  uint32 offset = 2;
  string sort   = 3;  // 列名，每 List 白名单校验；空/非法 → 默认列
  string order  = 4;  // "asc" | "desc"；空/非法 → 默认（多数 id asc）
  string q      = 5;  // 子串搜索，每 List 白名单字段 ILIKE '%q%'
}
```
- 每个 List 请求加 `ListPage page = N;`（保留既有结构化过滤字段，编号顺延）。
- 每个 List 响应加 `uint32 total = N;`（同 WHERE 的 `COUNT(*)`）。
- `ListPage` 为共享类型，被 11 个请求复用；buf lint 无障碍（非 request/response 顶层消息）。

## 4. 逐 List 白名单（服务端，防注入真相源）

每个 List 在 store/mgmt 维护：可排序列集合（含默认列）、可搜索列集合。`sort` 非白名单 → 回退默认列（不报错）；`order` 非 {asc,desc} → 回退默认；`q` 空 → 不加搜索条件。

| List | 表 | 搜索 q（ILIKE 列） | 可排序列（* = 默认，order 默认 asc） | 结构化过滤（既有 + 新增） |
|---|---|---|---|---|
| ListRoles | role | code, name | id*, code, name | — |
| ListPermissions | permission | code, name, resource, action | id*, code, resource, action, source | source(manual\|auto) |
| ListGrants | role_permission | —（纯 id 行） | id*, role_id | role_id（既有） |
| ListRoleInheritances | role_inheritance | — | id* | — |
| ListUserBindings | user_role_binding | user_id | id*, user_id | user_id（既有） |
| ListDataPolicies | data_policy | resource, subject_id, description | id*, resource, effect | resource（既有）, effect |
| ListOperators | admin_operator | principal | id*, principal, status | status |
| ListAdminRoles | admin_role | code, name | id*, code | — |
| ListApplications | application | name, domain, app_key | id*, name, domain, status | tenant_id（既有）, status |
| ListMembers | tenant_membership ⋈ admin_operator | principal | operator_id*, principal, tier | tenant_id（既有）, tier |
| ListMyTenants | membership（内存切片，无 SQL） | tenant_name | tenant_id*, tenant_name | — |

> 新增结构化过滤字段（source/effect/status/tier）作为各请求的可选标量字段（默认零值=不过滤），与 `ListPage` 并存。

## 5. 组件与实现

### 5.1 store（共享分页 + 逐 List 查询改造）
- 新增共享助手 `Page` 入参结构 + `orderClause(sort, order string, allowed map[string]string, defaultCol string) string`：`allowed` 把「外部列名→真实 SQL 列名」白名单映射（双重保险，输出永远是受控标识符），返回 `ORDER BY <col> <ASC|DESC>`。limit/offset 作为 `$n` 参数。
- 每个 List 查询函数改造：接收过滤 + 搜索 q + Page，动态拼 WHERE（既有 scope/过滤 + 可选 `(col ILIKE $k OR ...)`）、`ORDER BY` + `LIMIT $ OFFSET $`；并发一个 `COUNT(*)` 同 WHERE 取 total。SQL 全参数化（除受控的 ORDER BY 列名）。
- 返回 `(rows, total, error)`。

### 5.2 mgmt（共享 resolvePage + handler 映射）
- 新增 `resolvePage(p *adminv1.ListPage, allowed map[string]string, defaultCol string) (store.Page, error)`：`clampLimit`（复用 M2.3 既有 `clampLimit`，0→50/上限 200）、校验 sort∈allowed（非法→defaultCol）、order∈{asc,desc}（非法→asc）、组装搜索 q。
- 11 个 List handler：取 `r.Page`（nil 安全→空 ListPage）+ 既有/新增结构化过滤 → store 查询 → 映射 rows + 设 `total`。纯读、不 bump。

### 5.3 鉴权（零改）
- List 的 ruleTable 条目（read scopeApp/scopeSystem/scopeTenant/scopeSelf）**已存在，零新增**；`AuthorizeRule`/matcher 一字未改。分页/搜索/排序绝不拓宽鉴权 scope（既有域 WHERE 原样，仅在其上追加分页/过滤）。

### 5.4 三面 parity
- **REST**（`internal/controlplane/restgw`）：11 个 List 路由的 decode 加 `?limit=&offset=&sort=&order=&q=`（+新增结构化过滤 query），填入 `ListPage` 与请求字段。
- **Console**（`internal/controlplane/console`）：11 个 List 页统一控件——搜索框（q，GET 表单）、可排序表头（`<a>` 切 sort/order）、分页条（上一页/下一页 + 「显示 m-n / 共 total」）、既有过滤输入保留。复用读页范式（requireSession→AuthorizeRule→直调→renderPage），纯读、降级无枚举、不含 secret。共享一个分页/排序的模板片段（`_pager.html`/helper）避免 11 处重复。

## 6. 一致性与安全不变量（LS-1..LS-7，验收逐条核验）
- **LS-1 一致**：共享 `ListPage` + `clampLimit`（同 M2.3）；11 List × 3 面统一接入，无例外。
- **LS-2 注入防护**：`sort`/`order` 仅经白名单映射为受控 SQL 标识符（绝不插入原始用户输入列名）；`q` 走参数化 `ILIKE $n`；`limit`/`offset` 钳制并作 `$n` 参数。测试：恶意 sort（`id;DROP...`）→ 回退默认列、不执行。
- **LS-3 租户隔离零旁路**：分页/搜索/过滤只在既有 scope WHERE 之上追加，绝不拓宽；`adminauthz` matcher 一字未改、`enforcer.go` diff=0、ruleTable 零改。跨租户/跨 app List 仍被既有 scope + 租户隔离拦截。
- **LS-4 total 准确**：`COUNT(*)` 与分页查询同 WHERE（scope + 结构化过滤 + 搜索 q）；测试断言总数随过滤/搜索变化、与实际行数一致。
- **LS-5 向后兼容更新**：既有 Console/REST/e2e 调用方同步更新到分页；无静默截断（截到 50 的全量调用方都补 UI 控件/断言）。
- **LS-6 读纯净**：List 纯读、不 bump、无副作用、不改 status。
- **LS-7 数据面零影响**：List 为控制面读，sidecar 零触碰（git diff sidecar = 0）。

## 7. 错误处理
- `sort`/`order` 非法 → 静默回退默认（不报错，宽容输入；非法不泄露列结构）。
- `limit`/`offset` 经 clampLimit/非负钳制（负值/超大 → 钳到合法区间）。
- 查询内部失败 → `Internal`（沿用既有 `%v` 透传债；统一脱敏留 M3）。
- 空结果（过滤/搜索无命中）**不是错误**：正常返回空列表 + `total=0`。

## 8. 测试策略（TDD）
- **分页**：插 N 行，limit/offset 取页断言行数、total 恒为 N、跨页不重不漏、limit=0→默认 50、超 200→200。
- **搜索**：q 命中子串子集、total 随 q 变；q 空→全量（受 limit）。
- **排序**：sort 各白名单列 asc/desc 顺序正确；非法 sort→默认列（不报错、不注入）。
- **LS-2 注入**：恶意 sort/order 字符串 → 回退默认、查询正常、无副作用。
- **LS-4 total**：过滤 + 搜索组合下 total == 实际匹配行数。
- **LS-3 隔离**：跨租户/跨 app List 仍 403 / 空（既有 scope 不被分页旁路）。
- **三面 parity**：REST query（limit/offset/sort/q/过滤）状态码 + JSON（total 字段）；Console（搜索框渲染、表头排序链接、分页条「m-n / 共 N」、降级无枚举）。
- **向后兼容**：更新后的既有 List 测试 / e2e 仍绿（断言改为分页语义）。
- 兜底：`gofmt -l` / `go vet ./...` / `make proto-check`（无漂移）/ store·mgmt·restgw·console 包 `go test` + 全仓 `go test ./...`。

## 9. 范围边界 / 移交
- M2.4 仅 List 的分页/搜索/排序/过滤；游标分页、全文检索引擎、保存视图、导出各走独立周期。
- 范式延续：子代理驱动 + 逐任务控制者独立验证 + 整体安全评审；跨包改请求/响应签名后 `go vet ./...` 全仓兜底。
- 非阻塞观察项（沿 M1.4/M2.1/M2.2/M2.3 记录，留 M3）：Internal 错误 `%v` 透传统一脱敏。
- **本子项目完成后 M2（授权产品功能纵深）四子项目全部落地，M2 里程碑完结。**

## 10. 下一步
本 spec 经用户审查批准后，调用 writing-plans 创建 M2.4 实现计划（TDD 任务分解，按域分组 List）。
