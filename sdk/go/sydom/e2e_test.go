package sydom_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/authz"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// stubFresh 让陈旧守卫始终放行（Ready 且刚同步）。
type stubFresh struct{}

func (stubFresh) Ready() bool           { return true }
func (stubFresh) LastSyncAt() time.Time { return time.Now() }

func TestClient_EndToEnd_RealAuthService(t *testing.T) {
	table := dataperm.NewTable()
	engine, err := kernel.New("dom1", nil, table)
	require.NoError(t, err)
	filter := dataperm.NewFilter(engine, table)

	require.NoError(t, engine.ApplySnapshot(kernel.Snapshot{
		Version: 5,
		Rules: []kernel.Rule{
			{Ptype: "g", V: [6]string{"alice", "manager", "dom1"}},
			{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow"}},
		},
		DataPolicies: []kernel.DataPolicy{
			{ID: 1, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{ID: 2, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}))

	authzr := authz.New(engine, filter, stubFresh{}, authz.Config{MaxStaleness: 0})
	g := authz.NewGRPCServer(authzr)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	c, err := sydom.New("", sydom.WithConn(conn))
	require.NoError(t, err)

	// 功能权限：alice 经 manager 角色可 read order。
	allowed, err := c.Check(context.Background(), "alice", "order", "read")
	require.NoError(t, err)
	require.True(t, allowed)

	// 数据权限 deny-override 经真实 Filter 渲染。
	res, err := c.FilterSQL(context.Background(), "alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL)
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}
