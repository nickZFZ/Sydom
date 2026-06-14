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

// Compute 在调用方提供的只读 tx 内，对 (appID, user) 做瞬态求值。
// 内部自读 application.domain 作为引擎单一域来源。
// 任一步失败一律返回 error（fail-close），绝不返回空 Result 冒充「无权限」。
//
// 每调用建临时引擎（含 1024 槽 LRU + casbin 全量策略），灌策略后即弃；适合 Beta
// 低频管理接口（查看用户有效权限）。M2 若需高并发，可引入引擎池或复用常驻实例（缓存留 M2）。
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error) {
	var domain string
	if err := tx.QueryRowContext(ctx,
		`SELECT domain FROM application WHERE id=$1`, appID).Scan(&domain); err != nil {
		return Result{}, fmt.Errorf("effperm: read domain: %w", err)
	}

	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: read rules: %w", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: read data policies: %w", err)
	}

	table := dataperm.NewTable()
	eng, err := kernel.New(domain, nil, table)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: new engine: %w", err)
	}
	if err := eng.ApplySnapshot(toSnapshot(rules, dps)); err != nil {
		return Result{}, fmt.Errorf("effperm: apply snapshot: %w", err)
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
