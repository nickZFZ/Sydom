package projection_test

import (
	"context"
	"database/sql"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 在种子 app 下建一个角色、一个权限点、一条授权、一个用户绑定，返回 roleID。
func seedRBAC(t *testing.T, db *sql.DB, appID int64) int64 {
	t.Helper()
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`,
		appID).Scan(&permID))
	_, err := db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id, eft)
		 VALUES ($1,$2,$3,'allow')`, appID, roleID, permID)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO user_role_binding (app_id, user_id, role_id)
		 VALUES ($1,'u-100',$2)`, appID, roleID)
	require.NoError(t, err)
	return roleID
}

func TestProjectApp_PAndGRows(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	seedRBAC(t, db, appID)

	rules, err := projection.ProjectApp(context.Background(), db, appID)
	require.NoError(t, err)

	d := dbtest.SeedDomain
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"manager", d, "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"u-100", "manager", d, "", "", ""}},
	}, rules)
}

func TestProjectApp_InheritanceGRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var parentID, childID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'admin','管理员') RETURNING id`,
		appID).Scan(&parentID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&childID))
	_, err := db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		 VALUES ($1,$2,$3)`, appID, parentID, childID)
	require.NoError(t, err)

	rules, err := projection.ProjectApp(context.Background(), db, appID)
	require.NoError(t, err)
	// child 继承 parent → g(child.code, parent.code, domain)
	require.Contains(t, rules,
		cp.Rule{Ptype: "g", V: [6]string{"manager", "admin", dbtest.SeedDomain, "", "", ""}})
}

func TestCheckNoCycle(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var a, b int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'A','A') RETURNING id`, appID).Scan(&a))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'B','B') RETURNING id`, appID).Scan(&b))
	// 已有 A 继承 B（child=A, parent=B）
	_, err := db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id) VALUES ($1,$2,$3)`,
		appID, b, a)
	require.NoError(t, err)

	// 再加 B 继承 A（child=B, parent=A）会成环 → 报错
	require.Error(t, projection.CheckNoCycle(context.Background(), db, appID, b, a))
	// 自环 → 报错
	require.Error(t, projection.CheckNoCycle(context.Background(), db, appID, a, a))
	// 合法：新角色 C 继承 A（child=C,parent=A），无环 → 通过
	var c int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'C','C') RETURNING id`, appID).Scan(&c))
	require.NoError(t, projection.CheckNoCycle(context.Background(), db, appID, c, a))
}

// TestCheckNoCycle_Transitive 验证 DFS 能跨多跳检出传递环（DFS 相对两点直连的核心价值）。
func TestCheckNoCycle_Transitive(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var a, b, c int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'A','A') RETURNING id`, appID).Scan(&a))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'B','B') RETURNING id`, appID).Scan(&b))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'C','C') RETURNING id`, appID).Scan(&c))

	// 已有链：A 继承 B（child=A,parent=B），B 继承 C（child=B,parent=C）→ A 传递继承 C
	_, err := db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id) VALUES ($1,$2,$3)`,
		appID, b, a)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id) VALUES ($1,$2,$3)`,
		appID, c, b)
	require.NoError(t, err)

	// 再加 C 继承 A（child=C,parent=A）：C→A→B→C 形成传递环 → 必须被检出
	require.ErrorIs(t, projection.CheckNoCycle(context.Background(), db, appID, c, a), projection.ErrCycle)
}
