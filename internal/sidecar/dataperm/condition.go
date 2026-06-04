package dataperm

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// 逻辑算子。
const (
	OpAnd = "AND"
	OpOr  = "OR"
	OpNot = "NOT"
)

// 叶子算子。
const (
	OpEQ        = "EQ"
	OpNE        = "NE"
	OpGT        = "GT"
	OpGE        = "GE"
	OpLT        = "LT"
	OpLE        = "LE"
	OpIN        = "IN"
	OpNotIn     = "NOT_IN"
	OpLike      = "LIKE"
	OpNotLike   = "NOT_LIKE"
	OpIsNull    = "IS_NULL"
	OpIsNotNull = "IS_NOT_NULL"
	OpBetween   = "BETWEEN"
)

// fieldNameRe 是字段名白名单：字段进 SQL 文本而非参数，必须为合法标识符（堵注入）。
var fieldNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Condition 是条件树节点：逻辑节点（Op∈{AND,OR,NOT} + Children）或叶子（Field+Op+Value）。
type Condition struct {
	Op       string       `json:"op"`
	Children []*Condition `json:"children,omitempty"`
	Field    string       `json:"field,omitempty"`
	Value    any          `json:"value,omitempty"`
}

func isLogicalOp(op string) bool { return op == OpAnd || op == OpOr || op == OpNot }

func isLeafOp(op string) bool {
	switch op {
	case OpEQ, OpNE, OpGT, OpGE, OpLT, OpLE, OpIN, OpNotIn, OpLike, OpNotLike, OpIsNull, OpIsNotNull, OpBetween:
		return true
	}
	return false
}

// parseCondition 从不透明 JSON 解析并校验整棵树（fail-close）。
func parseCondition(raw string) (*Condition, error) {
	var c Condition
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("%w: 非法 JSON: %v", ErrInvalidPolicy, err)
	}
	if err := validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func validate(c *Condition) error {
	switch {
	case isLogicalOp(c.Op):
		if c.Field != "" || c.Value != nil {
			return fmt.Errorf("%w: 逻辑节点 %s 不应含 field/value", ErrInvalidPolicy, c.Op)
		}
		if c.Op == OpNot {
			if len(c.Children) != 1 {
				return fmt.Errorf("%w: NOT 必须恰好 1 个子节点", ErrInvalidPolicy)
			}
		} else if len(c.Children) == 0 {
			return fmt.Errorf("%w: %s 至少 1 个子节点", ErrInvalidPolicy, c.Op)
		}
		for _, ch := range c.Children {
			if err := validate(ch); err != nil {
				return err
			}
		}
		return nil
	case isLeafOp(c.Op):
		return validateLeaf(c)
	default:
		return fmt.Errorf("%w: 未知算子 %q", ErrInvalidPolicy, c.Op)
	}
}

func validateLeaf(c *Condition) error {
	if !fieldNameRe.MatchString(c.Field) {
		return fmt.Errorf("%w: 非法字段名 %q", ErrInvalidPolicy, c.Field)
	}
	switch c.Op {
	case OpIsNull, OpIsNotNull:
		if c.Value != nil {
			return fmt.Errorf("%w: %s 不应带 value", ErrInvalidPolicy, c.Op)
		}
	case OpIN, OpNotIn:
		arr, ok := c.Value.([]any)
		if !ok || len(arr) == 0 {
			return fmt.Errorf("%w: %s 需非空数组 value", ErrInvalidPolicy, c.Op)
		}
	case OpBetween:
		arr, ok := c.Value.([]any)
		if !ok || len(arr) != 2 {
			return fmt.Errorf("%w: %s 需 2 元数组 value", ErrInvalidPolicy, c.Op)
		}
	default: // 标量比较 / LIKE
		if c.Value == nil {
			return fmt.Errorf("%w: %s 需 value", ErrInvalidPolicy, c.Op)
		}
		if _, isArr := c.Value.([]any); isArr {
			return fmt.Errorf("%w: %s value 不应为数组", ErrInvalidPolicy, c.Op)
		}
	}
	return nil
}
