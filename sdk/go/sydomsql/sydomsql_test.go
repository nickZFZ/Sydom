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
