package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOperatorEmail_CreateThenSetAndConflicts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")

	// CreateOperator 带 email（小写化落库）。
	_, err := srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-a", Email: "A@Acme.com"})
	require.NoError(t, err)
	var got string
	require.NoError(t, db.QueryRow(`SELECT email FROM admin_operator WHERE principal='op-a'`).Scan(&got))
	require.Equal(t, "a@acme.com", got)

	// SetOperatorEmail 改到新 email。
	_, err = srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-b"})
	require.NoError(t, err)
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: "b@acme.com"})
	require.NoError(t, err)

	// email 冲突→AlreadyExists。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: "a@acme.com"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 未知 operator→NotFound。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "ghost", Email: "x@acme.com"})
	require.Equal(t, codes.NotFound, status.Code(err))

	// 空 email 清除（NULL）。
	_, err = srv.SetOperatorEmail(ctx, &adminv1.SetOperatorEmailRequest{Principal: "op-b", Email: ""})
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='op-b' AND email IS NULL`).Scan(&n))
	require.Equal(t, 1, n)
}

// CreateOperator email 冲突→AlreadyExists（回退回滚，不留半条 operator）。
func TestOperatorEmail_CreateConflict(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")

	_, err := srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-x", Email: "dup@acme.com"})
	require.NoError(t, err)
	_, err = srv.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-y", Email: "dup@acme.com"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// op-y 不应因失败留下残行。
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='op-y'`).Scan(&n))
	require.Equal(t, 0, n)
}
