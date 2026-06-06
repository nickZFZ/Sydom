// Package sydomsql 把司域数据权限的参数化 SQL 片段（sydom.FilterResult）译成目标
// 数据库方言并安全拼入查询。通用核心，不依赖任何具体 ORM。
package sydomsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// Dialect 决定占位符风格。
type Dialect int

const (
	// Postgres 用 $1, $2, … 编号占位符。
	Postgres Dialect = iota
	// Question 用 ?（MySQL / SQLite），与 FilterResult 原生风格一致。
	Question
)

// Kind 是数据权限片段的整体语义三态。
type Kind int

const (
	// MatchAll 无过滤：该 resource 未配数据策略，不应附加 WHERE（非泄漏）。
	MatchAll Kind = iota
	// MatchNone deny-all：恒假，必须返回空集。
	MatchNone
	// Conditional 有条件片段。
	Conditional
)

// Clause 是译成目标方言后的结果。用 Build 的调用方必须 switch Kind 并显式处理 MatchNone，
// 否则丢弃 deny-all 会导致行级越权泄漏。
type Clause struct {
	Kind Kind
	SQL  string // MatchAll=""；MatchNone="1=0"；Conditional=已按方言改写的片段
	Args []any  // MatchAll/MatchNone=nil；Conditional=占位符实参
}

// denyAllSQL 是 deny-all 的恒假谓词（两方言通用，无占位符）。
const denyAllSQL = "1=0"

// Build 把 FilterResult 译成目标方言的 Clause。
// startIndex 为既有占位符数（Postgres 续号偏移；Question 方言忽略）。
// 不变量：片段内 ? 数须 == len(fr.Args)，否则返回 error（fail-close，不带病拼 SQL）。
func Build(fr sydom.FilterResult, d Dialect, startIndex int) (Clause, error) {
	switch fr.SQL {
	case "":
		return Clause{Kind: MatchAll}, nil
	case denyAllSQL:
		return Clause{Kind: MatchNone, SQL: denyAllSQL}, nil
	}
	n := strings.Count(fr.SQL, "?")
	if n != len(fr.Args) {
		return Clause{}, fmt.Errorf("sydomsql: 占位符数 %d 与参数数 %d 不一致", n, len(fr.Args))
	}
	sql := fr.SQL
	if d == Postgres {
		sql = toDollar(fr.SQL, startIndex)
	}
	return Clause{Kind: Conditional, SQL: sql, Args: append([]any(nil), fr.Args...)}, nil
}

// toDollar 把从左到右的每个 ? 替换为 $startIndex+1, $startIndex+2, …
// 前置不变量：s 中除占位符外无其它 ?（已回源核实 dataperm 渲染器）。
func toDollar(s string, startIndex int) string {
	var b strings.Builder
	idx := startIndex
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			idx++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(idx))
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
