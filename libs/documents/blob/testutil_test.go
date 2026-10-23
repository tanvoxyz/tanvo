package blob

import (
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// testClock is a settable clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testOrgs(t *testing.T) (a, b core.OrganizationID) {
	t.Helper()
	return core.MustParseID[core.OrganizationID]("0190f3a8-0000-7000-8000-00000000000a"),
		core.MustParseID[core.OrganizationID]("0190f3a8-0000-7000-8000-00000000000b")
}
