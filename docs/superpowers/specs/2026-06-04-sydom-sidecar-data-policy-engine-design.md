# Sidecar 数据权限引擎（④-2）设计

> 子项目：Sydom Sidecar ④-2 数据权限引擎（库）。前置：④-1 鉴权内核已并入 main（`internal/sidecar/kernel`）。
> 上游架构：`docs/superpowers/specs/2026-05-30-sydom-architecture-design.md` §6.3/§6.4；内核切片 spec：`docs/superpowers/specs/2026-06-03-sydom-sidecar-kernel-design.md`。

## 1. 目标与边界

**目标**：把「单 app 的行级数据权限」封装为一个纯内存、可完全单测的库 `dataperm`——维护按 app 下发的 `DataPolicy` 内存表，求值时复用内核角色图把用户展开为隐式角色集，按 allow/deny 合并命中的条件树，渲染为参数化 SQL（或原始树）供上层 `/filter` 返回。实现内核注入接口 `kernel.DataPolicyApplier`。

**非目标（明确不在本切片）**：
- `/filter`/`/check` HTTP/gRPC server、序列化、入站 API（→ ④-4）。
- gRPC 客户端 / Subscribe / proto→域翻译 / `effect` 字段的上游生产链（DB/控制面/两个 proto 串接）（→ ④-3）。
- `/filter` 结果缓存（架构 §6.4：首期不缓存，每请求现算）。
- orm 方言渲染（首期只 sql + raw；orm 的结构化 Filter 中间表示待 SDK 需求明确再补）。

**交付物**：仅库/组件层，不出 cmd/binary（与 ③、④-1 一致）。

## 2. 关键设计决定（用户逐条拍板）

1. **空匹配两层语义**：某 resource 在内存表中**一条策略都没有**（未启用行级过滤）→ 返回「无行过滤」（放行全部，资源级访问交 `/check` 把关）；该 resource **有策略但本主体无 allow 命中** → 渲染 `1=0`（deny-all，看不到任何行）。
2. **多策略合并 = allow/deny deny-overrides**：`可见 = OR(命中的 allow 条件) AND NOT OR(命中的 deny 条件)`。这是 casbin 功能权限 effect（`some(where p.eft==allow) && !some(where p.eft==deny)`，见 `kernel/model.go`）的**行级同构**——功能权限与数据权限的 allow/deny 叠加规则一致。空 allow 集 = 空 OR = 假 = `1=0`，决定 1 的第二层是该恒等元的自然落点，非特判。
3. **范围切法 2（引擎先行）**：本期实现引擎 allow/deny 合并 + 给 `kernel.DataPolicy` 加 `Effect string` 字段（唯一改动的已发代码，加法式、内核不解读仅透传，现有测试不破）。`effect` 的上游生产链（`data_policy` 加列、控制面读写、mgmt/sync 两个 proto 串接、④-3 proto→域翻译赋值）**留到 ④-3** 一并接通。`Effect` 字段在 ④-3 之前为「已定义未端到端通」，向后兼容、默认 `allow`，无不一致风险。
4. **首期方言 = sql + raw**：sql=参数化模板 `{SQL, Args}`；raw=合并后条件树（变量已解析为具体值作为数据内联），交 SDK 自渲染。orm 后补。
5. **运行时变量缺失 = fail-close 报错**：条件引用 `$user.xxx` 而请求 `userAttrs` 缺该键 → 整个 `/filter` 返错（不静默降级、不当 NULL）。
6. **算子集**：比较 `EQ NE GT GE LT LE IN NOT_IN` + 逻辑 `AND OR NOT` + 字符 `LIKE NOT_LIKE` + 空值 `IS_NULL IS_NOT_NULL` + 范围 `BETWEEN`。架构留可扩展。
7. **字段名白名单（安全非可选，不作选项）**：字段名进 SQL 文本而非参数，必须匹配 `^[A-Za-z_][A-Za-z0-9_]*$`，非法即 fail-close 报错；所有值一律走 `?` 参数。

## 3. 包结构与单元边界

新包 `internal/sidecar/dataperm`（模块 `github.com/nickZFZ/Sydom`），纯库，依赖仅 `internal/sidecar/kernel` + stdlib。

| 文件 | 职责 |
|---|---|
| `condition.go` | 条件树域模型 `Condition` + `parseCondition([]byte)`：从不透明 JSON 解析，解析时做字段名白名单/算子合法/value 元数校验 |
| `table.go` | 内存表 `Table`，**实现 `kernel.DataPolicyApplier`**；apply 时解析每条策略存「已解析/中毒」形态；按 resource 索引；`sync.RWMutex` |
| `filter.go` | `Filter`：无状态查询/渲染编排（主体展开→收集→allow/deny 拆分→合并→渲染）；持 `RoleResolver` + `*Table` |
| `render_sql.go` | 合并树 → `{SQL string, Args []any}`（参数化） |
| `render_raw.go` | 合并树 → 变量已解析的结构化树（供 SDK 自渲染） |
| `errors.go` | 哨兵错误 `ErrInvalidPolicy`/`ErrMissingVar`/`ErrUnsupportedDialect` |

每个 `*_test.go` 与被测文件同包（白盒）。**全部纯单测，无 Docker/testcontainers。**

**装配（解决 applier ↔ roleResolver 鸡生蛋）**：
```go
tbl := dataperm.NewTable()
ke, _ := kernel.New(dom, cache, tbl)   // tbl 作为 DataPolicyApplier 注入内核
flt := dataperm.NewFilter(ke, tbl)     // ke 作 RoleResolver，tbl 作策略源
res, err := flt.FilterSQL(user, dom, resource, userAttrs)
```

`RoleResolver` 为窄接口，`*kernel.Engine` 天然满足，④-2 不依赖内核具体类型：
```go
type RoleResolver interface {
    GetImplicitRolesForUser(user, dom string) ([]string, error)
}
```
`Table`=存储/applier，`Filter`=查询/渲染，互不持锁、各自单测。

## 4. 数据模型

### 4.1 Condition 节点（解析自 `kernel.DataPolicy.Condition`，结构见架构 §6.3）

- **逻辑节点**：`Op ∈ {AND, OR, NOT}` + `Children[]`（NOT 单子）。
- **叶子节点**：`Field` + `Op` + `Value`。
  - 比较 `EQ NE GT GE LT LE`，集合 `IN NOT_IN`，字符 `LIKE NOT_LIKE`，空值 `IS_NULL IS_NOT_NULL`（无 Value），范围 `BETWEEN`（Value 为 2 元数组）。
- **Value**：字面量（string/number/bool）｜数组（IN/NOT_IN/BETWEEN）｜运行时变量串 `"$user.xxx"`。

JSON 形态示例（架构 §6.3）：
```json
{ "op": "AND", "children": [
  { "field": "department", "op": "EQ", "value": "$user.department" },
  { "field": "status",     "op": "IN", "value": ["pending", "approved"] }
]}
```

**解析时校验（fail-close）**：① `field` 匹配 `^[A-Za-z_][A-Za-z0-9_]*$`；② `op` 在算子集内；③ value 元数与 op 匹配（IN/NOT_IN 要非空数组、BETWEEN 要 2 元、IS_NULL/IS_NOT_NULL 无 value、其余标量）。任一不符 → 该策略标记**中毒**（存解析错误，§5 步骤 c 处理）。

### 4.2 内核 `DataPolicy` 加 `Effect` 字段（唯一改动的已发代码）

`internal/sidecar/kernel/types.go` 的 `DataPolicy` 追加 `Effect string`：
```go
type DataPolicy struct {
    ID          uint64
    SubjectType string
    SubjectID   string
    Resource    string
    Condition   string
    Effect      string // "allow" | "deny"；空串按 "allow"（对齐 DB 默认）。内核不解读，仅透传给 applier。
}
```
`dataperm` 归一化：`""→allow`，`allow`/`deny` 合法，其它 → 中毒。内核现有测试不受影响（加字段不破已有构造）。

## 5. Filter 流水线（引擎核心）

`FilterSQL(user, dom, resource, userAttrs map[string]any) (SQLResult, error)`，`FilterRaw(...) (RawResult, error)`：

a. **tier-1 守卫**：`policies, configured := tbl.Lookup(resource)`。`configured==false`（该 resource 无任何策略）→ 返回**无行过滤**（sql：空串 + 空 args；raw：null 树）。〔决定 2.1 第一层〕

b. **主体展开**：`roles, err := roleResolver.GetImplicitRolesForUser(user, dom)`；`err≠nil`（含内核 `ErrNotReady`/`ErrForeignDomain`）→ **fail-close 透传**。`subjectSet = {(user, U)} ∪ {(role, r) | r ∈ roles}`。

c. **收集 + 拆分**：遍历该 resource 的策略，subject 命中 subjectSet 者留下；任一命中策略**中毒** → `ErrInvalidPolicy`（fail-close，**绝不静默丢**——丢 deny 会扩权）。命中集按归一化 Effect 分 `allow[]`/`deny[]`。

d. **空 allow 守卫**：`len(allow)==0` → **deny-all**（sql：`1=0` + 空 args；raw：恒假节点）。〔决定 2.1 第二层〕

e. **合并树**：`AND( OR(allow_i), NOT( OR(deny_j) ) )`；`len(deny)==0` 则省略 `NOT(...)`，退化为 `OR(allow_i)`。

f. **渲染**（解析 `$user.xxx`：从 `userAttrs` 取值，**缺键 → `ErrMissingVar`**〔决定 2.5〕；值一律 `?` 参数、字段名解析时已白名单）：
   - **sql**：递归渲染 → `(…) AND NOT (…)` 字符串 + `Args[]`。叶子映射：比较 `field <op> ?`、`IN (?,?,…)`、`BETWEEN ? AND ?`、`LIKE ?`、`IS NULL`/`IS NOT NULL`（无参）。每个逻辑子表达式加括号保精度。
   - **raw**：返回合并树（`$user.xxx` 已解析为具体值，作为数据内联非 SQL 文本），交 SDK 自渲染参数化语句。

**并发**：步骤 b（内核自锁 RWMutex）与 c（`Table.RLock`）跨子系统不共锁，中间可能被一次 apply 插入 → 角色集与策略集存在极小新鲜度交叉。各自内部一致，最坏一次请求略陈旧、下次即正；非 fail-close/正确性问题，与内核「apply 后即新鲜」模型同源，可接受。

## 6. 错误与 fail-close 矩阵

| 场景 | 行为 |
|---|---|
| 内核未就绪 / 角色展开出错 | 透传 error（fail-close，不放行） |
| 命中集含中毒策略（解析/字段/算子/Effect 非法） | `ErrInvalidPolicy`（fail-close，不静默丢） |
| `$user.xxx` 在 userAttrs 缺键 | `ErrMissingVar`（fail-close 报错） |
| resource 未配置任何策略 | 无行过滤（放行全部，交 `/check` 把关）〔刻意，决定 2.1〕 |
| 配置了但本主体无 allow 命中 | `1=0` deny-all〔决定 2.1〕 |
| 不支持的 dialect | `ErrUnsupportedDialect` |

`var _ kernel.DataPolicyApplier = (*Table)(nil)` 编译期断言。

## 7. 主体解析与内核的关系（架构 §6.3 C1）

数据权限主体模型与功能权限**共用同一份角色继承图**：`Filter` 经 `RoleResolver.GetImplicitRolesForUser` 读的就是内核 casbin 内存角色图。角色关系变更（g 段 delta）经内核 `ApplyDelta`/`ApplySnapshot` 一次 `BuildIncrementalRoleLinks` 同时影响功能权限与数据权限主体解析，天然一致、无需双写。`DataPolicy` 不进 casbin `p`/`g` 段，独立存于 `Table`，经 `kernel.DataPolicyApplier` 与功能策略**同一条 delta/snapshot** 原子下发（内核 apply 时路由 `s.DataPolicies`/`DataChanges` 给 applier）。

## 8. 测试（纯单测）

- **condition**：各算子解析 + 字段名白名单拒非法 + value 元数校验 + 变量串识别。
- **table**：`ApplySnapshot`/`ApplyChange`（add/update/remove）重建索引；中毒策略被记录；`var _ kernel.DataPolicyApplier`。
- **filter（核心，fake RoleResolver）**：
  - 未配置 → 无过滤；配了未命中 → `1=0`（决定 2.1 两层）
  - 多 allow → OR 并集；allow+deny → `OR(allow) AND NOT OR(deny)`（deny-overrides 守门）
  - 经多级角色继承命中（fake resolver 返回继承角色集）
  - `$user.xxx` 解析为 arg；缺键 → `ErrMissingVar`
  - 命中中毒策略 → `ErrInvalidPolicy`；内核未就绪 → 透传
  - sql 参数化（值全进 Args、字段在文本、注入字段被拒）；raw 树变量已解析
- **edge**：IN/BETWEEN/IS_NULL 渲染；NOT 单子；空 deny 退化为纯 OR。

## 9. 移交后续切片

- **④-3 同步客户端**：实现 `effect` 上游生产链（`data_policy` 加 `effect` 列 + migration + 数据库 spec 同步；控制面 `types.go`/`store` 读写/`mgmt`/`translate` 串接；mgmt 与 sync 两个 proto 的 DataPolicy 消息加 `effect` 字段并重生成；proto→`kernel.DataPolicy.Effect` 翻译赋值）。
- **④-4 鉴权 API**：`/filter` 调 `dataperm.Filter.FilterSQL/FilterRaw`；`/check`/`/batch-check` 调内核 `Enforce/BatchEnforce`；未就绪/出错一律 deny/返错。orm 方言、`/filter` 缓存（须复用功能权限失效铁律）在此或后续评估。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]。
