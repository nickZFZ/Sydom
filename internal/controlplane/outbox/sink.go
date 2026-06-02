// Package outbox 实现控制面写事务性 outbox：写事务内落 Delta，独立 relay 可靠投递。
package outbox

import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/translate"
	"google.golang.org/protobuf/proto"
)

// Sink 把写事务产出的 Delta 翻译为 syncv1 并写入 policy_outbox（同事务，原子）。
type Sink struct{}

// NewSink 构造 Sink。
func NewSink() *Sink { return &Sink{} }

// Persist 实现 policy.DeltaSink。
func (s *Sink) Persist(ctx context.Context, tx cp.DBTX, appID int64, d *cp.Delta) error {
	pd := translate.DeltaToProto(*d)
	blob, err := proto.Marshal(pd)
	if err != nil {
		return fmt.Errorf("outbox: marshal delta: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES ($1,$2,$3)`,
		appID, d.Version, blob); err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}
