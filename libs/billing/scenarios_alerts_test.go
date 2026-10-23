package billing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// storeAlert stores an alert built by fn from a valid base alert.
func (s *scenario) storeAlert(fn func(a *alert.Alert)) *alert.Alert {
	s.T.Helper()
	now := s.Now()
	a := &alert.Alert{
		ID: core.NewID[core.AlertID](s.IDs), OrganizationID: s.OrgID(), Code: "alert",
		Direction: alert.DirectionIncreasing, CreatedAt: now, UpdatedAt: now,
	}
	fn(a)
	for i := range a.Thresholds {
		a.Thresholds[i].ID = core.NewID[core.AlertThresholdID](s.IDs)
	}
	s.must(a.Validate())
	s.tx(func(ctx context.Context, r port.Repos) error { return r.Alerts.Create(ctx, a) })
	return a
}

func TestProcessWalletAlerts(t *testing.T) {
	// The wallet has a balance and an ongoing balance of 400 (4 credits).
	tests := []struct {
		name      string
		source    string
		typ       alert.Type
		dir       alert.Direction
		previous  int64
		ths       []alert.Threshold
		wantValue string
		crossed   []string
	}{
		{
			name: "balance amount crossing", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:21",
			typ: alert.TypeWalletBalanceAmount, dir: alert.DirectionDecreasing, previous: 1000,
			ths: []alert.Threshold{th(500, "warn500"), th(800, "warn800")}, wantValue: "400",
			crossed: []string{"warn500:500:once", "warn800:800:once"},
		},
		{
			name: "credits balance", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:60",
			typ: alert.TypeWalletCreditsBalance, dir: alert.DirectionDecreasing, previous: 15,
			ths: []alert.Threshold{th(5, "warn5"), th(10, "warn10")}, wantValue: "4",
			crossed: []string{"warn5:5:once", "warn10:10:once"},
		},
		{
			name: "ongoing balance amount", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:85",
			typ: alert.TypeWalletOngoingBalanceAmount, dir: alert.DirectionDecreasing, previous: 1000,
			ths: []alert.Threshold{th(500, "warn500"), th(800, "warn800")}, wantValue: "400",
			crossed: []string{"warn500:500:once", "warn800:800:once"},
		},
		{
			name: "credits ongoing balance", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:111",
			typ: alert.TypeWalletCreditsOngoingBalance, dir: alert.DirectionDecreasing, previous: 15,
			ths: []alert.Threshold{th(5, "warn5"), th(10, "warn10")}, wantValue: "4",
			crossed: []string{"warn5:5:once", "warn10:10:once"},
		},
		{
			name: "no threshold crossed", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:138",
			typ: alert.TypeWalletBalanceAmount, dir: alert.DirectionDecreasing, previous: 500,
			ths: []alert.Threshold{th(100, "warn100"), th(200, "warn200")}, wantValue: "400",
		},
		{
			name: "increasing direction", source: "spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:166",
			typ: alert.TypeWalletBalanceAmount, dir: alert.DirectionIncreasing, previous: 200,
			ths: []alert.Threshold{th(300, "warn300"), th(500, "warn500")}, wantValue: "400",
			crossed: []string{"warn300:300:once"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC))
			w := s.Wallet(s.Customer(), "4")
			a := s.storeAlert(func(a *alert.Alert) {
				a.Type, a.WalletID, a.Direction, a.PreviousValue, a.Thresholds = tt.typ, w.ID, tt.dir, money.NewFromInt(tt.previous), tt.ths
			})
			s.must(s.E.ProcessWalletAlerts(s.Ctx, s.OrgID(), w.ID))

			got := s.reloadAlert(a)
			want := money.MustParse(tt.wantValue)
			if !got.PreviousValue.Equal(want) {
				t.Errorf("previous_value: got = %s, want = %s", got.PreviousValue, want)
			}
			if got.LastProcessedAt == nil || !got.LastProcessedAt.Equal(s.Now()) {
				t.Errorf("last_processed_at: got = %v, want = %s", got.LastProcessedAt, s.Now())
			}
			ts := s.alertTriggered(a)
			webhooks := monCount(s.Webhooks(), webhook.EventTypeAlertTriggered)
			if tt.crossed == nil {
				if len(ts) != 0 || webhooks != 0 {
					t.Errorf("triggered alerts, webhooks: got = %d, %d, want = 0, 0", len(ts), webhooks)
				}
				return
			}
			if len(ts) != 1 || webhooks != 1 {
				t.Fatalf("triggered alerts, webhooks: got = %d, %d, want = 1, 1", len(ts), webhooks)
			}
			ta := ts[0]
			if ta.WalletID != w.ID || !ta.SubscriptionID.IsZero() {
				t.Errorf("alertable: got = wallet %s, subscription %s, want = wallet %s, none", ta.WalletID, ta.SubscriptionID, w.ID)
			}
			if !ta.CurrentValue.Equal(want) || !ta.PreviousValue.Equal(money.NewFromInt(tt.previous)) {
				t.Errorf("values: got = %s/%s, want = %s/%d", ta.CurrentValue, ta.PreviousValue, want, tt.previous)
			}
			monStrings(t, "crossed_thresholds", crossed(ta), tt.crossed)
		})
	}

	// source: spec/services/usage_monitoring/process_wallet_alerts_service_spec.rb:13
	t.Run("no alert", func(t *testing.T) {
		s := newScenario(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC))
		w := s.Wallet(s.Customer(), "4")
		s.must(s.E.ProcessWalletAlerts(s.Ctx, s.OrgID(), w.ID))
		if got := monCount(s.Webhooks(), webhook.EventTypeAlertTriggered); got != 0 {
			t.Errorf("webhooks: got = %d, want = 0", got)
		}
	})
}

// walletAlertJobs counts the enqueued wallet alert jobs of w.
func (s *scenario) walletAlertJobs(w *wallet.Wallet) int {
	n := 0
	for _, j := range s.Store.TakeJobs() {
		if j.Kind == kernel.JobProcessWalletAlerts && j.UniqueKey == "wallet:"+w.ID.String() {
			n++
		}
	}
	return n
}

func TestWalletAlertsEnqueued(t *testing.T) {
	tests := []struct {
		name   string
		source string
		change func(s *scenario, w *wallet.Wallet)
		want   int
	}{
		{
			name: "balance increase", source: "spec/services/wallets/balance/increase_service_spec.rb:51",
			change: func(s *scenario, w *wallet.Wallet) {
				s.tx(func(ctx context.Context, r port.Repos) error {
					return kernel.IncreaseWalletBalance(ctx, s.Deps, r, w, &wallet.Transaction{Amount: 100, CreditAmount: money.One}, false)
				})
			},
			want: 1,
		},
		{
			name: "balance decrease", source: "spec/services/wallets/balance/decrease_service_spec.rb:55",
			change: func(s *scenario, w *wallet.Wallet) {
				s.tx(func(ctx context.Context, r port.Repos) error {
					return kernel.DecreaseWalletBalance(ctx, s.Deps, r, w, &wallet.Transaction{Amount: 100, CreditAmount: money.One})
				})
			},
			want: 1,
		},
		{
			name: "ongoing balance refresh", source: "spec/services/wallets/balance/update_ongoing_service_spec.rb:66",
			change: func(s *scenario, w *wallet.Wallet) {
				_, err := s.E.Wallets.RefreshOngoingBalances(s.Ctx, s.OrgID(), w.CustomerID)
				s.must(err)
			},
			want: 1,
		},
		{
			name: "alert saved on an active wallet", source: "app/services/usage_monitoring/create_alert_service.rb:146",
			change: func(s *scenario, w *wallet.Wallet) {
				s.must(s.E.AlertSaved(s.Ctx, s.OrgID(), &alert.Alert{OrganizationID: s.OrgID(), Type: alert.TypeWalletBalanceAmount, WalletID: w.ID}))
			},
			want: 1,
		},
		{
			name: "alert saved on a terminated wallet", source: "app/services/usage_monitoring/create_alert_service.rb:148",
			change: func(s *scenario, w *wallet.Wallet) {
				w = s.ReloadWallet(w.ID)
				w.Status = wallet.StatusTerminated
				s.tx(func(ctx context.Context, r port.Repos) error { return r.Wallets.Update(ctx, w) })
				s.must(s.E.AlertSaved(s.Ctx, s.OrgID(), &alert.Alert{OrganizationID: s.OrgID(), Type: alert.TypeWalletBalanceAmount, WalletID: w.ID}))
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario(t, time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC))
			w := s.Wallet(s.Customer(), "4")
			s.Store.TakeJobs()
			tt.change(s, s.ReloadWallet(w.ID))
			if got := s.walletAlertJobs(w); got != tt.want {
				t.Errorf("wallet alert jobs: got = %d, want = %d", got, tt.want)
			}
		})
	}
}

// lifetimeJobs returns the delayed lifetime usage alert jobs.
func (s *monScenario) lifetimeJobs() []port.Job {
	var out []port.Job
	for _, j := range append(s.Store.Jobs(), s.future...) {
		if j.Kind == kernel.JobProcessLifetimeUsageAlert {
			out = append(out, j)
		}
	}
	return out
}

func TestLifetimeUsageAlertEnqueued(t *testing.T) {
	// source: spec/services/usage_monitoring/process_subscription_activity_service_spec.rb:190
	s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
	a := s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "lifetime_units", s.ops, th(10, "warn"))
	s.event(s.ops.Code, "ops_count", "2")
	_, at := kernel.ActivityKey(s.sub.ID, s.Now())
	s.usageUpdate()
	js := s.lifetimeJobs()
	if len(js) != 1 {
		t.Fatalf("lifetime usage alert jobs: got = %d, want = 1", len(js))
	}
	if got, want := js[0].RunAt, at.Add(kernel.ActivityInterval); !got.Equal(want) {
		t.Errorf("run_at: got = %s, want = %s", got, want)
	}
	if got, want := js[0].UniqueKey, kernel.LifetimeUsageAlertKey(a.ID); got != want {
		t.Errorf("unique key: got = %q, want = %q", got, want)
	}
	if got := s.reloadAlert(a).LastProcessedAt; got != nil {
		t.Errorf("last_processed_at before the job: got = %s, want = nil", got)
	}
}

func TestLifetimeUsageAlertScenario(t *testing.T) {
	// The units since the subscription started cross the threshold while
	// the current period alone does not (lago full_usage,
	// app/services/usage_monitoring/process_lifetime_usage_alert_service.rb:22).
	s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
	a := s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "lifetime_units", s.ops, th(10, "warn"))
	settle := func() {
		s.usageUpdate()
		s.travelTo(s.Now().Add(kernel.ActivityInterval))
		s.drain()
	}
	s.event(s.ops.Code, "ops_count", "6")
	settle()
	if got := len(s.triggered(a)); got != 0 {
		t.Errorf("triggered alerts at 6: got = %d, want = 0", got)
	}
	monDecimal(t, "previous_value", s.reloadAlert(a).PreviousValue, 6)

	s.travelTo(time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC))
	s.performBilling()
	s.travelTo(time.Date(2024, 4, 3, 0, 0, 0, 0, time.UTC))
	s.event(s.ops.Code, "ops_count", "5")
	settle()
	ts := s.triggered(a)
	if len(ts) != 1 {
		t.Fatalf("triggered alerts at 11: got = %d, want = 1", len(ts))
	}
	monDecimal(t, "current_value", ts[0].CurrentValue, 11)
	monDecimal(t, "previous_value", ts[0].PreviousValue, 6)
	monStrings(t, "crossed_thresholds", crossed(ts[0]), []string{"warn:10:once"})
	if ts[0].SubscriptionID != s.sub.ID || !ts[0].WalletID.IsZero() {
		t.Errorf("alertable: got = subscription %s, wallet %s, want = subscription %s, none", ts[0].SubscriptionID, ts[0].WalletID, s.sub.ID)
	}
}

func TestProcessLifetimeUsageAlert(t *testing.T) {
	tests := []struct {
		name   string
		source string
		// setup returns the alert to process; it may change the scenario.
		setup func(s *monScenario) *alert.Alert
		// wantProcessed reports whether the alert is evaluated.
		wantProcessed bool
		wantErr       error
	}{
		{
			name: "evaluates the alert", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:28",
			setup: func(s *monScenario) *alert.Alert {
				return s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", s.ops, th(10, "warn"))
			},
			wantProcessed: true,
		},
		{
			name: "alert deleted", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:66",
			setup: func(s *monScenario) *alert.Alert {
				a := s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", s.ops, th(10, "warn"))
				s.tx(func(ctx context.Context, r port.Repos) error { return r.Alerts.Discard(ctx, s.OrgID(), a.ID, s.Now()) })
				return nil
			},
		},
		{
			name: "another alert type", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:101",
			setup: func(s *monScenario) *alert.Alert {
				return s.alert(alert.TypeCurrentUsageAmount, "u", nil, th(10, "warn"))
			},
		},
		{
			name: "no matching charge", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:132",
			setup: func(s *monScenario) *alert.Alert {
				m := s.Metric("other", catalog.AggregationTypeSum, "other_count")
				return s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", m, th(10, "warn"))
			},
		},
		{
			name: "subscription no longer active", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:145",
			setup: func(s *monScenario) *alert.Alert {
				a := s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", s.ops, th(10, "warn"))
				s.UpdateSubscription(s.sub, func(sub *subscription.Subscription) { sub.Status = subscription.StatusTerminated })
				return a
			},
		},
		{
			name: "full usage not allowed", source: "spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:84",
			setup: func(s *monScenario) *alert.Alert {
				m := s.Metric("seats", catalog.AggregationTypeSum, "seats", func(m *catalog.BillableMetric) { m.Recurring = true })
				plan := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
					return r.Plans.Get(ctx, s.OrgID(), s.sub.PlanID)
				})
				s.Charge(plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Prorated = true })
				return s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", m, th(10, "warn"))
			},
			wantErr: usage.ErrFullUsageNotAllowed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
			s.ingest(s.sub, s.ops.Code, "ops_count", "12")
			a := tt.setup(s)
			id := core.AlertID{}
			if a != nil {
				id = a.ID
			}
			err := s.E.ProcessLifetimeUsageAlert(s.Ctx, s.OrgID(), id, s.sub.ID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("error: got = %v, want = nil", err)
			}
			if a == nil {
				return
			}
			processed := s.reloadAlert(a).LastProcessedAt != nil
			if processed != tt.wantProcessed {
				t.Errorf("processed: got = %t, want = %t", processed, tt.wantProcessed)
			}
			if tt.wantProcessed {
				ts := s.triggered(a)
				if len(ts) != 1 || !slices.Equal(crossed(ts[0]), []string{"warn:10:once"}) {
					t.Errorf("triggered alerts: got = %d, want = 1 crossing warn:10", len(ts))
				}
			}
		})
	}
}

// afterTx is a [port.Transactor] running hook once, after its n-th
// transaction committed.
type afterTx struct {
	inner port.Transactor
	n     int
	hook  func()
}

func (t *afterTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if err := t.inner.InTx(ctx, fn); err != nil {
		return err
	}
	if t.n--; t.n == 0 {
		t.hook()
	}
	return nil
}

// source: spec/services/usage_monitoring/process_lifetime_usage_alert_service_spec.rb:47-64
func TestProcessLifetimeUsageAlertMetricChanged(t *testing.T) {
	for _, tt := range []struct {
		name          string
		change        func(s *monScenario, a *alert.Alert)
		wantProcessed bool
	}{
		{"unchanged", func(*monScenario, *alert.Alert) {}, true},
		// "skips the evaluation rather than measuring the new metric
		// against the old usage"
		{"metric changed", func(s *monScenario, a *alert.Alert) {
			s.tx(func(ctx context.Context, r port.Repos) error {
				a.BillableMetricID = s.api.ID
				return r.Alerts.Update(ctx, a)
			})
		}, false},
		// "when the alert is deleted while the usage is being built"
		{"alert deleted", func(s *monScenario, a *alert.Alert) {
			s.tx(func(ctx context.Context, r port.Repos) error { return r.Alerts.Discard(ctx, s.OrgID(), a.ID, s.Now()) })
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newMonScenario(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC))
			s.ingest(s.sub, s.ops.Code, "ops_count", "12")
			a := s.alert(alert.TypeBillableMetricLifetimeUsageUnits, "u", s.ops, th(10, "warn"))
			// The second transaction computes the usage; the change lands
			// before the third evaluates the alert.
			d := *s.Deps
			d.Tx = &afterTx{inner: s.Deps.Tx, n: 2, hook: func() { tt.change(s, a) }}
			e, err := billing.New(d)
			if err != nil {
				t.Fatalf("billing.New() error = %v", err)
			}
			if err := e.ProcessLifetimeUsageAlert(s.Ctx, s.OrgID(), a.ID, s.sub.ID); err != nil {
				t.Fatalf("ProcessLifetimeUsageAlert() error got = %v, want = nil", err)
			}
			if tt.name == "alert deleted" {
				if got := len(s.triggered(a)); got != 0 {
					t.Errorf("triggered alerts got = %d, want = 0", got)
				}
				return
			}
			if got := s.reloadAlert(a).LastProcessedAt != nil; got != tt.wantProcessed {
				t.Errorf("processed got = %t, want = %t", got, tt.wantProcessed)
			}
			if got, want := len(s.triggered(a)), map[bool]int{true: 1, false: 0}[tt.wantProcessed]; got != want {
				t.Errorf("triggered alerts got = %d, want = %d", got, want)
			}
		})
	}
}
