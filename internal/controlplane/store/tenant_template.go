package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/lib/pq"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ErrNotFound 表示记录不存在或跨租户访问——fail-close：不泄露存在性。
var ErrNotFound = errors.New("store: not found")

// ErrConflict 表示违反唯一约束（如同租户重名）。
var ErrConflict = errors.New("store: conflict")

// TenantTemplate 是租户私有模板的 DB 投影（bundle 为不透明 JSON blob，协议层解析）。
type TenantTemplate struct {
	ID          int64
	TenantID    int64
	Name        string
	Description string
	Bundle      []byte
	SourceAppID int64
}

// InsertTenantTemplate 写入一条租户私有模板；同租户重名→ErrConflict。
func InsertTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID int64, name, desc string, bundle []byte, sourceAppID int64) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx,
		`INSERT INTO tenant_template (tenant_id, name, description, bundle, source_app_id)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tenantID, name, desc, bundle, sourceAppID).Scan(&id)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetTenantTemplate 按 (tenant_id, id) 取模板；记录不存在或跨租户→ErrNotFound（不泄露存在性）。
func GetTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID, id int64) (TenantTemplate, error) {
	var t TenantTemplate
	var srcApp sql.NullInt64
	err := ex.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(description,''), bundle, source_app_id
		 FROM tenant_template WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.Bundle, &srcApp)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantTemplate{}, ErrNotFound
	}
	if err != nil {
		return TenantTemplate{}, err
	}
	t.SourceAppID = srcApp.Int64
	return t, nil
}

// ListTenantTemplates 列出某租户的模板（分页/搜索/排序），返回行切片与 total。
// order 由调用方经 resolveOrder 白名单化后整体传入（如 "id ASC"），本函数信任上游已校验。
func ListTenantTemplates(ctx context.Context, ex cp.DBTX, tenantID int64, limit, offset int, order, q string) ([]TenantTemplate, uint32, error) {
	conds := []string{"tenant_id = $1"}
	args := []any{tenantID}
	if q != "" {
		args = append(args, "%"+q+"%")
		conds = append(conds, "(name ILIKE $"+strconv.Itoa(len(args))+" OR description ILIKE $"+strconv.Itoa(len(args))+")")
	}
	where := strings.Join(conds, " AND ")

	var total uint32
	if err := ex.QueryRowContext(ctx, `SELECT count(*) FROM tenant_template WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	// #nosec G201 — order 由调用方 resolveOrder 白名单化后整体传入，非用户原始输入。
	rows, err := ex.QueryContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(description,''), source_app_id FROM tenant_template WHERE `+where+
			` ORDER BY `+order+
			` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []TenantTemplate
	for rows.Next() {
		var t TenantTemplate
		var srcApp sql.NullInt64
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &srcApp); err != nil {
			return nil, 0, err
		}
		t.SourceAppID = srcApp.Int64
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// DeleteTenantTemplate 删某租户的一条模板；记录不存在或跨租户→ErrNotFound（不泄露存在性）。
func DeleteTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID, id int64) error {
	res, err := ex.ExecContext(ctx, `DELETE FROM tenant_template WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
