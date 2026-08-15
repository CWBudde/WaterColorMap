// Package lru provides a bounded, optionally time-limited LRU cache.
//
// It is deliberately small and in-tree rather than a dependency. The tile
// server needs three things a general-purpose library does not hand over
// together: a capacity bound, a TTL, and hit/miss/eviction counters it can
// publish on its status endpoint. github.com/hashicorp/golang-lru's expirable
// variant covers the first two but exports no statistics, so it would have to
// be wrapped anyway — and the eviction policy wanted here is plain LRU, not one
// of the adaptive ones that make a library worth the dependency. If profiling
// ever calls for sharding or ARC, the swap happens behind this type.
package lru

import (
	"container/list"
	"sync"
	"time"
)

// Stats counts what the cache did. The counters are cumulative and never reset.
type Stats struct {
	Hits      uint64
	Misses    uint64
	Evictions uint64
}

// Cache is a fixed-capacity LRU with optional per-entry expiry. The zero value
// is not usable; call New. All methods are safe for concurrent use.
type Cache[K comparable, V any] struct {
	// now is the clock, injectable so the TTL can be tested without sleeping.
	now      func() time.Time
	items    map[K]*list.Element
	order    *list.List
	ttl      time.Duration
	capacity int
	mu       sync.Mutex
	stats    Stats
}

type entry[K comparable, V any] struct {
	expiresAt time.Time
	key       K
	value     V
}

// New returns a cache holding at most capacity entries, each living at most
// ttl.
//
// A capacity of zero or less yields a cache that stores nothing while still
// answering every call, so a caller can wire in a disabled cache without
// branching on nil at every use. A ttl of zero or less means entries never
// expire on their own and only leave by eviction.
func New[K comparable, V any](capacity int, ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[K]*list.Element),
		order:    list.New(),
		now:      time.Now,
	}
}

// Get returns the value stored under key and whether it was there. A hit moves
// the entry to the front of the eviction order.
//
// Expiry is lazy: an entry past its TTL is dropped when it is looked up rather
// than by a background sweep, so the cache owns no goroutine and there is
// nothing to stop.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	var zero V
	if c.capacity <= 0 {
		return zero, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		return zero, false
	}

	ent := el.Value.(*entry[K, V]) //nolint:errcheck // the list only ever holds this type
	if c.expired(ent) {
		c.removeElement(el)
		c.stats.Misses++
		return zero, false
	}

	c.order.MoveToFront(el)
	c.stats.Hits++
	return ent.value, true
}

// Put stores value under key, evicting the least recently used entry if the
// cache is full. Storing an existing key refreshes both its value and its TTL.
func (c *Cache[K, V]) Put(key K, value V) {
	if c.capacity <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		ent := el.Value.(*entry[K, V]) //nolint:errcheck // the list only ever holds this type
		ent.value = value
		ent.expiresAt = c.deadline()
		c.order.MoveToFront(el)
		return
	}

	c.items[key] = c.order.PushFront(&entry[K, V]{
		key:       key,
		value:     value,
		expiresAt: c.deadline(),
	})

	for c.order.Len() > c.capacity {
		if oldest := c.order.Back(); oldest != nil {
			c.removeElement(oldest)
			c.stats.Evictions++
		}
	}
}

// Remove drops the entry for key, reporting whether one was there. It is how a
// caller invalidates an entry it knows has gone stale, without waiting for the
// TTL.
func (c *Cache[K, V]) Remove(key K) bool {
	if c.capacity <= 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return false
	}
	c.removeElement(el)
	return true
}

// Purge drops every entry. The statistics are left alone: they describe the
// cache's history, not its contents.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[K]*list.Element)
	c.order.Init()
}

// Len reports how many entries are held, expired ones included: they are
// dropped when looked up, not counted out here.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Stats returns a snapshot of the counters.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *Cache[K, V]) deadline() time.Time {
	if c.ttl <= 0 {
		return time.Time{}
	}
	return c.now().Add(c.ttl)
}

func (c *Cache[K, V]) expired(ent *entry[K, V]) bool {
	return !ent.expiresAt.IsZero() && !c.now().Before(ent.expiresAt)
}

// removeElement drops one element. The caller holds the mutex.
func (c *Cache[K, V]) removeElement(el *list.Element) {
	ent := el.Value.(*entry[K, V]) //nolint:errcheck // the list only ever holds this type
	c.order.Remove(el)
	delete(c.items, ent.key)
}
