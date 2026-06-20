package presets

import (
	"strings"
	"testing"
	"testing/fstest"
)

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

// TestLoad_RejectsCorrupt 直接对损坏内容跑 load(fs.FS)，验证 fail-close 错误路径
// （每条约束都真实拦截，而非依赖内置内容恰好合法）。
func TestLoad_RejectsCorrupt(t *testing.T) {
	cases := map[string]string{
		"bad json":        `{`,
		"empty id":        `{"id":"","permissions":[]}`,
		"empty perm code": `{"id":"a","permissions":[{"code":""}]}`,
		"dup perm code":   `{"id":"a","permissions":[{"code":"x.read"},{"code":"x.read"}]}`,
		"empty role key":  `{"id":"a","permissions":[{"code":"x.read"}],"roles":[{"key":""}]}`,
		"dup role key":    `{"id":"a","permissions":[{"code":"x.read"}],"roles":[{"key":"r"},{"key":"r"}]}`,
		"unknown perm ref": `{"id":"a","permissions":[{"code":"x.read"}],` +
			`"roles":[{"key":"r","permission_codes":["nope"]}]}`,
		// 确定性 code "tpl:a:" + 60×x = 66 字符 > VARCHAR(64)：启动期拒绝（左移失败）。
		"role code too long": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"` + strings.Repeat("x", 60) + `"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"pack.json": {Data: []byte(body)}}
			if _, err := load(fsys); err == nil {
				t.Errorf("load(%q) should fail-close with error, got nil", name)
			}
		})
	}

	// 重复 template id 跨两个文件也必须拦截。
	fsys := fstest.MapFS{
		"a.json": {Data: []byte(`{"id":"dup","permissions":[]}`)},
		"b.json": {Data: []byte(`{"id":"dup","permissions":[]}`)},
	}
	if _, err := load(fsys); err == nil {
		t.Error("duplicate template id should fail-close, got nil")
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
