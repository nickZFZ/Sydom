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
