package syncclient

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

func TestRuleFromProto_PadsShortValues(t *testing.T) {
	r, err := ruleFromProto(&syncv1.PolicyRule{Ptype: "g", Values: []string{"alice", "manager", "dom1"}})
	require.NoError(t, err)
	require.Equal(t, kernel.Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}, r)
}

func TestRuleFromProto_RejectsTooManyValues(t *testing.T) {
	_, err := ruleFromProto(&syncv1.PolicyRule{
		Ptype:  "p",
		Values: []string{"1", "2", "3", "4", "5", "6", "7"}, // 7 > 6
	})
	require.Error(t, err, "变长越界必须 fail-close 报错")
}

func TestOpFromProto(t *testing.T) {
	for _, tc := range []struct {
		in   syncv1.ChangeOp
		want kernel.ChangeOp
	}{
		{syncv1.ChangeOp_CHANGE_OP_ADD, kernel.ChangeAdd},
		{syncv1.ChangeOp_CHANGE_OP_REMOVE, kernel.ChangeRemove},
		{syncv1.ChangeOp_CHANGE_OP_UPDATE, kernel.ChangeUpdate},
	} {
		got, err := opFromProto(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestOpFromProto_RejectsUnspecifiedAndUnknown(t *testing.T) {
	_, err := opFromProto(syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED)
	require.Error(t, err)
	_, err = opFromProto(syncv1.ChangeOp(99))
	require.Error(t, err)
}

// REMOVE 的待删行在线上躺在 old_rule，必须搬进内核 Rule（否则内核越域校验拒绝整条 delta）。
func TestPolicyChangeFromProto_Remove_RuleGoesToRule(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
		OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	})
	require.NoError(t, err)
	require.Equal(t, kernel.ChangeRemove, pc.Op)
	require.Equal(t, "p", pc.Rule.Ptype)
	require.Equal(t, "dom1", pc.Rule.V[1], "REMOVE 待删行必须落在 Rule（内核读 pc.Rule）")
	require.Equal(t, kernel.Rule{}, pc.OldRule, "REMOVE 不用 OldRule")
}

func TestPolicyChangeFromProto_Add_RuleGoesToRule(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	})
	require.NoError(t, err)
	require.Equal(t, kernel.ChangeAdd, pc.Op)
	require.Equal(t, "dom1", pc.Rule.V[1])
	require.Equal(t, kernel.Rule{}, pc.OldRule)
}

func TestPolicyChangeFromProto_Update_BothRules(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:      syncv1.ChangeOp_CHANGE_OP_UPDATE,
		Rule:    &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "write"}},
		OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read"}},
	})
	require.NoError(t, err)
	require.Equal(t, "write", pc.Rule.V[3])
	require.Equal(t, "read", pc.OldRule.V[3])
}

func TestDataPolicyFromProto_CarriesEffect(t *testing.T) {
	dp := dataPolicyFromProto(&syncv1.DataPolicy{
		Id: 7, SubjectType: "role", SubjectId: "manager",
		Resource: "order", Condition: `{"field":"a","op":"EQ","value":1}`, Effect: "deny",
	})
	require.Equal(t, kernel.DataPolicy{
		ID: 7, SubjectType: "role", SubjectID: "manager",
		Resource: "order", Condition: `{"field":"a","op":"EQ","value":1}`, Effect: "deny",
	}, dp)
}

func TestSnapshotFromProto_RoundTripsRulesAndData(t *testing.T) {
	ks, err := snapshotFromProto(&syncv1.Snapshot{
		Version: 9,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
		},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "manager", Resource: "order", Condition: "{}", Effect: "allow"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(9), ks.Version)
	require.Len(t, ks.Rules, 2)
	require.Len(t, ks.DataPolicies, 1)
	require.Equal(t, "allow", ks.DataPolicies[0].Effect)
}

func TestSnapshotFromProto_PropagatesRuleError(t *testing.T) {
	_, err := snapshotFromProto(&syncv1.Snapshot{
		Rules: []*syncv1.PolicyRule{{Ptype: "p", Values: []string{"1", "2", "3", "4", "5", "6", "7"}}},
	})
	require.Error(t, err, "快照内任一条非法 → 整快照不可用")
}

func TestDeltaFromProto_RoundTripsChanges(t *testing.T) {
	kd, err := deltaFromProto(&syncv1.Delta{
		Version: 12,
		PolicyChanges: []*syncv1.PolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_ADD, Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"x", "dom1", "o", "a", "allow"}}},
		},
		DataChanges: []*syncv1.DataPolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_REMOVE, Policy: &syncv1.DataPolicy{Id: 3}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(12), kd.Version)
	require.Equal(t, kernel.ChangeAdd, kd.PolicyChanges[0].Op)
	require.Equal(t, kernel.ChangeRemove, kd.DataChanges[0].Op)
	require.Equal(t, uint64(3), kd.DataChanges[0].Policy.ID)
}

func TestDeltaFromProto_PropagatesOpError(t *testing.T) {
	_, err := deltaFromProto(&syncv1.Delta{
		Version:       1,
		PolicyChanges: []*syncv1.PolicyChange{{Op: syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED}},
	})
	require.Error(t, err)
}
