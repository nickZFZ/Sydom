package dataperm

import (
	"fmt"
	"strings"
)

// SymbolicResult 是符号预览结果：Match 表整体语义，Predicate 为人类可读谓词（$user.xxx 保留符号）。
type SymbolicResult struct {
	Match     string // MatchAll | MatchNone | MatchConditional
	Predicate string // 仅 MatchConditional 非空
}

// FilterSymbolic 渲染合并后的行过滤谓词，变量保持符号形态（不解析 attrs）。
// 仅供展示：值内联进字符串，绝不进任何 SQL，无注入面。
func (f *Filter) FilterSymbolic(user, dom, resource string) (SymbolicResult, error) {
	p, err := f.selectAndMerge(user, dom, resource)
	if err != nil {
		return SymbolicResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return SymbolicResult{Match: MatchAll}, nil
	case modeDenyAll:
		return SymbolicResult{Match: MatchNone}, nil
	default:
		var b strings.Builder
		renderSymbolic(p.tree, &b)
		return SymbolicResult{Match: MatchConditional, Predicate: b.String()}, nil
	}
}

func renderSymbolic(c *Condition, b *strings.Builder) {
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
			renderSymbolic(ch, b)
		}
		b.WriteByte(')')
	case OpNot:
		b.WriteString("NOT (")
		renderSymbolic(c.Children[0], b)
		b.WriteByte(')')
	default:
		renderSymbolicLeaf(c, b)
	}
}

func renderSymbolicLeaf(c *Condition, b *strings.Builder) {
	switch c.Op {
	case OpIsNull:
		fmt.Fprintf(b, "%s IS NULL", c.Field)
	case OpIsNotNull:
		fmt.Fprintf(b, "%s IS NOT NULL", c.Field)
	case OpIN, OpNotIn:
		kw := "IN"
		if c.Op == OpNotIn {
			kw = "NOT IN"
		}
		fmt.Fprintf(b, "%s %s (%s)", c.Field, kw, symbolicList(c.Value))
	case OpBetween:
		if arr, ok := c.Value.([]any); ok && len(arr) == 2 {
			fmt.Fprintf(b, "%s BETWEEN %s AND %s", c.Field, symbolicValue(arr[0]), symbolicValue(arr[1]))
		} else {
			// 解析期已保证 BETWEEN 为 2 元数组，此分支不可达；防御性给可见输出而非静默空串，
			// 与 render_sql.go 的兜底风格对齐（彼处返回 error，符号路径无 error 通道故内联标记）。
			fmt.Fprintf(b, "%s BETWEEN <invalid>", c.Field)
		}
	default: // 标量比较 / LIKE
		fmt.Fprintf(b, "%s %s %s", c.Field, sqlComparator(c.Op), symbolicValue(c.Value))
	}
}

// symbolicValue 格式化展示值：$user.xxx 原样；字符串字面量加单引号；数值/布尔 fmt。
func symbolicValue(v any) string {
	if s, ok := v.(string); ok {
		if strings.HasPrefix(s, "$user.") {
			return s
		}
		return "'" + s + "'"
	}
	return fmt.Sprintf("%v", v)
}

func symbolicList(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, len(arr))
	for i, e := range arr {
		parts[i] = symbolicValue(e)
	}
	return strings.Join(parts, ", ")
}
