package outbox_test

import (
	"context"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// recordingPub 记录收到的 version。
type recordingPub struct {
	mu   sync.Mutex
	got  []uint64
	fail bool
}

func (p *recordingPub) Publish(ctx context.Context, appID int64, d *syncv1.Delta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return assertErr
	}
	p.got = append(p.got, d.Version)
	return nil
}
func (p *recordingPub) versions() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.got...)
}

func TestRelay_DrainsAndMarksPublished(t *testing.T) {
	db := dbtest.SetupSchema(t)
	for _, v := range []int64{1, 2} {
		blob, _ := proto.Marshal(&syncv1.Delta{Version: uint64(v)})
		_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,$1,$2)`, v, blob)
		require.NoError(t, err)
	}

	pub := &recordingPub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) }()

	require.Eventually(t, func() bool {
		var unpublished int
		if err := db.QueryRow(`SELECT count(*) FROM policy_outbox WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
			return false
		}
		return unpublished == 0
	}, 5*time.Second, 50*time.Millisecond)

	assert.ElementsMatch(t, []uint64{1, 2}, pub.versions())
}

// failOnPub 在收到指定 version 时返回 error，其余成功；记录所有被调用的 version（含失败尝试）。
type failOnPub struct {
	mu     sync.Mutex
	got    []uint64
	failOn uint64
}

func (p *failOnPub) Publish(ctx context.Context, appID int64, d *syncv1.Delta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, d.Version)
	if d.Version == p.failOn {
		return assertErr
	}
	return nil
}
func (p *failOnPub) versions() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.got...)
}

// TestRelay_MidBatchFailurePreservesOrder 验证核心一致性不变量：一批内中间行（id 升序，
// version=2）publish 失败时，仅失败行之前（version=1）被标记 published；失败行及其之后
// （version=2、3）保持未发布且绝不乱序跳投（v3 从未被尝试）。
func TestRelay_MidBatchFailurePreservesOrder(t *testing.T) {
	db := dbtest.SetupSchema(t)
	// 同 app_id=1，version 1/2/3 按升序灌入；IDENTITY 自增使插入顺序即 id 升序。
	for _, v := range []int64{1, 2, 3} {
		blob, _ := proto.Marshal(&syncv1.Delta{Version: uint64(v)})
		_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,$1,$2)`, v, blob)
		require.NoError(t, err)
	}

	pub := &failOnPub{failOn: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) }()

	// 等待第 1 行被标记 published（v2 失败即 break，故 v1 必先发布）。
	require.Eventually(t, func() bool {
		var publishedAtNotNull bool
		if err := db.QueryRow(
			`SELECT published_at IS NOT NULL FROM policy_outbox WHERE version=1`).Scan(&publishedAtNotNull); err != nil {
			return false
		}
		return publishedAtNotNull
	}, 3*time.Second, 50*time.Millisecond)

	// version=1 已发布。
	var v1NotNull bool
	require.NoError(t, db.QueryRow(
		`SELECT published_at IS NOT NULL FROM policy_outbox WHERE version=1`).Scan(&v1NotNull))
	assert.True(t, v1NotNull, "version=1 应已发布")

	// version=2（失败行）与 version=3（其后续）均未发布——证明 break 保序、不跳投。
	var v2Null, v3Null bool
	require.NoError(t, db.QueryRow(
		`SELECT published_at IS NULL FROM policy_outbox WHERE version=2`).Scan(&v2Null))
	require.NoError(t, db.QueryRow(
		`SELECT published_at IS NULL FROM policy_outbox WHERE version=3`).Scan(&v3Null))
	assert.True(t, v2Null, "version=2（失败行）应保持未发布")
	assert.True(t, v3Null, "version=3（失败行之后）应保持未发布")

	// 关键：v3 从未被尝试投递（未越过失败行）。
	assert.NotContains(t, pub.versions(), uint64(3), "v3 不应被投递（未越过失败行）")
}

func TestRelay_PublishFailureKeepsRowUnpublished(t *testing.T) {
	db := dbtest.SetupSchema(t)
	blob, _ := proto.Marshal(&syncv1.Delta{Version: 1})
	_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,1,$1)`, blob)
	require.NoError(t, err)

	pub := &recordingPub{fail: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) }()

	require.Never(t, func() bool {
		var published int
		_ = db.QueryRow(`SELECT count(*) FROM policy_outbox WHERE published_at IS NOT NULL`).Scan(&published)
		return published > 0
	}, 500*time.Millisecond, 50*time.Millisecond)
}
