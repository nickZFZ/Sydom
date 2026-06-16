package authz

import (
	"errors"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// fakeFresh 注入可控的就绪/陈旧信号。
type fakeFresh struct {
	ready bool
	last  time.Time
}

func (f fakeFresh) Ready() bool           { return f.ready }
func (f fakeFresh) LastSyncAt() time.Time { return f.last }

// appliedEngine 构造已应用快照（alice→manager；manager 可 read order；allow+deny 两条数据策略）的内核与表。
func appliedEngine(t *testing.T) (*kernel.Engine, *dataperm.Table) {
	t.Helper()
	table := dataperm.NewTable()
	engine, err := kernel.New("dom1", nil, table)
	require.NoError(t, err)
	snap := kernel.Snapshot{
		Version: 5,
		Rules: []kernel.Rule{
			{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
			{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		},
		DataPolicies: []kernel.DataPolicy{
			{ID: 1, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{ID: 2, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}
	require.NoError(t, engine.ApplySnapshot(snap))
	return engine, table
}

// newAuthorizer 组装 Authorizer（真实 Engine+Filter + 注入的 fresh）。
func newAuthorizer(t *testing.T, cfg Config, fresh Freshness) *Authorizer {
	t.Helper()
	engine, table := appliedEngine(t)
	filter := dataperm.NewFilter(engine, table)
	return New(engine, filter, fresh, cfg)
}

func TestAuthorizer_Check_AllowViaRole(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	allow, err := a.Check("alice", "order", "read") // alice 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)
}

func TestAuthorizer_Check_DenyUnconfigured(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	allow, err := a.Check("alice", "order", "delete") // 无此策略
	require.NoError(t, err)
	require.False(t, allow)
}

func TestAuthorizer_Check_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	allow, err := a.Check("alice", "order", "read")
	require.ErrorIs(t, err, kernel.ErrNotReady)
	require.False(t, allow, "未就绪必须 fail-close")
}

func TestAuthorizer_Check_TooStale_FailClose(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 10 * time.Second},
		fakeFresh{ready: true, last: now.Add(-10*time.Second - time.Nanosecond)})
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.ErrorIs(t, err, ErrTooStale)
	require.False(t, allow)
}

func TestAuthorizer_Check_AtStalenessBoundary_Passes(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 10 * time.Second},
		fakeFresh{ready: true, last: now.Add(-10 * time.Second)}) // 恰好等于阈值
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.NoError(t, err, "恰好等于阈值应放行（用 > 比较）")
	require.True(t, allow)
}

func TestAuthorizer_Check_MaxStalenessZero_DisablesGuard(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 0}, // 关闭陈旧守卫
		fakeFresh{ready: true, last: now.Add(-9999 * time.Hour)})
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.NoError(t, err, "MaxStaleness=0 时陈旧不拦截")
	require.True(t, allow)
}

func TestAuthorizer_BatchCheck_PreservesOrderAndLength(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	got, err := a.BatchCheck([]CheckReq{
		{Subject: "alice", Object: "order", Action: "read"},   // 命中
		{Subject: "alice", Object: "order", Action: "delete"}, // 不命中
		{Subject: "bob", Object: "order", Action: "read"},     // bob 无角色
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, false}, got)
}

func TestAuthorizer_BatchCheck_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	got, err := a.BatchCheck([]CheckReq{{Subject: "alice", Object: "order", Action: "read"}})
	require.ErrorIs(t, err, kernel.ErrNotReady)
	require.Nil(t, got)
}

func TestAuthorizer_FilterSQL_DenyOverride(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	res, err := a.FilterSQL("alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL)
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}

func TestAuthorizer_FilterSQL_MissingVar(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	_, err := a.FilterSQL("alice", "order", map[string]any{}) // 缺 department
	require.ErrorIs(t, err, dataperm.ErrMissingVar)
}

func TestAuthorizer_FilterSQL_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	_, err := a.FilterSQL("alice", "order", map[string]any{"department": "HR"})
	require.ErrorIs(t, err, kernel.ErrNotReady)
}

func TestAuthorizer_FilterRaw_MergedTree(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	res, err := a.FilterRaw("alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, dataperm.MatchConditional, res.Match)
	require.NotNil(t, res.Tree, "命中应返回合并条件树")
}

func TestReadyReflectsCheckFresh(t *testing.T) {
	// 未就绪 → ErrNotReady
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	if err := a.Ready(); !errors.Is(err, kernel.ErrNotReady) {
		t.Fatalf("not-ready want ErrNotReady, got %v", err)
	}
	// 就绪且新鲜 → nil
	a = newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	if err := a.Ready(); err != nil {
		t.Fatalf("fresh want nil, got %v", err)
	}
	// 就绪但超陈旧阈 → ErrTooStale
	a = newAuthorizer(t, Config{MaxStaleness: time.Minute}, fakeFresh{ready: true, last: time.Now().Add(-time.Hour)})
	if err := a.Ready(); !errors.Is(err, ErrTooStale) {
		t.Fatalf("stale want ErrTooStale, got %v", err)
	}
}
