# 司域 技术债清理（一轮）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 一次性清理 M1–M3 累积的非阻塞技术债（A2 关系表复合 FK / A3 operator 术语 / B 表现层快修 / C 删除确认），不引入新功能，授权决策核心一字不改。

**架构：** 纯增量清理。A2 = 一支可逆迁移把 app 局部性下沉到 DB（防御纵深）；A3/B/C = 控制面表现层。复用既有 `capabilityName`/`requireConfirm`/设计系统类；后端 `AuthorizeRule`/`ruleTable`/matcher/`enforcer.go` 零触碰。

**技术栈：** Go、PostgreSQL（golang-migrate）、html/template、testcontainers（PG/Redis）、testify。

**规格：** `docs/superpowers/specs/2026-06-28-sydom-tech-debt-cleanup-design.md`（`5f26b09`）。BASE = 本地 main `8718aff`。**A1 经回源核实已实现（`mgmt/errorsanitize.go`），移出范围。**

---

## 文件结构

- 创建：`db/migrations/000019_relational_composite_fk.up.sql` / `.down.sql` — 三关系表复合 FK 迁移（A2）。
- 创建：`internal/controlplane/store/composite_fk_test.go` — 复合 FK 拒绝跨 app 引用的 testcontainers 测试（A2）。
- 修改：`internal/controlplane/console/templates/{operators,operator_new,operator_created,operator_secret_reset}.html` — 算子→操作员（A3）。
- 修改：`internal/controlplane/console/flash.go`、`confirm.go` — 算子→操作员展示文案（A3）；confirm.go 加 DeleteDataPolicy 条目（C）。
- 修改：`internal/controlplane/console/routes_confirm_actions_test.go` — 既有 2 处断言文案同步改操作员（A3）。
- 修改：`internal/controlplane/console/routes_ops.go`（`opsRoleNewForm`）、`templates/ops_role_new.html` — 权限点选项走 capabilityName（B4）。
- 修改：`internal/controlplane/console/templates/ops_templates.html`、`templates/ops_tenant_template.html`、`static/css/components.css` — 2h1→h2 + 行内 style→类 + 破坏按钮 danger（B5/B6）。
- 修改：`internal/controlplane/console/routes_datapolicy.go`（`deleteDataPolicy`）、`templates/datapolicies.html` — 接入 requireConfirm + data-confirm（C）。
- 创建：`internal/controlplane/console/routes_datapolicy_confirm_test.go` — DeleteDataPolicy 确认门测试（C）。

每个任务独立、可单独审查；任务 1（A2）最重。

---

## 任务 1：A2 关系表复合 FK（迁移 000019 + testcontainers TDD）

**文件：**
- 创建：`db/migrations/000019_relational_composite_fk.up.sql`
- 创建：`db/migrations/000019_relational_composite_fk.down.sql`
- 创建：`internal/controlplane/store/composite_fk_test.go`

- [ ] **步骤 1：先写失败测试**

`internal/controlplane/store/composite_fk_test.go`：

```go
package store_test

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest" // blank-imports lib/pq，注册 postgres 驱动
	"github.com/stretchr/testify/require"
)

// 复合 FK 应拒绝「本行 app_id 与被引用 role/permission 的 app_id 不一致」的跨 app 引用。
func TestCompositeFK_RejectsCrossAppPermission(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, appA := dbtest.SeedAppInTenant(t, db, "fk-a", "fk-a", "fk-a-key")
	_, appB := dbtest.SeedAppInTenant(t, db, "fk-b", "fk-b", "fk-b-key")

	var roleA int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'r', 'R') RETURNING id`, appA).Scan(&roleA))
	var permB int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'c', 'res', 'act', 'data', 'P') RETURNING id`, appB).Scan(&permB))

	// app_id=A、role=A 的、permission=B 的 → 复合 FK (app_id,permission_id) 应拒绝。
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		appA, roleA, permB)
	require.Error(t, err, "跨 app permission 引用必须被复合 FK 拒绝")
	require.Contains(t, err.Error(), "foreign key")
}

// 同 app 引用应正常通过（复合 FK 不破坏合法写入）。
func TestCompositeFK_AllowsSameApp(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, app := dbtest.SeedAppInTenant(t, db, "fk-ok", "fk-ok", "fk-ok-key")
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'r', 'R') RETURNING id`, app).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'c', 'res', 'act', 'data', 'P') RETURNING id`, app).Scan(&permID))
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		app, roleID, permID)
	require.NoError(t, err)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run TestCompositeFK -count=1`
预期：`TestCompositeFK_RejectsCrossAppPermission` FAIL（迁移 000019 不存在，跨 app 插入被旧单列 FK 放行 → `require.Error` 失败）；`AllowsSameApp` 可能已 PASS。

- [ ] **步骤 3：编写 up 迁移**

`db/migrations/000019_relational_composite_fk.up.sql`：

```sql
-- 把 role/permission 的 app 局部性下沉到 DB（防御纵深，补「DB 真相源」）。
-- 前置：既有跨 app 引用 → RAISE 拒迁（fail-close，不静默修复）。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM role_permission rp JOIN role r ON r.id = rp.role_id WHERE r.app_id <> rp.app_id) THEN
        RAISE EXCEPTION 'role_permission has cross-app role references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_permission rp JOIN permission p ON p.id = rp.permission_id WHERE p.app_id <> rp.app_id) THEN
        RAISE EXCEPTION 'role_permission has cross-app permission references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_inheritance ri JOIN role r ON r.id = ri.parent_role_id WHERE r.app_id <> ri.app_id) THEN
        RAISE EXCEPTION 'role_inheritance has cross-app parent references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM role_inheritance ri JOIN role r ON r.id = ri.child_role_id WHERE r.app_id <> ri.app_id) THEN
        RAISE EXCEPTION 'role_inheritance has cross-app child references; aborting';
    END IF;
    IF EXISTS (SELECT 1 FROM user_role_binding b JOIN role r ON r.id = b.role_id WHERE r.app_id <> b.app_id) THEN
        RAISE EXCEPTION 'user_role_binding has cross-app role references; aborting';
    END IF;
END $$;

-- 复合 FK 目标：app 内唯一键（id 已是 PK，此唯一键供 (app_id,id) 复合 FK 引用）。
ALTER TABLE role       ADD CONSTRAINT uq_role_app_id       UNIQUE (app_id, id);
ALTER TABLE permission ADD CONSTRAINT uq_permission_app_id UNIQUE (app_id, id);

-- role_permission：单列 FK → 复合 FK（ADD 时 PG 校验既有行，违例则迁移失败）。
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_role;
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_permission;
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_role_app
    FOREIGN KEY (app_id, role_id) REFERENCES role(app_id, id);
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_permission_app
    FOREIGN KEY (app_id, permission_id) REFERENCES permission(app_id, id);

-- role_inheritance：parent/child 复合 FK。
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_parent;
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_child;
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_parent_app
    FOREIGN KEY (app_id, parent_role_id) REFERENCES role(app_id, id);
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_child_app
    FOREIGN KEY (app_id, child_role_id) REFERENCES role(app_id, id);

-- user_role_binding：role 复合 FK。
ALTER TABLE user_role_binding DROP CONSTRAINT fk_user_role_binding_role;
ALTER TABLE user_role_binding ADD CONSTRAINT fk_user_role_binding_role_app
    FOREIGN KEY (app_id, role_id) REFERENCES role(app_id, id);
```

- [ ] **步骤 4：编写 down 迁移（可逆）**

`db/migrations/000019_relational_composite_fk.down.sql`：

```sql
-- 逆序还原为单列 FK；先卸所有复合 FK，再删唯一键。
ALTER TABLE user_role_binding DROP CONSTRAINT fk_user_role_binding_role_app;
ALTER TABLE user_role_binding ADD CONSTRAINT fk_user_role_binding_role
    FOREIGN KEY (role_id) REFERENCES role(id);

ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_parent_app;
ALTER TABLE role_inheritance DROP CONSTRAINT fk_role_inheritance_child_app;
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_parent
    FOREIGN KEY (parent_role_id) REFERENCES role(id);
ALTER TABLE role_inheritance ADD CONSTRAINT fk_role_inheritance_child
    FOREIGN KEY (child_role_id) REFERENCES role(id);

ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_role_app;
ALTER TABLE role_permission DROP CONSTRAINT fk_role_permission_permission_app;
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_role
    FOREIGN KEY (role_id) REFERENCES role(id);
ALTER TABLE role_permission ADD CONSTRAINT fk_role_permission_permission
    FOREIGN KEY (permission_id) REFERENCES permission(id);

ALTER TABLE role       DROP CONSTRAINT uq_role_app_id;
ALTER TABLE permission DROP CONSTRAINT uq_permission_app_id;
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/store/ -run TestCompositeFK -count=1`
预期：两测 PASS（迁移 000019 被 MigratedDSN 自动应用；跨 app 插入被拒、同 app 通过）。

- [ ] **步骤 6：回归 + 提交**

运行：`go test ./internal/controlplane/store/ ./internal/controlplane/mgmt/ ./internal/dbtest/ -count=1`（既有写路径不受影响）。

```bash
git add db/migrations/000019_relational_composite_fk.up.sql db/migrations/000019_relational_composite_fk.down.sql internal/controlplane/store/composite_fk_test.go
git commit -m "feat(db): 000019 关系表复合 FK(role/permission 加 UNIQUE(app_id,id),role_permission/role_inheritance/user_role_binding 5 条 FK 改复合,app 局部性下沉 DB 防御纵深,up 前置跨 app 数据校验 fail-close)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：A3 operator 术语统一为「操作员」

**文件：**
- 修改：`internal/controlplane/console/templates/operators.html:3,24,29`、`operator_new.html:3`、`operator_created.html:3`、`operator_secret_reset.html:3`
- 修改：`internal/controlplane/console/flash.go:17,22,23`、`confirm.go:14,16`
- 修改：`internal/controlplane/console/routes_confirm_actions_test.go:125,264`（断言文案同步）

- [ ] **步骤 1：改测试断言（先红）**

`routes_confirm_actions_test.go`：把两处断言里的「算子」改「操作员」：
- 第 125 行：`require.Contains(t, body, "确定解绑该操作员角色吗？此操作立即生效。")`
- 第 264 行：`require.Contains(t, body, "确定重置该操作员凭据吗？旧凭据将立即失效。")`

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConfirm -count=1`
预期：两用例 FAIL（confirm.go 仍渲「算子」，断言找不到「操作员」）。

- [ ] **步骤 3：替换全部展示文案「算子」→「操作员」**

逐文件改（仅中文展示文案，**不动任何 Go 标识符/RPC 名/列名/URL**）：

- `confirm.go:14`：`svc + "UnbindOperatorRole": "确定解绑该操作员角色吗？此操作立即生效。",`
- `confirm.go:16`：`svc + "ResetOperatorSecret": "确定重置该操作员凭据吗？旧凭据将立即失效。",`
- `flash.go:17`：`svc + "UnbindOperatorRole": "操作员角色已解绑",`
- `flash.go:22`：`svc + "BindOperatorRole": "已绑定操作员角色",`
- `flash.go:23`：`svc + "SetOperatorStatus": "操作员状态已更新",`
- `operators.html:3`：`<nav class="breadcrumb" aria-label="面包屑">系统 · 操作员</nav>`
- `operators.html:24`：`data-confirm="确定解绑该操作员角色吗？此操作立即生效。"`
- `operators.html:29`：`data-confirm="确定重置该操作员凭据吗？旧凭据将立即失效。"`
- `operator_new.html:3`：`<nav class="breadcrumb" aria-label="面包屑">操作员 · 新建</nav>`
- `operator_created.html:3`：`<nav class="breadcrumb" aria-label="面包屑">操作员 · 已创建</nav>`
- `operator_secret_reset.html:3`：`<nav class="breadcrumb" aria-label="面包屑">操作员 · 凭据已重置</nav>`

- [ ] **步骤 4：运行验证通过 + 全仓 grep 核实无残留**

运行：`go test ./internal/controlplane/console/ -run 'TestConfirm' -count=1`（PASS）。
运行：`grep -rn "算子" internal/ docs/superpowers/specs docs/superpowers/plans 2>/dev/null | grep -v "项目_detailed\|MEMORY"`
预期：测试 PASS；grep 在 `internal/` 下无「算子」展示残留（计划/规格里的历史叙述不算）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/templates/operators.html internal/controlplane/console/templates/operator_new.html internal/controlplane/console/templates/operator_created.html internal/controlplane/console/templates/operator_secret_reset.html internal/controlplane/console/flash.go internal/controlplane/console/confirm.go internal/controlplane/console/routes_confirm_actions_test.go
git commit -m "refactor(console): A3 operator 中文术语统一为「操作员」(5 模板+flash/confirm 文案,代码标识符不动,测试断言同步)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：B 表现层快修（B4 capabilityName / B5 ops_templates / B6 danger）

**文件：**
- 修改：`internal/controlplane/console/routes_ops.go`（`opsRoleNewForm`）、`templates/ops_role_new.html`
- 修改：`internal/controlplane/console/templates/ops_templates.html`、`templates/ops_tenant_template.html`、`static/css/components.css`

- [ ] **步骤 1：先写失败测试（B4 无裸原语）**

在 `internal/controlplane/console/onboarding_test.go` 同包新建/追加到 `routes_ops_test.go`（若无则建 `internal/controlplane/console/ops_role_new_test.go`）：

```go
package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 建角色页的权限点选项必须渲业务名（capabilityName），缺名也不得出现裸 resource:action。
func TestOpsRoleNew_NoNakedPrimitive(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	// 直插一个无 name 的权限点，触发 capabilityName 合成「resource · 动词」。
	_, err := db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1,'order:read','order','read','data','')`, appID)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/ops/apps/" + strconv.FormatInt(appID, 10) + "/roles/new")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, body, "order:read", "缺名权限点不得渲裸 resource:action")
	require.Contains(t, body, "order · ", "应渲 capabilityName 合成的「resource · 动词」")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestOpsRoleNew_NoNakedPrimitive -count=1`
预期：FAIL（模板当前 `{{else}}{{.Resource}}:{{.Action}}` 渲出 `order:read`）。

- [ ] **步骤 3：B4 — handler 预算业务名，模板只渲 .Label**

`routes_ops.go` 的 `opsRoleNewForm`：把直传 `resp.Permissions` 改为构造带 Label 的视图（`capabilityName` 已在本包，签名 `capabilityName(name, resource, action) string`）。替换 `h.renderPage(... "Permissions": resp.Permissions ...)` 段为：

```go
	type permOption struct {
		PermissionId int64
		Label        string
	}
	var perms []permOption
	for _, p := range resp.Permissions {
		perms = append(perms, permOption{
			PermissionId: p.PermissionId,
			Label:        capabilityName(p.Name, p.Resource, p.Action), // 缺名→「resource · 动词」，不裸 resource:action
		})
	}
	h.renderPage(w, r, "ops_role_new.html", http.StatusOK, map[string]any{
		"AppID": appID, "Permissions": perms, "CSRF": sess.CSRF, "OpsNav": "roles",
	})
```

`templates/ops_role_new.html:12` 改为：

```html
<label><input type="checkbox" name="permission_ids" value="{{.PermissionId}}"> {{.Label}}</label><br>
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/console/ -run TestOpsRoleNew_NoNakedPrimitive -count=1`
预期：PASS。

- [ ] **步骤 5：B5 — ops_templates 2h1→h2 + 行内 style→类**

`static/css/components.css` 末尾追加（token 化，零硬编码色）：

```css
/* 运营台模板库版式（替代行内 style） */
.ops-subsection { margin-top: var(--space-6); }
.card-spaced { margin-bottom: var(--space-4); }
.card-spaced-top { margin-top: var(--space-4); }
```

`templates/ops_templates.html` 改：
- 第 31 行 `<h1 style="margin-top:var(--space-6)">我的模板</h1>` → `<h2 class="ops-subsection">我的模板</h2>`（页面恰一个 h1=「模板库」）。
- 第 10、35 行 `<div class="card" style="margin-bottom:var(--space-4)">` → `<div class="card card-spaced">`。
- 第 49 行 `<div class="card" style="margin-top:var(--space-4)">` → `<div class="card card-spaced-top">`。
- 第 39 行删除 `style="display:inline"`（`.inline-form` 已是 `display:inline-flex`，行内级流式排布，多余覆盖去掉；走查核实预览/删除仍并排）。

- [ ] **步骤 6：B6 — 破坏按钮加 danger**

- `templates/ops_templates.html:41` `<button class="btn">删除</button>` → `<button class="btn danger">删除</button>`。
- `templates/ops_tenant_template.html` 同类「删除」破坏按钮 `class="btn"` → `class="btn danger"`（grep 该文件确认目标按钮）。

- [ ] **步骤 7：B5 结构断言（单 h1）+ 回归**

在步骤 1 的测试文件追加：

```go
func TestOpsTemplates_SingleH1(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/ops/apps/" + strconv.FormatInt(appID, 10) + "/templates")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, 1, strings.Count(body, "<h1"), "模板库页应恰一个 h1")
}
```

运行：`go test ./internal/controlplane/console/ -run 'TestOpsRoleNew_NoNakedPrimitive|TestOpsTemplates_SingleH1' -count=1`（PASS）。
运行：`grep -n 'style="' internal/controlplane/console/templates/ops_templates.html`（预期无输出）。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/routes_ops.go internal/controlplane/console/templates/ops_role_new.html internal/controlplane/console/templates/ops_templates.html internal/controlplane/console/templates/ops_tenant_template.html internal/controlplane/console/static/css/components.css internal/controlplane/console/ops_role_new_test.go
git commit -m "fix(console): B 表现层快修(B4 建角色页权限点走 capabilityName 消裸 resource:action+B5 ops_templates 2h1→h2+行内 style→CSS 类+B6 破坏按钮 danger)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：C DeleteDataPolicy 接入二次确认

**文件：**
- 修改：`internal/controlplane/console/confirm.go`（confirmMessages 加条目）
- 修改：`internal/controlplane/console/routes_datapolicy.go`（`deleteDataPolicy` 加 requireConfirm 首行）
- 修改：`internal/controlplane/console/templates/datapolicies.html:26`（删除表单加 data-confirm）
- 创建：`internal/controlplane/console/routes_datapolicy_confirm_test.go`

- [ ] **步骤 1：先写失败测试**

`internal/controlplane/console/routes_datapolicy_confirm_test.go`（镜像 `routes_confirm_actions_test.go` 范式）：

```go
package console

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 未带 confirmed=1 的删除应渲服务端确认页、不执行删除。
func TestConfirm_DeleteDataPolicy_NoConfirmed_RendersConfirmPage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	resp, err := c.PostForm(ts.URL+"/apps/"+a+"/data-policies/1/delete",
		url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确定删除该数据策略吗？此操作不可撤销。")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConfirm_DeleteDataPolicy -count=1`
预期：FAIL（当前 `deleteDataPolicy` 直走 doWrite，无确认页；body 不含确认文案）。

- [ ] **步骤 3：confirm.go 加条目**

`confirm.go` 的 confirmMessages map 加一行（与既有删除文案口径一致）：

```go
	svc + "DeleteDataPolicy":        "确定删除该数据策略吗？此操作不可撤销。",
```

- [ ] **步骤 4：deleteDataPolicy 加 requireConfirm 首行**

`routes_datapolicy.go` 的 `deleteDataPolicy` 函数体首行加（镜像 routes_rbac.go:83 DeleteRole / routes_tenant_templates.go:177 范式）：

```go
func (h *Handler) deleteDataPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteDataPolicy") {
		return
	}
	h.doWrite(w, r, svc+"DeleteDataPolicy",
		// ...（其余不变）
```

- [ ] **步骤 5：datapolicies.html 删除表单加 data-confirm（JS 增强）**

`templates/datapolicies.html:26` 删除表单加 `data-confirm` 属性（JS 开时弹原生确认，JS 关时落服务端确认页）：

```html
<td><form method="post" action="/apps/{{$.AppID}}/data-policies/{{.DataPolicyId}}/delete" data-confirm="确定删除该数据策略吗？此操作不可撤销。">
```

- [ ] **步骤 6：运行验证通过 + 回归**

运行：`go test ./internal/controlplane/console/ -run 'TestConfirm_DeleteDataPolicy|TestConfirm' -count=1`（PASS，既有确认用例不回归）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/confirm.go internal/controlplane/console/routes_datapolicy.go internal/controlplane/console/templates/datapolicies.html internal/controlplane/console/routes_datapolicy_confirm_test.go
git commit -m "fix(console): C DeleteDataPolicy 接入 requireConfirm 二次确认(与 M3.4a 8 破坏动作一致,服务端确认页+data-confirm,有无 JS 都可用)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：整体核验 TD-1..7 + 安全评审 + FF

- [ ] **步骤 1：TD 不变量逐条核验**

```bash
BASE=8718aff
# TD-1 授权真相零触碰（期望全 0 行）
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go internal/controlplane/adminauthz/ casbin/enforcer.go | wc -l
# 后端决策核心未碰（A2 是 db/migrations，A3/B/C 是 console 表现层）
git diff $BASE..HEAD --name-only -- internal/controlplane/mgmt/ internal/sidecar/ proto/ gen/ | grep -v _test.go || echo "(仅测试/无 mgmt 非测试改动)"
```
预期：authz.go/adminauthz/enforcer.go diff=0；无 sidecar/proto/gen 改动。

- [ ] **步骤 2：格式/静态/全量测试**

```bash
[ -z "$(gofmt -l internal/ api/)" ] && echo GOFMT_CLEAN || gofmt -l internal/ api/
go vet ./...
go test ./... 2>&1 | grep -cE '^FAIL'   # 期望 0
make proto-check                          # 零漂移（本轮未动 proto）
```

- [ ] **步骤 3：真实浏览器 axe 走查（B 改动页）**

复用 M3.4c 走查脚手架范式（build-tag `walkthrough`、testcontainers、系统 Chrome + axe-core 4.10.2 页内注入、走查后删脚手架不提交）：对 `ops_templates`（B5/B6 改动页，验单 h1 + 0 违规）、`ops_role_new`（B4，验权限点业务名）走查；记录到 `docs/superpowers/2026-06-28-tech-debt-cleanup-walkthrough.md` 并 commit（脚手架不提交）。

- [ ] **步骤 4：opus 整体评审（子代理 model=opus）**

逐条核 TD-1..7 + 深挖：A2 复合 FK 对既有写路径零行为影响、迁移可逆、跨 app 拒绝有齿、up 数据校验 fail-close；A3 仅展示文案、代码标识符未动；B 无裸原语 / 单 h1 / danger / 零硬编码色；C 确认门与 M3.4a 一致且不绕过授权（confirmed=1 后仍过 doWrite 全闸）；secret 不泄露。READY 方可合并。（若子代理撞会话限额无输出，控制者 inline 复跑评审。）

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加「技术债清理（一轮）」节；`MEMORY.md` 索引追加该轮完成 + A1 回源核实移除的涌现。

- [ ] **步骤 6：FF 合并本地 main（不 push origin）**

worktree 全绿 + opus READY 后 FF（`git -C <main-repo> merge --ff-only <branch>`），核实 main==feature tip，清理 worktree。

---

## 自检记录

**规格覆盖度（对照 spec §2–§8）：** §2 A1（已实现移除，无任务，规格记录在案）✓；§3 A2 → 任务 1 ✓；§4 A3 → 任务 2 ✓；§5 B4/B5/B6 → 任务 3 ✓；§6 C（DeleteDataPolicy 纳入；usersWithRole/SetFlash/双 CSRF/done 校验 YAGNI 推迟，明列不做）→ 任务 4 ✓；§7 TD-1..7 → 任务 5 步骤 1–4 ✓；§8 任务分组 → 任务 1–5 ✓。

**占位符扫描：** 无 TODO/待定；每步含实际 SQL/Go/HTML/命令与预期。

**类型一致性：** `capabilityName(name, resource, action) string`（任务 3）与既有 bizterm.go 一致；`requireConfirm(w, r, fullMethod) bool`（任务 4）与既有 confirm.go 一致；迁移约束名 up/down 对称（`fk_*_app` 新增、`fk_*`/`uq_*_app_id` 还原）。
