// Package translate 把控制面领域类型 cp.* 单向翻译为 syncv1 proto 消息。
// 纯函数，无 DB / 网络副作用。
package translate

import (
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// DeltaToProto 把一次写事务的领域 Delta 翻译为 syncv1.Delta。
// casbin 行只有增/删：RuleAdds→ADD(rule)，RuleRemoves→REMOVE(old_rule)。
func DeltaToProto(d cp.Delta) *syncv1.Delta {
	out := &syncv1.Delta{Version: uint64(d.Version)}
	for _, r := range d.RuleAdds {
		out.PolicyChanges = append(out.PolicyChanges, &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: ruleToProto(r),
		})
	}
	for _, r := range d.RuleRemoves {
		out.PolicyChanges = append(out.PolicyChanges, &syncv1.PolicyChange{
			Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
			OldRule: ruleToProto(r),
		})
	}
	for _, c := range d.DataChanges {
		out.DataChanges = append(out.DataChanges, &syncv1.DataPolicyChange{
			Op:     opToProto(c.Op),
			Policy: dataPolicyToProto(c.Policy),
		})
	}
	return out
}

// RulesToProto 把全量规则翻译为 proto（供 PullSnapshot）。
func RulesToProto(rules []cp.Rule) []*syncv1.PolicyRule {
	out := make([]*syncv1.PolicyRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleToProto(r))
	}
	return out
}

// DataPoliciesToProto 把全量数据策略翻译为 proto（供 PullSnapshot）。
func DataPoliciesToProto(dps []cp.DataPolicy) []*syncv1.DataPolicy {
	out := make([]*syncv1.DataPolicy, 0, len(dps))
	for _, p := range dps {
		out = append(out, dataPolicyToProto(p))
	}
	return out
}

// ruleToProto 把 cp.Rule.V[6] 裁掉尾部连续空串后转 PolicyRule.values（贴 casbin 变长风格）。
func ruleToProto(r cp.Rule) *syncv1.PolicyRule {
	n := len(r.V)
	for n > 0 && r.V[n-1] == "" {
		n--
	}
	values := make([]string, n)
	copy(values, r.V[:n])
	return &syncv1.PolicyRule{Ptype: r.Ptype, Values: values}
}

func dataPolicyToProto(p cp.DataPolicy) *syncv1.DataPolicy {
	return &syncv1.DataPolicy{
		Id:          uint64(p.ID),
		SubjectType: p.SubjectType,
		SubjectId:   p.SubjectID,
		Resource:    p.Resource,
		Condition:   p.Condition,
		Effect:      p.Effect,
	}
}

func opToProto(op cp.ChangeOp) syncv1.ChangeOp {
	switch op {
	case cp.ChangeAdd:
		return syncv1.ChangeOp_CHANGE_OP_ADD
	case cp.ChangeUpdate:
		return syncv1.ChangeOp_CHANGE_OP_UPDATE
	case cp.ChangeRemove:
		return syncv1.ChangeOp_CHANGE_OP_REMOVE
	default:
		return syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED
	}
}
