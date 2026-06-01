package translate

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
)

func rule(ptype string, vs ...string) cp.Rule {
	var v [6]string
	copy(v[:], vs)
	return cp.Rule{Ptype: ptype, V: v}
}

func TestDeltaToProto_AddsRemovesData(t *testing.T) {
	d := cp.Delta{
		AppID:   7,
		Version: 5,
		RuleAdds: []cp.Rule{
			rule("p", "manager", "order-system", "order", "read", "allow"),
		},
		RuleRemoves: []cp.Rule{
			rule("g", "u-100", "manager", "order-system"),
		},
		DataChanges: []cp.DataPolicyChange{
			{Op: cp.ChangeAdd, Policy: cp.DataPolicy{ID: 9, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`}},
			{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: 3}},
		},
	}
	got := DeltaToProto(d)

	require.Equal(t, uint64(5), got.Version)
	require.Len(t, got.PolicyChanges, 2)

	// ADD：rule 填充，old_rule 空；尾部空串被裁（5 个值，无第 6 个空串）
	add := got.PolicyChanges[0]
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_ADD, add.Op)
	require.NotNil(t, add.Rule)
	require.Nil(t, add.OldRule)
	require.Equal(t, "p", add.Rule.Ptype)
	require.Equal(t, []string{"manager", "order-system", "order", "read", "allow"}, add.Rule.Values)

	// REMOVE：old_rule 填充，rule 空；g 行 3 个值
	rem := got.PolicyChanges[1]
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_REMOVE, rem.Op)
	require.Nil(t, rem.Rule)
	require.NotNil(t, rem.OldRule)
	require.Equal(t, []string{"u-100", "manager", "order-system"}, rem.OldRule.Values)

	// data 变更：op 映射 + id
	require.Len(t, got.DataChanges, 2)
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_ADD, got.DataChanges[0].Op)
	require.Equal(t, uint64(9), got.DataChanges[0].Policy.Id)
	require.Equal(t, "manager", got.DataChanges[0].Policy.SubjectId)
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_REMOVE, got.DataChanges[1].Op)
	require.Equal(t, uint64(3), got.DataChanges[1].Policy.Id)
}

func TestRuleToProto_TrimsTrailingEmpty(t *testing.T) {
	// 全 6 位有值不裁
	full := RulesToProto([]cp.Rule{rule("p", "a", "b", "c", "d", "e", "f")})
	require.Equal(t, []string{"a", "b", "c", "d", "e", "f"}, full[0].Values)
	// 中间空串保留，仅裁尾部
	mid := RulesToProto([]cp.Rule{rule("p", "a", "", "c")})
	require.Equal(t, []string{"a", "", "c"}, mid[0].Values)
	// 全空 ptype 仍保留 ptype、values 为空切片
	empty := RulesToProto([]cp.Rule{rule("g")})
	require.Equal(t, "g", empty[0].Ptype)
	require.Empty(t, empty[0].Values)
}

func TestDataPoliciesToProto(t *testing.T) {
	got := DataPoliciesToProto([]cp.DataPolicy{
		{ID: 1, SubjectType: "user", SubjectID: "u-1", Resource: "doc", Condition: `{"op":"EQ"}`},
	})
	require.Len(t, got, 1)
	require.Equal(t, uint64(1), got[0].Id)
	require.Equal(t, "user", got[0].SubjectType)
	require.Equal(t, "u-1", got[0].SubjectId)
	require.Equal(t, "doc", got[0].Resource)
	require.Equal(t, `{"op":"EQ"}`, got[0].Condition)
}
