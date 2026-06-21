package console

import "testing"

func TestConditionPredicate(t *testing.T) {
	cases := map[string]string{
		`{"field":"owner_id","op":"EQ","value":"$user.id"}`:                                                       "owner_id = $user.id",
		`{"field":"status","op":"IN","value":["a","b"]}`:                                                          "status IN [a, b]",
		`{"op":"AND","children":[{"field":"a","op":"EQ","value":"1"},{"field":"b","op":"EQ","value":"$user.x"}]}`: "(a = 1 AND b = $user.x)",
		// 数字 value 直显（不报错）。
		`{"field":"age","op":"GT","value":18}`: "age > 18",
		// NOT 单子节点。
		`{"op":"NOT","children":[{"field":"a","op":"EQ","value":"1"}]}`: "NOT a = 1",
		// 深层嵌套递归（OR 内含叶子，外层 AND 含 IN 叶子）。
		`{"op":"AND","children":[{"op":"OR","children":[{"field":"a","op":"EQ","value":"1"},{"field":"b","op":"GT","value":2}]},{"field":"c","op":"IN","value":["x","y"]}]}`: "((a = 1 OR b > 2) AND c IN [x, y])",
	}
	for cond, want := range cases {
		if got := conditionPredicate(cond); got != want {
			t.Errorf("conditionPredicate(%s)=%q want %q", cond, got, want)
		}
	}
	// fail-soft：以下输入一律回安全占位「（自定义条件）」，绝不 panic、绝不输出半成品/原串。
	failSoft := []string{
		"not-json",                   // 非法 JSON
		"",                           // 空串
		`{}`,                         // 空对象
		`{"op":"AND","children":[]}`, // 空 children 复合（不得渲染成空 "()"）
		`{"op":"NOT","children":[]}`, // NOT 零子节点
		`{"op":"NOT","children":[{"field":"a","op":"EQ","value":"1"},{"field":"b","op":"EQ","value":"2"}]}`, // NOT 多子节点
	}
	for _, cond := range failSoft {
		if got := conditionPredicate(cond); got != "（自定义条件）" {
			t.Errorf("conditionPredicate(%q) fail-soft got %q want 占位", cond, got)
		}
	}
}
