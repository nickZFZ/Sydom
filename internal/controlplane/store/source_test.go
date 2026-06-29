package store_test

import (
	"context"
	"encoding/json"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// ── UpsertPermissionWithSource + ListAppPermissionsWithSource ──

func TestUpsertPermissionWithSource_InsertAndUpdate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "perm-src", "perm-domain", "perm-key")
	ctx := context.Background()

	// 首次插入 source=iac
	id1, err := store.UpsertPermissionWithSource(ctx, db, appID,
		"p.order.read", "order", "read", "api", "读订单", "订单读取权限", "iac")
	require.NoError(t, err)
	require.Positive(t, id1)

	perms, err := store.ListAppPermissionsWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, perms, 1)
	require.Equal(t, "p.order.read", perms[0].Code)
	require.Equal(t, "iac", perms[0].Source)
	require.Equal(t, "读订单", perms[0].Name)

	// 再 upsert 同 code 改 name → 仍单行，name 已更新
	id2, err := store.UpsertPermissionWithSource(ctx, db, appID,
		"p.order.read", "order", "read", "api", "读订单V2", "新描述", "iac")
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	perms2, err := store.ListAppPermissionsWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, perms2, 1)
	require.Equal(t, "读订单V2", perms2[0].Name)
	require.Equal(t, "iac", perms2[0].Source)
}

// ── InsertRoleWithSource + UpdateRoleMeta + ListAppRolesWithSource ──

func TestInsertRoleWithSource_AndUpdateMeta(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "role-src", "role-domain", "role-key")
	ctx := context.Background()

	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "r.admin", "管理员", "iac")
	require.NoError(t, err)
	require.Positive(t, roleID)

	roles, err := store.ListAppRolesWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	require.Equal(t, roleID, roles[0].ID)
	require.Equal(t, "r.admin", roles[0].Code)
	require.Equal(t, "iac", roles[0].Source)

	// UpdateRoleMeta 改 name/description
	require.NoError(t, store.UpdateRoleMeta(ctx, db, appID, roleID, "超级管理员", "拥有所有权限"))

	roles2, err := store.ListAppRolesWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, roles2, 1)
	require.Equal(t, "超级管理员", roles2[0].Name)
	require.Equal(t, "拥有所有权限", roles2[0].Description)
}

// ── AdoptPermissionSource ──

func TestAdoptPermissionSource_OnlyFlipsManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "adopt-perm", "adopt-perm-d", "adopt-perm-k")
	ctx := context.Background()

	// 插入三行：manual、iac、auto
	_, err := db.ExecContext(ctx,
		`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		 VALUES ($1,'p.manual','order','read','api','手动权限','manual')`, appID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		 VALUES ($1,'p.iac','order','read','api','IaC权限','iac')`, appID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		 VALUES ($1,'p.auto','order','read','api','自动权限','auto')`, appID)
	require.NoError(t, err)

	// 正路径：manual → iac
	require.NoError(t, store.AdoptPermissionSource(ctx, db, appID, "p.manual"))
	var src string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.manual").Scan(&src))
	require.Equal(t, "iac", src)

	// 负路径：iac 行 adopt 不变
	require.NoError(t, store.AdoptPermissionSource(ctx, db, appID, "p.iac"))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.iac").Scan(&src))
	require.Equal(t, "iac", src)

	// 负路径：auto 行 adopt 不变
	require.NoError(t, store.AdoptPermissionSource(ctx, db, appID, "p.auto"))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.auto").Scan(&src))
	require.Equal(t, "auto", src)
}

// ── AdoptRoleSource ──

func TestAdoptRoleSource_OnlyFlipsManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "adopt-role", "adopt-role-d", "adopt-role-k")
	ctx := context.Background()

	// 插入 manual 角色（默认 source='manual'）
	var manualRoleID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name) VALUES ($1,'r.manual','手动角色') RETURNING id`,
		appID).Scan(&manualRoleID))

	// 插入 iac 角色
	var iacRoleID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name, source) VALUES ($1,'r.iac','IaC角色','iac') RETURNING id`,
		appID).Scan(&iacRoleID))

	// 正路径：manual → iac
	require.NoError(t, store.AdoptRoleSource(ctx, db, appID, manualRoleID))
	var src string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM role WHERE app_id=$1 AND id=$2`, appID, manualRoleID).Scan(&src))
	require.Equal(t, "iac", src)

	// 负路径：iac 行 adopt 不变
	require.NoError(t, store.AdoptRoleSource(ctx, db, appID, iacRoleID))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM role WHERE app_id=$1 AND id=$2`, appID, iacRoleID).Scan(&src))
	require.Equal(t, "iac", src)
}

// ── AdoptDataPolicySource ──

func TestAdoptDataPolicySource_OnlyFlipsManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "adopt-dp", "adopt-dp-d", "adopt-dp-k")
	ctx := context.Background()

	// 插入 manual 数据策略（默认 source='manual'）
	var manualDPID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		 VALUES ($1,'role','r','order','{}'::jsonb,'allow',1) RETURNING id`, appID).Scan(&manualDPID))

	// 插入 iac 数据策略
	var iacDPID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, source, version)
		 VALUES ($1,'role','r2','order','{}'::jsonb,'allow','iac',1) RETURNING id`, appID).Scan(&iacDPID))

	// 正路径：manual → iac
	require.NoError(t, store.AdoptDataPolicySource(ctx, db, appID, manualDPID))
	var src string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM data_policy WHERE app_id=$1 AND id=$2`, appID, manualDPID).Scan(&src))
	require.Equal(t, "iac", src)

	// 负路径：iac 行 adopt 不变
	require.NoError(t, store.AdoptDataPolicySource(ctx, db, appID, iacDPID))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source FROM data_policy WHERE app_id=$1 AND id=$2`, appID, iacDPID).Scan(&src))
	require.Equal(t, "iac", src)
}

// ── UpsertDataPolicyWithSource + ListAppDataPoliciesWithSource ──

func TestUpsertDataPolicyWithSource_CreateAndUpdate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "dp-src", "dp-domain", "dp-key")
	ctx := context.Background()

	// 新建（p.ID=0, source=iac）
	p := cp.DataPolicy{
		SubjectType: "role",
		SubjectID:   "manager",
		Resource:    "order",
		Condition:   `{"op":"eq","field":"status","value":"open"}`,
		Effect:      "allow",
	}
	id1, created, err := store.UpsertDataPolicyWithSource(ctx, db, appID, p, "iac", 1)
	require.NoError(t, err)
	require.True(t, created)
	require.Positive(t, id1)

	dps, err := store.ListAppDataPoliciesWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, dps, 1)
	require.Equal(t, "iac", dps[0].Source)
	require.Equal(t, "allow", dps[0].Effect)
	require.Equal(t, "order", dps[0].Resource)

	// condition 是合法 JSON 字符串
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(dps[0].Condition), &m))

	// 按返回 id 再 upsert（p.ID=id）→ created=false，字段更新
	p2 := cp.DataPolicy{
		ID:          id1,
		SubjectType: "role",
		SubjectID:   "manager",
		Resource:    "invoice",
		Condition:   `{}`,
		Effect:      "deny",
	}
	id2, created2, err := store.UpsertDataPolicyWithSource(ctx, db, appID, p2, "iac", 2)
	require.NoError(t, err)
	require.False(t, created2)
	require.Equal(t, id1, id2)

	dps2, err := store.ListAppDataPoliciesWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, dps2, 1)
	require.Equal(t, "invoice", dps2[0].Resource)
	require.Equal(t, "deny", dps2[0].Effect)
}

// ── RoleHasUserBindings ──

func TestRoleHasUserBindings(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "binding", "binding-d", "binding-k")
	ctx := context.Background()

	roleID, err := store.InsertRole(ctx, db, appID, "r.member", "成员")
	require.NoError(t, err)

	// 无绑定 → false
	has, err := store.RoleHasUserBindings(ctx, db, appID, roleID)
	require.NoError(t, err)
	require.False(t, has)

	// 插入绑定后 → true
	require.NoError(t, store.InsertUserRoleBinding(ctx, db, appID, "user-1", roleID))
	has, err = store.RoleHasUserBindings(ctx, db, appID, roleID)
	require.NoError(t, err)
	require.True(t, has)
}

// ── ListAppDataPoliciesWithSource condition 合法 JSON ──

func TestListAppDataPoliciesWithSource_ConditionIsValidJSON(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "cond-json", "cond-domain", "cond-key")
	ctx := context.Background()

	cond := `{"op":"and","conditions":[{"field":"status","op":"eq","value":"active"}]}`
	p := cp.DataPolicy{
		SubjectType: "user",
		SubjectID:   "alice",
		Resource:    "report",
		Condition:   cond,
		Effect:      "allow",
	}
	_, _, err := store.UpsertDataPolicyWithSource(ctx, db, appID, p, "iac", 1)
	require.NoError(t, err)

	dps, err := store.ListAppDataPoliciesWithSource(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, dps, 1)

	var out map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(dps[0].Condition), &out))
	require.Equal(t, "and", out["op"])
}
