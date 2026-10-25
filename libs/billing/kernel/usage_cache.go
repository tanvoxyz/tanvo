package kernel

import (
	"container/list"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// UsageCache keeps the current-usage fees of charge buckets between
// computations (lago Subscriptions::ChargeCacheService). An entry is
// validated lazily by its reader: it holds the ingestion watermark of the
// events it aggregated and is used only while no event was ingested after
// it (metering.Watermark). Implementations are safe for concurrent use
// and copy the fees in and out.
type UsageCache interface {
	// Get returns the entry of key, false when there is none or it
	// expired at now.
	Get(key string, now time.Time) (UsageCacheEntry, bool)
	// Put stores the entry of key, replacing any.
	Put(key string, e UsageCacheEntry)
}

// UsageCacheEntry is one cached bucket: its fees and the watermark they
// were computed at.
type UsageCacheEntry struct {
	// Fees are the bucket's fees.
	Fees []*invoice.Fee
	// CachedAt is the ingestion watermark of the events behind Fees
	// (lago cached_at).
	CachedAt time.Time
	// ExpiresAt is when the entry stops being used: the end of the
	// charges period.
	ExpiresAt time.Time
}

// MemoryUsageCache is the in-process [UsageCache]: a bounded map that
// evicts the least recently used entry when full.
type MemoryUsageCache struct {
	mu      sync.Mutex
	max     int
	order   *list.List
	entries map[string]*list.Element
}

var _ UsageCache = (*MemoryUsageCache)(nil)

type usageCacheItem struct {
	key   string
	entry UsageCacheEntry
}

// NewMemoryUsageCache returns a cache of at most maxEntries entries; nil
// when maxEntries is not positive, which turns the cache off.
func NewMemoryUsageCache(maxEntries int) *MemoryUsageCache {
	if maxEntries <= 0 {
		return nil
	}
	return &MemoryUsageCache{max: maxEntries, order: list.New(), entries: map[string]*list.Element{}}
}

// Get implements [UsageCache].
func (c *MemoryUsageCache) Get(key string, now time.Time) (UsageCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return UsageCacheEntry{}, false
	}
	it := el.Value.(*usageCacheItem)
	if !now.Before(it.entry.ExpiresAt) {
		c.order.Remove(el)
		delete(c.entries, key)
		return UsageCacheEntry{}, false
	}
	c.order.MoveToFront(el)
	return cloneUsageEntry(it.entry), true
}

// Put implements [UsageCache].
func (c *MemoryUsageCache) Put(key string, e UsageCacheEntry) {
	e = cloneUsageEntry(e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*usageCacheItem).entry = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&usageCacheItem{key: key, entry: e})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.entries, last.Value.(*usageCacheItem).key)
	}
}

// Len returns the number of entries.
func (c *MemoryUsageCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func cloneUsageEntry(e UsageCacheEntry) UsageCacheEntry {
	fees := make([]*invoice.Fee, len(e.Fees))
	for i, f := range e.Fees {
		cp := invoice.CloneFee(*f)
		fees[i] = &cp
	}
	e.Fees = fees
	return e
}
