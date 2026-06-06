// Package sydomgorm 把司域数据权限片段注入 GORM 查询。薄适配层：只 wrap sydomsql.Build。
package sydomgorm

import (
	"context"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
	"gorm.io/gorm"
)

// Scope 把数据权限片段注入 GORM 查询，返回 gorm scope。
// 用 Question 方言（?）交 GORM 按驱动自译占位符。
//
//	MatchAll    → 原样返回 db（不加过滤）
//	MatchNone   → db.Where("1=0")（deny-all，绝不放行）
//	Conditional → db.Where(frag, args...)
//
// Build 出错经 db.AddError 注入，使后续 Find 返回该 error 而非执行无过滤查询（fail-close）。
func Scope(fr sydom.FilterResult) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		cl, err := sydomsql.Build(fr, sydomsql.Question, 0)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		switch cl.Kind {
		case sydomsql.MatchAll:
			return db
		case sydomsql.MatchNone:
			return db.Where(cl.SQL)
		default:
			return db.Where(cl.SQL, cl.Args...)
		}
	}
}

// ScopeApply 便捷封装：调 f.FilterSQL 取片段再 Scope。
// FilterSQL 的错误（含 sydom.ErrUnavailable）经 db.AddError 注入，业务据 db.Error 自定 fail-open/close。
func ScopeApply(ctx context.Context, f sydomsql.Filterer, subject, resource string, attrs map[string]any) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		fr, err := f.FilterSQL(ctx, subject, resource, attrs)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		return Scope(fr)(db)
	}
}
