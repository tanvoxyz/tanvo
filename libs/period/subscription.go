package period

import (
	"errors"
	"fmt"
	"time"
)

// Plan holds the plan settings that shape billing periods.
type Plan struct {
	Interval Interval
	// PayInAdvance bills the subscription fee at the start of each period
	// instead of at its end.
	PayInAdvance bool
	// BillChargesMonthly bills usage charges every month on a yearly or
	// semiannual plan.
	BillChargesMonthly bool
	// BillFixedChargesMonthly bills fixed charges every month on a yearly or
	// semiannual plan.
	BillFixedChargesMonthly bool
	// TrialDays is the length of the free trial; 0 means no trial.
	TrialDays int
}

// ChargesBilledMonthly reports whether usage charges are billed in monthly
// periods inside a longer (yearly or semiannual) subscription period.
func (p Plan) ChargesBilledMonthly() bool {
	return p.BillChargesMonthly && p.Interval.splitsMonthly()
}

// FixedChargesBilledMonthly reports whether fixed charges are billed in
// monthly periods inside a longer (yearly or semiannual) subscription period.
func (p Plan) FixedChargesBilledMonthly() bool {
	return p.BillFixedChargesMonthly && p.Interval.splitsMonthly()
}

// HasTrial reports whether the plan has a free trial.
func (p Plan) HasTrial() bool { return p.TrialDays > 0 }

// Subscription holds the subscription facts that shape billing periods.
// Every instant may be in any location; Location decides how they map to
// days.
type Subscription struct {
	Plan        Plan
	BillingTime BillingTime
	// SubscriptionAt is the anchor of anniversary billing; on a subscription
	// that replaced another one it is inherited from it.
	SubscriptionAt time.Time
	// StartedAt is when the subscription became active; zero while it has
	// not started.
	StartedAt time.Time
	// TerminatedAt is when the subscription was terminated; zero while it is
	// not terminated.
	TerminatedAt time.Time
	// Next is how the subscription is replaced by its next subscription, if
	// it has one.
	Next Change
	// InitialStartedAt is the start of the first subscription of the chain
	// this one belongs to (the same external subscription); the free trial
	// runs from it. Zero means StartedAt.
	InitialStartedAt time.Time
	// TrialEndedAt is when the end of the trial was processed; zero while it
	// has not been.
	TrialEndedAt time.Time
	// Location is the time zone billing days are counted in (the customer's,
	// else the billing entity's); nil means UTC.
	Location *time.Location
	// Previous describes the last invoiced boundaries, used to keep charge
	// periods contiguous when Location changes; nil when nothing was
	// invoiced yet.
	Previous *PreviousInvoice
}

// PreviousInvoice holds the boundaries of the most recent invoice of a
// subscription.
type PreviousInvoice struct {
	// Location is the time zone the invoice was computed in.
	Location *time.Location
	// ChargesTo is the exclusive end of the invoiced charges period; zero if
	// none.
	ChargesTo time.Time
	// FixedChargesTo is the exclusive end of the invoiced fixed charges
	// period; zero if none.
	FixedChargesTo time.Time
}

// ErrInvalid is returned for a subscription that cannot be billed.
var ErrInvalid = errors.New("period: invalid subscription")

// Validate reports whether s has a known interval and billing time and a
// subscription date.
func (s Subscription) Validate() error {
	switch {
	case !s.Plan.Interval.Valid():
		return fmt.Errorf("%w: unknown interval %d", ErrInvalid, int(s.Plan.Interval))
	case !s.BillingTime.Valid():
		return fmt.Errorf("%w: unknown billing time %d", ErrInvalid, int(s.BillingTime))
	case s.SubscriptionAt.IsZero():
		return fmt.Errorf("%w: missing subscription date", ErrInvalid)
	case s.Plan.TrialDays < 0:
		return fmt.Errorf("%w: negative trial", ErrInvalid)
	}
	return nil
}

// Started reports whether the subscription has started.
func (s Subscription) Started() bool { return !s.StartedAt.IsZero() }

// Terminated reports whether the subscription is terminated.
func (s Subscription) Terminated() bool { return !s.TerminatedAt.IsZero() }

// TerminatedBy reports whether the subscription was terminated at or before t.
func (s Subscription) TerminatedBy(t time.Time) bool {
	return s.Terminated() && !s.TerminatedAt.After(t)
}

func (s Subscription) location() *time.Location { return orUTC(s.Location) }

func (s Subscription) initialStartedAt() time.Time {
	switch {
	case !s.InitialStartedAt.IsZero():
		return s.InitialStartedAt
	case s.Started():
		return s.StartedAt
	}
	return s.SubscriptionAt
}

// TrialEnd returns the instant the free trial ends: the plan's trial days
// after the start of the first subscription of the chain. ok is false when
// the plan has no trial.
func (s Subscription) TrialEnd() (end time.Time, ok bool) {
	if !s.Plan.HasTrial() {
		return time.Time{}, false
	}
	return s.initialStartedAt().UTC().AddDate(0, 0, s.Plan.TrialDays), true
}

// InTrial reports whether the subscription is in its free trial at now.
func (s Subscription) InTrial(now time.Time) bool {
	if !s.TrialEndedAt.IsZero() || s.initialStartedAt().After(now) {
		return false
	}
	end, ok := s.TrialEnd()
	return ok && end.After(now)
}

// TrialEndDue reports whether the end of the free trial is due at now and
// has not been processed yet.
func (s Subscription) TrialEndDue(now time.Time) bool {
	if !s.TrialEndedAt.IsZero() {
		return false
	}
	end, ok := s.TrialEnd()
	return ok && !end.After(now)
}

// BilledDays returns the number of days of [from, to) the subscription fee
// is billed for, see [DaysBetween]. The termination day of a subscription
// replaced by an upgrade is billed by the new subscription, so it is not
// counted here.
func (s Subscription) BilledDays(from, to time.Time) int {
	days := DaysBetween(from, to, s.location())
	if s.Terminated() && s.Next == Upgrade {
		days = max(days-1, 0)
	}
	return days
}

// IsBillingDay reports whether a billing period of the subscription (for the
// subscription fee, for monthly charges or for monthly fixed charges) rolls
// over on the day of at in the subscription's location.
func (s Subscription) IsBillingDay(at time.Time) bool {
	loc := s.location()
	today := dateOf(at.In(loc))
	anchor := dateOf(s.SubscriptionAt.In(loc))
	monthly := s.Plan.ChargesBilledMonthly() || s.Plan.FixedChargesBilledMonthly()

	if s.BillingTime == Calendar {
		switch s.Plan.Interval {
		case Weekly:
			return today.weekday() == time.Monday
		case Monthly:
			return today.day == 1
		case Quarterly, Semiannual, Yearly:
			if monthly {
				return today.day == 1
			}
			return today.day == 1 && today == today.beginningOfMonths(monthsIn(s.Plan.Interval))
		}
		return false
	}

	// The anniversary day of a month shorter than the anchor day is the
	// month's last day.
	anniversaryDay := today.day == anchor.day || (today.lastDayOfMonth() && anchor.day > today.day)
	switch s.Plan.Interval {
	case Weekly:
		return today.weekday() == anchor.weekday()
	case Monthly:
		return anniversaryDay
	case Quarterly, Semiannual:
		if monthly {
			return anniversaryDay
		}
		n := monthsIn(s.Plan.Interval)
		return (int(today.month)-int(anchor.month)+12)%n == 0 && anniversaryDay
	case Yearly:
		if monthly {
			return anniversaryDay
		}
		leap := today.month == time.February && today.day == 28 && today.lastDayOfMonth() && anchor.day == 29
		return today.month == anchor.month && (today.day == anchor.day || leap)
	}
	return false
}

// monthsIn returns the number of months of a period of i, or 0 for weekly.
func monthsIn(i Interval) int {
	switch i {
	case Monthly:
		return 1
	case Quarterly:
		return 3
	case Semiannual:
		return 6
	case Yearly:
		return 12
	}
	return 0
}
