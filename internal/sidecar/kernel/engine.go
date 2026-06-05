package kernel

import (
	"sync"
	"sync/atomic"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/persist/cache"
)

// Engine 是单 app（单 casbin domain）的纯内存功能权限内核。
type Engine struct {
	domain  string
	ce      *casbin.SyncedCachedEnforcer
	applier DataPolicyApplier

	applyMu sync.Mutex // 串行化 apply（validate→mutate→记版本）
	version atomic.Uint64
	ready   atomic.Bool
}

// New 构造内核：pin 本 app 的 domain；c 为决策缓存（nil 则内部建容量 1024 的有界 LRU）；
// applier 接收数据策略变更（nil 则退化 no-op，便于独立单测）。
func New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error) {
	m, err := buildModel()
	if err != nil {
		return nil, err
	}
	ce, err := casbin.NewSyncedCachedEnforcer(m, newMemoryAdapter(nil))
	if err != nil {
		return nil, err
	}
	ce.EnableAutoSave(false)          // 只读 adapter：运行期改内存不回写
	ce.EnableAutoNotifyWatcher(false) // 纯订阅端：杜绝回播
	if c == nil {
		c = newBoundedCache(1024)
	}
	ce.SetCache(c)
	if applier == nil {
		applier = noopApplier{}
	}
	return &Engine{domain: domain, ce: ce, applier: applier}, nil
}

// Version 返回当前已应用版本（未就绪为 0）。
func (e *Engine) Version() uint64 { return e.version.Load() }

// Ready 表示是否已成功应用过一次快照。
func (e *Engine) Ready() bool { return e.ready.Load() }

// Domain 返回构造时 pin 的 casbin 域（供上层组合者取单一真相源的域，避免平行配置漂移）。
func (e *Engine) Domain() string { return e.domain }

// Enforce 判定 (sub,dom,obj,act)。未就绪/越域/出错一律 fail-close。
func (e *Engine) Enforce(sub, dom, obj, act string) (bool, error) {
	if !e.ready.Load() {
		return false, ErrNotReady
	}
	if dom != e.domain {
		return false, ErrForeignDomain
	}
	return e.ce.Enforce(sub, dom, obj, act)
}

// ApplySnapshot 全量重建内核状态：校验越域→ClearPolicy→分段灌入→路由数据策略→全量清缓存→记版本就绪。
// 越域行整笔拒绝（pre-clear，状态不变）；进入重建后任何失败一律 fail-close（ready=false），等 ④-3 重试。
//
// 已知可接受行为：重建经多次自锁调用，并发 Enforce 可能读到「已清空未重建」瞬时态→暂拒（fail-close
// 新鲜度滞后，非错误放行；快照罕见）。符合架构 §2.2/§5，不消窗。
func (e *Engine) ApplySnapshot(s Snapshot) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()

	for _, r := range s.Rules { // 1. pre-clear 越域校验
		if r.domainValue() != e.domain {
			return ErrForeignDomain
		}
	}

	e.ce.ClearPolicy() // 2. 进入重建——此后任何失败 fail-close
	var pRules, gRules [][]string
	for _, r := range s.Rules {
		switch r.Ptype {
		case "p":
			pRules = append(pRules, r.values())
		case "g":
			gRules = append(gRules, r.values())
		}
	}
	if len(pRules) > 0 {
		if _, err := e.ce.AddNamedPolicies("p", pRules); err != nil {
			e.ready.Store(false)
			return err
		}
	}
	if len(gRules) > 0 {
		if _, err := e.ce.AddNamedGroupingPolicies("g", gRules); err != nil {
			e.ready.Store(false)
			return err
		}
	}

	e.applier.ApplySnapshot(s.DataPolicies) // 3. 路由数据策略
	if err := e.ce.InvalidateCache(); err != nil {
		e.ready.Store(false)
		return err
	}
	e.version.Store(s.Version)
	e.ready.Store(true)
	return nil
}

// ApplyDelta 增量应用一条变更：版本单调校验→越域校验→逐 PolicyChange 改 casbin→路由数据策略→
// 全量清缓存→记版本。版本未严格大于当前→ErrStaleVersion（拒重放/乱序）；越域→ErrForeignDomain（状态不变）；
// 进入变更后任何失败 fail-close（ready=false）。
func (e *Engine) ApplyDelta(d Delta) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()

	if d.Version <= e.version.Load() {
		return ErrStaleVersion
	}
	for _, pc := range d.PolicyChanges { // 越域校验（pre-mutation）
		if pc.Rule.domainValue() != e.domain {
			return ErrForeignDomain
		}
		if pc.Op == ChangeUpdate && pc.OldRule.domainValue() != e.domain {
			return ErrForeignDomain
		}
	}

	for _, pc := range d.PolicyChanges { // 进入变更——失败 fail-close
		if err := e.applyPolicyChange(pc); err != nil {
			e.ready.Store(false)
			return err
		}
	}
	for _, dc := range d.DataChanges {
		e.applier.ApplyChange(dc.Op, dc.Policy)
	}
	if err := e.ce.InvalidateCache(); err != nil {
		e.ready.Store(false)
		return err
	}
	e.version.Store(d.Version)
	return nil
}

func (e *Engine) applyPolicyChange(pc PolicyChange) error {
	switch pc.Op {
	case ChangeAdd:
		return e.addRule(pc.Rule)
	case ChangeRemove:
		return e.removeRule(pc.Rule)
	case ChangeUpdate: // 防御性：删旧+加新（section-correct）。③ 不对功能行发 UPDATE，但内核兜住。
		// 删旧成功而加新失败时 casbin 处于「半新半旧」部分应用态——调用方（ApplyDelta）随即
		// ready.Store(false)，后续 Enforce 全部 fail-close 屏蔽脏态，等 ④-3 拉全量快照 ApplySnapshot
		// （ClearPolicy 重建）覆盖。故此处无需回滚：一致性由 ready=false + 快照重建兜底。
		if err := e.removeRule(pc.OldRule); err != nil {
			return err
		}
		return e.addRule(pc.Rule)
	default:
		return nil
	}
}

// addRule/removeRule 按 ptype 走 section-correct 的 casbin 高层 API（g 段自动 BuildIncrementalRoleLinks）。
func (e *Engine) addRule(r Rule) error {
	switch r.Ptype {
	case "p":
		_, err := e.ce.AddNamedPolicies("p", [][]string{r.values()})
		return err
	case "g":
		_, err := e.ce.AddNamedGroupingPolicies("g", [][]string{r.values()})
		return err
	default:
		return nil
	}
}

func (e *Engine) removeRule(r Rule) error {
	switch r.Ptype {
	case "p":
		_, err := e.ce.RemoveNamedPolicies("p", [][]string{r.values()})
		return err
	case "g":
		_, err := e.ce.RemoveNamedGroupingPolicies("g", [][]string{r.values()})
		return err
	default:
		return nil
	}
}

// GetImplicitRolesForUser 把 user 展开为隐式角色集（含继承），供 ④-2 数据权限主体解析。
// 已回源核实（casbin v3.10.0）：SyncedEnforcer 重写了 GetImplicitRolesForUser（rbac_api_synced.go:155）
// 内部已自取 e.m.RLock()，且 SyncedCachedEnforcer 未再重写——故此处直接调用即并发安全。
// 切勿在外层再 GetLock().RLock()：GetLock() 返回的正是同一把 e.m，叠加读锁在 apply 写锁到来时会触发
// Go RWMutex 递归读锁死锁（写者阻塞于外层读锁、内层读锁又阻塞于待定写者）。
func (e *Engine) GetImplicitRolesForUser(user, dom string) ([]string, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	if dom != e.domain {
		return nil, ErrForeignDomain
	}
	return e.ce.GetImplicitRolesForUser(user, dom)
}

// BatchEnforce 批量鉴权。未就绪 fail-close。
// 注意语义差异（刻意取舍）：单条 Enforce 对外域请求显式返回 ErrForeignDomain；批量接口不逐条校验越域，
// 外域请求经 matcher 自然不命中任何本域策略→false。两者 fail-close 等价（都不放行），但批量以 false
// 表达拒绝、不回传越域信号——调用方需要区分「越域」与「域内无权」时应走单条 Enforce。
// 另：casbin 的 BatchEnforce 直调底层 enforce、绕过决策缓存（与单条 Enforce 走缓存不同），
// 故批量鉴权不享缓存命中，高频批量调用需自行权衡。
func (e *Engine) BatchEnforce(reqs [][]string) ([]bool, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	casReqs := make([][]interface{}, len(reqs))
	for i, r := range reqs {
		row := make([]interface{}, len(r))
		for j, v := range r {
			row[j] = v
		}
		casReqs[i] = row
	}
	return e.ce.BatchEnforce(casReqs)
}
