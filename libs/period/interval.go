package period

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// Interval is the length of a plan's billing period.
type Interval int

// Billing intervals.
const (
	Weekly Interval = iota + 1
	Monthly
	Quarterly
	Semiannual
	Yearly
)

var intervalNames = map[Interval]string{
	Weekly:     "weekly",
	Monthly:    "monthly",
	Quarterly:  "quarterly",
	Semiannual: "semiannual",
	Yearly:     "yearly",
}

// ParseInterval parses an interval name such as "monthly".
func ParseInterval(s string) (Interval, error) {
	for i, name := range intervalNames {
		if name == s {
			return i, nil
		}
	}
	return 0, fmt.Errorf("period: unknown interval %q", s)
}

// Valid reports whether i is one of the defined intervals.
func (i Interval) Valid() bool {
	_, ok := intervalNames[i]
	return ok
}

// String returns the interval name, such as "monthly".
func (i Interval) String() string {
	if name, ok := intervalNames[i]; ok {
		return name
	}
	return fmt.Sprintf("Interval(%d)", int(i))
}

// MarshalText implements [encoding.TextMarshaler].
func (i Interval) MarshalText() ([]byte, error) {
	if !i.Valid() {
		return nil, fmt.Errorf("period: invalid interval %d", int(i))
	}
	return []byte(i.String()), nil
}

// UnmarshalText implements [encoding.TextUnmarshaler].
func (i *Interval) UnmarshalText(b []byte) error {
	v, err := ParseInterval(string(b))
	if err != nil {
		return err
	}
	*i = v
	return nil
}

// PeriodsPerYear returns how many periods of i make a year: 52 weeks, 12
// months, 4 quarters, 2 half years or 1 year.
func (i Interval) PeriodsPerYear() int64 {
	switch i {
	case Weekly:
		return 52
	case Monthly:
		return 12
	case Quarterly:
		return 4
	case Semiannual:
		return 2
	case Yearly:
		return 1
	}
	return 0
}

// splitsMonthly reports whether charges of a plan with interval i may be
// billed every month instead of every period.
func (i Interval) splitsMonthly() bool { return i == Yearly || i == Semiannual }

// BillingTime says how billing periods are aligned.
type BillingTime int

// Billing times.
const (
	// Calendar periods start on calendar boundaries: Monday, the first of
	// the month, of the quarter, of the half year or of the year.
	Calendar BillingTime = iota + 1
	// Anniversary periods start on the anniversary of the subscription
	// date: same weekday, same day of the month (clamped to the month's
	// length), same month of the quarter, half year or year.
	Anniversary
)

// ParseBillingTime parses "calendar" or "anniversary".
func ParseBillingTime(s string) (BillingTime, error) {
	switch s {
	case "calendar":
		return Calendar, nil
	case "anniversary":
		return Anniversary, nil
	}
	return 0, fmt.Errorf("period: unknown billing time %q", s)
}

// Valid reports whether b is one of the defined billing times.
func (b BillingTime) Valid() bool { return b == Calendar || b == Anniversary }

// String returns "calendar" or "anniversary".
func (b BillingTime) String() string {
	switch b {
	case Calendar:
		return "calendar"
	case Anniversary:
		return "anniversary"
	}
	return fmt.Sprintf("BillingTime(%d)", int(b))
}

// MarshalText implements [encoding.TextMarshaler].
func (b BillingTime) MarshalText() ([]byte, error) {
	if !b.Valid() {
		return nil, fmt.Errorf("period: invalid billing time %d", int(b))
	}
	return []byte(b.String()), nil
}

// UnmarshalText implements [encoding.TextUnmarshaler].
func (b *BillingTime) UnmarshalText(text []byte) error {
	v, err := ParseBillingTime(string(text))
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// Change describes how a subscription is replaced by the one following it.
type Change int

// Plan changes.
const (
	// NoChange means the subscription has no next subscription.
	NoChange Change = iota
	// Upgrade means the next subscription's plan costs as much or more per
	// year; it starts immediately.
	Upgrade
	// Downgrade means the next subscription's plan costs less per year; it
	// starts at the end of the current period.
	Downgrade
)

// String returns "none", "upgrade" or "downgrade".
func (c Change) String() string {
	switch c {
	case NoChange:
		return "none"
	case Upgrade:
		return "upgrade"
	case Downgrade:
		return "downgrade"
	}
	return fmt.Sprintf("Change(%d)", int(c))
}

// YearlyAmount returns amount, billed once every interval, brought to a year
// (a weekly amount times 52). It only serves to compare plans of different
// intervals.
func YearlyAmount(interval Interval, amount money.Decimal) money.Decimal {
	return amount.Mul(money.NewFromInt(interval.PeriodsPerYear()))
}

// Compare classifies moving from a plan billed fromAmount every fromInterval
// to one billed toAmount every toInterval: a [Downgrade] when the new plan
// costs strictly less per year, an [Upgrade] otherwise.
func Compare(fromInterval Interval, fromAmount money.Decimal, toInterval Interval, toAmount money.Decimal) Change {
	if YearlyAmount(fromInterval, fromAmount).GreaterThan(YearlyAmount(toInterval, toAmount)) {
		return Downgrade
	}
	return Upgrade
}
