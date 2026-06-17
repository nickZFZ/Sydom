# 司域 (Sydom) M2.2 设计 · 决策可解释性（为什么 allow / deny）

> 里程碑：**M2 · 授权产品功能纵深（operate / understand）** 的第二个子项目（M2.2）。
> M2 拆 4 子项目：M2.1 撤权对称 + 生命周期补全（已交付）/ **M2.2 决策可解释性** / M2.3 审计查询 + 变更历史 / M2.4 List 分页·搜索·排序·过滤。本文仅覆盖 M2.2，经 brainstorm 收敛为「数据面决策 explain，复用 M1.3 瞬态求值机器」。

## 1. 背景与目标

生产就绪路线图（`d989045`）差距分析记：**explain「为什么 allow / deny」——casbin 支持 explain，AdminService 未暴露任何 explain RPC**。

M1.3 已答「user X 能做什么」（`GetEffectivePermissions`，枚举 allow 集）。但它**不解释为何某项不在集合里**——开发者排障「为什么 alice 读 order 被拒」时，只能看到「allice 不在允许集」，看不到 user→角色→授权 的判定链，也分不清「显式 deny 覆盖」与「无任何授权命中」。

**目标**：暴露**单条数据面授权决策**的解释——给定 (app, user, resource, action)，回答 allow/deny + **为什么**：判定规则、判定角色、用户有效角色链（含继承）、该 resource 的数据范围符号预览。形成「能做什么（M1.3）+ 这条为何（M2.2）」的排障闭环。

## 2. 范围与 YAGNI 边界

**纳入（M2.2 必须项）**：
- `ExplainDecision` RPC：单条数据面决策 explain。
- 三面 parity：gRPC + REST + Console（决策 c）。

**不纳入（移出 M2.2，留后续切片）**：
- **what-if 决策模拟 / 保存前预演**——路线图明确归 **M3**（决策模拟器）。M2.2 只解释**当前已落库状态**的决策。
- **控制面 admin 决策 explain**（为何 operator 能/不能执行某管理 RPC）——受众小、adminauthz 是另一套较简单 enforcer，可后续独立追加。
- **反查「谁能做 Y / 谁有权限 Z」**——独立特性，非本切片。
- 分页 / 搜索（M2.4）；审计（M2.3）。

**三处已定决策（brainstorm 收敛，记录于此供审查）**：
- **(a) 解释面 = 数据面授权决策**：解释「user U 在 app X 能否对 resource R 做 action A」——面向业务应用的「为何被拒」排障，价值最高，且 M1.3 已建可复用的控制面瞬态求值机器。否决控制面 admin explain（受众小）与「两面都做」（范围翻倍）。
- **(b) 解释深度 = 判定规则 + 角色链 + 数据范围**：allow/deny + 判定 p 规则（哪条 grant、经哪个角色）+ 用户有效角色（含继承）+ deny 覆盖时出覆盖规则 + 该 resource 数据策略符号谓词；默认 deny 时说明「无 grant 命中」并列出用户现有角色。否决「极简（仅 casbin 命中规则）」（默认 deny 时几乎无信息）与「全量 trace」（冗长、压垮使用者）。
- **(c) 三面 parity**：按 M1.2/M1.3/M1.4/M2.1 范式，gRPC + REST + Console；Console 出「决策解释器」页，与 M1.3「能做什么」页互补。三面共用同一 `AuthorizeRule`/`ruleTable`/`effperm` 机器，物理不可能策略漂移。

## 3. RPC 契约（`api/proto/sydom/admin/v1/admin.proto`）

新增 1 个 RPC + 请求/响应 + 2 个嵌套 message：

```proto
rpc ExplainDecision(ExplainDecisionRequest) returns (ExplainDecisionResponse);

message ExplainDecisionRequest {
  uint64 app_id   = 1;
  string user_id  = 2;
  string resource = 3;
  string action   = 4;
}
message ExplainDecisionResponse {
  bool   allowed       = 1;
  string reason        = 2;             // ALLOW_GRANTED | DENY_OVERRIDDEN | DENY_NO_MATCH
  DecidingRule deciding_rule = 3;       // 命中的判定 p 规则；DENY_NO_MATCH 时为空 message
  string deciding_role = 4;             // 携带该判定的角色码；DENY_NO_MATCH 时空
  repeated string roles = 5;            // 用户有效角色(含继承, casbin 角色码)
  DecisionDataScope data_scope = 6;     // 该 resource 数据策略符号预览
}
message DecidingRule {                  // 由 EnforceEx 的 []string=[sub,dom,obj,act,eft] 解构
  string subject  = 1;                  // = 判定角色码(或 user)
  string resource = 2;
  string action   = 3;
  string effect   = 4;                  // allow | deny
}
message DecisionDataScope {
  string match     = 1;                 // all | none | conditional
  string predicate = 2;                 // 仅 conditional 非空(符号谓词，$user.xxx 保留)
}
```

- 响应字段命名独立、不复用 M1.3 的 `GetEffectivePermissionsResponse`（语义不同：那是枚举集、这是单决策）。
- buf lint：`DecidingRule`/`DecisionDataScope` 为本 RPC 专用嵌套类型；`buf.yaml` 已 except `RPC_REQUEST_RESPONSE_UNIQUE`/`RPC_REQUEST_STANDARD_NAME`/`RPC_RESPONSE_STANDARD_NAME`，无 lint 障碍。

## 4. 组件与数据流

### 4.1 求值核心（`internal/controlplane/effperm` 扩展）
- 新增 `Explain(ctx, tx cp.DBTX, appID int64, user, resource, action string) (Explanation, error)`，镜像既有 `Compute` 的瞬态求值范式：自读 `application.domain`；`store.ReadAppRules`/`store.ReadAppDataPolicies`（与 Sidecar 快照同源）；`dataperm.NewTable()` + `kernel.New(domain, nil, table)` + `eng.ApplySnapshot(...)`。
- **建议把「建引擎 + 灌快照」抽成共享内部步**（如 `buildEngine(ctx,tx,appID) (*kernel.Engine,*dataperm.Table,domain,error)`），供 `Compute` 与 `Explain` 共用，避免两份漂移。这是 effperm 包内的纯重构，不改 `Compute` 对外签名/行为（M1.3 测试守门）。
- 求值三步：
  1. `roles, _ := eng.GetImplicitRolesForUser(user, domain)`（含继承，排序稳定）。
  2. `allowed, explain, _ := eng.EnforceEx(user, domain, resource, action)`（见 §4.2）。`explain` 是判定 p 规则 `[sub,dom,obj,act,eft]`（解构成 `DecidingRule`，`subject`=判定角色码）。reason 分类：`len(explain)>0 && eft==allow → ALLOW_GRANTED`；`len(explain)>0 && eft==deny → DENY_OVERRIDDEN`；`len(explain)==0 → DENY_NO_MATCH`。
  3. `dataperm.NewFilter(eng,table).FilterSymbolic(user, domain, resource)` → `DecisionDataScope{match,predicate}`（符号，沿用 M1.3 口径）。
- **fail-close**：任一步失败返 error，绝不返回空 `Explanation{}` 冒充「deny」。
- effperm 返回的领域结构 `Explanation`（Allowed/Reason/DecidingRule/DecidingRole/Roles/DataScope），由 mgmt handler 映射到 proto。

### 4.2 kernel（`internal/sidecar/kernel/engine.go`）
- 新增 `Engine.EnforceEx(sub, dom, obj, act string) (bool, []string, error)`，fail-close 守卫**镜像既有 `Enforce`**（`!ready → ErrNotReady`；`dom != e.domain → ErrForeignDomain`），委托 `e.ce.EnforceEx(sub,dom,obj,act)`。
- **回源核实（v3.10.0，已核）**：`CachedEnforcer`/`SyncedCachedEnforcer` 只覆写 `Enforce`、**未覆写 `EnforceEx`** → `ce.EnforceEx` 落到基类 `Enforcer.EnforceEx`，做真实求值、返回判定规则、绕过决策缓存。基类 `EnforceEx` 不走 SyncedCachedEnforcer 的 RWMutex；故注释标注「**仅供 effperm 瞬态（每请求新建、非共享）引擎调用**；production 共享 Sidecar 引擎不调用本方法」，规避并发问题。

### 4.3 鉴权（`internal/controlplane/mgmt/authz.go` ruleTable）
- 增量加 1 条（与 `GetEffectivePermissions` **同一读能力**：同 scope、同 resource 类、同受众）：

| RPC | rpcRule `{resource, action, isWrite, scope}` | 镜像 |
|---|---|---|
| `ExplainDecision` | `{"effective_permission", "read", false, scopeApp}` | `GetEffectivePermissions` |

- 域取自请求 `app_id`（scopeApp 路径，复用 `TenantDomainOf` 租户隔离）。matcher 与既有 ruleTable 条目一字未改。

### 4.4 Handler（`internal/controlplane/mgmt`）
- `ExplainDecision` handler：`scopeApp` 鉴权已由拦截器/AuthorizeRule 完成；handler 在只读路径调 `effperm.Explain(ctx, s.db, int64(r.AppId), r.UserId, r.Resource, r.Action)`，映射 `Explanation` → `ExplainDecisionResponse`。读不受 status 写拦截（isWrite=false）。

### 4.5 三面 parity（REST + Console）
- **REST**（`internal/controlplane/restgw`）：加 `GET /v1/apps/{app_id}/decision?user_id=&resource=&action=`，app_id **path 权威**，其余取 query。
- **Console**（`internal/controlplane/console`）：「决策解释器」页——表单输入 user/resource/action → 渲染 allow/deny 徽标 + reason + 判定规则 + 角色链 + 数据范围。与 M1.3「能做什么」页互补（同区导航）。降级无枚举、拒绝即 403 不泄露存在性、读页不含任何 secret。

## 5. 一致性与安全不变量（DX-1..DX-6，验收逐条核验）
- **DX-1 单一真相源**：explain 经 `effperm`+`kernel.Engine`，与 M1.3 同一求值栈；**无第二套决策逻辑**。
- **DX-2 与真实 Enforce 一致**：测试断言 `Explain(...).Allowed == eng.Enforce(user,dom,res,act)`（同输入同判定）；explain 的 allow/deny 不得与真实 Enforce 分叉。
- **DX-3 Sidecar 同源**：策略经 `store.ReadAppRules/ReadAppDataPolicies` 物化，与 Sidecar 快照同源；effperm 求值栈零漂移（kernel 改动仅新增 EnforceEx，不碰 Enforce/ApplySnapshot 路径）。
- **DX-4 符号口径忠实**：功能 allow/deny 是真实的（角色/授权落库、完全可判定）；数据行级过滤是符号（$user.xxx 不对真实行求值）。explain 忠实反映「功能决策 + 数据范围上限」，不冒充对具体数据行的判定。
- **DX-5 fail-close**：任一步失败返 error；绝不空响应冒充 deny；越域/未就绪经 kernel 守卫拒。
- **DX-6 租户隔离 / matcher / secret 零触碰**：`adminauthz` matcher 一字未改；ruleTable 仅新增 1 条；`ExplainDecision` 经 scopeApp + `TenantDomainOf` 租户隔离；explain 不读/不返任何 secret（不碰凭据列）；跨租户/跨域 explain → 403 安全矩阵守门。

## 6. 错误处理
- 未知 app / 跨租户跨域无权 → `PermissionDenied`（经 AuthorizeRule scopeApp fail-close，不泄露存在性）。
- effperm/kernel 内部失败（读库、建引擎、求值）→ `Internal`（沿用既有 `%v` 透传债；统一脱敏留 M3）。
- 默认 deny（无 grant 命中）**不是错误**：正常返回 `allowed=false, reason=DENY_NO_MATCH` + 用户角色链（帮助排障）。

## 7. 测试策略（TDD）
- **真实 Enforce parity（DX-2）**：构造角色/授权/继承，断言 `ExplainDecision.allowed` 与 `kernel.Engine.Enforce` 同输入同结果。
- **三类 reason**：allow（命中 grant，reason=ALLOW_GRANTED，deciding_rule/role 非空）；显式 deny 覆盖（建 deny 规则，reason=DENY_OVERRIDDEN，出覆盖规则）；默认 deny（无 grant，reason=DENY_NO_MATCH，deciding_rule 空、roles 仍列出）。
- **角色链含继承**：user 经角色继承获得 grant，explain.roles 含继承角色、deciding_role 为实际携权角色。
- **数据范围符号**：建该 resource 的 conditional 数据策略，explain.data_scope.match=conditional + predicate 含 `$user.xxx` 符号。
- **安全矩阵**：跨租户 / 跨域 explain → 403；响应不含任何 secret。
- **三面 parity**：REST 状态码（403 无权 / 200 有权 / path 权威覆写）；Console 表单（无会话→302、有会话→渲染、降级无枚举）。
- 兜底：`gofmt -l` / `go vet ./...` / `make proto-check`（无漂移）/ effperm·kernel·mgmt·restgw·console 包 `go test` + 全仓 `go test ./...`。

## 8. 范围边界 / 移交
- M2.2 仅数据面 current-state 单决策 explain；what-if 模拟（M3 决策模拟器）、控制面 admin explain、反查「谁能做 Y」各走独立 spec→plan→实现周期。
- 范式延续：子代理驱动 + 逐任务控制者独立验证 + 整体安全评审；跨包改签名后 `go vet ./...` 全仓兜底。
- 非阻塞观察项（沿 M1.4/M2.1 记录，留 M3）：Internal 错误 `%v` 透传统一脱敏。

## 9. 下一步
本 spec 经用户审查批准后，调用 writing-plans 创建 M2.2 实现计划（TDD 任务分解）。
