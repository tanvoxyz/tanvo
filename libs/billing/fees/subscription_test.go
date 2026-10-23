package fees_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var date = billingtest.Date

// subscriptionCase is a subscription fee scenario.
type subscriptionCase struct {
	name string
	// interval and amount of the plan (monthly, 100 by default).
	interval period.Interval
	amount   int64
	advance  bool
	trial    int
	// anniversary bills on the subscription's anniversary.
	anniversary bool
	// started is the start of the subscription; created its creation
	// (started by default).
	started, created time.Time
	// b are the boundaries billed at ts.
	b  period.Boundaries
	ts time.Time
	// terminated ends the subscription at that instant.
	terminated time.Time
	// next is the amount of the plan of a subscription replacing it; zero
	// means none.
	next int64
	// previous is the amount of the plan of the subscription it replaces;
	// zero means none.
	previous int64
	// billed stores a subscription fee of an earlier invoice.
	billed bool

	want    int64
	precise string
}

func TestSubscriptionFee(t *testing.T) {
	t.Parallel()
	jan1 := date(2022, time.January, 1, 0, 1)
	march := monthly(date(2022, time.March, 1), date(2022, time.April, 1))
	cases := []subscriptionCase{
		// source: spec/services/fees/subscription_service_spec.rb:59
		{name: "full period", started: jan1, b: march, ts: date(2022, time.April, 30, 23, 59), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:100
		{name: "full period, trial ends in it", trial: 3, started: date(2022, time.March, 1), b: march, ts: date(2022, time.April, 1), want: 90, precise: "90.32258064516129032258064516129"},
		// source: spec/services/fees/subscription_service_spec.rb:117
		{name: "full period, trial ends after it", trial: 45, started: date(2022, time.March, 1), b: march, ts: date(2022, time.April, 1), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:324
		{
			name: "weekly, first week from monday", interval: period.Weekly, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 100, precise: "100",
		},
		// source: spec/services/fees/subscription_service_spec.rb:345
		{
			name: "weekly, trial ends in the first week", interval: period.Weekly, trial: 3, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 57, precise: "57.142857142857142857142857142857",
		},
		// source: spec/services/fees/subscription_service_spec.rb:359
		{
			name: "weekly, trial ends after the first week", interval: period.Weekly, trial: 10, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 0, precise: "0",
		},
		// source: spec/services/fees/subscription_service_spec.rb:373
		{
			name: "weekly in advance, first week from monday", interval: period.Weekly, advance: true, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 20, 0, 1), want: 100, precise: "100",
		},
		// source: spec/services/fees/subscription_service_spec.rb:388
		{
			name: "weekly in advance, trial ends in the week", interval: period.Weekly, advance: true, trial: 3, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 20, 0, 1), want: 57, precise: "57.142857142857142857142857142857",
		},
		// source: spec/services/fees/subscription_service_spec.rb:401
		{
			name: "weekly in advance, trial ends after the week", interval: period.Weekly, advance: true, trial: 10, started: date(2022, time.June, 20, 0, 1),
			b: monthly(date(2022, time.June, 20), date(2022, time.June, 27)), ts: date(2022, time.June, 20, 0, 1), want: 0, precise: "0",
		},
		// source: spec/services/fees/subscription_service_spec.rb:417
		{
			name: "weekly, started on wednesday", interval: period.Weekly, started: date(2022, time.June, 22),
			b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 71, precise: "71.428571428571428571428571428571",
		},
		// source: spec/services/fees/subscription_service_spec.rb:436
		{
			name: "weekly, started on wednesday, trial in the week", interval: period.Weekly, trial: 3, started: date(2022, time.June, 22),
			b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 29, precise: "28.571428571428571428571428571429",
		},
		// source: spec/services/fees/subscription_service_spec.rb:449
		{
			name: "weekly, started on wednesday, trial after the week", interval: period.Weekly, trial: 10, started: date(2022, time.June, 22),
			b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 27), want: 0, precise: "0",
		},
		// source: spec/services/fees/subscription_service_spec.rb:463
		{
			name: "weekly in advance, started on wednesday", interval: period.Weekly, advance: true, started: date(2022, time.June, 22),
			b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 22), want: 71, precise: "71.428571428571428571428571428571",
		},
		// source: spec/services/fees/subscription_service_spec.rb:478
		{
			name: "weekly in advance, started on wednesday, trial in the week", interval: period.Weekly, advance: true, trial: 3, started: date(2022, time.June, 22),
			b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 22), want: 29, precise: "28.571428571428571428571428571429",
		},
		// source: spec/services/fees/subscription_service_spec.rb:509
		{
			name: "weekly in advance, created in the past", interval: period.Weekly, advance: true, started: date(2022, time.June, 22),
			created: date(2022, time.June, 24), b: monthly(date(2022, time.June, 22), date(2022, time.June, 27)), ts: date(2022, time.June, 22), want: 100, precise: "100",
		},
		// source: spec/services/fees/subscription_service_spec.rb:533
		{
			name: "weekly, started before the previous period", interval: period.Weekly, started: date(2022, time.June, 22),
			created: date(2022, time.June, 30), b: monthly(date(2022, time.June, 27), date(2022, time.July, 4)), ts: date(2022, time.July, 4), want: 100, precise: "100",
		},
		// source: spec/services/fees/subscription_service_spec.rb:552
		{name: "monthly, started on the 1st", started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:573
		{name: "monthly, started on the 1st, trial in the month", trial: 3, started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 90, precise: "90.32258064516129032258064516129"},
		// source: spec/services/fees/subscription_service_spec.rb:586
		{name: "monthly, started on the 1st, trial after the month", trial: 45, started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:611
		{name: "monthly in advance, started on the 1st", advance: true, started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.January, 2, 0, 1), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:626
		{name: "monthly in advance, trial in the month", advance: true, trial: 3, started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.January, 2, 0, 1), want: 90, precise: "90.32258064516129032258064516129"},
		// source: spec/services/fees/subscription_service_spec.rb:639
		{name: "monthly in advance, trial after the month", advance: true, trial: 45, started: jan1, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.January, 2, 0, 1), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:655
		{name: "monthly, started on the 15th", started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 55, precise: "54.838709677419354838709677419355"},
		// source: spec/services/fees/subscription_service_spec.rb:674
		{name: "monthly, started on the 15th, trial in the month", trial: 3, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 45, precise: "45.161290322580645161290322580645"},
		// source: spec/services/fees/subscription_service_spec.rb:687
		{name: "monthly, started on the 15th, trial after the month", trial: 45, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:712
		{name: "monthly in advance, started on the 15th", advance: true, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.March, 16), want: 55, precise: "54.838709677419354838709677419355"},
		// source: spec/services/fees/subscription_service_spec.rb:727
		{name: "monthly in advance, started on the 15th, trial in the month", advance: true, trial: 3, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.March, 16), want: 45, precise: "45.161290322580645161290322580645"},
		// source: spec/services/fees/subscription_service_spec.rb:740
		{name: "monthly in advance, started on the 15th, trial after the month", advance: true, trial: 45, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.March, 16), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:793
		{
			name: "anniversary in advance, trial in the period", amount: 3000, advance: true, trial: 15, anniversary: true,
			started: date(2022, time.August, 31, 0, 1), b: monthly(date(2022, time.August, 31), date(2022, time.October, 1)),
			ts: date(2022, time.October, 1), want: 1600, precise: "1600",
		},
		// source: spec/services/fees/subscription_service_spec.rb:847
		{name: "yearly, started on january 1st", interval: period.Yearly, started: date(2022, time.January, 1), b: monthly(date(2022, time.January, 1), date(2023, time.January, 1)), ts: date(2023, time.January, 1), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:876
		{name: "yearly in advance, started on january 1st", interval: period.Yearly, advance: true, started: date(2022, time.January, 1), b: monthly(date(2022, time.January, 1), date(2023, time.January, 1)), ts: date(2022, time.January, 1), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:901
		{name: "yearly, started on march 15th", interval: period.Yearly, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2023, time.January, 1)), ts: date(2023, time.January, 1), want: 80, precise: "80"},
		// source: spec/services/fees/subscription_service_spec.rb:917
		{name: "yearly in advance, started on march 15th", interval: period.Yearly, advance: true, started: date(2022, time.March, 15), b: monthly(date(2022, time.March, 15), date(2023, time.January, 1)), ts: date(2022, time.March, 15), want: 80, precise: "80"},
		// source: spec/services/fees/subscription_service_spec.rb:956
		{name: "billed before", started: date(2022, time.January, 1), billed: true, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 100, precise: "100"},
		// source: spec/services/fees/subscription_service_spec.rb:969
		{name: "billed before, trial in the period", trial: 3, started: date(2022, time.January, 1), billed: true, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 90, precise: "90.32258064516129032258064516129"},
		// source: spec/services/fees/subscription_service_spec.rb:1027
		{
			name: "billed before, monthly in advance, trial in the period", advance: true, trial: 15, started: date(2022, time.January, 1), billed: true,
			b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.January, 2), want: 52, precise: "51.612903225806451612903225806452",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1067
		{name: "billed before, trial after the period", trial: 45, started: date(2022, time.January, 1), billed: true, b: monthly(date(2022, time.January, 1), date(2022, time.February, 1)), ts: date(2022, time.February, 1), want: 0, precise: "0"},
		// source: spec/services/fees/subscription_service_spec.rb:1140
		{
			name: "terminated", started: date(2022, time.March, 15), terminated: date(2022, time.March, 20, 12),
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 21)), ts: date(2022, time.March, 21), want: 65, precise: "64.516129032258064516129032258065",
		},
		// The end of a period cut by a termination at local midnight bills
		// that day; any other end is counted as is, so the two whole days
		// from a mid-day trial end to a mid-day termination bill two days.
		// source: app/services/utils/datetime.rb:71
		{
			name: "terminated at midnight", started: date(2022, time.March, 15), terminated: date(2022, time.March, 20),
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 20)), ts: date(2022, time.March, 20), want: 65, precise: "64.516129032258064516129032258065",
		},
		// source: app/services/utils/datetime.rb:71
		{
			name: "terminated whole days after a mid-day trial end", trial: 3, started: date(2022, time.March, 15, 12), terminated: date(2022, time.March, 20, 12),
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 20, 12)), ts: date(2022, time.March, 21), want: 6, precise: "6.4516129032258064516129032258065",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1202
		{
			name: "terminated weekly", interval: period.Weekly, started: date(2022, time.March, 15), terminated: date(2022, time.March, 16, 12),
			b: monthly(date(2022, time.March, 14), date(2022, time.March, 17)), ts: date(2022, time.March, 17), want: 43, precise: "42.857142857142857142857142857143",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1221
		{
			name: "terminated by an upgrade", started: date(2022, time.March, 15), terminated: date(2022, time.March, 20, 12), next: 100,
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 21)), ts: date(2022, time.March, 21), want: 61, precise: "61.290322580645161290322580645161",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1244
		{
			name: "terminated by an upgrade, trial before the termination", trial: 3, started: date(2022, time.March, 15), terminated: date(2022, time.March, 20, 12), next: 100,
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 21)), ts: date(2022, time.March, 21), want: 6, precise: "6.4516129032258064516129032258065",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1257
		{
			name: "terminated by an upgrade, trial after the termination", trial: 45, started: date(2022, time.March, 15), terminated: date(2022, time.March, 20, 12), next: 100,
			b: monthly(date(2022, time.March, 1), date(2022, time.March, 21)), ts: date(2022, time.March, 21), want: 0, precise: "0",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1308
		{
			name: "created by an upgrade", started: date(2022, time.March, 15), previous: 80,
			b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 55, precise: "54.838709677419354838709677419355",
		},
		// The previous subscription started on 2021-09-15, 181 days earlier;
		// the trial runs from there.
		// source: spec/services/fees/subscription_service_spec.rb:1361
		{
			name: "created by an upgrade, trial ends in the period", trial: 181 + 3, started: date(2022, time.March, 15), previous: 80,
			b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 45, precise: "45.161290322580645161290322580645",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1376
		{
			name: "created by an upgrade, trial ends after the period", trial: 181 + 45, started: date(2022, time.March, 15), previous: 80,
			b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.April, 1), want: 0, precise: "0",
		},
		// source: spec/services/fees/subscription_service_spec.rb:1404
		{
			name: "created by an upgrade, in advance", advance: true, started: date(2022, time.March, 15), previous: 80,
			b: monthly(date(2022, time.March, 15), date(2022, time.April, 1)), ts: date(2022, time.March, 15), want: 55, precise: "54.838709677419354838709677419355",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runSubscriptionCase(t, tc)
		})
	}
}

func runSubscriptionCase(t *testing.T, tc subscriptionCase) {
	t.Helper()
	f := newFixture(t, tc.started)
	if tc.interval == 0 {
		tc.interval = period.Monthly
	}
	if tc.amount == 0 {
		tc.amount = 100
	}
	created := tc.created
	if created.IsZero() {
		created = tc.started
	}
	plan := f.Plan(tc.interval, tc.amount, func(p *catalog.Plan) {
		p.PayInAdvance, p.TrialPeriodDays = tc.advance, tc.trial
	})
	cust := f.Customer()
	var prev *subscription.Subscription
	if tc.previous != 0 {
		prevPlan := f.Plan(tc.interval, tc.previous)
		prev = f.Subscription(cust, prevPlan, billingtest.SubscribedAt(tc.started.AddDate(0, -6, 0)))
		f.UpdateSubscription(prev, func(s *subscription.Subscription) {
			s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(tc.started)
		})
	}
	s := f.Subscription(cust, plan, billingtest.SubscribedAt(tc.started), func(s *subscription.Subscription) {
		s.CreatedAt = created
		if tc.anniversary {
			s.BillingTime = period.Anniversary
		}
		if prev != nil {
			s.PreviousSubscriptionID = prev.ID
		}
	})
	if !tc.terminated.IsZero() {
		f.UpdateSubscription(s, func(s *subscription.Subscription) {
			s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(tc.terminated)
		})
	}
	if tc.next != 0 {
		nextPlan := f.Plan(tc.interval, tc.next)
		f.Subscription(cust, nextPlan, billingtest.SubscribedAt(tc.terminated), func(n *subscription.Subscription) {
			n.ExternalID, n.PreviousSubscriptionID = s.ExternalID, s.ID
		})
	}
	if tc.billed {
		earlier := f.scope(s, monthly(tc.b.Subscription.From.AddDate(0, -1, 0), tc.b.Subscription.From), tc.ts.AddDate(0, -1, 0))
		fee := &invoice.Fee{
			ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: earlier.Invoice.ID, SubscriptionID: s.ID,
			Type: core.FeeTypeSubscription, Currency: billingtest.USD, Amount: tc.amount, PreciseAmount: dec("100"),
			Units: dec("1"), Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: created,
		}
		f.store(fee)
		f.Clock.Advance(time.Hour)
	}
	sc := f.scope(s, tc.b, tc.ts)
	got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return f.svc.SubscriptionFee(ctx, r, sc, fees.Options{})
	})
	checkAmount(t, got, tc.want, tc.precise)
	if got.UnitAmount != tc.want {
		t.Errorf("got unit amount = %d, want = %d", got.UnitAmount, tc.want)
	}
	if !eqDec(got.Units, dec("1")) {
		t.Errorf("got units = %s, want = 1", got.Units)
	}
	if got.InvoiceID != sc.Invoice.ID || got.Type != core.FeeTypeSubscription || got.SubscriptionID != s.ID {
		t.Errorf("got fee = %+v, want a subscription fee of the invoice", got)
	}
}

// source: spec/services/fees/subscription_service_spec.rb:1105
func TestSubscriptionFeeAlreadyBilled(t *testing.T) {
	t.Parallel()
	f := newFixture(t, date(2022, time.January, 1))
	s := f.Subscription(f.Customer(), f.Plan(period.Monthly, 100), billingtest.SubscribedAt(date(2022, time.January, 1)))
	sc := f.scope(s, monthly(date(2022, time.February, 1), date(2022, time.March, 1)), date(2022, time.March, 2))
	f.store(&invoice.Fee{
		ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: sc.Invoice.ID, SubscriptionID: s.ID,
		Type: core.FeeTypeSubscription, Currency: billingtest.USD, Amount: 100, PreciseAmount: dec("100"),
		Units: dec("1"), Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: f.Now(),
	})
	got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return f.svc.SubscriptionFee(ctx, r, sc, fees.Options{})
	})
	if got != nil {
		t.Errorf("got fee = %+v, want nil", got)
	}
}

func TestSubscriptionFeeAdjusted(t *testing.T) {
	t.Parallel()
	name := "Adjusted"
	cases := []struct {
		name string
		// final keeps the invoice finalized instead of a draft.
		final bool
		adj   invoice.AdjustedFee
		want  int64
		unit  int64
		// units and preciseUnit are the fee's units and unit price.
		units, preciseUnit string
		display            *string
	}{
		// source: spec/services/fees/subscription_service_spec.rb:153
		{name: "units", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")}, want: 300, unit: 100, units: "3", preciseUnit: "1"},
		// source: spec/services/fees/subscription_service_spec.rb:186
		{
			name: "units and unit price", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: dec("3"), UnitPreciseAmount: dec("2")},
			want: 600, unit: 200, units: "3", preciseUnit: "2",
		},
		// source: spec/services/fees/subscription_service_spec.rb:218
		{
			name: "precise unit price", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: dec("1000"), UnitPreciseAmount: dec("0.001")},
			want: 100, unit: 0, units: "1000", preciseUnit: "0.001",
		},
		// source: spec/services/fees/subscription_service_spec.rb:251
		{
			name: "display name", adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: &name},
			want: 100, unit: 100, units: "1", preciseUnit: "1", display: &name,
		},
		// source: spec/services/fees/subscription_service_spec.rb:273
		{
			name: "invoice not a draft", final: true, adj: invoice.AdjustedFee{Mode: invoice.AdjustmentModeUnits, Units: dec("3")},
			want: 100, unit: 100, units: "1", preciseUnit: "1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, date(2022, time.January, 1, 0, 1))
			s := f.Subscription(f.Customer(), f.Plan(period.Monthly, 100), billingtest.SubscribedAt(date(2022, time.January, 1, 0, 1)))
			var opts []invoiceOpt
			if !tc.final {
				opts = append(opts, draft)
			}
			sc := f.scope(s, monthly(date(2022, time.March, 1), date(2022, time.April, 1)), date(2022, time.April, 1), opts...)
			a := tc.adj
			a.InvoiceID, a.SubscriptionID, a.FeeType, a.FeeID = sc.Invoice.ID, s.ID, core.FeeTypeSubscription, core.NewID[core.FeeID](f.IDs)
			f.adjust(&a)
			got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
				return f.svc.SubscriptionFee(ctx, r, sc, fees.Options{})
			})
			checkAmount(t, got, tc.want, "")
			if got.UnitAmount != tc.unit {
				t.Errorf("got unit amount = %d, want = %d", got.UnitAmount, tc.unit)
			}
			if !eqDec(got.Units, dec(tc.units)) {
				t.Errorf("got units = %s, want = %s", got.Units, tc.units)
			}
			if !eqDec(got.PreciseUnitAmount, dec(tc.preciseUnit)) {
				t.Errorf("got precise unit amount = %s, want = %s", got.PreciseUnitAmount, tc.preciseUnit)
			}
			if (got.InvoiceDisplayName == nil) != (tc.display == nil) || (tc.display != nil && *got.InvoiceDisplayName != *tc.display) {
				t.Errorf("got display name = %v, want = %v", got.InvoiceDisplayName, tc.display)
			}
		})
	}
}
