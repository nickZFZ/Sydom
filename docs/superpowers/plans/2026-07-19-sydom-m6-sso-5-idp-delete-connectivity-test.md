# M6-sso-5 IdP 删除 + 连通性测试 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。子代理对每个任务遵循 superpowers:test-driven-development。

**目标：** 补齐 Console IdP 管理——删除 IdP（DeleteTenantIdp RPC + 二次确认）+ 连通性测试（探已保存 issuer 的 Discover/JWKS，SSRF-安全）。

**架构：** ①删除=标准 additive RPC（proto+store+handler+ruleTable+REST+Console 二次确认）；②连通性测试=Console-only（零新增 RPC/ruleTable，复用 GetTenantIdp 授权 + `h.oidcHTTP`）。**授权求值核心零触碰**；`authz.go` 仅 +1 ruleTable `{"sso","delete",…,scopeTenant}`。

**技术栈：** Go（html/template、`internal/oidc`），Postgres（testcontainers via `internal/dbtest`），Redis（`dbtest.StartRedis`），buf/protoc-gen-go，`stretchr/testify`。

---

## 关键既有事实（实现者必读，勿凭猜）

- **`mgmt/sso.go`**：`ConfigureTenantIdp`/`GetTenantIdp`（scopeTenant）；`isUniqueViolation`/`isForeignKeyViolation`、`auditJSON`、`adminauthz.InsertAdminAudit(ctx, ex, tenantID sql.NullInt64, operator, action, entityType, entityID string, diff []byte, adminVersion sql.NullInt64)`、`cp.OperatorFromContext(ctx)` 均在 mgmt。`WriteResponse{version,changed}` proto 已有。
- **`store/tenant_idp.go`**：`UpsertTenantIdpTx`、`TenantIdpOf`、`TenantIdpSecretEnc`；导入 `context/database/sql/errors/strings/cp`。`tenant_idp_domain.tenant_id` 引用 `tenant(id)`（非 tenant_idp），**删 tenant_idp 不级联删域，须显式先删域**。
- **ruleTable**（`mgmt/authz.go`）：`ConfigureTenantIdp {"sso","update",false,scopeTenant}`、`GetTenantIdp {"sso","read",false,scopeTenant}`。本片加 `DeleteTenantIdp {"sso","delete",false,scopeTenant}`。
- **restgw**（`routes_accounts.go`）：idp 现有 `PUT`/`GET /v1/tenants/{tenant_id}/idp`；route 结构 `{method, pattern, fullMethod, decode, invoke}`，经 `mux.HandleFunc(rt.method+" "+rt.pattern, ...)` 注册（DELETE 支持）；`pfx = "/sydom.admin.v1.AdminService/"`；`pathUint64(r,"tenant_id")`；错误经 `errors.go` NotFound→404。
- **Console**：`svc = "/sydom.admin.v1.AdminService/"`；`doWrite(w,r,fullMethod, decode, invoke, redirectTo)`；`requireConfirm(w,r,fullMethod) bool`（缺 `confirmed=1`→渲染 `ops_confirm.html` 确认页并 return false；有→true）；确认文案在 `confirm.go confirmPrompts` map；Handler 持 `oidcHTTP *http.Client`（sso-2 装配，nil 于 `newConsole` 但非 nil 于 `newConsoleSSO`）；`h.sessions.SetFlash(ctx, id, msg)`；`h.sessionID(r)`；`renderGRPCError`/`renderError`/`renderPage`（layout 渲染 `.Flash` toast）。
- **`internal/oidc`**：`Discover(ctx, hc, issuer)(ProviderConfig,error)`（校 issuer 字段）、`FetchJWKS(ctx, hc, jwksURI)(JWKS,error)`。
- **测试基建**：`newConsole(t)(*httptest.Server,*RedisStore,*sql.DB)`（root@sydom 超管；`h.oidcHTTP=nil`）；`newConsoleSSO(t, baseURL)(*httptest.Server,*sql.DB,[]byte)`（**h.oidcHTTP 已设**，见 `oidc_test.go`）；`newMockIdP(t)`（serve discovery+jwks+token）；`seedIdP(t, db, mk, idpURL, enabled)int64`；`loginClient(t, ts, principal, secret)*http.Client`（CheckRedirect=ErrUseLastResponse 不跟随）；`loginAndCSRF(t, ts, store, principal, secret)(client,csrf)`；`idpGetBody`（sso-4 加，读 GET body）；`dbtest.SetupSchema(t)`；mgmt `accountsSrv(db)`、`cp.WithOperator(ctx,"root")`。
- **零触碰**：casbin/adminauthz/kernel/dataperm 机器 diff 空；`authz.go` 仅 +1 ruleTable。

## 文件结构

**修改：** `api/proto/.../admin.proto`、`mgmt/sso.go`(+`sso_test.go`)、`mgmt/authz.go`（ruleTable +1）、`restgw/routes_accounts.go`、`store/tenant_idp.go`(+test)、`console/confirm.go`（确认文案）、`console/routes_idp.go`（+2 动作）、`console/templates/idp.html`（+2 按钮）、`console/handler.go`（若新路由未在 registerIdP 内则无需改——见 T2）。**新建测试**：`console/routes_idp_test.go` 追加。

---

## 任务 1：删除 IdP 后端（proto + store + handler + ruleTable + REST）

**文件：** 改 `admin.proto`、`store/tenant_idp.go`(+test)、`mgmt/sso.go`(+`sso_test.go`)、`mgmt/authz.go`、`restgw/routes_accounts.go`。

- [ ] **步骤 1：改 proto** — `admin.proto`：
  - rpc 区（GetTenantIdp 后）加 `rpc DeleteTenantIdp(DeleteTenantIdpRequest) returns (WriteResponse);`
  - 新消息 `message DeleteTenantIdpRequest { uint64 tenant_id = 1; }`
- [ ] **步骤 2：生成 + 破坏性门** — `make proto-gen`；`make proto-breaking`（预期 PASS）。确认 `DeleteTenantIdpRequest`、`AdminServiceServer.DeleteTenantIdp` 生成。
- [ ] **步骤 3：写 mgmt 测试（先失败）** — 追加到 `internal/controlplane/mgmt/sso_test.go`：

```go
func TestDeleteTenantIdp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-del') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	// 无配置删→NotFound。
	_, err := srv.DeleteTenantIdp(ctx, &adminv1.DeleteTenantIdpRequest{TenantId: uint64(tid)})
	require.Equal(t, codes.NotFound, status.Code(err))

	// 配置后删→成功、GetTenantIdp Configured=false、域清空。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s", Domains: []string{"acme.com"}, Enabled: true})
	require.NoError(t, err)
	_, err = srv.DeleteTenantIdp(ctx, &adminv1.DeleteTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	got, err := srv.GetTenantIdp(ctx, &adminv1.GetTenantIdpRequest{TenantId: uint64(tid)})
	require.NoError(t, err)
	require.False(t, got.Configured)
	var domainCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&domainCount))
	require.Equal(t, 0, domainCount, "删除须清空域")
}
```

- [ ] **步骤 4：运行确认失败** — `go test ./internal/controlplane/mgmt/ -run TestDeleteTenantIdp`；预期 FAIL（DeleteTenantIdp 为 Unimplemented→codes.Unimplemented ≠ NotFound）。
- [ ] **步骤 5：store `DeleteTenantIdpTx`** — 追加到 `internal/controlplane/store/tenant_idp.go`：

```go
// DeleteTenantIdpTx 删除本租户 IdP 配置 + 其 email 域（域表引用 tenant 非 tenant_idp，不级联，须显式先删）。
// 返回是否真有配置被删（无配置→false，供调用方映射 NotFound）。
func DeleteTenantIdpTx(ctx context.Context, tx cp.DBTX, tenantID int64) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tenant_idp_domain WHERE tenant_id=$1`, tenantID); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tenant_idp WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
```

- [ ] **步骤 6：handler `DeleteTenantIdp`** — 追加到 `mgmt/sso.go`：

```go
// DeleteTenantIdp 删除本租户 OIDC IdP 配置（scopeTenant 自助）。无配置→NotFound。
// 不级联删 operator/membership（已开通账户保留，仅失去 SSO 登录）。
func (s *AdminServer) DeleteTenantIdp(ctx context.Context, r *adminv1.DeleteTenantIdpRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	deleted, err := store.DeleteTenantIdpTx(ctx, tx, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if !deleted {
		return nil, status.Error(codes.NotFound, "no idp configured")
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"delete_idp", "tenant_idp", fmt.Sprintf("%d", r.TenantId), nil, sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{}, nil
}
```

- [ ] **步骤 7：ruleTable +1** — `mgmt/authz.go`，GetTenantIdp 行下加：

```go
	"/sydom.admin.v1.AdminService/DeleteTenantIdp":            {"sso", "delete", false, scopeTenant},
```

- [ ] **步骤 8：REST DELETE 路由** — `restgw/routes_accounts.go`，GET idp route 后（切片内）加：

```go
		{"DELETE", "/v1/tenants/{tenant_id}/idp", pfx + "DeleteTenantIdp",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.DeleteTenantIdpRequest{TenantId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.DeleteTenantIdp(ctx, m.(*adminv1.DeleteTenantIdpRequest))
			}},
```

- [ ] **步骤 9：store 单测 + 运行通过** — 追加 `store/tenant_idp_test.go` `TestDeleteTenantIdpTx`（配置后删→TenantIdpOf Configured=false、无配置删→false）。运行 `go test ./internal/controlplane/store/ -run TestDeleteTenantIdpTx` + `go test ./internal/controlplane/mgmt/ -run TestDeleteTenantIdp` + `go build ./...`；预期 PASS。

```go
func TestDeleteTenantIdpTx(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('d') RETURNING id`).Scan(&tid))
	// 无配置→false。
	tx0, _ := db.BeginTx(ctx, nil)
	del, err := store.DeleteTenantIdpTx(ctx, tx0, tid)
	require.NoError(t, err)
	require.False(t, del)
	require.NoError(t, tx0.Commit())
	// 配置后删→true + 域清空。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc) VALUES ($1,'https://i','c','\xaa'::bytea)`, tid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, tid)
	require.NoError(t, err)
	tx1, _ := db.BeginTx(ctx, nil)
	del, err = store.DeleteTenantIdpTx(ctx, tx1, tid)
	require.NoError(t, err)
	require.True(t, del)
	require.NoError(t, tx1.Commit())
	got, err := store.TenantIdpOf(ctx, db, tid)
	require.NoError(t, err)
	require.False(t, got.Configured)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&n))
	require.Equal(t, 0, n)
}
```

- [ ] **步骤 10：Commit**

```bash
git add api/proto/ gen/ internal/controlplane/store/tenant_idp.go internal/controlplane/store/tenant_idp_test.go internal/controlplane/mgmt/sso.go internal/controlplane/mgmt/sso_test.go internal/controlplane/mgmt/authz.go internal/controlplane/restgw/routes_accounts.go
git commit -m "feat(mgmt): DeleteTenantIdp RPC/REST（scopeTenant，删配置+域，不级联删账户）（M6-sso-5 T1）"
```

---

## 任务 2：Console 删除动作（二次确认）+ 连通性测试

**文件：** 改 `console/confirm.go`（确认文案）、`console/routes_idp.go`（+2 动作 + 路由）、`console/templates/idp.html`（+2 按钮）；测试 `console/routes_idp_test.go` 追加。

- [ ] **步骤 1：写 Console 测试（先失败）** — 追加到 `internal/controlplane/console/routes_idp_test.go`：

```go
// 删除走二次确认：未确认→确认页；确认→删除后 idp 页未配置。
func TestIdPDelete_Confirm(t *testing.T) {
	ts, store, db := newConsole(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('del-ui') RETURNING id`).Scan(&tid))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	base := ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/idp"

	// 先配置。
	_, err := c.PostForm(base, url.Values{"csrf_token": {csrf}, "issuer": {"https://idp"},
		"client_id": {"cid"}, "client_secret": {"s"}, "domains": {"acme.com"}, "enabled": {"on"}})
	require.NoError(t, err)

	// 未确认删除 → 确认页（含锁死警示），未删。
	resp, err := c.PostForm(base+"/delete", url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&cnt))
	require.Equal(t, 1, cnt, "未确认不应删除")

	// 确认删除 → 删除。
	resp2, err := c.PostForm(base+"/delete", url.Values{"csrf_token": {csrf}, "confirmed": {"1"}})
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&cnt))
	require.Equal(t, 0, cnt, "确认后应删除")
}

// 连通性测试：mock IdP 可达→flash 正常；用 newConsoleSSO（h.oidcHTTP 已设）。
func TestIdPTest_Connectivity(t *testing.T) {
	idp := newMockIdP(t)
	idp.clientID = "cid"
	ts, db, mk := newConsoleSSO(t, "https://console.test")
	tid := seedIdP(t, db, mk, idp.srv.URL, true)
	c := loginClient(t, ts, "root@sydom", "rootsecret")
	base := ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/idp"

	csrf := extractCSRF(t, idpGetBody(t, c, base))
	resp, err := c.PostForm(base+"/test", url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 下一次 GET 消费 flash toast → 断言连通正常。
	require.Contains(t, idpGetBody(t, c, base), "连通正常")
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func extractCSRF(t *testing.T, body string) string {
	t.Helper()
	m := csrfRe.FindStringSubmatch(body)
	require.Len(t, m, 2, "页面应含 csrf_token")
	return m[1]
}
```
> 测试文件顶部导入需含 `net/http`、`net/url`、`regexp`、`strconv`（`io`/`strings` 已在 sso-4 的 routes_idp_test.go）。若 `extractCSRF`/`csrfRe` 已存在于测试包则复用、不重复定义。

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/console/ -run 'TestIdPDelete_Confirm|TestIdPTest_Connectivity'`；预期 FAIL（无 `/delete`、`/test` 路由）。
- [ ] **步骤 3：确认文案** — `console/confirm.go` 的 `confirmPrompts` map 加：

```go
	svc + "DeleteTenantIdp": "确定删除该租户的 SSO 配置吗？删除后该域 SSO 登录停用；仅 SSO 登录的 operator（含 JIT 开通、无密码）将无法登录，已开通的成员账户保留。此操作不可撤销。",
```

- [ ] **步骤 4：Console 动作 + 路由** — `console/routes_idp.go`：`registerIdP` 加两路由；加 `idpDelete`/`idpTest` 处理器：

```go
func (h *Handler) registerIdP(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/{tenant_id}/idp", h.idpConfig)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp", h.idpSave)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp/delete", h.idpDelete)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp/test", h.idpTest)
}

// idpDelete 删除 IdP（二次确认 + doWrite）。
func (h *Handler) idpDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteTenantIdp") {
		return
	}
	h.doWrite(w, r, svc+"DeleteTenantIdp",
		func(r *http.Request) (proto.Message, error) {
			tid, err := pathUint64(r, "tenant_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.DeleteTenantIdpRequest{TenantId: tid}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteTenantIdp(ctx, m.(*adminv1.DeleteTenantIdpRequest))
		},
		func(r *http.Request) string { return "/tenants/" + r.PathValue("tenant_id") + "/idp" })
}

// idpTest 连通性测试：探已保存 issuer 的 discovery+JWKS（SSRF-安全，不探表单 URL）。
func (h *Handler) idpTest(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	dest := "/tenants/" + r.PathValue("tenant_id") + "/idp"
	msg := &adminv1.GetTenantIdpRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantIdp", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	resp, err := h.srv.GetTenantIdp(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	var flash string
	if !resp.Configured {
		flash = "请先配置 IdP 再测试连接"
	} else if pc, err := oidc.Discover(r.Context(), h.oidcHTTP, resp.Issuer); err != nil {
		flash = "连通测试失败：无法访问 IdP discovery 端点"
	} else if _, err := oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI); err != nil {
		flash = "连通测试失败：无法访问/解析 JWKS 端点"
	} else {
		flash = "连通正常：discovery 与 JWKS 端点可达"
	}
	if id := h.sessionID(r); id != "" {
		_ = h.sessions.SetFlash(r.Context(), id, flash)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
```
文件顶部导入加 `"google.golang.org/grpc/codes"` 与 `"github.com/nickZFZ/Sydom/internal/oidc"`。

- [ ] **步骤 5：idp.html 加按钮** — `console/templates/idp.html`，在保存按钮后（`</form>` 前不行——须独立表单）加两个独立表单（均 POST+csrf，过 lint）：

```html
<button class="btn btn-primary">保存</button>
</form>
{{if .Configured}}
<form method="post" action="/tenants/{{.TenantID}}/idp/test" class="stacked-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<button class="btn">测试连接</button>
</form>
<form method="post" action="/tenants/{{.TenantID}}/idp/delete" class="stacked-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<button class="btn btn-danger">删除 SSO 配置</button>
</form>
{{end}}
```
（把原模板结尾的 `</form>{{end}}` 调整为：保存表单先闭合，再在 `{{if .Configured}}…{{end}}` 内加测试/删除表单，最后 `{{end}}` 收 content。注意原 content 的 define/end 结构。）

- [ ] **步骤 6：运行确认通过** — `go test ./internal/controlplane/console/ -run 'TestIdPDelete_Confirm|TestIdPTest_Connectivity|TestIdPConfigPage|TestTemplates_NoInlineStyle|TestPageSweep'`；预期全 PASS。
- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/confirm.go internal/controlplane/console/routes_idp.go internal/controlplane/console/routes_idp_test.go internal/controlplane/console/templates/idp.html
git commit -m "feat(console): IdP 删除（二次确认+锁死警示）+ 连通性测试（探已保存 issuer，SSRF-安全）（M6-sso-5 T2）"
```

---

## 任务 3：全局验证 + 变异 + 零触碰核验

- [ ] **步骤 1：全仓测试** — `go test ./...`；预期全绿。
- [ ] **步骤 2：proto 破坏性门** — `make proto-breaking`；预期 PASS。
- [ ] **步骤 3：零触碰机器 diff** —

```bash
BASE=origin/main
git diff --stat "$BASE"..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/
git diff "$BASE"..HEAD -- internal/controlplane/mgmt/authz.go
```
预期：前者**空**；后者仅 `ruleTable` +1 行（DeleteTenantIdp）。

- [ ] **步骤 4：变异证有齿（改后复原）** — ① 撤 `DeleteTenantIdpTx` 的 `DELETE tenant_idp_domain`（只删 tenant_idp）→ `go test ./internal/controlplane/mgmt/ -run TestDeleteTenantIdp`（域清空断言）；预期红。复原。② 撤 `idpTest` 的 `!resp.Configured` 先拦（直接 Discover 空 issuer）→ 未配置连通测试用例（可加：未配置租户 POST /test → 应 flash「请先配置」）；或复核既有断言。各复原后 PASS。
- [ ] **步骤 5：`go vet`** — `go vet ./internal/controlplane/store/ ./internal/controlplane/mgmt/ ./internal/controlplane/console/ ./internal/controlplane/restgw/`；预期无告警。
- [ ] **步骤 6：Commit（仅当步骤 3/4 有修补）**。

---

## 自检（规格覆盖 / 占位符 / 类型一致）

**规格覆盖度（对照 spec §2/§6）：** §2.1 DeleteTenantIdp 全套→T1；§2.2 Console 删除二次确认→T2；§2.3 连通性测试→T2；§2.4 零触碰+authz.go+1→T3。§6 SSRF-安全（探已保存 issuer）→T2 `idpTest` 读 GetTenantIdp.Issuer；INV-1（审计无 secret，diff=nil）→T1；二次确认→T2 requireConfirm；不级联删账户→T1（仅删 tenant_idp+域）。**全覆盖。**

**占位符扫描：** 无 TODO/待定；每步含可编译代码或精确命令。T2 测试 `extractCSRF`/导入若已存在则复用（已注明）。

**类型一致：** `DeleteTenantIdpTx(ctx, tx cp.DBTX, tenantID int64)(bool,error)`、`DeleteTenantIdp(ctx,*DeleteTenantIdpRequest)(*WriteResponse,error)`、ruleTable key `DeleteTenantIdp`、REST pfx+"DeleteTenantIdp" 一致；`oidc.Discover`/`FetchJWKS`、`h.oidcHTTP`、`SetFlash`/`sessionID` 均既有。

## 落地顺序 / 依赖

T1→T2→T3。T2 删除动作依赖 T1（DeleteTenantIdp RPC）；连通性测试依赖既有 oidc/GetTenantIdp。每任务独立 commit，本地 FF，push origin 待用户定。
