// Package controlplane 持有控制面策略核心引擎的共享领域类型。
package controlplane

import (
	"context"
	"database/sql"
)

// Rule 是一条 casbin_rule（ptype + v0..v5）。空位用空串（casbin 惯例）。
type Rule struct {
	Ptype string
	V     [6]string
}

// ChangeOp 是 data_policy 变更类型。
type ChangeOp int

const (
	ChangeAdd ChangeOp = iota
	ChangeUpdate
	ChangeRemove
)

// data_policy.effect 取值（空串按 EffectAllow，对齐 DB 默认）。
const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// DataPolicy 是一条数据权限规则（条件树以 JSON 字符串承载，协议层不透明）。
type DataPolicy struct {
	ID          int64
	SubjectType string // "role" / "user"
	SubjectID   string
	Resource    string
	Condition   string // 条件树 JSON
	Effect      string // "allow" | "deny"；空串按 "allow"（对齐 DB 默认）
}

// DataPolicyChange 是一次 data_policy 变更。
type DataPolicyChange struct {
	Op     ChangeOp
	Policy DataPolicy
}

// Delta 是一次写事务的产物，供 ③-2 翻译并下发。Version 用 int64（与 DB BIGINT 一致）。
type Delta struct {
	AppID       int64
	Version     int64
	RuleAdds    []Rule
	RuleRemoves []Rule
	DataChanges []DataPolicyChange
}

// DBTX 是 *sql.DB 与 *sql.Tx 的公共子集，使 DB 访问函数既能在事务内、也能独立调用。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type operatorKey struct{}

// WithOperator 把操作者标识注入 context（③-3 从认证上下文设置）。
func WithOperator(ctx context.Context, operator string) context.Context {
	return context.WithValue(ctx, operatorKey{}, operator)
}

// OperatorFromContext 取操作者；未设置时返回 "system"。
func OperatorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(operatorKey{}).(string); ok && v != "" {
		return v
	}
	return "system"
}
