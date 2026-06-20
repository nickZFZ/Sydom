package console

import "testing"

func TestActionLabel(t *testing.T) {
	cases := map[string]string{
		"read": "查看", "list": "查看", "get": "查看", "view": "查看",
		"create": "新建", "add": "新建",
		"update": "编辑", "write": "编辑", "edit": "编辑",
		"delete": "删除", "remove": "删除",
		"export": "导出", "import": "导入",
		"approve": "审批", "reject": "驳回", "assign": "分配",
		"frobnicate": "frobnicate", // 未知 action 原样返回，不臆造
	}
	for action, want := range cases {
		if got := actionLabel(action); got != want {
			t.Errorf("actionLabel(%q)=%q want %q", action, got, want)
		}
	}
}

func TestCapabilityName(t *testing.T) {
	// 显式 name 最优。
	if got := capabilityName("查看订单", "order", "read"); got != "查看订单" {
		t.Errorf("explicit name: got %q", got)
	}
	// 缺 name → 合成「resource · 动词」，绝不裸 resource:action。
	got := capabilityName("", "order", "read")
	if got != "order · 查看" {
		t.Errorf("composed: got %q want %q", got, "order · 查看")
	}
	if got == "order:read" {
		t.Errorf("must not fall back to raw resource:action")
	}
	// 未知 action → actionLabel 透传，合成时不臆造动词（组合路径边界）。
	if got := capabilityName("", "order", "frobnicate"); got != "order · frobnicate" {
		t.Errorf("unknown action: got %q want %q", got, "order · frobnicate")
	}
}

func TestRoleName(t *testing.T) {
	m := map[string]string{"sales": "销售经理"}
	if got := roleName(m, "sales"); got != "销售经理" {
		t.Errorf("hit: got %q", got)
	}
	if got := roleName(m, "unknown"); got != "unknown" {
		t.Errorf("miss must fall back to code, got %q", got)
	}
}
