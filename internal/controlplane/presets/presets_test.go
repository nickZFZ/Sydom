package presets

import "testing"

func TestLoad_ValidatesAndExposes(t *testing.T) {
	all := All()
	if len(all) < 2 {
		t.Fatalf("want >=2 packs, got %d", len(all))
	}
	tpl, ok := Get("general-admin")
	if !ok {
		t.Fatal("general-admin not found")
	}
	if tpl.Name == "" || len(tpl.Permissions) == 0 || len(tpl.Roles) == 0 {
		t.Fatalf("general-admin incomplete: %+v", tpl)
	}
	// 每个权限点都有中文业务名（运营台无原语前提）。
	for _, p := range tpl.Permissions {
		if p.Name == "" {
			t.Errorf("permission %q missing name", p.Code)
		}
	}
}

func TestGet_Unknown(t *testing.T) {
	if _, ok := Get("nope"); ok {
		t.Error("Get(nope) should be false")
	}
}

// validate 在 Load 失败时返回 error；这里直接对内置内容跑校验确保发布内容合法。
func TestValidate_BuiltinPacksAreConsistent(t *testing.T) {
	for _, tpl := range All() {
		codes := map[string]bool{}
		for _, p := range tpl.Permissions {
			if codes[p.Code] {
				t.Errorf("%s: dup permission code %q", tpl.ID, p.Code)
			}
			codes[p.Code] = true
		}
		for _, r := range tpl.Roles {
			for _, pc := range r.PermissionCodes {
				if !codes[pc] {
					t.Errorf("%s role %s: references unknown permission code %q", tpl.ID, r.Key, pc)
				}
			}
		}
	}
}
