package dataperm

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// dp 是构造 kernel.DataPolicy 的测试助手（本包内复用）。
func dp(id uint64, stype, sid, res, cond, eff string) kernel.DataPolicy {
	return kernel.DataPolicy{ID: id, SubjectType: stype, SubjectID: sid, Resource: res, Condition: cond, Effect: eff}
}

func TestTable_ImplementsApplier(t *testing.T) {
	var _ kernel.DataPolicyApplier = (*Table)(nil)
}

func TestTable_ApplySnapshot_IndexesByResource(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"),
		dp(2, "user", "alice", "invoice", `{"field":"b","op":"EQ","value":2}`, "allow"),
	})
	got, ok := tbl.Lookup("order")
	require.True(t, ok)
	require.Len(t, got, 1)
	require.Equal(t, "manager", got[0].subjectID)
	_, ok = tbl.Lookup("nope")
	require.False(t, ok)
}

func TestTable_ApplyChange_AddUpdateRemove(t *testing.T) {
	tbl := NewTable()
	tbl.ApplyChange(kernel.ChangeAdd, dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	got, ok := tbl.Lookup("order")
	require.True(t, ok)
	require.Len(t, got, 1)

	tbl.ApplyChange(kernel.ChangeUpdate, dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":2}`, "deny"))
	got, _ = tbl.Lookup("order")
	require.Len(t, got, 1)
	require.Equal(t, "deny", got[0].effect)

	tbl.ApplyChange(kernel.ChangeRemove, dp(1, "role", "manager", "order", `{}`, "allow"))
	_, ok = tbl.Lookup("order")
	require.False(t, ok, "移除最后一条后 resource 回到未配置")
}

func TestTable_PoisonsBadPolicy(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{
		dp(1, "role", "manager", "order", `{bad json`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "weird"),
	})
	got, _ := tbl.Lookup("order")
	require.Len(t, got, 2)
	require.Error(t, got[0].parseErr, "非法 JSON 应中毒")
	require.Error(t, got[1].parseErr, "非法 effect 应中毒")
}
