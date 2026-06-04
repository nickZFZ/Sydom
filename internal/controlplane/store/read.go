package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ResolveAppIDByKey 把外部 app_key（认证凭据标识）解析为 application.id。
// 无对应应用即报错（fail-close，不返回 0 让调用方误用）。
func ResolveAppIDByKey(ctx context.Context, q cp.DBTX, appKey string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`SELECT id FROM application WHERE app_key=$1`, appKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: unknown app_key %q", appKey)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ReadCurrentVersion 读取 app 当前版本号（只读路径，不加 FOR UPDATE，不串行化写）。
func ReadCurrentVersion(ctx context.Context, q cp.DBTX, appID int64) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx,
		`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: unknown app id=%d", appID)
	}
	return v, err
}

// ReadAppDataPolicies 读取某 app 全部数据策略（供全量快照）。
func ReadAppDataPolicies(ctx context.Context, q cp.DBTX, appID int64) ([]cp.DataPolicy, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, subject_type, subject_id, resource, condition, effect FROM data_policy WHERE app_id=$1`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cp.DataPolicy
	for rows.Next() {
		var p cp.DataPolicy
		if err := rows.Scan(&p.ID, &p.SubjectType, &p.SubjectID, &p.Resource, &p.Condition, &p.Effect); err != nil {
			return nil, fmt.Errorf("scan data_policy: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
