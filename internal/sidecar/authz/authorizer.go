package authz

import (
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// Freshness 暴露同步新鲜度信号；*syncclient.SyncClient 满足之。窄接口便于测试注入。
type Freshness interface {
	Ready() bool
	LastSyncAt() time.Time
}

// Config 是 Authorizer 的策略参数。
type Config struct {
	// MaxStaleness 为 0 关闭陈旧守卫（Ready 即服务）；>0 时 now-LastSyncAt 超阈 fail-close。
	MaxStaleness time.Duration
}

// CheckReq 是一条批量鉴权请求（域由 Authorizer pin，不在请求内）。
type CheckReq struct {
	Subject string
	Object  string
	Action  string
}

// Authorizer 组合内核 + 数据权限 + 陈旧守卫，是数据面鉴权门面。不做任何持久化/网络副作用。
type Authorizer struct {
	engine *kernel.Engine
	filter *dataperm.Filter
	fresh  Freshness
	domain string // = engine.Domain()，构造时取，单一真相源
	cfg    Config
	now    func() time.Time // 注入便于测试
}

// New 组装 Authorizer；pin 域取自内核（engine.Domain()），避免平行配置漂移成 deny-all。
func New(engine *kernel.Engine, filter *dataperm.Filter, fresh Freshness, cfg Config) *Authorizer {
	return &Authorizer{
		engine: engine,
		filter: filter,
		fresh:  fresh,
		domain: engine.Domain(),
		cfg:    cfg,
		now:    time.Now,
	}
}

// checkFresh 是陈旧守卫：未就绪 → ErrNotReady；超阈（含从未同步）→ ErrTooStale；否则放行。
func (a *Authorizer) checkFresh() error {
	if !a.fresh.Ready() {
		return kernel.ErrNotReady
	}
	if a.cfg.MaxStaleness > 0 {
		last := a.fresh.LastSyncAt()
		if last.IsZero() || a.now().Sub(last) > a.cfg.MaxStaleness {
			return ErrTooStale
		}
	}
	return nil
}

// Check 判定 (subject, object, action)；域由 pin。守卫不通过即 fail-close。
func (a *Authorizer) Check(subject, object, action string) (bool, error) {
	if err := a.checkFresh(); err != nil {
		return false, err
	}
	return a.engine.Enforce(subject, a.domain, object, action)
}

// BatchCheck 批量判定；用 pin 域组装 casbin 四元请求，等长同序返回。守卫不通过即 fail-close。
func (a *Authorizer) BatchCheck(reqs []CheckReq) ([]bool, error) {
	if err := a.checkFresh(); err != nil {
		return nil, err
	}
	rows := make([][]string, len(reqs))
	for i, r := range reqs {
		rows[i] = []string{r.Subject, a.domain, r.Object, r.Action}
	}
	return a.engine.BatchEnforce(rows)
}
