package broadcast_test

import (
	"context"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisPublishSubscribe(t *testing.T) {
	addr := dbtest.StartRedis(t)
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))

	type recv struct {
		appID int64
		delta *syncv1.Delta
	}
	got := make(chan recv, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = sub.Run(ctx, func(appID int64, d *syncv1.Delta) { got <- recv{appID, d} })
	}()

	// 等订阅就绪后再发布（Redis Pub/Sub at-most-once，订阅前发会丢）
	require.Eventually(t, func() bool {
		err := pub.Publish(context.Background(), 7, &syncv1.Delta{Version: 99})
		require.NoError(t, err)
		select {
		case r := <-got:
			require.Equal(t, int64(7), r.appID)
			require.Equal(t, uint64(99), r.delta.Version)
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	}, 5*time.Second, 50*time.Millisecond)
}
