package policy

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// 写路径领域 sentinel。PolicyManager 的写方法出错时，若属下列领域类别则错误链上
// 携带对应 sentinel（errors.Is 可判定），上层（mgmt/console）据此细化 gRPC/HTTP 状态码，
// 而非把一切压成 Internal。未归类的底层错误原样上抛，仍走 Internal + 脱敏日志。
var (
	// ErrInvalidInput 请求参数本身非法（如模板 id 含保留分隔符）。→ InvalidArgument。
	ErrInvalidInput = errors.New("policy: invalid input")
	// ErrConflict 违反唯一约束（重名 code、重复绑定等）。→ AlreadyExists。
	ErrConflict = errors.New("policy: conflict")
	// ErrNotFound 目标实体不存在（或跨租户不可见，不泄露存在性）。→ NotFound。
	ErrNotFound = errors.New("policy: not found")
	// ErrPrecondition 当前系统状态不满足操作前提（外键引用缺失、角色继承成环）。→ FailedPrecondition。
	ErrPrecondition = errors.New("policy: precondition failed")
)

// PostgreSQL SQLSTATE（github.com/lib/pq 的 pq.Error.Code）。
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// classify 把写路径底层错误归入领域 sentinel。匹配时用 %w 二次包裹：
// 既让 errors.Is(err, ErrX) 为真，又完整保留原始错误链（供上层脱敏后写日志）。
// 未匹配（含 nil）者原样返回——由上层落 Internal，绝不误标类别。
func classify(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, projection.ErrCycle):
		return fmt.Errorf("%w: %w", ErrPrecondition, err)
	case isPGCode(err, pgUniqueViolation):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case isPGCode(err, pgForeignKeyViolation):
		return fmt.Errorf("%w: %w", ErrPrecondition, err)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

// isPGCode 报告 err 链上是否有一个 pq.Error 且其 SQLSTATE 命中给定之一。
func isPGCode(err error, codes ...string) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	for _, c := range codes {
		if string(pqErr.Code) == c {
			return true
		}
	}
	return false
}
