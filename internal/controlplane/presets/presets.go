// Package presets 提供司域官方预设包（//go:embed 内嵌、随二进制版本化、租户不可改）。
// 启动期严格校验：包 id 唯一、包内 permission.code 唯一、role.permission_codes 引用存在；
// 任一违例 panic（fail-close，绝不带损坏内容运行）。
package presets

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed *.json
var files embed.FS

// maxRoleCodeLen 是 role.code 列宽（VARCHAR(64)，见 db/migrations/000003_role.up.sql）。
// 模板应用时 policy 层用确定性 code `tpl:<templateID>:<key>`（见 policy.ApplyTemplate）；
// loader 在启动期校验该 code 不超列宽，把「超长 code 致 apply 期 DB 插入失败」左移到启动/测试期
// （fail-close 优先于运行期才暴露）。
const maxRoleCodeLen = 64

// templateRoleCode 复刻 policy.ApplyTemplate 形成的确定性角色 code，仅供 loader 长度校验。
func templateRoleCode(templateID, key string) string { return "tpl:" + templateID + ":" + key }

// Permission 是预设包中的一条权限点。
type Permission struct {
	Code        string `json:"code"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DataScope 是预设角色的一条符号化数据范围（condition 为既有条件树 JSON，$user.xxx 符号保留，原样透传）。
type DataScope struct {
	Resource  string          `json:"resource"`
	Effect    string          `json:"effect"` // 空串按 allow
	Condition json.RawMessage `json:"condition"`
}

// Role 是预设包中的一个业务角色（key 用于确定性 code，permission_codes 引用本包权限点）。
type Role struct {
	Key             string      `json:"key"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	PermissionCodes []string    `json:"permission_codes"`
	DataScopes      []DataScope `json:"data_scopes"`
	// onboarding 预留：本片不解析（M3.4），未知字段被 json 忽略。
}

// Template 是一个预设包。
type Template struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Version     uint32       `json:"version"`
	Permissions []Permission `json:"permissions"`
	Roles       []Role       `json:"roles"`
}

var loaded []Template
var byID map[string]Template

func init() {
	ts, err := load(files)
	if err != nil {
		panic("presets: " + err.Error()) // fail-close：损坏内容拒绝启动
	}
	loaded = ts
	byID = map[string]Template{}
	for _, t := range ts {
		byID[t.ID] = t
	}
}

// load 读取并校验 fsys 根目录下的全部 *.json 预设包（取 fs.FS 参数以便用 fstest.MapFS
// 注入损坏内容测试 fail-close 错误路径）。校验：id 非空且唯一、permission.code 非空且
// 唯一、role.key 非空且唯一、role 确定性 code 不超列宽、role.permission_codes 引用存在于
// 本包。任一违例返回 error。
func load(fsys fs.FS) ([]Template, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Template
	seenID := map[string]bool{}
	for _, e := range entries {
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		var t Template
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("%s: empty id", e.Name())
		}
		if seenID[t.ID] {
			return nil, fmt.Errorf("duplicate template id %q", t.ID)
		}
		seenID[t.ID] = true
		codes := map[string]bool{}
		for _, p := range t.Permissions {
			if p.Code == "" {
				return nil, fmt.Errorf("%s: empty permission code", t.ID)
			}
			if codes[p.Code] {
				return nil, fmt.Errorf("%s: duplicate permission code %q", t.ID, p.Code)
			}
			codes[p.Code] = true
		}
		seenKey := map[string]bool{}
		for _, r := range t.Roles {
			if r.Key == "" {
				return nil, fmt.Errorf("%s: empty role key", t.ID)
			}
			if seenKey[r.Key] {
				return nil, fmt.Errorf("%s: duplicate role key %q", t.ID, r.Key)
			}
			seenKey[r.Key] = true
			if c := templateRoleCode(t.ID, r.Key); len(c) > maxRoleCodeLen {
				return nil, fmt.Errorf("%s role %q: deterministic code %q exceeds %d chars", t.ID, r.Key, c, maxRoleCodeLen)
			}
			for _, pc := range r.PermissionCodes {
				if !codes[pc] {
					return nil, fmt.Errorf("%s role %q: unknown permission code %q", t.ID, r.Key, pc)
				}
			}
			for _, ds := range r.DataScopes {
				if ds.Resource == "" {
					return nil, fmt.Errorf("%s role %q: empty data_scope resource", t.ID, r.Key)
				}
				if len(ds.Condition) == 0 || !json.Valid(ds.Condition) {
					return nil, fmt.Errorf("%s role %q: data_scope condition not valid json", t.ID, r.Key)
				}
				if ds.Effect != "" && ds.Effect != "allow" && ds.Effect != "deny" {
					return nil, fmt.Errorf("%s role %q: bad data_scope effect %q", t.ID, r.Key, ds.Effect)
				}
			}
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// All 返回全部内置预设包（按 id 排序、稳定）。
func All() []Template { return loaded }

// Get 按 id 取预设包。
func Get(id string) (Template, bool) { t, ok := byID[id]; return t, ok }
