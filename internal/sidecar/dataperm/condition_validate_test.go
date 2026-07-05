package dataperm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCondition(t *testing.T) {
	valid := []string{
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`,
		`{"op":"OR","children":[{"field":"a","op":"IN","value":["x","y"]},{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}]}`,
		`{"field":"amount","op":"BETWEEN","value":[1,100]}`,
		`{"field":"note","op":"IS_NULL"}`,
	}
	for _, raw := range valid {
		require.NoError(t, ValidateCondition(raw), "期望合法: %s", raw)
	}
	invalid := []string{
		``,                           // 空串（parseCondition 报错，与 eval 中毒同源）
		`{"op":"ALL"}`,               // 未知算子
		`{"op":"and","children":[]}`, // 小写 + AND 空 children
		`{"field":"a;DROP","op":"EQ","value":"x"}`,   // 非法字段名
		`{"field":"a","op":"IN","value":"notarray"}`, // IN 非数组
		`{"field":"a","op":"BETWEEN","value":[1]}`,   // BETWEEN 非 2 元
	}
	for _, raw := range invalid {
		require.ErrorIs(t, ValidateCondition(raw), ErrInvalidPolicy, "期望非法被拒: %s", raw)
	}
	// 与内部 parseCondition 完全同源（同一 raw 同一结论）。
	_, perr := parseCondition(`{"op":"ALL"}`)
	require.Equal(t, perr == nil, ValidateCondition(`{"op":"ALL"}`) == nil,
		"ValidateCondition 必须与 parseCondition 同源")
}
