# M3.4a 交互打磨基元（toast + 二次确认）— 有无 JS 双基线走查 / axe 记录（任务 6）

> 基准：plan `docs/superpowers/plans/2026-06-23-sydom-m3-4a-interaction-primitives.md` 任务 6；spec `docs/superpowers/specs/2026-06-23-sydom-m3-4a-interaction-primitives-design.md` §5/§7。
> 走查方式：一次性脚手架（build tag `walkthrough`）起一套真依赖 Console（testcontainers Postgres+Redis，`EnsureRootOperator` 种 `root@sydom`，`SeedApp` 种应用「订单系统」app_id=1），系统 **Google Chrome 147**（Playwright 1.61.1 `channel:"chrome"` headless）真浏览器逐步走查，注入 **axe-core 4.10.2** 跑 a11y。脚手架与驱动脚本均一次性，走查后已删除、未提交；截图为 gitignore 瞬时产物。
> 核心口径：所有交互均以 **角色页 `/apps/1/roles`** 为载体——建角色（非破坏性写 → toast）+ 删角色（破坏性写 → 二次确认）同页闭环，恰好同时覆盖两个基元。

## 1. JS 开（渐进增强：toast 自消失 + `<dialog>` 二次确认）

| 走查项 | 期望 | 实测 |
|---|---|---|
| 建角色后 toast 出现 | `[data-toast]` 可见、业务语言 | ✅ 文案「角色已创建」（+ `×` 关闭件） |
| toast 语义 | `role="status"` `aria-live="polite"` | ✅ `role=status` |
| `×` 手动关闭 | 点击即移除 | ✅ 点 `.toast-close` → DOM 移除 |
| toast 自消失 | ~4s 后淡出消失（setTimeout 4000 + .4s 过渡） | ✅ 等 5.2s 后 `[data-toast]` 计数归 0 |
| 删角色弹 `<dialog>` | 模态确认，非裸 `confirm()` | ✅ `dialog[open]` 可见 |
| dialog 语义 | `role="alertdialog"` + `aria-labelledby` 指向问句 | ✅ `role=alertdialog`、`aria-labelledby=confirm-msg` |
| dialog 问句 | 业务语言确认问句 | ✅「确定删除该业务角色吗？此操作不可撤销。」 |
| 初始焦点 | 落「确认」按钮 | ✅ `activeElement` 文本=「确认」 |
| 焦点不逸出背景 | Tab 不可达页面背景交互件 | ✅ 见 §1.1 焦点轨迹 |
| ESC 关闭不删 | ESC → dialog 移除、角色仍在 | ✅ `dialog[open]` 计数 0，角色行仍在表 |
| 焦点归还触发器 | 关闭后焦点回到「删除」按钮 | ✅ `activeElement` 文本=「删除」 |
| 确认即删除 + toast | 点「确认」→ 删除 + toast | ✅ toast「角色已删除」，角色行消失 |

### 1.1 焦点陷阱（焦点轨迹核实）

`<dialog>.showModal()` 的焦点陷阱是**原生浏览器保证**（非本项目代码实现）。逐 Tab 记录 `document.activeElement`（从「确认」起按 Tab×4）：

| Tab | activeElement | 在 dialog 内 |
|---|---|---|
| 1 | `BUTTON`「取消」 | 是 |
| 2 | `BODY`（文档根，瞬态） | 否 |
| 3 | `BUTTON`「确认」 | 是 |
| 4 | `BUTTON`「取消」 | 是 |

**判读**：循环为「确认 → 取消 →（文档 body 瞬态）→ 确认 → 取消 → …」。Chromium 原生模态在跨过最后一个可聚焦元素回绕时，`activeElement` 会瞬态落到文档 `body`（此时背景内容 `inert`），随即回绕到 dialog 首个可聚焦元素。**关键 a11y 保证成立**：轨迹中只出现「确认 / 取消 / body」，**从未出现任何背景交互件**（顶栏导航链接、建角色表单、其它行的删除按钮均不可达）——模态把焦点圈在 dialog 范围内，背景被屏蔽。最初「逐 Tab 全程严格在 dialog 元素内」的布尔判定把回绕途经的瞬态 body 误判为「逸出」；按轨迹核实，模态隔离真实成立（呼应「回源/渲染核实，不臆断」）。

## 2. JS 关（无 JS 基线：静态 flash + 服务端确认页）

浏览器上下文 `javaScriptEnabled:false`，全程不加载 `interactions.js`：

| 走查项 | 期望 | 实测 |
|---|---|---|
| 建角色后静态 flash 条 | `.toast` 静态可见（不自消失） | ✅ 文案「角色已创建」 |
| flash 一次性 | 再访问同页不再出现 | ✅ 计数归 0（读后即清） |
| 删角色 → 服务端确认页 | POST 落 `ops_confirm.html`（无 dialog） | ✅ URL 含 `/delete`、`dialog[open]` 计数 0 |
| 确认页回填 | `confirmed=1` 隐藏 + `csrf_token` 隐藏 | ✅ 各就位 |
| 确认页问句 | 业务语言 | ✅「确定删除该业务角色吗？此操作不可撤销。」 |
| 点「确认」→ 删除 + flash | confirmed=1 POST → 删除 + flash | ✅ flash「角色已删除」，角色行消失 |
| 放弃确认 → 不删 | 离开确认页不点确认 → 角色仍在 | ✅ 角色行仍在表 |

> 取消链接 `history.back()` 在 JS 关时退化为「无操作」（`javascript:` 链接不执行）；语义上「未点确认即未删除」成立——离开确认页即放弃，破坏性写未发生。spec/plan 已记此为管理台可接受的退化（plan §规格偏差③）。

## 3. axe-core 4.10.2 自动 a11y

| 走查面 | violations | critical | serious | 备注 |
|---|---|---|---|---|
| 打开态 `<dialog>` 确认框（M3.4a 新增面） | **0** | 0 | 0 | 干净 |
| 服务端确认页 `ops_confirm.html`（M3.4a 新增面） | **0** | 0 | 0 | 干净 |
| 角色页 `/apps/1/roles`（含 toast + `data-confirm` 表单） | 2 | 0 | 0 | 见下，均**非 M3.4a 引入** |

角色页 2 项 finding：
- `empty-table-header`（minor）：操作列表头 `<th></th>` 为空——`roles.html:13`，**M3.4a 未触碰**。
- `page-has-heading-one`（moderate）：该内容页用 `<h2>角色</h2>` 而非 `h1`——**M3.4a 未触碰**。

**归因核实**：`git diff b0abf86..HEAD -- roles.html` 显示 M3.4a 对该模板的**唯一**改动是把删除表单的 `onsubmit="return confirm('删除？')"` 换成 `data-confirm="..."`（line 15）；空 `<th></th>`（line 13）与 `<h2>`（line 3）一字未动。两项 finding 是 roles 内容页的既有结构债，**非 toast/dialog 引入**——M3.4a 新增的两个交互面（toast 区、dialog、确认页）axe 实测 **0 违规**。两项既有债既非 critical 亦非 serious，留待 **M3.4b 页面横扫**统一处理（M3.4b 范畴）。

## 4. 走查结论（spec §6 IA 对照）

- **IA-1（有无 JS 都可用）**：JS 开 = toast 自消失 + dialog；JS 关 = 静态 flash + 服务端确认页；两基线功能均完整闭环。✅
- **IA-3（确认门是 UX 层，不替代授权）**：确认页/dialog 仅前置一道用户确认；`confirmed=1` 最终 POST 仍过 CSRF/AuthorizeRule/CheckStatusWrite/status 闸（由 Go 集成测试守门，本走查只验交互层）。✅
- **IA-5（a11y）**：toast `role=status`/`aria-live`；dialog `role=alertdialog`/`aria-labelledby`/初始焦点/ESC/焦点归还/背景焦点隔离——逐项实测成立；M3.4a 新增面 axe 0 违规。✅
- **flash 一次性、读后即清**：JS 关下再访问 flash 不复现。✅

走查覆盖 plan 任务 6 步骤 1–3 全部断言；JS 关基线另由 Task 2/3/4 的 Go 集成测试（flash 渲染、确认门未带 `confirmed` 不执行）冗余守门。
