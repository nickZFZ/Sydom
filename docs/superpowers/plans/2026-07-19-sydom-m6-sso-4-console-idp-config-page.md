# M6-sso-4 Console IdP 配置页 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。子代理对每个任务遵循 superpowers:test-driven-development。

**目标：** 租户 owner 经 Console UI 自助配置本租户 OIDC IdP（issuer/client_id/client_secret/域/enabled/jit_enabled），补齐 SSO 可用性闭环。client_secret 编辑留空=保持不变。

**架构：** ①后端小加法（`store.TenantIdpSecretEnc` 读密文 + `ConfigureTenantIdp` 空-secret 复用旧密文，`UpsertTenantIdpTx` 不变）；②Console 页 `GET/POST /tenants/{id}/idp` 照 `routes_usage.go`/`doWrite` 范式。**授权求值核心 + ruleTable 零改**。

**技术栈：** Go（html/template、`google.golang.org/protobuf/proto`），Postgres（testcontainers via `internal/dbtest`），Redis（`dbtest.StartRedis`），`stretchr/testify`。

---

## 关键既有事实（实现者必读，勿凭猜）

- **`ConfigureTenantIdp`/`GetTenantIdp`**（`mgmt/sso.go`，M6-sso-1/3）：Configure 现校验 `if r.Issuer=="" || r.ClientId=="" || r.ClientSecret=="" || len(r.Domains)==0 → InvalidArgument`，再逐域非空校验，`enc=crypto.Encrypt(masterKey, secret)`，`tx`，`store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId), r.Issuer, r.ClientId, enc, r.Domains, r.Enabled, r.JitEnabled)`，审计 `configure_idp` diff `{issuer,client_id,domains,enabled,jit_enabled}`（`isUniqueViolation→AlreadyExists`、`isForeignKeyViolation→NotFound`）。Get 返回 `{Configured, Issuer, ClientId, Domains, Enabled, JitEnabled}`（**无 client_secret**）。二者已在 ruleTable `{"sso","update"/"read",false,scopeTenant}`。
- **`store/tenant_idp.go`**：`TenantIdp{Configured,Issuer,ClientID,Domains,Enabled,JITEnabled}`；`UpsertTenantIdpTx(ctx, tx cp.DBTX, tenantID int64, issuer, clientID string, secretEnc []byte, domains []string, enabled, jitEnabled bool)`；`TenantIdpOf`。导入含 `context/database/sql/errors/strings/cp`。
- **Console 读页范式**（`routes_usage.go`）：`principal,_,ok := h.requireSession(w,r)` → `tid,err := pathUint64(r,"tenant_id")` → `ctx,err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"X", principal, msg)` → `h.srv.X(ctx,msg)` → `h.renderPage(w,r,"x.html",200,map)`。错误 `h.renderGRPCError(w,r,svc+"X",err)`。
- **Console 写范式** `doWrite`（`handler.go`）：`h.doWrite(w, r, svc+"Method", decode func(*http.Request)(proto.Message,error), invoke func(context.Context,*mgmt.AdminServer,proto.Message)(proto.Message,error), redirectTo func(*http.Request)string)`——内部 session→CSRF→decode→AuthorizeRule→CheckStatusWrite→invoke→SetFlash(flashFor)→PRG。
- **`svc = "/sydom.admin.v1.AdminService/"`**（`routes_apps.go:16`）。`flashFor` 缺省回退「操作成功」（`flash.go`）。
- **表单 GET 传 CSRF**：`principal, sess, ok := h.requireSession(w,r)` → renderPage data 加 `"CSRF": sess.CSRF`；模板 `<input type="hidden" name="csrf_token" value="{{.CSRF}}">`（见 `members.html`）。
- **checkbox 解析**：未勾 → 不提交 → `r.PostFormValue("x")==""`；勾 → `"on"`。故 `enabled := r.PostFormValue("enabled") != ""`。
- **模板 lint**（`templates_lint_test.go`）：扫所有模板，禁内联 style/script、要 label 关联、单 h1 等。新模板须过。
- **测试基建**：`newConsole(t)(*httptest.Server,*RedisStore,*sql.DB)` 起真依赖（root@sydom/rootsecret 播为超管）；`loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")(client, csrf)`；`dbtest.SetupSchema(t)`；mgmt `accountsSrv(db)`、`cp.WithOperator(ctx,"root")`。
- **零触碰**：`casbin/`·`adminauthz/`·`sidecar/kernel/`·`sidecar/dataperm/`·`mgmt/authz.go` 本片**全零 diff**（无新 RPC、无 ruleTable 改）。

## 文件结构

**新建：** `internal/controlplane/console/routes_idp.go`(+`routes_idp_test.go`)、`internal/controlplane/console/templates/idp.html`。
**修改：** `store/tenant_idp.go`(+test)、`mgmt/sso.go`(+`sso_test.go`)、`console/handler.go`（注册）、`console/flash.go`（flash 文案）。

---

## 任务 1：后端（TenantIdpSecretEnc + ConfigureTenantIdp 空-secret 保留）

**文件：** 修改 `store/tenant_idp.go`(+`tenant_idp_test.go`)、`mgmt/sso.go`(+`sso_test.go`)。

- [ ] **步骤 1：写 mgmt 测试（先失败）** — 追加到 `internal/controlplane/mgmt/sso_test.go`：

```go
// M6-sso-4：编辑时空 client_secret 保留旧密文；首次配置须提供 secret。
func TestConfigureTenantIdp_KeepSecretOnEmptyUpdate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-keep') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	// 首次配置（带 secret）。
	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "s3cr3t", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: false,
	})
	require.NoError(t, err)
	var enc1 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc1))

	// 编辑：空 secret + 切 jit_enabled → 密文不变、jit 变。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)
	var enc2 []byte
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc2, &jit))
	require.Equal(t, enc1, enc2, "空 secret 编辑须保留旧密文")
	require.True(t, jit, "jit_enabled 应已切换")

	// 编辑：带新 secret → 密文变化（轮换）。
	_, err = srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "rotated", Domains: []string{"acme.com"}, Enabled: true, JitEnabled: true,
	})
	require.NoError(t, err)
	var enc3 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc3))
	require.NotEqual(t, enc2, enc3, "带新 secret 须轮换密文")
}

func TestConfigureTenantIdp_FirstConfigRequiresSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-first') RETURNING id`).Scan(&tid))
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.ConfigureTenantIdp(ctx, &adminv1.ConfigureTenantIdpRequest{
		TenantId: uint64(tid), Issuer: "https://idp", ClientId: "cid",
		ClientSecret: "", Domains: []string{"acme.com"}, Enabled: true,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "首次配置空 secret 须 InvalidArgument")
}
```

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/mgmt/ -run 'TestConfigureTenantIdp_KeepSecret|TestConfigureTenantIdp_FirstConfig'`；预期 FAIL（当前空 secret→InvalidArgument，KeepSecret 首个 Configure 用带 secret 会过但第二个空 secret 编辑会被拒→测试红）。
- [ ] **步骤 3：store 加 `TenantIdpSecretEnc`** — 追加到 `internal/controlplane/store/tenant_idp.go`：

```go
// TenantIdpSecretEnc 读租户 IdP 的原始加密 client_secret（密文）。无配置→ok=false。
// 仅供 ConfigureTenantIdp 编辑保留时把旧密文原样回写；从不解密、绝不出控制面（INV-1）。
func TenantIdpSecretEnc(ctx context.Context, ex cp.DBTX, tenantID int64) ([]byte, bool, error) {
	var enc []byte
	err := ex.QueryRowContext(ctx,
		`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tenantID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return enc, true, nil
}
```

- [ ] **步骤 4：`ConfigureTenantIdp` 放宽** — 改 `mgmt/sso.go` `ConfigureTenantIdp`：校验去掉 `r.ClientSecret == ""`；把 encrypt 挪进 tx 后按空/非空分支：

```go
	if r.Issuer == "" || r.ClientId == "" || len(r.Domains) == 0 {
		return nil, status.Error(codes.InvalidArgument, "issuer, client_id, domains required")
	}
	for _, d := range r.Domains {
		if strings.TrimSpace(d) == "" {
			return nil, status.Error(codes.InvalidArgument, "domain must not be empty")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	// client_secret 空=保持既有密文（编辑不改 secret）；非空=加密新值；首次配置须非空。
	var enc []byte
	if r.ClientSecret == "" {
		existing, ok, err := store.TenantIdpSecretEnc(ctx, tx, int64(r.TenantId))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%v", err)
		}
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "client_secret required for first configuration")
		}
		enc = existing
	} else {
		enc, err = crypto.Encrypt(s.masterKey, []byte(r.ClientSecret))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
		}
	}
	if err := store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId),
		r.Issuer, r.ClientId, enc, r.Domains, r.Enabled, r.JitEnabled); err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "domain already claimed by another tenant")
		}
		if isForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
```
审计 diff 的 map 加 `"secret_rotated": r.ClientSecret != ""`（仍不含 secret）。**删除原来 tx 前的 `enc, err := crypto.Encrypt(...)` 块**（已挪入）。

- [ ] **步骤 5：运行确认通过** — `go test ./internal/controlplane/mgmt/ -run 'TestConfigureTenantIdp'`；预期全 PASS（含既有 EncryptsAndGetOmitsSecret/JITRoundtrip 不回归）。
- [ ] **步骤 6：store 单测** — 追加到 `internal/controlplane/store/tenant_idp_test.go`：

```go
func TestTenantIdpSecretEnc(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('se') RETURNING id`).Scan(&tid))
	// 无配置→ok=false。
	_, ok, err := store.TenantIdpSecretEnc(context.Background(), db, tid)
	require.NoError(t, err)
	require.False(t, ok)
	// 有配置→返密文。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://i','cid','\xdead'::bytea)`, tid)
	require.NoError(t, err)
	enc, ok, err := store.TenantIdpSecretEnc(context.Background(), db, tid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte{0xde, 0xad}, enc)
}
```
运行 `go test ./internal/controlplane/store/ -run TestTenantIdpSecretEnc`；预期 PASS。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/store/tenant_idp.go internal/controlplane/store/tenant_idp_test.go internal/controlplane/mgmt/sso.go internal/controlplane/mgmt/sso_test.go
git commit -m "feat(mgmt): ConfigureTenantIdp 空 client_secret 保留旧密文（编辑不改 secret）+ store.TenantIdpSecretEnc（M6-sso-4 T1）"
```

---

## 任务 2：Console IdP 配置页

**文件：** 创建 `console/routes_idp.go`、`console/templates/idp.html`；修改 `console/handler.go`（注册）、`console/flash.go`（文案）；测试 `console/routes_idp_test.go`。

- [ ] **步骤 1：写 Console 测试（先失败）** — `internal/controlplane/console/routes_idp_test.go`：

```go
package console

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func idpGetBody(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestIdPConfigPage_RenderAndSave(t *testing.T) {
	ts, store, db := newConsole(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-ui') RETURNING id`).Scan(&tid))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	base := ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/idp"

	// 未配置 GET：200，表单无 secret。
	body := idpGetBody(t, c, base)
	require.Contains(t, body, "client_id")
	require.NotContains(t, strings.ToLower(body), "s3cr3t")

	// POST 新建（带 secret）。client c 不跟随重定向（loginAndCSRF 已设 ErrUseLastResponse）。
	form := url.Values{"csrf_token": {csrf}, "issuer": {"https://idp"}, "client_id": {"cid"},
		"client_secret": {"s3cr3t"}, "domains": {"acme.com\n  \nfoo.com"}, "enabled": {"on"}, "jit_enabled": {"on"}}
	resp, err := c.PostForm(base, form)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var enc1 []byte
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc1, &jit))
	require.True(t, jit)
	var domainCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&domainCount))
	require.Equal(t, 2, domainCount, "空行须被丢弃（acme.com/foo.com）")

	// GET 已配置：预填 issuer/域，仍无 secret。
	body2 := idpGetBody(t, c, base)
	require.Contains(t, body2, "https://idp")
	require.Contains(t, body2, "acme.com")
	require.NotContains(t, strings.ToLower(body2), "s3cr3t")

	// POST 编辑（空 secret，关 jit）→ 密文保留、jit 关。
	form2 := url.Values{"csrf_token": {csrf}, "issuer": {"https://idp"}, "client_id": {"cid"},
		"client_secret": {""}, "domains": {"acme.com\nfoo.com"}, "enabled": {"on"}}
	resp2, err := c.PostForm(base, form2)
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)

	var enc2 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc2, &jit))
	require.Equal(t, enc1, enc2, "空 secret 编辑须保留密文")
	require.False(t, jit, "jit 应被关闭")
}
```
> `newConsole`/`loginAndCSRF` 见 `handler_test.go`（后者返回的 client 已设 `CheckRedirect=ErrUseLastResponse`，故 PostForm 得 303 不跟随）。

- [ ] **步骤 2：运行确认失败** — `go test ./internal/controlplane/console/ -run TestIdPConfigPage`；预期 FAIL（无路由→404/编译）。
- [ ] **步骤 3：写 `routes_idp.go`**：

```go
package console

import (
	"context"
	"net/http"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

func (h *Handler) registerIdP(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/{tenant_id}/idp", h.idpConfig)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp", h.idpSave)
}

// idpConfig 渲染租户 OIDC IdP 配置表单（读，经 AuthorizeRule scopeTenant）。client_secret 绝不回填（INV-1）。
func (h *Handler) idpConfig(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
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
	h.renderPage(w, r, "idp.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid, "CSRF": sess.CSRF,
		"Configured": resp.Configured, "Issuer": resp.Issuer, "ClientID": resp.ClientId,
		"Domains": strings.Join(resp.Domains, "\n"), "Enabled": resp.Enabled, "JitEnabled": resp.JitEnabled,
	})
}

// idpSave 保存 IdP 配置（写，doWrite 管线）。client_secret 留空=保持不变（后端语义）。
func (h *Handler) idpSave(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"ConfigureTenantIdp",
		func(r *http.Request) (proto.Message, error) {
			tid, err := pathUint64(r, "tenant_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.ConfigureTenantIdpRequest{
				TenantId:     tid,
				Issuer:       strings.TrimSpace(r.PostFormValue("issuer")),
				ClientId:     strings.TrimSpace(r.PostFormValue("client_id")),
				ClientSecret: r.PostFormValue("client_secret"), // 不 trim：secret 原样；空=保持
				Domains:      splitLines(r.PostFormValue("domains")),
				Enabled:      r.PostFormValue("enabled") != "",
				JitEnabled:   r.PostFormValue("jit_enabled") != "",
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.ConfigureTenantIdp(ctx, m.(*adminv1.ConfigureTenantIdpRequest))
		},
		func(r *http.Request) string { return "/tenants/" + r.PathValue("tenant_id") + "/idp" })
}

// splitLines 把 textarea 文本按行拆、trim、去空。
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}
```

- [ ] **步骤 4：写 `templates/idp.html`**（照 `members.html`，过 lint：单 h1、label 关联、csrf 隐藏、无内联 style/script）：

```html
{{define "title"}}企业 SSO · 司域 Console{{end}}
{{define "content"}}
<nav class="breadcrumb" aria-label="面包屑">租户 · 企业 SSO</nav>
<h1>企业 SSO（租户 {{.TenantID}}）</h1>
{{if .Configured}}<p class="hint">已配置。发行方 {{.Issuer}}。留空 client_secret 则保持不变。</p>
{{else}}<p class="hint">尚未配置。首次配置须提供 client_secret。</p>{{end}}
<form method="post" action="/tenants/{{.TenantID}}/idp" class="stacked-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<label>发行方 Issuer <input name="issuer" value="{{.Issuer}}" placeholder="https://idp.example" required></label>
<label>Client ID <input name="client_id" value="{{.ClientID}}" required></label>
<label>Client Secret <input name="client_secret" type="password" placeholder="{{if .Configured}}留空=保持不变{{else}}首次配置必填{{end}}"{{if not .Configured}} required{{end}}></label>
<label>Email 域（一行一个）<textarea name="domains" rows="3" placeholder="acme.com">{{.Domains}}</textarea></label>
<label><input type="checkbox" name="enabled"{{if .Enabled}} checked{{end}}> 启用 SSO 登录</label>
<label><input type="checkbox" name="jit_enabled"{{if .JitEnabled}} checked{{end}}> 启用 JIT 自动开通</label>
<p class="hint">JIT：开启后该 IdP 域下<strong>全新</strong>用户首登自动开通为<strong>零权限</strong>成员。</p>
<button class="btn btn-primary">保存</button>
</form>
{{end}}
```

- [ ] **步骤 5：注册 + flash 文案** — `handler.go` 的 `NewHandler` 在其它 `h.registerX(mux)` 处加 `h.registerIdP(mux)`；`flash.go` 的 `flashMessages` 加 `svc + "ConfigureTenantIdp": "IdP 配置已保存",`。
- [ ] **步骤 6：运行确认通过** — `go test ./internal/controlplane/console/ -run 'TestIdPConfigPage|TestTemplates'`；预期 PASS（含模板 lint）。
- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/routes_idp.go internal/controlplane/console/routes_idp_test.go internal/controlplane/console/templates/idp.html internal/controlplane/console/handler.go internal/controlplane/console/flash.go
git commit -m "feat(console): 租户 OIDC IdP 配置页（GET/POST /tenants/{id}/idp，secret 留空保持）（M6-sso-4 T2）"
```

---

## 任务 3：全局验证 + 变异 + 零触碰核验

- [ ] **步骤 1：全仓测试** — `go test ./...`；预期全绿。
- [ ] **步骤 2：proto 破坏性门** — `make proto-breaking`（本片无 proto 改，仍跑确认）；预期 PASS。
- [ ] **步骤 3：零触碰机器 diff** —

```bash
BASE=origin/main
git diff --stat "$BASE"..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/controlplane/mgmt/authz.go
```
预期：**空输出**（本片零授权核心/ruleTable 改动）。

- [ ] **步骤 4：变异证有齿（改后复原）** — 临时把 `ConfigureTenantIdp` 的空-secret 分支改成恒 `enc, err = crypto.Encrypt(s.masterKey, []byte(r.ClientSecret))`（即删 `if r.ClientSecret == ""` 复用分支）→ `go test ./internal/controlplane/mgmt/ -run TestConfigureTenantIdp_KeepSecret`；预期红（空 secret 编辑后 `Encrypt("")` 使密文变化，`enc1==enc2` 断言失败）。复原后重跑 PASS。
- [ ] **步骤 5：`go vet`** — `go vet ./internal/controlplane/store/ ./internal/controlplane/mgmt/ ./internal/controlplane/console/`；预期无告警。
- [ ] **步骤 6：Commit（仅当步骤 3/4 有修补，否则无独立提交）**。

---

## 自检（规格覆盖 / 占位符 / 类型一致）

**规格覆盖度（对照 spec §2/§6）：** §2.1 TenantIdpSecretEnc+Configure 放宽→T1；§2.2 Console 页 GET/POST→T2；§2.3 INV-1 不显 secret + 模板 lint→T2（`NotContains secret` 断言 + lint）；§2.4 授权核心 zeroChange→T3。§6 INV-1→T1/T2 断言；授权 scopeTenant→复用既有 AuthorizeRule；零触碰→T3；向后兼容→T1（放宽 additive，既有测试不回归）。**全覆盖。**

**占位符扫描：** 无 TODO/待定；每步含可编译代码或精确命令。T2 步骤 1 的测试助手若不存在以既有等价物替换（已注明）。

**类型一致：** `store.TenantIdpSecretEnc(ctx, ex cp.DBTX, tenantID int64)([]byte,bool,error)` 在 T1 定义、mgmt handler 调用；`ConfigureTenantIdpRequest` 字段名（Issuer/ClientId/ClientSecret/Domains/Enabled/JitEnabled）proto 生成一致；Console `svc`/`doWrite`/`renderPage`/`pathUint64`/`sess.CSRF` 均既有。

## 落地顺序 / 依赖

T1→T2→T3。T2 依赖 T1（空-secret 语义）。每任务独立 commit，本地 FF，push origin 待用户定。
