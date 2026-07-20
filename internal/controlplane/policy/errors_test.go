package policy

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/stretchr/testify/require"
)

// classify 是写路径错误分类器：把底层驱动/领域错误归入领域 sentinel，
// 同时保留原始错误在链上（供上层脱敏日志），未匹配者原样返回（落 Internal）。
func TestClassify(t *testing.T) {
	uniq := &pq.Error{Code: "23505"}                            // unique_violation
	fk := &pq.Error{Code: "23503"}                              // foreign_key_violation
	wrappedUniq := fmt.Errorf("policy: mutate grant: %w", uniq) // 真实调用链里 classify 收到的是被包裹后的
	wrappedCycle := fmt.Errorf("policy: precheck add: %w", projection.ErrCycle)
	infra := errors.New("dial tcp: connection refused") // 非领域错误

	t.Run("nil 原样返回 nil", func(t *testing.T) {
		require.NoError(t, classify(nil))
	})

	t.Run("唯一冲突(23505)→ErrConflict 且保留原链", func(t *testing.T) {
		got := classify(wrappedUniq)
		require.ErrorIs(t, got, ErrConflict)
		require.ErrorIs(t, got, uniq, "原始 pq 错误须仍在链上供日志")
	})

	t.Run("外键(23503)→ErrPrecondition", func(t *testing.T) {
		got := classify(fk)
		require.ErrorIs(t, got, ErrPrecondition)
		require.NotErrorIs(t, got, ErrConflict)
	})

	t.Run("角色继承环→ErrPrecondition", func(t *testing.T) {
		got := classify(wrappedCycle)
		require.ErrorIs(t, got, ErrPrecondition)
		require.ErrorIs(t, got, projection.ErrCycle, "原始 ErrCycle 须仍在链上")
	})

	t.Run("store.ErrNotFound→ErrNotFound", func(t *testing.T) {
		require.ErrorIs(t, classify(store.ErrNotFound), ErrNotFound)
	})

	t.Run("sql.ErrNoRows→ErrNotFound", func(t *testing.T) {
		require.ErrorIs(t, classify(sql.ErrNoRows), ErrNotFound)
	})

	t.Run("非领域错误原样返回，不误挂任何 sentinel", func(t *testing.T) {
		got := classify(infra)
		require.Equal(t, infra, got, "未匹配错误须原样返回")
		require.NotErrorIs(t, got, ErrConflict)
		require.NotErrorIs(t, got, ErrPrecondition)
		require.NotErrorIs(t, got, ErrNotFound)
		require.NotErrorIs(t, got, ErrInvalidInput)
	})
}
