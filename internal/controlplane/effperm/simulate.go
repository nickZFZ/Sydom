package effperm

import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// Change 描述一次假设性角色变更。
// Type 取值：
//   - "bind_user"      : 把 UserID 绑到 roleCode（合成 g 行）
//   - "add_capability" : 给 roleCode 授 Resource:Action allow（合成 p 行），受影响用户=已绑该角色的全部用户
type Change struct {
	Type     string // "bind_user" | "add_capability"
	UserID   string // bind_user 时必填
	Resource string // add_capability 时必填
	Action   string // add_capability 时必填
}

// SubjectDiff 是单个用户受一次假设变更影响的有效权限双向 diff。
type SubjectDiff struct {
	UserID             string
	AddedPermissions   []Perm
	RemovedPermissions []Perm
	AddedDataViews     []DataView
	RemovedDataViews   []DataView
}

// nonEmpty 返回 diff 是否有实质内容——四个切片任一非空即为真实 diff 值得收录；
// 四个全空说明本次假设变更对该用户净效果为零，过滤掉。
func (d SubjectDiff) nonEmpty() bool {
	return len(d.AddedPermissions) > 0 || len(d.RemovedPermissions) > 0 ||
		len(d.AddedDataViews) > 0 || len(d.RemovedDataViews) > 0
}

// Simulate 在调用方提供的只读 tx 内，对一次假设性角色变更做反事实求值，
// 返回受影响用户的有效权限双向 diff（不落库、不 bump 版本、不广播）。
//
// 实现步骤：
//  1. readAppPolicy 读取 domain/rules/dps（只读 DB）
//  2. buildEngineFrom 建基准引擎（base）
//  3. 据 change.Type 生成合成规则 + subjects 列表
//  4. 建假设引擎（hypo = rules + synthetic rules）
//  5. 对每个 subject 用同一 computeResult 算 base/hypo 结果、双向 diff
//
// fail-close：任一步失败返回 error，绝不返回含空 Result 的假 diff。
func Simulate(ctx context.Context, tx cp.DBTX, appID int64, roleCode string, change Change) ([]SubjectDiff, error) {
	// 1. 读 DB policy
	domain, rules, dps, err := readAppPolicy(ctx, tx, appID)
	if err != nil {
		return nil, err
	}

	// 2. 建基准引擎
	baseEng, baseTable, err := buildEngineFrom(domain, rules, dps)
	if err != nil {
		return nil, err
	}

	// 3. 生成合成规则 + subjects
	var synthetic []cp.Rule
	var subjects []string

	switch change.Type {
	case "bind_user":
		if change.UserID == "" {
			return nil, fmt.Errorf("effperm: simulate bind_user: UserID is required")
		}
		// 合成 g 行：child=UserID, parent=roleCode, dom=domain
		synthetic = []cp.Rule{
			{Ptype: "g", V: [6]string{change.UserID, roleCode, domain, "", "", ""}},
		}
		subjects = []string{change.UserID}

	case "add_capability":
		if change.Resource == "" || change.Action == "" {
			return nil, fmt.Errorf("effperm: simulate add_capability: Resource and Action are required")
		}
		// 合成 p 行：sub=roleCode, dom=domain, obj=Resource, act=Action, eft=allow
		synthetic = []cp.Rule{
			{Ptype: "p", V: [6]string{roleCode, domain, change.Resource, change.Action, "allow", ""}},
		}
		// 受影响用户 = 已绑该角色的全部用户（基准引擎中，含继承路径）
		subjects, err = usersWithRole(ctx, tx, baseEng, appID, domain, roleCode)
		if err != nil {
			return nil, fmt.Errorf("effperm: simulate add_capability: list users with role: %w", err)
		}

	default:
		return nil, fmt.Errorf("effperm: simulate: unknown change type %q", change.Type)
	}

	// 4. 建假设引擎（合成规则追加到 rules 末尾）
	hypoRules := make([]cp.Rule, len(rules)+len(synthetic))
	copy(hypoRules, rules)
	copy(hypoRules[len(rules):], synthetic)

	hypoEng, hypoTable, err := buildEngineFrom(domain, hypoRules, dps)
	if err != nil {
		return nil, fmt.Errorf("effperm: simulate: build hypo engine: %w", err)
	}

	// 5. 对每个 subject 双向 diff
	var out []SubjectDiff
	for _, uid := range subjects {
		base, err := computeResult(baseEng, baseTable, rules, dps, domain, uid)
		if err != nil {
			return nil, fmt.Errorf("effperm: simulate: base result for %q: %w", uid, err)
		}
		hypo, err := computeResult(hypoEng, hypoTable, hypoRules, dps, domain, uid)
		if err != nil {
			return nil, fmt.Errorf("effperm: simulate: hypo result for %q: %w", uid, err)
		}
		diff := SubjectDiff{
			UserID:             uid,
			AddedPermissions:   permDiff(hypo.Permissions, base.Permissions),
			RemovedPermissions: permDiff(base.Permissions, hypo.Permissions),
			AddedDataViews:     viewDiff(hypo.DataViews, base.DataViews),
			RemovedDataViews:   viewDiff(base.DataViews, hypo.DataViews),
		}
		if diff.nonEmpty() {
			out = append(out, diff)
		}
	}
	return out, nil
}

// usersWithRole 查询 app 下所有曾绑定任意角色的用户（从 user_role_binding），
// 然后用基准引擎的 GetImplicitRolesForUser 判断其隐式角色中是否包含 roleCode。
// 返回含 roleCode（或其子角色祖先链覆盖 roleCode）的用户列表。
func usersWithRole(ctx context.Context, tx cp.DBTX, eng *kernel.Engine, appID int64, domain, roleCode string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT user_id FROM user_role_binding WHERE app_id=$1 ORDER BY user_id`,
		appID)
	if err != nil {
		return nil, fmt.Errorf("effperm: usersWithRole query: %w", err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("effperm: usersWithRole scan: %w", err)
		}
		roles, err := eng.GetImplicitRolesForUser(uid, domain)
		if err != nil {
			return nil, fmt.Errorf("effperm: usersWithRole GetImplicitRoles for %q: %w", uid, err)
		}
		for _, r := range roles {
			if r == roleCode {
				result = append(result, uid)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("effperm: usersWithRole rows: %w", err)
	}
	return result, nil
}

// permDiff 返回 a 中有但 b 中没有的 Perm（a − b）。Perm 按全字段可比较作 map key。
func permDiff(a, b []Perm) []Perm {
	bSet := make(map[Perm]struct{}, len(b))
	for _, p := range b {
		bSet[p] = struct{}{}
	}
	var diff []Perm
	for _, p := range a {
		if _, ok := bSet[p]; !ok {
			diff = append(diff, p)
		}
	}
	return diff
}

// viewDiff 返回 a 中有但 b 中没有的 DataView（a − b）。DataView 按全字段可比较作 map key。
func viewDiff(a, b []DataView) []DataView {
	bSet := make(map[DataView]struct{}, len(b))
	for _, v := range b {
		bSet[v] = struct{}{}
	}
	var diff []DataView
	for _, v := range a {
		if _, ok := bSet[v]; !ok {
			diff = append(diff, v)
		}
	}
	return diff
}
