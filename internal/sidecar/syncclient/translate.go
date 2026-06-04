package syncclient

import (
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// snapshotFromProto 把 syncv1.Snapshot 翻译为内核 Snapshot；任一条目非法 → error（fail-close）。
func snapshotFromProto(s *syncv1.Snapshot) (kernel.Snapshot, error) {
	out := kernel.Snapshot{Version: s.GetVersion()}
	for _, pr := range s.GetRules() {
		r, err := ruleFromProto(pr)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		out.Rules = append(out.Rules, r)
	}
	for _, dp := range s.GetDataPolicies() {
		out.DataPolicies = append(out.DataPolicies, dataPolicyFromProto(dp))
	}
	return out, nil
}

// deltaFromProto 把 syncv1.Delta 翻译为内核 Delta。
func deltaFromProto(d *syncv1.Delta) (kernel.Delta, error) {
	out := kernel.Delta{Version: d.GetVersion()}
	for _, pc := range d.GetPolicyChanges() {
		c, err := policyChangeFromProto(pc)
		if err != nil {
			return kernel.Delta{}, err
		}
		out.PolicyChanges = append(out.PolicyChanges, c)
	}
	for _, dc := range d.GetDataChanges() {
		c, err := dataPolicyChangeFromProto(dc)
		if err != nil {
			return kernel.Delta{}, err
		}
		out.DataChanges = append(out.DataChanges, c)
	}
	return out, nil
}

// ruleFromProto 把变长 values（≤6）拷进 Rule.V[6]，缺位补 ""（与 cp 裁尾空串互逆）。>6 → error。
func ruleFromProto(pr *syncv1.PolicyRule) (kernel.Rule, error) {
	vals := pr.GetValues()
	if len(vals) > 6 {
		return kernel.Rule{}, fmt.Errorf("syncclient: policy rule has %d values, max 6", len(vals))
	}
	r := kernel.Rule{Ptype: pr.GetPtype()}
	copy(r.V[:], vals)
	return r, nil
}

// opFromProto 映射 ChangeOp；UNSPECIFIED/未知 → error（fail-close）。
func opFromProto(op syncv1.ChangeOp) (kernel.ChangeOp, error) {
	switch op {
	case syncv1.ChangeOp_CHANGE_OP_ADD:
		return kernel.ChangeAdd, nil
	case syncv1.ChangeOp_CHANGE_OP_REMOVE:
		return kernel.ChangeRemove, nil
	case syncv1.ChangeOp_CHANGE_OP_UPDATE:
		return kernel.ChangeUpdate, nil
	default:
		return 0, fmt.Errorf("syncclient: unknown change op %v", op)
	}
}

// policyChangeFromProto 按「关键设计澄清」搬运字段：
// ADD→Rule(rule)；REMOVE→Rule(old_rule)；UPDATE→Rule(rule)+OldRule(old_rule)。
// 内核 ApplyDelta 对所有 op 读 pc.Rule 做越域校验与 add/remove，故 REMOVE 的待删行必须落在 Rule。
func policyChangeFromProto(pc *syncv1.PolicyChange) (kernel.PolicyChange, error) {
	op, err := opFromProto(pc.GetOp())
	if err != nil {
		return kernel.PolicyChange{}, err
	}
	out := kernel.PolicyChange{Op: op}
	switch op {
	case kernel.ChangeAdd:
		if out.Rule, err = ruleFromProto(pc.GetRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	case kernel.ChangeRemove:
		if out.Rule, err = ruleFromProto(pc.GetOldRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	case kernel.ChangeUpdate:
		if out.Rule, err = ruleFromProto(pc.GetRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
		if out.OldRule, err = ruleFromProto(pc.GetOldRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	}
	return out, nil
}

// dataPolicyFromProto 直传各字段，含刚打通的 Effect（空串透传，内核/dataperm 兜底归一）。
func dataPolicyFromProto(dp *syncv1.DataPolicy) kernel.DataPolicy {
	return kernel.DataPolicy{
		ID:          dp.GetId(),
		SubjectType: dp.GetSubjectType(),
		SubjectID:   dp.GetSubjectId(),
		Resource:    dp.GetResource(),
		Condition:   dp.GetCondition(),
		Effect:      dp.GetEffect(),
	}
}

func dataPolicyChangeFromProto(dc *syncv1.DataPolicyChange) (kernel.DataPolicyChange, error) {
	op, err := opFromProto(dc.GetOp())
	if err != nil {
		return kernel.DataPolicyChange{}, err
	}
	return kernel.DataPolicyChange{Op: op, Policy: dataPolicyFromProto(dc.GetPolicy())}, nil
}
