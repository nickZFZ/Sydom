package sydom_test

import (
	"context"
	"net"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/sidecar/authz"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// e2eMasterKey 返回固定 32 字节测试主密钥。
func e2eMasterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

func TestReportPermissions_EndToEnd(t *testing.T) {
	ctx := context.Background()
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// CP：装 app 凭据（HMAC 解密源）
	plain := []byte("e2e-secret-0123456789")
	res, err := secret.NewResolver(db, e2eMasterKey())
	require.NoError(t, err)
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	// CP：真 PolicyManager + 带 HMAC 拦截器的 PolicySync，跑在 bufconn
	mgr := policy.NewPolicyManager(db, nil)
	cpSrv := policysync.NewGRPCServer(policysync.NewServer(db, policysync.Config{}, mgr), res)
	cpLis := bufconn.Listen(1 << 20)
	go func() { _ = cpSrv.Serve(cpLis) }()
	t.Cleanup(cpSrv.Stop)

	// Sidecar：syncCli 经 bufconn 拨 CP（自动带 HMAC 凭据）
	table := dataperm.NewTable()
	engine, err := kernel.New("dom-e2e", nil, table)
	require.NoError(t, err)
	syncCli, err := syncclient.New(syncclient.Config{
		Endpoint: "passthrough:///bufnet",
		AppID:    dbtest.SeedAppKey,
		Secret:   plain,
		Secure:   false,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return cpLis.DialContext(ctx) }),
		},
	}, engine)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syncCli.Close() })

	// Sidecar：authz 本地服务，跑在另一个 bufconn
	authzr := authz.New(engine, dataperm.NewFilter(engine, table), syncCli, authz.Config{})
	sideSrv := authz.NewGRPCServer(authzr, syncCli)
	sideLis := bufconn.Listen(1 << 20)
	go func() { _ = sideSrv.Serve(sideLis) }()
	t.Cleanup(sideSrv.Stop)

	// SDK → 本地 Sidecar
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return sideLis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client, err := sydom.New("", sydom.WithConn(conn))
	require.NoError(t, err)

	// 预置一条 manual，验证 auto 不覆盖
	_, err = db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		VALUES ($1,'p.manual','order','write','api','人工','manual')`, appID)
	require.NoError(t, err)

	out, err := client.ReportPermissions(ctx, []sydom.Permission{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.manual", Resource: "x", Action: "x", Type: "x", Name: "篡改"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, out.Upserted)
	require.Equal(t, 1, out.Skipped)

	// 落库核验：p.read 是 auto；p.manual 仍 manual 未被篡改
	var src, name string
	require.NoError(t, db.QueryRow(`SELECT source FROM permission WHERE app_id=$1 AND code='p.read'`, appID).Scan(&src))
	require.Equal(t, "auto", src)
	require.NoError(t, db.QueryRow(`SELECT source, name FROM permission WHERE app_id=$1 AND code='p.manual'`, appID).Scan(&src, &name))
	require.Equal(t, "manual", src)
	require.Equal(t, "人工", name)
}
