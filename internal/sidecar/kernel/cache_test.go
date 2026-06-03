package kernel

import (
	"testing"

	"github.com/casbin/casbin/v3/persist/cache"
	"github.com/stretchr/testify/require"
)

func TestBoundedCache_SetGetDeleteClear(t *testing.T) {
	c := newBoundedCache(8)
	_, err := c.Get("missing")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)

	require.NoError(t, c.Set("k", true))
	v, err := c.Get("k")
	require.NoError(t, err)
	require.True(t, v)

	require.NoError(t, c.Set("k", false)) // 覆盖
	v, _ = c.Get("k")
	require.False(t, v)

	require.NoError(t, c.Delete("k"))
	_, err = c.Get("k")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)
	require.ErrorIs(t, c.Delete("k"), cache.ErrNoSuchKey)

	require.NoError(t, c.Set("a", true))
	require.NoError(t, c.Clear())
	_, err = c.Get("a")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)
}

func TestBoundedCache_EvictsLRU(t *testing.T) {
	c := newBoundedCache(2)
	require.NoError(t, c.Set("a", true))
	require.NoError(t, c.Set("b", true))
	_, _ = c.Get("a")              // a 变最近使用
	require.NoError(t, c.Set("c", true)) // 容量满 → 淘汰最久未用 b

	_, err := c.Get("b")
	require.ErrorIs(t, err, cache.ErrNoSuchKey, "b 应被淘汰")
	va, errA := c.Get("a")
	require.NoError(t, errA)
	require.True(t, va)
	vc, errC := c.Get("c")
	require.NoError(t, errC)
	require.True(t, vc)
}

func TestBoundedCache_ImplementsInterface(t *testing.T) {
	var _ cache.Cache = newBoundedCache(1)
}
