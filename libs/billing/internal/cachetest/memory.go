package cachetest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Memory is a map-based [port.Cache]. Several billing caches given the
// same Memory share it, as server replicas share Redis. It is safe for
// concurrent use.
type Memory struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]entry
}

var _ port.Cache = (*Memory)(nil)

type entry struct {
	value   []byte
	expires time.Time
}

// New returns an empty cache whose entries expire against now; nil uses
// time.Now.
func New(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, entries: map[string]entry{}}
}

// Get implements [port.Cache].
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	if !m.now().Before(e.expires) {
		delete(m.entries, key)
		return nil, false
	}
	return slices.Clone(e.value), true
}

// Set implements [port.Cache].
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = entry{value: slices.Clone(value), expires: m.now().Add(ttl)}
}

// Delete implements [port.Cache].
func (m *Memory) Delete(_ context.Context, keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.entries, k)
	}
}

// Len returns the number of entries not expired.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	n := 0
	for _, e := range m.entries {
		if now.Before(e.expires) {
			n++
		}
	}
	return n
}
