package billing_test

// Ported from lago spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:
// the usage signals of ingestion (Engine.TrackUsage) enqueue the activity
// of each subscription, whose processing evaluates the alerts.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// monScenario is the setup of subscription_alerts_spec.rb: a monthly plan
// of 49.00 with a standard charge of 5 on the sum of ops_count and of 100
// on the sum of api_count, and a subscription to it.
type monScenario struct {
	*scenario
	sub      *subscription.Subscription
	ops, api *catalog.BillableMetric
}

func newMonScenario(t *testing.T, at time.Time) *monScenario {
	t.Helper()
	s := newScenario(t, at)
	c := s.Customer()
	p := s.Plan(period.Monthly, 4900)
	ops := s.Metric("ops", catalog.AggregationTypeSum, "ops_count")
	api := s.Metric("api", catalog.AggregationTypeSum, "api_count")
	s.Charge(p, ops, catalog.ChargeModelStandard, `{"amount":"5"}`)
	s.Charge(p, api, catalog.ChargeModelStandard, `{"amount":"100"}`)
	return &monScenario{scenario: s, sub: s.Subscription(c, p), ops: ops, api: api}
}

// th is a one-time threshold of value with code.
func th(value int64, code string) alert.Threshold {
	return alert.Threshold{Code: &code, Value: money.NewFromInt(value)}
}

// rth is a recurring threshold of value with code.
func rth(value int64, code string) alert.Threshold {
	t := th(value, code)
	t.Recurring = true
	return t
}

// alert creates an alert of type typ on the subscription (lago
// create_alert); metric is nil for the alerts of no billable metric.
func (s *monScenario) alert(typ alert.Type, code string, metric *catalog.BillableMetric, ths ...alert.Threshold) *alert.Alert {
	s.T.Helper()
	now := s.Now()
	a := &alert.Alert{
		ID: core.NewID[core.AlertID](s.IDs), OrganizationID: s.OrgID(), Code: code, Type: typ,
		SubscriptionExternalID: s.sub.ExternalID, Direction: alert.DirectionIncreasing,
		CreatedAt: now, UpdatedAt: now,
	}
	if metric != nil {
		a.BillableMetricID = metric.ID
	}
	for _, t := range ths {
		t.ID = core.NewID[core.AlertThresholdID](s.IDs)
		a.Thresholds = append(a.Thresholds, t)
	}
	s.must(a.Validate())
	s.tx(func(ctx context.Context, r port.Repos) error { return r.Alerts.Create(ctx, a) })
	return a
}

// event ingests an event of metric code with properties and sends its
// usage signal (lago send_event! and Events::PostProcessService).
func (s *monScenario) event(code string, props ...string) {
	s.T.Helper()
	s.ingest(s.sub, code, props...)
	s.must(s.E.TrackUsage(s.Ctx, s.OrgID(), s.sub.CustomerID, []core.SubscriptionID{s.sub.ID}))
}

// usageUpdate is lago perform_usage_update: it moves the clock to the end
// of the current activity interval, so the tracked activities are due,
// and runs the lifetime usage sweep and the due jobs.
func (s *monScenario) usageUpdate() {
	s.T.Helper()
	_, at := kernel.ActivityKey(s.sub.ID, s.Now())
	s.travelTo(at)
	s.run(kernel.JobRefreshLifetimeUsages, struct{}{})
}

// triggered returns the triggered alerts of a, oldest first.
func (s *monScenario) triggered(a *alert.Alert) []*alert.TriggeredAlert {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*alert.TriggeredAlert, error) {
		return kernel.All(func(p core.PageRequest) (core.Page[*alert.TriggeredAlert], error) {
			return r.TriggeredAlerts.List(ctx, s.OrgID(), alert.TriggeredFilter{AlertID: a.ID}, p)
		})
	})
}

// pendingActivities counts the distinct activities not yet run: the
// unique key of an activity makes a second enqueue in its interval a
// no-op while the first one waits (River's unique jobs), which the
// harness, having taken the first job, does not see.
func (s *monScenario) pendingActivities() int {
	keys := map[string]bool{}
	for _, j := range append(s.Store.Jobs(), s.future...) {
		if j.Kind == kernel.JobProcessSubscriptionActivity {
			keys[j.UniqueKey] = true
		}
	}
	return len(keys)
}

// alertWebhooks counts the alert.triggered events.
func (s *monScenario) alertWebhooks() int {
	return monCount(s.Webhooks(), webhook.EventTypeAlertTriggered)
}

func monCount[T comparable](xs []T, x T) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// crossed renders the crossed thresholds of t as code:value:recurring.
func crossed(t *alert.TriggeredAlert) []string {
	var out []string
	for _, c := range t.CrossedThresholds {
		code := ""
		if c.Code != nil {
			code = *c.Code
		}
		r := "once"
		if c.Recurring {
			r = "recurring"
		}
		out = append(out, code+":"+c.Value.String()+":"+r)
	}
	return out
}

func monDecimal(t *testing.T, name string, got money.Decimal, want int64) {
	t.Helper()
	if !got.Equal(money.NewFromInt(want)) {
		t.Errorf("%s: got = %s, want = %d", name, got, want)
	}
}

func monStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: got = %v, want = %v", name, got, want)
	}
}

func TestMonitoringScenarioSubscriptionAlerts(t *testing.T) {
	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:33
	t.Run("monitors activity and triggers alerts", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		a := s.alert(alert.TypeCurrentUsageAmount, "simple", nil, th(1500, "warn"), th(3000, "warn"), th(5000, "alert"), th(123000, "block"))
		bm := s.alert(alert.TypeBillableMetricCurrentUsageAmount, "bm", s.api, th(1500, "warn"), th(3000, "warn"), th(5000, "alert"), th(123000, "block"))

		s.event(s.ops.Code, "ops_count", "2")
		if got := s.pendingActivities(); got != 1 {
			t.Errorf("activities after the first event: got = %d, want = 1", got)
		}
		s.usageUpdate()
		if got := len(s.triggered(a)); got != 0 {
			t.Errorf("triggered alerts at 10.00: got = %d, want = 0", got)
		}

		s.event(s.ops.Code, "ops_count", "2")
		s.event(s.ops.Code, "ops_count", "2")
		if got := s.pendingActivities(); got != 1 {
			t.Errorf("activities after two events in one interval: got = %d, want = 1", got)
		}
		s.usageUpdate()
		if got := s.pendingActivities(); got != 0 {
			t.Errorf("activities after the usage update: got = %d, want = 0", got)
		}
		ts := s.triggered(a)
		if len(ts) != 1 {
			t.Fatalf("triggered alerts at 30.00: got = %d, want = 1", len(ts))
		}
		monDecimal(t, "current_value", ts[0].CurrentValue, 3000)
		monDecimal(t, "previous_value", ts[0].PreviousValue, 1000)
		monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"warn:1500:once", "warn:3000:once"})
		if got := s.alertWebhooks(); got != 1 {
			t.Errorf("alert.triggered webhooks: got = %d, want = 1", got)
		}

		s.event(s.api.Code, "api_count", "4")
		s.usageUpdate()
		if got := len(s.triggered(a)); got != 2 {
			t.Errorf("triggered alerts of the usage alert: got = %d, want = 2", got)
		}
		if got := len(s.triggered(bm)); got != 1 {
			t.Errorf("triggered alerts of the metric alert: got = %d, want = 1", got)
		}
		if got := s.alertWebhooks(); got != 3 {
			t.Errorf("alert.triggered webhooks: got = %d, want = 3", got)
		}
	})

	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:109
	t.Run("recurring thresholds send alerts forever", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		a := s.alert(alert.TypeCurrentUsageAmount, "simple", nil, th(1500, "warn"), th(3000, "warn"), rth(1000, "alert"))
		s.event(s.ops.Code, "ops_count", "7")
		s.usageUpdate()
		ts := s.triggered(a)
		if len(ts) != 1 {
			t.Fatalf("triggered alerts: got = %d, want = 1", len(ts))
		}
		monDecimal(t, "current_value", ts[0].CurrentValue, 3500)
		monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"warn:1500:once", "warn:3000:once"})

		s.event(s.ops.Code, "ops_count", "4")
		s.usageUpdate()
		ts = s.triggered(a)
		if len(ts) != 2 {
			t.Fatalf("triggered alerts: got = %d, want = 2", len(ts))
		}
		monDecimal(t, "current_value", ts[1].CurrentValue, 5500)
		monStrings(t, "crossed_thresholds", crossed(ts[1]), []string{"alert:4000:recurring", "alert:5000:recurring"})
	})

	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:150
	t.Run("only a recurring threshold", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		a := s.alert(alert.TypeCurrentUsageAmount, "recurring_only", nil, rth(1000, "alert"))
		s.event(s.ops.Code, "ops_count", "7")
		s.usageUpdate()
		ts := s.triggered(a)
		if len(ts) != 1 {
			t.Fatalf("triggered alerts: got = %d, want = 1", len(ts))
		}
		monDecimal(t, "current_value", ts[0].CurrentValue, 3500)
		monDecimal(t, "previous_value", ts[0].PreviousValue, 0)
		monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"alert:1000:recurring", "alert:2000:recurring", "alert:3000:recurring"})
		evs := s.Emitted.Events()
		i := slices.IndexFunc(evs, func(e webhook.Event) bool { return e.Type == webhook.EventTypeAlertTriggered })
		if i < 0 {
			t.Fatalf("alert.triggered: got = none, want = one")
		}
		if got, want := evs[i].ObjectType, "triggered_alert"; got != want {
			t.Errorf("object_type: got = %q, want = %q", got, want)
		}

		s.event(s.ops.Code, "ops_count", "4")
		s.usageUpdate()
		ts = s.triggered(a)
		if len(ts) != 2 {
			t.Fatalf("triggered alerts: got = %d, want = 2", len(ts))
		}
		monDecimal(t, "current_value", ts[1].CurrentValue, 5500)
		monDecimal(t, "previous_value", ts[1].PreviousValue, 3500)
		monStrings(t, "crossed_thresholds", crossed(ts[1]), []string{"alert:4000:recurring", "alert:5000:recurring"})
	})

	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:203
	t.Run("billable_metric_current_usage_units", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		a := s.alert(alert.TypeBillableMetricCurrentUsageUnits, "bm_units", s.ops, th(90, "warn"))
		s.event(s.ops.Code, "ops_count", "89")
		s.usageUpdate()
		if got := len(s.triggered(a)); got != 0 {
			t.Errorf("triggered alerts at 89: got = %d, want = 0", got)
		}
		s.event(s.ops.Code, "ops_count", "5")
		s.usageUpdate()
		ts := s.triggered(a)
		if len(ts) != 1 {
			t.Fatalf("triggered alerts at 94: got = %d, want = 1", len(ts))
		}
		monDecimal(t, "current_value", ts[0].CurrentValue, 94)
		monDecimal(t, "previous_value", ts[0].PreviousValue, 89)
		monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"warn:90:once"})
	})

	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:251
	t.Run("lifetime_usage_amount", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		a := s.alert(alert.TypeLifetimeUsageAmount, "lifetime", nil, th(15000, "warn"))
		for _, month := range []time.Time{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)} {
			s.travelTo(month.AddDate(0, 0, 5))
			s.event(s.ops.Code, "ops_count", "10")
			s.usageUpdate()
			s.travelTo(month.AddDate(0, 1, 0))
			s.performBilling()
			if got := len(s.triggered(a)); got != 0 {
				t.Errorf("triggered alerts after billing %s: got = %d, want = 0", month.Format("2006-01"), got)
			}
		}
		s.travelTo(time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC))
		s.event(s.ops.Code, "ops_count", "11")
		s.usageUpdate()
		ts := s.triggered(a)
		if len(ts) != 1 {
			t.Fatalf("triggered alerts: got = %d, want = 1", len(ts))
		}
		monDecimal(t, "current_value", ts[0].CurrentValue, 15500)
		monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"warn:15000:once"})
	})

	// source: spec/scenarios/usage_monitoring/subscription_alerts_spec.rb:292
	t.Run("no alert", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		s.event(s.ops.Code, "ops_count", "20")
		s.usageUpdate()
		if got := s.alertWebhooks(); got != 0 {
			t.Errorf("alert.triggered webhooks: got = %d, want = 0", got)
		}
	})
}

func TestMonitoringActivityOfInactiveSubscription(t *testing.T) {
	// source: app/services/usage_monitoring/process_subscription_activity_service.rb:15
	s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
	a := s.alert(alert.TypeCurrentUsageAmount, "simple", nil, th(1000, "warn"))
	s.event(s.ops.Code, "ops_count", "4")
	s.UpdateSubscription(s.sub, func(sub *subscription.Subscription) { sub.Status = subscription.StatusTerminated })
	s.usageUpdate()
	if got := len(s.triggered(a)); got != 0 {
		t.Errorf("triggered alerts: got = %d, want = 0", got)
	}
}

func TestMonitoringTrackUsageRefreshesWallets(t *testing.T) {
	// source: app/services/events/post_process_service.rb:22 (flag_wallets_for_refresh)
	tests := []struct {
		name   string
		wallet bool
		want   int
	}{
		{"customer with an active wallet", true, 1},
		{"customer without a wallet", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
			if tt.wallet {
				s.Wallet(s.ReloadCustomer(s.sub.CustomerID), "10")
			}
			s.Store.TakeJobs()
			s.must(s.E.TrackUsage(s.Ctx, s.OrgID(), s.sub.CustomerID, []core.SubscriptionID{s.sub.ID}))
			kinds := s.JobKinds()
			if got := monCount(kinds, kernel.JobProcessSubscriptionActivity); got != 1 {
				t.Errorf("activity jobs: got = %d, want = 1", got)
			}
			if got := len(kinds) - 1; got != tt.want {
				t.Errorf("wallet refresh jobs: got = %d (%v), want = %d", got, kinds, tt.want)
			}
		})
	}
}

func TestMonitoringSweeps(t *testing.T) {
	// source: spec/jobs/clock/refresh_wallets_ongoing_balance_job_spec.rb:35,40
	t.Run("ongoing balances", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		flagged := s.ReloadCustomer(s.sub.CustomerID)
		w := s.Wallet(flagged, "10")
		notFlagged := s.Customer()
		s.Wallet(notFlagged, "1")
		withoutWallet := s.Customer()
		withTerminated := s.Customer()
		terminated := s.Wallet(withTerminated, "1")
		s.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []core.CustomerID{flagged.ID, withoutWallet.ID, withTerminated.ID} {
				if err := r.Customers.FlagWalletRefresh(ctx, s.OrgID(), c); err != nil {
					return err
				}
			}
			if err := terminated.Terminate(s.Now()); err != nil {
				return err
			}
			return r.Wallets.Update(ctx, terminated)
		})
		s.Store.TakeJobs()
		n, err := s.E.RefreshOngoingBalances(s.Ctx, s.OrgID())
		s.must(err)
		if n != 1 {
			t.Errorf("customers refreshed: got = %d, want = 1", n)
		}
		jobs := s.Store.TakeJobs()
		if len(jobs) != 1 || jobs[0].Kind != kernel.JobRefreshWalletOngoingBalance || jobs[0].UniqueKey != flagged.ID.String() {
			t.Errorf("jobs: got = %v, want = one %s for customer %s", jobs, kernel.JobRefreshWalletOngoingBalance, flagged.ID)
		}

		// A refresh clears the flag (refresh_wallets_service.rb:36), and the
		// next sweep finds nothing.
		if _, err := s.E.Wallets.RefreshOngoingBalances(s.Ctx, s.OrgID(), w.CustomerID); err != nil {
			t.Fatalf("RefreshOngoingBalances() error = %v", err)
		}
		s.Store.TakeJobs()
		n, err = s.E.RefreshOngoingBalances(s.Ctx, s.OrgID())
		s.must(err)
		if n != 0 {
			t.Errorf("customers refreshed after a refresh: got = %d, want = 0", n)
		}
	})
	// source: app/jobs/clock/refresh_lifetime_usages_job.rb:9
	t.Run("lifetime usages", func(t *testing.T) {
		s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
		s.ingest(s.sub, s.ops.Code, "ops_count", "1")
		s.tx(func(ctx context.Context, r port.Repos) error {
			lu, err := r.LifetimeUsages.GetBySubscription(ctx, s.OrgID(), s.sub.ID)
			if err != nil {
				return err
			}
			lu.RecalculateInvoicedUsage = true
			return r.LifetimeUsages.Upsert(ctx, lu)
		})
		s.Store.TakeJobs()
		n, err := s.E.RefreshLifetimeUsages(s.Ctx, s.OrgID())
		s.must(err)
		if n != 1 {
			t.Errorf("lifetime usages refreshed: got = %d, want = 1", n)
		}
		if got := monCount(s.JobKinds(), kernel.JobRefreshLifetimeUsage); got != 1 {
			t.Errorf("refresh jobs: got = %d (%v), want = 1", got, s.JobKinds())
		}
	})
}
