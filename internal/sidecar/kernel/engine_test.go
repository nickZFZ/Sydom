package kernel

import (
	"sync"
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

// TestEngine_ApplySnapshot_RoutesDataPolicyEffect 验证 DataPolicy.Effect 经 ApplySnapshot 原样透传给 applier。
func TestEngine_ApplySnapshot_RoutesDataPolicyEffect(t *testing.T) {
	spy := &spyApplier{}
	e, _ := New("dom1", nil, spy)
	require.NoError(t, e.ApplySnapshot(Snapshot{Version: 1, DataPolicies: []DataPolicy{
		{ID: 1, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "deny"},
	}}))
	require.Len(t, spy.snapshots, 1)
	require.Equal(t, "deny", spy.snapshots[0][0].Effect)
}

// TestEngine_ConcurrentApplyAndRead 在 apply（写）与读路径并发下运行，由 -race 守护：
// 既证明读写经同一把 RWMutex 安全协作，也守护 GetImplicitRolesForUser 不得在外层叠加读锁
// （否则写者夹在内外两次 RLock 之间会触发 Go RWMutex 递归读锁死锁）。run with -race。
func TestEngine_ConcurrentApplyAndRead(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	const iters = 200
	var wg sync.WaitGroup
	wg.Add(3)

	go func() { // 写者：持续重建/增量，反复抢写锁
		defer wg.Done()
		for i := uint64(2); i < 2+iters; i++ {
			_ = e.ApplySnapshot(mgrSnapshot(i))
		}
	}()
	go func() { // 读者：隐式角色展开（内部自取读锁）
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_, _ = e.GetImplicitRolesForUser("alice", "dom1")
		}
	}()
	go func() { // 读者：批量鉴权
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_, _ = e.BatchEnforce([][]string{{"alice", "dom1", "order", "read"}})
		}
	}()

	wg.Wait() // 不死锁、-race 干净即通过
}

func TestEngine_Domain_ReturnsPinnedDomain(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "dom1", e.Domain(), "Domain() 应返回构造时 pin 的域")
}

func TestEngine_EnforceEx_ReturnsDecidingRule(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(3)))

	// allow：命中 manager 的 (order,read,allow) 规则；bool 与 Enforce 同输入一致。
	allow, rule, err := e.EnforceEx("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.True(t, allow)
	require.Equal(t, []string{"manager", "dom1", "order", "read", "allow"}, rule)

	plain, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.Equal(t, plain, allow) // DX-2 引擎层 parity：EnforceEx.bool ≡ Enforce

	// 默认拒绝：无规则命中 → explain 空。
	allow2, rule2, err := e.EnforceEx("alice", "dom1", "order", "delete")
	require.NoError(t, err)
	require.False(t, allow2)
	require.Empty(t, rule2)
}

func TestEngine_EnforceEx_FailClose(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, _, err := e.EnforceEx("alice", "dom1", "order", "read") // 未就绪
	require.ErrorIs(t, err, ErrNotReady)

	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	_, _, err = e.EnforceEx("alice", "other", "order", "read") // 越域
	require.ErrorIs(t, err, ErrForeignDomain)
}

// T2: 批量判定须与逐条 Enforce 逐一相等（allow/deny 混合）——改写循环不得改判定。
// 刻意用两个独立引擎：同一引擎上批量会先填缓存、单条再读同一条目 → 两边"串供"会掩盖分歧。
func TestEngine_BatchEnforce_MatchesSingleEnforce(t *testing.T) {
	reqs := [][]string{
		{"alice", "dom1", "order", "read"},   // 经 manager 继承
		{"alice", "dom1", "order", "delete"}, // 无此 act
		{"bob", "dom1", "order", "read"},     // 非 manager
		{"manager", "dom1", "order", "read"}, // 直接主体（casbin HasLink 自反，role_manager.go:310）
	}

	eb, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, eb.ApplySnapshot(mgrSnapshot(1)))
	batch, err := eb.BatchEnforce(reqs)
	require.NoError(t, err)
	require.Len(t, batch, len(reqs))

	es, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, es.ApplySnapshot(mgrSnapshot(1)))
	for i, r := range reqs {
		single, serr := es.Enforce(r[0], r[1], r[2], r[3])
		require.NoError(t, serr)
		require.Equal(t, single, batch[i], "第 %d 行：批量与单条判定必须一致（req=%v）", i, r)
	}
}

// T3: 批量对外域行返 false 且不报错——与单条 Enforce 的 ErrForeignDomain 刻意不同（engine.go 契约）。
// 钉死陷阱：实现须调 e.ce.Enforce（casbin），若误写 e.Enforce（内核）→ 外域行变报错 → 本测试红。
func TestEngine_BatchEnforce_ForeignDomainRowReturnsFalseNotError(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	res, err := e.BatchEnforce([][]string{
		{"alice", "dom1", "order", "read"}, // 本域
		{"alice", "dom2", "order", "read"}, // 外域：须 false，不得报错
	})
	require.NoError(t, err, "批量不得对外域行返错（契约：外域以 false 表达拒绝，不回传越域信号）")
	require.Equal(t, []bool{true, false}, res)

	// 对照：单条 Enforce 对同一外域请求显式报 ErrForeignDomain——两者刻意不对称
	_, serr := e.Enforce("alice", "dom2", "order", "read")
	require.ErrorIs(t, serr, ErrForeignDomain, "单条须保留越域信号（与批量的刻意差异）")
}

// T5: 空输入返 nil（非空切片）——逐字对齐 casbin BatchEnforce 现行行为（authorizer.go:88 直接透传）。
func TestEngine_BatchEnforce_EmptyInputReturnsNil(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	res, err := e.BatchEnforce(nil)
	require.NoError(t, err)
	require.Nil(t, res, "空输入须返 nil（实现须用 var results []bool + append，非 make([]bool,0,n)）")

	res, err = e.BatchEnforce([][]string{})
	require.NoError(t, err)
	require.Nil(t, res)
}
