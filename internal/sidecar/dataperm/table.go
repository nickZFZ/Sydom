package dataperm

import (
	"fmt"
	"sync"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// 主体类型（与控制面 data_policy.subject_type 对齐）。
const (
	SubjectUser = "user"
	SubjectRole = "role"
)

// stored 是一条已解析（或中毒）的数据策略。
type stored struct {
	id          uint64
	subjectType string
	subjectID   string
	effect      string     // 归一化后 "allow"/"deny"（中毒时为空）
	cond        *Condition // 解析成功时的条件树
	parseErr    error      // 中毒时的解析错误（命中即 fail-close）
}

// Table 是内存 DataPolicy 表，实现 kernel.DataPolicyApplier。
// apply 时解析条件树（解析一次、求值多次）；按 resource 索引；读写用 RWMutex。
type Table struct {
	mu    sync.RWMutex
	byRes map[string][]stored
}

func NewTable() *Table {
	return &Table{byRes: make(map[string][]stored)}
}

func normalizeEffect(e string) (string, bool) {
	switch e {
	case "", "allow":
		return "allow", true
	case "deny":
		return "deny", true
	default:
		return "", false
	}
}

// toStored 解析一条策略为 stored；effect/condition 任一非法即标记中毒。
func toStored(p kernel.DataPolicy) stored {
	s := stored{id: p.ID, subjectType: p.SubjectType, subjectID: p.SubjectID}
	eff, ok := normalizeEffect(p.Effect)
	if !ok {
		s.parseErr = fmt.Errorf("%w: 未知 effect %q", ErrInvalidPolicy, p.Effect)
		return s
	}
	s.effect = eff
	cond, err := parseCondition(p.Condition)
	if err != nil {
		s.parseErr = err
		return s
	}
	s.cond = cond
	return s
}

// ApplySnapshot 全量重建内存表。
func (t *Table) ApplySnapshot(policies []kernel.DataPolicy) {
	next := make(map[string][]stored, len(policies))
	for _, p := range policies {
		next[p.Resource] = append(next[p.Resource], toStored(p))
	}
	t.mu.Lock()
	t.byRes = next
	t.mu.Unlock()
}

// ApplyChange 增量改表：add 追加；remove 按 ID 删；update = 删旧 + 加新。
func (t *Table) ApplyChange(op kernel.ChangeOp, p kernel.DataPolicy) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch op {
	case kernel.ChangeAdd:
		t.byRes[p.Resource] = append(t.byRes[p.Resource], toStored(p))
	case kernel.ChangeRemove:
		t.removeLocked(p.Resource, p.ID)
	case kernel.ChangeUpdate:
		t.removeLocked(p.Resource, p.ID)
		t.byRes[p.Resource] = append(t.byRes[p.Resource], toStored(p))
	}
}

// removeLocked 删某 resource 桶里的指定 ID；桶空则删 key（维持「未配置」语义）。
func (t *Table) removeLocked(resource string, id uint64) {
	bucket := t.byRes[resource]
	for i, s := range bucket {
		if s.id == id {
			bucket = append(bucket[:i], bucket[i+1:]...)
			break
		}
	}
	if len(bucket) == 0 {
		delete(t.byRes, resource)
		return
	}
	t.byRes[resource] = bucket
}

// Lookup 返回某 resource 的全部策略（副本）与「是否已配置」（key 是否存在）。
func (t *Table) Lookup(resource string) ([]stored, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	bucket, ok := t.byRes[resource]
	if !ok {
		return nil, false
	}
	out := make([]stored, len(bucket))
	copy(out, bucket)
	return out, true
}

var _ kernel.DataPolicyApplier = (*Table)(nil)
