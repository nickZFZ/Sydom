package console

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// seedRoleGraphApp 种全景+模拟所需各层（relational + casbin + data_policy）。
// 返回 (appID, viewerRoleID)。
//
// 三层各自的消费者：
//   - role / permission / role_permission（relational）→ GetRoleGraph（能力+全景）+ permNameMap（模拟页能力名）
//   - casbin_rule p 行                                → SimulateRoleChange（effperm）
//   - data_policy（role viewer）                      → GetRoleGraph 数据范围 + 模拟数据预览
func seedRoleGraphApp(t *testing.T, db *sql.DB) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	roleID, err := store.InsertRole(ctx, db, appID, "viewer", "查看员")
	require.NoError(t, err)

	permID, err := store.UpsertPermission(ctx, db, appID, "order:read", "order", "read", "api", "查看订单")
	require.NoError(t, err)

	// relational：GetRoleGraph 能力列表
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, "allow"))

	// casbin p 行：SimulateRoleChange → effperm 引擎
	insertCasbinRuleC(t, db, appID, "p", "viewer", dom, "order", "read", "allow")

	// data_policy：GetRoleGraph 数据范围 + 模拟数据预览
	_, err = db.Exec(
		`INSERT INTO data_policy(app_id,subject_type,subject_id,resource,condition,effect,version)
		 VALUES($1,'role','viewer','order',$2::jsonb,'allow',1)`,
		appID, `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`)
	require.NoError(t, err)

	return appID, roleID
}

func TestConsole_RoleGraph_And_Simulate(t *testing.T) {
	ts, sessStore, db := newConsole(t)
	appID, roleID := seedRoleGraphApp(t, db)
	c, _ := loginAndCSRF(t, ts, sessStore, "root@sydom", "rootsecret")

	a := strconv.FormatInt(appID, 10)
	rid := strconv.FormatInt(roleID, 10)

	// ---- 角色全景页 ----
	resp, err := c.Get(ts.URL + "/ops/apps/" + a + "/roles/" + rid + "/graph")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "查看订单")   // 能力业务名（GetRoleGraph 真实 name 经 capabilityName）
	require.Contains(t, body, "$user.") // 数据范围符号谓词（conditionPredicate）
	require.NotContains(t, body, "app_secret")

	// ---- 模拟 diff 页（bind_user）----
	resp, err = c.Get(ts.URL + "/ops/apps/" + a + "/roles/" + rid + "/simulate?change_type=bind_user&user_id=bob")
	require.NoError(t, err)
	body = readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "bob")
	require.Contains(t, body, "查看订单")   // 新增能力业务名（经 permNameMap.label 真实 name）
	require.Contains(t, body, "$user.") // 新增数据范围符号谓词
}
