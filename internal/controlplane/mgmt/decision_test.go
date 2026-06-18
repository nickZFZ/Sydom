package mgmt_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 直插一条 casbin_rule（p: v0=sub v1=dom v2=obj v3=act v4=eft；g: v0=child v1=parent v2=dom）。
func insertCasbinRuleM(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	var c [6]string
	copy(c[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
		appID, ptype, c[0], c[1], c[2], c[3], c[4], c[5])
	require.NoError(t, err)
}

func TestExplainDecision_ThreeReasons(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleM(t, db, appID, "p", "manager", dom, "orders", "read", "allow")
	insertCasbinRuleM(t, db, appID, "g", "alice", "manager", dom)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// allow
	r1, err := root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "read"})
	require.NoError(t, err)
	require.True(t, r1.Allowed)
	require.Equal(t, "ALLOW_GRANTED", r1.Reason)
	require.NotNil(t, r1.DecidingRule)
	require.Equal(t, "manager", r1.DecidingRole)

	// default-deny（无 grant 命中 delete）
	r2, err := root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "delete"})
	require.NoError(t, err)
	require.False(t, r2.Allowed)
	require.Equal(t, "DENY_NO_MATCH", r2.Reason)
	require.Nil(t, r2.DecidingRule)
	require.Contains(t, r2.Roles, "manager") // 仍列角色

	// user_id 空 → InvalidArgument
	_, err = root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), Resource: "orders", Action: "read"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// 跨租户：非超管操作员对他人 app 的 app 域无授权 → 403。
func TestExplainDecision_CrossTenantDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "mallory"})
	require.NoError(t, err)
	mallory := dialMgmt(t, db, "mallory", []byte(op.Secret))
	_, err = mallory.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "read"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
