package main

import (
	"container/list"
	"sync"
)

// recordCache is the service's in-process cache of rendered user records: a
// least-recently-used map bounded by entry count. A bound of zero means the
// cache never evicts.
type recordCache struct {
	mu      sync.Mutex
	max     int
	order   *list.List // front is most recently used
	entries map[string]*list.Element
	bytes   int
}

type cacheEntry struct {
	key  string
	body []byte
}

func newRecordCache(maxEntries int) *recordCache {
	return &recordCache{
		max:     maxEntries,
		order:   list.New(),
		entries: make(map[string]*list.Element),
	}
}

// Put stores body under key, evicting the least recently used entries while
// the cache is over its bound.
func (c *recordCache) Put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		e := el.Value.(*cacheEntry)
		c.bytes += len(body) - len(e.body)
		e.body = body
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, body: body})
	c.bytes += len(body)
	for c.max > 0 && c.order.Len() > c.max {
		oldest := c.order.Back()
		e := oldest.Value.(*cacheEntry)
		c.order.Remove(oldest)
		delete(c.entries, e.key)
		c.bytes -= len(e.body)
	}
}

// Get returns the body stored under key and marks it recently used.
func (c *recordCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheEntry).body, true
}

// Stats reports the number of entries and the bytes they hold.
func (c *recordCache) Stats() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len(), c.bytes
}
