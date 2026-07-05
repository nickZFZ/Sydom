package dataperm

import (
	"strings"
	"testing"
)

func TestValidateCondition(t *testing.T) {
	valid := []string{
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`,
		`{"op":"OR","children":[{"field":"a","op":"IN","value":["x","y"]},{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}]}`,
		`{"field":"amount","op":"BETWEEN","value":[1,100]}`,
		`{"field":"note","op":"IS_NULL"}`,
	}
	for _, raw := range valid {
		if err := ValidateCondition(raw); err != nil {
			t.Errorf("期望合法，得 err: %s → %v", raw, err)
		}
	}
	invalid := []string{
		``,                                          // 空串（parseCondition 报错，与 eval 中毒同源）
		`{"op":"ALL"}`,                               // 未知算子
		`{"op":"and","children":[]}`,                 // 小写 + AND 空 children
		`{"field":"a;DROP","op":"EQ","value":"x"}`,   // 非法字段名
		`{"field":"a","op":"IN","value":"notarray"}`, // IN 非数组
		`{"field":"a","op":"BETWEEN","value":[1]}`,   // BETWEEN 非 2 元
	}
	for _, raw := range invalid {
		if err := ValidateCondition(raw); err == nil {
			t.Errorf("期望非法被拒，得 nil: %s", raw)
		}
	}
	// 与内部 parseCondition 完全同源（同一 raw 同一结论）。
	if (ValidateCondition(`{"op":"ALL"}`) == nil) != func() bool { _, e := parseCondition(`{"op":"ALL"}`); return e == nil }() {
		t.Fatal("ValidateCondition 必须与 parseCondition 同源")
	}
	_ = strings.TrimSpace
}
