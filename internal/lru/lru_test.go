package lru

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// withClock swaps in a clock the test drives, so TTL behaviour is asserted
// rather than slept for.
func withClock[K comparable, V any](c *Cache[K, V], now *time.Time) {
	c.now = func() time.Time { return *now }
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c := New[string, int](2, 0)

	c.Put("a", 1)
	c.Put("b", 2)
	// Touching "a" makes "b" the eviction candidate, which is the whole point
	// of LRU over insertion order.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should still be cached")
	}
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was evicted despite being used most recently")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c is missing")
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
	if got := c.Stats().Evictions; got != 1 {
		t.Errorf("evictions = %d, want 1", got)
	}
}

func TestEntriesExpire(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	c := New[string, int](4, time.Minute)
	withClock(c, &now)

	c.Put("a", 1)

	now = now.Add(59 * time.Second)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a expired before its TTL was up")
	}

	now = now.Add(2 * time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("a survived its TTL")
	}
	// The expired entry is dropped on lookup, not merely hidden.
	if got := c.Len(); got != 0 {
		t.Errorf("Len = %d, want 0 — an expired entry must be released", got)
	}
}

// Re-storing a key restarts its life. Without this, an entry refreshed every
// few seconds would still vanish one TTL after it was first written.
func TestPutRefreshesTheDeadline(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	c := New[string, int](4, time.Minute)
	withClock(c, &now)

	c.Put("a", 1)
	now = now.Add(50 * time.Second)
	c.Put("a", 2)
	now = now.Add(30 * time.Second)

	got, ok := c.Get("a")
	if !ok {
		t.Fatal("a expired despite being rewritten")
	}
	if got != 2 {
		t.Errorf("value = %d, want the rewritten 2", got)
	}
}

func TestZeroTTLNeverExpires(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	c := New[string, int](4, 0)
	withClock(c, &now)

	c.Put("a", 1)
	now = now.Add(1000 * time.Hour)

	if _, ok := c.Get("a"); !ok {
		t.Error("a expired although no TTL was configured")
	}
}

// A capacity of zero is how a caller disables the cache. It has to answer every
// call rather than panic, so the call sites need no nil check.
func TestZeroCapacityStoresNothing(t *testing.T) {
	c := New[string, int](0, time.Minute)

	c.Put("a", 1)

	if _, ok := c.Get("a"); ok {
		t.Error("a disabled cache returned a value")
	}
	if got := c.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
	if c.Remove("a") {
		t.Error("Remove reported an entry in a disabled cache")
	}
	c.Purge()
}

func TestRemoveAndPurge(t *testing.T) {
	c := New[string, int](4, 0)
	c.Put("a", 1)
	c.Put("b", 2)

	c.Get("b") // so there is a statistic for Purge to leave alone

	if !c.Remove("a") {
		t.Error("Remove did not find a")
	}
	if c.Remove("a") {
		t.Error("Remove found a twice")
	}
	if got := c.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}

	c.Purge()
	if got := c.Len(); got != 0 {
		t.Errorf("Len = %d after Purge, want 0", got)
	}
	// Statistics describe the cache's history, so Purge leaves them alone.
	if got := c.Stats(); got.Hits == 0 && got.Misses == 0 {
		t.Error("Purge cleared the statistics")
	}
}

func TestStatsCountHitsAndMisses(t *testing.T) {
	c := New[string, int](2, 0)
	c.Put("a", 1)

	c.Get("a")
	c.Get("a")
	c.Get("missing")

	got := c.Stats()
	if got.Hits != 2 {
		t.Errorf("hits = %d, want 2", got.Hits)
	}
	if got.Misses != 1 {
		t.Errorf("misses = %d, want 1", got.Misses)
	}
}

// The tile server's lock map leaked an entry per tile ever requested until it
// was refcounted; this is the same regression class one layer up. A key walk
// far larger than the capacity must leave the cache at its bound.
func TestCapacityHoldsUnderAKeyWalk(t *testing.T) {
	const (
		capacity = 64
		keys     = 10_000
	)

	c := New[string, int](capacity, 0)
	for i := range keys {
		c.Put(fmt.Sprintf("tile-%d", i), i)
	}

	if got := c.Len(); got != capacity {
		t.Errorf("Len = %d, want %d", got, capacity)
	}
	if got := c.Stats().Evictions; got != keys-capacity {
		t.Errorf("evictions = %d, want %d", got, keys-capacity)
	}
}

func TestConcurrentUse(t *testing.T) {
	const (
		workers = 8
		rounds  = 500
	)

	c := New[int, int](32, time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := range workers {
		go func() {
			defer wg.Done()
			for i := range rounds {
				key := (w*rounds + i) % 64
				c.Put(key, i)
				c.Get(key)
				if i%16 == 0 {
					c.Remove(key)
					_ = c.Len()
					_ = c.Stats()
				}
			}
		}()
	}
	wg.Wait()

	if got := c.Len(); got > 32 {
		t.Errorf("Len = %d, want <= 32", got)
	}
}
