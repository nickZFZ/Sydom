package dataperm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterSQL_Unconfigured_Empty(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}})
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "", res.SQL)
	require.Empty(t, res.Args)
}

func TestFilterSQL_DenyAll(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"viewer"}},
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "1=0", res.SQL)
}

func TestFilterSQL_ParamizedAndDenyOverrides(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"$user.department"}`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"status","op":"IN","value":["locked","void"]}`, "deny"))
	res, err := f.FilterSQL("alice", "dom1", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL)
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}

func TestFilterSQL_Operators(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"op":"OR","children":[
			{"field":"amount","op":"BETWEEN","value":[10,20]},
			{"field":"note","op":"IS_NULL"}
		]}`, "allow"))
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "(amount BETWEEN ? AND ? OR note IS NULL)", res.SQL)
	require.Equal(t, []any{float64(10), float64(20)}, res.Args)
}
