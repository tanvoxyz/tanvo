package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// Memory is an in-process [port.Cache]: a least-recently-used cache of at
// most a fixed number of entries, each with its own expiry. It is safe for
// concurrent use.
//
// A nil *Memory is a valid cache that stores nothing: every Get misses.
type Memory struct {
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[string]*list.Element
	// lru orders the entries from the most (front) to the least (back)
	// recently used.
	lru list.List
}

// memoryEntry is one entry of a [Memory] cache.
type memoryEntry struct {
	key     string
	value   []byte
	expires time.Time
}

// MemoryOption configures a [Memory] cache.
type MemoryOption func(*Memory)

// WithClock sets the clock that expires entries, time.Now by default; tests
// use it to move time forward.
func WithClock(now func() time.Time) MemoryOption {
	return func(m *Memory) {
		if now != nil {
			m.now = now
		}
	}
}

// NewMemory returns an in-process cache of at most maxEntries entries; when
// it is full, a Set evicts the least recently used entry. A maxEntries that
// is not positive returns nil, a cache that stores nothing.
func NewMemory(maxEntries int, opts ...MemoryOption) *Memory {
	if maxEntries <= 0 {
		return nil
	}
	m := &Memory{
		max:     maxEntries,
		now:     time.Now,
		entries: make(map[string]*list.Element, min(maxEntries, 1024)),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Get returns a copy of the value of key, false when there is none or it
// has expired.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	if m == nil {
		return nil, false
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*memoryEntry)
	if !now.Before(e.expires) {
		m.remove(el)
		return nil, false
	}
	m.lru.MoveToFront(el)
	return clone(e.value), true
}

// Set stores a copy of value under key for ttl, replacing any; a ttl that
// is not positive stores nothing and leaves any previous value in place.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	if m == nil || ttl <= 0 {
		return
	}
	v := clone(value)
	expires := m.now().Add(ttl)
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		e := el.Value.(*memoryEntry)
		e.value, e.expires = v, expires
		m.lru.MoveToFront(el)
		return
	}
	for m.lru.Len() >= m.max {
		m.remove(m.lru.Back())
	}
	m.entries[key] = m.lru.PushFront(&memoryEntry{key: key, value: v, expires: expires})
}

// Delete removes the keys; missing keys are ignored.
func (m *Memory) Delete(_ context.Context, keys ...string) error {
	if m == nil || len(keys) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		if el, ok := m.entries[k]; ok {
			m.remove(el)
		}
	}
	return nil
}

// Len returns the number of entries, expired ones that no Get has removed
// yet included.
func (m *Memory) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lru.Len()
}

// remove removes an entry; m.mu is held.
func (m *Memory) remove(el *list.Element) {
	delete(m.entries, el.Value.(*memoryEntry).key)
	m.lru.Remove(el)
}

// clone returns a copy of b that is never nil, so that an empty value
// reads back as a hit.
func clone(b []byte) []byte {
	return append(make([]byte, 0, len(b)), b...)
}
