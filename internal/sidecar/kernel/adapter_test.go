package kernel

import (
	"testing"

	"github.com/casbin/casbin/v3/persist"
	"github.com/stretchr/testify/require"
)

func TestMemoryAdapter_ImplementsInterfaces(t *testing.T) {
	var (
		_ persist.Adapter         = (*memoryAdapter)(nil)
		_ persist.BatchAdapter    = (*memoryAdapter)(nil)
		_ persist.FilteredAdapter = (*memoryAdapter)(nil)
	)
}

func TestMemoryAdapter_LoadPolicy_LoadsAllHeldRules(t *testing.T) {
	rules := []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
	}
	a := newMemoryAdapter(rules)
	m, err := buildModel()
	require.NoError(t, err)
	require.NoError(t, a.LoadPolicy(m))

	ok, _ := m.HasPolicy("p", "p", []string{"manager", "dom1", "order", "read", "allow"})
	require.True(t, ok)
	ok, _ = m.HasPolicy("g", "g", []string{"alice", "manager", "dom1"})
	require.True(t, ok)
	require.False(t, a.IsFiltered())
}

func TestMemoryAdapter_LoadFilteredPolicy_FiltersByDomain(t *testing.T) {
	rules := []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom2", "order", "read", "allow", ""}},
	}
	a := newMemoryAdapter(rules)
	m, err := buildModel()
	require.NoError(t, err)
	require.NoError(t, a.LoadFilteredPolicy(m, "dom1"))

	ok, _ := m.HasPolicy("p", "p", []string{"manager", "dom1", "order", "read", "allow"})
	require.True(t, ok)
	ok, _ = m.HasPolicy("p", "p", []string{"manager", "dom2", "order", "read", "allow"})
	require.False(t, ok, "外域行不应加载")
	require.True(t, a.IsFiltered())
}

func TestMemoryAdapter_WritesAreNoop(t *testing.T) {
	a := newMemoryAdapter([]Rule{{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}})
	require.NoError(t, a.AddPolicy("p", "p", []string{"x"}))
	require.NoError(t, a.RemovePolicy("p", "p", []string{"x"}))
	require.NoError(t, a.AddPolicies("p", "p", [][]string{{"x"}}))
	require.NoError(t, a.RemovePolicies("p", "p", [][]string{{"x"}}))
	require.NoError(t, a.RemoveFilteredPolicy("p", "p", 0, "x"))
	require.NoError(t, a.SavePolicy(nil))
	require.Len(t, a.rules, 1, "no-op 写不得改动持有快照")
}
