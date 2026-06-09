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
