# SP3 司域 Admin Web Console 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给人类运维者一个服务端渲染（`html/template`）的浏览器控制台，对司域全部 27 个 AdminService RPC 做可视化管理；折进控制面进程作第 4 个监听器，复用 SP2 已导出的鉴权尾链，前门换 session cookie。

**架构：** 新包 `internal/controlplane/console`，镜像 `restgw` 形态。每个请求：会话查 Redis→principal →（写则 CSRF 校验）→表单/path→proto（path 权威覆写）→ `mgmt.AuthorizeRule` →（写则 `mgmt.CheckStatusWrite`）→直调 `*AdminServer` →渲染 HTML / PRG 重定向。会话存 Redis（仅 `{principal,csrf}`，**绝不含 secret**）；登录用 operator secret 当密码、`OperatorResolver` 常量时间比对、通用「凭据无效」防枚举。

**技术栈：** Go 1.26、`net/http`（Go 1.22 方法感知 ServeMux + `r.PathValue`）、`html/template`（自动转义）、`embed.FS`、`github.com/redis/go-redis/v9`、`crypto/subtle`、`crypto/rand`、testcontainers（PG+Redis）、`net/http/httptest`、testify、Playwright（走查）。

**全局复用（已存在，勿改语义）：**
- `mgmt.AuthorizeRule(ctx, enf *adminauthz.Enforcer, fullMethod, principal string, req any) (context.Context, error)`
- `mgmt.CheckStatusWrite(ctx, db *sql.DB, fullMethod string, req any) error`
- `mgmt.AdminServer` 的 27 个方法；`adminauthz.NewOperatorResolver(db, masterKey).ResolveSecret(ctx, principal) ([]byte, error)`（未知/停用/解密失败一律 error）。
- `ruleTable` 唯一鉴权真相源（不导出），经 `fullMethod` 字符串间接引用。
- proto 包别名：`adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"`。

**proto 字段速查（写表单用）：**
| Request | 字段（Go 名） |
|---|---|
| CreateRoleRequest | AppId uint64, Code string, Name string |
| DeleteRoleRequest | AppId uint64, RoleId int64 |
| UpsertPermissionRequest | AppId, Code, Resource, Action, Ptype, Name string |
| GrantPermissionRequest | AppId, RoleId int64, PermissionId int64, Eft string |
| RevokePermissionRequest | AppId, RoleId, PermissionId |
| RoleInheritanceRequest | AppId, ChildRoleId, ParentRoleId int64 |
| UserRoleRequest | AppId, UserId string, RoleId int64 |
| UpsertDataPolicyRequest | AppId, Id int64, SubjectType, SubjectId, Resource, Condition, Effect string |
| DeleteDataPolicyRequest | AppId, DataPolicyId int64 |
| CreateApplicationRequest | TenantName, Domain, Name, AppKey string →resp.AppSecret（一次性） |
| SetApplicationStatusRequest | AppId, Status uint32（1=启用，2=停用） |
| CreateOperatorRequest | Principal string →resp.Secret（一次性） |
| SetOperatorStatusRequest | OperatorId int64, Status uint32 |
| CreateAdminRoleRequest | Code, Name string |
| GrantAdminRoleRequest | RoleId int64, Domain, Resource, Action string |
| BindOperatorRoleRequest | OperatorId, RoleId int64, Domain string |

**读 Summary 字段速查（表格模板用）：** RoleSummary{RoleId,Code,Name,Description}；PermissionSummary{PermissionId,Code,Resource,Action,Ptype,Name,Source}；GrantSummary{GrantId,RoleId,PermissionId,Eft}；RoleInheritanceSummary{InheritanceId,ParentRoleId,ChildRoleId}；UserBindingSummary{BindingId,UserId,RoleId}；DataPolicySummary{DataPolicyId,SubjectType,SubjectId,Resource,Condition,Effect,Version}；ApplicationSummary{AppId,Domain,Name,AppKey,Status,CurrentVersion}；OperatorSummary{OperatorId,Principal,Status}；AdminRoleSummary{RoleId,Code,Name}。

---

## 文件结构

```
internal/controlplane/console/
  session.go        Redis 会话存储（Create/Get/Delete/Renew）             — 任务 1
  session_test.go
  auth.go           登录/登出 handler + requireSession + CSRF             — 任务 2
  auth_test.go
  errors.go         code→HTTP + 友好错误页 + Internal 脱敏                 — 任务 3
  render.go         模板解析(embed) + render 助手 + flash/PRG              — 任务 3
  templates/layout.html  base 布局（顶栏 + 左栏上下文 + content block）    — 任务 3
  templates/error.html / login.html                                       — 任务 2/3
  handler.go        Handler 结构 + NewHandler(mux) + 读/写共享助手          — 任务 4
  handler_test.go
  forms.go          表单→proto 解码助手（path 权威覆写）                    — 任务 4
  routes_apps.go    应用列表/详情/状态 + 仪表盘降级                          — 任务 4/8
  routes_rbac.go    角色/权限/授权/继承/绑定 读写                            — 任务 5/6
  routes_datapolicy.go  数据策略 读写（condition canonical）                — 任务 7
  routes_system.go  operators / admin-roles 读写                           — 任务 9
  templates/*.html  每资源页（工作台左栏二级导航 + 表格 + 表单）
  static/app.css    极简 CSS                                              — 任务 3
  static/datapolicy.js  唯一 JS（condition 构建器↔JSON）                   — 任务 7
internal/controlplane/app/config.go   +ConsoleAddr/+ConsoleSessionTTL     — 任务 10
internal/controlplane/app/run.go      +consoleLis 第 4 监听器              — 任务 10
test/e2e/e2e_test.go                  跟随 Run 签名传 nil consoleLis        — 任务 10
test/e2e/console_walkthrough_test.go + WALKTHROUGH.md  Playwright 走查      — 任务 11
```

**职责边界：** `session.go` 只管会话存储；`auth.go` 只管认证/CSRF；`errors.go`/`render.go` 只管输出；`routes_*.go` 按资源域分组，每文件一个明确域。HTTP code 映射在 console 自有（渲染 HTML 页，与 restgw 的 JSON body 不同，非重复）。

---

## 任务 1：Redis 会话存储

**文件：**
- 创建：`internal/controlplane/console/session.go`
- 测试：`internal/controlplane/console/session_test.go`

会话只存 `{Principal, CSRF, CreatedAt}`，**绝不含 secret**。session ID = 32 字节 `crypto/rand` 的 base64url。空闲 TTL，`Get` 命中即续期。

- [ ] **步骤 1：编写失败的测试**

```go
package console

import (
	"context"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, ttl time.Duration) *RedisStore {
	t.Helper()
	addr := dbtest.StartRedis(t)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisStore(rdb, ttl)
}

func TestRedisStore_CreateGetDelete(t *testing.T) {
	s := newTestStore(t, time.Minute)
	ctx := context.Background()

	id, csrf, err := s.Create(ctx, "root@sydom")
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NotEmpty(t, csrf)

	sess, err := s.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "root@sydom", sess.Principal)
	require.Equal(t, csrf, sess.CSRF)

	require.NoError(t, s.Delete(ctx, id))
	_, err = s.Get(ctx, id)
	require.ErrorIs(t, err, ErrNoSession)
}

func TestRedisStore_UnknownID(t *testing.T) {
	s := newTestStore(t, time.Minute)
	_, err := s.Get(context.Background(), "nonexistent")
	require.ErrorIs(t, err, ErrNoSession)
}

func TestRedisStore_Expiry(t *testing.T) {
	s := newTestStore(t, 50*time.Millisecond)
	ctx := context.Background()
	id, _, err := s.Create(ctx, "x")
	require.NoError(t, err)
	time.Sleep(120 * time.Millisecond)
	_, err = s.Get(ctx, id)
	require.ErrorIs(t, err, ErrNoSession)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestRedisStore -v`
预期：编译失败（`RedisStore`/`NewRedisStore`/`ErrNoSession` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

```go
// Package console 是控制面 Admin Web Console（服务端 BFF）：把 AdminService 包成
// html/template 渲染的人面管理界面，复用 mgmt.AuthorizeRule/CheckStatusWrite 鉴权核心。
package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNoSession 表示会话不存在/已过期（fail-close：调用方一律重定向登录）。
var ErrNoSession = errors.New("console: no session")

// Session 是会话状态。绝不含 operator secret。
type Session struct {
	Principal string `json:"principal"`
	CSRF      string `json:"csrf"`
	CreatedAt int64  `json:"created_at"`
}

// RedisStore 以 Redis 为后端的会话存储，键 console:sess:<id>，带空闲 TTL。
type RedisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedisStore(rdb *redis.Client, ttl time.Duration) *RedisStore {
	return &RedisStore{rdb: rdb, ttl: ttl}
}

func (s *RedisStore) key(id string) string { return "console:sess:" + id }

// randToken 返回 32 字节随机的 base64url 串（session ID / CSRF token 共用）。
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create 新建会话，返回 (sessionID, csrfToken)。
func (s *RedisStore) Create(ctx context.Context, principal string) (string, string, error) {
	id, err := randToken()
	if err != nil {
		return "", "", err
	}
	csrf, err := randToken()
	if err != nil {
		return "", "", err
	}
	sess := Session{Principal: principal, CSRF: csrf, CreatedAt: time.Now().Unix()}
	raw, err := json.Marshal(sess)
	if err != nil {
		return "", "", err
	}
	if err := s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err(); err != nil {
		return "", "", err
	}
	return id, csrf, nil
}

// Get 取会话；命中则续期空闲 TTL。未命中返回 ErrNoSession。
func (s *RedisStore) Get(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return Session{}, ErrNoSession
	}
	raw, err := s.rdb.Get(ctx, s.key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return Session{}, err
	}
	_ = s.rdb.Expire(ctx, s.key(id), s.ttl).Err() // 续期，失败不致命
	return sess, nil
}

func (s *RedisStore) Delete(ctx context.Context, id string) error {
	return s.rdb.Del(ctx, s.key(id)).Err()
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run TestRedisStore -v`
预期：PASS（3 个用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/session.go internal/controlplane/console/session_test.go
git commit -m "feat(console): Redis 会话存储（仅存 principal/csrf，绝不含 secret，空闲 TTL）"
```

---

## 任务 2：登录 / 登出 / 会话中间件 / CSRF

**文件：**
- 创建：`internal/controlplane/console/auth.go`、`internal/controlplane/console/templates/login.html`
- 测试：`internal/controlplane/console/auth_test.go`

登录：`OperatorResolver.ResolveSecret(principal)` 取存储密钥，`subtle.ConstantTimeCompare` 比对用户输入；任一失败→**通用「凭据无效」**。验过建会话、写 cookie。`requireSession` 取 cookie→Redis→principal，缺失则 302 `/login`。CSRF：会话内 token，写请求比对表单 `csrf_token`。

本任务先定义 `Handler` 的最小骨架（任务 4 扩充）。`SecretResolver` 用窄接口便于测试注入。

- [ ] **步骤 1：编写失败的测试**

```go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeResolver 实现 secretResolver。
type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, p string) ([]byte, error) {
	s, ok := f[p]
	if !ok {
		return nil, ErrNoSession // 任意 error 即可（登录不区分原因）
	}
	return s, nil
}

func newAuthHandler(t *testing.T) (*Handler, *RedisStore) {
	t.Helper()
	store := newTestStore(t, time.Minute)
	h := &Handler{
		sessions: store,
		resolver: fakeResolver{"root@sydom": []byte("s3cr3t")},
		cookieSecure: false,
	}
	return h, store
}

func TestLogin_Success_SetsCookie(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"root@sydom"}, "secret": {"s3cr3t"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.handleLoginPost(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code) // 302/303 → "/"
	require.Equal(t, "/", w.Header().Get("Location"))
	require.NotEmpty(t, w.Result().Cookies(), "应设置会话 cookie")
	c := w.Result().Cookies()[0]
	require.Equal(t, sessionCookieName, c.Name)
	require.True(t, c.HttpOnly)
}

func TestLogin_WrongSecret_Generic401(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"root@sydom"}, "secret": {"WRONG"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.handleLoginPost(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, w.Result().Cookies(), "失败不得设 cookie")
	require.NotContains(t, w.Body.String(), "WRONG")
}

func TestLogin_UnknownPrincipal_SameGeneric(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"ghost@sydom"}, "secret": {"s3cr3t"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleLoginPost(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code) // 与错密码同一响应，无枚举
}

func TestRequireSession_NoCookie_Redirects(t *testing.T) {
	h, _ := newAuthHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	_, _, ok := h.requireSession(w, req)
	require.False(t, ok)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/login", w.Header().Get("Location"))
}

func TestRequireSession_ValidCookie_OK(t *testing.T) {
	h, store := newAuthHandler(t)
	id, _, _ := store.Create(context.Background(), "root@sydom")
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	w := httptest.NewRecorder()
	principal, sess, ok := h.requireSession(w, req)
	require.True(t, ok)
	require.Equal(t, "root@sydom", principal)
	require.NotEmpty(t, sess.CSRF)
}

func TestCheckCSRF(t *testing.T) {
	h, _ := newAuthHandler(t)
	sess := Session{CSRF: "tok123"}
	good := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{"csrf_token": {"tok123"}}.Encode()))
	good.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.True(t, h.checkCSRF(good, sess))

	bad := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{"csrf_token": {"nope"}}.Encode()))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.False(t, h.checkCSRF(bad, sess))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestLogin|TestRequireSession|TestCheckCSRF' -v`
预期：编译失败（`Handler`/`handleLoginPost`/`requireSession`/`checkCSRF`/`sessionCookieName`/`secretResolver` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

`auth.go`：

```go
package console

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

const sessionCookieName = "sydom_console_session"

// secretResolver 是登录验证所需的窄接口（生产由 *adminauthz.OperatorResolver 满足）。
type secretResolver interface {
	ResolveSecret(ctx context.Context, principal string) ([]byte, error)
}

// Handler 持有 Console 全部依赖（与 gRPC/REST 端共用同一运行时实例）。
type Handler struct {
	srv          *mgmt.AdminServer
	enf          *adminauthz.Enforcer
	db           *sql.DB
	resolver     secretResolver
	sessions     *RedisStore
	logger       *slog.Logger
	cookieSecure bool
}

// handleLoginGet 渲染登录表单。
func (h *Handler) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "login.html", http.StatusOK, map[string]any{"Error": ""})
}

// handleLoginPost 验证 principal+secret（secret 当密码），成功建会话+写 cookie。
func (h *Handler) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	principal := r.FormValue("principal")
	secret := r.FormValue("secret")

	stored, err := h.resolver.ResolveSecret(r.Context(), principal)
	// 通用失败：未知/停用/解密失败/密码错一律同一响应（无枚举 oracle）。
	if err != nil || subtle.ConstantTimeCompare([]byte(secret), stored) != 1 {
		h.renderPage(w, r, "login.html", http.StatusUnauthorized, map[string]any{"Error": "凭据无效"})
		return
	}
	id, _, err := h.sessions.Create(r.Context(), principal)
	if err != nil {
		h.renderError(w, r, codeInternal, "会话创建失败", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: id, Path: "/",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout 销会话 + 过期 cookie。
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = h.sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// requireSession 取 cookie→Redis→principal；缺失则 302 /login 并返回 ok=false。
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (string, Session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return "", Session{}, false
	}
	sess, err := h.sessions.Get(r.Context(), c.Value)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return "", Session{}, false
	}
	return sess.Principal, sess, true
}

// checkCSRF 常量时间比对表单 csrf_token 与会话内 token。
func (h *Handler) checkCSRF(r *http.Request, sess Session) bool {
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(sess.CSRF)) == 1
}
```

> 注：`renderPage`/`renderError`/`codeInternal` 在任务 3 定义。本任务编译依赖任务 3 的 `render.go`/`errors.go` 骨架——**任务 2 与任务 3 可合并提交，或先建任务 3 的空壳**。子代理实现时若遇未定义，先落任务 3 的 `render.go`/`errors.go`/`templates`，再回填本任务。

`templates/login.html`（居中卡片；`{{.Error}}` 自动转义）：

```html
{{define "title"}}登录 · 司域 Console{{end}}
{{define "content"}}
<div class="login-card">
  <h1>司域 Console</h1>
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form method="post" action="/login">
    <label>Principal<input name="principal" autofocus></label>
    <label>Secret<input name="secret" type="password"></label>
    <button type="submit">登录</button>
  </form>
</div>
{{end}}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestLogin|TestRequireSession|TestCheckCSRF' -v`
预期：PASS（6 个用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/auth.go internal/controlplane/console/auth_test.go internal/controlplane/console/templates/login.html
git commit -m "feat(console): 登录(secret当密码/通用401防枚举)/登出/会话中间件/CSRF"
```

---

## 任务 3：错误页 + 渲染脚手架 + base 布局

**文件：**
- 创建：`internal/controlplane/console/errors.go`、`internal/controlplane/console/render.go`、`internal/controlplane/console/templates/layout.html`、`internal/controlplane/console/templates/error.html`、`internal/controlplane/console/static/app.css`
- 测试：`internal/controlplane/console/render_test.go`

`errors.go`：gRPC code→HTTP status + 友好页码名；`Internal/Unknown`→500 通用文案、细节仅进 slog（镜像 restgw 安全口径，但渲染 HTML）。`render.go`：`embed.FS` 解析模板 + `renderPage`/`renderError`/PRG flash 助手。

- [ ] **步骤 1：编写失败的测试**

```go
package console

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHTTPStatusForCode(t *testing.T) {
	require.Equal(t, 403, httpStatusForCode(codes.PermissionDenied))
	require.Equal(t, 404, httpStatusForCode(codes.NotFound))
	require.Equal(t, 409, httpStatusForCode(codes.FailedPrecondition))
	require.Equal(t, 400, httpStatusForCode(codes.InvalidArgument))
	require.Equal(t, 500, httpStatusForCode(codes.Internal))
}

func TestRenderError_InternalScrubbed(t *testing.T) {
	h := &Handler{logger: testLogger(t), templates: mustTemplates()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	err := status.Error(codes.Internal, "constraint admin_operator_pkey violated")
	h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/CreateOperator", err)
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "admin_operator_pkey", "Internal 细节绝不外泄")
	require.Contains(t, w.Body.String(), "internal")
}

func TestRenderError_PermissionDenied(t *testing.T) {
	h := &Handler{logger: testLogger(t), templates: mustTemplates()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	h.renderGRPCError(w, r, "m", status.Error(codes.PermissionDenied, "permission denied"))
	require.Equal(t, 403, w.Code)
}
```

> 测试助手 `testLogger`/`mustTemplates`/`Handler.templates` 在本任务实现中提供（`render.go` 暴露 `mustTemplates()` 解析 embed，`templates` 字段挂到 Handler）。`renderGRPCError` 区别于任务 2 用的 `renderError`(自定义文案)：前者解 gRPC status，后者直给文案——本任务统一实现两者。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestHTTPStatusForCode|TestRenderError' -v`
预期：编译失败（未定义符号）。

- [ ] **步骤 3：编写最少实现代码**

`errors.go`：

```go
package console

import (
	"log/slog"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 内部错误分类常量（renderError 用）。
const codeInternal = codes.Internal

// httpStatusForCode：gRPC code → HTTP status（其余一律 500）。与 restgw 同表，
// 但 console 自有一份（渲染 HTML 页而非 JSON body，职责不同非重复）。
func httpStatusForCode(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.FailedPrecondition:
		return http.StatusConflict
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// renderGRPCError 把 gRPC status 错误渲染成友好 HTML 错误页。
// Unauthenticated → 302 登录；Internal/Unknown(→500) 脱敏：通用文案 + 细节仅进日志。
func (h *Handler) renderGRPCError(w http.ResponseWriter, r *http.Request, method string, err error) {
	st := status.Convert(err)
	hs := httpStatusForCode(st.Code())
	if hs == http.StatusUnauthorized {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	msg := st.Message()
	if hs == http.StatusInternalServerError {
		h.logger.Error("console internal error",
			"method", method, "code", st.Code().String(), "detail", st.Message())
		msg = "internal error"
	}
	h.renderError(w, r, st.Code(), msg, nil)
}

// renderError 渲染错误页（自定义 code/文案；errDetail 仅日志，不入页）。
func (h *Handler) renderError(w http.ResponseWriter, r *http.Request, c codes.Code, msg string, errDetail error) {
	if errDetail != nil {
		h.logger.Error("console error", "code", c.String(), "detail", errDetail)
	}
	h.renderPage(w, r, "error.html", httpStatusForCode(c), map[string]any{"Message": msg, "Code": c.String()})
}

var _ = slog.Default
```

`render.go`：

```go
package console

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// mustTemplates 解析全部模板（含 base 布局）。解析失败 panic（启动期硬错误）。
func mustTemplates() *template.Template {
	return template.Must(template.ParseFS(templatesFS, "templates/*.html"))
}

// renderPage 以 base 布局渲染指定页模板。data 须含页所需键。
// 先渲到 buffer，成功才写 header+body（避免半截页）。
func (h *Handler) renderPage(w http.ResponseWriter, r *http.Request, page string, statusCode int, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	var buf bytes.Buffer
	// 每页模板 define "content"/"title"；layout.html 是入口，{{template "content" .}}。
	tmpl := h.templates.Lookup(page)
	if tmpl == nil {
		h.logger.Error("console template missing", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.templates.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		h.logger.Error("console render", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = buf.WriteTo(w)
}
```

> **模板机制说明（实现者注意）：** 采用「每页文件 `{{define "content"}}…{{end}}` + `{{define "title"}}…{{end}}`，由 `layout.html` 顶层 `{{template "content" .}}` 拼装」。因 `ExecuteTemplate(buf, "layout.html", data)` 会用最后解析到的 `content` 定义，**多页共用一个 `*template.Template` 会发生 `content` 互相覆盖**。解决：每个请求按页 clone——`h.templates.Lookup(page)` 拿到该页关联集，或更稳妥地**每页解析为独立 `*template.Template`**（`template.Must(template.ParseFS(fs, "templates/layout.html", "templates/"+page))`）。实现者改用按页解析的 map：`map[string]*template.Template{ "roles.html": parse(layout+roles), ... }`，`renderPage(page)` 选对应模板 `ExecuteTemplate(buf,"layout.html",data)`。`render_test.go` 的 `mustTemplates()` 相应返回该 map 封装类型。**请在步骤 3 落地此 map 方案，避免 define 覆盖 bug。**

`templates/layout.html`：

```html
{{define "layout.html"}}<!DOCTYPE html>
<html lang="zh">
<head><meta charset="utf-8"><title>{{block "title" .}}司域 Console{{end}}</title>
<link rel="stylesheet" href="/static/app.css"></head>
<body>
{{if .Nav}}
<header class="topbar"><span class="brand">司域 Sydom</span>
  <nav><a href="/" {{if eq .Nav "apps"}}class="active"{{end}}>应用</a>
  <a href="/operators" {{if eq .Nav "system"}}class="active"{{end}}>系统</a></nav>
  <form method="post" action="/logout" class="logout"><button>登出</button></form>
</header>{{end}}
<main>{{block "content" .}}{{end}}</main>
</body></html>{{end}}
```

`templates/error.html`：

```html
{{define "title"}}错误 · 司域 Console{{end}}
{{define "content"}}<div class="error-page"><h2>{{.Code}}</h2><p>{{.Message}}</p>
<a href="/">返回</a></div>{{end}}
```

`static/app.css`：极简（顶栏/侧栏/表格/表单/徽章/错误色）。给出最小骨架即可，约 60 行。

`render_test.go` 顶部助手：

```go
func testLogger(t *testing.T) *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestHTTPStatusForCode|TestRenderError' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/errors.go internal/controlplane/console/render.go internal/controlplane/console/render_test.go internal/controlplane/console/templates internal/controlplane/console/static
git commit -m "feat(console): 错误页(Internal脱敏)+渲染脚手架(embed,按页模板)+base布局"
```

---

## 任务 4：Handler 装配 + 读/写共享助手 + 仪表盘（含降级）

**文件：**
- 创建：`internal/controlplane/console/handler.go`、`internal/controlplane/console/forms.go`、`internal/controlplane/console/routes_apps.go`、`internal/controlplane/console/templates/dashboard.html`
- 测试：`internal/controlplane/console/handler_test.go`

`NewHandler` 注册方法感知路由 + 静态文件。共享助手 `doRead`（会话→授权→invoke→render）与 `doWrite`（会话→CSRF→decode→授权→status闸→invoke→PRG）。仪表盘 `/` 调 ListApplications，`PermissionDenied`→降级渲染「按 app ID 直达」表单（无枚举）。

- [ ] **步骤 1：编写失败的测试**（用 testcontainers PG+Redis 起真 Handler）

```go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// newConsole 起一套真依赖的 Console（root 已播种为超管）+ httptest server。
func newConsole(t *testing.T) (*httptest.Server, *RedisStore) {
	t.Helper()
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mk := bytes.Repeat([]byte{7}, 32)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk, "root@sydom", []byte("rootsecret")))
	resolver, err := adminauthz.NewOperatorResolver(db, mk)
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	rdb := redis.NewClient(&redis.Options{Addr: dbtest.StartRedis(t)})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewRedisStore(rdb, time.Minute)

	h := NewHandler(srv, resolver, enf, db, store, testLogger(t), false)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, store
}

// loginClient 返回带会话 cookie 的 client + 当前 csrf。
func loginClient(t *testing.T, ts *httptest.Server, principal, secret string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"principal": {principal}, "secret": {secret}}
	resp, err := c.PostForm(ts.URL+"/login", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	// 取 csrf：登录后访问任意写页抓 hidden token；这里访问 dashboard 抓不到，
	// 用 store 直接读（测试便利）——见 csrfFor 助手。
	return c, ""
}

func TestDashboard_SuperAdmin_ListsApps(t *testing.T) {
	ts, _ := newConsole(t)
	c, _ := loginClient(t, ts, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// 新库无 app，断言渲染了应用区骨架（标题）。
	body := readBody(t, resp)
	require.Contains(t, body, "应用")
}

func TestDashboard_NoSession_RedirectsLogin(t *testing.T) {
	ts, _ := newConsole(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
}
```

> 实现者补 `readBody`、`import bytes`/`database/sql`/`net/http/cookiejar`。降级用例（受限 operator 看不到 app 列表得到「直达」表单）放在任务 9 之后（需先有建 operator/授权能力造受限身份）——本任务先验证超管 + 无会话两条。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestDashboard -v`
预期：编译失败（`NewHandler` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

`handler.go`：

```go
package console

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// NewHandler 装配 Console 的 ServeMux（方法感知路由 + 静态文件）。
func NewHandler(srv *mgmt.AdminServer, resolver secretResolver, enf *adminauthz.Enforcer,
	db *sql.DB, sessions *RedisStore, logger *slog.Logger, cookieSecure bool) http.Handler {
	h := &Handler{srv: srv, resolver: resolver, enf: enf, db: db,
		sessions: sessions, logger: logger, cookieSecure: cookieSecure, templates: mustTemplates()}
	mux := http.NewServeMux()

	// 认证（无会话亦可访问）。
	mux.HandleFunc("GET /login", h.handleLoginGet)
	mux.HandleFunc("POST /login", h.handleLoginPost)
	mux.HandleFunc("POST /logout", h.handleLogout)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	// 业务路由（每个内部 requireSession）。
	h.registerApps(mux)     // 任务 4/8
	h.registerRBAC(mux)     // 任务 5/6
	h.registerDataPolicy(mux) // 任务 7
	h.registerSystem(mux)   // 任务 9
	return mux
}

// doRead：会话→授权→invoke→render（GET 列表/详情页）。
func (h *Handler) doRead(w http.ResponseWriter, r *http.Request, fullMethod string,
	build func(*http.Request) (proto.Message, error),
	invoke func(context.Context, *mgmt.AdminServer, proto.Message) (proto.Message, error),
	page string, present func(proto.Message, Session) map[string]any) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	msg, err := build(r)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fullMethod, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	resp, err := invoke(ctx, h.srv, msg)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	h.renderPage(w, r, page, http.StatusOK, present(resp, sess))
}

// doWrite：会话→CSRF→decode→授权→status闸→invoke→PRG 重定向（POST 写动作）。
// redirectTo 由调用方据 path 算出目标列表页（PRG）。
func (h *Handler) doWrite(w http.ResponseWriter, r *http.Request, fullMethod string,
	decode func(*http.Request) (proto.Message, error),
	invoke func(context.Context, *mgmt.AdminServer, proto.Message) (proto.Message, error),
	redirectTo func(*http.Request) string) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	msg, err := decode(r)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fullMethod, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, fullMethod, msg); err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	if _, err := invoke(ctx, h.srv, msg); err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	http.Redirect(w, r, redirectTo(r), http.StatusSeeOther)
}

var _ = status.Error
```

`forms.go`（表单/path 解码助手，path 权威）：

```go
package console

import (
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func pathUint64(r *http.Request, key string) (uint64, error) {
	v, err := strconv.ParseUint(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid path %s", key)
	}
	return v, nil
}

func pathInt64(r *http.Request, key string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid path %s", key)
	}
	return v, nil
}

// formInt64 取表单可选 int64（缺=0）。
func formInt64(r *http.Request, key string) (int64, error) {
	s := r.FormValue(key)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid field %s", key)
	}
	return v, nil
}
```

`routes_apps.go`（仪表盘 + 降级；应用建/状态/详情见任务 8 扩充本文件）：

```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const svc = "/sydom.admin.v1.AdminService/"

func (h *Handler) registerApps(mux *http.ServeMux) {
	mux.HandleFunc("GET /", h.dashboard)
	// 任务 8 在此追加：GET /apps/new, POST /apps, GET /apps/{app_id}, POST /apps/{app_id}/status
}

// dashboard：ListApplications；PermissionDenied → 降级渲染「按 app ID 直达」表单（无枚举）。
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	const fm = svc + "ListApplications"
	msg := &adminv1.ListApplicationsRequest{}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			h.renderPage(w, r, "dashboard.html", http.StatusOK,
				map[string]any{"Nav": "apps", "Degraded": true, "CSRF": sess.CSRF})
			return
		}
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListApplications(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "dashboard.html", http.StatusOK, map[string]any{
		"Nav": "apps", "Degraded": false, "Apps": resp.(*adminv1.ListApplicationsResponse).Applications, "CSRF": sess.CSRF})
}

var _ proto.Message = (*adminv1.ListApplicationsRequest)(nil)
```

`templates/dashboard.html`：

```html
{{define "title"}}应用 · 司域 Console{{end}}
{{define "content"}}
<h2>应用</h2>
{{if .Degraded}}
<p class="hint">你没有列出全部应用的权限。直接输入要管理的 App ID：</p>
<form method="get" action="/apps/redirect"><input name="app_id" placeholder="App ID">
<button>前往</button></form>
{{else}}
<a class="btn-primary" href="/apps/new">+ 新建应用</a>
<table><thead><tr><th>ID</th><th>名称</th><th>Domain</th><th>状态</th></tr></thead>
<tbody>{{range .Apps}}<tr><td><a href="/apps/{{.AppId}}/roles">{{.AppId}}</a></td>
<td>{{.Name}}</td><td>{{.Domain}}</td><td>{{if eq .Status 1}}启用{{else}}停用{{end}}</td></tr>{{end}}</tbody></table>
{{end}}
{{end}}
```

> `GET /apps/redirect`（降级直达）：读 `?app_id=` → 302 到 `/apps/{id}/roles`，在任务 8 注册（仅做参数校验+重定向，不查库，无枚举）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run TestDashboard -v`
预期：PASS（超管列表 + 无会话重定向）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/handler.go internal/controlplane/console/forms.go internal/controlplane/console/routes_apps.go internal/controlplane/console/templates/dashboard.html internal/controlplane/console/handler_test.go
git commit -m "feat(console): Handler 装配 + doRead/doWrite 共享管线 + 仪表盘(超管列表/受限降级无枚举)"
```

---

## 任务 5：应用工作台 · 角色 + 权限点（读写）

**文件：**
- 创建：`internal/controlplane/console/routes_rbac.go`、`templates/roles.html`、`templates/permissions.html`、`templates/_appnav.html`（左栏二级导航 partial）
- 测试：扩充 `handler_test.go`

实现角色（List/Create/Delete）+ 权限点（List/Upsert）。建立工作台「左栏二级导航 + 表格 + 内联新增表单」的范式，后续资源照搬。

- [ ] **步骤 1：编写失败的测试**

```go
func TestRoles_CreateThenList(t *testing.T) {
	ts, store := newConsole(t)
	appID := seedAppRow(t, ts) // 经 POST /apps 建一个 app（任务 8 后可用）；本任务用 dbtest.SeedApp 直插
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{"csrf_token": {csrf}, "code": {"manager"}, "name": {"经理"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/roles", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "manager")
	require.Contains(t, body, "经理")
}

func TestRoles_CSRFMissing_Forbidden(t *testing.T) {
	ts, _ := newConsole(t)
	appID := dbtest.SeedApp(t, openDB(t, ts))
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	form := url.Values{"code": {"x"}} // 无 csrf_token
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
```

> 助手 `loginAndCSRF`：登录后从会话存储或一个写页 GET 抓 hidden `csrf_token`。推荐实现：登录后 `store.Get` 拿 CSRF（测试便利，生产从页面隐藏域取）。`seedAppRow`/`openDB` 见实现者补；本任务可用 `dbtest.SeedApp(t, db)` 直插 app 行避免依赖任务 8。

- [ ] **步骤 2：运行测试验证失败** → 编译失败（路由未注册）。

- [ ] **步骤 3：编写最少实现代码**

`routes_rbac.go`（角色 + 权限点；授权/继承/绑定在任务 6 追加到本文件）：

```go
package console

import (
	"context"
	"fmt"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

func (h *Handler) registerRBAC(mux *http.ServeMux) {
	// 角色
	mux.HandleFunc("GET /apps/{app_id}/roles", h.listRoles)
	mux.HandleFunc("POST /apps/{app_id}/roles", h.createRole)
	mux.HandleFunc("POST /apps/{app_id}/roles/{role_id}/delete", h.deleteRole)
	// 权限点
	mux.HandleFunc("GET /apps/{app_id}/permissions", h.listPermissions)
	mux.HandleFunc("POST /apps/{app_id}/permissions", h.upsertPermission)
	// 任务 6：grants / inheritances / bindings
}

func appListRedirect(seg string) func(*http.Request) string {
	return func(r *http.Request) string { return fmt.Sprintf("/apps/%s/%s", r.PathValue("app_id"), seg) }
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	h.doRead(w, r, svc+"ListRoles",
		func(r *http.Request) (proto.Message, error) {
			id, err := pathUint64(r, "app_id")
			return &adminv1.ListRolesRequest{AppId: id}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.ListRoles(ctx, m.(*adminv1.ListRolesRequest))
		},
		"roles.html",
		func(m proto.Message, sess Session) map[string]any {
			return map[string]any{"Nav": "apps", "AppID": r_app(m), "Tab": "roles",
				"Roles": m.(*adminv1.ListRolesResponse).Roles, "CSRF": sess.CSRF}
		})
}
```

> 注：`present` 闭包拿不到 `r`。改法：`doRead` 把 `appID` 作为 present 入参，或 present 直接读已构造的 `msg`（`m.(*…Request).AppId`）。实现者用 `m.(*adminv1.ListRolesResponse)` 无 AppId，故**在 present 里从外层闭包捕获 appID**——将 appID 在 build 后存入一个局部并由 present 闭包捕获。简洁做法：listRoles 内联不走 doRead 的 present 签名，改为：

```go
func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok { return }
	appID, err := pathUint64(r, "app_id")
	if err != nil { h.renderGRPCError(w, r, svc+"ListRoles", err); return }
	msg := &adminv1.ListRolesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, msg)
	if err != nil { h.renderGRPCError(w, r, svc+"ListRoles", err); return }
	resp, err := h.srv.ListRoles(ctx, msg)
	if err != nil { h.renderGRPCError(w, r, svc+"ListRoles", err); return }
	h.renderPage(w, r, "roles.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "roles", "Roles": resp.Roles, "CSRF": sess.CSRF})
}
```

> **决定：读页一律内联此 6 行范式**（比 doRead 的 present 签名更直白、可捕获 appID），`doRead` 仅用于无需 appID 上下文的简单读。`doWrite` 仍统一用于所有写（不需要 present）。后续资源读页照此内联范式。

```go
func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"CreateRole",
		func(r *http.Request) (proto.Message, error) {
			id, err := pathUint64(r, "app_id")
			return &adminv1.CreateRoleRequest{AppId: id, Code: r.FormValue("code"), Name: r.FormValue("name")}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.CreateRole(ctx, m.(*adminv1.CreateRoleRequest))
		},
		appListRedirect("roles"))
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"DeleteRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil { return nil, err }
			roleID, err := pathInt64(r, "role_id")
			return &adminv1.DeleteRoleRequest{AppId: appID, RoleId: roleID}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteRole(ctx, m.(*adminv1.DeleteRoleRequest))
		},
		appListRedirect("roles"))
}

// listPermissions / upsertPermission 同范式：
// upsertPermission decode：CreateRole 同样取 app_id + Code/Resource/Action/Ptype/Name 表单字段。
```

`templates/_appnav.html`（左栏二级导航，所有工作台页 include）：

```html
{{define "appnav"}}
<aside class="appnav"><div class="appname">App #{{.AppID}}</div>
<a href="/apps/{{.AppID}}/roles" {{if eq .Tab "roles"}}class="active"{{end}}>角色</a>
<a href="/apps/{{.AppID}}/permissions" {{if eq .Tab "permissions"}}class="active"{{end}}>权限点</a>
<a href="/apps/{{.AppID}}/grants" {{if eq .Tab "grants"}}class="active"{{end}}>授权</a>
<a href="/apps/{{.AppID}}/inheritances" {{if eq .Tab "inheritances"}}class="active"{{end}}>继承</a>
<a href="/apps/{{.AppID}}/bindings" {{if eq .Tab "bindings"}}class="active"{{end}}>用户</a>
<a href="/apps/{{.AppID}}/data-policies" {{if eq .Tab "datapolicies"}}class="active"{{end}}>数据策略</a>
</aside>{{end}}
```

`templates/roles.html`（工作台：左栏 + 内容表格 + 内联新增表单）：

```html
{{define "title"}}角色 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section><h2>角色</h2>
<form method="post" action="/apps/{{.AppID}}/roles" class="inline-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input name="code" placeholder="code" required><input name="name" placeholder="名称">
<button>+ 新建角色</button></form>
<table><thead><tr><th>ID</th><th>Code</th><th>名称</th><th></th></tr></thead><tbody>
{{range .Roles}}<tr><td>{{.RoleId}}</td><td>{{.Code}}</td><td>{{.Name}}</td>
<td><form method="post" action="/apps/{{$.AppID}}/roles/{{.RoleId}}/delete" onsubmit="return confirm('删除？')">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}"><button class="danger">删除</button></form></td></tr>{{end}}
</tbody></table></section></div>{{end}}
```

`templates/permissions.html`：同结构，表单字段 code/resource/action/ptype/name，表格列 PermissionId/Code/Resource/Action/Ptype/Name/Source。

> **模板注册：** 任务 3 的按页解析 map 需把 `_appnav.html`（partial）一并解析进每个工作台页的模板集：`parse(layout + _appnav + roles)`。实现者更新 `mustTemplates()` 的 map 构造，工作台页都带上 `_appnav.html`。

- [ ] **步骤 2/4：运行测试验证**

运行：`go test ./internal/controlplane/console/ -run 'TestRoles' -v`
预期：步骤 2 FAIL（未注册）→ 步骤 4 PASS（建后列出含 manager/经理；缺 CSRF 得 403）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_rbac.go internal/controlplane/console/templates/roles.html internal/controlplane/console/templates/permissions.html internal/controlplane/console/templates/_appnav.html internal/controlplane/console/handler_test.go
git commit -m "feat(console): 应用工作台角色+权限点读写(左栏二级导航范式/内联表单/CSRF/PRG)"
```

---

## 任务 6：应用工作台 · 授权 + 角色继承 + 用户绑定（读写）

**文件：**
- 修改：`internal/controlplane/console/routes_rbac.go`（追加 6 个写 + 3 个读 handler 与路由）
- 创建：`templates/grants.html`、`templates/inheritances.html`、`templates/bindings.html`
- 测试：扩充 `handler_test.go`

按任务 5 范式实现：授权（ListGrants/GrantPermission/RevokePermission）、继承（ListRoleInheritances/AddRoleInheritance/RemoveRoleInheritance）、绑定（ListUserBindings/BindUserRole/UnbindUserRole）。

**路由注册（追加到 `registerRBAC`）：**
```
GET  /apps/{app_id}/grants                              listGrants
POST /apps/{app_id}/grants                              grantPermission   (form: role_id, permission_id, eft)
POST /apps/{app_id}/grants/revoke                       revokePermission  (form: role_id, permission_id)
GET  /apps/{app_id}/inheritances                        listInheritances
POST /apps/{app_id}/inheritances                        addInheritance    (form: child_role_id, parent_role_id)
POST /apps/{app_id}/inheritances/remove                 removeInheritance (form: child_role_id, parent_role_id)
GET  /apps/{app_id}/bindings                            listBindings
POST /apps/{app_id}/bindings                            bindUser          (form: user_id, role_id)
POST /apps/{app_id}/bindings/unbind                     unbindUser        (form: user_id, role_id)
```

**decode 映射（全部 path app_id 权威 + 表单字段）：**
- grantPermission → `&adminv1.GrantPermissionRequest{AppId, RoleId:form, PermissionId:form, Eft:form("eft")}`（eft 空→后端按 allow）
- revokePermission → `&adminv1.RevokePermissionRequest{AppId, RoleId:form, PermissionId:form}`
- addInheritance → `&adminv1.RoleInheritanceRequest{AppId, ChildRoleId:form, ParentRoleId:form}`
- removeInheritance → 同上消息（Remove 用同一 Request 类型）
- bindUser → `&adminv1.UserRoleRequest{AppId, UserId:form, RoleId:form}`
- unbindUser → 同上消息

invoke 各调对应 `s.GrantPermission/RevokePermission/AddRoleInheritance/RemoveRoleInheritance/BindUserRole/UnbindUserRole`，redirect 各回 `appListRedirect("grants"|"inheritances"|"bindings")`。

读页内联范式（同任务 5）：listGrants 支持可选 `?role_id=` 过滤（`formInt64` 取 query 用 `r.URL.Query().Get`）；listBindings 支持可选 `?user_id=`。模板表格列对应 Summary 字段。授权页新增表单的 role/permission 用 `<input>` 数字 ID（首版不做下拉联动，YAGNI；运维从权限点/角色页查 ID）。

- [ ] **步骤 1：编写失败的测试**（关键 3 条 + 1 安全条）

```go
func TestGrants_GrantThenList(t *testing.T) { /* 建角色+权限点后授权，列表含该行 */ }
func TestBindings_BindThenList(t *testing.T) { /* 绑定 user→role 后列表含 */ }
func TestInheritances_AddThenList(t *testing.T) { /* 加继承后列表含 */ }

func TestGrants_CrossApp_Forbidden(t *testing.T) {
	// 登录后对一个 root 无该域权限的 app 发 POST grant → 403（AuthorizeRule 拒）。
	// root 是 * 域超管，故构造一个「停用了 root 在该域」的情形不易；
	// 改为：用任务 9 能力建一个仅 app A 域权限的 operator，对 app B 发请求 → 403。
	// 本任务先放 happy path 三条 + CSRF 缺失 403；跨域用例移至任务 9 安全矩阵。
}
```

- [ ] **步骤 2-4：** 实现并跑 `go test ./internal/controlplane/console/ -run 'TestGrants|TestBindings|TestInheritances' -v`，先 FAIL 后 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_rbac.go internal/controlplane/console/templates/grants.html internal/controlplane/console/templates/inheritances.html internal/controlplane/console/templates/bindings.html internal/controlplane/console/handler_test.go
git commit -m "feat(console): 应用工作台授权/继承/用户绑定读写(同范式/path权威/CSRF/PRG)"
```

---

## 任务 7：数据策略（list + upsert + delete）+ condition 构建器 JS

**文件：**
- 创建：`internal/controlplane/console/routes_datapolicy.go`、`templates/datapolicies.html`、`static/datapolicy.js`
- 测试：扩充 `handler_test.go`

condition 字段始终以原始 JSON 串提交（canonical / 无 JS 基线）；可视化构建器是其上的渐进增强（唯一 JS）。两模式都只提交 `condition`，服务端解码路径不变（后端 `dataperm` fail-close 校验）。

**路由：**
```
GET  /apps/{app_id}/data-policies                    listDataPolicies   (可选 ?resource=)
POST /apps/{app_id}/data-policies                    upsertDataPolicy   (form: id, subject_type, subject_id, resource, condition, effect)
POST /apps/{app_id}/data-policies/{id}/delete        deleteDataPolicy
```

**decode：**
- upsertDataPolicy → `&adminv1.UpsertDataPolicyRequest{AppId, Id:formInt64("id"), SubjectType:form, SubjectId:form, Resource:form, Condition:form("condition"), Effect:form("effect")}`（Id=0 新增）。**condition 原样透传**（不在 console 解析校验，交后端 fail-close）。
- deleteDataPolicy → `&adminv1.DeleteDataPolicyRequest{AppId, DataPolicyId:pathInt64("id")}`，redirect `appListRedirect("data-policies")`。

- [ ] **步骤 1：编写失败的测试**

```go
func TestDataPolicy_UpsertRawJSON_ThenList(t *testing.T) {
	ts, store := newConsole(t)
	appID := dbtest.SeedApp(t, openDB(t, ts))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	cond := `{"op":"and","children":[{"field":"dept","op":"eq","value":"$user.dept"}]}`
	form := url.Values{"csrf_token": {csrf}, "id": {"0"}, "subject_type": {"role"},
		"subject_id": {"clerk"}, "resource": {"order"}, "condition": {cond}, "effect": {"allow"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	page := readBody(t, mustGet(t, c, ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID)))
	require.Contains(t, page, "order")
	require.Contains(t, page, "$user.dept")
}

func TestDataPolicy_InvalidCondition_FailClose(t *testing.T) {
	// condition 非法 JSON → 后端 InvalidArgument → 400，列表不新增。
	ts, store := newConsole(t)
	appID := dbtest.SeedApp(t, openDB(t, ts))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	form := url.Values{"csrf_token": {csrf}, "id": {"0"}, "subject_type": {"role"},
		"subject_id": {"clerk"}, "resource": {"order"}, "condition": {"{not valid"}, "effect": {"allow"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
```

- [ ] **步骤 2-4：** 实现 `routes_datapolicy.go`（同范式）+ 模板 + JS，跑 `go test ./internal/controlplane/console/ -run TestDataPolicy -v`，先 FAIL 后 PASS。

`templates/datapolicies.html` 要点：表格列 DataPolicyId/SubjectType/SubjectId/Resource/Effect/Condition + 删除按钮；新增表单含隐藏 `id`、subject_type/subject_id/resource/effect 下拉与输入、`condition` 区域分两块：默认 `<div id="builder">`（JS 渐进增强）+ `<textarea name="condition" id="cond-json">`（canonical，`noscript`/专业模式可见）；`<script src="/static/datapolicy.js"></script>`。

`static/datapolicy.js` 要点（纯 vanilla，无网络）：初始化时若 JS 启用则显示构建器、隐藏 textarea，提供「专业模式」切换；构建器增删条件行、选 AND/OR、字段/算子/值；提交前 `serialize()` 把构建器状态写回 `#cond-json` 的 value（确保始终提交合法 `condition`）。**关 JS 时 textarea 保持可见可用**（noscript 基线）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_datapolicy.go internal/controlplane/console/templates/datapolicies.html internal/controlplane/console/static/datapolicy.js internal/controlplane/console/handler_test.go
git commit -m "feat(console): 数据策略读写(condition原始JSON canonical/无JS基线 + 构建器渐进增强)"
```

---

## 任务 8：应用管理（建应用一次性 secret / 状态切换 / 详情 / 降级直达）

**文件：**
- 修改：`internal/controlplane/console/routes_apps.go`（追加 4 路由）
- 创建：`templates/app_new.html`、`templates/app_created.html`（一次性 secret）、`templates/app_status.html` 或并入详情
- 测试：扩充 `handler_test.go`

**路由（追加到 `registerApps`）：**
```
GET  /apps/new                  appNewForm
POST /apps                      createApp     → 渲染 app_created.html 显示一次性 app_secret（不 PRG）
GET  /apps/redirect             appRedirect   (?app_id= → 302 /apps/{id}/roles，仅校验，无枚举)
POST /apps/{app_id}/status      setAppStatus  (form: status ∈ {1,2})
```

> **一次性 secret 特例（重要）**：CreateApplication 响应含明文 `app_secret`（服务端只存加密）。PRG 重定向会丢失它。故 `createApp` **不走 doWrite 的 PRG**：会话+CSRF+授权后直调 `s.CreateApplication`，成功渲染 `app_created.html` 一次性展示 `app_secret`，附「立即保存，不再显示」。**console 绝不记录/落盘该 secret**（不进 slog）。

```go
func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok { return }
	if !h.checkCSRF(r, sess) { h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil); return }
	const fm = svc + "CreateApplication"
	msg := &adminv1.CreateApplicationRequest{
		TenantName: r.FormValue("tenant_name"), Domain: r.FormValue("domain"),
		Name: r.FormValue("name"), AppKey: r.FormValue("app_key")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil { h.renderGRPCError(w, r, fm, err); return }
	resp, err := h.srv.CreateApplication(ctx, msg)
	if err != nil { h.renderGRPCError(w, r, fm, err); return }
	cr := resp.(*adminv1.CreateApplicationResponse)
	h.renderPage(w, r, "app_created.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": cr.AppId, "AppSecret": cr.AppSecret}) // 一次性展示，绝不日志
}
```

`setAppStatus` 走 doWrite（path app_id 权威，form status；redirect 回 `/` 或 `/apps/{id}/roles`）。`appRedirect`：`strconv.ParseUint(?app_id)` 校验后 302，失败回 `/` —— 不查库（无枚举）。

- [ ] **步骤 1：编写失败的测试**

```go
func TestCreateApp_ShowsOneTimeSecret(t *testing.T) {
	ts, store := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	form := url.Values{"csrf_token": {csrf}, "tenant_name": {"acme"}, "domain": {"acme"},
		"name": {"acme-app"}, "app_key": {"ak_acme"}}
	resp, err := c.PostForm(ts.URL+"/apps", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode) // 非 PRG：直接渲染 secret 页
	body := readBody(t, resp)
	require.Contains(t, body, "app_secret") // 页面标注 + 实际值（实际值非空）
}

func TestSetAppStatus_Disable(t *testing.T) {
	// 建 app → POST status=2 → ListApplications 显示停用。
}
```

- [ ] **步骤 2-4：** 实现并跑 `go test ./internal/controlplane/console/ -run 'TestCreateApp|TestSetAppStatus' -v`，先 FAIL 后 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_apps.go internal/controlplane/console/templates/app_new.html internal/controlplane/console/templates/app_created.html internal/controlplane/console/handler_test.go
git commit -m "feat(console): 应用管理(建应用一次性secret不PRG/状态切换/降级直达无枚举)"
```

---

## 任务 9：系统域（operators / admin-roles）+ 安全矩阵

**文件：**
- 创建：`internal/controlplane/console/routes_system.go`、`templates/operators.html`、`templates/operator_created.html`（一次性 secret）、`templates/admin_roles.html`
- 测试：扩充 `handler_test.go`（含跨域 403、降级无枚举）

**路由（`registerSystem`）：**
```
GET  /operators                       listOperators
GET  /operators/new                   operatorNewForm
POST /operators                       createOperator   → operator_created.html 一次性 secret（不 PRG）
POST /operators/{operator_id}/status  setOperatorStatus (form: status)
POST /operators/{operator_id}/roles   bindOperatorRole  (form: role_id, domain)
GET  /admin-roles                     listAdminRoles
POST /admin-roles                     createAdminRole   (form: code, name)
POST /admin-roles/{role_id}/grants    grantAdminRole    (form: domain, resource, action)
```

`createOperator` 同任务 8 一次性 secret 特例（响应 `Secret` 直接展示，不 PRG，不日志）。其余写走 doWrite，redirect 回 `/operators` 或 `/admin-roles`。读页内联范式。`Nav:"system"`。

**安全矩阵测试（本任务关键）：**

```go
// 造一个仅 app A 域有权的受限 operator，验证：
// 1. 它登录后访问 /（ListApplications system 域）→ 降级页（无枚举，不泄露 app 列表）。
// 2. 它对 app A 能建角色（200/PRG）。
// 3. 它对 app B 建角色 → 403。
// 4. 它访问 /operators（system 域）→ 403。
func TestSecurityMatrix_LimitedOperator(t *testing.T) {
	ts, store := newConsole(t)
	db := openDB(t, ts)
	root, csrfRoot := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	appA := dbtest.SeedApp(t, db) // 或经 POST /apps
	appB := dbtest.SeedApp(t, db)

	// root 经 console 建 operator "ops@a" + admin-role 授予其 appA 域 role:create 等，绑定。
	// （用 POST /operators, /admin-roles, /admin-roles/{id}/grants, /operators/{id}/roles）
	opSecret := createLimitedOperator(t, ts, root, csrfRoot, "ops@a", domainOf(appA))

	c, csrf := loginAndCSRF(t, ts, store, "ops@a", opSecret)

	// 1. 降级
	dash := mustGet(t, c, ts.URL+"/")
	require.Contains(t, readBody(t, dash), "App ID") // 降级直达表单，非应用列表
	// 2. appA 建角色成功
	r1, _ := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appA),
		url.Values{"csrf_token": {csrf}, "code": {"r1"}})
	require.Equal(t, http.StatusSeeOther, r1.StatusCode)
	// 3. appB 建角色 403
	r2, _ := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appB),
		url.Values{"csrf_token": {csrf}, "code": {"r2"}})
	require.Equal(t, http.StatusForbidden, r2.StatusCode)
	// 4. system 域 403
	r3 := mustGet(t, c, ts.URL+"/operators")
	require.Equal(t, http.StatusForbidden, r3.StatusCode)
}
```

> `createLimitedOperator`/`domainOf` 实现者补：经 console 写接口造身份（CreateOperator 拿一次性 secret、CreateAdminRole、GrantAdminRole 给 `domainOf(appA)` 域 `role:create`/`role:read`、BindOperatorRole）。`domainOf(id)=strconv.FormatInt(id,10)`（= `mgmt.DomainOfAppID`）。

- [ ] **步骤 2-4：** 实现并跑 `go test ./internal/controlplane/console/ -run 'TestOperators|TestAdminRoles|TestSecurityMatrix' -v`，先 FAIL 后 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_system.go internal/controlplane/console/templates/operators.html internal/controlplane/console/templates/operator_created.html internal/controlplane/console/templates/admin_roles.html internal/controlplane/console/handler_test.go
git commit -m "feat(console): 系统域operators/admin-roles读写 + 安全矩阵(跨域403/system域闸/降级无枚举)"
```

---

## 任务 10：配置 + run.go 第 4 监听器 + e2e 签名修复 + 全仓兜底

**文件：**
- 修改：`internal/controlplane/app/config.go`、`internal/controlplane/app/run.go`、`test/e2e/e2e_test.go`、`cmd/sydom-controlplane/config.example.yaml`（若存在）
- 测试：扩充 `internal/controlplane/app` 既有测试 + 全仓 `go vet`

`config.go` 加 `ConsoleAddr`（`console_addr`，非必填，不进 required 校验）+ `ConsoleSessionTTL`（默认 30m）+ `ConsoleCookieInsecure`。`run.go` 加 `consoleLis net.Listener` 形参，nil 则不起；起则建 `RedisStore`+`console.NewHandler`（复用同一 adminSrv/enf/operatorResolver/db/rdb），launch + 优雅 Shutdown，`errCh` 容量 +1。

- [ ] **步骤 1：编写失败的测试**

```go
// config_test.go：console_addr 可缺省（不报错），TTL 默认 30m。
func TestLoadConfig_ConsoleOptional(t *testing.T) {
	// 写一个不含 console_addr 的 YAML + 必需 env → LoadConfig 成功，cfg.ConsoleAddr=="".
	// 写一个含 console_addr/console_session_ttl 的 → 解析正确。
}
```

- [ ] **步骤 2：运行验证失败**：`go test ./internal/controlplane/app/ -run TestLoadConfig_ConsoleOptional -v` → 编译失败（字段未定义）。

- [ ] **步骤 3：实现**

`config.go`：`Config` 加 `ConsoleAddr string`、`ConsoleSessionTTL time.Duration`、`ConsoleCookieInsecure bool`；`fileConfig` 加 `ConsoleAddr string yaml:"console_addr"`、`ConsoleSessionTTL string yaml:"console_session_ttl"`、`ConsoleCookieInsecure bool yaml:"console_cookie_insecure"`；`LoadConfig` 填充（TTL 用 `parseDurationDefault(fc.ConsoleSessionTTL, 30*time.Minute)`），**不加入 required 循环**。

`run.go`：

```go
func Run(ctx context.Context, cfg Config, adminLis, syncLis, restLis, consoleLis net.Listener, logger *slog.Logger) error {
	// …既有…（errCh 容量 5→6）
	var consoleSrv *http.Server
	if consoleLis != nil {
		store := console.NewRedisStore(rdb, cfg.ConsoleSessionTTL)
		consoleSrv = &http.Server{Handler: console.NewHandler(
			adminSrv, operatorResolver, enforcer, db, store, logger, !cfg.ConsoleCookieInsecure)}
		logger.Info("control plane Console enabled", "console_addr", consoleLis.Addr().String())
		launch("console-serve", func() error {
			if e := consoleSrv.Serve(consoleLis); e != nil && !errors.Is(e, http.ErrServerClosed) {
				return e
			}
			return nil
		})
	}
	// …<-runCtx.Done() 后，restSrv.Shutdown 旁加 consoleSrv.Shutdown(5s)…
}
```

`Main()`：`cfg.ConsoleAddr != ""` 才 `net.Listen` 建 `consoleLis`，调 `Run(..., restLis, consoleLis, logger)`。

`test/e2e/e2e_test.go:60`：`go func() { _ = cpapp.Run(ctx, cfg, adminLis, syncLis, nil, nil, logger) }()`（restLis、consoleLis 均 nil，e2e 不起这两面，向后兼容）。

- [ ] **步骤 4：运行验证 + 全仓兜底（关键）**

```bash
go test ./internal/controlplane/app/ -run TestLoadConfig_ConsoleOptional -v   # PASS
go vet ./...        # 全仓编译所有测试文件——捕获跨包签名断裂（SP2 任务 8 教训：go build 不编译测试）
go build ./...
```
预期：`go vet ./...` 干净（确认 e2e 已跟随新签名）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/app/config.go internal/controlplane/app/run.go internal/controlplane/app/config_test.go test/e2e/e2e_test.go
git commit -m "feat(app): 控制面第4监听器Console(可选console_addr/复用同实例/优雅关闭) + e2e跟随签名传nil"
```

---

## 任务 11：Playwright 走查 + 全量验证 + 收尾

**文件：**
- 创建：`test/e2e/browser/CONSOLE_WALKTHROUGH.md`（+ 截图）
- 验证：全仓门禁

- [ ] **步骤 1：起真控制面（含 Console）** —— 用 `deploy/` compose 或本地起 `cmd/sydom-controlplane`（配 `console_addr`、`console_cookie_insecure: true` 走 loopback http），播种 root。

- [ ] **步骤 2：Playwright 走查并截图**（照 Demo `WALKTHROUGH.md` 先例）：
  1. 访问 `/` → 302 登录页；用 root 登录 → 进应用区。
  2. 新建应用 → 一次性 secret 页（截图，验证「不再显示」文案）。
  3. 进 app 工作台：建角色 → 建权限点 → 授权 → 绑用户 → 数据策略（可视化建一条 + 切专业模式看 JSON）。
  4. 系统区：建 operator（一次性 secret）→ 建 admin-role → 授权 → 绑定。
  5. 用受限 operator 登录 → 仪表盘降级（截图，无应用列表）→ 撞 403 页（截图）。
  6. 登出 → 回登录页；带旧 cookie 访问 `/` → 302 登录（会话已销）。

- [ ] **步骤 3：写 `CONSOLE_WALKTHROUGH.md`** 串起截图与说明（功能可视化、降级无枚举、CSRF、登出失效）。

- [ ] **步骤 4：全量验证门禁**

```bash
gofmt -l internal/controlplane/console internal/controlplane/app   # 空
go vet ./...                                                        # 干净
go build ./...
make proto-check                                                   # 无 proto 改动，应无漂移
go test ./internal/controlplane/console/ -count=1
go test ./internal/controlplane/console/ -race -count=1            # 会话/渲染并发安全
go test ./internal/controlplane/app/ -count=1
go test ./...                                                       # 全仓回归
```
预期：全绿。

- [ ] **步骤 5：Commit**

```bash
git add test/e2e/browser/CONSOLE_WALKTHROUGH.md
git commit -m "test(console): Playwright 真浏览器走查(登录/全资源管理/降级/CSRF/登出) + 全量验证绿"
```

---

## 收尾交接

全部任务完成后，用 `finishing-a-development-branch` 收尾（合并/PR/清理）。最终独立整体评审（建议 opus）逐条核验安全不变量：
1. 会话存储**绝不含 secret**（grep `session.go`/Redis 写入仅 principal/csrf）。
2. 三方（Console/REST/gRPC）**共用** `AuthorizeRule`/`CheckStatusWrite`/`ruleTable`（无旁路）。
3. 管线顺序 **认证→授权→status 闸**（status 闸在授权后）。
4. **path 权威覆写**表单 app_id；跨域 403（安全矩阵已证）。
5. 登录**无枚举 oracle**（通用「凭据无效」）+ 仪表盘**降级无枚举**。
6. **CSRF 全 POST 覆盖**；一次性 secret **不 PRG、不日志、不落盘**。
7. `Internal/Unknown` **脱敏**（细节仅 slog）。

---

## 自检（规格覆盖度对照）

- 规格 §4 架构（第 4 监听器/进程内直调/复用实例）→ 任务 10。✅
- §5 认证/会话/CSRF/登出 → 任务 1、2。✅
- §6.1-6.2 IA 骨架/页型 → 任务 3（layout）、4（dashboard）、5（工作台范式）。✅
- §6.3 路由表 27 RPC → 任务 4（ListApplications）、5（角色/权限）、6（授权/继承/绑定）、7（数据策略）、8（应用管理）、9（系统域）。逐 RPC 已映射。✅
- §6.4 仪表盘降级无枚举 → 任务 4 + 安全矩阵 任务 9。✅
- §6.5 导航不预过滤/enforce-on-access → 任务 9 安全矩阵（system 域 403）。✅
- §6.6 condition 可视化+专业模式 → 任务 7。✅
- §7 读/写管线 → 任务 4 doRead/doWrite。✅
- §8 渲染/embed/零JS基线 → 任务 3 + 任务 7（唯一 JS）。✅
- §9 安全不变量 → 贯穿；收尾整体评审逐条核。✅
- §10 错误脱敏 → 任务 3。✅
- §11 测试（httptest 骨干 + Playwright）→ 任务 1-9 httptest、任务 11 Playwright。✅
- §12 包结构/接线/e2e 修复 → 文件结构表 + 任务 10。✅
- §13 配置 → 任务 10。✅

**一次性 secret 显示**（CreateApplication/CreateOperator 响应明文）规格未显式列出，计划任务 8/9 已补为专门处理（不 PRG、不日志），并在收尾不变量 6 加守。

**类型一致性核对**：`Handler`/`RedisStore`/`Session`/`secretResolver`/`doRead`/`doWrite`/`renderPage`/`renderGRPCError`/`renderError`/`requireSession`/`checkCSRF`/`svc`/`appListRedirect`/`pathUint64`/`pathInt64`/`formInt64` 跨任务命名一致；proto 字段名对照速查表。模板 define 覆盖风险已在任务 3 用「按页解析 map」消解。
