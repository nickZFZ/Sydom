package policysync_test

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// masterKey 固定 32 字节测试主密钥。
func masterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

// startServer 起一个带认证拦截器的 PolicySync 服务端（bufconn），返回连接与 app 的 secret。
func startServer(t *testing.T, db *sql.DB) (*grpc.ClientConn, []byte) {
	t.Helper()
	res, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	// 给种子 app 写入可解密的 secret（与下面 client 用同一份）
	plain := []byte("app-secret")
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	srv := policysync.NewGRPCServer(policysync.NewServer(db, policysync.Config{
		HeartbeatInterval: 50 * time.Millisecond,
		BufSize:           8,
	}, &stubReporter{}), res)

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, plain, false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, plain
}

// startServerNoAuth 起同一个 PolicySync 服务端，但返回的客户端连接不携带 per-RPC 凭据，
// 用于验证认证拦截器的拒绝行为。
func startServerNoAuth(t *testing.T, db *sql.DB) *grpc.ClientConn {
	t.Helper()
	res, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	plain := []byte("app-secret")
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	srv := policysync.NewGRPCServer(policysync.NewServer(db, policysync.Config{
		HeartbeatInterval: 50 * time.Millisecond,
		BufSize:           8,
	}, &stubReporter{}), res)

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	// 不带 grpc.WithPerRPCCredentials — 触发 Unauthenticated
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestPullSnapshot(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// 直接造一条 casbin_rule 与一条 data_policy + 推进版本
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		VALUES ($1,'p','manager','order-system','order','read','allow','',1)`, appID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','manager','order','{"op":"ALL"}'::jsonb,1)`, appID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET current_version=1 WHERE id=$1`, appID)
	require.NoError(t, err)

	conn, _ := startServer(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snap, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), snap.Version)
	require.Len(t, snap.Rules, 1)
	require.Equal(t, "p", snap.Rules[0].Ptype)
	require.Equal(t, []string{"manager", "order-system", "order", "read", "allow"}, snap.Rules[0].Values)
	require.Len(t, snap.DataPolicies, 1)
	require.Equal(t, "manager", snap.DataPolicies[0].SubjectId)
}

func TestPullSnapshot_Unauthenticated(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)

	conn := startServerNoAuth(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestSubscribe_ColdStartSendsSnapshotRequired(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`UPDATE application SET current_version=3 WHERE id=$1`, appID)
	require.NoError(t, err)

	conn, _ := startServer(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 冷启动 last_applied=0 < current=3 → 首事件应为 SnapshotRequired(behind)
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)
	ev, err := stream.Recv()
	require.NoError(t, err)
	sr := ev.GetSnapshotRequired()
	require.NotNil(t, sr)
	require.Equal(t, uint64(3), sr.CurrentVersion)
	require.Equal(t, "behind", sr.Reason)
}

func TestSubscribe_InSyncReceivesDispatchedDelta(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // current_version=0

	srvHolder := make(chan *policysync.Server, 1)
	conn := startServerCapture(t, db, srvHolder)
	srv := <-srvHolder

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// last_applied==current==0 → 无 SnapshotRequired，注册后等 Dispatch
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// 注册要先过认证 + 2 次 DB 读，存在时序窗口：后台每 50ms 持续投递一条 Delta，
	// 直到注册生效后某次 Dispatch 落入缓冲；前台 Recv 循环跳过心跳直到收到 Delta。
	dispatchDone := make(chan struct{})
	go func() {
		defer close(dispatchDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				srv.Hub().Dispatch(appID, &syncv1.SyncEvent{
					Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}},
				})
			}
		}
	}()

	var delta *syncv1.Delta
	for delta == nil {
		ev, err := stream.Recv()
		require.NoError(t, err)
		delta = ev.GetDelta() // 心跳的 GetDelta()==nil，跳过；收到 Delta 即退出
	}
	require.Equal(t, uint64(1), delta.Version)

	cancel()       // 停止后台 Dispatch
	<-dispatchDone // 等 goroutine 退出，避免泄漏
}

func TestEndToEnd_PublishToSubscribeStream(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // current_version=0
	addr := dbtest.StartRedis(t)

	srvHolder := make(chan *policysync.Server, 1)
	conn := startServerCapture(t, db, srvHolder)
	srv := <-srvHolder

	// 接线：RedisSubscriber → srv.Hub().Dispatch
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))
	loopCtx, loopCancel := context.WithCancel(context.Background())
	defer loopCancel()
	go func() { _ = srv.RunDispatchLoop(loopCtx, sub) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// Redis pub/sub 无缓冲 + 订阅/注册存在时序窗口（RedisSubscriber 需先 SUBSCRIBE、
	// Subscribe 流需先 hub.register）。故后台每 50ms 持续发布，直到流上收到 Delta；
	// 前台 Recv 循环跳过 SnapshotRequired/Heartbeat 等非 Delta 事件。
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = pub.Publish(context.Background(), appID, &syncv1.Delta{Version: 1})
			}
		}
	}()

	var got *syncv1.Delta
	for got == nil {
		ev, err := stream.Recv()
		require.NoError(t, err)
		got = ev.GetDelta() // 非 Delta 事件 GetDelta()==nil，跳过
	}
	require.Equal(t, uint64(1), got.Version)

	cancel()  // 停止后台发布
	<-pubDone // 等 goroutine 退出，避免泄漏
}

// startServerCapture 同 startServer，但把 *Server 经 holder 回传，便于测试直接 Dispatch。
func startServerCapture(t *testing.T, db *sql.DB, holder chan *policysync.Server) *grpc.ClientConn {
	t.Helper()
	res, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)
	plain := []byte("app-secret")
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	srv := policysync.NewServer(db, policysync.Config{HeartbeatInterval: 50 * time.Millisecond, BufSize: 8}, &stubReporter{})
	holder <- srv
	g := policysync.NewGRPCServer(srv, res)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, plain, false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type stubReporter struct {
	gotAppID int64
	gotN     int
	res      cp.ReportResult
}

func (s *stubReporter) ReportPermissions(_ context.Context, appID int64, pts []cp.PermissionPoint) (cp.ReportResult, error) {
	s.gotAppID = appID
	s.gotN = len(pts)
	return s.res, nil
}

func TestReportPermissions_ResolvesAppIDAndDelegates(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	stub := &stubReporter{res: cp.ReportResult{Upserted: 2}}
	srv := policysync.NewServer(db, policysync.Config{}, stub)

	ctx := auth.WithAppID(context.Background(), dbtest.SeedAppKey)
	resp, err := srv.ReportPermissions(ctx, &syncv1.ReportPermissionsRequest{
		Permissions: []*syncv1.PermissionPoint{
			{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
			{Code: "p.write", Resource: "order", Action: "write", Type: "api", Name: "写"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, appID, stub.gotAppID)
	require.Equal(t, 2, stub.gotN)
	require.Equal(t, uint32(2), resp.GetUpserted())
}

func TestReportPermissions_RejectsEmptyCode(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	srv := policysync.NewServer(db, policysync.Config{}, &stubReporter{})
	ctx := auth.WithAppID(context.Background(), dbtest.SeedAppKey)
	_, err := srv.ReportPermissions(ctx, &syncv1.ReportPermissionsRequest{
		Permissions: []*syncv1.PermissionPoint{{Code: "", Resource: "o", Action: "r"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestReportPermissions_Unauthenticated(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := policysync.NewServer(db, policysync.Config{}, &stubReporter{})
	_, err := srv.ReportPermissions(context.Background(), &syncv1.ReportPermissionsRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
