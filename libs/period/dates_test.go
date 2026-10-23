package period

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// datesCase is a subscription, a billing run and the expected results.
type datesCase struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`

	Interval                string `yaml:"interval"`
	BillingTime             string `yaml:"billing_time"`
	PayInAdvance            bool   `yaml:"pay_in_advance"`
	BillChargesMonthly      bool   `yaml:"bill_charges_monthly"`
	BillFixedChargesMonthly bool   `yaml:"bill_fixed_charges_monthly"`
	SubscriptionAt          ts     `yaml:"subscription_at"`
	// StartedAt defaults to SubscriptionAt; null means not started.
	StartedAt    ts     `yaml:"started_at"`
	TerminatedAt ts     `yaml:"terminated_at"`
	Next         string `yaml:"next"`
	Timezone     string `yaml:"timezone"`
	Previous     *struct {
		Timezone       string `yaml:"timezone"`
		ChargesTo      ts     `yaml:"charges_to"`
		FixedChargesTo ts     `yaml:"fixed_charges_to"`
	} `yaml:"previous"`

	BillingAt    ts   `yaml:"billing_at"`
	CurrentUsage bool `yaml:"current_usage"`
	// Termination runs [Subscription.TerminationDates] at BillingAt.
	Termination bool `yaml:"termination"`
	// PeriodDaysAt is the argument of the period_days_at result.
	PeriodDaysAt ts `yaml:"period_days_at"`
	// DayPriceOf is the amount of the day_price result.
	DayPriceOf string `yaml:"day_price_of"`

	// Want maps a result name (see [datesResult]) to its expected value;
	// null means the result is not available.
	Want map[string]any `yaml:"want"`
}

func (c datesCase) subscription(t *testing.T) Subscription {
	t.Helper()
	interval, err := ParseInterval(c.Interval)
	if err != nil && c.Interval != "invalid" {
		t.Fatal(err)
	}
	billingTime := Calendar
	if c.BillingTime != "" {
		if billingTime, err = ParseBillingTime(c.BillingTime); err != nil {
			t.Fatal(err)
		}
	}
	s := Subscription{
		Plan: Plan{
			Interval:                interval,
			PayInAdvance:            c.PayInAdvance,
			BillChargesMonthly:      c.BillChargesMonthly,
			BillFixedChargesMonthly: c.BillFixedChargesMonthly,
		},
		BillingTime:    billingTime,
		SubscriptionAt: c.SubscriptionAt.Time,
		StartedAt:      c.StartedAt.Time,
		TerminatedAt:   c.TerminatedAt.Time,
		Location:       loadLocation(t, c.Timezone),
	}
	switch c.Next {
	case "", "none":
	case "upgrade":
		s.Next = Upgrade
	case "downgrade":
		s.Next = Downgrade
	default:
		t.Fatalf("bad next %q", c.Next)
	}
	if p := c.Previous; p != nil {
		s.Previous = &PreviousInvoice{
			Location:       loadLocation(t, p.Timezone),
			ChargesTo:      p.ChargesTo.Time,
			FixedChargesTo: p.FixedChargesTo.Time,
		}
	}
	return s
}

// datesResult returns the named result of d; ok is false when it is not
// available.
func datesResult(c datesCase, d *Dates, adjusted bool, name string) (v any, ok bool) {
	switch name {
	case "from":
		p, ok := d.SubscriptionPeriod()
		return p.From, ok
	case "to":
		p, ok := d.SubscriptionPeriod()
		return p.To, ok
	case "charges_from":
		p, ok := d.ChargesPeriod()
		return p.From, ok
	case "charges_to":
		p, ok := d.ChargesPeriod()
		return p.To, ok
	case "fixed_charges_from":
		p, ok := d.FixedChargesPeriod()
		return p.From, ok
	case "fixed_charges_to":
		p, ok := d.FixedChargesPeriod()
		return p.To, ok
	case "fixed_charges_period_end":
		return d.FixedChargesPeriodEnd()
	case "current_period_end":
		return d.CurrentPeriodEnd(), true
	case "period_end":
		return d.PeriodEnd(), true
	case "previous_period_start":
		return d.PreviousPeriodStart(), true
	case "current_period_start":
		return d.CurrentPeriodStart(), true
	case "period_days":
		return d.PeriodDays(), true
	case "period_days_at":
		return d.PeriodDaysAt(c.PeriodDaysAt.Time), true
	case "charges_days":
		return d.ChargesDays(), true
	case "fixed_charges_days":
		return d.FixedChargesDays(), true
	case "day_price":
		return d.DayPrice(money.MustParse(c.DayPriceOf)).String(), true
	case "first_month_of_period":
		return d.FirstMonthOfPeriod(), true
	case "first_month_of_first_period":
		return d.FirstMonthOfFirstPeriod(), true
	case "adjusted":
		return adjusted, true
	}
	panic("unknown result " + name)
}

var timeResults = []string{
	"from", "to", "charges_from", "charges_to", "fixed_charges_from", "fixed_charges_to",
	"fixed_charges_period_end", "current_period_end", "period_end", "previous_period_start",
	"current_period_start",
}

func runDatesFile(t *testing.T, file string) {
	cases := loadCases[datesCase](t, file, func(m map[string]any) {
		if _, ok := m["started_at"]; !ok {
			m["started_at"] = m["subscription_at"]
		}
	})
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			t.Logf("source: %s", c.Source)
			s := c.subscription(t)
			mode := Billing
			if c.CurrentUsage {
				mode = CurrentUsage
			}
			var (
				d        *Dates
				adjusted bool
				err      error
			)
			if c.Termination {
				d, adjusted, err = s.TerminationDates(c.BillingAt.Time)
			} else {
				d, err = s.Dates(c.BillingAt.Time, mode)
			}
			if want, ok := c.Want["error"]; ok {
				if got := errors.Is(err, ErrInvalid); got != want {
					t.Fatalf("error: got = %v, want = %v", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Want) == 0 {
				t.Fatal("no expectation")
			}
			checkBoundaries(t, s, d)
			for _, name := range slices.Sorted(maps.Keys(c.Want)) {
				want := c.Want[name]
				// "=name" expects the same value as another result, as in
				// lago's relative expectations.
				if ref, isRef := want.(string); isRef && strings.HasPrefix(ref, "=") {
					v, ok := datesResult(c, d, adjusted, ref[1:])
					if !ok {
						v = nil
					}
					want = v
				}
				got, ok := datesResult(c, d, adjusted, name)
				switch {
				case want == nil:
					if ok {
						t.Errorf("%s: got = %v, want = unavailable", name, got)
					}
				case !ok:
					t.Errorf("%s: got = unavailable, want = %v", name, want)
				case slices.Contains(timeResults, name):
					w, err := wantTime(want)
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if g := got.(time.Time); !g.Equal(w) {
						t.Errorf("%s: got = %s, want = %s", name, g.UTC().Format(time.RFC3339Nano), w.UTC().Format(time.RFC3339Nano))
					}
				default:
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Errorf("%s: got = %v, want = %v", name, got, want)
					}
				}
			}
		})
	}
}

// checkBoundaries checks that Boundaries and PeriodEnd agree with the
// single-period methods.
func checkBoundaries(t *testing.T, s Subscription, d *Dates) {
	t.Helper()
	b, ok := d.Boundaries()
	sub, subOK := d.SubscriptionPeriod()
	if ok != subOK || b.Subscription != sub {
		t.Errorf("Boundaries.Subscription: got = %v (%v), want = %v (%v)", b.Subscription, ok, sub, subOK)
	}
	if !ok {
		if b != (Boundaries{}) {
			t.Errorf("Boundaries: got = %+v, want = zero", b)
		}
		return
	}
	charges, billed := d.ChargesPeriod()
	if b.Charges != charges || b.ChargesBilled != billed || b.ChargesDays != d.ChargesDays() {
		t.Errorf("Boundaries charges: got = %v %v %d, want = %v %v %d",
			b.Charges, b.ChargesBilled, b.ChargesDays, charges, billed, d.ChargesDays())
	}
	fixed, billed := d.FixedChargesPeriod()
	if b.FixedCharges != fixed || b.FixedChargesBilled != billed || b.FixedChargesDays != d.FixedChargesDays() {
		t.Errorf("Boundaries fixed charges: got = %v %v %d, want = %v %v %d",
			b.FixedCharges, b.FixedChargesBilled, b.FixedChargesDays, fixed, billed, d.FixedChargesDays())
	}
	// Before any clamp, the subscription period ends at PeriodEnd.
	if !s.Terminated() && sub.To.After(s.StartedAt) {
		if got := d.PeriodEnd(); !got.Equal(sub.To) {
			t.Errorf("PeriodEnd: got = %s, want = %s", got, sub.To)
		}
	}
}

func TestDates(t *testing.T) {
	for _, file := range []string{
		"weekly.yaml", "monthly.yaml", "quarterly.yaml", "semiannual.yaml", "yearly.yaml",
		"dates_service.yaml", "termination.yaml",
	} {
		t.Run(strings.TrimSuffix(file, ".yaml"), func(t *testing.T) { runDatesFile(t, file) })
	}
}
