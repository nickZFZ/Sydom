package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// AppAuditFilter 是 QueryAppAudit 的过滤参数。零值字段不参与过滤。
type AppAuditFilter struct {
	EntityType, EntityID, Action, Operator string
	Since, Until                           time.Time
	Cursor                                 uint64 // keyset：仅返回 id < Cursor 的行（0=从头）
	Limit                                  int
}

// AppAuditEntry 是 policy_audit_log 的一行投影。
type AppAuditEntry struct {
	ID                           int64
	Operator, Action, EntityType string
	EntityID, Diff               sql.NullString
	Version                      int64
	CreatedAt                    time.Time
}

// QueryAppAudit 按 app_id + 过滤做 keyset 分页（id 降序，新→旧）。
// 内部取 Limit+1 行判断是否有下页，返回 trim 到 Limit 的条目与 nextCursor（无下页=0）。
// 全部参数化（绝不拼接用户值）。
func QueryAppAudit(ctx context.Context, q cp.DBTX, appID int64, f AppAuditFilter) ([]AppAuditEntry, uint64, error) {
	conds := []string{"app_id = $1"}
	args := []any{appID}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+strconv.Itoa(len(args)))
	}
	if f.Cursor > 0 {
		add("id <", int64(f.Cursor))
	}
	if f.EntityType != "" {
		add("entity_type =", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id =", f.EntityID)
	}
	if f.Action != "" {
		add("action =", f.Action)
	}
	if f.Operator != "" {
		add("operator =", f.Operator)
	}
	if !f.Since.IsZero() {
		add("created_at >=", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at <=", f.Until)
	}
	args = append(args, f.Limit+1)
	query := `SELECT id, operator, action, entity_type, entity_id, diff, version, created_at
		FROM policy_audit_log WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AppAuditEntry
	for rows.Next() {
		var e AppAuditEntry
		if err := rows.Scan(&e.ID, &e.Operator, &e.Action, &e.EntityType,
			&e.EntityID, &e.Diff, &e.Version, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(out) > f.Limit {
		next = uint64(out[f.Limit-1].ID)
		out = out[:f.Limit]
	}
	return out, next, nil
}
