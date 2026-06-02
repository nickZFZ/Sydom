package outbox_test

import (
	"context"
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSink_PolicyWritePersistsOutboxRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	roleID, _, err := mgr.CreateRole(ctx, appID, "manager", "经理")
	require.NoError(t, err)
	permID, _, err := mgr.UpsertPermission(ctx, appID, "order.read", "order", "read", "p", "读订单")
	require.NoError(t, err)
	d, err := mgr.GrantPermission(ctx, appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.NotNil(t, d, "授权应产生 Delta")

	var blob []byte
	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT version, delta_proto FROM policy_outbox WHERE app_id=$1 ORDER BY id DESC LIMIT 1`, appID).Scan(&ver, &blob))
	require.Equal(t, d.Version, ver)
	var pd syncv1.Delta
	require.NoError(t, proto.Unmarshal(blob, &pd))
	require.Equal(t, uint64(d.Version), pd.Version)
}

func TestSink_FailureRollsBackWrite(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	mgr := policy.NewPolicyManager(db, failingSink{})
	roleID, _, _ := mgr.CreateRole(ctx, appID, "manager", "经理")
	permID, _, _ := mgr.UpsertPermission(ctx, appID, "order.read", "order", "read", "p", "读订单")
	_, err := mgr.GrantPermission(ctx, appID, roleID, permID, "allow")
	require.Error(t, err, "sink 失败应使写事务回滚并返错")

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM policy_outbox`).Scan(&n))
	require.Equal(t, 0, n)
	var ver int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, int64(0), ver, "回滚后版本不应 bump")
}

type failingSink struct{}

func (failingSink) Persist(ctx context.Context, tx cp.DBTX, appID int64, d *cp.Delta) error {
	return assertErr
}

var assertErr = errSink("boom")

type errSink string

func (e errSink) Error() string { return string(e) }
