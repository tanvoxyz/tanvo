package billingtest

import (
	"slices"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Events is a [metering.Store] that accepts new events: it rebuilds the
// immutable [metering.MemoryStore] on every addition. It is not safe for
// concurrent additions.
type Events struct {
	*metering.MemoryStore

	all []metering.Event
}

// NewEvents returns an empty event store.
func NewEvents() *Events { return &Events{MemoryStore: metering.NewMemoryStore(nil)} }

// Add stores events.
func (s *Events) Add(events ...metering.Event) {
	s.all = append(s.all, events...)
	s.MemoryStore = metering.NewMemoryStore(s.all)
}

// All returns the stored events in insertion order.
func (s *Events) All() []metering.Event { return slices.Clone(s.all) }
