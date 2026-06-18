package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExplainDecision 解释单条数据面授权决策「为什么 allow/deny」。app 域只读。
// 鉴权由 AuthorizeRule(scopeApp) 前置完成；本 handler 只在只读 tx 内瞬态求值（复用 effperm，与 Sidecar 同源）。
func (s *AdminServer) ExplainDecision(ctx context.Context, r *adminv1.ExplainDecisionRequest) (*adminv1.ExplainDecisionResponse, error) {
	if r.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	if r.Resource == "" || r.Action == "" {
		return nil, status.Error(codes.InvalidArgument, "resource and action required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	exp, err := effperm.Explain(ctx, tx, int64(r.AppId), r.UserId, r.Resource, r.Action)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "explain decision: %v", err)
	}
	out := &adminv1.ExplainDecisionResponse{
		Allowed:      exp.Allowed,
		Reason:       exp.Reason,
		DecidingRole: exp.DecidingRole,
		Roles:        exp.Roles,
		DataScope:    &adminv1.DecisionDataScope{Match: exp.DataScope.Match, Predicate: exp.DataScope.Predicate},
	}
	if exp.DecidingRule != nil {
		out.DecidingRule = &adminv1.DecidingRule{
			Subject:  exp.DecidingRule.Subject,
			Resource: exp.DecidingRule.Resource,
			Action:   exp.DecidingRule.Action,
			Effect:   exp.DecidingRule.Effect,
		}
	}
	return out, nil
}
