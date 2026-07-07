package obs

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsCache_HitMiss(t *testing.T) {
	m := New()
	c := NewMetricsCache(kernel.NewBoundedCache(8), m)

	_, err := c.Get("k") // 未命中
	require.Error(t, err)
	require.NoError(t, c.Set("k", true))
	v, err := c.Get("k") // 命中
	require.NoError(t, err)
	require.True(t, v)

	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheMiss))
	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheHits))
}
