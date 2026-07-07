package obs

import "github.com/casbin/casbin/v3/persist/cache"

// metricsCache 装饰任意 casbin cache.Cache：在 Get 处计命中/未命中，其余透传。
// 不改被装饰缓存的任何语义（命中/未命中由 inner.Get 的 error 判定：非 nil=未命中）。
type metricsCache struct {
	inner cache.Cache
	m     *Metrics
}

// NewMetricsCache 用指标装饰 inner；注入 kernel.New 的 cache 参数即可为决策缓存计命中率。
func NewMetricsCache(inner cache.Cache, m *Metrics) cache.Cache {
	return &metricsCache{inner: inner, m: m}
}

func (c *metricsCache) Get(key string) (bool, error) {
	v, err := c.inner.Get(key)
	if err != nil {
		c.m.CacheMiss()
		return v, err
	}
	c.m.CacheHit()
	return v, nil
}

func (c *metricsCache) Set(key string, value bool, extra ...interface{}) error {
	return c.inner.Set(key, value, extra...)
}
func (c *metricsCache) Delete(key string) error { return c.inner.Delete(key) }
func (c *metricsCache) Clear() error            { return c.inner.Clear() }

var _ cache.Cache = (*metricsCache)(nil)
