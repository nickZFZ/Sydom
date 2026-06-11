package console

import (
	"context"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, ttl time.Duration) *RedisStore {
	t.Helper()
	addr := dbtest.StartRedis(t)
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewRedisStore(rdb, ttl)
}

func TestRedisStore_CreateGetDelete(t *testing.T) {
	s := newTestStore(t, time.Minute)
	ctx := context.Background()

	id, csrf, err := s.Create(ctx, "root@sydom")
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NotEmpty(t, csrf)

	sess, err := s.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "root@sydom", sess.Principal)
	require.Equal(t, csrf, sess.CSRF)

	require.NoError(t, s.Delete(ctx, id))
	_, err = s.Get(ctx, id)
	require.ErrorIs(t, err, ErrNoSession)
}

func TestRedisStore_UnknownID(t *testing.T) {
	s := newTestStore(t, time.Minute)
	_, err := s.Get(context.Background(), "nonexistent")
	require.ErrorIs(t, err, ErrNoSession)
}

func TestRedisStore_EmptyID(t *testing.T) {
	s := newTestStore(t, time.Minute)
	_, err := s.Get(context.Background(), "")
	require.ErrorIs(t, err, ErrNoSession)
}

func TestRedisStore_Expiry(t *testing.T) {
	s := newTestStore(t, 50*time.Millisecond)
	ctx := context.Background()
	id, _, err := s.Create(ctx, "x")
	require.NoError(t, err)
	time.Sleep(120 * time.Millisecond)
	_, err = s.Get(ctx, id)
	require.ErrorIs(t, err, ErrNoSession)
}
