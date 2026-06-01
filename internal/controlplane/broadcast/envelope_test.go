package broadcast

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	d := &syncv1.Delta{
		Version: 42,
		PolicyChanges: []*syncv1.PolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_ADD, Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"a", "b"}}},
		},
	}
	blob, err := EncodeEnvelope(7, d)
	require.NoError(t, err)

	appID, got, err := DecodeEnvelope(blob)
	require.NoError(t, err)
	require.Equal(t, int64(7), appID)
	require.Equal(t, uint64(42), got.Version)
	require.Len(t, got.PolicyChanges, 1)
	require.Equal(t, "p", got.PolicyChanges[0].Rule.Ptype)
	require.Equal(t, []string{"a", "b"}, got.PolicyChanges[0].Rule.Values)
}

func TestDecodeEnvelope_TooShort(t *testing.T) {
	_, _, err := DecodeEnvelope([]byte{0x00, 0x01}) // < 8 字节前缀
	require.Error(t, err)
}

func TestEnvelopeRoundTrip_EmptyBody(t *testing.T) {
	blob, err := EncodeEnvelope(99, &syncv1.Delta{})
	require.NoError(t, err)
	require.Len(t, blob, 8) // 空 delta → 恰好 8 字节前缀，无 body

	appID, got, err := DecodeEnvelope(blob)
	require.NoError(t, err)
	require.Equal(t, int64(99), appID)
	require.EqualValues(t, 0, got.Version)
}

func TestDecodeEnvelope_CorruptBody(t *testing.T) {
	corrupt := append(make([]byte, 8), 0xFF, 0xFF) // 8 字节前缀 + 损坏 proto
	_, _, err := DecodeEnvelope(corrupt)
	require.Error(t, err)
	require.Contains(t, err.Error(), "broadcast:")
}
