package iac

import "testing"

func TestParse_AutoDetectsJSONAndYAML_SameModel(t *testing.T) {
	js := `{"apiVersion":"sydom.policy/v1","permissions":[{"code":"order:read","resource":"order","action":"read","type":"app","name":"查看订单"}],"roles":[{"key":"viewer","name":"查看员","permission_codes":["order:read"]}]}`
	ya := "apiVersion: sydom.policy/v1\npermissions:\n  - code: order:read\n    resource: order\n    action: read\n    type: app\n    name: 查看订单\nroles:\n  - key: viewer\n    name: 查看员\n    permission_codes: [order:read]\n"
	dj, err := Parse([]byte(js))
	if err != nil {
		t.Fatalf("json parse: %v", err)
	}
	dy, err := Parse([]byte(ya))
	if err != nil {
		t.Fatalf("yaml parse: %v", err)
	}
	if len(dj.Permissions) != 1 || dj.Permissions[0].Code != "order:read" {
		t.Fatalf("json model: %+v", dj)
	}
	if len(dy.Roles) != 1 || dy.Roles[0].PermissionCodes[0] != "order:read" {
		t.Fatalf("yaml model: %+v", dy)
	}
}

func TestValidate_RejectsUndeclaredPermissionCode(t *testing.T) {
	d := &Document{APIVersion: APIVersion,
		Permissions: []Permission{{Code: "a:read", Resource: "a", Action: "read", Type: "app", Name: "A"}},
		Roles:       []Role{{Key: "r", Name: "R", PermissionCodes: []string{"b:write"}}}, // b:write 未声明
	}
	if err := Validate(d); err == nil {
		t.Fatal("expected validation error for undeclared permission code")
	}
}

func TestValidate_RejectsColonInRoleKey(t *testing.T) {
	d := &Document{APIVersion: APIVersion, Roles: []Role{{Key: "a:b", Name: "X"}}}
	if err := Validate(d); err == nil {
		t.Fatal("expected error for ':' in role key")
	}
}
