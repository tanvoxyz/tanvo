// Package schedule is the billing math of subscriptions priced by rate
// cards (domain §21.4): intervals, the calendar of a rate card, day
// counting, phases, the rate segments of a cycle, billing terms, the cycle
// walker and the schedule of billable segments.
//
// It adopts the v2 calendar rules: every window is half-open [From, To),
// boundaries sit at local midnight in the customer's time zone and are
// derived from one anchor (so month ends clamp without drifting), and a
// day belongs to the window that holds its local midnight, so the shares
// of a cut window always add up to the whole.
//
// Computing a schedule is pure: no I/O and no clock. Instants are compared
// as instants; returned instants carry the schedule's location.
package schedule
