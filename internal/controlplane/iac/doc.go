// Package iac 是 M4.1 策略即代码的纯函数核心：文件信封模型 + YAML/JSON 解析/序列化/校验 + 收敛 diff。
// 无 DB、无 I/O，可隔离单测。写入由 policy 包在 runVersionedWrite 事务内执行。
package iac

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

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
	// Source 是 output-only 来源标记（export 填充）：import/Diff 不消费它（采纳判定只看 DB Current 的 source）。
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

type Role struct {
	Key             string      `json:"key" yaml:"key"`
	Name            string      `json:"name" yaml:"name"`
	Description     string      `json:"description,omitempty" yaml:"description,omitempty"`
	PermissionCodes []string    `json:"permission_codes" yaml:"permission_codes"`
	DataScopes      []DataScope `json:"data_scopes,omitempty" yaml:"data_scopes,omitempty"`
	// Source 同 Permission.Source：output-only，import/Diff 不消费。
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

type DataScope struct {
	Resource  string    `json:"resource" yaml:"resource"`
	Effect    string    `json:"effect" yaml:"effect"`
	Condition Condition `json:"condition" yaml:"condition"`
}

type DataPolicy struct {
	SubjectType string    `json:"subject_type" yaml:"subject_type"`
	SubjectID   string    `json:"subject_id" yaml:"subject_id"`
	Resource    string    `json:"resource" yaml:"resource"`
	Effect      string    `json:"effect" yaml:"effect"`
	Condition   Condition `json:"condition" yaml:"condition"`
	// Source 同 Permission.Source：output-only，import/Diff 不消费。
	Source string `json:"source,omitempty" yaml:"source,omitempty"`
}

// Condition 是数据条件的桥接类型：内部恒以规范化 JSON 字节存储，
// 同时支持 JSON 与 YAML 双向编解码——确保 YAML 与 JSON 两种格式解析后
// 得到完全一致的规范条件（key 排序、无多余空白），从而保证双格式 parity
// 与 diff 比较的稳定性。零值表示「无条件」。
type Condition struct {
	raw json.RawMessage // 规范化后的紧凑 JSON
}

// JSON 返回规范化的条件 JSON 字节（供 diff 比较与写入消费）。零值时返回 nil。
func (c Condition) JSON() json.RawMessage { return c.raw }

func (c Condition) MarshalJSON() ([]byte, error) {
	if len(c.raw) == 0 {
		return []byte("null"), nil
	}
	return c.raw, nil
}

func (c *Condition) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	nb, err := json.Marshal(v) // 归一化：排序 key、去空白
	if err != nil {
		return err
	}
	c.raw = nb
	return nil
}

func (c Condition) MarshalYAML() (any, error) {
	if len(c.raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(c.raw, &v); err != nil {
		return nil, err
	}
	return v, nil // 让 yaml 以原生 map/标量 输出，人类可读
}

func (c *Condition) UnmarshalYAML(value *yaml.Node) error {
	var v any
	if err := value.Decode(&v); err != nil {
		return err
	}
	nb, err := json.Marshal(v) // YAML 节点 → 规范 JSON 字节
	if err != nil {
		return err
	}
	c.raw = nb
	return nil
}
