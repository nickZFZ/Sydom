package kernel

import (
	"container/list"
	"sync"

	"github.com/casbin/casbin/v3/persist/cache"
)

// boundedCache 是有界 LRU，实现 casbin persist/cache.Cache。
// 仅作决策缓存的内存上界（非一致性手段——一致性靠每次 apply 后 InvalidateCache 全量清）。
type boundedCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type cacheEntry struct {
	key string
	val bool
}

// newBoundedCache 构造容量为 capacity 的 LRU（capacity<=0 视为 1）。
func newBoundedCache(capacity int) *boundedCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &boundedCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

func (c *boundedCache) Set(key string, value bool, _ ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).val = value
		c.ll.MoveToFront(el)
		return nil
	}
	el := c.ll.PushFront(&cacheEntry{key: key, val: value})
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
	return nil
}

func (c *boundedCache) Get(key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return false, cache.ErrNoSuchKey
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).val, nil
}

func (c *boundedCache) Delete(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return cache.ErrNoSuchKey
	}
	c.ll.Remove(el)
	delete(c.items, key)
	return nil
}

func (c *boundedCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[string]*list.Element, c.capacity)
	return nil
}

var _ cache.Cache = (*boundedCache)(nil)
