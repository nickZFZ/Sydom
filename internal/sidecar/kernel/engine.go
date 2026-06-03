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
