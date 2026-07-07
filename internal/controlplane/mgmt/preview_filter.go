package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PreviewDataFilter 预览数据权限渲染出的参数化 SQL 片段（app 域只读）。
// 鉴权由 AuthorizeRule(scopeApp read) 前置；本 handler 在只读 tx 内复用 effperm.PreviewFilter
// （与 Sidecar 数据面同源，零第二套渲染）。缺变量等 → InvalidArgument（报错而非误导性 SQL）。
func (s *AdminServer) PreviewDataFilter(ctx context.Context, r *adminv1.PreviewDataFilterRequest) (*adminv1.PreviewDataFilterResponse, error) {
	if r.Subject == "" || r.Resource == "" {
		return nil, status.Error(codes.InvalidArgument, "subject and resource required")
	}
	attrs := make(map[string]any, len(r.Attrs))
	for k, v := range r.Attrs {
		attrs[k] = v
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := effperm.PreviewFilter(ctx, tx, int64(r.AppId), r.Subject, r.Resource, attrs)
	if err != nil {
		if errors.Is(err, dataperm.ErrMissingVar) {
			return nil, status.Errorf(codes.InvalidArgument, "数据策略引用了未提供的属性：%v", err)
		}
		return nil, status.Errorf(codes.Internal, "preview data filter: %v", err)
	}
	args := make([]string, len(res.Args))
	for i, a := range res.Args {
		args[i] = fmt.Sprintf("%v", a)
	}
	return &adminv1.PreviewDataFilterResponse{Sql: res.SQL, Args: args}, nil
}
