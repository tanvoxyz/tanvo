package alerts_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/alerts"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

type fixture struct {
	h   *billingtest.Harness
	s   *alerts.Service
	sub *subscription.Subscription
	w   *wallet.Wallet
}

func setup(t *testing.T) fixture {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := alerts.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	c := h.Customer()
	return fixture{h: h, s: s, sub: h.Subscription(c, h.Plan(period.Monthly, 1000)), w: h.Wallet(c, "100")}
}

func (f fixture) subOwner() alerts.Owner { return alerts.Owner{SubscriptionID: f.sub.ID} }

func (f fixture) walletOwner() alerts.Owner { return alerts.Owner{WalletID: f.w.ID} }

func ptr[T any](v T) *T { return &v }

func th(value string, recurring bool) alert.Threshold {
	return alert.Threshold{Value: money.MustParse(value), Recurring: recurring}
}

func usageInput(code string, ths ...alert.Threshold) alerts.CreateInput {
	if len(ths) == 0 {
		ths = []alert.Threshold{th("1000", false)}
	}
	return alerts.CreateInput{Type: alert.TypeCurrentUsageAmount, Code: code, Name: ptr("Name"), Thresholds: ths}
}

func walletInput(typ alert.Type, code string) alerts.CreateInput {
	return alerts.CreateInput{Type: typ, Code: code, Thresholds: []alert.Threshold{th("10", false)}}
}

// wantFieldError fails unless err carries the field error.
func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) {
		t.Fatalf("errors = %v, want %s %s", ve.Errors, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want %s not found", err, resource)
	}
}

func (f fixture) alertCount() int {
	return len(billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*alert.Alert, error) {
		a, err := r.Alerts.ListBySubscription(ctx, f.h.OrgID(), f.sub.ExternalID)
		if err != nil {
			return nil, err
		}
		b, err := r.Alerts.ListByWallet(ctx, f.h.OrgID(), f.w.ID)
		return append(a, b...), err
	}))
}

func (f fixture) jobs(kind string) int {
	return len(slices.DeleteFunc(f.h.JobKinds(), func(k string) bool { return k != kind }))
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:16
	t.Run("creates a subscription alert and tracks the subscription activity", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		v, err := f.s.Create(context.Background(), f.h.OrgID(), alerts.Owner{SubscriptionExternalID: f.sub.ExternalID}, usageInput("first", th("1000", false), th("2000", false)))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if v.SubscriptionID != f.sub.ID || v.SubscriptionExternalID != f.sub.ExternalID || v.Direction != alert.DirectionIncreasing || !v.PreviousValue.IsZero() || len(v.Thresholds) != 2 {
			t.Fatalf("alert = %+v, want an increasing alert of the subscription with 2 thresholds", v.Alert)
		}
		if got := f.jobs(kernel.JobProcessSubscriptionActivity); got != 1 {
			t.Fatalf("activity jobs = %d, want 1", got)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:34
	t.Run("creates an alert with a recurring threshold", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		v, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), usageInput("first", th("1000", false), th("500", true)))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if _, ok := v.RecurringThreshold(); !ok {
			t.Fatalf("thresholds = %v, want a recurring one", v.Thresholds)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:45
	t.Run("rejects a code already used", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		if _, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), alerts.CreateInput{Type: alert.TypeLifetimeUsageAmount, Code: "dup", Thresholds: []alert.Threshold{th("1", false)}}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		_, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), usageInput("dup"))
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:53
	t.Run("sets the billable metric of a billable_metric type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m := f.h.Metric("calls", catalog.AggregationTypeCount, "")
		in := usageInput("bm")
		in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageAmount, alerts.MetricRef{Code: m.Code}
		v, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), in)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if v.BillableMetricID != m.ID {
			t.Fatalf("billable metric = %s, want %s", v.BillableMetricID, m.ID)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:65
	t.Run("requires the billable metric of a billable_metric type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		in := usageInput("bm")
		in.Type = alert.TypeBillableMetricCurrentUsageAmount
		_, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), in)
		wantFieldError(t, err, "billable_metric_id", core.CodeRequired)
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:75
	t.Run("rejects a billable metric that does not exist", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		in := usageInput("bm")
		in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageAmount, alerts.MetricRef{Code: "missing"}
		_, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), in)
		wantNotFound(t, err, "billable_metric")
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:86
	t.Run("does not track the activity of a subscription that is not active", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		f.h.UpdateSubscription(f.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusTerminated })
		if _, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), usageInput("first")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got := f.jobs(kernel.JobProcessSubscriptionActivity); got != 0 {
			t.Fatalf("activity jobs = %d, want 0", got)
		}
	})
	t.Run("finds only an active subscription by external ID", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		f.h.UpdateSubscription(f.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusTerminated })
		_, err := f.s.Create(context.Background(), f.h.OrgID(), alerts.Owner{SubscriptionExternalID: f.sub.ExternalID}, usageInput("first"))
		wantNotFound(t, err, "subscription")
	})

	invalid := []struct {
		name, source string
		in           alerts.CreateInput
		field, code  string
	}{
		{"blank code", "spec/services/usage_monitoring/create_alert_service_spec.rb:115", usageInput(""), "code", core.CodeRequired},
		{"blank alert type", "spec/services/usage_monitoring/create_alert_service_spec.rb:124", alerts.CreateInput{Code: "x", Thresholds: []alert.Threshold{th("1", false)}}, "alert_type", core.CodeRequired},
		{"blank thresholds", "spec/services/usage_monitoring/create_alert_service_spec.rb:133", alerts.CreateInput{Type: alert.TypeCurrentUsageAmount, Code: "x"}, "thresholds", core.CodeRequired},
		{"duplicate threshold values", "spec/services/usage_monitoring/create_alert_service_spec.rb:142", usageInput("x", th("10", false), th("10", false)), "thresholds", alert.CodeDuplicateThresholdValues},
		{"negative recurring value", "spec/services/usage_monitoring/create_alert_service_spec.rb:313", usageInput("x", th("10", false), th("-5", true)), "thresholds[1].value", alert.CodeRecurringValueIsNegative},
		{"unknown alert type", "spec/services/usage_monitoring/create_alert_service_spec.rb:340", alerts.CreateInput{Type: "unknown", Code: "x", Thresholds: []alert.Threshold{th("1", false)}}, "alert_type", alert.CodeInvalidType},
		{"wallet alert type", "spec/services/usage_monitoring/create_alert_service_spec.rb:349", walletInput(alert.TypeWalletBalanceAmount, "x"), "alert_type", alert.CodeInvalidType},
		{"too many thresholds", "spec/services/usage_monitoring/create_alert_service_spec.rb:358", usageInput("x", slices.Collect(func(yield func(alert.Threshold) bool) {
			for i := range 21 {
				if !yield(alert.Threshold{Value: money.NewFromInt(int64(i))}) {
					return
				}
			}
		})...), "thresholds", alert.CodeTooManyThresholds},
	}
	for _, tc := range invalid {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			t.Parallel()
			t.Log("source:", tc.source)
			f := setup(t)
			_, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), tc.in)
			wantFieldError(t, err, tc.field, tc.code)
			if got := f.alertCount(); got != 0 {
				t.Fatalf("alerts = %d, want 0", got)
			}
		})
	}

	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:260
	t.Run("accepts one-time and recurring thresholds of the same value", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		if _, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), usageInput("x", th("10", false), th("10", true))); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:298
	t.Run("accepts negative one-time thresholds", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		if _, err := f.s.Create(context.Background(), f.h.OrgID(), f.subOwner(), usageInput("x", th("-10", false), th("-5", false))); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	})
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:174
	t.Run("rejects a second alert of the same type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		if _, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), usageInput("a")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		_, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), usageInput("b"))
		wantFieldError(t, err, "base", alerts.CodeAlertAlreadyExists)
	})
}

func TestCreateWalletAlert(t *testing.T) {
	t.Parallel()
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:445
	t.Run("is decreasing and starts from the wallet's value", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		v, err := f.s.Create(context.Background(), f.h.OrgID(), f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "low"))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if v.WalletID != f.w.ID || v.Direction != alert.DirectionDecreasing || !v.PreviousValue.Equal(money.MustParse("100")) {
			t.Fatalf("alert = %+v, want a decreasing alert of the wallet from 100", v.Alert)
		}
		// source: spec/services/usage_monitoring/create_alert_service_spec.rb:622
		if got := f.jobs(kernel.JobProcessWalletAlerts); got != 1 {
			t.Fatalf("wallet alert jobs = %d, want 1", got)
		}
		// source: spec/services/usage_monitoring/create_alert_service_spec.rb:528
		if got := f.jobs(kernel.JobProcessSubscriptionActivity); got != 0 {
			t.Fatalf("activity jobs = %d, want 0", got)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:532
	t.Run("rejects a code used on the same wallet", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		if _, err := f.s.Create(ctx, f.h.OrgID(), f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "low")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		_, err := f.s.Create(ctx, f.h.OrgID(), f.walletOwner(), walletInput(alert.TypeWalletBalanceAmount, "low"))
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:561
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:572
	t.Run("reports a taken type before a taken code", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		for _, in := range []alerts.CreateInput{walletInput(alert.TypeWalletCreditsBalance, "a"), walletInput(alert.TypeWalletBalanceAmount, "b")} {
			if _, err := f.s.Create(ctx, f.h.OrgID(), f.walletOwner(), in); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
		}
		_, err := f.s.Create(ctx, f.h.OrgID(), f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "b"))
		wantFieldError(t, err, "base", alerts.CodeAlertAlreadyExists)
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:584
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:603
	t.Run("allows the same type and code on another wallet", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		other := f.h.Wallet(f.h.Customer(), "5")
		for _, o := range []alerts.Owner{f.walletOwner(), {WalletID: other.ID}} {
			if _, err := f.s.Create(ctx, f.h.OrgID(), o, walletInput(alert.TypeWalletCreditsBalance, "low")); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:635
	t.Run("does not process the alerts of a terminated wallet", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		w := f.h.Wallet(f.h.Customer(), "5", func(w *wallet.Wallet) { w.Status = wallet.StatusTerminated })
		if _, err := f.s.Create(context.Background(), f.h.OrgID(), alerts.Owner{WalletID: w.ID}, walletInput(alert.TypeWalletCreditsBalance, "low")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got := f.jobs(kernel.JobProcessWalletAlerts); got != 0 {
			t.Fatalf("wallet alert jobs = %d, want 0", got)
		}
	})
	// source: spec/services/usage_monitoring/create_alert_service_spec.rb:652
	t.Run("rejects a subscription alert type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		_, err := f.s.Create(context.Background(), f.h.OrgID(), f.walletOwner(), usageInput("x"))
		wantFieldError(t, err, "alert_type", alert.CodeInvalidType)
	})
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	create := func(t *testing.T, f fixture, o alerts.Owner, in alerts.CreateInput) *alerts.View {
		t.Helper()
		v, err := f.s.Create(context.Background(), f.h.OrgID(), o, in)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		return v
	}
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:28
	t.Run("updates the name, the code and the thresholds", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		a := create(t, f, f.subOwner(), usageInput("old"))
		v, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "old"}, alerts.UpdateInput{
			Code:       core.Some("new"),
			Name:       core.Some(ptr("New name")),
			Thresholds: core.Some([]alert.Threshold{th("1", false), th("2", false), th("3", true)}),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if v.ID != a.ID || v.Code != "new" || *v.Name != "New name" || len(v.Thresholds) != 3 {
			t.Fatalf("alert = %+v, want the renamed alert with 3 thresholds", v.Alert)
		}
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:37
	t.Run("changes the billable metric", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m1, m2 := f.h.Metric("a", catalog.AggregationTypeCount, ""), f.h.Metric("b", catalog.AggregationTypeCount, "")
		in := usageInput("bm")
		in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageUnits, alerts.MetricRef{ID: m1.ID}
		create(t, f, f.subOwner(), in)
		v, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "bm"}, alerts.UpdateInput{BillableMetric: core.Some(alerts.MetricRef{ID: m2.ID})})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if v.BillableMetricID != m2.ID {
			t.Fatalf("billable metric = %s, want %s", v.BillableMetricID, m2.ID)
		}
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:53
	t.Run("rejects a billable metric on another type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m := f.h.Metric("a", catalog.AggregationTypeCount, "")
		create(t, f, f.subOwner(), usageInput("x"))
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "x"}, alerts.UpdateInput{BillableMetric: core.Some(alerts.MetricRef{Code: m.Code})})
		wantFieldError(t, err, "billable_metric_id", alert.CodeMustBeBlank)
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:62
	t.Run("rejects a billable metric that does not exist", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m := f.h.Metric("a", catalog.AggregationTypeCount, "")
		in := usageInput("bm")
		in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageUnits, alerts.MetricRef{ID: m.ID}
		create(t, f, f.subOwner(), in)
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "bm"}, alerts.UpdateInput{BillableMetric: core.Some(alerts.MetricRef{Code: "missing"})})
		wantNotFound(t, err, "billable_metric")
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:71
	t.Run("rejects a code already used", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		create(t, f, f.subOwner(), usageInput("a"))
		create(t, f, f.subOwner(), alerts.CreateInput{Type: alert.TypeLifetimeUsageAmount, Code: "b", Thresholds: []alert.Threshold{th("1", false)}})
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "b"}, alerts.UpdateInput{Code: core.Some("a")})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:103
	t.Run("rejects too many thresholds", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		create(t, f, f.subOwner(), usageInput("a"))
		ths := make([]alert.Threshold, 21)
		for i := range ths {
			ths[i] = alert.Threshold{Value: money.NewFromInt(int64(i))}
		}
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "a"}, alerts.UpdateInput{Thresholds: core.Some(ths)})
		wantFieldError(t, err, "thresholds", alert.CodeTooManyThresholds)
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:118
	t.Run("rejects duplicate threshold values", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		create(t, f, f.subOwner(), usageInput("a"))
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "a"}, alerts.UpdateInput{Thresholds: core.Some([]alert.Threshold{th("5", false), th("5", false)})})
		wantFieldError(t, err, "thresholds", alert.CodeDuplicateThresholdValues)
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:219
	t.Run("tracks the activity of the active subscription", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		create(t, f, f.subOwner(), usageInput("a"))
		before := f.jobs(kernel.JobProcessSubscriptionActivity)
		if _, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "a"}, alerts.UpdateInput{Name: core.Some(ptr("x"))}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		// The activity job of the same window is enqueued once.
		if got := f.jobs(kernel.JobProcessSubscriptionActivity); got != before || got != 1 {
			t.Fatalf("activity jobs = %d, want 1", got)
		}
	})
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:254
	// source: spec/services/usage_monitoring/update_alert_service_spec.rb:285
	t.Run("checks the code among the wallet's alerts", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		create(t, f, f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "a"))
		create(t, f, f.walletOwner(), walletInput(alert.TypeWalletBalanceAmount, "b"))
		ctx := context.Background()
		_, err := f.s.Update(ctx, f.h.OrgID(), f.walletOwner(), alerts.Ref{Code: "b"}, alerts.UpdateInput{Code: core.Some("a")})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
		if _, err := f.s.Update(ctx, f.h.OrgID(), f.walletOwner(), alerts.Ref{Code: "b"}, alerts.UpdateInput{Code: core.Some("b")}); err != nil {
			t.Fatalf("Update(own code) error = %v", err)
		}
	})
	t.Run("does not find an alert of another owner", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		a := create(t, f, f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "a"))
		_, err := f.s.Update(context.Background(), f.h.OrgID(), f.subOwner(), alerts.Ref{ID: a.ID}, alerts.UpdateInput{Name: core.Some(ptr("x"))})
		wantNotFound(t, err, "alert")
	})
}

func TestGetListDelete(t *testing.T) {
	t.Parallel()
	// source: spec/services/usage_monitoring/destroy_alert_service_spec.rb:11
	t.Run("deletes the alert", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		a, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), usageInput("a"))
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got, err := f.s.Get(ctx, f.h.OrgID(), f.subOwner(), alerts.Ref{ID: a.ID}); err != nil || got.Code != "a" {
			t.Fatalf("Get() = %v, %v, want the alert", got, err)
		}
		if _, err := f.s.Delete(ctx, f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "a"}); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		_, err = f.s.Get(ctx, f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "a"})
		wantNotFound(t, err, "alert")
	})
	// source: spec/queries/usage_monitoring/alerts_query_spec.rb:29
	t.Run("lists the alerts of the owner", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		if _, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), usageInput("a")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if _, err := f.s.Create(ctx, f.h.OrgID(), f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "w")); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		p, err := f.s.List(ctx, f.h.OrgID(), f.subOwner(), core.PageRequest{})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(p.Items) != 1 || p.Items[0].Code != "a" || p.Items[0].SubscriptionID != f.sub.ID {
			t.Fatalf("alerts = %v, want the subscription's alert", p.Items)
		}
	})
	// The order of apply_consistent_ordering (base_query.rb:111-112,
	// alerts_query.rb:10).
	t.Run("lists newest first", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		ctx := context.Background()
		for i, code := range []string{"a", "b"} {
			in := usageInput(code)
			if i == 1 {
				in.Type = alert.TypeLifetimeUsageAmount
			}
			if _, err := f.s.Create(ctx, f.h.OrgID(), f.subOwner(), in); err != nil {
				t.Fatalf("Create(%s) error = %v", code, err)
			}
			f.h.Clock.Advance(time.Hour)
		}
		var got []string
		for page := (core.PageRequest{Size: 1}); ; {
			p, err := f.s.List(ctx, f.h.OrgID(), f.subOwner(), page)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			for _, a := range p.Items {
				got = append(got, a.Code)
			}
			if p.NextToken == "" {
				break
			}
			page.Token = p.NextToken
		}
		if !slices.Equal(got, []string{"b", "a"}) {
			t.Fatalf("alerts: got = %v, want = [b a]", got)
		}
	})
}

func TestBatchCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:31
	t.Run("creates the alerts", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m := f.h.Metric("calls", catalog.AggregationTypeCount, "")
		bm := usageInput("b")
		bm.Type, bm.BillableMetric = alert.TypeBillableMetricCurrentUsageAmount, alerts.MetricRef{Code: m.Code}
		vs, err := f.s.BatchCreate(context.Background(), f.h.OrgID(), f.subOwner(), []alerts.CreateInput{usageInput("a"), bm})
		if err != nil {
			t.Fatalf("BatchCreate() error = %v", err)
		}
		if len(vs) != 2 || f.alertCount() != 2 {
			t.Fatalf("alerts = %d, want 2", len(vs))
		}
	})
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:85
	t.Run("rejects a second wallet alert of the same type", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		_, err := f.s.BatchCreate(context.Background(), f.h.OrgID(), f.walletOwner(), []alerts.CreateInput{
			walletInput(alert.TypeWalletBalanceAmount, "duplicated"), walletInput(alert.TypeWalletBalanceAmount, "duplicated"),
		})
		wantFieldError(t, err, "alerts[1].base", alerts.CodeAlertAlreadyExists)
		if got := f.alertCount(); got != 0 {
			t.Fatalf("alerts = %d, want 0", got)
		}
	})
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:103
	t.Run("rejects an empty batch", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		_, err := f.s.BatchCreate(context.Background(), f.h.OrgID(), f.subOwner(), nil)
		wantFieldError(t, err, "alerts", alerts.CodeNoAlerts)
	})
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:201
	t.Run("rolls back every alert and returns every error", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m0, m1 := f.h.Metric("m0", catalog.AggregationTypeCount, ""), f.h.Metric("m1", catalog.AggregationTypeCount, "")
		bm := func(code string, m *catalog.BillableMetric, ths ...alert.Threshold) alerts.CreateInput {
			in := usageInput(code, ths...)
			in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageAmount, alerts.MetricRef{Code: m.Code}
			in.Thresholds = ths
			return in
		}
		_, err := f.s.BatchCreate(context.Background(), f.h.OrgID(), f.subOwner(), []alerts.CreateInput{
			{Type: "invalid_type", Code: "alert1", Thresholds: []alert.Threshold{th("80", false)}},
			bm("alert2", m0),
			bm("alert3", m1, th("100", false)),
			bm("alert4", m1, th("100", false)),
			usageInput("alert3"),
		})
		wantFieldError(t, err, "alerts[0].alert_type", alert.CodeInvalidType)
		wantFieldError(t, err, "alerts[1].thresholds", core.CodeRequired)
		wantFieldError(t, err, "alerts[3].base", alerts.CodeAlertAlreadyExists)
		wantFieldError(t, err, "alerts[4].code", core.CodeAlreadyExists)
		if got := f.alertCount(); got != 0 {
			t.Fatalf("alerts = %d, want 0", got)
		}
	})
	// source: spec/services/usage_monitoring/alerts/create_batch_service_spec.rb:250
	t.Run("reports each missing billable metric", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		m := f.h.Metric("m0", catalog.AggregationTypeCount, "")
		bm := func(code, metric string) alerts.CreateInput {
			in := usageInput(code)
			in.Type, in.BillableMetric = alert.TypeBillableMetricCurrentUsageAmount, alerts.MetricRef{Code: metric}
			return in
		}
		_, err := f.s.BatchCreate(context.Background(), f.h.OrgID(), f.subOwner(), []alerts.CreateInput{
			bm("alert0", "first_non_existing_code"), bm("alert1", m.Code), bm("alert2", "non_existing_code"),
		})
		wantFieldError(t, err, "alerts[0].billable_metric_id", "billable_metric_not_found")
		wantFieldError(t, err, "alerts[2].billable_metric_id", "billable_metric_not_found")
	})
}

func TestDeleteAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mustCreate := func(t *testing.T, f fixture, o alerts.Owner, in alerts.CreateInput) {
		t.Helper()
		if _, err := f.s.Create(ctx, f.h.OrgID(), o, in); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	// source: spec/services/usage_monitoring/alerts/destroy_all_service_spec.rb:23
	t.Run("discards all alerts of the subscription", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		mustCreate(t, f, f.subOwner(), usageInput("a1", th("1", false), th("2", false)))
		lifetime := usageInput("a2", th("3", false))
		lifetime.Type = alert.TypeLifetimeUsageAmount
		mustCreate(t, f, f.subOwner(), lifetime)
		mustCreate(t, f, f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "other"))
		if err := f.s.DeleteAll(ctx, f.h.OrgID(), f.subOwner()); err != nil {
			t.Fatalf("DeleteAll() error = %v", err)
		}
		for _, code := range []string{"a1", "a2"} {
			_, err := f.s.Get(ctx, f.h.OrgID(), f.subOwner(), alerts.Ref{Code: code})
			wantNotFound(t, err, "alert")
		}
		if _, err := f.s.Get(ctx, f.h.OrgID(), f.walletOwner(), alerts.Ref{Code: "other"}); err != nil {
			t.Fatalf("Get() of another owner's alert error = %v, want it kept", err)
		}
		// The codes are free again.
		mustCreate(t, f, f.subOwner(), usageInput("a1"))
	})
	// source: spec/services/usage_monitoring/alerts/destroy_all_service_spec.rb:58
	t.Run("discards all alerts of the wallet", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		mustCreate(t, f, f.walletOwner(), walletInput(alert.TypeWalletBalanceAmount, "a1"))
		mustCreate(t, f, f.walletOwner(), walletInput(alert.TypeWalletCreditsBalance, "a2"))
		mustCreate(t, f, f.subOwner(), usageInput("other"))
		if err := f.s.DeleteAll(ctx, f.h.OrgID(), f.walletOwner()); err != nil {
			t.Fatalf("DeleteAll() error = %v", err)
		}
		if got := f.alertCount(); got != 1 {
			t.Fatalf("alerts = %d, want = 1", got)
		}
		if _, err := f.s.Get(ctx, f.h.OrgID(), f.subOwner(), alerts.Ref{Code: "other"}); err != nil {
			t.Fatalf("Get() of another owner's alert error = %v, want it kept", err)
		}
	})
	// source: spec/services/usage_monitoring/alerts/destroy_all_service_spec.rb:76
	t.Run("fails for an unknown owner", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		err := f.s.DeleteAll(ctx, f.h.OrgID(), alerts.Owner{WalletID: core.NewID[core.WalletID](f.h.IDs)})
		wantNotFound(t, err, "wallet")
		err = f.s.DeleteAll(ctx, f.h.OrgID(), alerts.Owner{SubscriptionExternalID: "unknown"})
		wantNotFound(t, err, "subscription")
	})
	// source: spec/services/usage_monitoring/alerts/destroy_all_service_spec.rb:83
	t.Run("succeeds without alerts", func(t *testing.T) {
		t.Parallel()
		f := setup(t)
		if err := f.s.DeleteAll(ctx, f.h.OrgID(), f.subOwner()); err != nil {
			t.Fatalf("DeleteAll() error = %v", err)
		}
	})
}
