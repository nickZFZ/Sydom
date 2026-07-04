package console

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 任务5：Console 多选批量操作——覆盖 5 个批量删除/撤销 handler 的确认门 + PRG + 友好空选处理。
// 每个用例都遵循同一骨架：
//   1) 先经既有单数 handler 建若干行；
//   2) 无 confirmed 的批量提交 → 200 确认页 + 底层行数不变（确认门真拦住）；
//   3) confirmed=1 → 303 PRG + 底层行数归零（批量真执行）。

// parseUserRoleRefs 必须按【末】冒号切分：user_id 是自由文本可含冒号（联合身份如
// "google-oauth2:110169..."），role_id 恒纯数字。用首冒号切分会在 user_id 含冒号时把该绑定
// 静默丢弃（批量报成功却漏解绑=权限残留）。本测试对旧的 strings.Cut(首冒号)实现会 FAIL。
func TestParseUserRoleRefs_UserIDWithColon(t *testing.T) {
	refs := parseUserRoleRefs([]string{"google-oauth2:110169484474386276334:42"})
	require.Len(t, refs, 1, "含冒号的 user_id 不应被静默丢弃(否则批量解绑漏项、权限残留)")
	require.Equal(t, "google-oauth2:110169484474386276334", refs[0].UserId)
	require.Equal(t, int64(42), refs[0].RoleId)

	// 普通 user_id(无冒号)仍正确。
	refs2 := parseUserRoleRefs([]string{"alice@corp:7"})
	require.Len(t, refs2, 1)
	require.Equal(t, "alice@corp", refs2[0].UserId)
	require.Equal(t, int64(7), refs2[0].RoleId)

	// 非法项(无冒号/role 非数字/user_id 空)被丢弃。
	require.Empty(t, parseUserRoleRefs([]string{"nocolon", "user:notnum", ":42"}))
}

// ---- 1. roles：确认门全套 ----

func TestConsole_BatchDeleteRole_ConfirmGate(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	id1 := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "r1", "角色1")
	id2 := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "r2", "角色2")

	countRoles := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role WHERE app_id=$1`, appID).Scan(&n))
		return n
	}
	require.Equal(t, 2, countRoles(), "前置：两个角色应已建立")

	// 无 confirmed → 确认页，不落库。
	form := url.Values{"csrf_token": {csrf}, "ids": {fmt.Sprint(id1), fmt.Sprint(id2)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles/batch-delete", appID), form)
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, "确认门应渲确认页(200)")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Contains(t, body, fmt.Sprintf(`value="%d"`, id1))
	require.Contains(t, body, fmt.Sprintf(`value="%d"`, id2))
	require.Contains(t, body, "确认批量移除选中的角色", "确认页应显示批量专属业务文案(confirmPrompts 命中,非通用兜底语)")
	require.Equal(t, 2, countRoles(), "未确认不应删")

	// confirmed=1 → 批量删 + 303 PRG。
	form.Set("confirmed", "1")
	resp2, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles/batch-delete", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode, "确认后应 PRG(303)")
	require.Equal(t, 0, countRoles(), "确认后应删空")
}

// 勾选 0 项：友好提示，不 500，不空调 RPC，不落库变更。
func TestConsole_BatchDeleteRole_EmptySelection_Friendly(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	_ = mustCreateRole(t, c, ts, db, csrf, uint64(appID), "keep", "留存角色")

	form := url.Values{"csrf_token": {csrf}, "confirmed": {"1"}} // 无 ids
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles/batch-delete", appID), form)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusInternalServerError, resp.StatusCode, "空选不应 500")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "空选应友好 400")

	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n, "空选不应触发任何删除")
}

// ---- 2. grants：确认后批量撤销生效 + PRG（复合 ids："role_id:permission_id"）----

func TestConsole_BatchRevokePermission_Confirmed(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")
	perm1 := mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "order.read")
	perm2 := mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "order.write")

	for _, permID := range []int64{perm1, perm2} {
		g := url.Values{"csrf_token": {csrf}, "role_id": {fmt.Sprint(roleID)}, "permission_id": {fmt.Sprint(permID)}, "eft": {"allow"}}
		resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/grants", appID), g)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	}

	countGrants := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role_permission WHERE app_id=$1 AND role_id=$2`, appID, roleID).Scan(&n))
		return n
	}
	require.Equal(t, 2, countGrants(), "前置：两条授权应已建立")

	form := url.Values{
		"csrf_token": {csrf}, "confirmed": {"1"},
		"ids": {fmt.Sprintf("%d:%d", roleID, perm1), fmt.Sprintf("%d:%d", roleID, perm2)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/grants/batch-delete", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "确认后应 PRG(303)")
	require.Equal(t, 0, countGrants(), "确认后应撤销空")
}

// ---- 3. inheritances：确认后批量移除生效 + PRG（复合 ids："child_role_id:parent_role_id"）----

func TestConsole_BatchRemoveInheritance_Confirmed(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	child := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "clerk", "店员")
	parent1 := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")
	parent2 := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "director", "总监")

	for _, parentID := range []int64{parent1, parent2} {
		resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/inheritances", appID),
			url.Values{"csrf_token": {csrf}, "child_role_id": {fmt.Sprint(child)}, "parent_role_id": {fmt.Sprint(parentID)}})
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	}

	countInh := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role_inheritance WHERE app_id=$1 AND child_role_id=$2`, appID, child).Scan(&n))
		return n
	}
	require.Equal(t, 2, countInh(), "前置：两条继承应已建立")

	form := url.Values{
		"csrf_token": {csrf}, "confirmed": {"1"},
		"ids": {fmt.Sprintf("%d:%d", child, parent1), fmt.Sprintf("%d:%d", child, parent2)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/inheritances/batch-delete", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "确认后应 PRG(303)")
	require.Equal(t, 0, countInh(), "确认后应移除空")
}

// ---- 4. bindings：确认后批量解绑生效 + PRG（复合 ids："user_id:role_id"）----

func TestConsole_BatchUnbindUser_Confirmed(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")

	for _, userID := range []string{"alice@corp", "bob@corp"} {
		resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings", appID),
			url.Values{"csrf_token": {csrf}, "user_id": {userID}, "role_id": {fmt.Sprint(roleID)}})
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	}

	countBindings := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM user_role_binding WHERE app_id=$1 AND role_id=$2`, appID, roleID).Scan(&n))
		return n
	}
	require.Equal(t, 2, countBindings(), "前置：两条绑定应已建立")

	form := url.Values{
		"csrf_token": {csrf}, "confirmed": {"1"},
		"ids": {fmt.Sprintf("alice@corp:%d", roleID), fmt.Sprintf("bob@corp:%d", roleID)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings/batch-delete", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "确认后应 PRG(303)")
	require.Equal(t, 0, countBindings(), "确认后应解绑空")
}

// ---- 5. data-policies：确认后批量删除生效 + PRG（裸 ids）----

func TestConsole_BatchDeleteDataPolicy_Confirmed(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	var ids []int64
	for _, resource := range []string{"order", "invoice"} {
		form := url.Values{
			"csrf_token": {csrf}, "id": {"0"},
			"subject_type": {"role"}, "subject_id": {"clerk"},
			"resource":  {resource},
			"condition": {`{"op":"and","children":[]}`},
			"effect":    {"allow"},
		}
		resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
		var id int64
		require.NoError(t, db.QueryRow(`SELECT id FROM data_policy WHERE app_id=$1 AND resource=$2`, appID, resource).Scan(&id))
		ids = append(ids, id)
	}

	countDP := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&n))
		return n
	}
	require.Equal(t, 2, countDP(), "前置：两条数据策略应已建立")

	form := url.Values{"csrf_token": {csrf}, "confirmed": {"1"}, "ids": {fmt.Sprint(ids[0]), fmt.Sprint(ids[1])}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies/batch-delete", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "确认后应 PRG(303)")
	require.Equal(t, 0, countDP(), "确认后应删空")
}
