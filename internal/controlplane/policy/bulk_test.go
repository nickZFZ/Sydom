package policy_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func bulkCurrentVersion(t *testing.T, db *sql.DB, appID int64) int64 {
	t.Helper()
	var v int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v))
	return v
}

func bulkCountRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

// bulkSeedPermission 建一个权限点；action 必须按调用方区分，避免同角色下两个权限点
// 因 resource/action 相同而投影出同一条 casbin p 行(revoke 其一后 diff 仍含另一份同名规则,
// 造成误判 no-op——TestBatchRevokePermission_AtomicAndCounts 曾因两个权限点同用 "read" 撞车,
// 详见踩坑记录)。
func bulkSeedPermission(t *testing.T, db *sql.DB, appID int64, code, action string) int64 {
	t.Helper()
	id, err := store.UpsertPermission(context.Background(), db, appID, code, "order", action, "api", code)
	require.NoError(t, err)
	return id
}

// ── BatchDeleteRole ──

func TestBatchDeleteRole_AtomicAndCounts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	permID := bulkSeedPermission(t, db, appID, "p.read", "read")
	r1, _, err := mgr.CreateRole(ctx, appID, "iac:a", "A")
	require.NoError(t, err)
	r2, _, err := mgr.CreateRole(ctx, appID, "iac:b", "B")
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r1, permID, cp.EffectAllow)
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r2, permID, cp.EffectAllow)
	require.NoError(t, err)
	vBefore := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{r1, r2, 424242})
	require.NoError(t, err)
	require.Equal(t, 2, applied, "r1,r2 存在;424242 no-op")
	require.NotNil(t, d, "期望非 nil Delta(有实际删除应 bump)")
	require.Equal(t, vBefore+1, d.Version)
	require.Len(t, d.RuleRemoves, 2, "两条角色的 p 行随删除一并移除")

	require.Equal(t, 0, bulkCountRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID))
	require.Equal(t, 0, bulkCountRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1`, appID), "级联删授权")
	require.Equal(t, vBefore+1, bulkCurrentVersion(t, db, appID))
}

func TestBatchDeleteRole_AllNoOp_NoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	v0 := bulkCurrentVersion(t, db, appID)
	d, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{111, 222})
	require.NoError(t, err)
	require.Equal(t, 0, applied)
	require.Nil(t, d, "全 no-op 不应 bump(期望 nil Delta)")
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID), "版本不应变")
}

func TestBatchDeleteRole_SourceBlind(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	manualID, err := store.InsertRole(ctx, db, appID, "manual:a", "手动角色")
	require.NoError(t, err)
	iacID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:a", "IaC角色", "iac")
	require.NoError(t, err)

	_, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{manualID, iacID})
	require.NoError(t, err)
	require.Equal(t, 2, applied, "批量删除不按 source 过滤,manual/iac 两种来源都应删除")
	require.Equal(t, 0, bulkCountRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID))
}

// 并发下多个批量删除（各删不同角色）应经由 app 行锁串行化，版本严格递增，无丢失更新。
func TestBatchDeleteRole_ConcurrentSerialized(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	const n = 8
	permID := bulkSeedPermission(t, db, appID, "p.read", "read")
	roleIDs := make([]int64, n)
	for i := range roleIDs {
		rid, _, err := mgr.CreateRole(ctx, appID, "iac:r"+string(rune('A'+i)), "R")
		require.NoError(t, err)
		_, err = mgr.GrantPermission(ctx, appID, rid, permID, cp.EffectAllow)
		require.NoError(t, err)
		roleIDs[i] = rid
	}
	vBefore := bulkCurrentVersion(t, db, appID)

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for _, rid := range roleIDs {
		wg.Add(1)
		go func(rid int64) {
			defer wg.Done()
			if _, _, err := mgr.BatchDeleteRole(context.Background(), appID, []int64{rid}); err != nil {
				errCh <- err
			}
		}(rid)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	require.Equal(t, vBefore+int64(n), bulkCurrentVersion(t, db, appID))
	require.Equal(t, 0, bulkCountRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID))
}

// BC-1 原子回滚有齿：向一批注入一个会在 DB 层报错的项——用测试专属的 BEFORE DELETE 触发器
// 让删除哨兵角色时 RAISE EXCEPTION，使整批 set-based DELETE 事务在末条(删 role)失败。
// 验证整批无一落库，尤其是批次内已「先于失败」执行的级联子行（r1 的授权/绑定在同一事务里
// 已被 DELETE，原子回滚必须把它们复原）——这正是「有齿」所在——且 version 未 bump。
//
// 反向验证（对齐 M2.4 教训「回归测试须验证能捕获回归」）：本测试的齿在于「r1 的级联子行
// 残留=1」这两条断言。已实测：临时把 bulk.go 中 BatchDeleteRole 的
// store.DeleteRolesBatch(ctx, tx, ...) 改为在 m.db(autocommit) 上执行——即去掉原子性——
// 级联子删各自提交、不随外层事务回滚，这两条断言即变为 0 → 测试 FAIL；恢复为 tx 后重新 PASS。
// 故本测试确实能捕获「批量级联非原子」这一回归。
func TestBatchDeleteRole_AtomicRollback_Injected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	permID := bulkSeedPermission(t, db, appID, "p.read", "read")
	r1, _, err := mgr.CreateRole(ctx, appID, "iac:r1", "R1")
	require.NoError(t, err)
	sentinel, _, err := mgr.CreateRole(ctx, appID, "iac:sentinel", "Sentinel")
	require.NoError(t, err)
	// 给 r1 一条授权 + 一条绑定，作为级联子行「被回滚复原」的见证。
	_, err = mgr.GrantPermission(ctx, appID, r1, permID, cp.EffectAllow)
	require.NoError(t, err)
	_, err = mgr.BindUserRole(ctx, appID, "u1", r1)
	require.NoError(t, err)
	v0 := bulkCurrentVersion(t, db, appID)

	// 测试专属故障注入：删除哨兵角色(code=iac:sentinel)时抛异常，使整批 DELETE 事务失败。
	_, err = db.Exec(`CREATE OR REPLACE FUNCTION sydom_test_block_role_del() RETURNS trigger
AS $$ BEGIN RAISE EXCEPTION 'injected: blocked delete of role %', OLD.id; END; $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER sydom_test_block_role_del BEFORE DELETE ON role
FOR EACH ROW WHEN (OLD.code = 'iac:sentinel') EXECUTE FUNCTION sydom_test_block_role_del()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS sydom_test_block_role_del ON role`)
		_, _ = db.Exec(`DROP FUNCTION IF EXISTS sydom_test_block_role_del()`)
	})

	// 批量删 [r1, sentinel]：级联先删 r1 的授权/绑定，随后删 role 时哨兵触发器抛错 → 整批回滚。
	d, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{r1, sentinel})
	require.Error(t, err, "注入的触发器应使整批事务失败")
	require.Nil(t, d, "失败不产出 Delta")
	require.Equal(t, 0, applied)

	// 整批无一落库——两角色都仍在。
	require.Equal(t, 2, bulkCountRows(t, db, `SELECT count(*) FROM role WHERE app_id=$1`, appID), "两角色都应仍在")
	// 有齿点：r1 的级联子行在同一事务里已被删，原子回滚后必须复原（非原子则此处为 0）。
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1 AND role_id=$2`, appID, r1), "r1 授权应随整批回滚复原")
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND role_id=$2`, appID, r1), "r1 绑定应随整批回滚复原")
	// version 未 bump。
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID), "失败事务不应 bump 版本")
}

// ── BatchRevokePermission ──

func TestBatchRevokePermission_AtomicAndCounts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	r, _, err := mgr.CreateRole(ctx, appID, "iac:r", "R")
	require.NoError(t, err)
	p1 := bulkSeedPermission(t, db, appID, "p.read", "read")
	p2 := bulkSeedPermission(t, db, appID, "p.write", "write")
	_, err = mgr.GrantPermission(ctx, appID, r, p1, cp.EffectAllow)
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r, p2, cp.EffectAllow)
	require.NoError(t, err)
	vBefore := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchRevokePermission(ctx, appID, []store.GrantPair{
		{RoleID: r, PermissionID: p1}, {RoleID: r, PermissionID: 999999},
	})
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.NotNil(t, d)
	require.Equal(t, vBefore+1, d.Version)
	require.Len(t, d.RuleRemoves, 1)

	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1`, appID))
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM role_permission WHERE app_id=$1 AND permission_id=$2`, appID, p2))
}

func TestBatchRevokePermission_AllNoOp_NoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	r, _, err := mgr.CreateRole(ctx, appID, "iac:r", "R")
	require.NoError(t, err)
	v0 := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchRevokePermission(ctx, appID, []store.GrantPair{{RoleID: r, PermissionID: 999999}})
	require.NoError(t, err)
	require.Equal(t, 0, applied)
	require.Nil(t, d)
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID))
}

// ── BatchRemoveRoleInheritance ──

func TestBatchRemoveRoleInheritance_AtomicAndCounts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	parent, _, err := mgr.CreateRole(ctx, appID, "iac:parent", "Parent")
	require.NoError(t, err)
	childA, _, err := mgr.CreateRole(ctx, appID, "iac:childA", "ChildA")
	require.NoError(t, err)
	childB, _, err := mgr.CreateRole(ctx, appID, "iac:childB", "ChildB")
	require.NoError(t, err)
	_, err = mgr.AddRoleInheritance(ctx, appID, childA, parent)
	require.NoError(t, err)
	_, err = mgr.AddRoleInheritance(ctx, appID, childB, parent)
	require.NoError(t, err)
	vBefore := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchRemoveRoleInheritance(ctx, appID, []store.InheritancePair{
		{ChildRoleID: childA, ParentRoleID: parent}, {ChildRoleID: 999999, ParentRoleID: parent},
	})
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.NotNil(t, d)
	require.Equal(t, vBefore+1, d.Version)
	require.Len(t, d.RuleRemoves, 1)

	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM role_inheritance WHERE app_id=$1`, appID))
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM role_inheritance WHERE app_id=$1 AND child_role_id=$2`, appID, childB))
}

func TestBatchRemoveRoleInheritance_AllNoOp_NoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	parent, _, err := mgr.CreateRole(ctx, appID, "iac:parent", "Parent")
	require.NoError(t, err)
	child, _, err := mgr.CreateRole(ctx, appID, "iac:child", "Child")
	require.NoError(t, err)
	v0 := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchRemoveRoleInheritance(ctx, appID,
		[]store.InheritancePair{{ChildRoleID: child, ParentRoleID: parent}})
	require.NoError(t, err)
	require.Equal(t, 0, applied, "该继承边本不存在")
	require.Nil(t, d)
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID))
}

// ── BatchUnbindUserRole ──

func TestBatchUnbindUserRole_AtomicAndCounts(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	r, _, err := mgr.CreateRole(ctx, appID, "iac:r", "R")
	require.NoError(t, err)
	_, err = mgr.BindUserRole(ctx, appID, "u1", r)
	require.NoError(t, err)
	_, err = mgr.BindUserRole(ctx, appID, "u2", r)
	require.NoError(t, err)
	vBefore := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchUnbindUserRole(ctx, appID, []store.UserRolePair{
		{UserID: "u1", RoleID: r}, {UserID: "nobody", RoleID: r},
	})
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.NotNil(t, d)
	require.Equal(t, vBefore+1, d.Version)
	require.Len(t, d.RuleRemoves, 1)

	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1`, appID))
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND user_id='u2'`, appID))
}

func TestBatchUnbindUserRole_AllNoOp_NoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	r, _, err := mgr.CreateRole(ctx, appID, "iac:r", "R")
	require.NoError(t, err)
	v0 := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchUnbindUserRole(ctx, appID, []store.UserRolePair{{UserID: "nobody", RoleID: r}})
	require.NoError(t, err)
	require.Equal(t, 0, applied)
	require.Nil(t, d)
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID))
}

// ── BatchDeleteDataPolicy（data 写变体：始终 bump，非空即 bump，与单数 DeleteDataPolicy 一致） ──

func TestBatchDeleteDataPolicy_AlwaysBumps(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	d1, err := mgr.UpsertDataPolicy(ctx, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	require.NotNil(t, d1)
	p1ID := d1.DataChanges[0].Policy.ID

	d2, err := mgr.UpsertDataPolicy(ctx, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "clerk", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	p2ID := d2.DataChanges[0].Policy.ID
	vBefore := bulkCurrentVersion(t, db, appID)

	d, applied, err := mgr.BatchDeleteDataPolicy(ctx, appID, []int64{p1ID, 999999})
	require.NoError(t, err)
	require.Equal(t, 1, applied)
	require.NotNil(t, d, "data 写变体应 bump")
	require.Equal(t, vBefore+1, d.Version)
	require.Len(t, d.DataChanges, 1)
	require.Equal(t, cp.ChangeRemove, d.DataChanges[0].Op)
	require.Equal(t, p1ID, d.DataChanges[0].Policy.ID)

	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM data_policy WHERE app_id=$1`, appID))
	require.Equal(t, 1, bulkCountRows(t, db, `SELECT count(*) FROM data_policy WHERE app_id=$1 AND id=$2`, appID, p2ID))
}

// BC-2 例外核心断言：即便所给 id 全不存在（applied=0），只要输入非空就仍 bump——
// 与其余 4 个批量方法"全 no-op 不 bump"的一般规则相反，因 data_policy 不参与投影 diff。
func TestBatchDeleteDataPolicy_AllNonExistent_StillBumps(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	v0 := bulkCurrentVersion(t, db, appID)
	d, applied, err := mgr.BatchDeleteDataPolicy(ctx, appID, []int64{111, 222})
	require.NoError(t, err)
	require.Equal(t, 0, applied, "两个 id 均不存在")
	require.NotNil(t, d, "非空批量即始终 bump,即便无实际删除")
	require.Equal(t, v0+1, d.Version)
	require.Empty(t, d.DataChanges)
	require.Equal(t, v0+1, bulkCurrentVersion(t, db, appID))
}

// 空切片显式短路为真 no-op（不同于"全不存在"的 id 列表）。
func TestBatchDeleteDataPolicy_EmptyInput_NoOp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	v0 := bulkCurrentVersion(t, db, appID)
	d, applied, err := mgr.BatchDeleteDataPolicy(ctx, appID, nil)
	require.NoError(t, err)
	require.Equal(t, 0, applied)
	require.Nil(t, d)
	require.Equal(t, v0, bulkCurrentVersion(t, db, appID))
}

func TestBatchDeleteDataPolicy_SourceBlind(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	mgr := policy.NewPolicyManager(db, nil)

	manualID, _, err := store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	}, 1)
	require.NoError(t, err)
	iacID, _, err := store.UpsertDataPolicyWithSource(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "clerk", Resource: "order", Condition: `{"op":"ALL"}`,
	}, "iac", 1)
	require.NoError(t, err)

	_, applied, err := mgr.BatchDeleteDataPolicy(ctx, appID, []int64{manualID, iacID})
	require.NoError(t, err)
	require.Equal(t, 2, applied, "批量删除不按 source 过滤")
	require.Equal(t, 0, bulkCountRows(t, db, `SELECT count(*) FROM data_policy WHERE app_id=$1`, appID))
}
