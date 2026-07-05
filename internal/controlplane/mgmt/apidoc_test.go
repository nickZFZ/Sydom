package mgmt

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleEntries_CoversRuleTableAndScopes(t *testing.T) {
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
	}
	// scope 映射为可读字符串（非空、属已知集）。
	for _, e := range entries {
		require.Contains(t, []string{"system", "app", "tenant", "self"}, e.Scope, "未知 scope: %s", e.FullMethod)
	}
	// 稳定排序（按 FullMethod 升序）。
	require.True(t, sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].FullMethod < entries[j].FullMethod }))
}
