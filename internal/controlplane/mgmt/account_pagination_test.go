package mgmt_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// ─── ListOperators ────────────────────────────────────────────────────────────

// TestListOperators_PageStatusSearchTotal 验证 ListOperators 分页/status 过滤/搜索/total，
// 并断言响应中绝无 secret（OperatorSummary 结构本就不含 secret 字段）。
func TestListOperators_PageStatusSearchTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 造 5 个 operator：principal op0..op4，status=1（active）
	for i := 0; i < 5; i++ {
		_, err := db.Exec(
			`INSERT INTO admin_operator(principal, secret_enc, status) VALUES($1,'enc',1)`,
			fmt.Sprintf("op%d@sydom", i))
		require.NoError(t, err)
	}
	// 再造 2 个 status=2（suspended）
	for i := 5; i < 7; i++ {
		_, err := db.Exec(
			`INSERT INTO admin_operator(principal, secret_enc, status) VALUES($1,'enc',2)`,
			fmt.Sprintf("op%d@sydom", i))
		require.NoError(t, err)
	}
	// 总共 7 个（不含 setup 可能预置的 root；seed 行数<50，安全）

	// 不过滤：limit=3 → 3 行，total=7
	resp, err := s.ListOperators(ctx, &adminv1.ListOperatorsRequest{
		Page: &adminv1.ListPage{Limit: 3, Sort: "id", Order: "asc"}})
	require.NoError(t, err)
	require.Len(t, resp.Operators, 3)
	require.Equal(t, uint32(7), resp.Total)

	// status=2 过滤 → 2 行，total=2
	resp, err = s.ListOperators(ctx, &adminv1.ListOperatorsRequest{
		Status: 2})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Total)
	require.Len(t, resp.Operators, 2)
	for _, op := range resp.Operators {
		require.Equal(t, uint32(2), op.Status)
	}

	// 搜索 q=op1 → 1 行（op1@sydom 匹配）
	resp, err = s.ListOperators(ctx, &adminv1.ListOperatorsRequest{
		Page: &adminv1.ListPage{Q: "op1@sydom"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Equal(t, "op1@sydom", resp.Operators[0].Principal)

	// OFFSET：limit=3 offset=4 → 3 行，total 仍=7
	resp, err = s.ListOperators(ctx, &adminv1.ListOperatorsRequest{
		Page: &adminv1.ListPage{Limit: 3, Offset: 4, Sort: "id", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(7), resp.Total)
	require.Len(t, resp.Operators, 3)

	// 断言 OperatorSummary 没有 secret 字段（静态类型检查已保证，运行期亦无法获取）
	// 以下只检查字段集合不包含任何 "secret" 字段名（通过 proto 反射无法直接做，
	// 但 OperatorSummary 结构体本身不含 secret，编译已证明，这里只做运行期基线断言）
	for _, op := range resp.Operators {
		require.NotEmpty(t, op.Principal) // 有 principal 无 secret
		// OperatorSummary 只有 OperatorId / Principal / Status 三个字段
	}
}

// ─── ListAdminRoles ───────────────────────────────────────────────────────────

// TestListAdminRoles_PageSearchTotal 验证 ListAdminRoles 分页/搜索/total。
// schema migration(000013) 预置 1 个 admin_role(super-admin)，故 total = seed+1。
func TestListAdminRoles_PageSearchTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	// 造 5 个 admin_role；加上 migration 预置的 super-admin = 6 个
	for i := 0; i < 5; i++ {
		_, err := db.Exec(
			`INSERT INTO admin_role(code, name) VALUES($1,$2)`,
			fmt.Sprintf("role_%d", i), fmt.Sprintf("管理角色%d", i))
		require.NoError(t, err)
	}

	// limit=2 → 2 行，total=6（5+1预置）
	resp, err := s.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{
		Page: &adminv1.ListPage{Limit: 2, Sort: "id", Order: "asc"}})
	require.NoError(t, err)
	require.Len(t, resp.Roles, 2)
	require.Equal(t, uint32(6), resp.Total)

	// offset=4 → 2 行（最后两条），total=6
	resp, err = s.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{
		Page: &adminv1.ListPage{Limit: 10, Offset: 4, Sort: "id", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(6), resp.Total)
	require.Len(t, resp.Roles, 2)

	// 搜索 q=role_2 → 1 行（code 精确含 "role_2"）
	resp, err = s.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{
		Page: &adminv1.ListPage{Q: "role_2"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Equal(t, "role_2", resp.Roles[0].Code)

	// 注入 sort → 回退默认、表未破坏
	resp, err = s.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{
		Page: &adminv1.ListPage{Sort: "id;DROP TABLE admin_role"}})
	require.NoError(t, err)
	require.Equal(t, uint32(6), resp.Total)
}

// ─── ListApplications ────────────────────────────────────────────────────────

// TestListApplications_PageStatusTenantScopeTotal 验证 ListApplications 分页/status 过滤/
// tenant scope / 超管全量 / total。
func TestListApplications_PageStatusTenantScopeTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := context.Background()

	// 造两个租户 + 各若干 app
	rA, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tenA", OwnerPrincipal: "oa"})
	require.NoError(t, err)
	rB, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tenB", OwnerPrincipal: "ob"})
	require.NoError(t, err)

	// 租户 A：3 个 app（status=1）
	for i := 0; i < 3; i++ {
		_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
			TenantId: rA.TenantId,
			Domain:   fmt.Sprintf("da%d", i),
			Name:     fmt.Sprintf("appA%d", i),
			AppKey:   fmt.Sprintf("AK_A%d", i),
		})
		require.NoError(t, err)
	}
	// 租户 B：2 个 app（status=1）
	for i := 0; i < 2; i++ {
		_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
			TenantId: rB.TenantId,
			Domain:   fmt.Sprintf("db%d", i),
			Name:     fmt.Sprintf("appB%d", i),
			AppKey:   fmt.Sprintf("AK_B%d", i),
		})
		require.NoError(t, err)
	}
	// 把租户 A 的第一个 app 设为 status=2（disabled）
	appsA, err := s.ListApplications(ctx, &adminv1.ListApplicationsRequest{TenantId: rA.TenantId})
	require.NoError(t, err)
	require.Len(t, appsA.Applications, 3)
	firstAID := appsA.Applications[0].AppId
	_, err = s.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: firstAID, Status: 2})
	require.NoError(t, err)

	// 超管全量（tenant_id=0）：total=5，limit=2 → 2 行
	resp, err := s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		Page: &adminv1.ListPage{Limit: 2}})
	require.NoError(t, err)
	require.Len(t, resp.Applications, 2)
	require.Equal(t, uint32(5), resp.Total)

	// 租户 A scope：total=3
	resp, err = s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		TenantId: rA.TenantId})
	require.NoError(t, err)
	require.Equal(t, uint32(3), resp.Total)
	for _, a := range resp.Applications {
		// 确认 status 字段存在（非 secret）
		_ = a.Status
	}

	// 租户 A scope + status=2 过滤 → 1 行
	resp, err = s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		TenantId: rA.TenantId,
		Status:   2})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Len(t, resp.Applications, 1)
	require.Equal(t, uint32(2), resp.Applications[0].Status)

	// 超管全量 + status=1 → total=4（5 个 app 里 1 个 status=2）
	resp, err = s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		Status: 1})
	require.NoError(t, err)
	require.Equal(t, uint32(4), resp.Total)

	// 超管全量 + limit=2 + offset=3 → 2 行，total=5
	resp, err = s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		Page: &adminv1.ListPage{Limit: 2, Offset: 3}})
	require.NoError(t, err)
	require.Len(t, resp.Applications, 2)
	require.Equal(t, uint32(5), resp.Total)

	// 租户 B scope：total=2（不含租户 A 的 app）
	resp, err = s.ListApplications(ctx, &adminv1.ListApplicationsRequest{
		TenantId: rB.TenantId})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Total)
}

// ─── ListMembers ─────────────────────────────────────────────────────────────

// TestListMembers_PageTierFilterTenantScopeTotal 验证 ListMembers 分页/tier 过滤/
// tenant scope / total。
func TestListMembers_PageTierFilterTenantScopeTotal(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := context.Background()

	// 造两个租户
	rA, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "mA", OwnerPrincipal: "mOwner"})
	require.NoError(t, err)
	rB, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "mB", OwnerPrincipal: "mOwnerB"})
	require.NoError(t, err)

	// 抬高 free 成员限（该测试需租户 A 达 4 成员，测分页非配额；解耦 M6.1d 成员配额）
	_, err = db.Exec(`UPDATE plan SET max_members=1000 WHERE name='free'`)
	require.NoError(t, err)

	// 租户 A：owner（已有）+ 3 个 admin 成员
	for i := 0; i < 3; i++ {
		_, err := s.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId:  rA.TenantId,
			Principal: fmt.Sprintf("admin%d@sydom", i),
		})
		require.NoError(t, err)
	}
	// 租户 B：仅 owner
	// 租户 A 共 4 成员（1 owner + 3 admin）

	// 无过滤，全返（limit 大）：total=4，租户 A 所有成员
	resp, err := s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId})
	require.NoError(t, err)
	require.Equal(t, uint32(4), resp.Total)
	require.Len(t, resp.Members, 4)

	// limit=2 → 2 行，total=4
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId,
		Page:     &adminv1.ListPage{Limit: 2}})
	require.NoError(t, err)
	require.Len(t, resp.Members, 2)
	require.Equal(t, uint32(4), resp.Total)

	// tier=1（owner）过滤 → 1 行
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId,
		Tier:     1})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Len(t, resp.Members, 1)
	require.Equal(t, uint32(1), resp.Members[0].Tier)

	// tier=2（admin）过滤 → 3 行
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId,
		Tier:     2})
	require.NoError(t, err)
	require.Equal(t, uint32(3), resp.Total)
	require.Len(t, resp.Members, 3)

	// tenant scope：只返回租户 A 的成员（不含租户 B）
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId})
	require.NoError(t, err)
	// 租户 A 成员 principal 集合
	principals := make([]string, 0, len(resp.Members))
	for _, m := range resp.Members {
		principals = append(principals, m.Principal)
	}
	require.NotContains(t, principals, "mOwnerB") // 租户 B owner 不在列表里

	// 租户 B：只有 1 个 owner，total=1
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rB.TenantId})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)

	// offset：limit=2 offset=2 → 2 行（admin1 和 admin2），total=4
	resp, err = s.ListMembers(ctx, &adminv1.ListMembersRequest{
		TenantId: rA.TenantId,
		Page:     &adminv1.ListPage{Limit: 2, Offset: 2, Sort: "principal", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(4), resp.Total)
	require.Len(t, resp.Members, 2)
}

// ─── ListMyTenants ────────────────────────────────────────────────────────────

// TestListMyTenants_InMemoryPageQSort 验证 ListMyTenants 内存分页/q 搜索/sort/total。
func TestListMyTenants_InMemoryPageQSort(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := context.Background()
	const principal = "multi@sydom"

	// 造 5 个租户：multi@sydom 是第一个租户的 owner，其余 4 个通过 InviteMember 加入
	tenantNames := []string{"alpha", "beta", "gamma", "delta", "epsilon"}

	// 第一个租户：multi@sydom 是 owner
	r0, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
		TenantName:     tenantNames[0],
		OwnerPrincipal: principal,
	})
	require.NoError(t, err)
	_ = r0

	// 其余 4 个租户：各自有不同 owner，然后邀请 multi@sydom 加入
	for i, name := range tenantNames[1:] {
		reg, err := s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
			TenantName:     name,
			OwnerPrincipal: fmt.Sprintf("owner%d@sydom", i),
		})
		require.NoError(t, err)
		_, err = s.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId:  reg.TenantId,
			Principal: principal,
		})
		require.NoError(t, err)
	}

	// 也建一个 multi@sydom 不是成员的租户（另一个 owner）
	_, err = s.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
		TenantName: "other", OwnerPrincipal: "other@sydom"})
	require.NoError(t, err)

	myCtx := cp.WithOperator(ctx, principal)

	// 全量（无分页）：total=5，只含 multi@sydom 的租户
	resp, err := s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total)
	require.Len(t, resp.Memberships, 5)
	// 不含 "other"
	for _, m := range resp.Memberships {
		require.NotEqual(t, "other", m.TenantName)
	}

	// 内存分页：limit=2 → 2 行，total=5
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Limit: 2, Sort: "tenant_id", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total)
	require.Len(t, resp.Memberships, 2)

	// offset：limit=2 offset=3 → 2 行（最后两个），total=5
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Limit: 2, Offset: 3, Sort: "tenant_id", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total)
	require.Len(t, resp.Memberships, 2)

	// q 搜索 "eta"（匹配 beta、gamma→不匹配、delta→不匹配；只有 beta 含 eta）
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Q: "eta"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total) // 只有 beta 含 "eta"
	require.Len(t, resp.Memberships, 1)
	require.Equal(t, "beta", resp.Memberships[0].TenantName)

	// q 搜索 大小写不敏感："ALPHA" → 1 行
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Q: "ALPHA"}})
	require.NoError(t, err)
	require.Equal(t, uint32(1), resp.Total)
	require.Equal(t, "alpha", resp.Memberships[0].TenantName)

	// sort=tenant_name asc → 按名字升序
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Sort: "tenant_name", Order: "asc"}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total)
	require.Len(t, resp.Memberships, 5)
	names := make([]string, len(resp.Memberships))
	for i, m := range resp.Memberships {
		names[i] = m.TenantName
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	require.Equal(t, sorted, names)

	// sort=tenant_name desc → 按名字降序
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Sort: "tenant_name", Order: "desc"}})
	require.NoError(t, err)
	require.Len(t, resp.Memberships, 5)
	descNames := make([]string, len(resp.Memberships))
	for i, m := range resp.Memberships {
		descNames[i] = m.TenantName
	}
	revSorted := make([]string, len(sorted))
	copy(revSorted, sorted)
	// 反转
	for i, j := 0, len(revSorted)-1; i < j; i, j = i+1, j-1 {
		revSorted[i], revSorted[j] = revSorted[j], revSorted[i]
	}
	require.Equal(t, revSorted, descNames)

	// 边界：offset > total → 返回空列表，total=5
	resp, err = s.ListMyTenants(myCtx, &adminv1.ListMyTenantsRequest{
		Page: &adminv1.ListPage{Limit: 2, Offset: 100}})
	require.NoError(t, err)
	require.Equal(t, uint32(5), resp.Total)
	require.Len(t, resp.Memberships, 0)

	// 不含 is_operating_plane=false（multi@sydom 非超管）
	require.False(t, resp.IsOperatingPlane)
}
