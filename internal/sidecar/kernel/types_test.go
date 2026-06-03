package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRule_Values_TrimsTrailingEmpty(t *testing.T) {
	p := Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}
	require.Equal(t, []string{"manager", "dom1", "order", "read", "allow"}, p.values())

	g := Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}
	require.Equal(t, []string{"alice", "manager", "dom1"}, g.values())
}

func TestRule_DomainValue_ByPtype(t *testing.T) {
	p := Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}
	require.Equal(t, "dom1", p.domainValue()) // p 段 domain 在 V[1]

	g := Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}
	require.Equal(t, "dom1", g.domainValue()) // g 段 domain 在 V[2]

	require.Equal(t, "", Rule{Ptype: "x"}.domainValue())
}

func TestNoopApplier_SatisfiesInterfaceAndNoPanic(t *testing.T) {
	var a DataPolicyApplier = noopApplier{}
	require.NotPanics(t, func() {
		a.ApplySnapshot([]DataPolicy{{ID: 1}})
		a.ApplyChange(ChangeAdd, DataPolicy{ID: 2})
	})
}
