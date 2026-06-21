package console

import "testing"

func TestConditionPredicate(t *testing.T) {
	cases := map[string]string{
		`{"field":"owner_id","op":"EQ","value":"$user.id"}`:                                                       "owner_id = $user.id",
		`{"field":"status","op":"IN","value":["a","b"]}`:                                                          "status IN [a, b]",
		`{"op":"AND","children":[{"field":"a","op":"EQ","value":"1"},{"field":"b","op":"EQ","value":"$user.x"}]}`: "(a = 1 AND b = $user.x)",
	}
	for cond, want := range cases {
		if got := conditionPredicate(cond); got != want {
			t.Errorf("conditionPredicate(%s)=%q want %q", cond, got, want)
		}
	}
	// fail-soft：非法 JSON 不 panic、不泄露原串，回安全占位。
	if got := conditionPredicate("not-json"); got != "（自定义条件）" {
		t.Errorf("bad json fallback got %q", got)
	}
}
