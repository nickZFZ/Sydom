package mgmt_test

import (
	"context"
	"fmt"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestListPermissions_PageSearchSortTotal 验证 ListPermissions 分页/搜索/排序/注入防御/total。
func TestListPermissions_PageSearchSortTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	for i := 0; i < 5; i++ {
		_, err := db.Exec(`INSERT INTO permission(app_id,code,resource,action,type,name,source)
			VALUES($1,$2,'order','read','api',$3,'manual')`,
			appID, fmt.Sprintf("p%d", i), fmt.Sprintf("权限%d", i))
		require.NoError(t, err)
	}
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 分页 limit=2 offset=0 → 2 行，total=5
	resp, err := s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 2, Offset: 0, Sort: "code", Order: "asc"}})
	require.NoError(t, err)
	require.Len(t, resp.Permissions, 2)
	require.Equal(t, uint32(5), resp.Total)
	require.Equal(t, "p0", resp.Permissions[0].Code)

	// 搜索 q=p1 → 1 行
	resp, err = s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Q: "p1"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)

	// 注入 sort → 回退默认、不报错、不破坏表
	resp, err = s.ListPermissions(ctx, &adminv1.ListPermissionsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Sort: "id;DROP TABLE permission"}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total) // 表未被破坏
}

// TestListRoles_Page 验证 ListRoles 分页 + total。
func TestListRoles_Page(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	for i := 0; i < 4; i++ {
		_, err := db.Exec(`INSERT INTO role(app_id,code,name) VALUES($1,$2,$3)`,
			appID, fmt.Sprintf("r%d", i), fmt.Sprintf("角色%d", i))
		require.NoError(t, err)
	}
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// limit=2 → 2 行，total=4
	resp, err := s.ListRoles(ctx, &adminv1.ListRolesRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 2, Sort: "code", Order: "asc"}})
	require.NoError(t, err)
	require.Len(t, resp.Roles, 2)
	require.Equal(t, uint32(4), resp.Total)
	require.Equal(t, "r0", resp.Roles[0].Code)

	// 搜索 q=r2
	resp, err = s.ListRoles(ctx, &adminv1.ListRolesRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Q: "r2"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
}

// TestListGrants_PageAndFilter 验证 ListGrants 分页 + total + role_id 过滤仍生效。
func TestListGrants_PageAndFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// 建 2 个角色
	var roleA, roleB int64
	require.NoError(t, db.QueryRow(`INSERT INTO role(app_id,code,name) VALUES($1,'ra','角色A') RETURNING id`, appID).Scan(&roleA))
	require.NoError(t, db.QueryRow(`INSERT INTO role(app_id,code,name) VALUES($1,'rb','角色B') RETURNING id`, appID).Scan(&roleB))
	// 建 3 个权限
	pids := make([]int64, 3)
	for i := range pids {
		require.NoError(t, db.QueryRow(
			`INSERT INTO permission(app_id,code,resource,action,type,name) VALUES($1,$2,'res','act','api',$3) RETURNING id`,
			appID, fmt.Sprintf("pg%d", i), fmt.Sprintf("权限G%d", i)).Scan(&pids[i]))
	}
	// roleA → 2 个 grant；roleB → 1 个 grant；共 3 个
	for _, pid := range pids[:2] {
		_, err := db.Exec(`INSERT INTO role_permission(app_id,role_id,permission_id,eft) VALUES($1,$2,$3,'allow')`, appID, roleA, pid)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO role_permission(app_id,role_id,permission_id,eft) VALUES($1,$2,$3,'allow')`, appID, roleB, pids[2])
	require.NoError(t, err)

	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 不过滤：total=3，limit=2 → 2 行
	resp, err := s.ListGrants(ctx, &adminv1.ListGrantsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 2}})
	require.NoError(t, err)
	require.Len(t, resp.Grants, 2)
	require.Equal(t, uint32(3), resp.Total)

	// role_id 过滤：roleA → 2 行
	resp, err = s.ListGrants(ctx, &adminv1.ListGrantsRequest{
		AppId: uint64(appID), RoleId: roleA})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Total)
	require.Len(t, resp.Grants, 2)

	// role_id 过滤：roleB → 1 行
	resp, err = s.ListGrants(ctx, &adminv1.ListGrantsRequest{
		AppId: uint64(appID), RoleId: roleB})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
}

// TestListRoleInheritances_Page 验证 ListRoleInheritances 分页 + total。
func TestListRoleInheritances_Page(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// 建 3 个角色，r0←r1←r2（链）
	var rids [3]int64
	for i := range rids {
		require.NoError(t, db.QueryRow(`INSERT INTO role(app_id,code,name) VALUES($1,$2,$3) RETURNING id`,
			appID, fmt.Sprintf("ri%d", i), fmt.Sprintf("继承角色%d", i)).Scan(&rids[i]))
	}
	// 2 条 inheritance：rids[0]←rids[1]，rids[1]←rids[2]
	_, err := db.Exec(`INSERT INTO role_inheritance(app_id,parent_role_id,child_role_id) VALUES($1,$2,$3)`, appID, rids[0], rids[1])
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO role_inheritance(app_id,parent_role_id,child_role_id) VALUES($1,$2,$3)`, appID, rids[1], rids[2])
	require.NoError(t, err)

	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// limit=1 → 1 行，total=2
	resp, err := s.ListRoleInheritances(ctx, &adminv1.ListRoleInheritancesRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 1}})
	require.NoError(t, err)
	require.Len(t, resp.Inheritances, 1)
	require.Equal(t, uint32(2), resp.Total)

	// 全取
	resp, err = s.ListRoleInheritances(ctx, &adminv1.ListRoleInheritancesRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Total)
	require.Len(t, resp.Inheritances, 2)
}

// TestListUserBindings_PageAndFilter 验证 ListUserBindings 分页 + total + user_id 过滤仍生效。
func TestListUserBindings_PageAndFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var rid int64
	require.NoError(t, db.QueryRow(`INSERT INTO role(app_id,code,name) VALUES($1,'ub-role','绑定角色') RETURNING id`, appID).Scan(&rid))

	// alice → 1 个绑定；bob → 1 个绑定；共 2 个
	_, err := db.Exec(`INSERT INTO user_role_binding(app_id,user_id,role_id) VALUES($1,'alice',$2)`, appID, rid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO user_role_binding(app_id,user_id,role_id) VALUES($1,'bob',$2)`, appID, rid)
	require.NoError(t, err)

	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 不过滤：total=2，limit=1 → 1 行
	resp, err := s.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 1}})
	require.NoError(t, err)
	require.Len(t, resp.Bindings, 1)
	require.Equal(t, uint32(2), resp.Total)

	// user_id 过滤：alice → 1 行
	resp, err = s.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{
		AppId: uint64(appID), UserId: "alice"})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Equal(t, "alice", resp.Bindings[0].UserId)

	// 搜索 q=ali（ILIKE）→ 1 行
	resp, err = s.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Q: "ali"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
}

// TestListDataPolicies_PageAndFilter 验证 ListDataPolicies 分页 + total + resource/effect 过滤仍生效。
func TestListDataPolicies_PageAndFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// seed：3 条 order-allow，1 条 order-deny，1 条 product-allow；共 5 条
	for i := 0; i < 3; i++ {
		_, err := db.Exec(
			`INSERT INTO data_policy(app_id,subject_type,subject_id,resource,condition,effect,version)
			 VALUES($1,'role',$2,'order','{"op":"EQ"}'::jsonb,'allow',1)`,
			appID, fmt.Sprintf("role%d", i))
		require.NoError(t, err)
	}
	_, err := db.Exec(
		`INSERT INTO data_policy(app_id,subject_type,subject_id,resource,condition,effect,version)
		 VALUES($1,'role','deny-role','order','{"op":"EQ"}'::jsonb,'deny',1)`, appID)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO data_policy(app_id,subject_type,subject_id,resource,condition,effect,version)
		 VALUES($1,'role','prod-role','product','{"op":"EQ"}'::jsonb,'allow',1)`, appID)
	require.NoError(t, err)

	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 不过滤：total=5，limit=2 → 2 行
	resp, err := s.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{
		AppId: uint64(appID), Page: &adminv1.ListPage{Limit: 2}})
	require.NoError(t, err)
	require.Len(t, resp.DataPolicies, 2)
	require.Equal(t, uint32(5), resp.Total)

	// resource=order 过滤 → 4 行
	resp, err = s.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{
		AppId: uint64(appID), Resource: "order"})
	require.NoError(t, err)
	require.Equal(t, uint32(4), resp.Total)
	for _, dp := range resp.DataPolicies {
		require.Equal(t, "order", dp.Resource)
	}

	// effect=deny 过滤 → 1 行
	resp, err = s.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{
		AppId: uint64(appID), Effect: "deny"})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Equal(t, "deny", resp.DataPolicies[0].Effect)

	// resource+effect 组合过滤 → order+allow=3 行
	resp, err = s.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{
		AppId: uint64(appID), Resource: "order", Effect: "allow"})
	require.NoError(t, err)
	require.Equal(t, uint32(3), resp.Total)
}
