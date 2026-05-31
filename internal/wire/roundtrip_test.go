package wire

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSyncEvent_DeltaRoundTrip(t *testing.T) {
	ev := &syncv1.SyncEvent{
		Event: &syncv1.SyncEvent_Delta{
			Delta: &syncv1.Delta{
				Version: 42,
				PolicyChanges: []*syncv1.PolicyChange{{
					Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
					Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"alice", "data1", "read"}},
				}},
				DataChanges: []*syncv1.DataPolicyChange{{
					Op: syncv1.ChangeOp_CHANGE_OP_REMOVE,
					Policy: &syncv1.DataPolicy{
						Id: 7, SubjectType: "role", SubjectId: "admin",
						Resource: "order", Condition: `{"op":"eq"}`,
					},
				}},
			},
		},
	}

	raw, err := proto.Marshal(ev)
	require.NoError(t, err)

	got := &syncv1.SyncEvent{}
	require.NoError(t, proto.Unmarshal(raw, got))

	require.True(t, proto.Equal(ev, got))
	// 变长 values 保真（casbin []string 语义）
	require.Equal(t, []string{"alice", "data1", "read"},
		got.GetDelta().GetPolicyChanges()[0].GetRule().GetValues())
}

func TestSyncEvent_HeartbeatOneof(t *testing.T) {
	ev := &syncv1.SyncEvent{
		Event: &syncv1.SyncEvent_Heartbeat{Heartbeat: &syncv1.Heartbeat{CurrentVersion: 99}},
	}
	raw, err := proto.Marshal(ev)
	require.NoError(t, err)

	got := &syncv1.SyncEvent{}
	require.NoError(t, proto.Unmarshal(raw, got))
	require.Equal(t, uint64(99), got.GetHeartbeat().GetCurrentVersion())
	require.Nil(t, got.GetDelta()) // oneof 互斥
}

// TestSnapshot_RoundTrip 覆盖冷启动核心链路：全量快照的 version + rules + data_policies 保真。
func TestSnapshot_RoundTrip(t *testing.T) {
	snap := &syncv1.Snapshot{
		Version: 1234,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "p", Values: []string{"admin", "order", "read"}},
			{Ptype: "g", Values: []string{"alice", "admin"}},
		},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "admin", Resource: "order", Condition: `{"op":"all"}`},
		},
	}

	raw, err := proto.Marshal(snap)
	require.NoError(t, err)

	got := &syncv1.Snapshot{}
	require.NoError(t, proto.Unmarshal(raw, got))
	require.True(t, proto.Equal(snap, got))
	require.Equal(t, uint64(1234), got.GetVersion())
	require.Len(t, got.GetRules(), 2)
	require.Equal(t, []string{"alice", "admin"}, got.GetRules()[1].GetValues())
	require.Len(t, got.GetDataPolicies(), 1)
}

// TestSnapshotRequired_RoundTrip 验证 SnapshotRequired 事件的 current_version + reason 保真。
func TestSnapshotRequired_RoundTrip(t *testing.T) {
	ev := &syncv1.SyncEvent{
		Event: &syncv1.SyncEvent_SnapshotRequired{
			SnapshotRequired: &syncv1.SnapshotRequired{CurrentVersion: 500, Reason: "behind"},
		},
	}

	raw, err := proto.Marshal(ev)
	require.NoError(t, err)

	got := &syncv1.SyncEvent{}
	require.NoError(t, proto.Unmarshal(raw, got))
	require.Equal(t, uint64(500), got.GetSnapshotRequired().GetCurrentVersion())
	require.Equal(t, "behind", got.GetSnapshotRequired().GetReason())
	require.Nil(t, got.GetHeartbeat()) // oneof 互斥
}
