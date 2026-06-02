package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"google.golang.org/protobuf/proto"
)

// RunRelayLoop 跑 outbox 投递循环：drain 未发布行→Publisher.Publish→标记 published_at。
// 阻塞至 ctx 取消。poll 为无新行时的轮询间隔。at-least-once：失败行下轮重试，不动业务数据。
func RunRelayLoop(ctx context.Context, db *sql.DB, pub broadcast.Publisher, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		n, err := drainOnce(ctx, db, pub)
		if err != nil && ctx.Err() == nil {
			// 记录但不中断循环（DB 抖动等）；下轮重试。
			// TODO: 接入日志/metric 后在此记录 drain 错误（当前全包未引入日志设施，暂静默续投）。
			n = 0
		}
		if n > 0 {
			continue // 还有积压，立刻下一批
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// drainOnce 取一批未发布行并尝试发布；返回成功发布的行数。
func drainOnce(ctx context.Context, db *sql.DB, pub broadcast.Publisher) (int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, app_id, delta_proto FROM policy_outbox
		 WHERE published_at IS NULL ORDER BY id ASC LIMIT 100`)
	if err != nil {
		return 0, fmt.Errorf("outbox: query unpublished: %w", err)
	}
	type rec struct {
		id    int64
		appID int64
		blob  []byte
	}
	var batch []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.appID, &r.blob); err != nil {
			rows.Close()
			return 0, fmt.Errorf("outbox: scan: %w", err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	published := 0
	for _, r := range batch {
		var d syncv1.Delta
		if err := proto.Unmarshal(r.blob, &d); err != nil {
			// 坏行：跳过不阻塞后续（与 ③-2 坏消息跳过一致）。
			continue
		}
		if err := pub.Publish(ctx, r.appID, &d); err != nil {
			// 发布失败：保持未发布，停止本批（按序投递），下轮重试。
			break
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE policy_outbox SET published_at=now() WHERE id=$1`, r.id); err != nil {
			return published, fmt.Errorf("outbox: mark published: %w", err)
		}
		published++
	}
	return published, nil
}
