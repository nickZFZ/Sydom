# M3.4a 交互打磨基元（toast + 二次确认）— 设计

> **里程碑上下文**：M3.4（体验打磨横扫 + onboarding）拆 3 子项目——**M3.4a 交互打磨基元** / M3.4b 页面迁移横扫 + breadcrumb / M3.4c Onboarding 向导（见总览 spec `c527d51`）。本 spec 只覆盖 M3.4a。**批量多选**已从 M3.4a 移出（推迟，见 §8）。

## 1. 背景与目标

Console 当前：写动作经 `doWrite`（requireSession → CSRF → AuthorizeRule → CheckStatusWrite → invoke → PRG 303）成功后**直接跳转、无成功反馈**；破坏性动作（删角色/撤权/轮换 secret 等）**无任何二次确认**，提交即执行。M3.1 出了 `.toast`/`.dialog` 纯视觉 CSS 壳但无行为。唯一 JS 是 `datapolicy.js`（171 行，渐进增强先例）。

M3.4a 落地两个**写动作反馈与安全基元**，全程守「有无 JS 都可用」（EX-1）：
- **toast 成功反馈**：写后给出业务语言成功提示，JS 在则自消失 toast，JS 无则静态成功条。
- **二次确认**：破坏性动作执行前要确认，JS 在则模态 `dialog`，JS 无则服务端确认页。

不引入框架/构建步；新增唯一一处 `console/static/interactions.js`（无依赖原生、渐进增强、`//go:embed static/*` 自动收录）。后端 adminauthz/enforcer/sidecar 零触碰。

## 2. 范围

| 在内 | 不在内（§8） |
|---|---|
| 服务端一次性 flash + toast 自消失增强 | 批量多选（推迟） |
| 通用二次确认门 + 通用确认页 + dialog 渐进增强 | 瞬时通知中心/历史 |
| 把确认门接入破坏性动作清单（§4.3） | 非破坏性写动作加确认（YAGNI） |
| a11y（dialog 焦点陷阱/ESC/aria、flash role=status） | i18n、移动端重构 |

## 3. 架构

两基元彼此独立、各有清晰边界，共用唯一一处 `interactions.js`。

### 3.1 基元一：服务端 flash + toast

**数据流（无 JS 即完整）：**
1. `doWrite` invoke 成功后、PRG 重定向前，在 **session 写一条一次性 flash**（业务语言，如「角色已删除」）。消息由 `flashMessages` 映射（`fullMethod → 成功文案`，集中一处，缺省回退通用「操作成功」）决定。
2. 重定向目标页渲染时，layout 读 session flash → 渲染 `.toast`（含 `role="status"` 礼貌播报）→ **读后即清**（一次性，刷新不重现）。
3. `interactions.js` 把存在的 `.toast` 增强为「N 秒后淡出 + 可手动 ×关闭」；无 JS 时静态成功条常驻直到下次导航。

**边界与铁律：**
- flash 存 session（Redis），**绝不含 secret**（EX-6）。一次性 secret 页（轮换/重置）维持既有专管线，不走 flash。
- flash 是纯展示态，不影响任何授权/写语义。

### 3.2 基元二：通用二次确认

**确认门（无 JS 即完整）：**
- 破坏性动作的 `doWrite` 变体（`doWriteConfirmed`）在 CSRF 校验后、invoke 前插入**确认门**：检测 `confirmed=1`。
  - **缺失**（无 JS 首次提交，或 JS 关）→ 渲染**通用确认页** `ops_confirm.html`：展示业务语言确认问句（由 `confirmPrompts` 映射 `fullMethod → 问句`，如「确定删除角色「查看员」吗？此操作不可撤销。」）+ 一个表单（`action` = 原破坏性端点路径、把原请求所有表单值原样回填为隐藏字段、追加 `confirmed=1`、带同一 CSRF）+「确认」「取消」两按钮。
  - **存在** → 正常走完 doWrite 余下管线（AuthorizeRule → CheckStatusWrite → invoke → flash → PRG）。
- `interactions.js`：对标了 `data-confirm="问句"` 的破坏性表单加 submit 拦截 → `dialog.showModal()`（焦点陷阱、ESC 关、`aria-labelledby`/`aria-describedby`、关闭时焦点归还触发元素）→ 点「确认」即给该表单追加隐藏 `confirmed=1` 并提交到其**原 action**（跳过确认页往返）；点「取消」关闭不提交。无 JS 时表单照常提交到原 action → 服务端确认门渲染确认页。

**铁律：**
- 确认门是 **UX 安全层非授权替代**：CSRF + AuthorizeRule + CheckStatusWrite + status 闸在最终执行 POST 上**全部照常**（EX-5）。确认页与最终 POST 是同一 session 同一 CSRF。
- 确认页纯展示中转：`action` 是服务端已知的原端点（非用户可注入的任意 URL），隐藏字段经 `html/template` 自动转义，最终 POST 服务端重新 decode+校验一切——无开放重定向/注入面。
- 未知/缺映射 → 确认问句回退通用「确定执行此操作吗？」（fail-soft，绝不漏原语/技术细节，TP-8）。

### 3.3 interactions.js（唯一新增 JS）
- 无依赖原生 JS；两职责：① toast 自消失 ② `data-confirm` 表单的 dialog 拦截。
- 渐进增强：脚本不存在/报错时页面功能完整（静态 flash + 服务端确认页）。
- `//go:embed static/*` 自动收录；layout `<script>` 末尾加载（`defer`）。

## 4. 细节

### 4.1 flashMessages / confirmPrompts 映射
- 两张 `map[fullMethod]string`（成功文案 / 确认问句），与 ruleTable 同包同风格、集中一处真相源；新破坏性动作加一行。业务语言、含实体名占位时由 handler 注入实体显示名（经 bizterm/roleName，绝不裸 code/id，TP-8）。

### 4.2 session flash 存取
- Session 结构加一个瞬态 `Flash string` 字段；`setFlash` 在 PRG 前写 + persist、layout 渲染时 `takeFlash`（读+清+persist）。读后即清保证一次性。

### 4.3 加确认的破坏性动作清单
删角色 / 移除角色继承 / 撤管理员授权 / 解绑算子角色 / 轮换 app secret / 重置算子 secret / 删租户模板 / 停用 app（SetApplicationStatus→disabled）。其余非破坏性写（建角色/授权/绑定/应用模板等）不加确认。

### 4.4 错误处理
- 确认门渲染确认页失败、flash 存取失败 → 不阻塞主写路径的安全语义；flash 失败仅丢成功提示（fail-soft，不影响已成功的写）。
- 最终执行 POST 的任何授权/状态/业务错误仍走既有 `renderGRPCError`（不泄露）。

## 5. a11y
- `dialog`：原生 `<dialog>` + `showModal()` 自带焦点陷阱；补 `aria-labelledby`（问句）、确认/取消按钮可键盘达、ESC 关闭、关闭焦点归还触发元素。
- toast：`role="status"` `aria-live="polite"`（不抢焦点）；× 关闭按钮有 `aria-label`。
- 确认页：标准表单语义，可键盘操作；对比度 ≥ AA。
- axe-core 对确认页 + 含 dialog/toast 的页 0 违规。

## 6. 验收（M3.4a，逐条 PASS + 最终 opus READY）

- **IA-1 渐进增强（EX-1）**：toast 与二次确认**有无 JS 都可用**——无 JS：静态 flash 条 + 服务端确认页 POST→确认→执行；有 JS：自消失 toast + 模态确认跳过往返。Go 测试覆盖无 JS 路径，浏览器走查覆盖 JS 增强。
- **IA-2 后端零触碰（EX-3）**：`git diff -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/` = 0 行；M1.1 matcher 一字未改；无新 proto/RPC/ruleTable。
- **IA-3 写动作安全（EX-5）**：确认门后 CSRF+AuthorizeRule+CheckStatusWrite+status 闸全保留；确认页与最终 POST 同 session 同 CSRF；确认非授权替代。Go 测试：破坏性动作无 `confirmed` → 渲确认页不执行；带 `confirmed=1` → 执行 + flash + PRG。
- **IA-4 secret 不泄露（EX-6）**：flash 绝不含 secret；session 无 secret；一次性 secret 页不变。
- **IA-5 无原语（EX-7）**：flash 文案 / 确认问句全业务语言，实体名经 bizterm/roleName，缺映射回退通用语不漏 code/id/技术细节。
- **IA-6 a11y（EX-4）**：dialog 焦点陷阱+ESC+aria、toast role=status、确认页可键盘达；axe-core 0 违规。
- **IA-7 零构建（EX-8）**：唯一新增 `interactions.js` 无依赖原生、渐进增强、`//go:embed`；无框架/打包器；CSS 仍 token 化（仅可能微调 `.toast`/`.dialog` 视觉，组件类不破坏既有页）。
- **IA-8 一次性**：flash 读后即清，刷新不重现。

## 7. 测试策略
- **Go（无 JS 路径，testcontainers + httptest）**：每个破坏性动作 `doWriteConfirmed` 无 confirmed→确认页（200 含业务问句、不执行、行数/版本不变）；带 confirmed=1→执行 + 重定向目标含 flash 文案。flash 一次性（二次 GET 不再现）。CSRF/AuthorizeRule 在确认门后仍生效（无凭据/跨租户→拒）。
- **浏览器走查（Playwright，JS 增强）**：toast 自消失 + ×关闭；`data-confirm` 表单弹 dialog、确认提交/取消不提交、ESC 关、焦点陷阱；有无 JS 基线对照。
- **回归**：既有 console 全包测试不回归；axe-core 0 违规。

## 8. 不做（YAGNI / 推后）

- **批量多选**：从 M3.4a 移出——属列表批量操作另一类关注点、风险更高（逐条调既有写 RPC）。真有页面需要时单独薄片或折进 M3.4b。
- **非破坏性写加确认**：YAGNI。
- **通知中心 / flash 历史 / 多条堆叠 toast**：单条一次性 flash 够用。
- **i18n / 移动端**：见总览 §7。

相关：[[feedback-consistency-over-simplicity]]、[[project-detailed-design-progress]]
