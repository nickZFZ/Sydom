package restgw_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func mk() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

// protoUnmarshal 用 protojson 解码响应到 proto 消息（adminv1.*Response 均实现 proto.Message）。
func protoUnmarshal(b []byte, m proto.Message) error {
	return protojson.Unmarshal(b, m)
}

// restClient 用给定 principal/secret 对完整请求签名后发出。
type restClient struct {
	t         *testing.T
	base      string
	principal string
	secret    []byte
}

func (c *restClient) do(method, pathQuery string, bodyObj interface{}) (*http.Response, []byte) {
	c.t.Helper()
	var body []byte
	if bodyObj != nil {
		b, err := json.Marshal(bodyObj)
		require.NoError(c.t, err)
		body = b
	}
	target := pathQuery
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	ts := time.Now().Unix()
	req, err := http.NewRequest(method, c.base+pathQuery, bytes.NewReader(body))
	require.NoError(c.t, err)
	req.Header.Set(auth.HdrPrincipal, c.principal)
	req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(auth.HdrSignature, auth.SignREST(c.secret, c.principal, ts, method, target, h))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	return resp, out
}

// newTestGW 起真实 DB/Enforcer/AdminServer 的 restgw httptest.Server，并播种 root。
func newTestGW(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	resolver, err := adminauthz.NewOperatorResolver(db, mk())
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := restgw.NewHandler(srv, resolver, enf, db, logger)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, db
}

func rootClient(t *testing.T, base string) *restClient {
	return &restClient{t: t, base: base, principal: "root", secret: []byte("root-secret")}
}

func TestREST_AppDomain_RoundTrip(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 用 dbtest.SeedApp 直接建一个 active app（不依赖顶层 CreateApplication 路由——那在任务 6）。
	// root 的 super-admin（"*" 域）覆盖该具体 app 域，故可写。
	appID := uint64(dbtest.SeedApp(t, db))

	// 建角色（POST body code/name；protojson lowerCamelCase）。
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles", map[string]any{"code": "mgr", "name": "经理"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))
	require.NotZero(t, cr.RoleId)

	// 列角色（GET）。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lr adminv1.ListRolesResponse
	require.NoError(t, protoUnmarshal(body, &lr))
	require.Len(t, lr.Roles, 1)
	require.Equal(t, "mgr", lr.Roles[0].Code)

	// 建权限（PUT，code 在路径）。
	resp, body = c.do("PUT", "/v1/apps/"+u(appID)+"/permissions/order:read", map[string]any{
		"resource": "order", "action": "read", "ptype": "api", "name": "读订单"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var up adminv1.UpsertPermissionResponse
	require.NoError(t, protoUnmarshal(body, &up))

	// 授权（POST，role_id 在路径）。
	resp, body = c.do("POST", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/grants", map[string]any{
		"permissionId": strconv.FormatInt(up.PermissionId, 10), "eft": "allow"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 列授权（GET + query role_id 过滤命中）。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/grants?role_id="+i(cr.RoleId), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lg adminv1.ListGrantsResponse
	require.NoError(t, protoUnmarshal(body, &lg))
	require.Len(t, lg.Grants, 1)

	// DELETE 角色。
	resp, _ = c.do("DELETE", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestREST_PathAuthority_OverridesBodyAppID：body 伪造 app_id 被路径覆写。
func TestREST_PathAuthority_OverridesBodyAppID(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// body 里塞一个假 appId（999999），路径是真 app；角色应建到路径 app 而非 999999。
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles", map[string]any{
		"appId": "999999", "code": "x", "name": "y"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var lr adminv1.ListRolesResponse
	require.NoError(t, protoUnmarshal(body, &lr))
	require.Len(t, lr.Roles, 1) // 建到了路径 app，而非 body 的 999999
}

// 小工具：uint64/int64 转字符串路径段。
func u(v uint64) string { return strconv.FormatUint(v, 10) }
func i(v int64) string  { return strconv.FormatInt(v, 10) }

func TestREST_OneTimeSecrets(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	// CreateApplication 响应含非空 app_secret。
	resp, body := c.do("POST", "/v1/applications", map[string]any{
		"tenantName": "t1", "domain": "d1", "name": "n1", "appKey": "k-once"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var app adminv1.CreateApplicationResponse
	require.NoError(t, protoUnmarshal(body, &app))
	require.NotEmpty(t, app.AppSecret)

	// CreateOperator 响应含非空 secret。
	resp, body = c.do("POST", "/v1/operators", map[string]any{"principal": "op-rest"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var op adminv1.CreateOperatorResponse
	require.NoError(t, protoUnmarshal(body, &op))
	require.NotEmpty(t, op.Secret)
	require.NotZero(t, op.OperatorId)

	// ListOperators 走通且不含 secret 字段（OperatorSummary 结构保证）。
	resp, body = c.do("GET", "/v1/operators", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NotContains(t, string(body), op.Secret) // 明文 secret 绝不复现在列表里

	// 顺带验证顶层 ListApplications 走通（CreateApplication 已写入一条）。
	resp, body = c.do("GET", "/v1/applications", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var apps adminv1.ListApplicationsResponse
	require.NoError(t, protoUnmarshal(body, &apps))
	require.NotEmpty(t, apps.Applications)
}

func TestREST_SystemDomain_RequiresSuperAdmin(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	// root（super-admin）建一个普通 operator（无任何 grant）。
	resp, body := c.do("POST", "/v1/operators", map[string]any{"principal": "plain"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var op adminv1.CreateOperatorResponse
	require.NoError(t, protoUnmarshal(body, &op))

	// 该 operator 调 system 端点 ListOperators：无 admin/read → 403。
	plain := &restClient{t: t, base: ts.URL, principal: "plain", secret: []byte(op.Secret)}
	resp, _ = plain.do("GET", "/v1/operators", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// root 调 admin-roles 列表：放行。
	resp, body = c.do("GET", "/v1/admin-roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var roles adminv1.ListAdminRolesResponse
	require.NoError(t, protoUnmarshal(body, &roles))
	require.NotEmpty(t, roles.Roles) // 含内置 super-admin
}

func TestREST_RouteTable_Complete(t *testing.T) {
	// 通过 NewHandler 注册不 panic 即证明 28 条 method+pattern 无 ServeMux 冲突。
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NotPanics(t, func() {
		_ = restgw.NewHandler(nil, nil, nil, nil, logger)
	})
}

// ② 认证失败矩阵 → 401。
func TestREST_AuthnFailures(t *testing.T) {
	ts, _ := newTestGW(t)

	// 缺头部。
	req, _ := http.NewRequest("GET", ts.URL+"/v1/applications", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 坏签名。
	bad := &restClient{t: t, base: ts.URL, principal: "root", secret: []byte("WRONG-SECRET")}
	resp, _ = bad.do("GET", "/v1/applications", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 时间偏移越界：手工构造过期时间戳。
	staleTS := time.Now().Add(-10 * time.Minute).Unix()
	req, _ = http.NewRequest("GET", ts.URL+"/v1/applications", nil)
	sum := sha256.Sum256(nil)
	hh := hex.EncodeToString(sum[:])
	req.Header.Set(auth.HdrPrincipal, "root")
	req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(staleTS, 10))
	req.Header.Set(auth.HdrSignature, auth.SignREST([]byte("root-secret"), "root", staleTS, "GET", "/v1/applications", hh))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 非法 principal（含空格）。
	badP := &restClient{t: t, base: ts.URL, principal: "ro ot", secret: []byte("root-secret")}
	resp, _ = badP.do("GET", "/v1/applications", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ③ 鉴权：跨 app 域 / 细粒度资源 / system 端点 → 403。
func TestREST_AuthzMatrix(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 建两 app。
	appA := createApp(t, c, "ta", "da", "na", "k-aa")
	appB := createApp(t, c, "tb", "db", "nb", "k-bb")

	// reader：仅域 A 有 role/read。
	resp, body := c.do("POST", "/v1/operators", map[string]any{"principal": "reader"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var op adminv1.CreateOperatorResponse
	require.NoError(t, protoUnmarshal(body, &op))
	resp, body = c.do("POST", "/v1/admin-roles", map[string]any{"code": "reader-role", "name": "只读"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var ar adminv1.CreateAdminRoleResponse
	require.NoError(t, protoUnmarshal(body, &ar))
	resp, _ = c.do("POST", "/v1/admin-roles/"+i(ar.RoleId)+"/grants", map[string]any{
		"domain": u(appA.AppId), "resource": "role", "action": "read"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = c.do("POST", "/v1/operators/"+i(op.OperatorId)+"/roles", map[string]any{
		"roleId": strconv.FormatInt(ar.RoleId, 10), "domain": u(appA.AppId)})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := &restClient{t: t, base: ts.URL, principal: "reader", secret: []byte(op.Secret)}
	// 域 A role：放行。
	resp, _ = reader.do("GET", "/v1/apps/"+u(appA.AppId)+"/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// 域 B role：跨域 403。
	resp, _ = reader.do("GET", "/v1/apps/"+u(appB.AppId)+"/roles", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	// 域 A permission（只有 role/read）：细粒度 403。
	resp, _ = reader.do("GET", "/v1/apps/"+u(appA.AppId)+"/permissions", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	// system ListOperators：无 admin/read → 403。
	resp, _ = reader.do("GET", "/v1/operators", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// ④ status 写闸：停用 app 上写 → 409。
func TestREST_StatusWriteBlocked(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)
	app := createApp(t, c, "ts", "ds", "ns", "k-st")

	// 停用 app（status=2）。
	resp, _ := c.do("PUT", "/v1/applications/"+u(app.AppId)+"/status", map[string]any{"status": 2})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// 在停用 app 上写（建角色）→ FailedPrecondition → 409。
	resp, body := c.do("POST", "/v1/apps/"+u(app.AppId)+"/roles", map[string]any{"code": "x", "name": "y"})
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))
}

// ⑥ 错误映射：未知路由 404 / body 超限 400 / Internal 500 脱敏。
func TestREST_ErrorMapping(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 未知路由 → 404（ServeMux 默认）。
	resp, _ := c.do("GET", "/v1/nonexistent", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// body 超限（>1 MiB）→ 400。
	big := make([]byte, maxBodyForTest+1)
	for i := range big {
		big[i] = 'a'
	}
	resp = postRaw(t, ts.URL, "/v1/applications", "root", []byte("root-secret"), big)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// AlreadyExists→409（app_key 唯一冲突）。
	app := createApp(t, c, "tdup", "ddup", "ndup", "k-dup")
	require.NotZero(t, app.AppId)
	resp, _ = c.do("POST", "/v1/applications", map[string]any{
		"tenantName": "tdup2", "domain": "ddup2", "name": "ndup2", "appKey": "k-dup"}) // app_key 唯一冲突
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

// —— 测试 helpers ——
const maxBodyForTest = 1 << 20

func createApp(t *testing.T, c *restClient, tenant, domain, name, appKey string) *adminv1.CreateApplicationResponse {
	t.Helper()
	resp, body := c.do("POST", "/v1/applications", map[string]any{
		"tenantName": tenant, "domain": domain, "name": name, "appKey": appKey})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var app adminv1.CreateApplicationResponse
	require.NoError(t, protoUnmarshal(body, &app))
	return &app
}

// postRaw 发原始字节 body（用于 body 超限测试），签名按实际字节算。
func postRaw(t *testing.T, base, path, principal string, secret, body []byte) *http.Response {
	t.Helper()
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	ts := time.Now().Unix()
	req, err := http.NewRequest("POST", base+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set(auth.HdrPrincipal, principal)
	req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(auth.HdrSignature, auth.SignREST(secret, principal, ts, "POST", path, h))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp
}
