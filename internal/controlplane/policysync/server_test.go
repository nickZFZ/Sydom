package policysync_test

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	}), res)

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
