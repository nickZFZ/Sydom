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
