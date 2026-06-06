package sydomsql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
)

type stubFilterer struct {
	fr  sydom.FilterResult
	err error
}

func (s stubFilterer) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error) {
	return s.fr, s.err
}

func TestApply_HappyPath_PostgresRenumber(t *testing.T) {
	f := stubFilterer{fr: sydom.FilterResult{SQL: "dept = ?", Args: []any{"HR"}}}
	cl, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.Conditional || cl.SQL != "dept = $1" {
		t.Fatalf("got kind=%v sql=%q", cl.Kind, cl.SQL)
	}
}

func TestApply_UnavailablePassthrough(t *testing.T) {
	f := stubFilterer{err: sydom.ErrUnavailable}
	_, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Question, 0)
	if !errors.Is(err, sydom.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestApply_BuildInvariantError_Propagates(t *testing.T) {
	// 契约：Build 不变量破坏（? 数 ≠ args 数）经 Apply 透传，绝不带病拼接（fail-close）
	f := stubFilterer{fr: sydom.FilterResult{SQL: "a = ? AND b = ?", Args: []any{1}}}
	_, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Question, 0)
	if err == nil {
		t.Fatal("want error from Build invariant violation")
	}
}

func TestApply_DenyAll_MatchNone(t *testing.T) {
	// 契约：deny-all 片段经 Apply 得 MatchNone（调用方据此返回空集，绝不放行）
	f := stubFilterer{fr: sydom.FilterResult{SQL: "1=0"}}
	cl, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.MatchNone {
		t.Fatalf("want MatchNone, got %v", cl.Kind)
	}
}
