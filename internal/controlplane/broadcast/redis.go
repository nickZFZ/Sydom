package broadcast

import (
	"context"
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/redis/go-redis/v9"
)

// RedisPublisher 用 Redis PUBLISH 把 Delta 发布到全局频道。
type RedisPublisher struct {
	client *redis.Client
}

// NewRedisPublisher 构造 RedisPublisher。
func NewRedisPublisher(client *redis.Client) *RedisPublisher {
	return &RedisPublisher{client: client}
}

// Publish 编码 {appID, delta} 后 PUBLISH 到 Channel。
func (p *RedisPublisher) Publish(ctx context.Context, appID int64, delta *syncv1.Delta) error {
	blob, err := EncodeEnvelope(appID, delta)
	if err != nil {
		return err
	}
	if err := p.client.Publish(ctx, Channel, blob).Err(); err != nil {
		return fmt.Errorf("broadcast: redis publish: %w", err)
	}
	return nil
}

// RedisSubscriber 订阅全局频道，对每条消息解码后回调 handler。
type RedisSubscriber struct {
	client *redis.Client
}

// NewRedisSubscriber 构造 RedisSubscriber。
func NewRedisSubscriber(client *redis.Client) *RedisSubscriber {
	return &RedisSubscriber{client: client}
}

// Run 订阅 Channel，阻塞循环直至 ctx 取消。解码失败的消息跳过（记录由调用方决定），
// 不中断订阅——单条坏消息不应拖垮整条扩散链路。
func (s *RedisSubscriber) Run(ctx context.Context, handler func(appID int64, delta *syncv1.Delta)) error {
	ps := s.client.Subscribe(ctx, Channel)
	defer ps.Close()
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			appID, delta, err := DecodeEnvelope([]byte(msg.Payload))
			if err != nil {
				continue // 坏消息跳过，不中断
			}
			handler(appID, delta)
		}
	}
}
