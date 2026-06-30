package iac

import (
	"strings"
	"testing"
)

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

func TestParse_ConditionParity_YAMLEqualsJSON(t *testing.T) {
	js := `{"apiVersion":"sydom.policy/v1","data_policies":[{"subject_type":"user","subject_id":"u1","resource":"order","effect":"allow","condition":{"dept":"sales","level":3}}]}`
	ya := "apiVersion: sydom.policy/v1\ndata_policies:\n  - subject_type: user\n    subject_id: u1\n    resource: order\n    effect: allow\n    condition:\n      dept: sales\n      level: 3\n"
	dj, err := Parse([]byte(js))
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	dy, err := Parse([]byte(ya))
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	cj := string(dj.DataPolicies[0].Condition.JSON())
	cy := string(dy.DataPolicies[0].Condition.JSON())
	if cj != cy {
		t.Fatalf("condition parity broken: json=%q yaml=%q", cj, cy)
	}
	if cj != `{"dept":"sales","level":3}` {
		t.Fatalf("unexpected canonical condition: %q", cj)
	}
}

func TestSerialize_YAMLConditionIsReadableAndRoundTrips(t *testing.T) {
	d, err := Parse([]byte(`{"data_policies":[{"subject_type":"user","subject_id":"u1","resource":"order","effect":"allow","condition":{"dept":"sales"}}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Serialize(d, "yaml")
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if !strings.Contains(string(out), "dept: sales") {
		t.Fatalf("yaml condition not a readable map:\n%s", out)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if string(back.DataPolicies[0].Condition.JSON()) != string(d.DataPolicies[0].Condition.JSON()) {
		t.Fatalf("round-trip mismatch: %q vs %q", back.DataPolicies[0].Condition.JSON(), d.DataPolicies[0].Condition.JSON())
	}
}

func TestValidate_RejectsNullCondition(t *testing.T) {
	d, err := Parse([]byte(`{"data_policies":[{"subject_type":"user","subject_id":"u1","resource":"order","effect":"allow","condition":null}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(d); err == nil {
		t.Fatal("expected error for null condition")
	}
}

func TestValidate_RejectsEmptyEffect(t *testing.T) {
	d, err := Parse([]byte(`{"data_policies":[{"subject_type":"user","subject_id":"u1","resource":"order","condition":{"a":1}}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(d); err == nil {
		t.Fatal("expected error for empty effect")
	}
}

func TestValidate_RejectsMissingRequiredFields(t *testing.T) {
	d := &Document{APIVersion: APIVersion, Permissions: []Permission{{Code: "a:read", Action: "read", Type: "app", Name: "A"}}} // 缺 Resource
	if err := Validate(d); err == nil {
		t.Fatal("expected error for permission missing resource")
	}
}
