package cli_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// bill moves the clock to at and runs the billing clock jobs there, as
// lago's perform_billing does, then the usage clock jobs.
func (s *scenario) bill(at time.Time) {
	s.t.Helper()
	s.at(at)
	s.trigger(jobs.KindBillSubscriptions)
	s.trigger(jobs.KindBillEndedTrials)
	s.idle()
	s.usageUpdate()
}

// usageUpdate runs the usage clock jobs, as lago's perform_usage_update
// does: daily usages and lifetime usages (progressive billing). The
// subscription activities have no clock job: their delayed jobs run on
// their own.
func (s *scenario) usageUpdate() {
	s.t.Helper()
	s.trigger(jobs.KindComputeDailyUsages)
	s.trigger(jobs.KindRefreshLifetimeUsages)
	s.idle()
}

// plan creates a plan without charges through the API.
func (s *scenario) plan(code, interval, amount string, payInAdvance bool) {
	s.t.Helper()
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": code, "code": code, "interval": interval, "pay_in_advance": payInAdvance,
		"amount": map[string]any{"currency": "EUR", "amount": amount},
	})
}

// subscriptionFee returns the amount in cents of the single subscription
// fee of inv, failing when it has not exactly one.
func subscriptionFee(t *testing.T, inv jsonValue) int64 {
	t.Helper()
	var out []int64
	for _, f := range inv.list("fees") {
		if f.str("fee_type") == "FEE_TYPE_SUBSCRIPTION" {
			out = append(out, f.cents("amount"))
		}
	}
	if len(out) != 1 {
		t.Fatalf("subscription fees: got = %v, want = one", out)
	}
	return out[0]
}

// TestScenarioUpgradeProration upgrades an anniversary subscription in
// the middle of a quarter and terminates it a month later: the final
// subscription fee is prorated on the whole period, as is that of a
// calendar subscription started in the middle of its quarter.
//
// source: spec/scenarios/subscriptions/upgrade_proration_spec.rb:20
// source: spec/scenarios/subscriptions/upgrade_proration_spec.rb:60
func TestScenarioUpgradeProration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		upgrade bool
		billing string
	}{
		{"upgrade", true, "BILLING_TIME_ANNIVERSARY"},
		{"calendar", false, "BILLING_TIME_CALENDAR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
			if !tt.upgrade {
				start = time.Date(2024, 5, 20, 10, 0, 0, 0, time.UTC)
			}
			s := newScenario(t, start, scenarioOptions{})
			s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "timezone": "UTC"})
			// A day of the 91-day quarter costs 10 EUR.
			s.plan("plan", "INTERVAL_QUARTERLY", "910", false)
			s.plan("cheaper_plan", "INTERVAL_QUARTERLY", "455", false)
			first := "plan"
			if tt.upgrade {
				first = "cheaper_plan"
			}
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
				"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:" + first, "billing_time": tt.billing,
			})
			s.idle()
			sub := "external_id%3Acust"
			if tt.upgrade {
				s.at(time.Date(2024, 5, 20, 10, 0, 0, 0, time.UTC))
				res := s.api(http.MethodPost, "/v1/subscriptions/external_id%3Acust:change_plan", map[string]any{"plan_id": "code:plan"})
				if got := res.str("subscription.subscription_at"); got != "2024-01-15T10:00:00Z" {
					t.Errorf("upgraded subscription_at: got = %s, want = 2024-01-15T10:00:00Z", got)
				}
				if got := res.str("subscription.started_at"); got != "2024-05-20T10:00:00Z" {
					t.Errorf("upgraded started_at: got = %s, want = 2024-05-20T10:00:00Z", got)
				}
				sub = res.str("subscription.id")
				s.idle()
			}
			before := len(s.invoices("cust"))
			s.at(time.Date(2024, 6, 20, 10, 0, 0, 0, time.UTC))
			s.api(http.MethodPost, "/v1/subscriptions/"+sub+":terminate", map[string]any{})
			s.idle()
			invs := s.waitInvoices("cust", before+1)
			inv := s.api(http.MethodGet, "/v1/invoices/"+invs[len(invs)-1].str("id"), nil).at("invoice")
			if got := subscriptionFee(t, inv); got != 32_000 {
				t.Errorf("final subscription fee: got = %d, want = 32000", got)
			}
			s.noFailedJobs()
		})
	}
}

// TestScenarioDowngrade bills a monthly plan in advance for four periods,
// downgrades it to a yearly plan, which starts at the end of the period
// and is billed prorated up to the anniversary.
//
// source: spec/scenarios/subscriptions/downgrade_spec.rb:32
func TestScenarioDowngrade(t *testing.T) {
	t.Parallel()
	subscriptionAt := time.Date(2023, 7, 19, 12, 12, 0, 0, time.UTC)
	s := newScenario(t, subscriptionAt, scenarioOptions{})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.plan("monthly", "INTERVAL_MONTHLY", "129", true)
	s.plan("yearly", "INTERVAL_YEARLY", "1188", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
		"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:monthly",
		"billing_time": "BILLING_TIME_ANNIVERSARY", "subscription_at": subscriptionAt.Format(time.RFC3339),
	})
	s.idle()

	check := func(n int, fees int64, from, to, chargesFrom, chargesTo string) jsonValue {
		t.Helper()
		invs := s.waitInvoices("cust", n)
		if len(invs) != n {
			t.Fatalf("invoices: got = %d, want = %d", len(invs), n)
		}
		inv := invs[n-1]
		if got := inv.cents("fees_amount"); got != fees {
			t.Errorf("invoice %d fees: got = %d, want = %d", n, got, fees)
		}
		for path, want := range map[string]string{
			"subscriptions.0.period.from": from, "subscriptions.0.period.to": to,
			"subscriptions.0.charges_period.from": chargesFrom, "subscriptions.0.charges_period.to": chargesTo,
		} {
			if want == "" {
				continue
			}
			if got := inv.str(path); got != want {
				t.Errorf("invoice %d %s: got = %s, want = %s", n, path, got, want)
			}
		}
		return inv
	}
	// The ranges' ends are exclusive: lago's 23:59:59 ends are the next
	// second here.
	check(1, 12_900, "2023-07-19T00:00:00Z", "2023-08-19T00:00:00Z", "", "")
	s.bill(time.Date(2023, 8, 19, 12, 12, 0, 0, time.UTC))
	check(2, 12_900, "2023-08-19T00:00:00Z", "2023-09-19T00:00:00Z", "2023-07-19T12:12:00Z", "2023-08-19T00:00:00Z")
	s.bill(time.Date(2023, 9, 19, 12, 12, 0, 0, time.UTC))
	check(3, 12_900, "2023-09-19T00:00:00Z", "2023-10-19T00:00:00Z", "2023-08-19T00:00:00Z", "2023-09-19T00:00:00Z")
	s.bill(time.Date(2023, 10, 19, 12, 12, 0, 0, time.UTC))
	check(4, 12_900, "2023-10-19T00:00:00Z", "2023-11-19T00:00:00Z", "2023-09-19T00:00:00Z", "2023-10-19T00:00:00Z")

	s.at(time.Date(2023, 11, 9, 0, 0, 0, 0, time.UTC))
	res := s.api(http.MethodPost, "/v1/subscriptions/external_id%3Acust:change_plan", map[string]any{"plan_id": "code:yearly"})
	if got := res.str("previous_subscription.status"); got != "SUBSCRIPTION_STATUS_ACTIVE" {
		t.Errorf("downgraded subscription status: got = %s, want = SUBSCRIPTION_STATUS_ACTIVE", got)
	}
	next := res.str("subscription.id")
	s.idle()
	if n := len(s.invoices("cust")); n != 4 {
		t.Errorf("invoices after the downgrade: got = %d, want = 4", n)
	}

	s.bill(time.Date(2023, 11, 19, 12, 12, 0, 0, time.UTC))
	invs := s.waitInvoices("cust", 5)
	if len(invs) != 5 {
		t.Fatalf("invoices: got = %d, want = 5", len(invs))
	}
	if got := s.api(http.MethodGet, "/v1/subscriptions/"+res.str("previous_subscription.id"), nil).str("subscription.status"); got != "SUBSCRIPTION_STATUS_TERMINATED" {
		t.Errorf("previous subscription status: got = %s, want = SUBSCRIPTION_STATUS_TERMINATED", got)
	}
	if got := s.api(http.MethodGet, "/v1/subscriptions/"+next, nil).str("subscription.status"); got != "SUBSCRIPTION_STATUS_ACTIVE" {
		t.Errorf("new subscription status: got = %s, want = SUBSCRIPTION_STATUS_ACTIVE", got)
	}
	// 243 of the 366 days to the anniversary of the yearly plan; the
	// terminated in-advance plan has no charges to bill.
	inv := invs[4]
	if got := inv.cents("fees_amount"); got != 78_875 {
		t.Errorf("downgrade invoice fees: got = %d, want = 78875", got)
	}
	var period []string
	for _, is := range inv.list("subscriptions") {
		if is.str("subscription_id") == next {
			period = []string{is.str("period.from"), is.str("period.to")}
		}
	}
	if want := []string{"2023-11-19T00:00:00Z", "2024-07-19T00:00:00Z"}; len(period) != 2 || period[0] != want[0] || period[1] != want[1] {
		t.Errorf("new subscription period: got = %v, want = %v", period, want)
	}
	s.noFailedJobs()
}
