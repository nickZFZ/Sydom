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
	// 有 REST 路由的条目应带 method+path（抽查一条已知有 REST 的写 RPC）。
	upsertDP, ok := byFM["/sydom.admin.v1.AdminService/UpsertDataPolicy"]
	require.True(t, ok)
	require.NotEmpty(t, upsertDP.RESTPath, "UpsertDataPolicy 应有 REST 路由信息")
}
