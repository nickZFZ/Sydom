package mgmt

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleEntries_CoversRuleTableAndScopes(t *testing.T) {
	// 独立复述期望的 scope→字符串映射（不经 scopeName，否则断言退化为
	// scopeName==scopeName 的恒真式，抓不到 scopeName 自身的映射漂移）。
	wantScope := map[ruleScope]string{
		scopeSystem: "system",
		scopeApp:    "app",
		scopeTenant: "tenant",
		scopeSelf:   "self",
	}
	entries := RuleEntries()
	// 覆盖全 ruleTable（一一对应，无漏无多）。
	require.Len(t, entries, len(ruleTable))
	seen := map[string]RPCDoc{}
	for _, e := range entries {
		seen[e.FullMethod] = e
	}
	for fm, r := range ruleTable {
		e, ok := seen[fm]
		require.True(t, ok, "RuleEntries 漏了 %s", fm)
		require.Equal(t, r.resource, e.Resource)
		require.Equal(t, r.action, e.Action)
		require.Equal(t, r.isWrite, e.IsWrite)
		// scope 映射具体正确（有齿：期望值来自独立表，抓 scopeName 漂移）。
		want, known := wantScope[r.scope]
		require.True(t, known, "ruleTable 出现未登记的 scope: %s", fm)
		require.Equal(t, want, e.Scope, "%s 的 scope 字符串错映射", fm)
	}
	// 稳定排序（按 FullMethod 升序）。
	require.True(t, sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].FullMethod < entries[j].FullMethod }))
}
