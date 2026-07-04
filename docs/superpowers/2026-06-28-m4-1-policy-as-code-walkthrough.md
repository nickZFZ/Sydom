# M4.1 策略即代码 — 真实浏览器 axe 走查记录（任务 9 步骤 3）

> 基准：plan `docs/superpowers/plans/2026-06-28-sydom-m4-1-policy-as-code.md`；spec `docs/superpowers/specs/2026-06-28-sydom-m4-1-policy-as-code-design.md`。BASE = `git merge-base main HEAD` = `ee5aac1`（spec/plan 提交为本轮实现基线）。
> 走查方式：一次性脚手架（build tag `walkthrough`，`internal/controlplane/console/zz_walkthrough_scaffold_test.go`，复用 M3.4c/技术债清理范式）起一套真依赖 Console——testcontainers Postgres + Redis，`EnsureRootOperator` 种 `root@sydom`，`SeedApp` 播种一个应用，并**预置一个 `source='manual'` 权限点 `doc.read`**（与导入文档同 code → 触发 ADOPT，与新角色 `reader` 的 CREATE 同屏，验 PC-3 来源感知收敛在真实渲染层成立）；会话 TTL 调长 `time.Hour` 防走查中途过期；URL/appID 经 `os.WriteFile` 写文件传递（非 buffered stdout）。系统 **Chrome via Playwright MCP**（真浏览器）驱动；注入本地静态服务（`python3 -m http.server 8123`）上的 **axe-core 4.10.2** 跑 a11y。脚手架、axe 静态服务均一次性，走查后已删除/停止、未提交（`.playwright-mcp/` 已 gitignore）。
> 走查口径：root 同源 `fetch('/login')`（POST principal/secret，HttpOnly 会话 cookie 入浏览器上下文）→ `page.goto` 真实渲染策略即代码页 → 页内 `<script src>` 注入 axe-core 后 `axe.run(document,{resultTypes:['violations']})` 收 violations；再在浏览器内 `textarea[name=content]` 填入最小合法 YAML、点「预览变更」真实 POST 到 import → 渲染 diff 预览页 → 再次注入 axe 跑。

## 1. 覆盖页（2 页面 / axe-core 4.10.2 实测）

| 页 | 路由 | axe violations | 单 h1 | breadcrumb | 关键断言（真实渲染核实） |
|---|---|---|---|---|---|
| 策略即代码（建模台 tab） | `GET /apps/1/policy-code` | **0** | ✓「策略即代码」 | ✓「建模台 · 策略即代码」 | 导出表单 `action$="/policy-code/export"` 在；导入 `textarea[name=content]` 在；`<label for=format>`/`<label for=content>` 与控件关联；**两个新模板无 `<script>`**（页内唯一脚本来自 layout 基座，非本页引入） |
| 策略即代码 · 变更预览 | `POST /apps/1/policy-code/import`（预览，dry-run） | **0** | ✓「策略即代码变更预览」 | ✓「建模台 · 策略即代码 · 变更预览」 | 计数摘要「新建 1 · 采纳 1 · 更新 0 · 删除 0 · 冲突 0」；diff 表两行（见 §2）；0 冲突 → 渲「确认应用」表单（`input[name=confirmed][value=1]`）；隐藏 `textarea[name=content][hidden]` 原文回显 193 字符（apply 提交与预览一致） |

- 每页 `document.querySelectorAll('h1').length === 1`；每页 `.breadcrumb` 在；axe-core 实测版本 `4.10.2`。

## 2. 走查涌现：PC-3 来源感知收敛在真实渲染层成立

diff 预览页表格真实渲染两行（浏览器实测）：

| 动作 | 类型 | 标识 | 说明 |
|---|---|---|---|
| `adopt` | `permission` | `doc.read` | 纳入 IaC 托管(manual→iac) |
| `create` | `role` | `reader` | 新建: 阅读者 |

- **PC-3 真实渲染核实**：预置的 `source='manual'` 权限点 `doc.read` 被判为 **adopt（manual→iac）**而非重建或忽略，新角色 `reader` 为 **create**——证「收敛只动 iac 子集 + 采纳被引用的 manual 实体、不碰其它」在真实渲染层成立。计数摘要与表格一致。
- **PC-5/PC-6 删除安全（fail-close）渲染核实**：本次文档无删除/冲突项（冲突 0），故渲「确认应用」表单；模板 `{{if gt .Conflicts 0}}` 分支（冲突>0 时隐藏确认按钮、改显「存在冲突，需先解决后再应用」）由代码评审静态核实，后端 apply 对含冲突文档亦 fail-close 拒绝（`mgmt.TestPolicyAsCode_ConflictReturnsFailedPrecondition` 有齿）——双保险。
- **PC-4 dry-run 零副作用**：预览为 dry-run，未落库（底层 `policy`/`mgmt`/`restgw` 层 dry-run 零副作用测试有齿；此处走查仅渲染预览、未点「确认应用」）。
- **content 回显安全（XSS）**：原始 YAML 经 `html/template` 转义后回显进隐藏 `textarea`，193 字符原样保真，无注入路径（评审静态核实 + 真实渲染长度一致）。

## 3. 结论（a11y 维度 + 走查纪律）

- **a11y**：策略即代码两页（`policy_code`、`policy_code_diff`）真实浏览器 axe-core 4.10.2 实测 **各 0 违规**；每页恰一个 `<h1>` + breadcrumb；表单 `label for` 与控件关联、diff 表有表头。复用 M3.1 设计系统组件类（`workspace`/`appnav`/`breadcrumb`/`table`/`btn`/`btn-primary`/`danger`），零新增硬编码色值、零新 JS。
- **结构性回归另由 Go 测试守门**：`console.TestPolicyCode_GetPage`（恰一 h1 + breadcrumb + 导出 action + import textarea）、`TestPolicyCode_ImportPreview`（dry-run 渲预览）、`TestPolicyCode_ImportConfirm_PRG`（确认 apply PRG 303 + 落库）、`TestPolicyCode_Export_Download`（Content-Disposition attachment + PC-2 无 secret）。
- **走查纪律**：① 脚手架 `time.Sleep` 阻塞 + URL 写文件传递（非 buffered stdout）；② 会话 TTL 复刻 `time.Hour` 防过期；③ 无 CSP → `page.goto` 后页内 `<script src>` 注入 axe 跑 `axe.run(document)`；④ 停后台进程按确切 PID（非 `pkill -f` 防自杀——本轮一次 `pkill -f "go test -tags walkthrough"` 误伤自身 shell，已改按 PID kill 收尾）；⑤ 脚手架 + axe 静态服务走查后即删/停、未提交。
