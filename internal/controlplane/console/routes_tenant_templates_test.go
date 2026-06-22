package console

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// seedTenantTemplateApp 为「我的模板」用例播种一个最小授权模型：1 角色 + 1 权限点 + 1 条
// role 主体 data_policy（condition 用符号化 $user.，对齐 TT-5）。注意 data_policy 主体
// subject_id 必须 = role.code（与 casbin/dataperm 主体匹配一致）。
func seedTenantTemplateApp(t *testing.T, db *sql.DB, appID int64) {
	t.Helper()
	ctx := context.Background()
	permID, err := store.UpsertPermission(ctx, db, appID, "order:read", "order", "read", "app", "查看订单")
	require.NoError(t, err)
	roleID, err := store.InsertRole(ctx, db, appID, "viewer", "查看员")
	require.NoError(t, err)
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, "allow"))
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role",
		SubjectID:   "viewer",
		Resource:    "order",
		Condition:   `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`,
		Effect:      "allow",
	}, 1)
	require.NoError(t, err)
}

// TestConsole_TenantTemplate_SaveAndPreview：存为模板（PRG）→「我的模板」区见模板名 →
// 预览渲染符号谓词（$user. 保留，TT-5），且页面不含 secret。
func TestConsole_TenantTemplate_SaveAndPreview(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	seedTenantTemplateApp(t, db, appID)
	u := strconv.FormatUint(uint64(appID), 10)

	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 1. 存为模板（POST 带 csrf）→ PRG 303。
	resp, err := c.PostForm(ts.URL+"/ops/apps/"+u+"/template-captures", url.Values{
		"csrf_token":  {csrf},
		"name":        {"标准后台"},
		"description": {"通用"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG

	// 2. 列表页：「我的模板」区见模板名。
	page, err := c.Get(ts.URL + "/ops/apps/" + u + "/templates")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	require.Contains(t, readBody(t, page), "标准后台")

	// 3. 取 template_id，预览：符号谓词保留 $user.，且无 secret/枚举真实值。
	var tplID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant_template WHERE name=$1`, "标准后台").Scan(&tplID))

	preview, err := c.Get(ts.URL + "/ops/apps/" + u + "/tenant-templates/" + strconv.FormatInt(tplID, 10))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, preview.StatusCode)
	body := readBody(t, preview)
	require.Contains(t, body, "$user.")        // TT-5：符号谓词保留
	require.NotContains(t, body, "app_secret") // TT-6：绝不含 secret
}
