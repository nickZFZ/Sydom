package dataperm

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// fakeRoles 是测试用 RoleResolver。
type fakeRoles struct {
	roles map[string][]string
	err   error
}

func (f fakeRoles) GetImplicitRolesForUser(user, dom string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.roles[user], nil
}

// newFilter 用快照构造一个挂 fakeRoles 的 Filter。
func newFilter(roles map[string][]string, pols ...kernel.DataPolicy) *Filter {
	tbl := NewTable()
	tbl.ApplySnapshot(pols)
	return NewFilter(fakeRoles{roles: roles}, tbl)
}

func TestFilterRaw_Unconfigured_NoRestriction(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}})
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "all", res.Match)
}

func TestFilterRaw_ConfiguredNoMatch_DenyAll(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"viewer"}},
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "none", res.Match)
}

func TestFilterRaw_DenyOverrides(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"HR"}`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"status","op":"EQ","value":"locked"}`, "deny"))
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "conditional", res.Match)
	require.Equal(t, OpAnd, res.Tree.Op)
	require.Equal(t, OpNot, res.Tree.Children[1].Op)
}

func TestFilter_MissingVar_FailClose(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"$user.department"}`, "allow"))
	_, err := f.FilterRaw("alice", "dom1", "order", map[string]any{})
	require.ErrorIs(t, err, ErrMissingVar)
}

func TestFilter_PoisonHit_FailClose(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{bad`, "allow"))
	_, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestFilter_ResolverError_FailClose(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow")})
	f := NewFilter(fakeRoles{err: kernel.ErrNotReady}, tbl)
	_, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.ErrorIs(t, err, kernel.ErrNotReady)
}

func TestFilter_UserSubjectMatch(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {}},
		dp(1, "user", "alice", "order", `{"field":"owner","op":"EQ","value":"$user.id"}`, "allow"))
	res, err := f.FilterRaw("alice", "dom1", "order", map[string]any{"id": "alice"})
	require.NoError(t, err)
	require.Equal(t, "conditional", res.Match)
}
