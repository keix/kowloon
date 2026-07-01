// Package memory is an in-process LRU implementation of cache.Cache.
// It is the v0 default: no persistence, capacity-bounded, thread-safe.
// Restarting kowloon-api drops the cache — that is the trade-off for
// zero infrastructure. A DynamoDB implementation lands in a follow-up
// commit for production persistence.
package memory

import (
	"container/list"
	"context"
	"sync"

	"github.com/keix/kowloon/internal/embed/cache"
)

// DefaultCapacity is the entry count the LRU falls back to when
// New(0) is called. At 3072-dim float32 (text-embedding-3-large),
// 10_000 entries hold roughly ~120 MB — small enough for a single
// long-running process.
const DefaultCapacity = 10_000

type Cache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	lookup   map[string]*list.Element
}

type entry struct {
	key    string
	vector []float32
}

func New(capacity int) *Cache {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Cache{
		capacity: capacity,
		ll:       list.New(),
		lookup:   make(map[string]*list.Element),
	}
}

func (c *Cache) Get(_ context.Context, key cache.Key) ([]float32, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.String()
	elem, ok := c.lookup[k]
	if !ok {
		return nil, false, nil
	}
	c.ll.MoveToFront(elem)
	return elem.Value.(*entry).vector, true, nil
}

func (c *Cache) Put(_ context.Context, key cache.Key, vec []float32) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key.String()
	if elem, ok := c.lookup[k]; ok {
		c.ll.MoveToFront(elem)
		elem.Value.(*entry).vector = vec
		return nil
	}
	e := &entry{key: k, vector: vec}
	elem := c.ll.PushFront(e)
	c.lookup[k] = elem
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.lookup, oldest.Value.(*entry).key)
		}
	}
	return nil
}

// Len reports the current entry count. Useful for tests and for the
// admin endpoint that will eventually expose cache stats.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
