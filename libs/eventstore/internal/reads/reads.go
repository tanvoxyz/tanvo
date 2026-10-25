// Package reads throttles the records of the reads served from usage
// bucket definitions, so that the stores record a definition's read at most
// once per [metering.BucketReadResolution] and process.
package reads

import (
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Throttle remembers when each definition's read was last recorded. The
// zero value is ready.
type Throttle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// key identifies a definition.
func key(d metering.BucketDefinition) string {
	return d.OrganizationID + "\x00" + d.Code + "\x00" + d.ID()
}

// Due reports whether the read of d at now is to be recorded, and if so
// remembers it as recorded.
func (t *Throttle) Due(d metering.BucketDefinition, now time.Time) bool {
	k := key(d)
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[k]; ok && now.Sub(last) < metering.BucketReadResolution && !now.Before(last) {
		return false
	}
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	t.last[k] = now
	return true
}

// Forget forgets d, dropped: its next read after a new definition is
// recorded.
func (t *Throttle) Forget(d metering.BucketDefinition) {
	t.mu.Lock()
	delete(t.last, key(d))
	t.mu.Unlock()
}
