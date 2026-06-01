// Package broadcast 把控制面策略变更经 Redis Pub/Sub 扩散到各副本。
// 消息体 = 8 字节大端 app_id 前缀 + proto.Marshal(syncv1.Delta)。
package broadcast

import (
	"context"
	"encoding/binary"
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"google.golang.org/protobuf/proto"
)

// Channel 是控制面广播策略变更的单一全局 Redis 频道。
const Channel = "sydom:policy:delta"

// Publisher 把一次策略变更（领域 Delta，已翻译为 proto）发布到广播总线。
type Publisher interface {
	// Publish 发布 app 的一条 Delta。at-most-once：失败返错由调用方决策，不重试。
	Publish(ctx context.Context, appID int64, delta *syncv1.Delta) error
}

// Subscriber 订阅广播总线，对每条消息回调 handler。
// Run 阻塞直至 ctx 取消；ctx 取消时返回 ctx.Err()，底层订阅错误时返回非 nil error。
type Subscriber interface {
	Run(ctx context.Context, handler func(appID int64, delta *syncv1.Delta)) error
}

// EncodeEnvelope 把 {appID, delta} 编码为广播字节。
func EncodeEnvelope(appID int64, delta *syncv1.Delta) ([]byte, error) {
	body, err := proto.Marshal(delta)
	if err != nil {
		return nil, fmt.Errorf("broadcast: marshal delta: %w", err)
	}
	buf := make([]byte, 8+len(body))
	binary.BigEndian.PutUint64(buf[:8], uint64(appID))
	copy(buf[8:], body)
	return buf, nil
}

// DecodeEnvelope 从广播字节解出 {appID, delta}。
func DecodeEnvelope(blob []byte) (int64, *syncv1.Delta, error) {
	if len(blob) < 8 {
		return 0, nil, fmt.Errorf("broadcast: envelope too short (%d bytes)", len(blob))
	}
	appID := int64(binary.BigEndian.Uint64(blob[:8]))
	var d syncv1.Delta
	if err := proto.Unmarshal(blob[8:], &d); err != nil {
		return 0, nil, fmt.Errorf("broadcast: unmarshal delta: %w", err)
	}
	return appID, &d, nil
}
