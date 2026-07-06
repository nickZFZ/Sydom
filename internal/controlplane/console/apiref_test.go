package console

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/stretchr/testify/require"
)

// DP-2：ruleTable 每条 RPC 都必须出现在组装的管理面参考里（漏一条即 FAIL）。
func TestBuildAPIReference_CoversEveryAdminRPC(t *testing.T) {
	ref := buildAPIReference()
	byFM := map[string]APIRefEntry{}
	for _, e := range ref {
		byFM[e.FullMethod] = e
	}
	for _, r := range mgmt.RuleEntries() {
		e, ok := byFM[r.FullMethod]
		require.True(t, ok, "管理面参考漏了 admin RPC: %s（DP-2 零漂移失败）", r.FullMethod)
		require.Equal(t, r.Resource, e.Resource)
		require.Equal(t, r.Action, e.Action)
		require.Equal(t, r.Scope, e.Scope)
		require.Equal(t, r.IsWrite, e.IsWrite)
	}
	// join 逻辑有齿：抽查一条已知多路由的写 RPC，锁死 REST 字段的具体取值——
	// 既抓 RESTMethod/RESTPath 错位，也钉死"多路由取稳定排序首条"的 tie-break
	// （UpsertDataPolicy 有 POST /data-policies 与 PUT /data-policies/{id} 两条，
	// 按 pattern 升序首条是 POST；若取末条则会变成 PUT/.../{id}，断言即失败）。
	upsertDP, ok := byFM["/sydom.admin.v1.AdminService/UpsertDataPolicy"]
	require.True(t, ok)
	require.Equal(t, "POST", upsertDP.RESTMethod, "UpsertDataPolicy 应 join 到稳定排序首条 REST 路由的动词")
	require.Equal(t, "/v1/apps/{app_id}/data-policies", upsertDP.RESTPath, "UpsertDataPolicy 应 join 到稳定排序首条 REST 路由的路径")
}
