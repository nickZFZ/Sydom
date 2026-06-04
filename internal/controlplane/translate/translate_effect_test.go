package translate

import (
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
)

// TestDataPoliciesToProto_Effect 验证快照出口携带 effect。
func TestDataPoliciesToProto_Effect(t *testing.T) {
	out := DataPoliciesToProto([]cp.DataPolicy{
		{ID: 1, SubjectType: "role", SubjectID: "m", Resource: "order", Condition: "{}", Effect: "deny"},
	})
	require.Len(t, out, 1)
	require.Equal(t, "deny", out[0].Effect)
}

// TestDataPoliciesToProto_EmptyEffectPassthrough 守护 translate 不做归一的层次不变量：
// 空 effect 原样透传为 ""（归一在 store/mgmt 层，translate 不做归一）。
func TestDataPoliciesToProto_EmptyEffectPassthrough(t *testing.T) {
	empt := DataPoliciesToProto([]cp.DataPolicy{
		{ID: 2, SubjectType: "role", SubjectID: "r", Resource: "doc", Condition: "{}", Effect: ""},
	})
	require.Len(t, empt, 1)
	require.Equal(t, "", empt[0].Effect)
}

// TestDeltaToProto_DataEffect 验证增量出口携带 effect（复用 dataPolicyToProto）。
func TestDeltaToProto_DataEffect(t *testing.T) {
	out := DeltaToProto(cp.Delta{
		Version: 2,
		DataChanges: []cp.DataPolicyChange{
			{Op: cp.ChangeAdd, Policy: cp.DataPolicy{ID: 1, SubjectType: "user", SubjectID: "a", Resource: "order", Condition: "{}", Effect: "deny"}},
		},
	})
	require.Len(t, out.DataChanges, 1)
	require.Equal(t, "deny", out.DataChanges[0].Policy.Effect)
}
