# M3.4a 交互打磨基元（toast + 二次确认）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给 Console 写动作加两个渐进增强基元——成功后 toast（无 JS 时静态成功条）+ 破坏性动作二次确认（无 JS 时服务端确认页，有 JS 时模态 dialog），全程「有无 JS 都可用」。

**架构：** ① 服务端一次性 flash 存 session（`doWrite` 成功后写、`renderPage` 读后即清、layout 渲 `.toast`）+ `interactions.js` 增强自消失。② 通用确认门 `requireConfirm`（破坏性 handler 首行调用，缺 `confirmed=1` 渲通用确认页 `ops_confirm.html`，有则放行）+ `interactions.js` 用 `<dialog>` 拦截跳过往返。后端 adminauthz/enforcer/sidecar/proto 零触碰；写管线（CSRF/AuthorizeRule/CheckStatusWrite/status 闸）不变。

**技术栈：** Go、html/template、Redis 会话、原生 JS（无依赖、`//go:embed`）、testcontainers、Playwright。

**spec：** `docs/superpowers/specs/2026-06-23-sydom-m3-4a-interaction-primitives-design.md`（commit `54df084`）。**BASE sha** = 本计划 commit。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/controlplane/console/session.go` | `Session.Flash` 字段 + `RedisStore.SetFlash/TakeFlash` | 1 |
| `internal/controlplane/console/session_test.go` | flash 存取 + 一次性单测 | 1 |
| `internal/controlplane/console/flash.go` | `flashMessages` 映射 + `sessionID(r)` + `setFlash`/`takeFlash` handler 助手 | 2 |
| `internal/controlplane/console/handler.go` | `doWrite` 成功后写 flash | 2 |
| `internal/controlplane/console/render.go` | `renderPage` 读 flash 注入 `data["Flash"]` | 2 |
| `internal/controlplane/console/templates/layout.html` | 渲 `.toast`（role=status）+ `<script defer interactions.js>` | 2,5 |
| `internal/controlplane/console/confirm.go` | `requireConfirm` 确认门 + `confirmPrompts` 映射 | 3 |
| `internal/controlplane/console/templates/ops_confirm.html` | 通用确认页 | 3 |
| `internal/controlplane/console/confirm_test.go` | 确认门单测 | 3 |
| `internal/controlplane/console/routes_rbac.go` / `routes_apps.go` / `routes_secret_revoke*.go` / `routes_tenant_templates.go` | 8 破坏性 handler 首行加 `requireConfirm` | 4 |
| 各破坏性动作模板 | 表单加 `data-confirm`、去旧 `onsubmit confirm` | 4 |
| `internal/controlplane/console/*_test.go` | 破坏性动作确认门集成测试 | 4 |
| `internal/controlplane/console/static/interactions.js` | toast 自消失 + dialog 确认（唯一新 JS） | 5 |
| `internal/controlplane/console/static/css/components.css` | `.toast`/`.dialog` 视觉微调（仅 token） | 5 |
| `test/e2e` 或 Playwright 走查脚本 | 有无 JS 基线走查 | 6 |

---

## 任务 1：session flash 存储

**文件：**
- 修改：`internal/controlplane/console/session.go`
- 测试：`internal/controlplane/console/session_test.go`

- [ ] **步骤 1：先写失败测试 `session_test.go`**

参照既有 session 测试（若无则新建文件 `package console`，用 `miniredis` 或既有 `newConsole` 的 redis；核对既有 session 测试如何起 redis——若 `handler_test.go` 有 `newRedis(t)`/`miniredis` 助手则复用）。

```go
func TestRedisStore_FlashOneShot(t *testing.T) {
	store := newTestStore(t) // 复用既有 redis 测试夹具；核对 handler_test.go 的 redis 起法照搬
	ctx := context.Background()
	id, _, err := store.Create(ctx, "root@sydom")
	require.NoError(t, err)

	// 初始无 flash。
	msg, err := store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "", msg)

	// 写 flash → 取到 → 再取为空（一次性）。
	require.NoError(t, store.SetFlash(ctx, id, "角色已删除"))
	msg, err = store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "角色已删除", msg)
	msg, err = store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "", msg, "flash 读后即清，一次性")

	// 会话其余字段不被 flash 操作破坏。
	sess, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "root@sydom", sess.Principal)
	require.NotEmpty(t, sess.CSRF)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestRedisStore_Flash -count=1`
预期：FAIL（`SetFlash`/`TakeFlash` 未定义）。

- [ ] **步骤 3：实现 `Session.Flash` + `SetFlash`/`TakeFlash`**

在 `session.go` 的 Session 结构加字段：

```go
type Session struct {
	Principal string `json:"principal"`
	CSRF      string `json:"csrf"`
	CreatedAt int64  `json:"created_at"`
	Flash     string `json:"flash,omitempty"` // 一次性成功提示（业务语言，绝不含 secret）
}
```

加两个方法（读-改-写，保 TTL）：

```go
// SetFlash 给已存在会话写一条一次性 flash（读-改-写，保留剩余 TTL 用 store.ttl 续期）。
func (s *RedisStore) SetFlash(ctx context.Context, id, msg string) error {
	if id == "" {
		return ErrNoSession
	}
	sess, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	sess.Flash = msg
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err()
}

// TakeFlash 取并清空 flash（一次性）。无 flash 返回 ""。
func (s *RedisStore) TakeFlash(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	sess, err := s.Get(ctx, id)
	if errors.Is(err, ErrNoSession) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sess.Flash == "" {
		return "", nil
	}
	msg := sess.Flash
	sess.Flash = ""
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err(); err != nil {
		return "", err
	}
	return msg, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run TestRedisStore_Flash -count=1`
预期：PASS。

- [ ] **步骤 5：gofmt/vet/Commit**

运行：`gofmt -l internal/controlplane/console/session.go && go vet ./internal/controlplane/console/`
```bash
git add internal/controlplane/console/session.go internal/controlplane/console/session_test.go
git commit -m "feat(console): session 一次性 flash 存储(SetFlash/TakeFlash,读后即清,绝不含 secret)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：flash 写入/读出/渲染管线

**文件：**
- 创建：`internal/controlplane/console/flash.go`
- 修改：`internal/controlplane/console/handler.go`（doWrite 末段）、`render.go`（renderPage 注入）、`templates/layout.html`（toast）
- 测试：`internal/controlplane/console/flash_test.go`

- [ ] **步骤 1：先写失败测试 `flash_test.go`**

复用 `handler_test.go`：`newConsole`、`loginAndCSRF`、`readBody`。挑一个**已存在的非破坏性 doWrite 动作**做载体（如 CreateRole，避免依赖任务 3/4）。

```go
func TestConsole_Flash_ShownOnceAfterWrite(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建角色（doWrite + flash）→ PRG 到角色列表。
	form := url.Values{"code": {"viewer"}, "name": {"查看员"}, "csrf_token": {csrf}}
	resp, err := c.PostForm(ts.URL+"/apps/"+strconv.FormatInt(appID, 10)+"/roles", form)
	require.NoError(t, err)
	body := readBody(t, resp) // 跟随 PRG 后的页
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "已创建") // flash 业务语言成功提示

	// 再访问同页 → flash 不再出现（一次性）。
	resp2, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/roles")
	require.NoError(t, err)
	body2 := readBody(t, resp2)
	require.NotContains(t, body2, "已创建", "flash 读后即清")
}
```

> 核对 `loginAndCSRF`/`newConsole`/`readBody` 真实签名（任务 6 of M3.3 已用过：`newConsole(t)→(ts,store,db)`、`loginAndCSRF(t,ts,store,principal,secret)→(client,csrf)`）。`CreateRole` 的 flashMessages 文案设为「角色已创建」——断言用「已创建」子串。

- [ ] **步骤 2：运行验证失败** → FAIL（无 flash 渲染）。

- [ ] **步骤 3：实现 `flash.go`（映射 + handler 助手）**

```go
package console

import "net/http"

// flashMessages 是 fullMethod → 成功后 flash 文案（业务语言；缺省回退通用语）。
var flashMessages = map[string]string{
	svc + "CreateRole":             "角色已创建",
	svc + "DeleteRole":             "角色已删除",
	svc + "GrantPermission":        "权限已授予",
	svc + "RevokePermission":       "权限已撤销",
	svc + "AddRoleInheritance":     "继承已添加",
	svc + "RemoveRoleInheritance":  "继承已移除",
	svc + "BindUserRole":           "已绑定角色",
	svc + "UnbindUserRole":         "已解绑角色",
	svc + "SetApplicationStatus":   "应用状态已更新",
	svc + "RevokeAdminGrant":       "管理员授权已撤销",
	svc + "UnbindOperatorRole":     "算子角色已解绑",
	svc + "DeleteTenantTemplate":   "模板已删除",
	// 一次性 secret 动作(轮换/重置)不进 flash(走专管线)。
}

// flashFor 返回该 fullMethod 的成功文案，缺省回退通用语。
func flashFor(fullMethod string) string {
	if m, ok := flashMessages[fullMethod]; ok {
		return m
	}
	return "操作成功"
}

// sessionID 从 cookie 取会话 id（无则空串）。
func (h *Handler) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
```

> 核对 `sessionCookieName` 常量名（auth.go 已定义）。映射里的 fullMethod 用既有 `svc` 前缀常量。

- [ ] **步骤 4：doWrite 成功后写 flash（handler.go）**

把 `doWrite` 末段的 `http.Redirect(...)` 前插入：

```go
	if id := h.sessionID(r); id != "" {
		if err := h.sessions.SetFlash(ctx, id, flashFor(fullMethod)); err != nil {
			h.logger.Warn("console set flash", "err", err) // fail-soft：flash 失败不影响已成功的写
		}
	}
	http.Redirect(w, r, redirectTo(r), http.StatusSeeOther)
```

> 核对 `h.logger` 字段名（render.go 用 `h.logger.Error`，存在）。

- [ ] **步骤 5：renderPage 读 flash 注入（render.go）**

在 `renderPage` 取 tmpl 后、Execute 前插入：

```go
	if _, set := data["Flash"]; !set {
		if id := h.sessionID(r); id != "" {
			if msg, err := h.sessions.TakeFlash(r.Context(), id); err == nil && msg != "" {
				data["Flash"] = msg
			}
		}
	}
```

- [ ] **步骤 6：layout.html 渲 toast**

在 `<main id="main">` 行后插入：

```html
{{if .Flash}}<div class="toast" role="status" aria-live="polite" data-toast>{{.Flash}}<button type="button" class="toast-close" aria-label="关闭提示">×</button></div>{{end}}
```

- [ ] **步骤 7：测试验证通过 + 全包回归**

运行：`go test ./internal/controlplane/console/ -run TestConsole_Flash -count=1` → PASS；再 `go test ./internal/controlplane/console/ -count=1` → 全绿。`gofmt -l`/`go vet` 净。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/flash.go internal/controlplane/console/flash_test.go internal/controlplane/console/handler.go internal/controlplane/console/render.go internal/controlplane/console/templates/layout.html
git commit -m "feat(console): 写动作成功后一次性 flash → toast(doWrite 写/renderPage 读后即清/layout role=status,业务语言,fail-soft)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：通用二次确认门

**文件：**
- 创建：`internal/controlplane/console/confirm.go`、`templates/ops_confirm.html`、`confirm_test.go`

- [ ] **步骤 1：先写失败测试 `confirm_test.go`**

用一个**临时探针路由**直接测确认门（避免依赖任务 4 接入）。在测试里注册一个走 `requireConfirm` 的最小 handler，或直接对一个已接入的动作测——但任务 3 先测机制本身：

```go
func TestConsole_ConfirmGate(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	// 先建一个角色供删除。
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := seedRoleViaConsole(t, ts, c, csrf, appID, "viewer", "查看员") // 本文件助手：POST /roles 建角色取 RoleId；或直接 store.InsertRole

	base := ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/roles/" + strconv.FormatInt(roleID, 10) + "/delete"

	// 不带 confirmed → 渲确认页（200，含确认问句），角色仍在。
	resp, err := c.PostForm(base, url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确认") // 确认问句
	require.Contains(t, body, `name="confirmed" value="1"`) // 确认页回填确认标志
	require.Contains(t, body, `name="csrf_token"`)          // 回填 CSRF

	// 带 confirmed=1 → 执行 + PRG（302/303 → 跟随到列表）。
	resp2, err := c.PostForm(base, url.Values{"csrf_token": {csrf}, "confirmed": {"1"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode) // 跟随 PRG 后
	body2 := readBody(t, resp2)
	require.Contains(t, body2, "已删除") // flash
}
```

> `seedRoleViaConsole` 用既有 `store.InsertRole(ctx, db, appID, code, name)` 直接建并返回 id 最简（与 M3.3 mgmt 测试一致）。本任务依赖任务 4 把 `deleteRole` 接上确认门——若想任务 3 独立，可先在本任务内把 `deleteRole` 接上（移一小步到任务 3），并把任务 4 缩为「接入其余 7 个」。**实现者择一，保持每任务可独立验证。**

- [ ] **步骤 2：运行验证失败** → FAIL（无确认页 / deleteRole 直接执行）。

- [ ] **步骤 3：实现 `confirm.go`**

```go
package console

import "net/http"

// confirmPrompts 是破坏性 fullMethod → 业务语言确认问句（缺省回退通用语）。
var confirmPrompts = map[string]string{
	svc + "DeleteRole":            "确定删除该业务角色吗？此操作不可撤销。",
	svc + "RemoveRoleInheritance": "确定移除该继承关系吗？",
	svc + "RevokeAdminGrant":      "确定撤销该管理员授权吗？此操作立即生效。",
	svc + "UnbindOperatorRole":    "确定解绑该算子角色吗？此操作立即生效。",
	svc + "RotateApplicationSecret": "确定轮换应用凭据吗？旧凭据将立即失效。",
	svc + "ResetOperatorSecret":   "确定重置该算子凭据吗？旧凭据将立即失效。",
	svc + "DeleteTenantTemplate":  "确定删除该模板吗？此操作不可撤销。",
	svc + "SetApplicationStatus":  "确定停用该应用吗？停用后将拒绝该应用的写操作。",
}

func confirmPrompt(fullMethod string) string {
	if p, ok := confirmPrompts[fullMethod]; ok {
		return p
	}
	return "确定执行此操作吗？"
}

// requireConfirm 是破坏性动作的二次确认门。
// 缺 confirmed=1 → 校验会话+CSRF 后渲染通用确认页(回显原 POST 表单值为隐藏字段)，返回 false(调用方应 return)；
// 有 confirmed=1 → 返回 true，调用方继续(后续 doWrite/专管线再次校验 CSRF/授权/status)。
// 无 JS：表单 POST→确认页→确认 POST；有 JS：interactions.js 弹 dialog 跳过往返。
func (h *Handler) requireConfirm(w http.ResponseWriter, r *http.Request, fullMethod string) bool {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return false
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return false
	}
	if r.FormValue("confirmed") == "1" {
		return true
	}
	// 回显原 POST 的非 csrf/confirmed 表单值为隐藏字段(html/template 自动转义)。
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, codes.InvalidArgument, "表单解析失败", nil)
		return false
	}
	type kv struct{ Name, Value string }
	var hidden []kv
	for name, vals := range r.PostForm {
		if name == "csrf_token" || name == "confirmed" {
			continue
		}
		for _, v := range vals {
			hidden = append(hidden, kv{Name: name, Value: v})
		}
	}
	h.renderPage(w, r, "ops_confirm.html", http.StatusOK, map[string]any{
		"Action": r.URL.Path,
		"Prompt": confirmPrompt(fullMethod),
		"Hidden": hidden,
		"CSRF":   sess.CSRF,
	})
	return false
}
```

> 核对 `h.renderError` 签名（doWrite 用 `h.renderError(w, r, codes.PermissionDenied, "...", nil)`，存在）+ `codes` import。

- [ ] **步骤 4：实现 `templates/ops_confirm.html`**

```html
{{define "title"}}确认操作 · 司域 Console{{end}}
{{define "content"}}<div class="workspace"><section>
<div class="card dialog" role="alertdialog" aria-labelledby="confirm-q">
<h1 id="confirm-q">{{.Prompt}}</h1>
<form method="post" action="{{.Action}}" class="inline-form">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <input type="hidden" name="confirmed" value="1">
  {{range .Hidden}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}
  <button type="submit" class="btn danger">确认</button>
  <a class="btn" href="javascript:history.back()">取消</a>
</form>
</section></div>{{end}}
```

> 取消用 `history.back()`（无 JS 时退化为返回上一页；可接受）。若要纯无 JS 取消，改 `<a class="btn" href="{{.Action}}">` 不可——指回原页更稳妥：实现者可把取消链接指向 referer 的安全回退（如所在 app 的列表页），但 `history.back()` 对管理台够用。

- [ ] **步骤 5：测试验证通过**（含把 `deleteRole` 接上确认门——见步骤 1 备注，若选择任务 3 内接入则此处改 `routes_rbac.go` 的 deleteRole 首行加 `if !h.requireConfirm(w, r, svc+"DeleteRole") { return }`）。

运行：`go test ./internal/controlplane/console/ -run TestConsole_ConfirmGate -count=1` → PASS。

- [ ] **步骤 6：gofmt/vet/Commit**

```bash
git add internal/controlplane/console/confirm.go internal/controlplane/console/confirm_test.go internal/controlplane/console/templates/ops_confirm.html internal/controlplane/console/routes_rbac.go
git commit -m "feat(console): 通用二次确认门 requireConfirm(confirmed=1 门+通用确认页回显原POST,CSRF/授权不变)+接入 DeleteRole

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：确认门接入其余破坏性动作 + 表单升级

**文件：**
- 修改：各破坏性 handler（首行加 `requireConfirm`）+ 对应模板（表单加 `data-confirm`、去旧 `onsubmit confirm`）
- 测试：对应 `*_test.go` 各加「无 confirmed → 确认页、带 confirmed → 执行」断言

**破坏性动作清单（fullMethod → handler 定位 → 模板）：**

| fullMethod | handler（grep 定位） | 备注 |
|---|---|---|
| `DeleteRole` | routes_rbac.go `deleteRole`（已接，任务 3） | doWrite |
| `RemoveRoleInheritance` | routes_rbac.go `removeInheritance` | doWrite |
| `RevokeAdminGrant` | grep `svc+"RevokeAdminGrant"` | doWrite |
| `UnbindOperatorRole` | grep `svc+"UnbindOperatorRole"` | doWrite |
| `DeleteTenantTemplate` | routes_tenant_templates.go | doWrite |
| `SetApplicationStatus` | routes_apps.go `setAppStatus` | **仅停用时确认**（见步骤 2） |
| `RotateApplicationSecret` | routes_apps.go `rotateAppSecret` | 一次性 secret 专管线（非 doWrite） |
| `ResetOperatorSecret` | grep `svc+"ResetOperatorSecret"` | 一次性 secret 专管线 |

- [ ] **步骤 1：先写失败测试（每动作一条，追加到对应 *_test.go）**

对每个动作仿任务 3 的 `TestConsole_ConfirmGate` 写「无 confirmed → 200 含确认问句 + 未执行；带 confirmed=1 → 执行」。播种用既有 store helper（`store.InsertRole`/`InsertRoleInheritance`/`UpsertDataPolicy` 等）。一次性 secret 动作（rotate/reset）断言：无 confirmed → 确认页；带 confirmed=1 → 渲一次性 secret 展示页（核对既有展示页标识串）。

- [ ] **步骤 2：每个 handler 首行加确认门**

doWrite 类（DeleteRole 已做）——在 handler 函数体首行（取 path 前或后均可，但须在调 doWrite 前）加：

```go
	if !h.requireConfirm(w, r, svc+"RemoveRoleInheritance") {
		return
	}
```

**SetApplicationStatus 特例**（仅停用确认）：

```go
	if r.FormValue("status") == "disabled" { // 核对 setAppStatus 实际读的状态字段名/值
		if !h.requireConfirm(w, r, svc+"SetApplicationStatus") {
			return
		}
	}
```

一次性 secret 类（rotate/reset）——在 handler 首行（专管线执行前）加同样的 `if !h.requireConfirm(...) { return }`。

> **定位指令**：用 `grep -rn 'svc+"<Method>"' internal/controlplane/console/*.go`（排除 _test）找到每个 handler，按其实际函数结构在「校验通过、执行写之前」插入确认门。RevokeAdminGrant/UnbindOperatorRole/ResetOperatorSecret 的 handler 名与文件以 grep 结果为准。

- [ ] **步骤 3：每个破坏性表单升级（模板）**

把每个破坏性表单（如 `roles.html:15` 的删除表单）：
- 去掉旧 `onsubmit="return confirm('删除？')"`。
- 给 `<form>` 加 `data-confirm="<同 confirmPrompts 的简短问句>"`（interactions.js 据此弹 dialog）。
例（roles.html 删除表单）：

```html
<td><form method="post" action="/apps/{{$.AppID}}/roles/{{.RoleId}}/delete" data-confirm="确定删除该业务角色吗？此操作不可撤销。">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}"><button class="btn danger">删除</button></form></td>
```

> 逐个破坏性表单同样处理（grep `onsubmit="return confirm` 找旧确认全部替换；grep 各破坏动作的 `action="..."` 表单加 `data-confirm`）。模板里凡破坏性动作表单都加 `data-confirm`。

- [ ] **步骤 4：测试 + 全包**

运行：`go test ./internal/controlplane/console/ -count=1` → 全绿。`gofmt -l`/`go vet` 净。`grep -rn 'onsubmit="return confirm' internal/controlplane/console/templates/` → 空（旧裸 confirm 已清）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): 8 破坏性动作接入二次确认门(含 rotate/reset 一次性 secret 与 SetApplicationStatus 仅停用)+表单 data-confirm 替代裸 onsubmit confirm

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：interactions.js（toast 自消失 + dialog 确认）+ 加载 + CSS

**文件：**
- 创建：`internal/controlplane/console/static/interactions.js`
- 修改：`templates/layout.html`（`<script defer>`）、`static/css/components.css`（`.toast`/`.dialog`/`.toast-close` 视觉）

- [ ] **步骤 1：实现 `interactions.js`（无依赖原生，渐进增强）**

```javascript
// interactions.js —— M3.4a 渐进增强：toast 自消失 + 破坏性表单 dialog 二次确认。
// 无此脚本时页面功能完整(静态 flash + 服务端确认页)。
(function () {
  "use strict";

  // ① toast：N 秒后淡出 + 可手动关闭。
  function initToasts() {
    document.querySelectorAll("[data-toast]").forEach(function (el) {
      var close = el.querySelector(".toast-close");
      if (close) close.addEventListener("click", function () { el.remove(); });
      setTimeout(function () { el.classList.add("toast-hide"); }, 4000);
      el.addEventListener("transitionend", function () {
        if (el.classList.contains("toast-hide")) el.remove();
      });
    });
  }

  // ② 破坏性表单：拦截提交 → <dialog> 确认 → 确认即带 confirmed=1 提交原 action。
  function initConfirms() {
    var forms = document.querySelectorAll("form[data-confirm]");
    if (!forms.length || typeof HTMLDialogElement === "undefined") return; // 不支持 dialog → 退化为服务端确认页
    forms.forEach(function (form) {
      form.addEventListener("submit", function (e) {
        if (form.dataset.confirmed === "1") return; // 已确认，放行
        e.preventDefault();
        showConfirm(form.getAttribute("data-confirm"), function () {
          var hidden = document.createElement("input");
          hidden.type = "hidden"; hidden.name = "confirmed"; hidden.value = "1";
          form.appendChild(hidden);
          form.dataset.confirmed = "1";
          form.submit();
        });
      });
    });
  }

  function showConfirm(message, onOk) {
    var dlg = document.createElement("dialog");
    dlg.className = "dialog";
    dlg.setAttribute("role", "alertdialog");
    var p = document.createElement("p"); p.textContent = message; dlg.appendChild(p);
    var ok = document.createElement("button"); ok.className = "btn danger"; ok.textContent = "确认";
    var cancel = document.createElement("button"); cancel.className = "btn"; cancel.textContent = "取消";
    dlg.appendChild(ok); dlg.appendChild(cancel);
    document.body.appendChild(dlg);
    var trigger = document.activeElement;
    cancel.addEventListener("click", function () { dlg.close(); });
    ok.addEventListener("click", function () { dlg.close(); onOk(); });
    dlg.addEventListener("close", function () { dlg.remove(); if (trigger && trigger.focus) trigger.focus(); });
    dlg.showModal(); // 自带焦点陷阱 + ESC 关
    ok.focus();
  }

  document.addEventListener("DOMContentLoaded", function () { initToasts(); initConfirms(); });
})();
```

- [ ] **步骤 2：layout.html 末尾加载脚本**

在 `</body>` 前加：

```html
<script defer src="/static/interactions.js"></script>
```

- [ ] **步骤 3：components.css 视觉（仅 token，不破坏既有页）**

给 `.toast` 加自消失过渡 + `.toast-close`/`.toast-hide`，`.dialog` 居中遮罩。只用既有 token 变量；不改其它组件类。例：

```css
.toast { /* 既有 inline-block 基础上补 */ transition: opacity .4s ease; }
.toast-hide { opacity: 0; }
.toast-close { margin-left: var(--space-3); background: none; border: 0; cursor: pointer; font-size: 1.1em; }
dialog.dialog::backdrop { background: rgba(0,0,0,.4); }
dialog.dialog { /* 居中：原生 dialog showModal 自动居中，补内边距 */ padding: var(--space-5); }
```

> 核对 components.css 既有 `.toast`/`.dialog` 定义，补充而非覆盖核心；颜色一律 token。

- [ ] **步骤 4：构建 + 全包测试不回归**

运行：`go build ./... && go test ./internal/controlplane/console/ -count=1`（JS 不被 Go 测试执行，但确认 embed 收录 + 无回归）。确认 `git status` 仅 .js/.css/.html。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/static/ internal/controlplane/console/templates/layout.html
git commit -m "feat(console): interactions.js 渐进增强(toast 自消失+dialog 二次确认,无依赖原生,有无 JS 基线)+layout 加载+toast/dialog 视觉 token

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 6：有无 JS 基线浏览器走查（Playwright）

**文件：**
- 走查脚本/记录（参照 M3.1 a11y 走查范式 `docs/superpowers/2026-06-20-...-walkthrough`）

- [ ] **步骤 1：起本地 Console（参照既有 e2e/compose 或 newConsole 起法）**，登录 root@sydom。

- [ ] **步骤 2：JS 开**：
  - 触发一次写（建角色）→ 断言 toast 出现且 ~4s 后消失、× 可关。
  - 点删除 → 断言弹 `<dialog>`、ESC 关闭不删、确认则删除并 toast；焦点陷阱（Tab 不逸出）、关闭焦点归还。
  - axe-core 跑确认页 + 含 toast/dialog 的页 → 0 违规。

- [ ] **步骤 3：JS 关**（浏览器禁用 JS 或移除 script）：
  - 触发写 → flash 静态成功条可见。
  - 点删除 → 跳服务端确认页 → 点确认 → 删除 + flash；点取消 → 不删。
  - 断言全程功能完整（有无 JS 基线对照）。

- [ ] **步骤 4：记录走查结果**到 `docs/superpowers/2026-06-23-m3-4a-interaction-walkthrough.md`，commit。

---

## 任务 7：整体验证 IA-1..8 + opus 评审 + FF 合并

- [ ] **步骤 1：IA 不变量逐条核验**

```bash
BASE=<本计划 commit sha>
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ | wc -l   # IA-2 期望 0
git diff $BASE..HEAD -- api/proto gen/ | wc -l                                                          # 期望 0(无 proto 变更)
grep -rn 'onsubmit="return confirm' internal/controlplane/console/templates/ || echo "旧裸 confirm 已清"
ls internal/controlplane/console/static/*.js                                                            # datapolicy.js + interactions.js
grep -rn "secret" internal/controlplane/console/flash.go internal/controlplane/console/confirm.go || echo "IA-4 OK: flash/confirm 无 secret"
```

- [ ] **步骤 2：格式/静态/全量测试**

```bash
gofmt -l internal/ api/        # 空
go vet ./...                   # 净
go test ./... 2>&1 | grep -cE '^FAIL'   # 0
```

- [ ] **步骤 3：opus 整体安全评审**（子代理 model=opus）：逐条核 IA-1..8 + 深挖（确认门不被绕过：confirmed=1 仍过 CSRF/AuthorizeRule/CheckStatusWrite；确认页无开放重定向/注入；flash 无 secret 不可被他人会话读到；dialog a11y；无 JS 路径完整；setAppStatus 仅停用确认正确）。READY 方可合并。

- [ ] **步骤 4：更新记忆**：`project_detailed_design_progress.md` 加 M3.4a 节；`MEMORY.md` 索引钩子追加 M3.4a 完成 + 下一步 M3.4b。

- [ ] **步骤 5：FF 合并本地 main（不 push origin）**：worktree 全绿 + opus READY 后 `git merge --ff-only`，清 worktree（参照 M3.3 收尾）。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3.1 flash+toast→任务 1(存储)+2(管线)+5(JS 自消失) ✓；§3.2 二次确认→任务 3(门)+4(接入)+5(dialog) ✓；§3.3 interactions.js→任务 5 ✓；§4.3 破坏动作清单→任务 4 ✓；§5 a11y→任务 5(dialog/toast)+6(走查) ✓；§6 IA-1..8→任务 7 ✓；§7 测试策略→各任务 TDD(无 JS 路径)+任务 6(JS 走查) ✓。

**规格偏差（计划期明确）：** ① flash 用 session（spec 既定，非 cookie）——`SetFlash/TakeFlash` 读-改-写，renderPage 每渲多一次 Redis GET，Beta 低频可接受。② SetApplicationStatus 仅在「停用」时确认（启用不破坏性），spec §4.3 写「停用 app」，计划落为条件确认。③ ops_confirm.html 取消用 `history.back()`（无 JS 退化返回上页），管理台够用。

**占位符扫描：** 各任务给出实际 Go/JS/HTML/SQL；破坏动作接入用「fullMethod→handler grep 定位」表 + 统一 pattern 代码（非占位，是适配既有 8 处同形夹具的明确指令，比照 M3.3 测试 helper 范式）。

**类型一致性：** `Session.Flash`（任务1）↔ `SetFlash/TakeFlash`（任务1）↔ `flashFor`/`sessionID`（任务2 flash.go）↔ doWrite/renderPage 调用（任务2）一致；`requireConfirm(w,r,fullMethod)`（任务3）↔ 8 handler 调用（任务4）一致；`confirmPrompts`/`flashMessages` 均 `map[string]string` 键 `svc+Method`；模板 `data-confirm`（任务4）↔ interactions.js `form[data-confirm]`（任务5）一致；`ops_confirm.html` 字段 `.Action/.Prompt/.Hidden/.CSRF`（任务3）↔ requireConfirm renderPage data（任务3）一致。
