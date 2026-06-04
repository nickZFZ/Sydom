package dataperm

import (
	"fmt"
	"strings"
)

// SQLResult 是 sql 方言结果：参数化模板 + 参数（值绝不进 SQL 文本）。
type SQLResult struct {
	SQL  string
	Args []any
}

// FilterSQL 渲染参数化 SQL 片段。无过滤→空串；deny-all→"1=0"；否则参数化条件。
func (f *Filter) FilterSQL(user, dom, resource string, attrs map[string]any) (SQLResult, error) {
	p, err := f.buildPlan(user, dom, resource, attrs)
	if err != nil {
		return SQLResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return SQLResult{}, nil
	case modeDenyAll:
		return SQLResult{SQL: "1=0"}, nil
	default:
		var b strings.Builder
		var args []any
		renderSQL(p.tree, &b, &args)
		return SQLResult{SQL: b.String(), Args: args}, nil
	}
}

// renderSQL 递归渲染已解析条件树为参数化 SQL。字段名已在解析期白名单校验，可安全内联。
func renderSQL(c *Condition, b *strings.Builder, args *[]any) {
	switch c.Op {
	case OpAnd, OpOr:
		sep := " AND "
		if c.Op == OpOr {
			sep = " OR "
		}
		b.WriteByte('(')
		for i, ch := range c.Children {
			if i > 0 {
				b.WriteString(sep)
			}
			renderSQL(ch, b, args)
		}
		b.WriteByte(')')
	case OpNot:
		b.WriteString("NOT (")
		renderSQL(c.Children[0], b, args)
		b.WriteByte(')')
	default:
		renderLeaf(c, b, args)
	}
}

func renderLeaf(c *Condition, b *strings.Builder, args *[]any) {
	switch c.Op {
	case OpIsNull:
		fmt.Fprintf(b, "%s IS NULL", c.Field)
	case OpIsNotNull:
		fmt.Fprintf(b, "%s IS NOT NULL", c.Field)
	case OpIN, OpNotIn:
		arr := c.Value.([]any)
		kw := "IN"
		if c.Op == OpNotIn {
			kw = "NOT IN"
		}
		ph := make([]string, len(arr))
		for i := range arr {
			ph[i] = "?"
			*args = append(*args, arr[i])
		}
		fmt.Fprintf(b, "%s %s (%s)", c.Field, kw, strings.Join(ph, ", "))
	case OpBetween:
		arr := c.Value.([]any)
		fmt.Fprintf(b, "%s BETWEEN ? AND ?", c.Field)
		*args = append(*args, arr[0], arr[1])
	default: // 标量比较 / LIKE
		fmt.Fprintf(b, "%s %s ?", c.Field, sqlComparator(c.Op))
		*args = append(*args, c.Value)
	}
}

func sqlComparator(op string) string {
	switch op {
	case OpEQ:
		return "="
	case OpNE:
		return "<>"
	case OpGT:
		return ">"
	case OpGE:
		return ">="
	case OpLT:
		return "<"
	case OpLE:
		return "<="
	case OpLike:
		return "LIKE"
	case OpNotLike:
		return "NOT LIKE"
	default:
		return "="
	}
}
