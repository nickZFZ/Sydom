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
