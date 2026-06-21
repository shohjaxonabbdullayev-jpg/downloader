// Package fidcache caches Telegram file_id tokens per source URL so repeat
// requests can be served instantly — Telegram re-sends its own stored copy by
// file_id with no download and no re-upload. Only short string tokens are kept,
// in memory; nothing is written to disk.
package fidcache

import "sync"

// Item is one cached Telegram media reference for a URL.
type Item struct {
	Kind   string // "video", "animation", "photo", or "document"
	FileID string
}

// Cache is a bounded, concurrency-safe URL -> []Item store with FIFO eviction.
type Cache struct {
	mu    sync.Mutex
	max   int
	items map[string][]Item
	order []string // insertion order, for eviction once over capacity
}

// New returns a cache holding at most max entries (default 2000).
func New(max int) *Cache {
	if max <= 0 {
		max = 2000
	}
	return &Cache{max: max, items: make(map[string][]Item)}
}

// Get returns the cached items for key, if present.
func (c *Cache) Get(key string) ([]Item, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

// Put stores items for key, evicting the oldest entries beyond capacity.
func (c *Cache) Put(key string, items []Item) {
	if key == "" || len(items) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
		for len(c.order) > c.max {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.items, oldest)
		}
	}
	c.items[key] = items
}

// Delete removes a (likely stale) entry.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
