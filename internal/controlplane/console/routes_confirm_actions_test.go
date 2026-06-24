package console

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 任务 4：其余 7 个破坏性动作的「无 confirmed → 渲确认页、底层未执行」用例（有齿，查 DB）。
// 每个用例都断言：HTTP 200 + 含该动作的业务语言确认问句子串 + 含 name="confirmed" value="1"，
// 且底层状态/行数不变（确认门确实拦在了真正执行之前）。

// ---- 1. removeInheritance（doWrite 类）----
func TestConfirm_RemoveInheritance_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	childID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "clerk", "店员")
	parentID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")
	// 经 console 建继承（PRG）。
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/inheritances", appID),
		url.Values{"csrf_token": {csrf}, "child_role_id": {fmt.Sprint(childID)}, "parent_role_id": {fmt.Sprint(parentID)}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	countInh := func() int {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM role_inheritance WHERE app_id=$1 AND child_role_id=$2 AND parent_role_id=$3`,
			appID, childID, parentID).Scan(&n))
		return n
	}
	require.Equal(t, 1, countInh(), "前置：继承应已建立")

	// 无 confirmed POST → 确认页，继承仍在。
	resp2, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/inheritances/remove", appID),
		url.Values{"csrf_token": {csrf}, "child_role_id": {fmt.Sprint(childID)}, "parent_role_id": {fmt.Sprint(parentID)}})
	require.NoError(t, err)
	body := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, body, "确定移除该继承关系吗？")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Equal(t, 1, countInh(), "无 confirmed 时继承不应被移除")
}

// ---- 2. revokeAdminGrant（doWrite 类）----
func TestConfirm_RevokeAdminGrant_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建管理角色 + 一条授权（经 console）。
	resp, err := c.PostForm(ts.URL+"/admin-roles", url.Values{"csrf_token": {csrf}, "code": {"g-admin"}, "name": {"n"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var roleID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code=$1`, "g-admin").Scan(&roleID))
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/admin-roles/%d/grants", roleID),
		url.Values{"csrf_token": {csrf}, "domain": {"*"}, "resource": {"role"}, "action": {"read"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	countGrant := func() int {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM admin_role_grant WHERE role_id=$1 AND domain=$2 AND resource=$3 AND action=$4`,
			roleID, "*", "role", "read").Scan(&n))
		return n
	}
	require.Equal(t, 1, countGrant(), "前置：授权应已建立")

	resp2, err := c.PostForm(ts.URL+fmt.Sprintf("/admin-roles/%d/revoke-grant", roleID),
		url.Values{"csrf_token": {csrf}, "domain": {"*"}, "resource": {"role"}, "action": {"read"}})
	require.NoError(t, err)
	body := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, body, "确定撤销该管理员授权吗？此操作立即生效。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Equal(t, 1, countGrant(), "无 confirmed 时授权不应被撤销")
}

// ---- 3. unbindOperatorRole（doWrite 类）----
func TestConfirm_UnbindOperatorRole_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建操作员（一次性 secret 页）。
	resp, err := c.PostForm(ts.URL+"/operators", url.Values{"csrf_token": {csrf}, "principal": {"bob@ops"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	var opID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_operator WHERE principal=$1`, "bob@ops").Scan(&opID))
	// 建管理角色。
	resp, err = c.PostForm(ts.URL+"/admin-roles", url.Values{"csrf_token": {csrf}, "code": {"u-admin"}, "name": {"n"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var roleID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code=$1`, "u-admin").Scan(&roleID))
	// 绑角色（domain="*"）。
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/operators/%d/roles", opID),
		url.Values{"csrf_token": {csrf}, "role_id": {fmt.Sprint(roleID)}, "domain": {"*"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	countBind := func() int {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM admin_subject_role WHERE operator_id=$1 AND role_id=$2 AND domain=$3`,
			opID, roleID, "*").Scan(&n))
		return n
	}
	require.Equal(t, 1, countBind(), "前置：绑定应已建立")

	resp2, err := c.PostForm(ts.URL+fmt.Sprintf("/operators/%d/unbind-role", opID),
		url.Values{"csrf_token": {csrf}, "role_id": {fmt.Sprint(roleID)}, "domain": {"*"}})
	require.NoError(t, err)
	body := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, body, "确定解绑该算子角色吗？此操作立即生效。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Equal(t, 1, countBind(), "无 confirmed 时绑定不应被解绑")
}

// ---- 4. opsDeleteTenantTemplate（doWrite 类）----
func TestConfirm_DeleteTenantTemplate_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	seedTenantTemplateApp(t, db, appID)
	u := strconv.FormatUint(uint64(appID), 10)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 存为模板（PRG）。
	resp, err := c.PostForm(ts.URL+"/ops/apps/"+u+"/template-captures",
		url.Values{"csrf_token": {csrf}, "name": {"待删模板"}, "description": {"x"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var tplID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant_template WHERE name=$1`, "待删模板").Scan(&tplID))

	countTpl := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tenant_template WHERE id=$1`, tplID).Scan(&n))
		return n
	}
	require.Equal(t, 1, countTpl(), "前置：模板应已建立")

	resp2, err := c.PostForm(
		ts.URL+"/ops/apps/"+u+"/tenant-templates/"+strconv.FormatInt(tplID, 10)+"/delete",
		url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	body := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, body, "确定删除该模板吗？此操作不可撤销。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Equal(t, 1, countTpl(), "无 confirmed 时模板不应被删除")
}

// ---- 5a. setAppStatus 停用（条件类，status=2 触发门）----
func TestConfirm_SetAppStatus_Disable_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	appStatus := func() uint32 {
		var s uint32
		require.NoError(t, db.QueryRow(`SELECT status FROM application WHERE id=$1`, appID).Scan(&s))
		return s
	}
	require.Equal(t, uint32(1), appStatus(), "前置：新建 app 应为启用(1)")

	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/status", appID),
		url.Values{"csrf_token": {csrf}, "status": {"2"}}) // 停用，无 confirmed
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确定停用该应用吗？停用后将拒绝该应用的写操作。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Equal(t, uint32(1), appStatus(), "无 confirmed 时停用不应执行（仍启用）")
}

// ---- 5b. setAppStatus 启用（条件门对照：status=1 不触发门，直接执行）----
func TestConfirm_SetAppStatus_Enable_NoConfirmed_Executes(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 先把 app 置停用（带 confirmed 过门）。
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/status", appID),
		url.Values{"csrf_token": {csrf}, "status": {"2"}, "confirmed": {"1"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	resp.Body.Close()

	appStatus := func() uint32 {
		var s uint32
		require.NoError(t, db.QueryRow(`SELECT status FROM application WHERE id=$1`, appID).Scan(&s))
		return s
	}
	require.Equal(t, uint32(2), appStatus(), "前置：应已停用(2)")

	// 启用（status=1，无 confirmed）→ 不触发门，直接 PRG 执行。
	resp2, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/status", appID),
		url.Values{"csrf_token": {csrf}, "status": {"1"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode) // 启用不需确认，直接 doWrite PRG
	resp2.Body.Close()
	require.Equal(t, uint32(1), appStatus(), "启用无需确认，应直接执行(1)")
}

// ---- 6. rotateAppSecret（一次性 secret 专管线类）----
func TestConfirm_RotateAppSecret_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	secretEnc := func() []byte {
		var b []byte
		require.NoError(t, db.QueryRow(`SELECT app_secret_enc FROM application WHERE id=$1`, appID).Scan(&b))
		return b
	}
	before := secretEnc()

	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/rotate-secret", appID),
		url.Values{"csrf_token": {csrf}}) // 无 confirmed
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确定轮换应用凭据吗？旧凭据将立即失效。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.NotContains(t, body, "已轮换") // 未落到一次性 secret 页
	require.Equal(t, before, secretEnc(), "无 confirmed 时 secret 不应轮换")
}

// ---- 7. resetOperatorSecret（一次性 secret 专管线类）----
func TestConfirm_ResetOperatorSecret_NoConfirmed_NotExecuted(t *testing.T) {
	ts, store, db := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.PostForm(ts.URL+"/operators", url.Values{"csrf_token": {csrf}, "principal": {"carol@ops"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	var opID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_operator WHERE principal=$1`, "carol@ops").Scan(&opID))

	secretEnc := func() []byte {
		var b []byte
		require.NoError(t, db.QueryRow(`SELECT secret_enc FROM admin_operator WHERE id=$1`, opID).Scan(&b))
		return b
	}
	before := secretEnc()

	resp2, err := c.PostForm(ts.URL+"/operators/"+strconv.FormatInt(opID, 10)+"/reset-secret",
		url.Values{"csrf_token": {csrf}}) // 无 confirmed
	require.NoError(t, err)
	body := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, body, "确定重置该算子凭据吗？旧凭据将立即失效。")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.NotContains(t, body, "已重置") // 未落到一次性 secret 页
	require.Equal(t, before, secretEnc(), "无 confirmed 时 secret 不应重置")
}
