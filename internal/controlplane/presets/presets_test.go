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
		"empty data_scope resource": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"","condition":{"field":"x","op":"EQ","value":"1"}}]}]}`,
		// condition 值为裸字符串 not-json（不带引号），JSON 解析时 json.RawMessage 会接收到
		// 字面量 not-json，json.Valid 返回 false，loader 拒绝。
		// 注：原计划写 "not-json"（含引号），那是合法 JSON 字符串值，json.Valid=true 不会被拒；
		// 此处修正为真正非法的 JSON 字节序列（DSC-3：校验合法 JSON，不解析语义）。
		"invalid data_scope condition json": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"order","condition":not-json}]}]}`,
		"bad data_scope effect": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"order","effect":"maybe","condition":{"field":"x","op":"EQ","value":"1"}}]}]}`,
		// condition:null 是合法 JSON(模板解析通过)，但非真实条件树——由 loader 的显式 guard 拒绝。
		// 此用例对 loader 校验有齿(区别于裸 not-json 走外层解析失败路径)。
		"null data_scope condition": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"order","condition":null}]}]}`,
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

func TestLoad_ParsesDataScopes(t *testing.T) {
	// 正向覆盖两个官方包：general-admin(editor) + ecommerce-ops(customer-service) 各 >=1 示意。
	var total int
	for _, tpl := range All() {
		for _, r := range tpl.Roles {
			for _, ds := range r.DataScopes {
				if ds.Resource == "" {
					t.Errorf("%s role %s data_scope missing resource", tpl.ID, r.Key)
				}
				if len(ds.Condition) == 0 {
					t.Errorf("%s role %s data_scope missing condition", tpl.ID, r.Key)
				}
				total++
			}
		}
	}
	if total < 2 {
		t.Errorf("两个官方包应各发布 >=1 示意 data_scope，实得 %d", total)
	}
}

// TestOnboarding_Curation 断言两个官方包都携带完整 onboarding 策展元数据。
// 显式断言 general-admin 与 ecommerce-ops 的 Onboarding 都非 nil，防止任一包 onboarding
// 字段拼写错误被 fail-soft 静默吞掉；并对所有带 onboarding 的包断言字段完整。
func TestOnboarding_Curation(t *testing.T) {
	for _, id := range []string{"general-admin", "ecommerce-ops"} {
		tpl, ok := Get(id)
		if !ok {
			t.Fatalf("%s not found", id)
		}
		if tpl.Onboarding == nil {
			t.Fatalf("%s.Onboarding should not be nil", id)
		}
	}
	// 对所有携带 onboarding 的包断言字段完整（recommended/intro/next_steps）。
	for _, tpl := range All() {
		if tpl.Onboarding == nil {
			continue
		}
		if !tpl.Onboarding.Recommended {
			t.Errorf("%s.Onboarding.Recommended should be true", tpl.ID)
		}
		if tpl.Onboarding.Intro == "" {
			t.Errorf("%s.Onboarding.Intro should not be empty", tpl.ID)
		}
		if len(tpl.Onboarding.NextSteps) == 0 {
			t.Errorf("%s.Onboarding.NextSteps should not be empty", tpl.ID)
		}
	}
}

// TestOnboarding_AbsentIsNilNotError 验证 loader 对无 onboarding 字段的包不报错（fail-soft，向后兼容）。
func TestOnboarding_AbsentIsNilNotError(t *testing.T) {
	body := `{"id":"x","name":"X","version":1,"permissions":[{"code":"a.read","resource":"a","action":"read","type":"act","name":"看"}],"roles":[{"key":"r","name":"R","permission_codes":["a.read"]}]}`
	fsys := fstest.MapFS{"pack.json": {Data: []byte(body)}}
	ts, err := load(fsys)
	if err != nil {
		t.Fatalf("load should not error for pack without onboarding, got: %v", err)
	}
	if len(ts) == 0 {
		t.Fatal("expected 1 template, got 0")
	}
	if ts[0].Onboarding != nil {
		t.Errorf("Onboarding should be nil when absent from JSON, got %+v", ts[0].Onboarding)
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
