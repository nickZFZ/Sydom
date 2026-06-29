package iac

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Current 是 app 现状的来源感知快照（由 policy 包从 DB 读出后喂入）。
type Current struct {
	Permissions  []CurrentPermission
	Roles        []CurrentRole
	DataPolicies []CurrentDataPolicy
}

type CurrentPermission struct{ Code, Resource, Action, Type, Name, Description, Source string }

type CurrentRole struct {
	Key, Name, Description, Source string
	PermissionCodes                []string
	DataScopes                     []DataScope
	HasUserBindings                bool
}

type CurrentDataPolicy struct {
	SubjectType, SubjectID, Resource, Effect, Source string
	Condition                                        []byte
}

// PlanItem 是一条收敛动作。Kind ∈ create|adopt|update|delete|conflict。
type PlanItem struct{ Kind, EntityType, Identity, Detail string }

// Plan 是 dry-run 的结构化产物。
type Plan struct{ Items []PlanItem }

func (p *Plan) Count(kind string) int {
	n := 0
	for _, it := range p.Items {
		if it.Kind == kind {
			n++
		}
	}
	return n
}

// ConditionFromJSON 用 DB 中的条件 JSON 构造规范化 Condition（供 policy 包构建 Current 快照，
// 也用于 diff 内把库侧 []byte 条件归一化后与文件侧比较）。非法 JSON 原样保留以暴露差异，不静默吞。
func ConditionFromJSON(b []byte) Condition {
	if len(bytes.TrimSpace(b)) == 0 {
		return Condition{}
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return Condition{raw: append(json.RawMessage(nil), b...)}
	}
	nb, _ := json.Marshal(v)
	return Condition{raw: nb}
}

// Diff 计算期望态（Document）与现状（Current）的收敛计划（纯函数，无 DB、无 I/O）。
// 来源治理规则（PC-3）：只治理 source=iac 实体；manual→iac 为 adopt；auto/其他永不触碰（fail-close）。
// 删除安全（PC-6）：iac 角色有用户绑定时删除降级为 conflict。
func Diff(desired *Document, cur *Current) *Plan {
	plan := &Plan{}

	// ── 权限点 ────────────────────────────────────────────────────────────────
	desiredPermMap := make(map[string]Permission, len(desired.Permissions))
	for _, p := range desired.Permissions {
		desiredPermMap[p.Code] = p
	}
	curPermMap := make(map[string]CurrentPermission, len(cur.Permissions))
	for _, cp := range cur.Permissions {
		curPermMap[cp.Code] = cp
	}

	// 处理库侧权限点
	curPermCodes := make([]string, 0, len(cur.Permissions))
	for _, cp := range cur.Permissions {
		curPermCodes = append(curPermCodes, cp.Code)
	}
	sort.Strings(curPermCodes)

	for _, code := range curPermCodes {
		cp := curPermMap[code]
		switch cp.Source {
		case "iac":
			if dp, ok := desiredPermMap[cp.Code]; ok {
				if !permFieldsEqual(cp, dp) {
					plan.Items = append(plan.Items, PlanItem{
						Kind: "update", EntityType: "permission", Identity: cp.Code,
						Detail: permUpdateDetail(cp, dp),
					})
				}
				// 字段相同 → 无项
			} else {
				plan.Items = append(plan.Items, PlanItem{
					Kind: "delete", EntityType: "permission", Identity: cp.Code,
					Detail: "删除",
				})
			}
		case "manual":
			if _, ok := desiredPermMap[cp.Code]; ok {
				plan.Items = append(plan.Items, PlanItem{
					Kind: "adopt", EntityType: "permission", Identity: cp.Code,
					Detail: "纳入 IaC 托管(manual→iac)",
				})
			}
			// 文件未声明 → 忽略（PC-3）
		default:
			// auto / 其他 → 永不触碰（fail-close，PC-3）
		}
	}

	// 文件声明、库侧不存在 → create
	desiredPermCodes := make([]string, 0, len(desired.Permissions))
	for _, dp := range desired.Permissions {
		desiredPermCodes = append(desiredPermCodes, dp.Code)
	}
	sort.Strings(desiredPermCodes)
	for _, code := range desiredPermCodes {
		if _, ok := curPermMap[code]; !ok {
			plan.Items = append(plan.Items, PlanItem{
				Kind: "create", EntityType: "permission", Identity: code,
				Detail: "新建",
			})
		}
	}

	// ── 角色 ──────────────────────────────────────────────────────────────────
	desiredRoleMap := make(map[string]Role, len(desired.Roles))
	for _, r := range desired.Roles {
		desiredRoleMap[r.Key] = r
	}
	curRoleMap := make(map[string]CurrentRole, len(cur.Roles))
	for _, cr := range cur.Roles {
		curRoleMap[cr.Key] = cr
	}

	curRoleKeys := make([]string, 0, len(cur.Roles))
	for _, cr := range cur.Roles {
		curRoleKeys = append(curRoleKeys, cr.Key)
	}
	sort.Strings(curRoleKeys)

	for _, key := range curRoleKeys {
		cr := curRoleMap[key]
		switch cr.Source {
		case "iac":
			if dr, ok := desiredRoleMap[cr.Key]; ok {
				if !roleFieldsEqual(cr, dr) {
					plan.Items = append(plan.Items, PlanItem{
						Kind: "update", EntityType: "role", Identity: cr.Key,
						Detail: "字段变更",
					})
				}
			} else {
				// 库侧 iac 角色不在文件中 → delete 或 conflict（PC-6）
				if cr.HasUserBindings {
					plan.Items = append(plan.Items, PlanItem{
						Kind: "conflict", EntityType: "role", Identity: cr.Key,
						Detail: "仍有用户绑定，需先解绑",
					})
				} else {
					plan.Items = append(plan.Items, PlanItem{
						Kind: "delete", EntityType: "role", Identity: cr.Key,
						Detail: "删除",
					})
				}
			}
		case "manual":
			if _, ok := desiredRoleMap[cr.Key]; ok {
				plan.Items = append(plan.Items, PlanItem{
					Kind: "adopt", EntityType: "role", Identity: cr.Key,
					Detail: "纳入 IaC 托管(manual→iac)",
				})
			}
			// 文件未声明 → 忽略（PC-3）
		default:
			// auto / 其他 → 永不触碰
		}
	}

	// 文件声明、库侧不存在 → create
	desiredRoleKeys := make([]string, 0, len(desired.Roles))
	for _, dr := range desired.Roles {
		desiredRoleKeys = append(desiredRoleKeys, dr.Key)
	}
	sort.Strings(desiredRoleKeys)
	for _, key := range desiredRoleKeys {
		if _, ok := curRoleMap[key]; !ok {
			dr := desiredRoleMap[key]
			plan.Items = append(plan.Items, PlanItem{
				Kind: "create", EntityType: "role", Identity: key,
				Detail: fmt.Sprintf("新建: %s", dr.Name),
			})
		}
	}

	// ── 数据策略 ──────────────────────────────────────────────────────────────
	dpID := func(subjectType, subjectID, resource string) string {
		return subjectType + ":" + subjectID + ":" + resource
	}

	desiredDPMap := make(map[string]DataPolicy, len(desired.DataPolicies))
	for _, dp := range desired.DataPolicies {
		desiredDPMap[dpID(dp.SubjectType, dp.SubjectID, dp.Resource)] = dp
	}
	curDPMap := make(map[string]CurrentDataPolicy, len(cur.DataPolicies))
	for _, cdp := range cur.DataPolicies {
		curDPMap[dpID(cdp.SubjectType, cdp.SubjectID, cdp.Resource)] = cdp
	}

	curDPKeys := make([]string, 0, len(cur.DataPolicies))
	for _, cdp := range cur.DataPolicies {
		curDPKeys = append(curDPKeys, dpID(cdp.SubjectType, cdp.SubjectID, cdp.Resource))
	}
	sort.Strings(curDPKeys)

	for _, id := range curDPKeys {
		cdp := curDPMap[id]
		switch cdp.Source {
		case "iac":
			if dp, ok := desiredDPMap[id]; ok {
				if !dpFieldsEqual(cdp, dp) {
					plan.Items = append(plan.Items, PlanItem{
						Kind: "update", EntityType: "data_policy", Identity: id,
						Detail: "condition/effect 变更",
					})
				}
			} else {
				plan.Items = append(plan.Items, PlanItem{
					Kind: "delete", EntityType: "data_policy", Identity: id,
					Detail: "删除",
				})
			}
		case "manual":
			if _, ok := desiredDPMap[id]; ok {
				plan.Items = append(plan.Items, PlanItem{
					Kind: "adopt", EntityType: "data_policy", Identity: id,
					Detail: "纳入 IaC 托管(manual→iac)",
				})
			}
			// 文件未声明 → 忽略（PC-3）
		default:
			// auto / 其他 → 永不触碰
		}
	}

	// 文件声明、库侧不存在 → create
	desiredDPKeys := make([]string, 0, len(desired.DataPolicies))
	for _, dp := range desired.DataPolicies {
		desiredDPKeys = append(desiredDPKeys, dpID(dp.SubjectType, dp.SubjectID, dp.Resource))
	}
	sort.Strings(desiredDPKeys)
	for _, id := range desiredDPKeys {
		if _, ok := curDPMap[id]; !ok {
			plan.Items = append(plan.Items, PlanItem{
				Kind: "create", EntityType: "data_policy", Identity: id,
				Detail: "新建",
			})
		}
	}

	return plan
}

// ── 比较辅助 ──────────────────────────────────────────────────────────────────

func permFieldsEqual(cp CurrentPermission, dp Permission) bool {
	return cp.Resource == dp.Resource &&
		cp.Action == dp.Action &&
		cp.Type == dp.Type &&
		cp.Name == dp.Name &&
		cp.Description == dp.Description
}

func permUpdateDetail(cp CurrentPermission, dp Permission) string {
	var parts []string
	if cp.Name != dp.Name {
		parts = append(parts, fmt.Sprintf("name: %s → %s", cp.Name, dp.Name))
	}
	if cp.Resource != dp.Resource {
		parts = append(parts, fmt.Sprintf("resource: %s → %s", cp.Resource, dp.Resource))
	}
	if cp.Action != dp.Action {
		parts = append(parts, fmt.Sprintf("action: %s → %s", cp.Action, dp.Action))
	}
	if cp.Type != dp.Type {
		parts = append(parts, fmt.Sprintf("type: %s → %s", cp.Type, dp.Type))
	}
	if cp.Description != dp.Description {
		parts = append(parts, fmt.Sprintf("description: %s → %s", cp.Description, dp.Description))
	}
	return strings.Join(parts, "; ")
}

func roleFieldsEqual(cr CurrentRole, dr Role) bool {
	if cr.Name != dr.Name || cr.Description != dr.Description {
		return false
	}
	// permission_codes：顺序无关比较
	if !strSlicesEqual(sortedCopy(cr.PermissionCodes), sortedCopy(dr.PermissionCodes)) {
		return false
	}
	// data_scopes：规范化后顺序无关比较
	crScopes := dataScopeKeys(cr.DataScopes)
	drScopes := dataScopeKeys(dr.DataScopes)
	sort.Strings(crScopes)
	sort.Strings(drScopes)
	return strSlicesEqual(crScopes, drScopes)
}

// dataScopeKeys 把每个 DataScope 规范化为可排序、可比较的字符串键。
func dataScopeKeys(scopes []DataScope) []string {
	keys := make([]string, len(scopes))
	for i, ds := range scopes {
		keys[i] = ds.Resource + "|" + ds.Effect + "|" + string(ds.Condition.JSON())
	}
	return keys
}

func dpFieldsEqual(cdp CurrentDataPolicy, dp DataPolicy) bool {
	if cdp.Effect != dp.Effect {
		return false
	}
	// 条件双侧规范化后比较，避免 key 顺序/空白误判 update
	curCond := ConditionFromJSON(cdp.Condition)
	return string(curCond.JSON()) == string(dp.Condition.JSON())
}

func sortedCopy(ss []string) []string {
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	return cp
}

func strSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
