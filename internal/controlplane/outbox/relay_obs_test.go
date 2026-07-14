package outbox_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/stretchr/testify/require"
)

// drain 遇 DB 错误（关 DB → QueryContext 失败）须记一条 warn；循环随 ctx 超时正常退出（不 panic）。
// 注：drainOnce 对 Publish 失败是 break+下轮重试（不返错，见 relay.go），故 drain 错误路径专指 DB 异常。
func TestRelay_DrainError_Logged(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, db.Close()) // 关 DB → drainOnce 的 QueryContext 失败 → 返回 error

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(obs.With(context.Background(), logger), 200*time.Millisecond)
	defer cancel()

	pub := &recordingPub{} // 到不了 Publish（query 先失败）
	_ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) // 同 goroutine 阻塞至 ctx 超时

	require.Contains(t, buf.String(), "relay drain error", "drain(DB 错误)须记 warn")
}
