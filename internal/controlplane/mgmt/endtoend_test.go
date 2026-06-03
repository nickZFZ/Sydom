package mgmt_test

import (
	"context"
	"net"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestEndToEnd_AdminWriteReachesSidecarStream(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	addr := dbtest.StartRedis(t)

	// 1) 管理面（含 outbox sink），播种 super-admin root
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))

	// 2) relay：outbox → Redis
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go func() { _ = outbox.RunRelayLoop(relayCtx, db, pub, 20*time.Millisecond) }()

	// 3) ③-2 PolicySync：给 SeedApp 的 app 写可解密 secret 以便 Sidecar 认证
	res, err := secret.NewResolver(db, mk())
	require.NoError(t, err)
	enc, err := res.EncryptSecret([]byte("sidecar-secret"))
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	ps := policysync.NewServer(db, policysync.Config{HeartbeatInterval: 50 * time.Millisecond, BufSize: 8})
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))
	dispCtx, dispCancel := context.WithCancel(context.Background())
	defer dispCancel()
	go func() { _ = ps.RunDispatchLoop(dispCtx, sub) }()

	psSrv := policysync.NewGRPCServer(ps, res)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = psSrv.Serve(lis) }()
	t.Cleanup(psSrv.Stop)
	sidecarConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, []byte("sidecar-secret"), false)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sidecarConn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(sidecarConn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// 4) 管理面持续写（产生 Delta），直到 Sidecar 流上收到 Delta
	//    后台写 goroutine 内禁止用 require.*（非测试 goroutine Goexit 会吞诊断）
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		var i int
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				i++
				cr, e := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{
					AppId: uint64(appID), Code: "r" + itoa(i), Name: "n"})
				if e != nil || cr == nil {
					continue
				}
				up, e := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
					AppId: uint64(appID), Code: "p" + itoa(i), Resource: "res", Action: "read", Ptype: "p", Name: "n"})
				if e != nil || up == nil {
					continue
				}
				_, _ = cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
					AppId: uint64(appID), RoleId: cr.RoleId, PermissionId: up.PermissionId, Eft: "allow"})
			}
		}
	}()

	var got *syncv1.Delta
	for got == nil {
		ev, err := stream.Recv()
		require.NoError(t, err)
		got = ev.GetDelta() // 跳过 SnapshotRequired/Heartbeat
	}
	require.Greater(t, got.Version, uint64(0))

	cancel()
	<-writeDone
}

// itoa 避免引入 strconv 仅为拼名。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
