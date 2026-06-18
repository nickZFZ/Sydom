// Package effperm 在控制面内瞬态复用 Sidecar 求值栈（kernel.Engine + dataperm），
// 从 DB 物化策略（store.ReadAppRules/ReadAppDataPolicies，与 Sidecar 快照同源）算「某 user 能做什么」。
//
// casbin v3.10.0 已回源核实（enforcer.go:950–960、rbac_api.go:225–252）：
// BatchEnforce 对每条 req 逐条调用 enforce("", nil, request...)，等价单条 Enforce，
// 逐条套用 model 中 some(allow)&&!some(deny)（deny 覆盖）；
// GetImplicitRolesForUser 聚合所有 rmMap/condRmMap 的隐式角色、返回含继承角色但不含 user 自身。
package effperm

import (
	"context"
	"fmt"
	"sort"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// Perm 是一条功能权限（deny 覆盖后的允许动作）。
type Perm struct {
	Resource string
	Action   string
}

// DataView 是某 resource 的数据策略符号预览。
type DataView struct {
	Resource  string
	Match     string // all | none | conditional
	Predicate string // 仅 conditional 非空
}

// Result 是一次有效权限求值结果。
type Result struct {
	Roles       []string
	Permissions []Perm
	DataViews   []DataView
}

// buildEngine 在只读 tx 内从 DB 物化策略、建瞬态引擎（Compute/Explain 共用，杜绝两份漂移）。
// 返回引擎、数据策略表、原始 rules/dps（供 Compute 枚举）、域。
func buildEngine(ctx context.Context, tx cp.DBTX, appID int64) (*kernel.Engine, *dataperm.Table, []cp.Rule, []cp.DataPolicy, string, error) {
	var domain string
	if err := tx.QueryRowContext(ctx,
		`SELECT domain FROM application WHERE id=$1`, appID).Scan(&domain); err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read domain: %w", err)
	}
	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read rules: %w", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read data policies: %w", err)
	}
	table := dataperm.NewTable()
	eng, err := kernel.New(domain, nil, table)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: new engine: %w", err)
	}
	if err := eng.ApplySnapshot(toSnapshot(rules, dps)); err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: apply snapshot: %w", err)
	}
	return eng, table, rules, dps, domain, nil
}

// Compute 在调用方提供的只读 tx 内，对 (appID, user) 做瞬态求值。
// 内部自读 application.domain 作为引擎单一域来源。
// 任一步失败一律返回 error（fail-close），绝不返回空 Result 冒充「无权限」。
//
// 每调用建临时引擎（含 1024 槽 LRU + casbin 全量策略），灌策略后即弃；适合 Beta
// 低频管理接口（查看用户有效权限）。M2 若需高并发，可引入引擎池或复用常驻实例（缓存留 M2）。
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error) {
	eng, table, rules, dps, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return Result{}, err
	}

	roles, err := eng.GetImplicitRolesForUser(user, domain)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: implicit roles: %w", err)
	}
	sort.Strings(roles)

	perms, err := computePerms(eng, rules, domain, user)
	if err != nil {
		return Result{}, err
	}
	views, err := computeViews(eng, table, dps, domain, user)
	if err != nil {
		return Result{}, err
	}
	return Result{Roles: roles, Permissions: perms, DataViews: views}, nil
}

// reason 分类（决策可解释性）。
const (
	ReasonAllowGranted   = "ALLOW_GRANTED"   // 命中 allow 授权
	ReasonDenyOverridden = "DENY_OVERRIDDEN" // 命中 deny 规则覆盖
	ReasonDenyNoMatch    = "DENY_NO_MATCH"   // 无任何规则命中（默认拒绝）
)

// DecidingRule 是判定的 casbin p 规则（解构自 EnforceEx 的 [sub,dom,obj,act,eft]）。
type DecidingRule struct {
	Subject  string
	Resource string
	Action   string
	Effect   string // allow | deny
}

// Explanation 是一次单决策 explain 结果。
type Explanation struct {
	Allowed      bool
	Reason       string
	DecidingRule *DecidingRule // 默认拒绝时为 nil
	DecidingRole string        // 默认拒绝时为 ""
	Roles        []string      // 用户有效角色(含继承)，排序稳定
	DataScope    DataView      // 该 resource 数据策略符号预览(复用 DataView)
}

// Explain 在只读 tx 内对单条 (appID, user, resource, action) 做瞬态求值并解释。
// 复用与 Compute 同一引擎栈（buildEngine），杜绝第二套决策逻辑。任一步失败一律返 error（fail-close），
// 绝不返回空 Explanation 冒充「拒绝」。
func Explain(ctx context.Context, tx cp.DBTX, appID int64, user, resource, action string) (Explanation, error) {
	eng, table, _, _, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return Explanation{}, err
	}

	roles, err := eng.GetImplicitRolesForUser(user, domain)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: implicit roles: %w", err)
	}
	sort.Strings(roles)

	allowed, rule, err := eng.EnforceEx(user, domain, resource, action)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: enforce ex: %w", err)
	}

	exp := Explanation{Allowed: allowed, Roles: roles}
	if len(rule) == 0 {
		exp.Reason = ReasonDenyNoMatch // 无规则命中
	} else {
		// rule = [sub, dom, obj, act, eft]（Sydom p 行 5 段）。
		exp.DecidingRole = rule[0]
		dr := &DecidingRule{Subject: rule[0]}
		if len(rule) >= 5 {
			dr.Resource, dr.Action, dr.Effect = rule[2], rule[3], rule[4]
		}
		exp.DecidingRule = dr
		if allowed {
			exp.Reason = ReasonAllowGranted
		} else {
			exp.Reason = ReasonDenyOverridden
		}
	}

	sr, err := dataperm.NewFilter(eng, table).FilterSymbolic(user, domain, resource)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: symbolic filter %q: %w", resource, err)
	}
	exp.DataScope = DataView{Resource: resource, Match: sr.Match, Predicate: sr.Predicate}
	return exp, nil
}

// toSnapshot 把控制面 Rule/DataPolicy 转为 kernel.Snapshot。
func toSnapshot(rules []cp.Rule, dps []cp.DataPolicy) kernel.Snapshot {
	ks := make([]kernel.Rule, len(rules))
	for i, r := range rules {
		ks[i] = kernel.Rule{Ptype: r.Ptype, V: r.V}
	}
	kd := make([]kernel.DataPolicy, len(dps))
	for i, d := range dps {
		kd[i] = kernel.DataPolicy{
			ID:          uint64(d.ID),
			SubjectType: d.SubjectType,
			SubjectID:   d.SubjectID,
			Resource:    d.Resource,
			Condition:   d.Condition,
			Effect:      d.Effect,
		}
	}
	// Version 仅满足 Snapshot 非零约束令引擎 ready；临时引擎不走 ApplyDelta，无版本单调性需求。
	return kernel.Snapshot{Version: 1, Rules: ks, DataPolicies: kd}
}

// computePerms 枚举该域 p 行的 (obj,act) 候选去重，BatchEnforce 跑真实 deny 覆盖，收 allow 集（排序稳定）。
// p 行格式：V[0]=sub, V[1]=dom, V[2]=obj, V[3]=act, V[4]=eft（对齐 model.go policy_definition）。
func computePerms(eng *kernel.Engine, rules []cp.Rule, domain, user string) ([]Perm, error) {
	type oa struct{ obj, act string }
	seen := map[oa]bool{}
	var cands []oa
	for _, r := range rules {
		if r.Ptype != "p" {
			continue
		}
		k := oa{r.V[2], r.V[3]} // V[2]=obj, V[3]=act
		if !seen[k] {
			seen[k] = true
			cands = append(cands, k)
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	reqs := make([][]string, len(cands))
	for i, c := range cands {
		reqs[i] = []string{user, domain, c.obj, c.act}
	}
	results, err := eng.BatchEnforce(reqs)
	if err != nil {
		return nil, fmt.Errorf("effperm: batch enforce: %w", err)
	}
	var perms []Perm
	for i, ok := range results {
		if ok {
			perms = append(perms, Perm{Resource: cands[i].obj, Action: cands[i].act})
		}
	}
	sort.Slice(perms, func(i, j int) bool {
		if perms[i].Resource != perms[j].Resource {
			return perms[i].Resource < perms[j].Resource
		}
		return perms[i].Action < perms[j].Action
	})
	return perms, nil
}

// computeViews 对每个 distinct data_policy resource 做符号预览（resource 排序稳定）。
func computeViews(eng *kernel.Engine, table *dataperm.Table, dps []cp.DataPolicy, domain, user string) ([]DataView, error) {
	seen := map[string]bool{}
	var resources []string
	for _, d := range dps {
		if !seen[d.Resource] {
			seen[d.Resource] = true
			resources = append(resources, d.Resource)
		}
	}
	sort.Strings(resources)
	filter := dataperm.NewFilter(eng, table)
	var views []DataView
	for _, res := range resources {
		sr, err := filter.FilterSymbolic(user, domain, res)
		if err != nil {
			return nil, fmt.Errorf("effperm: symbolic filter %q: %w", res, err)
		}
		views = append(views, DataView{Resource: res, Match: sr.Match, Predicate: sr.Predicate})
	}
	return views, nil
}
