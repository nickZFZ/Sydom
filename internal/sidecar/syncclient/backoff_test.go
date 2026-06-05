package syncclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoff_CapForSequence(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	want := []time.Duration{
		500 * time.Millisecond, // 2^0
		1 * time.Second,        // 2^1
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // 32s 截顶到 max
		30 * time.Second, // 此后恒为 max
	}
	for i, w := range want {
		require.Equal(t, w, b.capFor(i), "attempt %d 的 cap 不符", i)
	}
}

func TestBackoff_NextJitterWithinCap(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	// 注入确定性 rng：返回 n-1（cap 上界附近），断言 next 落在 [0, cap)。
	b.rng = func(n int64) int64 { return n - 1 }
	for i := 0; i < 10; i++ {
		cap := b.capFor(b.attempt)
		d := b.next()
		require.GreaterOrEqual(t, d, time.Duration(0))
		require.Less(t, d, cap, "全抖动必须严格小于 cap")
	}
}

func TestBackoff_Reset(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	_ = b.next()
	_ = b.next()
	b.reset()
	require.Equal(t, 500*time.Millisecond, b.capFor(b.attempt), "reset 后 attempt 归零")
}

func TestBackoff_DefaultsOnZero(t *testing.T) {
	b := newBackoff(0, 0)
	require.Equal(t, defaultBackoffInitial, b.capFor(0))
	require.Equal(t, defaultBackoffMax, b.capFor(100))
}
