package projection

import (
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
)

func r(ptype string, vs ...string) cp.Rule {
	var v [6]string
	copy(v[:], vs)
	return cp.Rule{Ptype: ptype, V: v}
}

func TestDiff_AddsAndRemoves(t *testing.T) {
	current := []cp.Rule{
		r("p", "admin", "d", "order", "read"),
		r("g", "alice", "admin", "d"),
	}
	desired := []cp.Rule{
		r("p", "admin", "d", "order", "read"),  // 不变
		r("p", "admin", "d", "order", "write"), // 新增
	}
	adds, removes := Diff(current, desired)

	require.ElementsMatch(t, []cp.Rule{r("p", "admin", "d", "order", "write")}, adds)
	require.ElementsMatch(t, []cp.Rule{r("g", "alice", "admin", "d")}, removes)
}

func TestDiff_Empty(t *testing.T) {
	rules := []cp.Rule{r("p", "admin", "d", "order", "read")}
	adds, removes := Diff(rules, rules)
	require.Empty(t, adds)
	require.Empty(t, removes)
}
