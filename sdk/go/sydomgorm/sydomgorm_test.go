package sydomgorm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomgorm"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type order struct {
	ID     uint
	Dept   string
	Status string
}

type stubFilterer struct {
	fr  sydom.FilterResult
	err error
}

func (s stubFilterer) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error) {
	return s.fr, s.err
}

func newDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return gdb
}

func TestScope_Conditional(t *testing.T) {
	db := newDryRunDB(t)
	fr := sydom.FilterResult{SQL: "dept = ? AND status <> ?", Args: []any{"HR", "void"}}
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(fr)).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	got := res.Statement.SQL.String()
	if !strings.Contains(got, "dept = ?") || !strings.Contains(got, "status <> ?") {
		t.Fatalf("missing where: %q", got)
	}
	if len(res.Statement.Vars) != 2 {
		t.Fatalf("want 2 vars, got %v", res.Statement.Vars)
	}
}

func TestScope_MatchNone(t *testing.T) {
	db := newDryRunDB(t)
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: "1=0"})).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	if !strings.Contains(res.Statement.SQL.String(), "1=0") {
		t.Fatalf("deny-all 丢失: %q", res.Statement.SQL.String())
	}
}

func TestScope_MatchAll_NoWhere(t *testing.T) {
	db := newDryRunDB(t)
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: ""})).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	if strings.Contains(res.Statement.SQL.String(), "WHERE") {
		t.Fatalf("MatchAll 不应有 WHERE: %q", res.Statement.SQL.String())
	}
}

func TestScope_InvariantError(t *testing.T) {
	db := newDryRunDB(t)
	// 1 个 ? 但 0 个 arg → Build 报错 → AddError，fail-close
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: "a = ?"})).Find(&[]order{})
	if res.Error == nil {
		t.Fatal("want error on invariant violation")
	}
}

func TestScopeApply_UnavailablePropagates(t *testing.T) {
	db := newDryRunDB(t)
	f := stubFilterer{err: sydom.ErrUnavailable}
	res := db.Model(&order{}).Scopes(sydomgorm.ScopeApply(context.Background(), f, "alice", "order", nil)).Find(&[]order{})
	if !errors.Is(res.Error, sydom.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", res.Error)
	}
}
