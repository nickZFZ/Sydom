# M4.5 开发者自助闭环：凭据总览 + 数据权限沙箱 — 设计

> 里程碑：M4.5（技术向建模台 + 开发者 DX 之尾项）。BASE=main `94a615d`（M4.4 tip）。M4 第 5 也是最后一个子项目。
> 路线图定位：M4「开发者 DX」= API 文档门户（M4.4 ✅）+ SDK quickstart（M4.4 ✅）+ **密钥管理界面 + sandbox 测试模式**（本 M4.5）。

## 1. 目标与范围

把 M4.4 的开发者文档接成**可操作的自助闭环**——两薄件，都在建模台开发者区，**复用既有内核、不造第二套决策**：

- **件① 开发者凭据总览**：开发者在 `/developer` 看到本 app 的接入凭据要素（`app_id`/`app_key`/`domain`/Sidecar 端点约定/状态），把 M4.4 quickstart 说的「凭据经环境注入」落成可复制的具体值，并给「轮换凭据」入口（复用既有流程）。**绝不渲染 secret**。
- **件② 数据权限沙箱（FilterSQL 试一试）**：开发者在专页输入 subject / resource / attrs，看到**数据面同一渲染器**产出的参数化 `WHERE` 片段 + args——所见即接入时 SDK `FilterSQL` 所得。功能权限 Check「试一试」已由既有 `/apps/{id}/decision`（M2.2 ExplainDecision）覆盖，本里程碑不重造，仅从沙箱页链接过去。

**为什么这么薄**：密钥轮换（`RotateApplicationSecret`/`ResetOperatorSecret` 一次性展示 + 二次确认）、功能权限决策解释（`/decision`）、有效权限（`/effective`）、决策模拟（`/roles/.../graph`）均已存在。M4.5 只补两处真空：**开发者视角的凭据聚合视图** 与 **数据权限（FilterSQL）的控制面预览**（此前只在数据面有）。

**非目标 / 裁剪**（YAGNI）：不做多凭据/凭据过期提醒/轮换历史；沙箱不列「命中的数据策略」（只出 sql+args，与 `SQLResult` 精确对齐，理解性诉求由既有 `/effective` 承接）；attrs 用字符串键值（预览值一律进 args，类型不改变 WHERE 形状）；不做功能 Check 沙箱（复用 `/decision`）。

## 2. 架构

两个新增**只读 RPC**（都 `scopeApp` read、三面 parity gRPC/REST/Console、fail-close、零副作用），Console 作 BFF 渲染。控制面复用既有 `effperm`（快照装配）+ `dataperm`（同一渲染器），**不引入第二套决策/渲染**——延续 effperm（M1.3）「与 Sidecar 快照同源」、M4.3 预览端点「复用 dataperm」的既定纪律。

```
浏览器表单(GET/POST) → Console handler(requireSession) → AuthorizeRule(fail-close)
  → AdminService RPC(GetApplication | PreviewDataFilter) → 复用 effperm.buildEngine + dataperm → 服务端渲染
```

## 3. RPC 契约（proto，AdminService 增两条 read）

### 3.1 `GetApplication`（供件①）
```proto
rpc GetApplication(GetApplicationRequest) returns (GetApplicationResponse);
message GetApplicationRequest { uint64 app_id = 1; }
message GetApplicationResponse { ApplicationSummary application = 1; }  // 复用既有 ApplicationSummary
```
- **复用既有 `ApplicationSummary`**（`app_id/domain/name/app_key/status/current_version`）——**该 message 本就无任何 secret 字段**，从类型层面杜绝泄露。
- ruleTable：`"/sydom.admin.v1.AdminService/GetApplication": {"application", "read", false, scopeApp}`。
- 实现：`AdminServer.GetApplication` 从 `application` 表按 `app_id` 读一行组装 `ApplicationSummary`（`app_secret_hash` 永不进 message）。

### 3.2 `PreviewDataFilter`（供件②）
```proto
rpc PreviewDataFilter(PreviewDataFilterRequest) returns (PreviewDataFilterResponse);
message PreviewDataFilterRequest {
  uint64 app_id = 1;
  string subject = 2;               // 待预览的用户/主体
  string resource = 3;              // 资源名（如 "order"）
  map<string, string> attrs = 4;    // 资源/环境属性（预览值一律进 args）
}
message PreviewDataFilterResponse {
  string sql = 1;                   // 参数化 WHERE 片段（可能为空=无行级限制）
  repeated string args = 2;         // 占位符按序对应的值（字符串化仅供展示）
}
```
- ruleTable：`"/sydom.admin.v1.AdminService/PreviewDataFilter": {"effective_permission", "read", false, scopeApp}`（与 `ExplainDecision`/`GetEffectivePermissions` 的 `effective_permission,read` 家族一致）。
- 实现（**单一真相源 / 零触碰数据面 eval**）：在 `effperm` 新增导出 `PreviewFilter(ctx, tx, appID, subject, resource, attrs) (dataperm.SQLResult, error)`：
  1. `eng, table, _, _, dom, err := buildEngine(ctx, tx, appID)`（既有，app 实时快照）
  2. `f := dataperm.NewFilter(eng, table)`（`*kernel.Engine` 已满足 `dataperm.RoleResolver`）
  3. `return f.FilterSQL(subject, dom, resource, attrs)`（**数据面同一函数**，一字不改）
  - `AdminServer.PreviewDataFilter` 薄包 `effperm.PreviewFilter`，把 `SQLResult{SQL,Args}` 装进 response（args 字符串化）。
- **零触碰硬约束**：`internal/sidecar/dataperm/`、`internal/sidecar/authz/`、`internal/kernel/` 内容一字不改（diff 证明）；仅 `effperm` 加一导出函数、`mgmt`/`restgw` 加 handler/route、`authz.go` 加两条 ruleTable 项。

## 4. Console 面（无新 JS，服务端渲染，渐进增强）

### 4.1 件① 凭据总览 — `/developer` 加一 section
- `/developer` handler 增调 `GetApplication`（AuthorizeRule fail-close），传 `ApplicationSummary` 给模板。
- 模板加「接入凭据」section：表格展示 `app_id`/`app_key`/`domain`/Sidecar 端点约定（文案 `127.0.0.1:8090`，与 quickstart 一致）/状态；一句「secret 仅轮换时一次性展示，绝不在此显示」；「轮换凭据」链接指向既有 `/apps/{id}/rotate-secret` 流程。
- **绝不渲染 secret**（模板无 secret 变量；`ApplicationSummary` 无 secret 字段）。

### 4.2 件② 数据权限沙箱 — 专页 `/apps/{app_id}/data-sandbox`
- 镜像 `/decision`（M2.2）的读页范式：`GET ?subject=&resource=&attrs=` 三者齐备时调 `PreviewDataFilter` 渲染结果，否则只渲表单。
- 表单：`subject`、`resource` 文本框；`attrs` 用 `<textarea>`「`key=value` 每行」，**服务端**解析为 map（无新 JS）。
- 结果：渲染 `sql`（空则显示「无行级限制：该主体对该资源全部可见」）+ args 列表；附「功能权限试一试 → `/decision`」链接。
- `_appnav` 加「沙箱」tab（`Tab=="datasandbox"`）；单 h1；breadcrumb「建模台 · 数据权限沙箱」。

## 5. 数据流与错误处理

- 两页均 `requireSession` 守卫；未登录 303 `/login`。
- RPC 前 `AuthorizeRule`（fail-close）：无权即 `renderGRPCError`（降级、不泄露存在性）。
- `PreviewDataFilter` 内 `FilterSQL` 可返回 `dataperm.ErrMissingVar`（策略引用了 attrs 未提供的变量）/`ErrInvalidPolicy`——映射为 `InvalidArgument`，Console 内联给出「请补充属性 X」类可读提示（预览语义：**报错而非给出误导性 SQL**，非授权 fail-close 而是「不放行错误结果」）。
- `attrs` 空、无匹配数据策略：`sql` 为空 → 展示「无行级限制」。

## 6. 安全不变量（SD-1..SD-7）

- **SD-1 secret 绝不泄露**：件① 只经 `ApplicationSummary`（无 secret 字段）；`app_secret_hash` 永不出 DB/不进任何 response/模板。测试断言渲染体 `NotContains` secret 相关字面。
- **SD-2 单一真相源 / 零触碰数据面 eval**：`PreviewFilter` 复用 `dataperm.Filter.FilterSQL`（数据面同一函数）+ `effperm.buildEngine`，无第二套渲染/决策；`dataperm`/`authz`(sidecar)/`kernel` 内容 diff=0（机器验证）。
- **SD-3 参数化**：预览 `sql` 与数据面一致——值全进 `args`，绝不进 SQL 文本（`FilterSQL` 本身保证；测试断言注入型 attr 值落 args 不落 sql）。
- **SD-4 只读 fail-close**：两 RPC `scopeApp` read、`AuthorizeRule` 守卫、无写/无 bump/无审计；拒绝走 `renderGRPCError` 降级。
- **SD-5 无新 JS**：两页纯 html/template 服务端渲染（attrs 服务端解析）；全 diff 无新增 `<script>`/.js。
- **SD-6 授权面最小改动**：`authz.go` 仅 +2 条 read ruleTable 项（`GetApplication`/`PreviewDataFilter`），`casbin`/`adminauthz`/`kernel` 决策逻辑零改（diff 证明）；三面 parity（gRPC/REST/Console 走同一 ruleTable）。
- **SD-7 a11y**：新页/新 section 真实浏览器 axe-core 4.10.2 0 违规；单 h1 + breadcrumb + 表单控件 aria-label + 结果区 `role=status`。

## 7. 测试策略

- **effperm.PreviewFilter 单测**：种子数据策略 → 指定 subject+resource+attrs → 断言 `SQLResult.SQL`+`Args` 精确取值（含 `$user.xxx` 展开、多策略合并、空策略→空 sql、ErrMissingVar）；**反向验证有齿**（改断言期望值须 FAIL）。
- **PreviewDataFilter / GetApplication RPC 测**：装配 AdminServer 直调，断言契约；GetApplication 断言 response 无 secret。
- **authz 表驱动测试**：既有 ruleTable 全覆盖测试自动纳入两条新项（scope/read 正确）。
- **REST parity 测**：`GetApplication` = `GET /v1/applications/{app_id}`（与既有 application 管理族 `/v1/applications/...` 一致）、`PreviewDataFilter` = `POST /v1/apps/{app_id}/data-filter/preview`（app 域数据操作族 `/v1/apps/{app_id}/...`）——与 gRPC 同 ruleTable、同结果。
- **Console 测**：凭据 section（含 app_key、**NotContains secret**、session 守卫）；沙箱页（表单渲染、提交出 sql+args、session 303、非法 attrs 报错）。
- **真实浏览器 axe 走查**：`/developer`（含新凭据 section）+ `/apps/1/data-sandbox` 各 0 违规 + 端到端搭 attrs → 预览 sql+args。

## 8. 任务分解（留给 writing-plans）

1. `GetApplication` RPC（proto + `AdminServer` 读 + `authz.go` ruleTable 项 + 单测；无 secret 断言）。
2. `effperm.PreviewFilter` 导出 + `PreviewDataFilter` RPC（proto + handler 薄包 + ruleTable 项 + 单测反向验证 + 零触碰 diff 核验）。
3. REST parity（两 route + parity 测）。
4. Console 件① 凭据 section（`/developer` handler 增读 + 模板 section）。
5. Console 件② 数据权限沙箱专页（route + 模板 + `_appnav` tab + 测）。
6. 整体核验 SD-1..7 + 真实浏览器 axe 走查 + 最终评审 + FF。

## 9. 自检小结

- 占位符：无 TODO；RPC 契约、ruleTable 项、复用函数、模板结构均具体。
- 一致性：两 RPC read/scopeApp 与 effperm 家族一致；沙箱页范式镜像 `/decision`；凭据视图复用 `ApplicationSummary`+既有轮换流程。
- 范围：单一实现计划可覆盖（6 任务，同 M4.4 量级）。
- 模糊性：attrs 字符串键值（预览值进 args）、applied_policies 裁剪、功能 Check 复用 `/decision` 均已明确取舍。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]
