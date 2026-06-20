// Package presets 提供司域官方预设包（//go:embed 内嵌、随二进制版本化、租户不可改）。
// 启动期严格校验：包 id 唯一、包内 permission.code 唯一、role.permission_codes 引用存在；
// 任一违例 panic（fail-close，绝不带损坏内容运行）。
package presets

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed *.json
var files embed.FS

// Permission 是预设包中的一条权限点。
type Permission struct {
	Code        string `json:"code"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Role 是预设包中的一个业务角色（key 用于确定性 code，permission_codes 引用本包权限点）。
type Role struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	PermissionCodes []string `json:"permission_codes"`
	// data_scopes / onboarding 预留：本片不解析（M3.2c/M3.4），未知字段被 json 忽略。
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
	ts, err := load()
	if err != nil {
		panic("presets: " + err.Error()) // fail-close：损坏内容拒绝启动
	}
	loaded = ts
	byID = map[string]Template{}
	for _, t := range ts {
		byID[t.ID] = t
	}
}

func load() ([]Template, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []Template
	seenID := map[string]bool{}
	for _, e := range entries {
		b, err := files.ReadFile(e.Name())
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
		for _, r := range t.Roles {
			for _, pc := range r.PermissionCodes {
				if !codes[pc] {
					return nil, fmt.Errorf("%s role %q: unknown permission code %q", t.ID, r.Key, pc)
				}
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// All 返回全部内置预设包（按 id 排序、稳定）。
func All() []Template { return loaded }

// Get 按 id 取预设包。
func Get(id string) (Template, bool) { t, ok := byID[id]; return t, ok }
