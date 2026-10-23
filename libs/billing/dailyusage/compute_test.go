package dailyusage_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

const computeSpec = "spec/services/daily_usages/compute_service_spec.rb"

// computeFixture is the setup of compute_service_spec.rb: a calendar
// subscription started a year before ts to a plan with a standard charge
// of 100 on a count metric.
type computeFixture struct {
	*fixture
	metric *catalog.BillableMetric
	charge *catalog.Charge
	sub    *subscription.Subscription
}

func newComputeFixture(t *testing.T, now, ts time.Time, amount string, custOpts []func(*customer.Customer), subOpts ...func(*subscription.Subscription)) *computeFixture {
	t.Helper()
	return newComputeFixtureWith(t, now, ts, amount, custOpts, nil, subOpts...)
}

func newComputeFixtureWith(t *testing.T, now, ts time.Time, amount string, custOpts []func(*customer.Customer), chargeOpts []func(*catalog.Charge), subOpts ...func(*subscription.Subscription)) *computeFixture {
	t.Helper()
	f := &computeFixture{fixture: newFixture(t, now, custOpts...)}
	f.metric = f.Metric("api_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "region", Values: []string{"us-east", "eu-central"}}}
	})
	f.charge = f.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`, chargeOpts...)
	f.sub = f.fixture.sub(f.plan, ts.AddDate(-1, 0, 0), subOpts...)
	return f
}

// event ingests the spec's event, two hours before ts.
func (f *computeFixture) event(ts time.Time) {
	f.Event(f.sub, f.metric.Code, ts.Add(-2*time.Hour), "region", "eu-central")
}

func TestComputeNoUsage(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:43
	ts := billingtest.Date(2024, 10, 22, 0, 5)
	f := newComputeFixture(t, ts, ts, "100", nil)
	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if du != nil || len(f.usages(f.sub)) != 0 {
		t.Errorf("%s:43: Compute() = %v with %d rows, want = nil with 0", computeSpec, du, len(f.usages(f.sub)))
	}
}

func TestCompute(t *testing.T) {
	ts := billingtest.Date(2024, 10, 22, 0, 5)
	tests := []struct {
		source string
		amount string
		// filtered adds a charge filter on a region no event has.
		filtered   bool
		wantAmount int64
	}{
		{source: ":64", amount: "100", filtered: true, wantAmount: 10000},
		{source: ":78", amount: "0", wantAmount: 0},
		{source: ":92", amount: "100", wantAmount: 10000},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			src := computeSpec + tt.source
			var chargeOpts []func(*catalog.Charge)
			if tt.filtered {
				chargeOpts = append(chargeOpts, func(c *catalog.Charge) {
					c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"us-east"}}, `{"amount":"10"}`)}
				})
			}
			f := newComputeFixtureWith(t, ts, ts, tt.amount, nil, chargeOpts)
			f.event(ts)

			du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
			if err != nil {
				t.Fatalf("%s: Compute() error = %v", src, err)
			}
			rows := f.usages(f.sub)
			if du == nil || len(rows) != 1 {
				t.Fatalf("%s: Compute() = %v with %d rows, want = a row", src, du, len(rows))
			}
			if got := len(du.Usage.Charges); got != 1 {
				t.Errorf("%s: charges usage: got = %d, want = 1", src, got)
			}
			if tt.filtered {
				if got := len(du.Usage.Charges[0].Filters); got != 1 {
					t.Errorf("%s: filters usage: got = %d, want = 1 (the zero-unit filter is left out)", src, got)
				}
			}
			if du.Usage.Amount != tt.wantAmount {
				t.Errorf("%s: amount_cents: got = %d, want = %d", src, du.Usage.Amount, tt.wantAmount)
			}
			checkRow(t, src, du, f.sub, core.NewDate(2024, 10, 21), ts, billingtest.Date(2024, 10, 1), billingtest.Date(2024, 11, 1))
			if du.UsageDiff == nil {
				t.Errorf("%s: usage_diff: got = nil, want = the whole usage", src)
			}
		})
	}
}

// checkRow checks the identity, day, refresh instant and charges period
// of a stored snapshot. The period's end is exclusive, where lago stores
// the last second.
func checkRow(t *testing.T, src string, du *subscription.DailyUsage, s *subscription.Subscription, day core.Date, refreshed, from, to time.Time) {
	t.Helper()
	if du.OrganizationID != s.OrganizationID || du.CustomerID != s.CustomerID || du.SubscriptionID != s.ID || du.ExternalSubscriptionID != s.ExternalID {
		t.Errorf("%s: identity: got = %v/%v/%v/%q, want = %v/%v/%v/%q", src, du.OrganizationID, du.CustomerID, du.SubscriptionID, du.ExternalSubscriptionID,
			s.OrganizationID, s.CustomerID, s.ID, s.ExternalID)
	}
	if du.UsageDate != day {
		t.Errorf("%s: usage_date: got = %s, want = %s", src, du.UsageDate, day)
	}
	if !du.RefreshedAt.Equal(refreshed) {
		t.Errorf("%s: refreshed_at: got = %v, want = %v", src, du.RefreshedAt, refreshed)
	}
	if !du.From.Equal(from) || !du.To.Equal(to) {
		t.Errorf("%s: period: got = [%v, %v), want = [%v, %v)", src, du.From, du.To, from, to)
	}
}

func TestComputeExisting(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:127-170
	ts := billingtest.Date(2024, 10, 22, 0, 5)
	sp := paris(t, "America/Sao_Paulo")
	tests := []struct {
		source string
		custTZ *time.Location
		entTZ  *time.Location
		day    core.Date
	}{
		{source: ":127", day: core.NewDate(2024, 10, 21)},
		{source: ":141", entTZ: sp, day: core.NewDate(2024, 10, 20)},
		{source: ":158", custTZ: sp, day: core.NewDate(2024, 10, 20)},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			src := computeSpec + tt.source
			var custOpts []func(*customer.Customer)
			if tt.custTZ != nil {
				custOpts = append(custOpts, func(c *customer.Customer) { c.Timezone = tt.custTZ })
			}
			f := newComputeFixture(t, ts, ts, "100", custOpts)
			if tt.entTZ != nil {
				f.UpdateBillingEntity(func(be *organization.BillingEntity) { be.Timezone = tt.entTZ })
			}
			f.event(ts)
			existing := f.storeUsage(f.sub, tt.day)

			du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
			if err != nil {
				t.Fatalf("%s: Compute() error = %v", src, err)
			}
			if du == nil || du.ID != existing.ID {
				t.Errorf("%s: Compute() = %v, want = the existing %s", src, du, existing.ID)
			}
			if got := len(f.usages(f.sub)); got != 1 {
				t.Errorf("%s: rows: got = %d, want = 1", src, got)
			}
		})
	}
}

func TestComputeBillingDay(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:174
	now := billingtest.Date(2024, 10, 22, 10)
	f := newComputeFixture(t, now, now, "100", nil, billingtest.Anniversary)
	f.event(now)
	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if du != nil || len(f.usages(f.sub)) != 0 {
		t.Errorf("%s:174: Compute() = %v, want = nil on the billing day", computeSpec, du)
	}
}

func TestComputeTerminatedAfterTimestamp(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:186
	now := billingtest.Date(2024, 11, 24)
	terminated := billingtest.Date(2024, 10, 30)
	ts := terminated.Add(-24 * time.Hour)
	started := billingtest.Date(2023, 11, 24)
	f := newComputeFixture(t, now, now, "100", nil, func(s *subscription.Subscription) {
		s.Status, s.StartedAt, s.TerminatedAt = subscription.StatusTerminated, &started, &terminated
	})
	f.event(ts)

	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if du == nil {
		t.Fatalf("%s:186: Compute() = nil, want = a row", computeSpec)
	}
	checkRow(t, computeSpec+":186", du, f.sub, core.NewDate(2024, 10, 28), ts, billingtest.Date(2024, 10, 1), terminated)
	if du.Usage.Amount != 10000 {
		t.Errorf("%s:186: amount_cents: got = %d, want = 10000", computeSpec, du.Usage.Amount)
	}
}

func TestComputeCustomerTimezone(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:231
	ts := billingtest.Date(2024, 10, 21, 15, 5)
	syd := paris(t, "Australia/Sydney")
	f := newComputeFixture(t, ts, ts, "100", []func(*customer.Customer){func(c *customer.Customer) { c.Timezone = syd }})
	f.event(ts)
	du, err := f.svc.Compute(f.Ctx, f.OrgID(), f.sub.ID, ts)
	if err != nil {
		t.Fatal(err)
	}
	if du == nil || du.UsageDate != core.NewDate(2024, 10, 21) {
		t.Errorf("%s:231: usage_date: got = %v, want = 2024-10-21", computeSpec, du)
	}
}
