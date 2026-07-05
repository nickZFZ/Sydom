# M4.2 批量操作（Bulk Operations）整体核验记录

> 里程碑：M4.2（技术向建模台 + 开发者 DX 之批量操作）。分支 `worktree-feat+m4-2-bulk-operations`，BASE=main `f84d452`。
> 实现范式：子代理驱动 + 两阶段审查（规格合规 → 代码质量），每任务 TDD、独立 commit，禁用 `--amend`。

## 交付概要

为 5 个 app 域「移除族」操作加**原子批量变体**（勾选即操作、source-agnostic、全原子+幂等即 no-op），三面 parity（gRPC + REST + Console）。方案 A：每操作一个 batch RPC，复用其单数兄弟的 `ruleTable` 规则（授权同构）；每 batch = 一个 `runVersionedWrite`（数据策略走 `runVersionedWriteData`），mutate 内一条 set-based `DELETE … = ANY(...)` / pair 用 `unnest`，一次 reproject+Diff、一次 version bump、一次 outbox 广播。

5 个操作：`BatchUnbindUserRole` / `BatchRevokePermission` / `BatchRemoveRoleInheritance` / `BatchDeleteRole` / `BatchDeleteDataPolicy`。

## 提交序列（BASE `f84d452` 之后）

| commit | 层 | 摘要 |
|---|---|---|
| `466ca8b` | proto | 5 RPC + 3 Ref + 5 Request + BatchWriteResponse |
| `ddca745` | proto | service 内小节注释破折号统一（质量审查收敛） |
| `8d18156` | store | set-based 5 批量删除助手（级联/pair/RETURNING，返回 applied） |
| `6e15d42` | policy | PolicyManager 5 批量方法（各一 versioned write；data 走 data 变体） |
| `f9f7e1e` | policy | BC-1 原子回滚有齿（注入触发器致中途失败，级联+version 均回滚，反向验证） |
| `2fad53e` | store | 补 DeleteRolesBatch 的 role_inheritance 级联覆盖（FK 无级联删故自带齿） |
| `bd2e9c0` | mgmt | 5 handler + ruleTable +5（复用单数规则）+ 空/超限 InvalidArgument + 跨租户矩阵 |
| `d2a8efb` | mgmt | batchResp 形参对齐 BatchWriteResponse 字段顺序（防同型 int 传反） |
| `761d478` | restgw | 5 批量删除路由（POST …/batch-delete，app_id path 权威，复用 AuthorizeRule） |
| `7ba3765` | console | 5 列表页多选 + 批量移除 handler（requireConfirm 二次确认+PRG，无新 JS） |
| `88f7c23` | console | 补 5 批量 confirmPrompts 业务文案（无 JS 确认页与模板 data-confirm 逐字一致） |
| `80bbf58` | console | **修复**：批量解绑 user_id 含冒号被静默丢弃（权限残留）→ 末冒号切分 + 有齿回归测试 |
| `0b5ffbe` | console | datapolicies aria-label 加 subject 判别防重复 + bindings 补复合 id 确认门断言 + 解析 helper 表驱动单测 |

## BC-1..8 逐条核验（对照 spec §4）

| BC | 结论 | 依据 |
|---|---|---|
| **BC-1 全原子** | ✅ | 每批量方法只一次 `runVersionedWrite`/`runVersionedWriteData`（单事务 + `defer tx.Rollback()`），无循环逐项调用。`TestBatchDeleteRole_AtomicRollback_Injected`：测试专属 BEFORE DELETE 触发器让删哨兵角色抛异常 → 整批回滚，含批次内已先执行的 r1 级联子行（授权/绑定）均复原，version 未 bump。**反向验证**：临时把级联改到 `m.db`（autocommit=非原子）→ 测试 FAIL（子行未复原）；恢复后 PASS。 |
| **BC-2 no-op + data 例外** | ✅ | 4 个 casbin 批量：全 no-op → `runVersionedWrite` diff 空分支 → 不 bump、`changed=false`（`Test*_AllNoOp_NoBump`）。`BatchDeleteDataPolicy` 走 `runVersionedWriteData`（无 diff 判定，非空即 bump）：`TestBatchDeleteDataPolicy_AllNonExistent_StillBumps`（全不存在 id 仍 bump）+ `_EmptyInput_NoOp`（空切片显式短路不 bump）。`applied` = `RETURNING id` 实删数。 |
| **BC-3 无冲突级联** | ✅ | 5 个纯 DELETE，无 FK 拒绝/FailedPrecondition。`DeleteRolesBatch` 级联（role_permission→role_inheritance(parent∨child)→user_role_binding→role）与单数 `DeleteRole` 逐语句一致；`TestDeleteRolesBatch_CascadesInheritance` 覆盖作父/作子双分支 + 存活边不受影响（role_inheritance FK 无 ON DELETE CASCADE，故 require.NoError 自带齿）。错误码仅 Internal/PermissionDenied/InvalidArgument。 |
| **BC-4 租户隔离 fail-close** | ✅ | `scopeApp` + app_id path 权威 + `TenantDomainOf` fail-close。mgmt `TestBatchOps_CrossTenant_PermissionDenied`（5 方法各跨租户 → PermissionDenied，本租户 → OK 对照）。REST 未知 app → 403 fail-close，path 权威（body app_id 被 path 覆写）经**故障注入**证实有齿（删覆写行 → 测试 FAIL 403）。 |
| **BC-5 status 闸** | ✅ | 5 个 Batch ruleTable 条目均 `isWrite=true` → 拦截器统一施加 `CheckStatusWrite`；停用 app 拒绝批量写（与单数写一致）。 |
| **BC-6 source-blind** | ✅ | `store/batch.go` 的 DELETE 无任何 `source` 过滤（仅注释说明 source-blind）。`TestBatchDeleteRole_SourceBlind` / `TestBatchDeleteDataPolicy_SourceBlind`：一批混删 manual+iac 两来源都删。 |
| **BC-7 授权核心零触碰** | ✅ | `git diff f84d452..HEAD` 对 `casbin/enforcer.go`+`internal/controlplane/adminauthz/`+`internal/sidecar/`+`internal/kernel/` = **0 行**。`mgmt/authz.go` 空白归一化后（`grep '/sydom' | tr -s ' ' | sort` 新旧对比）**恰 5 条净新增（全 Batch）、0 删除/改动**，每条 resource/action/scope 与单数兄弟一致（binding/grant/inheritance/role/data_policy 全 `delete`/`scopeApp`），无新鉴权原语。M1.1 tenant matcher 一字未改。 |
| **BC-8 dedup** | ✅ | set-based `= ANY($2)` / `IN (SELECT unnest…)` 对重复项天然幂等吸收（重复 id/pair 只匹配一次，`RowsAffected` 计实删行数），无需显式去重。 |

## 补充安全审视（inline opus 评审）

- **SQL 注入面**：`batch.go` 全部 `$N` 参数化 + `pq.Array`/`unnest`，无字符串拼 SQL。
- **整型转换**：`uint32(requested/applied)` 受 `maxBatchItems=1000` 上限约束、`applied≤requested`；`uint64(d.Version)` 非负。均无溢出。
- **DoS 防护**：`maxBatchItems=1000` 防单事务/单锁窗口过长。
- **复合 id 解析边界**：`parseUserRoleRefs` 用末冒号切分——user_id 自由文本（含冒号如 `google-oauth2:…`）不被静默丢弃（否则批量报成功却漏解绑=权限残留）；已修 + 有齿回归测试 `TestParseUserRoleRefs_UserIDWithColon`（反向验证首冒号切分 FAIL）。
- **无 secret 泄露**：批量路径（BatchWriteResponse/审计 `batch:<n>`/flash/确认页回显 ids）绝不含凭据。
- **无新 JS**：Console 全 diff 无新增 `<script>`/.js；多选靠 HTML5 `form=` 关联 + 服务端 `requireConfirm` 确认页 + 既有 `interactions.js` 的 `data-confirm`。

**审查中发现并修复的真实缺陷**：批量解绑 user_id 含冒号被静默丢弃（`80bbf58`，权限一致性问题，fail-close 反例）。

## 全量验证

```
gofmt -l internal/ api/     → 空（干净）
go vet ./...                → 干净
go build ./...              → 干净
make proto-check            → buf lint + generate + git diff --exit-code gen/ 通过（零漂移）
go test ./...               → exit 0；35 ok，0 FAIL，9 no test files（含各 e2e 包）
```

触碰的 5 包（store/policy/mgmt/restgw/console）在各自任务内均以真实 testcontainers（PG 17 + Redis）全新跑绿，全量套件复用其缓存结果。

## a11y 核验

- **静态结构核验**（代码质量审查逐页确认）：5 个列表页（roles/grants/inheritances/bindings/datapolicies）每行首列复选框均带关联 `aria-label`（datapolicies 加 `subject_type + subject_id + 对 resource` 判别防重复）；表头选择列 `.visually-hidden`；保持单 `h1` + `breadcrumb`；复用 M3.1 设计系统类；批量 form 用 HTML5 `form=` 属性关联避免与既有行内单数删除 form 非法嵌套。
- **基线**：这 5 个页面在 **M3.4b 的 25 页 axe-0 走查**中已验证；M4.2 仅**增量**加复选框 + 批量 form + 提交按钮，增量 a11y 面已静态核验。
- **⏳ 真实浏览器 axe 走查（延后跟进）**：本轮因 Playwright MCP 断开、无现成 build-tag 走查脚手架、且会话上限反复，按决策**将实浏览器 axe 逐页 0 违规复核留作独立后续任务**（下个会话或工具恢复后补做，脚手架/axe 走查后删除未提交）。系统 Chrome（`/usr/bin/google-chrome`）就绪，届时直接搭脚手架驱动即可。

## 裁决

**READY**（合并就绪）。BC-1..8 逐条满足，全量套件绿，授权核心零触碰经空白归一化严格证明；唯一遗留为「真实浏览器 axe 逐页复核」按决策延后为独立跟进项（增量 a11y 面已静态核验、基线页 M3.4b 已 axe-0）。
