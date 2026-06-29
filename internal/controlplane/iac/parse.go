package iac

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse 自动识别 JSON / YAML（首个非空白字符 '{' 或 '[' → JSON，否则 YAML）→ Document。
func Parse(content []byte) (*Document, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("iac: empty document")
	}
	var d Document
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &d); err != nil {
			return nil, fmt.Errorf("iac: json parse: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(trimmed, &d); err != nil {
			return nil, fmt.Errorf("iac: yaml parse: %w", err)
		}
	}
	return &d, nil
}

// Serialize 把 Document 序列化为 yaml 或 json。
func Serialize(d *Document, format string) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(d, "", "  ")
	case "yaml", "":
		return yaml.Marshal(d)
	default:
		return nil, fmt.Errorf("iac: unknown format %q", format)
	}
}

// Validate 做引用完整性 + 唯一性 + 合法性校验（fail-close）。
func Validate(d *Document) error {
	if d.APIVersion != "" && d.APIVersion != APIVersion {
		return fmt.Errorf("iac: unsupported apiVersion %q", d.APIVersion)
	}
	permCodes := map[string]bool{}
	for _, p := range d.Permissions {
		if strings.TrimSpace(p.Code) == "" {
			return fmt.Errorf("iac: permission code empty")
		}
		if permCodes[p.Code] {
			return fmt.Errorf("iac: duplicate permission code %q", p.Code)
		}
		permCodes[p.Code] = true
	}
	roleKeys := map[string]bool{}
	for _, r := range d.Roles {
		if r.Key == "" {
			return fmt.Errorf("iac: role key empty")
		}
		if strings.ContainsRune(r.Key, ':') {
			return fmt.Errorf("iac: role key %q must not contain ':'", r.Key)
		}
		if roleKeys[r.Key] {
			return fmt.Errorf("iac: duplicate role key %q", r.Key)
		}
		roleKeys[r.Key] = true
		for _, pc := range r.PermissionCodes {
			if !permCodes[pc] {
				return fmt.Errorf("iac: role %q references undeclared permission code %q", r.Key, pc)
			}
		}
		for _, ds := range r.DataScopes {
			if err := validCondition(ds.Condition); err != nil {
				return fmt.Errorf("iac: role %q data_scope: %w", r.Key, err)
			}
			if err := validEffect(ds.Effect); err != nil {
				return err
			}
		}
	}
	for _, dp := range d.DataPolicies {
		if err := validCondition(dp.Condition); err != nil {
			return fmt.Errorf("iac: data_policy %s/%s: %w", dp.SubjectID, dp.Resource, err)
		}
		if err := validEffect(dp.Effect); err != nil {
			return err
		}
	}
	return nil
}

func validCondition(c json.RawMessage) error {
	if len(bytes.TrimSpace(c)) == 0 {
		return fmt.Errorf("condition empty")
	}
	if !json.Valid(c) {
		return fmt.Errorf("condition not valid json")
	}
	return nil
}

func validEffect(e string) error {
	if e != "" && e != "allow" && e != "deny" {
		return fmt.Errorf("iac: invalid effect %q", e)
	}
	return nil
}
