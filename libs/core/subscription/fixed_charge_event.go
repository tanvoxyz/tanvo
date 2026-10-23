package subscription

import (
	"cmp"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// FixedChargeEvent is a change of the units of a fixed charge for one
// subscription (domain §7.5). At most one event per subscription, fixed
// charge and day is kept: a later one the same day replaces the earlier.
type FixedChargeEvent struct {
	// ID identifies the event.
	ID core.FixedChargeEventID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the subscription whose units change.
	SubscriptionID core.SubscriptionID
	// FixedChargeID is the fixed charge whose units change.
	FixedChargeID core.FixedChargeID
	// Units are the units in force from the event on (≥ 0).
	Units money.Decimal
	// Timestamp is the instant of the change.
	Timestamp time.Time
	// ApplyUnitsImmediately makes the change take effect at Timestamp
	// rather than at the start of the next billing period.
	ApplyUnitsImmediately bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the event's invariants.
func (e *FixedChargeEvent) Validate() error {
	var v core.Validator
	v.Check(!e.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Check(!e.FixedChargeID.IsZero(), "fixed_charge_id", core.CodeRequired)
	v.DecimalNonNegative("units", e.Units)
	v.Check(!e.Timestamp.IsZero(), "timestamp", core.CodeRequired)
	return v.Err()
}

// SameDay reports whether e and o concern the same subscription and fixed
// charge on the same day in loc (the customer's time zone).
func (e *FixedChargeEvent) SameDay(o *FixedChargeEvent, loc *time.Location) bool {
	return e.SubscriptionID == o.SubscriptionID && e.FixedChargeID == o.FixedChargeID &&
		core.DateOf(e.Timestamp, loc) == core.DateOf(o.Timestamp, loc)
}

// KeepLatestPerDay returns events ordered by timestamp, keeping only the
// latest one per subscription, fixed charge and day in loc. Events with
// equal timestamps keep the one that comes last in the input.
func KeepLatestPerDay(events []*FixedChargeEvent, loc *time.Location) []*FixedChargeEvent {
	sorted := slices.Clone(events)
	slices.SortStableFunc(sorted, func(a, b *FixedChargeEvent) int { return a.Timestamp.Compare(b.Timestamp) })
	out := make([]*FixedChargeEvent, 0, len(sorted))
	for _, e := range sorted {
		if i := slices.IndexFunc(out, func(o *FixedChargeEvent) bool { return o.SameDay(e, loc) }); i >= 0 {
			out = slices.Delete(out, i, i+1)
		}
		out = append(out, e)
	}
	slices.SortStableFunc(out, func(a, b *FixedChargeEvent) int {
		return cmp.Or(a.Timestamp.Compare(b.Timestamp), a.ID.Compare(b.ID))
	})
	return out
}

// UnitsAt returns the units in force at t among events of one subscription
// and fixed charge: those of the latest immediate event at or before t, or
// of a deferred event whose effective instant (effectiveAt, the start of
// the billing period following its timestamp) is at or before t. It
// returns ok = false when none is in force (the fixed charge's own units
// apply).
func UnitsAt(events []*FixedChargeEvent, t time.Time, effectiveAt func(time.Time) time.Time) (money.Decimal, bool) {
	var best time.Time
	var units money.Decimal
	ok := false
	for _, e := range events {
		at := e.Timestamp
		if !e.ApplyUnitsImmediately {
			at = effectiveAt(e.Timestamp)
		}
		if at.After(t) || (ok && at.Before(best)) {
			continue
		}
		best, units, ok = at, e.Units, true
	}
	return units, ok
}
