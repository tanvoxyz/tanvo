package period

import "time"

// TerminationDates returns the periods to bill when the subscription is
// terminated at timestamp.
//
// A subscription terminated without a next subscription bills its current
// period. When it is terminated on the first day after a period ended, that
// period may not have been invoiced yet, because the billing run had not
// happened. In that case TerminationDates returns the periods of that ended
// period instead, and adjusted is true. The caller must check that no
// invoice already covers those charge boundaries. If one does, it must use
// the regular dates instead, which are [Subscription.Dates] at timestamp in
// [Billing] mode.
func (s Subscription) TerminationDates(timestamp time.Time) (d *Dates, adjusted bool, err error) {
	regular, err := s.Dates(timestamp, Billing)
	if err != nil {
		return nil, false, err
	}
	if !s.Terminated() || s.Next != NoChange || !s.Started() {
		return regular, false, nil
	}
	dayBefore := timestamp.Add(-24 * time.Hour)
	if dayBefore.Before(s.StartedAt) {
		return regular, false, nil
	}

	active := s
	active.TerminatedAt = time.Time{}
	probe, err := active.Dates(dayBefore, CurrentUsage)
	if err != nil {
		return nil, false, err
	}
	if !justEnded(timestamp, probe.chargeEndOf(probe.chargesToDate(probe.chargesFromDate()))) {
		return regular, false, nil
	}
	if probe.interval.splitsMonthly() && probe.plan().BillFixedChargesMonthly {
		if !justEnded(timestamp, probe.chargeEndOf(probe.fixedChargesToDate(probe.fixedChargesFromDate()))) {
			return regular, false, nil
		}
	}

	ended, err := active.Dates(timestamp, Billing)
	if err != nil {
		return nil, false, err
	}
	return ended, true, nil
}

// justEnded reports whether a period ending at end closed less than a day
// before t.
func justEnded(t, end time.Time) bool {
	return !t.Before(end) && t.Sub(end) < 24*time.Hour
}

// chargeEndOf is the end of a charges period ending on toDate, before it is
// cut by a termination.
func (d *Dates) chargeEndOf(toDate date) time.Time { return toDate.end(d.loc).UTC() }
