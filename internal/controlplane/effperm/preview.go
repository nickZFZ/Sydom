package effperm

import (
	"context"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
)

// PreviewFilter 在只读 tx 内对 (appID, subject, resource, attrs) 渲染数据权限的参数化 SQL 片段。
// 复用与 Sidecar 数据面完全同源的渲染器：buildEngine 建 app 快照 → dataperm.NewFilter → FilterSQL，
// 杜绝第二套渲染/决策。任一步失败 fail-close 透传 error（含 ErrMissingVar），绝不返回空 SQL 冒充「无限制」。
func PreviewFilter(ctx context.Context, tx cp.DBTX, appID int64, subject, resource string, attrs map[string]any) (dataperm.SQLResult, error) {
	eng, table, _, _, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return dataperm.SQLResult{}, err
	}
	return dataperm.NewFilter(eng, table).FilterSQL(subject, domain, resource, attrs)
}
