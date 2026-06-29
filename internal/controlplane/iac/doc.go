// Package iac 是 M4.1 策略即代码的纯函数核心：文件信封模型 + YAML/JSON 解析/序列化/校验 + 收敛 diff。
// 无 DB、无 I/O，可隔离单测。写入由 policy 包在 runVersionedWrite 事务内执行。
package iac

import "encoding/json"

const APIVersion = "sydom.policy/v1"

// Document 是策略即代码文件的信封 + 期望态模型。
type Document struct {
	APIVersion   string       `json:"apiVersion" yaml:"apiVersion"`
	App          *AppRef      `json:"app,omitempty" yaml:"app,omitempty"`
	Permissions  []Permission `json:"permissions" yaml:"permissions"`
	Roles        []Role       `json:"roles" yaml:"roles"`
	DataPolicies []DataPolicy `json:"data_policies,omitempty" yaml:"data_policies,omitempty"`
}

type AppRef struct {
	Key string `json:"key,omitempty" yaml:"key,omitempty"`
}

type Permission struct {
	Code        string `json:"code" yaml:"code"`
	Resource    string `json:"resource" yaml:"resource"`
	Action      string `json:"action" yaml:"action"`
	Type        string `json:"type" yaml:"type"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Role struct {
	Key             string      `json:"key" yaml:"key"`
	Name            string      `json:"name" yaml:"name"`
	Description     string      `json:"description,omitempty" yaml:"description,omitempty"`
	PermissionCodes []string    `json:"permission_codes" yaml:"permission_codes"`
	DataScopes      []DataScope `json:"data_scopes,omitempty" yaml:"data_scopes,omitempty"`
}

type DataScope struct {
	Resource  string          `json:"resource" yaml:"resource"`
	Effect    string          `json:"effect" yaml:"effect"`
	Condition json.RawMessage `json:"condition" yaml:"condition"`
}

type DataPolicy struct {
	SubjectType string          `json:"subject_type" yaml:"subject_type"`
	SubjectID   string          `json:"subject_id" yaml:"subject_id"`
	Resource    string          `json:"resource" yaml:"resource"`
	Effect      string          `json:"effect" yaml:"effect"`
	Condition   json.RawMessage `json:"condition" yaml:"condition"`
}
