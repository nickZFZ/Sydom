package policy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/iac"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// M4.1 策略即代码（Policy-as-Code）导入导出。
//
// 角色 key↔code 映射（镜像模板 tpl: 范式）：iac 角色 DB code 用命名空间 "iac:<key>"。
//   - 由 code 派生 key：key = TrimPrefix(code, "iac:")。
//   - 新建 iac 角色：code = "iac:" + key。
//   - 「IaC 可表达」角色 = 派生 key 非空且不含 ':'。不可表达角色（如 tpl:... 模板角色，
//     派生 key 仍含 ':'）：export 跳过、Current 跳过（它们不受 IaC 治理，import 本就忽略）。
const (
	iacRolePrefix = "iac:"
	iacSource     = "iac"
)

// roleRef 是 import apply 时按 key 解析出的角色定位（DB roleID + 当前 code）。
type roleRef struct {
	roleID int64
	code   string
}

func iacRoleKey(code string) string { return strings.TrimPrefix(code, iacRolePrefix) }

func isExpressibleRoleKey(key string) bool { return key != "" && !strings.Contains(key, ":") }

func dpIdentity(subjectType, subjectID, resource string) string {
	return subjectType + ":" + subjectID + ":" + resource
}

// ExportAppPolicy 把 app 的 IaC 可治理态导出为 YAML/JSON 策略文件（纯读，来源感知组装）。
// 权限点全量导出（code 可含 ':'，原样）；角色仅导出 IaC 可表达者（key=TrimPrefix(code,"iac:")）；
// 角色数据范围来自 subject_type='role' 且 subject_id=角色 code 的数据策略；顶层 DataPolicies
// 为 subject_type!='role' 者。Source 由 export 填充（output-only）。
// 输出顺序由 store.ListApp* 的稳定排序保证，故同一库态导出幂等。
func (m *PolicyManager) ExportAppPolicy(ctx context.Context, appID int64, format string) (string, error) {
	perms, err := store.ListAppPermissionsWithSource(ctx, m.db, appID)
	if err != nil {
		return "", fmt.Errorf("policy: export list permissions app %d: %w", appID, err)
	}
	roles, err := store.ListAppRolesWithSource(ctx, m.db, appID)
	if err != nil {
		return "", fmt.Errorf("policy: export list roles app %d: %w", appID, err)
	}
	dps, err := store.ListAppDataPoliciesWithSource(ctx, m.db, appID)
	if err != nil {
		return "", fmt.Errorf("policy: export list data policies app %d: %w", appID, err)
	}

	roleDPsByCode, topDPs := splitDataPolicies(dps)

	doc := &iac.Document{APIVersion: iac.APIVersion}
	for _, p := range perms {
		doc.Permissions = append(doc.Permissions, iac.Permission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type,
			Name: p.Name, Description: p.Description, Source: p.Source,
		})
	}
	for _, r := range roles {
		key := iacRoleKey(r.Code)
		if !isExpressibleRoleKey(key) {
			continue // 不可表达（如 tpl: 模板角色）→ 不受 IaC 治理，跳过
		}
		codes, err := store.RolePermissionCodes(ctx, m.db, appID, r.ID)
		if err != nil {
			return "", fmt.Errorf("policy: export role %d permissions: %w", r.ID, err)
		}
		doc.Roles = append(doc.Roles, iac.Role{
			Key: key, Name: r.Name, Description: r.Description,
			PermissionCodes: codes, DataScopes: dataScopesFromRows(roleDPsByCode[r.Code]), Source: r.Source,
		})
	}
	for _, dp := range topDPs {
		doc.DataPolicies = append(doc.DataPolicies, iac.DataPolicy{
			SubjectType: dp.SubjectType, SubjectID: dp.SubjectID, Resource: dp.Resource,
			Effect: dp.Effect, Condition: iac.ConditionFromJSON([]byte(dp.Condition)), Source: dp.Source,
		})
	}

	b, err := iac.Serialize(doc, format)
	if err != nil {
		return "", fmt.Errorf("policy: export serialize: %w", err)
	}
	return string(b), nil
}

// ImportAppPolicy 把 IaC 策略文件收敛到 app。dryRun=true 纯读返回计划 + 当前版本（零副作用）；
// dryRun=false 原子收敛：含任何 conflict 项即整笔拒绝；否则单个 runVersionedWrite 内按 FK 安全序
// 「先清后设」对齐到文件态。返回 (plan, version, delta, err)；无投影且无数据变更时 delta=nil、
// 版本未 bump（往返幂等场景）。
func (m *PolicyManager) ImportAppPolicy(ctx context.Context, appID int64, content []byte, dryRun bool) (*iac.Plan, int64, *cp.Delta, error) {
	doc, err := iac.Parse(content)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("policy: import parse: %w", err)
	}
	if err := iac.Validate(doc); err != nil {
		return nil, 0, nil, fmt.Errorf("policy: import validate: %w", err)
	}

	cur, err := m.snapshotCurrent(ctx, m.db, appID)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("policy: import snapshot app %d: %w", appID, err)
	}
	plan := iac.Diff(doc, cur)

	if dryRun {
		ver, err := store.LockAppVersion(ctx, m.db, appID) // 纯读：autocommit 下 FOR UPDATE 即取即放
		if err != nil {
			return nil, 0, nil, fmt.Errorf("policy: import read version app %d: %w", appID, err)
		}
		return plan, ver, nil, nil
	}

	if n := plan.Count("conflict"); n > 0 {
		return plan, 0, nil, fmt.Errorf("policy: import has %d unresolved conflict(s)", n)
	}

	ctx = cp.WithOperator(ctx, "iac-import") // bump 路径的 audit actor
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "import_policy", entityType: "app", entityID: fmt.Sprint(appID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return m.applyImportPlan(ctx, tx, appID, doc, plan)
		},
	})
	if err != nil {
		return plan, 0, nil, err
	}
	if d == nil {
		// 无投影变化且无数据变更 → runVersionedWrite 未 bump；回当前版本、delta=nil（幂等收敛）。
		ver, e := store.LockAppVersion(ctx, m.db, appID)
		if e != nil {
			return plan, 0, nil, fmt.Errorf("policy: import read version app %d: %w", appID, e)
		}
		return plan, ver, nil, nil
	}
	return plan, d.Version, d, nil
}

// snapshotCurrent 用来源感知 store 读组装 iac.Current（只读）。角色按 key 收敛，
// 两个可表达角色派生同一 key 即身份冲突 fail-close。
func (m *PolicyManager) snapshotCurrent(ctx context.Context, ex cp.DBTX, appID int64) (*iac.Current, error) {
	perms, err := store.ListAppPermissionsWithSource(ctx, ex, appID)
	if err != nil {
		return nil, err
	}
	roles, err := store.ListAppRolesWithSource(ctx, ex, appID)
	if err != nil {
		return nil, err
	}
	dps, err := store.ListAppDataPoliciesWithSource(ctx, ex, appID)
	if err != nil {
		return nil, err
	}
	roleDPsByCode, topDPs := splitDataPolicies(dps)

	cur := &iac.Current{}
	for _, p := range perms {
		cur.Permissions = append(cur.Permissions, iac.CurrentPermission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type,
			Name: p.Name, Description: p.Description, Source: p.Source,
		})
	}
	seenKey := map[string]bool{}
	for _, r := range roles {
		key := iacRoleKey(r.Code)
		if !isExpressibleRoleKey(key) {
			continue
		}
		if seenKey[key] {
			return nil, fmt.Errorf("policy: role key %q derived from multiple role codes (identity collision)", key)
		}
		seenKey[key] = true
		codes, err := store.RolePermissionCodes(ctx, ex, appID, r.ID)
		if err != nil {
			return nil, err
		}
		hasBind, err := store.RoleHasUserBindings(ctx, ex, appID, r.ID)
		if err != nil {
			return nil, err
		}
		cur.Roles = append(cur.Roles, iac.CurrentRole{
			Key: key, Name: r.Name, Description: r.Description, Source: r.Source,
			PermissionCodes: codes, DataScopes: dataScopesFromRows(roleDPsByCode[r.Code]),
			HasUserBindings: hasBind,
		})
	}
	for _, dp := range topDPs {
		cur.DataPolicies = append(cur.DataPolicies, iac.CurrentDataPolicy{
			SubjectType: dp.SubjectType, SubjectID: dp.SubjectID, Resource: dp.Resource,
			Effect: dp.Effect, Source: dp.Source, Condition: []byte(dp.Condition),
		})
	}
	return cur, nil
}

// applyImportPlan 在写事务内按 FK 安全序执行收敛计划，返回 data_policy 变更供 runVersionedWrite
// 进 audit/广播。事务内重读快照得新鲜定位映射（post-lock，一致），按 pre-tx 计划项执行：
// 计划项指向的实体若已不存在则 fail-close。
func (m *PolicyManager) applyImportPlan(ctx context.Context, tx *sql.Tx, appID int64, doc *iac.Document, plan *iac.Plan) ([]cp.DataPolicyChange, error) {
	roles, err := store.ListAppRolesWithSource(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	dps, err := store.ListAppDataPoliciesWithSource(ctx, tx, appID)
	if err != nil {
		return nil, err
	}

	roleByKey := map[string]roleRef{}
	for _, r := range roles {
		key := iacRoleKey(r.Code)
		if !isExpressibleRoleKey(key) {
			continue
		}
		if _, dup := roleByKey[key]; dup {
			return nil, fmt.Errorf("policy: import: role key %q maps to multiple roles", key)
		}
		roleByKey[key] = roleRef{roleID: r.ID, code: r.Code}
	}
	// subject_type='role' 的数据策略按角色 code 分组（供角色数据范围先清后设）；
	// 顶层（非 role 主体）按身份建索引（供 update/adopt/delete 定位 id）。唯一身份才会被
	// 计划触碰（不唯一身份已被 Diff 拦为 conflict、apply 前已拒），首条占位即安全。
	roleDPsByCode := map[string][]store.DataPolicyWithSource{}
	topDPByIdentity := map[string]store.DataPolicyWithSource{}
	for _, dp := range dps {
		if dp.SubjectType == "role" {
			roleDPsByCode[dp.SubjectID] = append(roleDPsByCode[dp.SubjectID], dp)
			continue
		}
		id := dpIdentity(dp.SubjectType, dp.SubjectID, dp.Resource)
		if _, ok := topDPByIdentity[id]; !ok {
			topDPByIdentity[id] = dp
		}
	}

	desiredPermByCode := map[string]iac.Permission{}
	for _, p := range doc.Permissions {
		desiredPermByCode[p.Code] = p
	}
	desiredRoleByKey := map[string]iac.Role{}
	for _, r := range doc.Roles {
		desiredRoleByKey[r.Key] = r
	}
	desiredDPByIdentity := map[string]iac.DataPolicy{}
	for _, dp := range doc.DataPolicies {
		desiredDPByIdentity[dpIdentity(dp.SubjectType, dp.SubjectID, dp.Resource)] = dp
	}

	var dataChanges []cp.DataPolicyChange
	var vNew int64
	getVNew := func() (int64, error) {
		if vNew == 0 {
			cur, e := store.LockAppVersion(ctx, tx, appID) // 同 tx 已持锁，返回当前版本
			if e != nil {
				return 0, e
			}
			vNew = cur + 1
		}
		return vNew, nil
	}

	// ── Phase A：删除（先删，释放引用）─────────────────────────────────────────
	// A1. 角色删除（连同其数据范围）。带绑定的 iac 角色已被 conflict 拦在 apply 前，不会到这。
	for _, it := range plan.Items {
		if it.EntityType != "role" || it.Kind != "delete" {
			continue
		}
		rr, ok := roleByKey[it.Identity]
		if !ok {
			return nil, fmt.Errorf("policy: import: role %q to delete no longer present", it.Identity)
		}
		for _, dp := range roleDPsByCode[rr.code] {
			if e := store.DeleteDataPolicy(ctx, tx, appID, dp.ID); e != nil {
				return nil, e
			}
			dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: dp.ID}})
		}
		if e := store.DeleteRole(ctx, tx, appID, rr.roleID); e != nil { // 级联删 role_permission/inheritance/binding
			return nil, e
		}
	}
	// A2. 顶层数据策略删除。
	for _, it := range plan.Items {
		if it.EntityType != "data_policy" || it.Kind != "delete" {
			continue
		}
		dp, ok := topDPByIdentity[it.Identity]
		if !ok {
			return nil, fmt.Errorf("policy: import: data_policy %q to delete no longer present", it.Identity)
		}
		if e := store.DeleteDataPolicy(ctx, tx, appID, dp.ID); e != nil {
			return nil, e
		}
		dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: dp.ID}})
	}

	// ── Phase B：建/改/采纳 ─────────────────────────────────────────────────────
	// B1. 权限点 create/update（upsert source=iac）；adopt（manual→iac，仅翻 source）。
	for _, it := range plan.Items {
		if it.EntityType != "permission" {
			continue
		}
		switch it.Kind {
		case "create", "update":
			p := desiredPermByCode[it.Identity]
			if _, e := store.UpsertPermissionWithSource(ctx, tx, appID,
				p.Code, p.Resource, p.Action, p.Type, p.Name, p.Description, iacSource); e != nil {
				return nil, e
			}
		case "adopt":
			if e := store.AdoptPermissionSource(ctx, tx, appID, it.Identity); e != nil {
				return nil, e
			}
		}
	}

	// B2. 角色 create/adopt/update：一律「先清后设」授权与数据范围对齐文件态。
	for _, it := range plan.Items {
		if it.EntityType != "role" || it.Kind == "delete" || it.Kind == "conflict" {
			continue
		}
		dr := desiredRoleByKey[it.Identity]
		var (
			roleID int64
			code   string
		)
		switch it.Kind {
		case "create":
			code = iacRolePrefix + it.Identity
			id, e := store.InsertRoleWithSource(ctx, tx, appID, code, dr.Name, iacSource)
			if e != nil {
				return nil, e
			}
			roleID = id
		case "adopt":
			rr, ok := roleByKey[it.Identity]
			if !ok {
				return nil, fmt.Errorf("policy: import: role %q to adopt no longer present", it.Identity)
			}
			roleID, code = rr.roleID, rr.code
			if e := store.AdoptRoleSource(ctx, tx, appID, roleID); e != nil { // manual→iac
				return nil, e
			}
		case "update":
			rr, ok := roleByKey[it.Identity]
			if !ok {
				return nil, fmt.Errorf("policy: import: role %q to update no longer present", it.Identity)
			}
			roleID, code = rr.roleID, rr.code
		}
		// 元数据对齐（name + description）。
		if e := store.UpdateRoleMeta(ctx, tx, appID, roleID, dr.Name, dr.Description); e != nil {
			return nil, e
		}
		// 授权先清后设（role_permission 变化由 runVersionedWrite 重投影自动转 casbin_rule）。
		if e := m.resetRolePermissions(ctx, tx, appID, roleID, dr.PermissionCodes); e != nil {
			return nil, e
		}
		// 数据范围先清后设。
		for _, dp := range roleDPsByCode[code] {
			if e := store.DeleteDataPolicy(ctx, tx, appID, dp.ID); e != nil {
				return nil, e
			}
			dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: dp.ID}})
		}
		for _, ds := range dr.DataScopes {
			v, e := getVNew()
			if e != nil {
				return nil, e
			}
			p := cp.DataPolicy{
				SubjectType: "role", SubjectID: code, Resource: ds.Resource,
				Condition: string(ds.Condition.JSON()), Effect: normEffect(ds.Effect),
			}
			id, _, e := store.UpsertDataPolicyWithSource(ctx, tx, appID, p, iacSource, v)
			if e != nil {
				return nil, e
			}
			p.ID = id
			dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeAdd, Policy: p})
		}
	}

	// B3. 顶层数据策略 create/adopt/update。
	for _, it := range plan.Items {
		if it.EntityType != "data_policy" || it.Kind == "delete" || it.Kind == "conflict" {
			continue
		}
		dp := desiredDPByIdentity[it.Identity]
		v, e := getVNew()
		if e != nil {
			return nil, e
		}
		switch it.Kind {
		case "create":
			p := cp.DataPolicy{
				SubjectType: dp.SubjectType, SubjectID: dp.SubjectID, Resource: dp.Resource,
				Condition: string(dp.Condition.JSON()), Effect: normEffect(dp.Effect),
			}
			id, _, e := store.UpsertDataPolicyWithSource(ctx, tx, appID, p, iacSource, v)
			if e != nil {
				return nil, e
			}
			p.ID = id
			dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeAdd, Policy: p})
		case "adopt", "update":
			row, ok := topDPByIdentity[it.Identity]
			if !ok {
				return nil, fmt.Errorf("policy: import: data_policy %q to %s no longer present", it.Identity, it.Kind)
			}
			if it.Kind == "adopt" {
				if e := store.AdoptDataPolicySource(ctx, tx, appID, row.ID); e != nil { // manual→iac
					return nil, e
				}
			}
			p := cp.DataPolicy{
				ID: row.ID, SubjectType: dp.SubjectType, SubjectID: dp.SubjectID, Resource: dp.Resource,
				Condition: string(dp.Condition.JSON()), Effect: normEffect(dp.Effect),
			}
			if _, _, e := store.UpsertDataPolicyWithSource(ctx, tx, appID, p, iacSource, v); e != nil {
				return nil, e
			}
			dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeUpdate, Policy: p})
		}
	}

	// ── Phase C：权限点删除（最后，此时引用已清）──────────────────────────────────
	for _, it := range plan.Items {
		if it.EntityType != "permission" || it.Kind != "delete" {
			continue
		}
		if e := store.DeletePermission(ctx, tx, appID, it.Identity); e != nil { // fail-close：仍被引用则报错整笔回滚
			return nil, e
		}
	}

	return dataChanges, nil
}

// resetRolePermissions 把角色授权全量对齐到 desiredCodes（先清后设，一致性优先而非差量）。
// 任一 code 解析不到 permission id → fail-close 整笔回滚。
func (m *PolicyManager) resetRolePermissions(ctx context.Context, tx *sql.Tx, appID, roleID int64, desiredCodes []string) error {
	curCodes, err := store.RolePermissionCodes(ctx, tx, appID, roleID)
	if err != nil {
		return err
	}
	union := map[string]bool{}
	for _, c := range curCodes {
		union[c] = true
	}
	for _, c := range desiredCodes {
		union[c] = true
	}
	all := make([]string, 0, len(union))
	for c := range union {
		all = append(all, c)
	}
	idByCode, err := store.PermissionIDsByCode(ctx, tx, appID, all)
	if err != nil {
		return err
	}
	for _, c := range curCodes {
		id, ok := idByCode[c]
		if !ok {
			return fmt.Errorf("policy: import: current permission code %q has no id (role %d)", c, roleID)
		}
		if e := store.DeleteRolePermission(ctx, tx, appID, roleID, id); e != nil {
			return e
		}
	}
	for _, c := range desiredCodes {
		id, ok := idByCode[c]
		if !ok {
			return fmt.Errorf("policy: import: role %d references undeclared permission code %q", roleID, c)
		}
		if e := store.InsertRolePermission(ctx, tx, appID, roleID, id, cp.EffectAllow); e != nil {
			return e
		}
	}
	return nil
}

// splitDataPolicies 把库侧数据策略分为「角色数据范围（subject_type='role'，按 code 分组）」
// 与「顶层数据策略（subject_type!='role'）」两组。
func splitDataPolicies(dps []store.DataPolicyWithSource) (roleByCode map[string][]store.DataPolicyWithSource, top []store.DataPolicyWithSource) {
	roleByCode = map[string][]store.DataPolicyWithSource{}
	for _, dp := range dps {
		if dp.SubjectType == "role" {
			roleByCode[dp.SubjectID] = append(roleByCode[dp.SubjectID], dp)
		} else {
			top = append(top, dp)
		}
	}
	return roleByCode, top
}

// dataScopesFromRows 把角色的数据策略行转为 iac.DataScope（condition 经规范化桥接）。
func dataScopesFromRows(rows []store.DataPolicyWithSource) []iac.DataScope {
	if len(rows) == 0 {
		return nil
	}
	out := make([]iac.DataScope, 0, len(rows))
	for _, dp := range rows {
		out = append(out, iac.DataScope{
			Resource: dp.Resource, Effect: dp.Effect,
			Condition: iac.ConditionFromJSON([]byte(dp.Condition)),
		})
	}
	return out
}

// normEffect 把空 effect 归一为 allow（对齐 DB DEFAULT 与广播 Delta 的真相值）。
func normEffect(e string) string {
	if e == "" {
		return cp.EffectAllow
	}
	return e
}
