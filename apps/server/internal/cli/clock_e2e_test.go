package cli_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// TestScenarioClockJobs runs the subscription, invoice, coupon and wallet
// clock jobs of the server at their instants.
func TestScenarioClockJobs(t *testing.T) {
	t.Parallel()
	subscription := func(s *scenario, id string) jsonValue {
		t.Helper()
		return s.api(http.MethodGet, "/v1/subscriptions/"+id, nil).at("subscription")
	}

	// source: spec/scenarios/subscriptions/activation_spec.rb:25
	t.Run("activation at the subscription date", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2023, 8, 24, 0, 7, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "timezone": "America/Bogota"})
		s.plan("plan", "INTERVAL_MONTHLY", "10", false)
		at := time.Date(2023, 8, 25, 10, 0, 0, 0, time.UTC)
		sub := s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
			"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan",
			"billing_time": "BILLING_TIME_CALENDAR", "subscription_at": at.Format(time.RFC3339),
		}).str("subscription.id")
		s.idle()
		if got := subscription(s, sub).str("status"); got != "SUBSCRIPTION_STATUS_PENDING" {
			t.Fatalf("status before the subscription date: got = %s, want = SUBSCRIPTION_STATUS_PENDING", got)
		}
		s.at(at)
		s.trigger(jobs.KindActivateSubscriptions)
		s.idle()
		if got := subscription(s, sub).str("status"); got != "SUBSCRIPTION_STATUS_ACTIVE" {
			t.Errorf("status at the subscription date: got = %s, want = SUBSCRIPTION_STATUS_ACTIVE", got)
		}
		s.noFailedJobs()
	})

	// source: spec/scenarios/subscriptions/terminate_ended_spec.rb:22
	t.Run("termination at the ending date", func(t *testing.T) {
		t.Parallel()
		paris, err := time.LoadLocation("Europe/Paris")
		if err != nil {
			t.Fatal(err)
		}
		// Time.zone is UTC in the lago spec.
		start, end := time.Date(2023, 9, 5, 0, 0, 0, 0, time.UTC), time.Date(2023, 9, 6, 0, 0, 0, 0, time.UTC)
		s := newScenario(t, start, scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "timezone": paris.String()})
		s.plan("plan", "INTERVAL_MONTHLY", "10", false)
		sub := s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
			"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan", "billing_time": "BILLING_TIME_ANNIVERSARY",
			"subscription_at": start.Format(time.RFC3339), "ending_at": end.Format(time.RFC3339),
		}).str("subscription.id")
		s.idle()
		if got := subscription(s, sub).str("status"); got != "SUBSCRIPTION_STATUS_ACTIVE" {
			t.Fatalf("status before the ending date: got = %s, want = SUBSCRIPTION_STATUS_ACTIVE", got)
		}
		s.at(end.Add(15 * time.Minute))
		s.trigger(jobs.KindTerminateEndedSubscriptions)
		s.idle()
		if got := subscription(s, sub).str("status"); got != "SUBSCRIPTION_STATUS_TERMINATED" {
			t.Errorf("status after the ending date: got = %s, want = SUBSCRIPTION_STATUS_TERMINATED", got)
		}
		invs := s.waitInvoices("cust", 1)
		if len(invs) != 1 {
			t.Fatalf("invoices: got = %d, want = 1", len(invs))
		}
		// 10.00 / 30 for one day.
		if got := invs[0].cents("total_amount"); got != 67 {
			t.Errorf("termination invoice total: got = %d, want = 67", got)
		}
		if got := invs[0].date("issuing_date"); got != "2023-09-06" {
			t.Errorf("termination invoice issuing date: got = %s, want = 2023-09-06", got)
		}
		s.noFailedJobs()
	})

	// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:47
	t.Run("billing at the end of the free trial", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2024, 3, 5, 12, 12, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
		s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{"name": "Count", "code": "count", "aggregation_type": "AGGREGATION_TYPE_COUNT"})
		s.api(http.MethodPost, "/v1/plans", map[string]any{
			"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "pay_in_advance": true, "amount": money("50000"),
			"trial_period_days": 10,
			"charges": []map[string]any{{
				"code": "count", "billable_metric_id": "code:count", "charge_model": "CHARGE_MODEL_STANDARD",
				"properties": map[string]any{"standard": map[string]any{"amount": "10"}},
			}},
		})
		s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
		s.idle()
		if n := len(s.invoices("cust")); n != 0 {
			t.Fatalf("invoices at the start of the trial: got = %d, want = 0", n)
		}
		for _, at := range []time.Time{time.Date(2024, 3, 10, 12, 12, 0, 0, time.UTC), time.Date(2024, 3, 15, 12, 2, 0, 0, time.UTC)} {
			s.bill(at)
			if n := len(s.invoices("cust")); n != 0 {
				t.Fatalf("invoices during the trial on %s: got = %d, want = 0", at, n)
			}
		}
		s.bill(time.Date(2024, 3, 15, 13, 2, 0, 0, time.UTC))
		invs := s.waitInvoices("cust", 1)
		if len(invs) != 1 {
			t.Fatalf("invoices after the trial: got = %d, want = 1", len(invs))
		}
		inv := s.api(http.MethodGet, "/v1/invoices/"+invs[0].str("id"), nil).at("invoice")
		if fees := inv.list("fees"); len(fees) != 1 {
			t.Errorf("fees after the trial: got = %d, want = 1", len(fees))
		}
		// (31 - 4 - 10) / 31 × 50 000.00
		if got := subscriptionFee(t, inv); got != 2_741_935 {
			t.Errorf("subscription fee after the trial: got = %d, want = 2741935", got)
		}
		s.noFailedJobs()
	})

	// source: spec/scenarios/customers/update_invoice_grace_period_spec.rb:13
	t.Run("grace period removed", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2022, 12, 15, 0, 0, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "invoice_grace_period_days": 3})
		s.plan("plan", "INTERVAL_MONTHLY", "10", true)
		s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
		s.idle()
		invs := s.waitInvoices("cust", 1)
		if len(invs) != 1 || invs[0].str("status") != "INVOICE_STATUS_DRAFT" {
			t.Fatalf("invoices in the grace period: got = %v, want = one draft", invs)
		}
		s.at(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
		s.api(http.MethodPatch, "/v1/customers/external_id%3Acust?update_mask=invoiceGracePeriodDays", map[string]any{"invoice_grace_period_days": 0})
		s.idle()
		if got := s.api(http.MethodGet, "/v1/invoices/"+invs[0].str("id"), nil).str("invoice.status"); got != "INVOICE_STATUS_FINALIZED" {
			t.Errorf("invoice after the grace period is removed: got = %s, want = INVOICE_STATUS_FINALIZED", got)
		}
		s.noFailedJobs()
	})

	// source: spec/jobs/clock/finalize_invoices_job_spec.rb
	t.Run("finalization at the end of the grace period", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2022, 12, 15, 0, 0, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "invoice_grace_period_days": 3})
		s.plan("plan", "INTERVAL_MONTHLY", "10", true)
		s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
		s.idle()
		id := s.waitInvoices("cust", 1)[0].str("id")
		for _, tt := range []struct {
			at   time.Time
			want string
		}{
			{time.Date(2022, 12, 17, 12, 0, 0, 0, time.UTC), "INVOICE_STATUS_DRAFT"},
			{time.Date(2022, 12, 18, 1, 0, 0, 0, time.UTC), "INVOICE_STATUS_FINALIZED"},
		} {
			s.at(tt.at)
			s.trigger(jobs.KindFinalizeInvoices)
			s.idle()
			if got := s.api(http.MethodGet, "/v1/invoices/"+id, nil).str("invoice.status"); got != tt.want {
				t.Errorf("invoice on %s: got = %s, want = %s", tt.at, got, tt.want)
			}
		}
		s.noFailedJobs()
	})

	// source: spec/jobs/clock/terminate_coupons_job_spec.rb
	// source: spec/jobs/clock/terminate_wallets_job_spec.rb
	t.Run("coupon and wallet expiration", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "currency": "EUR"})
		coupon := s.api(http.MethodPost, "/v1/coupons", map[string]any{
			"name": "coupon", "code": "coupon", "coupon_type": "COUPON_TYPE_FIXED_AMOUNT", "frequency": "COUPON_FREQUENCY_ONCE",
			"amount": money("10"), "expiration": "COUPON_EXPIRATION_TIME_LIMIT",
			"expiration_at": time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}).str("coupon.id")
		wallet := s.api(http.MethodPost, "/v1/wallets", map[string]any{
			"customer_id": "external_id:cust", "rate_amount": "1", "currency": "EUR", "granted_credits": "10",
			"expires_at": time.Date(2024, 3, 7, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}).str("wallet.id")
		s.idle()
		status := func() (string, string) {
			return s.api(http.MethodGet, "/v1/coupons/"+coupon, nil).str("coupon.status"),
				s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).str("wallet.status")
		}
		for _, tt := range []struct {
			at             time.Time
			coupon, wallet string
		}{
			{time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC), "COUPON_STATUS_ACTIVE", "WALLET_STATUS_ACTIVE"},
			{time.Date(2024, 3, 6, 0, 0, 0, 0, time.UTC), "COUPON_STATUS_TERMINATED", "WALLET_STATUS_ACTIVE"},
			{time.Date(2024, 3, 8, 0, 0, 0, 0, time.UTC), "COUPON_STATUS_TERMINATED", "WALLET_STATUS_TERMINATED"},
		} {
			s.at(tt.at)
			s.trigger(jobs.KindTerminateCoupons)
			s.trigger(jobs.KindTerminateWallets)
			s.idle()
			if c, w := status(); c != tt.coupon || w != tt.wallet {
				t.Errorf("on %s: got = (%s, %s), want = (%s, %s)", tt.at.Format(time.DateOnly), c, w, tt.coupon, tt.wallet)
			}
		}
		s.noFailedJobs()
	})

	// Every clock job of the schedule runs in the server; the subscription
	// activities have delayed jobs instead (§3), and the bucket repair is
	// registered only with ClickHouse.
	t.Run("every clock job", func(t *testing.T) {
		t.Parallel()
		s := newScenario(t, time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), scenarioOptions{})
		s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
		s.plan("plan", "INTERVAL_MONTHLY", "10", true)
		s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
		s.idle()
		s.at(time.Date(2024, 4, 1, 1, 0, 0, 0, time.UTC))
		for _, p := range jobs.Schedule() {
			if p.Kind == jobs.KindProcessSubscriptionActivities || p.Kind == jobs.KindRepairUsageBuckets {
				continue
			}
			s.trigger(p.Kind)
		}
		s.idle()
		s.noFailedJobs()
	})
}
