package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEngine_New_NotReadyFailsClose(t *testing.T) {
	e, err := New("dom1", nil, nil) // cache=nil→默认有界；applier=nil→noop
	require.NoError(t, err)
	require.False(t, e.Ready())
	require.Equal(t, uint64(0), e.Version())

	allow, err := e.Enforce("alice", "dom1", "order", "read")
	require.ErrorIs(t, err, ErrNotReady)
	require.False(t, allow, "未就绪必须 fail-close 拒绝")
}

func mgrSnapshot(version uint64) Snapshot {
	return Snapshot{
		Version: version,
		Rules: []Rule{
			{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
			{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
		},
	}
}

func TestEngine_ApplySnapshot_EnforcesViaRole(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(7)))
	require.True(t, e.Ready())
	require.Equal(t, uint64(7), e.Version())

	allow, err := e.Enforce("alice", "dom1", "order", "read") // alice 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)

	deny, err := e.Enforce("alice", "dom1", "order", "delete") // 无此权限
	require.NoError(t, err)
	require.False(t, deny)
}

func TestEngine_ApplySnapshot_DenyOverride(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "deny", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s))
	allow, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, allow, "deny 覆盖 allow")
}

func TestEngine_ApplySnapshot_DomainIsolation(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	deny, err := e.Enforce("alice", "dom2", "order", "read") // 外域请求
	require.ErrorIs(t, err, ErrForeignDomain)
	require.False(t, deny)
}

func TestEngine_ApplySnapshot_ForeignDomainRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom2", "order", "read", "allow", ""}}, // 外域行
	}}
	require.ErrorIs(t, e.ApplySnapshot(s), ErrForeignDomain)
	require.False(t, e.Ready(), "越域快照整笔拒绝，状态不变")
}

func TestEngine_ApplySnapshot_RebuildNoResidue(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	// 二次快照只含一条无关 p、无 alice 的 g
	s2 := Snapshot{Version: 2, Rules: []Rule{
		{Ptype: "p", V: [6]string{"viewer", "dom1", "report", "read", "allow", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s2))
	require.Equal(t, uint64(2), e.Version())
	deny, err := e.Enforce("alice", "dom1", "order", "read") // 旧策略应已清除
	require.NoError(t, err)
	require.False(t, deny, "ClearPolicy 后旧策略不得残留")
}

// spyApplier 记录收到的数据策略变更，验证路由。
type spyApplier struct {
	snapshots [][]DataPolicy
	changes   []DataPolicyChange
}

func (s *spyApplier) ApplySnapshot(p []DataPolicy) { s.snapshots = append(s.snapshots, p) }
func (s *spyApplier) ApplyChange(op ChangeOp, p DataPolicy) {
	s.changes = append(s.changes, DataPolicyChange{Op: op, Policy: p})
}

// 缓存铁律守门：撤权必须即时生效（按 key 删在角色间接性下会漏，全量清才正确）。
func TestEngine_ApplyDelta_RevokeTakesEffectImmediately(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	allow, err := e.Enforce("alice", "dom1", "order", "read") // 命中并入缓存 true
	require.NoError(t, err)
	require.True(t, allow)

	// 撤掉 manager 的 order:read 权限（delta REMOVE p）
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeRemove, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	require.Equal(t, uint64(2), e.Version())

	deny, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, deny, "撤权后 alice（经 manager）必须立即被拒——证明全量清生效")
}

func TestEngine_ApplyDelta_AddGrant(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeAdd, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "delete", "allow", ""}}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	allow, err := e.Enforce("alice", "dom1", "order", "delete")
	require.NoError(t, err)
	require.True(t, allow)
}

func TestEngine_ApplyDelta_UpdateRule(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{{
		Op:      ChangeUpdate,
		OldRule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		Rule:    Rule{Ptype: "p", V: [6]string{"manager", "dom1", "invoice", "read", "allow", ""}},
	}}}
	require.NoError(t, e.ApplyDelta(d))
	old, _ := e.Enforce("alice", "dom1", "order", "read")
	require.False(t, old, "旧权限应被移除")
	neu, _ := e.Enforce("alice", "dom1", "invoice", "read")
	require.True(t, neu, "新权限应生效")
}

func TestEngine_ApplyDelta_StaleVersionRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(5)))
	d := Delta{Version: 5} // 非严格大于当前 5
	require.ErrorIs(t, e.ApplyDelta(d), ErrStaleVersion)
	require.Equal(t, uint64(5), e.Version(), "拒绝后版本不变")
}

func TestEngine_ApplyDelta_ForeignDomainRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeAdd, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom2", "x", "y", "allow", ""}}},
	}}
	require.ErrorIs(t, e.ApplyDelta(d), ErrForeignDomain)
	require.Equal(t, uint64(1), e.Version())
}

func TestEngine_ApplyDelta_RoutesDataChanges(t *testing.T) {
	spy := &spyApplier{}
	e, _ := New("dom1", nil, spy)
	require.NoError(t, e.ApplySnapshot(Snapshot{Version: 1, DataPolicies: []DataPolicy{{ID: 9}}}))
	require.Len(t, spy.snapshots, 1)
	require.Equal(t, uint64(9), spy.snapshots[0][0].ID)

	d := Delta{Version: 2, DataChanges: []DataPolicyChange{
		{Op: ChangeAdd, Policy: DataPolicy{ID: 10, Resource: "order"}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	require.Len(t, spy.changes, 1)
	require.Equal(t, ChangeAdd, spy.changes[0].Op)
	require.Equal(t, uint64(10), spy.changes[0].Policy.ID)
}

func TestEngine_GetImplicitRolesForUser_Hierarchy(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	// admin > manager > viewer（角色继承），alice 绑 admin
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "g", V: [6]string{"alice", "admin", "dom1", "", "", ""}},
		{Ptype: "g", V: [6]string{"admin", "manager", "dom1", "", "", ""}},
		{Ptype: "g", V: [6]string{"manager", "viewer", "dom1", "", "", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s))

	roles, err := e.GetImplicitRolesForUser("alice", "dom1")
	require.NoError(t, err)
	require.Subset(t, roles, []string{"admin", "manager", "viewer"})
}

func TestEngine_GetImplicitRolesForUser_NotReady(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, err := e.GetImplicitRolesForUser("alice", "dom1")
	require.ErrorIs(t, err, ErrNotReady)
}

func TestEngine_BatchEnforce(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	res, err := e.BatchEnforce([][]string{
		{"alice", "dom1", "order", "read"},   // true（经 manager）
		{"alice", "dom1", "order", "delete"}, // false
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, res)
}

func TestEngine_BatchEnforce_NotReady(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, err := e.BatchEnforce([][]string{{"a", "dom1", "o", "r"}})
	require.ErrorIs(t, err, ErrNotReady)
}
