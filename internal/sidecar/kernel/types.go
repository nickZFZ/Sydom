package kernel

// Rule 是一条 casbin_rule（ptype + v0..v5），空位用空串（casbin 惯例）。
type Rule struct {
	Ptype string
	V     [6]string
}

// values 返回去掉尾部空串的值切片（casbin []string 风格）。
func (r Rule) values() []string {
	n := len(r.V)
	for n > 0 && r.V[n-1] == "" {
		n--
	}
	out := make([]string, n)
	copy(out, r.V[:n])
	return out
}

// domainValue 取该行的 domain 列：p 段在 V[1]，g 段在 V[2]（对齐控制面 projection）。
func (r Rule) domainValue() string {
	switch r.Ptype {
	case "p":
		return r.V[1]
	case "g":
		return r.V[2]
	default:
		return ""
	}
}

// ChangeOp 是策略变更操作类型。
type ChangeOp int

const (
	ChangeAdd    ChangeOp = iota
	ChangeUpdate          //nolint:deadcode
	ChangeRemove          //nolint:deadcode
)

// PolicyChange 是一条 casbin 策略行变更。
type PolicyChange struct {
	Op      ChangeOp
	Rule    Rule // ADD/UPDATE 的新行；REMOVE 的待删行
	OldRule Rule // 仅 UPDATE 用：旧行
}

// DataPolicy 是一条数据权限规则（Condition 为不透明 JSON 串，求值归 ④-2）。
type DataPolicy struct {
	ID          uint64
	SubjectType string
	SubjectID   string
	Resource    string
	Condition   string
}

// DataPolicyChange 是一条数据权限变更。
type DataPolicyChange struct {
	Op     ChangeOp
	Policy DataPolicy
}

// Delta 是一次策略变更（功能行 + 数据策略，共享统一版本号）。
type Delta struct {
	Version       uint64
	PolicyChanges []PolicyChange
	DataChanges   []DataPolicyChange
}

// Snapshot 是全量策略快照。
type Snapshot struct {
	Version      uint64
	Rules        []Rule
	DataPolicies []DataPolicy
}

// DataPolicyApplier 接收数据策略的全量/增量变更（④-2 实现，默认 no-op）。
type DataPolicyApplier interface {
	ApplySnapshot(policies []DataPolicy)
	ApplyChange(op ChangeOp, p DataPolicy)
}

// noopApplier 是默认空实现，便于内核独立单测。
type noopApplier struct{}

func (noopApplier) ApplySnapshot([]DataPolicy)       {}
func (noopApplier) ApplyChange(ChangeOp, DataPolicy) {}
