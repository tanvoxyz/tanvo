package period

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Mode selects which period [Subscription.Dates] resolves.
type Mode int

// Modes.
const (
	// Billing resolves the period an invoice issued at the given instant
	// covers: for a fee paid in arrears, the period that just ended.
	Billing Mode = iota
	// CurrentUsage resolves the period the given instant falls in, as for
	// current usage, pay-in-advance charges and progressive billing.
	CurrentUsage
)

// maxTimezoneShift bounds the distance between two places' clocks; a
// previous invoice further away is not the one preceding the period.
const maxTimezoneShift = 26 * time.Hour

// Dates resolves the billing periods of a subscription for one billing run.
// It is created by [Subscription.Dates] and is immutable.
type Dates struct {
	sub          Subscription
	interval     Interval
	loc          *time.Location
	billingAt    time.Time
	currentUsage bool
	billingDate  date      // the day of billingAt in loc
	anchor       time.Time // SubscriptionAt in loc
}

// Dates returns the billing periods of s for a run at billingAt.
//
// In [Billing] mode billingAt is usually the first instant after the period
// being invoiced (its next billing day), or the termination instant of a
// terminated subscription. In [CurrentUsage] mode it is any instant of the
// period to resolve.
func (s Subscription) Dates(billingAt time.Time, mode Mode) (*Dates, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	loc := s.location()
	return &Dates{
		sub:          s,
		interval:     s.Plan.Interval,
		loc:          loc,
		billingAt:    billingAt,
		currentUsage: mode == CurrentUsage,
		billingDate:  dateOf(billingAt.In(loc)),
		anchor:       s.SubscriptionAt.In(loc),
	}, nil
}

// monthly returns the monthly view of a yearly or semiannual subscription,
// which bounds its monthly charges.
func (d *Dates) monthly() *Dates {
	m := *d
	m.interval = Monthly
	return &m
}

func (d *Dates) plan() Plan        { return d.sub.Plan }
func (d *Dates) calendar() bool    { return d.sub.BillingTime == Calendar }
func (d *Dates) anniversary() bool { return d.sub.BillingTime == Anniversary }
func (d *Dates) anchorDay() int    { return d.anchor.Day() }

// terminatedAt reports whether the subscription is terminated as of the run.
func (d *Dates) terminatedAt() bool { return d.sub.TerminatedBy(d.billingAt) }

// terminatedInArrears reports whether the run closes a subscription paid in
// arrears that is terminated without a downgrade: it bills the current
// period, not the previous one.
func (d *Dates) terminatedInArrears() bool {
	return d.terminatedAt() && !d.plan().PayInAdvance && d.sub.Next != Downgrade
}

// terminatedForGood reports whether the run closes a subscription terminated
// without a next subscription: charges are billed for the current period.
func (d *Dates) terminatedForGood() bool { return d.terminatedAt() && d.sub.Next == NoChange }

// SubscriptionPeriod returns the period the subscription fee of this run
// covers. It starts no earlier than the day the subscription started and
// ends no later than its termination. ok is false while the subscription has
// not started.
func (d *Dates) SubscriptionPeriod() (p Period, ok bool) {
	if !d.sub.Started() {
		return Period{}, false
	}
	from := d.fromDate().start(d.loc)
	if from.Before(d.sub.StartedAt) {
		from = dateOf(d.sub.StartedAt.In(d.loc)).start(d.loc)
	}

	to := d.toDate(d.fromDate()).end(d.loc)
	if d.terminatedAt() && to.After(d.sub.TerminatedAt) {
		to = d.sub.TerminatedAt
	}
	if to.Before(d.sub.StartedAt) {
		to = d.sub.StartedAt
	}
	return Period{from.UTC(), to.UTC()}, true
}

// ChargesPeriod returns the period the usage charges of this run cover. ok is
// false while the subscription has not started, and on runs of a yearly or
// semiannual plan that bill only its monthly fixed charges.
func (d *Dates) ChargesPeriod() (p Period, ok bool) {
	if !d.sub.Started() || !d.billsCharges() {
		return Period{}, false
	}
	var previousTo time.Time
	if d.sub.Previous != nil {
		previousTo = d.sub.Previous.ChargesTo
	}
	from := d.chargesFromDate()
	return d.chargePeriod(from, d.chargesToDate(from), previousTo), true
}

// FixedChargesPeriod returns the period the fixed charges of this run cover.
// ok is false while the subscription has not started, and on runs of a
// yearly or semiannual plan that bill only its monthly usage charges.
func (d *Dates) FixedChargesPeriod() (p Period, ok bool) {
	if !d.sub.Started() || !d.billsFixedCharges() {
		return Period{}, false
	}
	var previousTo time.Time
	if d.sub.Previous != nil {
		previousTo = d.sub.Previous.FixedChargesTo
	}
	from := d.fixedChargesFromDate()
	return d.chargePeriod(from, d.fixedChargesToDate(from), previousTo), true
}

// FixedChargesPeriodEnd returns the end of the current fixed charges period
// even on runs that do not bill fixed charges, for scheduling a change at the
// end of that period. ok is false while the subscription has not started.
func (d *Dates) FixedChargesPeriodEnd() (end time.Time, ok bool) {
	if !d.sub.Started() {
		return time.Time{}, false
	}
	return d.chargeEnd(d.fixedChargesToDate(d.fixedChargesFromDate())), true
}

func (d *Dates) chargePeriod(fromDate, toDate date, previousTo time.Time) Period {
	from := fromDate.start(d.loc)
	// When the location changed since the previous invoice, the new period
	// starts where the previous one ended, so that no event is counted
	// twice or missed.
	if d.timezoneChanged() && !previousTo.IsZero() {
		if delta := from.Sub(previousTo); delta.Abs() < maxTimezoneShift {
			from = previousTo
		}
	}
	if from.Before(d.sub.StartedAt) {
		from = d.sub.StartedAt
	}
	return Period{from.UTC(), d.chargeEnd(toDate)}
}

func (d *Dates) chargeEnd(toDate date) time.Time {
	to := toDate.end(d.loc)
	if d.sub.Terminated() && d.sub.TerminatedAt.Before(to) {
		to = d.sub.TerminatedAt
	}
	if to.Before(d.sub.StartedAt) {
		to = d.sub.StartedAt
	}
	return to.UTC()
}

func (d *Dates) timezoneChanged() bool {
	prev := d.sub.Previous
	return prev != nil && orUTC(prev.Location).String() != d.loc.String()
}

// Boundaries gathers the periods of one billing run.
type Boundaries struct {
	// Subscription is the period of the subscription fee.
	Subscription Period
	// Charges is the period of usage charges; zero when ChargesBilled is
	// false.
	Charges Period
	// ChargesBilled is false on runs that bill no usage charges.
	ChargesBilled bool
	// FixedCharges is the period of fixed charges; zero when
	// FixedChargesBilled is false.
	FixedCharges Period
	// FixedChargesBilled is false on runs that bill no fixed charges.
	FixedChargesBilled bool
	// ChargesDays is the length in days of the whole charges period
	// [Charges] belongs to.
	ChargesDays int
	// FixedChargesDays is the length in days of the whole fixed charges
	// period [FixedCharges] belongs to.
	FixedChargesDays int
}

// Boundaries returns every period of the run. ok is false while the
// subscription has not started.
func (d *Dates) Boundaries() (b Boundaries, ok bool) {
	if b.Subscription, ok = d.SubscriptionPeriod(); !ok {
		return Boundaries{}, false
	}
	b.Charges, b.ChargesBilled = d.ChargesPeriod()
	b.FixedCharges, b.FixedChargesBilled = d.FixedChargesPeriod()
	b.ChargesDays = d.ChargesDays()
	b.FixedChargesDays = d.FixedChargesDays()
	return b, true
}

// CurrentPeriodEnd returns the end of the subscription period holding the day
// of the run (its local midnight); a downgrade takes effect then.
func (d *Dates) CurrentPeriodEnd() time.Time {
	return d.nextEndOfPeriod().end(d.loc).UTC()
}

// PeriodEnd returns the end of the subscription period of the run, before it
// is cut by a termination: the instant the next period starts.
func (d *Dates) PeriodEnd() time.Time {
	return d.toDate(d.fromDate()).end(d.loc).UTC()
}

// PreviousPeriodStart returns the start of the subscription period before the
// one holding the day of the run (in [Billing] mode; in [CurrentUsage] mode,
// of the one holding it).
func (d *Dates) PreviousPeriodStart() time.Time {
	return d.periodStart(d.baseDate()).start(d.loc).UTC()
}

// CurrentPeriodStart returns the start of the subscription period holding the
// day of the run.
func (d *Dates) CurrentPeriodStart() time.Time {
	return d.periodStart(d.billingDate).start(d.loc).UTC()
}

// PeriodDays returns the length in days of the whole subscription period of
// the run. It is the divisor of a prorated subscription fee.
func (d *Dates) PeriodDays() int { return d.duration(d.fromDate()) }

// PeriodDaysAt returns the length in days of the whole subscription period
// holding the day of t. A subscription created by an upgrade starts in the
// middle of a period and is still prorated on the whole period.
func (d *Dates) PeriodDaysAt(t time.Time) int { return d.duration(dateOf(t.In(d.loc))) }

// ChargesDays returns the length in days of the whole charges period of the
// run.
func (d *Dates) ChargesDays() int {
	if d.interval.splitsMonthly() && d.plan().BillChargesMonthly {
		return d.monthly().duration(d.chargesFromDate())
	}
	return d.duration(d.chargesFromDate())
}

// FixedChargesDays returns the length in days of the whole fixed charges
// period of the run.
func (d *Dates) FixedChargesDays() int {
	if d.interval.splitsMonthly() && d.plan().BillFixedChargesMonthly {
		return d.monthly().duration(d.fixedChargesFromDate())
	}
	return d.duration(d.fixedChargesFromDate())
}

// FirstMonthOfPeriod reports whether the run falls in the first month of a
// yearly or semiannual subscription period. It is false for other intervals.
func (d *Dates) FirstMonthOfPeriod() bool {
	switch d.interval {
	case Yearly:
		if d.calendar() {
			return d.billingDate.month == time.January
		}
		return d.monthly().periodStart(d.billingDate).month == d.anchor.Month()
	case Semiannual:
		if d.calendar() {
			return d.billingDate.month == time.January || d.billingDate.month == time.July
		}
		start := d.anchor.Month()
		half := start + 6
		if start > 6 {
			half = start - 6
		}
		m := d.monthly().periodStart(d.billingDate).month
		return m == start || m == half
	}
	return false
}

// FirstMonthOfFirstPeriod reports whether the run falls in the first month of
// the first yearly or semiannual subscription period. It is false for other
// intervals.
func (d *Dates) FirstMonthOfFirstPeriod() bool {
	switch d.interval {
	case Yearly:
		if d.calendar() {
			return d.billingDate.month == time.January && d.billingDate.year == d.anchor.Year()
		}
	case Semiannual:
		if d.calendar() {
			jan := d.billingDate.month == time.January || d.billingDate.month == time.July
			return jan && d.billingDate.year == d.anchor.Year()
		}
	default:
		return false
	}
	start := d.monthly().periodStart(d.billingDate)
	return start.month == d.anchor.Month() && start.year == d.anchor.Year()
}

// billsCharges reports whether the run bills usage charges: always, except on
// a yearly or semiannual plan billing only fixed charges monthly, outside the
// first month of its period.
func (d *Dates) billsCharges() bool {
	if !d.interval.splitsMonthly() || d.currentUsage || d.plan().BillChargesMonthly {
		return true
	}
	if d.plan().BillFixedChargesMonthly {
		return d.FirstMonthOfPeriod()
	}
	return true
}

// billsFixedCharges reports whether the run bills fixed charges: always,
// except on a yearly or semiannual plan billing only usage charges monthly,
// outside the first month of its period.
func (d *Dates) billsFixedCharges() bool {
	if !d.interval.splitsMonthly() || d.plan().BillFixedChargesMonthly {
		return true
	}
	if d.plan().BillChargesMonthly {
		return d.FirstMonthOfPeriod()
	}
	return true
}

// baseDate is a day of the period billed by the run: the day of the run in
// current usage mode, the same day one period earlier otherwise.
func (d *Dates) baseDate() date {
	if d.currentUsage {
		return d.billingDate
	}
	if d.interval == Weekly {
		return d.billingDate.addDays(-7)
	}
	months := monthsIn(d.interval)
	b := d.billingDate
	prev := b.addMonths(-months)
	// On the last day of a month shorter than the anniversary day, the
	// period being billed opened on the anniversary day of the earlier month
	// (or on its last day if it is shorter too).
	if d.anniversary() && b.lastDayOfMonth() && b.day < d.anchorDay() {
		end := prev.endOfMonth()
		if end.day >= d.anchorDay() {
			end.day = d.anchorDay()
		}
		return end
	}
	return prev
}

// beginningOf returns the first day of the calendar period holding day.
func (d *Dates) beginningOf(day date) date {
	if d.interval == Weekly {
		return day.beginningOfWeek()
	}
	return day.beginningOfMonths(monthsIn(d.interval))
}

// endOf returns the last day of the calendar period holding day.
func (d *Dates) endOf(day date) date {
	if d.interval == Weekly {
		return day.endOfWeek()
	}
	return day.endOfMonths(monthsIn(d.interval))
}

// fromDate is the first day of the subscription period of the run.
func (d *Dates) fromDate() date { return d.fromDateOf(d.baseDate()) }

func (d *Dates) fromDateOf(day date) date {
	if d.plan().PayInAdvance || d.terminatedInArrears() {
		day = d.billingDate
	}
	if d.anniversary() {
		return d.previousAnniversary(day)
	}
	return d.beginningOf(day)
}

// toDate is the last day of the subscription period starting on from.
func (d *Dates) toDate(from date) date {
	anchor := dateOf(d.anchor)
	switch d.interval {
	case Weekly:
		if d.calendar() {
			return from.endOfWeek()
		}
		return from.addDays(6)
	case Monthly:
		if d.calendar() || anchor.day == 1 {
			return from.endOfMonth()
		}
		next := from.addMonths(1)
		return d.dayBeforeAnniversary(buildDate(next.year, next.month, anchor.day-1))
	case Quarterly, Semiannual:
		n := monthsIn(d.interval)
		if d.calendar() || (anchor.day == 1 && anchor == anchor.beginningOfMonths(n)) {
			return from.endOfMonths(n)
		}
		next := from.addMonths(n)
		return buildDate(next.year, next.month, d.anniversaryDayIn(next.year, next.month)).addDays(-1)
	case Yearly:
		if d.calendar() || d.anchor.YearDay() == 1 {
			return from.endOfMonths(12)
		}
		return d.dayBeforeAnniversary(buildDate(from.year+1, from.month, anchor.day-1))
	}
	return from
}

// dayBeforeAnniversary moves a period end clamped to a month's last day one
// day back when the anniversary itself is clamped to that last day.
func (d *Dates) dayBeforeAnniversary(end date) date {
	if end.lastDayOfMonth() && d.anchorDay() > end.day {
		return end.addDays(-1)
	}
	return end
}

// anniversaryDayIn is the anniversary day in a month, clamped to its length.
func (d *Dates) anniversaryDayIn(year int, month time.Month) int {
	return min(d.anchorDay(), daysIn(year, month))
}

// chargesFromDate is the first day of the charges period of the run.
func (d *Dates) chargesFromDate() date {
	if d.interval.splitsMonthly() && d.plan().BillChargesMonthly {
		return d.monthly().chargesFromDate()
	}
	return d.ownChargesFromDate()
}

func (d *Dates) fixedChargesFromDate() date {
	if d.interval.splitsMonthly() && d.plan().BillFixedChargesMonthly {
		return d.monthly().chargesFromDate()
	}
	return d.ownChargesFromDate()
}

func (d *Dates) ownChargesFromDate() date {
	switch {
	case d.terminatedForGood():
		if d.anniversary() {
			return d.previousAnniversary(d.billingDate)
		}
		return d.beginningOf(d.billingDate)
	case !d.plan().PayInAdvance:
		return d.fromDate()
	case d.calendar():
		return d.beginningOf(d.baseDate())
	}
	return d.previousAnniversary(d.baseDate())
}

// chargesToDate is the last day of the charges period starting on from.
func (d *Dates) chargesToDate(from date) date {
	if d.interval.splitsMonthly() && d.plan().BillChargesMonthly {
		return d.monthly().ownChargesToDate(from)
	}
	return d.ownChargesToDate(from)
}

func (d *Dates) fixedChargesToDate(from date) date {
	if d.interval.splitsMonthly() && d.plan().BillFixedChargesMonthly {
		return d.monthly().ownChargesToDate(from)
	}
	return d.ownChargesToDate(from)
}

func (d *Dates) ownChargesToDate(from date) date {
	if d.calendar() {
		return d.endOf(from)
	}
	return d.toDate(from)
}

// nextEndOfPeriod is the last day of the subscription period holding the day
// of the run.
func (d *Dates) nextEndOfPeriod() date {
	b := d.billingDate
	if d.calendar() {
		return d.endOf(b)
	}
	switch d.interval {
	case Weekly:
		if b.weekday() == d.anchor.AddDate(0, 0, -1).Weekday() {
			return b
		}
		return b.nextOccurring(d.anchor.Weekday()).addDays(-1)
	case Monthly:
		end := buildDate(b.year, b.month, d.anchorDay()).addDays(-1)
		if !end.before(b) {
			return end
		}
		next := b.addMonths(1)
		return buildDate(next.year, next.month, d.anchorDay()).addDays(-1)
	}
	return d.toDate(d.previousAnniversary(b))
}

// periodStart is the first day of the subscription period holding day.
func (d *Dates) periodStart(day date) date {
	if d.calendar() {
		return d.beginningOf(day)
	}
	return d.previousAnniversary(day)
}

// duration is the length in days of the whole subscription period holding
// day.
func (d *Dates) duration(day date) int {
	switch d.interval {
	case Weekly:
		return 7
	case Yearly:
		if d.calendar() {
			return newDate(day.year, time.December, 31).sub(newDate(day.year, time.January, 1)) + 1
		}
	}
	start := d.periodStart(day)
	return d.toDate(start).sub(start) + 1
}

// previousAnniversary is the anniversary opening the period holding day.
func (d *Dates) previousAnniversary(day date) date {
	switch d.interval {
	case Weekly:
		if day.weekday() == d.anchor.Weekday() {
			return day
		}
		return day.prevOccurring(d.anchor.Weekday())
	case Monthly:
		anniversaryDay := d.anchorDay()
		if d.anniversary() && day.lastDayOfMonth() && day.day < anniversaryDay {
			anniversaryDay = day.day
		}
		if day.day < anniversaryDay {
			prev := day.addMonths(-1)
			return buildDate(prev.year, prev.month, anniversaryDay)
		}
		return buildDate(day.year, day.month, anniversaryDay)
	case Quarterly, Semiannual:
		return d.previousMonthlyAnniversary(day, monthsIn(d.interval))
	case Yearly:
		year := day.year
		if d.periodStartedLastYear(day) {
			year--
		}
		return buildDate(year, d.anchor.Month(), d.anchorDay())
	}
	return day
}

// previousMonthlyAnniversary is the anniversary opening the quarter or half
// year (every n months) holding day.
func (d *Dates) previousMonthlyAnniversary(day date, n int) date {
	var months []time.Month
	for m := int(d.anchor.Month()); len(months) < 12/n; m += n {
		months = append(months, time.Month((m-1)%12+1))
	}
	slices.Sort(months)

	findPrevious := func() bool {
		if !slices.Contains(months, day.month) {
			return true
		}
		return day.day < d.anniversaryDayIn(day.year, day.month)
	}

	year, month := day.year, day.month
	switch {
	case day.month < months[0] || (day.month == months[0] && findPrevious()):
		year, month = day.year-1, months[len(months)-1]
	case findPrevious():
		for _, m := range months {
			if m < day.month {
				month = m
			}
		}
	}
	return buildDate(year, month, d.anniversaryDayIn(year, month))
}

// periodStartedLastYear reports whether the yearly period holding day opened
// in the previous calendar year.
func (d *Dates) periodStartedLastYear(day date) bool {
	m := d.anchor.Month()
	switch {
	case day.month < m:
		return true
	case day.month > m:
		return false
	}
	// A 29 February anchor has its anniversary on 28 February in a common
	// year.
	return day.day < d.anniversaryDayIn(day.year, m)
}

// DayPrice returns the price of one day of the subscription period of the
// run for a plan amount billed once per period.
func (d *Dates) DayPrice(amount money.Decimal) money.Decimal {
	return amount.Div(money.NewFromInt(int64(d.PeriodDays())))
}
