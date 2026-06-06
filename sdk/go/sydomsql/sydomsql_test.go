package sydomsql_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
)

func TestBuild_MatchAll(t *testing.T) {
	cl, err := sydomsql.Build(sydom.FilterResult{SQL: ""}, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.MatchAll || cl.SQL != "" || cl.Args != nil {
		t.Fatalf("got %+v", cl)
	}
}

func TestBuild_MatchNone(t *testing.T) {
	cl, err := sydomsql.Build(sydom.FilterResult{SQL: "1=0"}, sydomsql.Question, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.MatchNone || cl.SQL != "1=0" {
		t.Fatalf("got %+v", cl)
	}
}

func TestBuild_Conditional_Question_Passthrough(t *testing.T) {
	fr := sydom.FilterResult{SQL: "dept = ? AND status <> ?", Args: []any{"HR", "void"}}
	cl, err := sydomsql.Build(fr, sydomsql.Question, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.Conditional || cl.SQL != "dept = ? AND status <> ?" {
		t.Fatalf("got sql=%q", cl.SQL)
	}
	if len(cl.Args) != 2 || cl.Args[0] != "HR" || cl.Args[1] != "void" {
		t.Fatalf("args=%v", cl.Args)
	}
}

func TestBuild_Conditional_Postgres_Renumber(t *testing.T) {
	fr := sydom.FilterResult{SQL: "(dept = ? AND NOT (status IN (?, ?)))", Args: []any{"HR", "locked", "void"}}
	cl, err := sydomsql.Build(fr, sydomsql.Postgres, 1) // 既有 1 个占位符，片段从 $2 起
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "(dept = $2 AND NOT (status IN ($3, $4)))"
	if cl.SQL != want {
		t.Fatalf("got %q want %q", cl.SQL, want)
	}
}

func TestBuild_Conditional_Postgres_StartIndexZero(t *testing.T) {
	// 最常见路径：首个片段，startIndex=0 → 从 $1 起（off-by-one 边界）
	fr := sydom.FilterResult{SQL: "dept = ? AND status <> ?", Args: []any{"HR", "void"}}
	cl, err := sydomsql.Build(fr, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.SQL != "dept = $1 AND status <> $2" {
		t.Fatalf("got %q", cl.SQL)
	}
}

func TestBuild_Conditional_ZeroPlaceholders(t *testing.T) {
	// 合法 Conditional 但零占位符（对应 dataperm OpIsNull）：n==len(Args)==0，原样透传
	fr := sydom.FilterResult{SQL: "active IS NULL"}
	cl, err := sydomsql.Build(fr, sydomsql.Postgres, 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.Conditional || cl.SQL != "active IS NULL" || len(cl.Args) != 0 {
		t.Fatalf("got %+v", cl)
	}
}

func TestBuild_InvariantViolation(t *testing.T) {
	// 2 个 ? 但只有 1 个 arg → fail-close
	_, err := sydomsql.Build(sydom.FilterResult{SQL: "a = ? AND b = ?", Args: []any{1}}, sydomsql.Question, 0)
	if err == nil {
		t.Fatal("want error on placeholder/arg mismatch")
	}
}

func TestAndWhere_MatchAll_Unchanged(t *testing.T) {
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, sydom.FilterResult{SQL: ""}, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "tenant_id = $1" || len(args) != 1 || args[0] != 42 {
		t.Fatalf("got where=%q args=%v", where, args)
	}
}

func TestAndWhere_MatchNone_BaseNonEmpty_NeverOpen(t *testing.T) {
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, sydom.FilterResult{SQL: "1=0"}, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "(tenant_id = $1) AND (1=0)" {
		t.Fatalf("deny-all 退化为放行: %q", where)
	}
	if len(args) != 1 {
		t.Fatalf("args=%v", args)
	}
}

func TestAndWhere_MatchNone_BaseEmpty(t *testing.T) {
	where, _, err := sydomsql.AndWhere("", nil, sydom.FilterResult{SQL: "1=0"}, sydomsql.Question)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "1=0" {
		t.Fatalf("got %q", where)
	}
}

func TestAndWhere_Conditional_Postgres_OffsetByBaseArgs(t *testing.T) {
	fr := sydom.FilterResult{SQL: "(dept = ? AND NOT (status IN (?, ?)))", Args: []any{"HR", "locked", "void"}}
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, fr, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "(tenant_id = $1) AND ((dept = $2 AND NOT (status IN ($3, $4))))"
	if where != want {
		t.Fatalf("got %q want %q", where, want)
	}
	if len(args) != 4 || args[0] != 42 || args[1] != "HR" || args[3] != "void" {
		t.Fatalf("args=%v", args)
	}
}

func TestAndWhere_Conditional_BaseEmpty_Question(t *testing.T) {
	fr := sydom.FilterResult{SQL: "dept = ?", Args: []any{"HR"}}
	where, args, err := sydomsql.AndWhere("", nil, fr, sydomsql.Question)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "dept = ?" || len(args) != 1 || args[0] != "HR" {
		t.Fatalf("got where=%q args=%v", where, args)
	}
}

func TestAndWhere_MatchAll_BaseEmpty(t *testing.T) {
	// 契约：MatchAll + base 空 → 空 where、nil args（调用方据此不附加 WHERE）
	where, args, err := sydomsql.AndWhere("", nil, sydom.FilterResult{SQL: ""}, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "" || args != nil {
		t.Fatalf("got where=%q args=%v", where, args)
	}
}

func TestAndWhere_BuildError_Propagates(t *testing.T) {
	// 契约：Build 不变量破坏（? 数 ≠ args 数）必须经 AndWhere 透传，不静默拼接（fail-close）
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42},
		sydom.FilterResult{SQL: "a = ? AND b = ?", Args: []any{1}}, sydomsql.Postgres)
	if err == nil {
		t.Fatal("want error propagated from Build")
	}
	if where != "" || args != nil {
		t.Fatalf("出错时不应返回部分结果: where=%q args=%v", where, args)
	}
}

func TestAndWhere_Conditional_Question_BaseNonEmpty(t *testing.T) {
	// Question 方言 + base 非空：片段 ? 原样透传，括号包裹 AND 拼接，args 合并
	fr := sydom.FilterResult{SQL: "dept = ?", Args: []any{"HR"}}
	where, args, err := sydomsql.AndWhere("tenant_id = ?", []any{42}, fr, sydomsql.Question)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "(tenant_id = ?) AND (dept = ?)" {
		t.Fatalf("got %q", where)
	}
	if len(args) != 2 || args[0] != 42 || args[1] != "HR" {
		t.Fatalf("args=%v", args)
	}
}
