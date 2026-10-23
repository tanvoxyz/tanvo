package billingtest

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Clock is a settable [core.Clock] for tests.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock returns a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{t: t.UTC()} }

// Now returns the clock's instant.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC()
}

// Advance moves the clock by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// IDs generates increasing UUIDv7-shaped identifiers, so creation order is
// identifier order and runs are reproducible.
type IDs struct {
	mu sync.Mutex
	n  uint64
}

// NewUUID returns the next identifier.
func (g *IDs) NewUUID() uuid.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	var u uuid.UUID
	for i := range 8 {
		u[15-i] = byte(g.n >> (8 * i))
	}
	u[0] = 0x01
	u[6] = 0x70 // version 7
	u[8] = 0x80 // RFC 4122 variant
	return u
}
