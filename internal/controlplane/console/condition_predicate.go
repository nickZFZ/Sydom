package console

import "encoding/json"

// condition_predicate —— 把 data_policy 条件树渲染为只读符号谓词（展示用，$user.xxx 保留）。
// 控制面自足，不调用/不解析数据面求值逻辑（Sidecar 零漂移）；解析失败 fail-soft 回安全占位。

type condNode struct {
	Op       string          `json:"op"`
	Field    string          `json:"field"`
	Value    json.RawMessage `json:"value"`
	Children []condNode      `json:"children"`
}

// conditionPredicate 渲染条件树为人类可读谓词；非法/空回「（自定义条件）」。
func conditionPredicate(conditionJSON string) string {
	if conditionJSON == "" {
		return "（自定义条件）"
	}
	var n condNode
	if err := json.Unmarshal([]byte(conditionJSON), &n); err != nil {
		return "（自定义条件）"
	}
	s := renderNode(n)
	if s == "" {
		return "（自定义条件）"
	}
	return s
}

func renderNode(n condNode) string {
	switch n.Op {
	case "AND", "OR":
		var parts []string
		for _, c := range n.Children {
			parts = append(parts, renderNode(c))
		}
		if len(parts) == 0 {
			return ""
		}
		sep := " " + n.Op + " "
		out := parts[0]
		for _, p := range parts[1:] {
			out += sep + p
		}
		return "(" + out + ")"
	case "NOT":
		if len(n.Children) != 1 {
			return ""
		}
		return "NOT " + renderNode(n.Children[0])
	default:
		// 叶子：field op value。
		if n.Field == "" {
			return ""
		}
		op := n.Op
		if op == "" {
			op = "EQ"
		}
		return n.Field + " " + symbol(op) + " " + renderValue(n.Value)
	}
}

func symbol(op string) string {
	switch op {
	case "EQ":
		return "="
	case "NE":
		return "!="
	case "GT":
		return ">"
	case "GE":
		return ">="
	case "LT":
		return "<"
	case "LE":
		return "<="
	default:
		return op // IN/BETWEEN 等保留原 token
	}
}

func renderValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "?"
	}
	// 字符串值（含 $user.xxx）去引号直显；数组渲染为 [a, b]；其余原样。
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, e := range arr {
			parts = append(parts, renderValue(e))
		}
		out := "["
		for i, p := range parts {
			if i > 0 {
				out += ", "
			}
			out += p
		}
		return out + "]"
	}
	return string(raw)
}
