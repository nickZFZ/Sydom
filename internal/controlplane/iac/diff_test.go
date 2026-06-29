package iac

import (
	"encoding/json"
	"testing"
)

func TestDiff_CreateUpdateDeleteAdopt(t *testing.T) {
	desired := &Document{
		Permissions: []Permission{{Code: "order:read", Resource: "order", Action: "read", Type: "app", Name: "查看订单"}},
		Roles:       []Role{{Key: "viewer", Name: "查看员", PermissionCodes: []string{"order:read"}}},
	}
	cur := &Current{
		Permissions: []CurrentPermission{
			{Code: "order:read", Resource: "order", Action: "read", Type: "app", Name: "旧名", Source: "iac"},   // update（name 变）
			{Code: "order:write", Resource: "order", Action: "write", Type: "app", Name: "写", Source: "iac"},   // delete（文件未声明）
			{Code: "order:list", Resource: "order", Action: "list", Type: "app", Name: "列", Source: "manual"}, // 不碰
		},
		Roles: []CurrentRole{{Key: "viewer", Name: "查看员", Source: "manual", PermissionCodes: nil}}, // adopt（manual→iac）
	}
	p := Diff(desired, cur)
	if p.Count("update") != 1 {
		t.Fatalf("want 1 update, got plan %+v", p.Items)
	}
	if p.Count("delete") != 1 {
		t.Fatalf("want 1 delete(order:write), got %+v", p.Items)
	}
	if p.Count("adopt") < 1 {
		t.Fatalf("want adopt for viewer manual→iac, got %+v", p.Items)
	}
	for _, it := range p.Items {
		if it.Kind == "delete" && it.Identity == "order:list" {
			t.Fatal("manual permission order:list must never be deleted")
		}
	}
}

func TestDiff_DeleteRoleWithBindings_Conflict(t *testing.T) {
	desired := &Document{} // 文件空 → iac 实体应删
	cur := &Current{Roles: []CurrentRole{{Key: "viewer", Source: "iac", HasUserBindings: true}}}
	p := Diff(desired, cur)
	if p.Count("conflict") != 1 {
		t.Fatalf("want 1 conflict(role with bindings), got %+v", p.Items)
	}
	if p.Count("delete") != 0 {
		t.Fatalf("bound role must not be a plain delete, got %+v", p.Items)
	}
}

func mustDP(t *testing.T, js string) DataPolicy {
	t.Helper()
	var dp DataPolicy
	if err := json.Unmarshal([]byte(js), &dp); err != nil {
		t.Fatalf("mustDP: %v", err)
	}
	return dp
}

func TestDiff_DataPolicy_Lifecycle_AndManualIgnored(t *testing.T) {
	desired := &Document{DataPolicies: []DataPolicy{
		mustDP(t, `{"subject_type":"role","subject_id":"viewer","resource":"order","effect":"allow","condition":{"dept":"sales"}}`), // create
		mustDP(t, `{"subject_type":"role","subject_id":"editor","resource":"order","effect":"deny","condition":{"dept":"ops"}}`),    // update
	}}
	cur := &Current{DataPolicies: []CurrentDataPolicy{
		{SubjectType: "role", SubjectID: "editor", Resource: "order", Effect: "allow", Source: "iac", Condition: []byte(`{"dept":"hr"}`)}, // update
		{SubjectType: "role", SubjectID: "old", Resource: "order", Effect: "allow", Source: "iac", Condition: []byte(`{"a":1}`)},          // delete
		{SubjectType: "role", SubjectID: "keep", Resource: "order", Effect: "allow", Source: "manual", Condition: []byte(`{"a":1}`)},      // ignore（manual）
	}}
	p := Diff(desired, cur)
	if p.Count("create") != 1 {
		t.Fatalf("want 1 create, got %+v", p.Items)
	}
	if p.Count("update") != 1 {
		t.Fatalf("want 1 update, got %+v", p.Items)
	}
	if p.Count("delete") != 1 {
		t.Fatalf("want 1 delete, got %+v", p.Items)
	}
	for _, it := range p.Items {
		if it.Identity == "role:keep:order" {
			if it.Kind == "delete" || it.Kind == "update" || it.Kind == "adopt" {
				t.Fatalf("manual data_policy must never be touched, got %+v", it)
			}
		}
	}
}

func TestDiff_NoUpdate_WhenConditionSemanticallyEqual(t *testing.T) {
	desired := &Document{DataPolicies: []DataPolicy{
		mustDP(t, `{"subject_type":"role","subject_id":"viewer","resource":"order","effect":"allow","condition":{"a":1,"b":2}}`),
	}}
	cur := &Current{DataPolicies: []CurrentDataPolicy{
		{SubjectType: "role", SubjectID: "viewer", Resource: "order", Effect: "allow", Source: "iac", Condition: []byte(`{"b":2,"a":1}`)},
	}}
	p := Diff(desired, cur)
	if p.Count("update") != 0 {
		t.Fatalf("semantically-equal condition must not produce update, got %+v", p.Items)
	}
}

func TestDiff_NoUpdate_WhenPermissionCodesReordered(t *testing.T) {
	desired := &Document{
		Permissions: []Permission{
			{Code: "a:read", Resource: "a", Action: "read", Type: "app", Name: "A"},
			{Code: "b:read", Resource: "b", Action: "read", Type: "app", Name: "B"},
		},
		Roles: []Role{{Key: "r", Name: "R", PermissionCodes: []string{"a:read", "b:read"}}},
	}
	cur := &Current{
		Permissions: []CurrentPermission{
			{Code: "a:read", Resource: "a", Action: "read", Type: "app", Name: "A", Source: "iac"},
			{Code: "b:read", Resource: "b", Action: "read", Type: "app", Name: "B", Source: "iac"},
		},
		Roles: []CurrentRole{{Key: "r", Name: "R", Source: "iac", PermissionCodes: []string{"b:read", "a:read"}}},
	}
	p := Diff(desired, cur)
	if p.Count("update") != 0 {
		t.Fatalf("reordered permission_codes must not produce update, got %+v", p.Items)
	}
}

func TestDiff_AutoSourceNeverTouched(t *testing.T) {
	// 文件声明了某权限点，但库中它是 auto → 不采纳、不改、不删（永不触碰）。
	desired := &Document{Permissions: []Permission{{Code: "rep:read", Resource: "rep", Action: "read", Type: "app", Name: "新名"}}}
	cur := &Current{Permissions: []CurrentPermission{{Code: "rep:read", Resource: "rep", Action: "read", Type: "app", Name: "旧名", Source: "auto"}}}
	p := Diff(desired, cur)
	if len(p.Items) != 0 {
		t.Fatalf("auto-source entity declared in file must produce no plan item, got %+v", p.Items)
	}
}

func TestDiff_DataPolicy_DuplicateCurIdentity_Conflict(t *testing.T) {
	desired := &Document{DataPolicies: []DataPolicy{
		mustDP(t, `{"subject_type":"user","subject_id":"u1","resource":"order","effect":"allow","condition":{"a":1}}`),
	}}
	cur := &Current{DataPolicies: []CurrentDataPolicy{
		{SubjectType: "user", SubjectID: "u1", Resource: "order", Effect: "allow", Source: "iac", Condition: []byte(`{"a":1}`)},
		{SubjectType: "user", SubjectID: "u1", Resource: "order", Effect: "deny", Source: "iac", Condition: []byte(`{"b":2}`)},
	}}
	p := Diff(desired, cur)
	if p.Count("conflict") != 1 {
		t.Fatalf("want 1 conflict for ambiguous data_policy identity, got %+v", p.Items)
	}
	if p.Count("update")+p.Count("create")+p.Count("delete")+p.Count("adopt") != 0 {
		t.Fatalf("ambiguous identity must not produce other items, got %+v", p.Items)
	}
}

func TestDiff_DataPolicy_DuplicateManualNotInFile_Ignored(t *testing.T) {
	desired := &Document{}
	cur := &Current{DataPolicies: []CurrentDataPolicy{
		{SubjectType: "user", SubjectID: "u1", Resource: "order", Effect: "allow", Source: "manual", Condition: []byte(`{"a":1}`)},
		{SubjectType: "user", SubjectID: "u1", Resource: "order", Effect: "deny", Source: "manual", Condition: []byte(`{"b":2}`)},
	}}
	p := Diff(desired, cur)
	if len(p.Items) != 0 {
		t.Fatalf("duplicate manual data_policies not in file must be ignored (PC-3), got %+v", p.Items)
	}
}

func TestValidate_RejectsDuplicateDataPolicyIdentity(t *testing.T) {
	d, err := Parse([]byte(`{"data_policies":[{"subject_type":"user","subject_id":"u1","resource":"order","effect":"allow","condition":{"a":1}},{"subject_type":"user","subject_id":"u1","resource":"order","effect":"deny","condition":{"b":2}}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(d); err == nil {
		t.Fatal("expected error for duplicate data_policy identity in file")
	}
}

func TestDiff_RoleUpdate_OnDataScopeAndNameChange(t *testing.T) {
	desired := &Document{
		Roles: []Role{{Key: "viewer", Name: "新名", DataScopes: []DataScope{
			{Resource: "order", Effect: "allow", Condition: ConditionFromJSON([]byte(`{"dept":"sales"}`))},
		}}},
	}
	cur := &Current{
		Roles: []CurrentRole{{Key: "viewer", Name: "旧名", Source: "iac", DataScopes: []DataScope{
			{Resource: "order", Effect: "allow", Condition: ConditionFromJSON([]byte(`{"dept":"hr"}`))},
		}}},
	}
	p := Diff(desired, cur)
	if p.Count("update") != 1 {
		t.Fatalf("want 1 role update (name + data_scope changed), got %+v", p.Items)
	}
}

func TestDiff_RoleNoUpdate_OnDataScopeReorder(t *testing.T) {
	desired := &Document{Roles: []Role{{Key: "viewer", Name: "V", DataScopes: []DataScope{
		{Resource: "order", Effect: "allow", Condition: ConditionFromJSON([]byte(`{"dept":"sales"}`))},
		{Resource: "item", Effect: "deny", Condition: ConditionFromJSON([]byte(`{"x":1}`))},
	}}}}
	cur := &Current{Roles: []CurrentRole{{Key: "viewer", Name: "V", Source: "iac", DataScopes: []DataScope{
		{Resource: "item", Effect: "deny", Condition: ConditionFromJSON([]byte(`{"x":1}`))},
		{Resource: "order", Effect: "allow", Condition: ConditionFromJSON([]byte(`{"dept":"sales"}`))},
	}}}}
	p := Diff(desired, cur)
	if p.Count("update") != 0 {
		t.Fatalf("reordered data_scopes must not produce update, got %+v", p.Items)
	}
}

func TestDiff_PermissionCreate(t *testing.T) {
	desired := &Document{Permissions: []Permission{{Code: "new:read", Resource: "new", Action: "read", Type: "app", Name: "N"}}}
	cur := &Current{}
	p := Diff(desired, cur)
	if p.Count("create") != 1 {
		t.Fatalf("want 1 create, got %+v", p.Items)
	}
}
