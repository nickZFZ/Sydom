package e2e_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	oapp "github.com/nickZFZ/Sydom/examples/orderservice/app"
	seed "github.com/nickZFZ/Sydom/examples/seed"
	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cpapp "github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	sideapp "github.com/nickZFZ/Sydom/internal/sidecar/app"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// noRedirect 不自动跟随重定向——删除成功返回 303，需原样断言（DefaultClient 会跟随到 200）。
var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func masterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

// startControlPlane 起 CP（随机端口），返回 adminAddr、syncAddr、root secret。
func startControlPlane(t *testing.T, dsn, redisAddr string) (adminAddr, syncAddr string, rootSecret []byte) {
	t.Helper()
	rootSecret = []byte("root-secret")
	cfg := cpapp.Config{
		DatabaseDSN: dsn, RedisAddr: redisAddr, RootPrincipal: "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond, RelayPollInterval: 20 * time.Millisecond,
		MasterKey: masterKey(), RootSecret: rootSecret,
	}
	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = cpapp.Run(ctx, cfg, adminLis, syncLis, nil, logger) }()
	return adminLis.Addr().String(), syncLis.Addr().String(), rootSecret
}

// startSidecar 起 Sidecar（随机端口），返回本地 AuthService 地址。
func startSidecar(t *testing.T, syncAddr, appKey, domain string, secret []byte) string {
	t.Helper()
	authLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg := sideapp.Config{
		ControlPlaneAddr: syncAddr, AppKey: appKey, Domain: domain,
		MaxStaleness: 0, BackoffInitial: 20 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
		Secret: secret,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = sideapp.Run(ctx, cfg, authLis, logger) }()
	return authLis.Addr().String()
}

func dialAdmin(t *testing.T, addr, principal string, secret []byte) adminv1.AdminServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(principal, secret, false)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return adminv1.NewAdminServiceClient(conn)
}

// do 带 demo_user cookie 发请求；不跟随重定向（见 noRedirect 注释）。
// 仅用于 require.Eventually/Never 之外的直接断言（调用方在主测试 goroutine 中）。
func do(t *testing.T, base, method, path, user string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	require.NoError(t, err)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "demo_user", Value: user})
	}
	resp, err := noRedirect.Do(req)
	require.NoError(t, err)
	return resp
}

// tryDo 是 do 的"安全版"，用于 require.Eventually/Never 的条件函数内部。
// testify 的 Eventually/Never 在新 goroutine 中执行条件函数；若条件函数调用
// t.FailNow()（即 require.NoError），runtime.Goexit() 会退出该 goroutine 而不向
// 内部 channel 发值，导致 select 阻塞直至计时器超时——产生难以排查的假失败。
// tryDo 出错时返回 nil，让调用方决定是 false（Eventually）还是 true（Never）。
func tryDo(base, method, path, user string) *http.Response {
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		return nil
	}
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "demo_user", Value: user})
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil
	}
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestEndToEnd_OrderServiceFullChain(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)
	adminAddr, syncAddr, rootSecret := startControlPlane(t, dsn, redisAddr)

	adminCli := dialAdmin(t, adminAddr, "root@sydom", rootSecret)
	// 等 CP 就绪。
	require.Eventually(t, func() bool {
		_, err := adminCli.ListApplications(context.Background(), &adminv1.ListApplicationsRequest{})
		return err == nil
	}, 15*time.Second, 100*time.Millisecond)

	// 供应：建 app + 全套授权 + 数据策略，拿明文 secret。
	secret, err := seed.Provision(context.Background(), adminCli, "demo", "shop", "demo-shop")
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	// 起 Sidecar。
	sidecarAddr := startSidecar(t, syncAddr, "demo-shop", "shop", []byte(secret))

	// 订单服务自有库（复用同一 PG）。
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	client, err := sydom.New(sidecarAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	h, err := oapp.New(context.Background(), db, client)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// 等 Sidecar bootstrap 完成（alice 读应放行）。
	require.Eventually(t, func() bool {
		resp := tryDo(srv.URL, http.MethodGet, "/orders", "alice")
		if resp == nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 15*time.Second, 100*time.Millisecond, "Sidecar 应在引导后放行 alice 读")

	// G1#1 功能权限：alice 删成功（303 重定向回列表）；bob 删被拒（403 友好页）。
	respA := do(t, srv.URL, http.MethodPost, "/orders/1/delete", "alice")
	require.Equal(t, http.StatusSeeOther, respA.StatusCode)
	respA.Body.Close()
	respB := do(t, srv.URL, http.MethodPost, "/orders/2/delete", "bob")
	require.Equal(t, http.StatusForbidden, respB.StatusCode)
	require.Contains(t, bodyOf(t, respB), "无权")

	// G1#2 数据权限：bob 只见 shanghai；alice 见全部（含 beijing）。
	bobList := bodyOf(t, do(t, srv.URL, http.MethodGet, "/orders", "bob"))
	require.Contains(t, bobList, "上海客户")
	require.NotContains(t, bobList, "北京客户")
	aliceList := bodyOf(t, do(t, srv.URL, http.MethodGet, "/orders", "alice"))
	require.Contains(t, aliceList, "北京客户")

	// G1#2 deny-all 负向：本段动态新建 guest 角色 + 绑定 dave（区别于 seed 预配的 alice/bob）。
	// guest 有 read 功能权限但无 allow 数据策略 → 列表 0 行。
	guest, err := adminCli.CreateRole(context.Background(), &adminv1.CreateRoleRequest{AppId: appID(t, db), Code: "guest", Name: "访客"})
	require.NoError(t, err)
	pr, err := adminCli.UpsertPermission(context.Background(), &adminv1.UpsertPermissionRequest{
		AppId: appID(t, db), Code: "order:read", Resource: "order", Action: "read", Ptype: "api", Name: "查看订单"})
	require.NoError(t, err)
	_, err = adminCli.GrantPermission(context.Background(), &adminv1.GrantPermissionRequest{
		AppId: appID(t, db), RoleId: guest.GetRoleId(), PermissionId: pr.GetPermissionId(), Eft: "allow"})
	require.NoError(t, err)
	_, err = adminCli.BindUserRole(context.Background(), &adminv1.UserRoleRequest{AppId: appID(t, db), UserId: "dave", RoleId: guest.GetRoleId()})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resp := tryDo(srv.URL, http.MethodGet, "/orders", "dave")
		if resp == nil {
			return false
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return false
		}
		body := string(b)
		// dave 能进列表页（有 read）但 0 行（无 allow 数据策略 → deny-all，绝不退化为全表）
		return !strings.Contains(body, "上海客户") && !strings.Contains(body, "北京客户") && strings.Contains(body, "订单列表")
	}, 15*time.Second, 100*time.Millisecond, "deny-all 必须 0 行，绝不退化为全表")

	// G1#3 权限点上报：order:export=auto；read 仍 manual 未被覆盖。
	// 上报必须在 bootstrap 之后（此时 Sidecar→CP 中继连接已建立）；ReportCatalog 是 fail-soft，
	// 用 Eventually 重报直至 export 落 auto（ReportPermissions 同步中继，通常首次即成）。
	require.Eventually(t, func() bool {
		rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		oapp.ReportCatalog(rctx, client)
		cancel()
		aid, ok := tryAppID(db) // 闭包在子 goroutine：不可调 require.*（会 Goexit）
		if !ok {
			return false
		}
		var src string
		e := db.QueryRow(`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, aid, "order:export").Scan(&src)
		return e == nil && src == "auto"
	}, 15*time.Second, 200*time.Millisecond, "order:export 应经 auto 上报落库")
	requireSource(t, db, "order:read", "manual") // 人工配置不被 auto 覆盖

	// G1#4 实时同步：revoke alice 的 delete → 轮询至翻转为 403。
	_, err = adminCli.RevokePermission(context.Background(), &adminv1.RevokePermissionRequest{
		AppId: appID(t, db), RoleId: managerRoleID(t, db), PermissionId: deletePermID(t, db)})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resp := tryDo(srv.URL, http.MethodPost, "/orders/3/delete", "alice")
		if resp == nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusForbidden
	}, 15*time.Second, 100*time.Millisecond, "revoke 后 alice 删除应实时翻为 403")

	// G1#5 fail-close：另起一个错密钥 sidecar（永不就绪），其 client → 列表 503。
	badAddr := startSidecar(t, syncAddr, "demo-shop", "shop", []byte("wrong-secret"))
	badClient, err := sydom.New(badAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = badClient.Close() })
	badH, err := oapp.New(context.Background(), db, badClient)
	require.NoError(t, err)
	badSrv := httptest.NewServer(badH)
	t.Cleanup(badSrv.Close)
	require.Never(t, func() bool {
		resp := tryDo(badSrv.URL, http.MethodGet, "/orders", "alice")
		if resp == nil {
			return false // 连接失败视为未放行（fail-close）
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 200*time.Millisecond, "永不就绪的 Sidecar 必须始终 fail-close（503，绝不放行）")
}

// —— DB 辅助：直接查 demo-shop 的 app_id/角色/权限/权限点 source（断言用）——

func appID(t *testing.T, db *sql.DB) uint64 {
	t.Helper()
	var id uint64
	require.NoError(t, db.QueryRow(`SELECT id FROM application WHERE app_key='demo-shop'`).Scan(&id))
	return id
}

// tryAppID 是 appID 的非断言版本，供 require.Eventually 的条件 goroutine 用
// （goroutine 内不可调 require.*——t.FailNow→Goexit 会破坏 Eventually 的内部 channel）。
func tryAppID(db *sql.DB) (uint64, bool) {
	var id uint64
	if err := db.QueryRow(`SELECT id FROM application WHERE app_key='demo-shop'`).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}
func managerRoleID(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM role WHERE app_id=$1 AND code='manager'`, appID(t, db)).Scan(&id))
	return id
}
func deletePermID(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM permission WHERE app_id=$1 AND code='order:delete'`, appID(t, db)).Scan(&id))
	return id
}
func requireSource(t *testing.T, db *sql.DB, code, want string) {
	t.Helper()
	var src string
	require.NoError(t, db.QueryRow(`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, appID(t, db), code).Scan(&src))
	require.Equal(t, want, src, code)
}
