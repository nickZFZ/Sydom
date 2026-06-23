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

func TestRedisStore_FlashOneShot(t *testing.T) {
	store := newTestStore(t, time.Minute)
	ctx := context.Background()
	id, _, err := store.Create(ctx, "root@sydom")
	require.NoError(t, err)

	// 初始无 flash。
	msg, err := store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "", msg)

	// 写 flash → 取到 → 再取为空（一次性）。
	require.NoError(t, store.SetFlash(ctx, id, "角色已删除"))
	msg, err = store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "角色已删除", msg)
	msg, err = store.TakeFlash(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "", msg, "flash 读后即清，一次性")

	// 会话其余字段不被 flash 操作破坏。
	sess, err := store.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "root@sydom", sess.Principal)
	require.NotEmpty(t, sess.CSRF)

	// 空 id 写 flash 走 ErrNoSession 分支（无会话可写）。
	require.ErrorIs(t, store.SetFlash(ctx, "", "x"), ErrNoSession)
}
