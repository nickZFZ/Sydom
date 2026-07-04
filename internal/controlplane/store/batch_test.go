package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func insertRole(t *testing.T, db *sql.DB, appID int64, code, name string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$3) RETURNING id`,
		appID, code, name).Scan(&id))
	return id
}

func insertPermission(t *testing.T, db *sql.DB, appID int64, code string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name) VALUES ($1,$2,'res','act','api',$2) RETURNING id`,
		appID, code).Scan(&id))
	return id
}

func bindUserRole(t *testing.T, db *sql.DB, appID int64, userID string, roleID int64) {
	t.Helper()
	require.NoError(t, store.InsertUserRoleBinding(context.Background(), db, appID, userID, roleID))
}

func grantRolePermission(t *testing.T, db *sql.DB, appID, roleID, permID int64) {
	t.Helper()
	require.NoError(t, store.InsertRolePermission(context.Background(), db, appID, roleID, permID, "allow"))
}

func addRoleInheritance(t *testing.T, db *sql.DB, appID, childID, parentID int64) {
	t.Helper()
	require.NoError(t, store.InsertRoleInheritance(context.Background(), db, appID, childID, parentID))
}

func insertDataPolicy(t *testing.T, db *sql.DB, appID int64, subjectID string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role',$2,'order','{"op":"ALL"}'::jsonb,1) RETURNING id`,
		appID, subjectID).Scan(&id))
	return id
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestDeleteRolesBatch_CascadesAndCounts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	r1 := insertRole(t, db, appID, "iac:a", "A")
	r2 := insertRole(t, db, appID, "iac:b", "B")
	permID := insertPermission(t, db, appID, "p.read")
	bindUserRole(t, db, appID, "u1", r1)
	grantRolePermission(t, db, appID, r1, permID)

	applied, err := store.DeleteRolesBatch(ctx, db, appID, []int64{r1, r2, 999999})
	require.NoError(t, err)
	require.EqualValues(t, 2, applied, "r1,r2 存在;999999 no-op")

	require.Equal(t, 0, countRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID))
	require.Equal(t, 0, countRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1`, appID), "级联删绑定")
	require.Equal(t, 0, countRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1`, appID), "级联删授权")
}

func TestDeleteRolesBatch_EmptyInput_NoOp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	r1 := insertRole(t, db, appID, "iac:a", "A")

	applied, err := store.DeleteRolesBatch(ctx, db, appID, nil)
	require.NoError(t, err)
	require.EqualValues(t, 0, applied)
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID), "空批不应触碰任何行")
	_ = r1
}

func TestDeleteRolesBatch_AllNoOp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	applied, err := store.DeleteRolesBatch(ctx, db, appID, []int64{111, 222})
	require.NoError(t, err)
	require.EqualValues(t, 0, applied)
}

func TestDeleteUserRoleBindingsBatch_PairMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	r1 := insertRole(t, db, appID, "iac:a", "A")
	bindUserRole(t, db, appID, "u1", r1)
	bindUserRole(t, db, appID, "u2", r1)

	applied, err := store.DeleteUserRoleBindingsBatch(ctx, db, appID,
		[]store.UserRolePair{{UserID: "u1", RoleID: r1}, {UserID: "nobody", RoleID: r1}})
	require.NoError(t, err)
	require.EqualValues(t, 1, applied, "u1 存在;nobody no-op")

	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1`, appID))
	require.Equal(t, 0, countRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND user_id='u1'`, appID))
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND user_id='u2'`, appID))
}

func TestDeleteRolePermissionsBatch_PairMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	r := insertRole(t, db, appID, "iac:a", "A")
	p1 := insertPermission(t, db, appID, "p.read")
	p2 := insertPermission(t, db, appID, "p.write")
	grantRolePermission(t, db, appID, r, p1)
	grantRolePermission(t, db, appID, r, p2)

	applied, err := store.DeleteRolePermissionsBatch(ctx, db, appID,
		[]store.GrantPair{{RoleID: r, PermissionID: p1}, {RoleID: r, PermissionID: 999999}})
	require.NoError(t, err)
	require.EqualValues(t, 1, applied)

	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1`, appID))
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1 AND permission_id=$2`, appID, p2))
}

func TestDeleteRoleInheritancesBatch_PairMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	parent := insertRole(t, db, appID, "iac:parent", "Parent")
	childA := insertRole(t, db, appID, "iac:childA", "ChildA")
	childB := insertRole(t, db, appID, "iac:childB", "ChildB")
	addRoleInheritance(t, db, appID, childA, parent)
	addRoleInheritance(t, db, appID, childB, parent)

	applied, err := store.DeleteRoleInheritancesBatch(ctx, db, appID,
		[]store.InheritancePair{{ChildRoleID: childA, ParentRoleID: parent}, {ChildRoleID: 999999, ParentRoleID: parent}})
	require.NoError(t, err)
	require.EqualValues(t, 1, applied)

	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM role_inheritance WHERE app_id=$1`, appID))
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM role_inheritance WHERE app_id=$1 AND child_role_id=$2`, appID, childB))
}

func TestDeleteDataPoliciesBatch_ReturningIDs(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	id1 := insertDataPolicy(t, db, appID, "manager")
	id2 := insertDataPolicy(t, db, appID, "clerk")

	removed, err := store.DeleteDataPoliciesBatch(ctx, db, appID, []int64{id1, 999999})
	require.NoError(t, err)
	require.Equal(t, []int64{id1}, removed)

	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM data_policy WHERE app_id=$1`, appID))
	require.Equal(t, 1, countRows(t, db, `SELECT count(*) FROM data_policy WHERE app_id=$1 AND id=$2`, appID, id2))
}
