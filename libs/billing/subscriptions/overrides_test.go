package subscriptions_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// overrideFixture is a plan with two fixed charges of 5 and 9 units.
type overrideFixture struct {
	*fixture
	plan     *catalog.Plan
	fc1, fc2 *catalog.FixedCharge
}

func newOverrideFixture(t *testing.T) *overrideFixture {
	t.Helper()
	f := newFixture(t)
	p := f.Plan(period.Monthly, 1000)
	a := f.AddOn(100)
	return &overrideFixture{
		fixture: f, plan: p,
		fc1: f.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "5"),
		fc2: f.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "9"),
	}
}

func (f *overrideFixture) planOf(id core.PlanID) *catalog.Plan {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		return r.Plans.Get(ctx, f.OrgID(), id)
	})
}

func (f *overrideFixture) fixedUnits(planID core.PlanID) map[core.FixedChargeID]string {
	f.T.Helper()
	fcs := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.FixedCharge, error) {
		return r.FixedCharges.ListByPlan(ctx, f.OrgID(), planID)
	})
	out := map[core.FixedChargeID]string{}
	for _, fc := range fcs {
		key := fc.ID
		if !fc.ParentID.IsZero() {
			key = fc.ParentID
		}
		out[key] = fc.Units.String()
	}
	return out
}

func (f *overrideFixture) unitsOverrides(sub core.SubscriptionID) []*subscription.FixedChargeUnitsOverride {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.FixedChargeUnitsOverride, error) {
		return r.FixedChargeUnitsOverrides.ListBySubscription(ctx, f.OrgID(), sub)
	})
}

func (f *overrideFixture) fixedChargeEvents(sub core.SubscriptionID) []*subscription.FixedChargeEvent {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.FixedChargeEvent, error) {
		return r.FixedChargeEvents.ListBySubscription(ctx, f.OrgID(), sub, core.FixedChargeID{})
	})
}

func (f *overrideFixture) ownThresholds(sub core.SubscriptionID) []*catalog.UsageThreshold {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.UsageThreshold, error) {
		return r.UsageThresholds.ListBySubscription(ctx, f.OrgID(), sub)
	})
}

func (f *overrideFixture) unitsOverride(sub *subscription.Subscription, fc *catalog.FixedCharge, units string) {
	f.T.Helper()
	o := &subscription.FixedChargeUnitsOverride{
		ID: core.NewID[core.FixedChargeUnitsOverrideID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: sub.ID,
		FixedChargeID: fc.ID, Units: money.MustParse(units), CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.FixedChargeUnitsOverrides.Create(ctx, o) })
}

func units(s string) plans.FixedChargeInput {
	return plans.FixedChargeInput{Units: core.Some(money.MustParse(s))}
}

func threshold(amount int64, recurring bool) plans.ThresholdInput {
	return plans.ThresholdInput{Amount: &amount, Recurring: recurring}
}

func TestCreateWithPlanOverrides(t *testing.T) {
	t.Parallel()

	// source: spec/services/subscriptions/create_service_spec.rb:532
	t.Run("override plan", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		v, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) {
			in.PlanOverrides = &plans.OverrideInput{
				Amount: core.Some(int64(12_345)), FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc2.ID: units("100")},
			}
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		child := f.planOf(v.Subscription.PlanID)
		if child.ParentID != f.plan.ID || child.Amount != 12_345 || v.Subscription.Status != subscription.StatusActive {
			t.Fatalf("got plan = parent %s amount %d, status %s, want = override of %s at 12345, active", child.ParentID, child.Amount, v.Subscription.Status, f.plan.ID)
		}
		if got := f.fixedUnits(child.ID); got[f.fc1.ID] != "5" || got[f.fc2.ID] != "100" {
			t.Errorf("got override units = %v, want = 5 and 100", got)
		}
		if got := f.unitsOverrides(v.Subscription.ID); len(got) != 0 {
			t.Errorf("got %d units overrides, want = 0", len(got))
		}
	})

	// source: spec/services/subscriptions/create_service_spec.rb:461
	t.Run("units only", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		v, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) {
			in.PlanOverrides = &plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc2.ID: units("100")}}
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if v.Subscription.PlanID != f.plan.ID {
			t.Fatalf("got plan = %s, want = the original plan %s", v.Subscription.PlanID, f.plan.ID)
		}
		got := f.unitsOverrides(v.Subscription.ID)
		if len(got) != 1 || got[0].FixedChargeID != f.fc2.ID || got[0].Units.String() != "100" {
			t.Errorf("got units overrides = %+v, want = 100 units of fc2", got)
		}

		// source: spec/services/subscriptions/create_service_spec.rb:474
		sub := f.reload(v.Subscription.ID)
		want := map[core.FixedChargeID]string{f.fc1.ID: "5", f.fc2.ID: "100"}
		events := f.fixedChargeEvents(sub.ID)
		if len(events) != len(want) {
			t.Fatalf("got %d fixed charge events, want = %d", len(events), len(want))
		}
		for _, e := range events {
			if want[e.FixedChargeID] != e.Units.String() || !e.Timestamp.Equal(sub.StartedAt.Add(time.Second)) || !e.ApplyUnitsImmediately {
				t.Errorf("got event = %s %s at %s, want = %s units a second after %s", e.FixedChargeID, e.Units, e.Timestamp, want[e.FixedChargeID], sub.StartedAt)
			}
		}
	})

	// source: spec/services/subscriptions/create_service_spec.rb:489
	t.Run("units only in the future", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		v, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) {
			in.SubscriptionAt = new(f.Now().AddDate(0, 0, 7))
			in.PlanOverrides = &plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc2.ID: units("100")}}
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		sub := f.reload(v.Subscription.ID)
		if sub.Status != subscription.StatusPending || sub.StartedAt != nil || sub.PlanID != f.plan.ID {
			t.Errorf("got = %s started %v on %s, want = pending, not started, on %s", sub.Status, sub.StartedAt, sub.PlanID, f.plan.ID)
		}
		if got := f.unitsOverrides(sub.ID); len(got) != 1 || got[0].Units.String() != "100" {
			t.Errorf("got units overrides = %+v, want = 100 units of fc2", got)
		}
		if got := f.fixedChargeEvents(sub.ID); len(got) != 0 {
			t.Errorf("got %d fixed charge events, want = 0", len(got))
		}
	})

	// source: spec/services/subscriptions/create_service_spec.rb:609
	// source: spec/services/subscriptions/create_service_spec.rb:616
	t.Run("units of a foreign fixed charge", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		other := f.FixedCharge(f.Plan(period.Monthly, 0), f.AddOn(1), catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
		c := f.Customer()
		_, err := f.create(c, f.plan, func(in *subscriptions.CreateInput) {
			in.PlanOverrides = &plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{other.ID: units("100")}}
		})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != subscriptions.ResourceFixedCharge {
			t.Fatalf("Create() error = %v, want = fixed_charge not found", err)
		}
		subs := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
			return r.Subscriptions.ListByCustomer(ctx, f.OrgID(), c.ID)
		})
		if len(subs) != 0 {
			t.Errorf("got %d subscriptions, want = 0", len(subs))
		}
	})

	// source: app/services/subscriptions/create_service.rb:129-134
	t.Run("same plan keeps the subscription", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		c := f.Customer()
		first, err := f.create(c, f.plan)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		v, err := f.create(c, f.plan, func(in *subscriptions.CreateInput) {
			in.PlanOverrides = &plans.OverrideInput{Amount: core.Some(int64(1))}
		})
		if err != nil || v.Subscription.ID != first.Subscription.ID || v.Subscription.PlanID != f.plan.ID {
			t.Fatalf("Create() = %+v, %v, want = the subscription unchanged", v, err)
		}
		children := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.Plan, error) {
			return r.Plans.ListChildren(ctx, f.OrgID(), f.plan.ID)
		})
		if len(children) != 0 {
			t.Errorf("got %d override plans, want = 0", len(children))
		}
	})

	// source: spec/services/subscriptions/create_service_spec.rb:327
	t.Run("incompatible thresholds", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		_, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) {
			in.UsageThresholds = []plans.ThresholdInput{threshold(100, false)}
			in.PlanOverrides = &plans.OverrideInput{UsageThresholds: []plans.ThresholdInput{threshold(200, false)}}
		})
		want := []string{"plan_overrides.usage_thresholds: incompatible_params", "usage_thresholds: incompatible_params"}
		if got := fieldCodes(t, err); !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})

	// source: spec/services/subscriptions/create_service_spec.rb:368
	// source: spec/services/usage_thresholds/update_service_spec.rb:21
	t.Run("own thresholds", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		f.UsageThreshold(f.plan, 50, false)
		v, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) {
			in.UsageThresholds = []plans.ThresholdInput{threshold(1000, false), threshold(500, true)}
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		own := f.ownThresholds(v.Subscription.ID)
		if len(own) != 2 || own[0].Amount != 1000 || !own[1].Recurring || own[1].Amount != 500 {
			t.Fatalf("got own thresholds = %+v, want = 1000 then recurring 500", own)
		}
		if len(v.UsageThresholds) != 2 || v.UsageThresholds[0].ID != own[0].ID {
			t.Errorf("got applicable thresholds = %+v, want = the subscription's own", v.UsageThresholds)
		}
	})
}

// source: spec/services/usage_thresholds/update_service_spec.rb:31
func TestThresholdsValidation(t *testing.T) {
	t.Parallel()
	missing := plans.ThresholdInput{}
	for _, tt := range []struct {
		name string
		in   []plans.ThresholdInput
		want string
	}{
		{"missing amount", []plans.ThresholdInput{missing}, "usage_thresholds: missing_amount_cents"},
		{"duplicated values", []plans.ThresholdInput{threshold(10, false), threshold(10, false)}, "usage_thresholds: duplicated_values"},
		{"multiple recurring", []plans.ThresholdInput{threshold(10, true), threshold(20, true)}, "usage_thresholds: multiple_recurring_thresholds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newOverrideFixture(t)
			_, err := f.create(f.Customer(), f.plan, func(in *subscriptions.CreateInput) { in.UsageThresholds = tt.in })
			if got := fieldCodes(t, err); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("got = %v, want = [%s]", got, tt.want)
			}
		})
	}
}

func TestUpdateWithPlanOverrides(t *testing.T) {
	t.Parallel()
	update := func(f *overrideFixture, sub *subscription.Subscription, fn func(*subscriptions.UpdateInput)) (*subscriptions.View, error) {
		in := subscriptions.UpdateInput{OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: sub.ID}}
		fn(&in)
		return f.svc.Update(f.Ctx, in)
	}

	// source: spec/services/subscriptions/update_service_spec.rb:733
	t.Run("original plan", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		sub := f.Subscription(f.Customer(), f.plan)
		v, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{Name: core.Some("new name")})
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		child := f.planOf(v.Subscription.PlanID)
		if child.ParentID != f.plan.ID || child.Name != "new name" || f.reload(sub.ID).PlanID != child.ID {
			t.Errorf("got plan = %+v, want = an override of %s named new name", child, f.plan.ID)
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:765
	t.Run("override plan", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		child := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = f.plan.ID })
		a := f.AddOn(1)
		cfc := f.FixedCharge(child, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "5", func(fc *catalog.FixedCharge) { fc.ParentID = f.fc1.ID })
		sub := f.Subscription(f.Customer(), child)
		v, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{
				Name: core.Some("new name"), FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{cfc.ID: units("7")},
				// Left as they are: the plan has a subscription
				// (plans/update_service.rb:40-49).
				Currency: core.Some(money.MustCurrency("EUR")), TrialPeriodDays: core.Some(30),
			})
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		got := f.planOf(child.ID)
		if v.Subscription.PlanID != child.ID || got.Name != "new name" || got.Currency != child.Currency || got.TrialPeriodDays != child.TrialPeriodDays {
			t.Errorf("got plan = %s named %q in %s with %d trial days, want = %s renamed, currency and trial kept",
				v.Subscription.PlanID, got.Name, got.Currency, got.TrialPeriodDays, child.ID)
		}
		if u := f.fixedUnits(child.ID); u[cfc.ParentID] != "7" {
			t.Errorf("got units = %v, want = 7", u)
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:805
	t.Run("override plan keeps the fixed charges left out", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		child := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = f.plan.ID })
		a := f.AddOn(1)
		c1 := f.FixedCharge(child, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "5", func(fc *catalog.FixedCharge) { fc.ParentID = f.fc1.ID })
		c2 := f.FixedCharge(child, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "7", func(fc *catalog.FixedCharge) { fc.ParentID = f.fc2.ID })
		sub := f.Subscription(f.Customer(), child)
		if _, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{c1.ID: units("20")}})
		}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if u := f.fixedUnits(child.ID); u[c1.ParentID] != "20" || u[c2.ParentID] != "7" {
			t.Errorf("got units = %v, want = 20 and 7", u)
		}

		// source: app/services/subscriptions/update_service.rb:266-271
		_, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc1.ID: units("3")}})
		})
		if nf := new(core.NotFoundError); !errors.As(err, &nf) || nf.Resource != subscriptions.ResourceFixedCharge {
			t.Errorf("Update(original plan's fixed charge) error = %v, want = fixed_charge not found", err)
		}
	})

	// source: app/services/plans/update_service.rb:245-276
	t.Run("override plan charges replaced", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		child := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = f.plan.ID })
		m := f.Metric("api", catalog.AggregationTypeCount, "")
		kept := f.Charge(child, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		dropped := f.Charge(child, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
		sub := f.Subscription(f.Customer(), child)
		if _, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{Charges: map[core.ChargeID]plans.ChargeInput{
				kept.ID: {InvoiceDisplayName: core.Some(new("API"))},
			}})
		}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.Charge, error) {
			return r.Charges.ListByPlan(ctx, f.OrgID(), child.ID)
		})
		if len(got) != 1 || got[0].ID != kept.ID || got[0].InvoiceDisplayName == nil || *got[0].InvoiceDisplayName != "API" {
			t.Errorf("got charges = %+v, want = %s alone, renamed API (%s removed)", got, kept.ID, dropped.ID)
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:941
	t.Run("units only", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		sub := f.Subscription(f.Customer(), f.plan)
		v, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc2.ID: units("3")}})
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		got := f.unitsOverrides(sub.ID)
		if v.Subscription.PlanID != f.plan.ID || len(got) != 1 || got[0].FixedChargeID != f.fc2.ID || got[0].Units.String() != "3" {
			t.Errorf("got plan %s, overrides %+v, want = the original plan with 3 units of fc2", v.Subscription.PlanID, got)
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:1207
	t.Run("units of a foreign fixed charge", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		other := f.FixedCharge(f.Plan(period.Monthly, 0), f.AddOn(1), catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
		sub := f.Subscription(f.Customer(), f.plan)
		_, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{other.ID: units("3")}})
		})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) {
			t.Fatalf("Update() error = %v, want = not found", err)
		}
		if got := f.unitsOverrides(sub.ID); len(got) != 0 {
			t.Errorf("got %d units overrides, want = 0", len(got))
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:1264
	t.Run("units promoted", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		sub := f.Subscription(f.Customer(), f.plan)
		f.unitsOverride(sub, f.fc1, "11")
		f.unitsOverride(sub, f.fc2, "22")
		v, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.PlanOverrides = core.Some(plans.OverrideInput{
				Amount: core.Some(int64(12_345)), FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{f.fc1.ID: units("99")},
			})
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		child := f.planOf(v.Subscription.PlanID)
		if child.ParentID != f.plan.ID || child.Amount != 12_345 {
			t.Fatalf("got plan = %+v, want = an override at 12345", child)
		}
		if u := f.fixedUnits(child.ID); u[f.fc1.ID] != "99" || u[f.fc2.ID] != "22" {
			t.Errorf("got units = %v, want = 99 and 22", u)
		}
		if got := f.unitsOverrides(sub.ID); len(got) != 0 {
			t.Errorf("got %d units overrides, want = 0", len(got))
		}
	})

	// source: spec/services/subscriptions/update_service_spec.rb:135
	t.Run("incompatible thresholds", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		sub := f.Subscription(f.Customer(), f.plan)
		_, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.UsageThresholds = core.Some([]plans.ThresholdInput{threshold(100, false)})
			in.PlanOverrides = core.Some(plans.OverrideInput{UsageThresholds: []plans.ThresholdInput{threshold(200, false)}})
		})
		want := []string{"plan_overrides.usage_thresholds: incompatible_params", "usage_thresholds: incompatible_params"}
		if got := fieldCodes(t, err); !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})

	// source: spec/services/subscriptions/update_usage_thresholds_service_spec.rb:64
	// source: spec/services/subscriptions/update_usage_thresholds_service_spec.rb:77
	t.Run("thresholds replaced", func(t *testing.T) {
		t.Parallel()
		f := newOverrideFixture(t)
		child := f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = f.plan.ID })
		f.UsageThreshold(child, 300, false)
		sub := f.Subscription(f.Customer(), child)
		lu := &subscription.LifetimeUsage{
			ID: core.NewID[core.LifetimeUsageID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: sub.ID,
			ExternalSubscriptionID: sub.ExternalID, Currency: money.MustCurrency("USD"), CreatedAt: f.Now(), UpdatedAt: f.Now(),
		}
		f.Do(func(ctx context.Context, r port.Repos) error { return r.LifetimeUsages.Upsert(ctx, lu) })
		if _, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.UsageThresholds = core.Some([]plans.ThresholdInput{threshold(100, false)})
		}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		v, err := update(f, sub, func(in *subscriptions.UpdateInput) {
			in.UsageThresholds = core.Some([]plans.ThresholdInput{threshold(200, false), threshold(50, true)})
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		own := f.ownThresholds(sub.ID)
		if len(own) != 2 || own[0].Amount != 200 || own[1].Amount != 50 || len(v.UsageThresholds) != 2 {
			t.Fatalf("got own thresholds = %+v, want = 200 then recurring 50", own)
		}
		left := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*catalog.UsageThreshold, error) {
			return r.UsageThresholds.ListByPlan(ctx, f.OrgID(), child.ID)
		})
		if len(left) != 0 {
			t.Errorf("got %d override plan thresholds, want = 0", len(left))
		}
		got := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.LifetimeUsage, error) {
			return r.LifetimeUsages.GetBySubscription(ctx, f.OrgID(), sub.ID)
		})
		if !got.RecalculateInvoicedUsage {
			t.Errorf("got RecalculateInvoicedUsage = false, want = true")
		}
	})
}

// source: app/services/subscriptions/plan_downgrade_service.rb:36
func TestChangePlanWithOverrides(t *testing.T) {
	t.Parallel()
	f := newOverrideFixture(t)
	large := f.Plan(period.Monthly, 100_000)
	sub := f.Subscription(f.Customer(), large)
	pc, err := f.svc.ChangePlan(f.Ctx, subscriptions.ChangePlanInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: sub.ID}, Plan: refs.ByID(f.plan.ID),
		PlanOverrides: &plans.OverrideInput{Amount: core.Some(int64(5000))},
	})
	if err != nil {
		t.Fatalf("ChangePlan() error = %v", err)
	}
	child := f.planOf(pc.Subscription.Subscription.PlanID)
	if child.ParentID != f.plan.ID || child.Amount != 5000 || pc.Subscription.Subscription.Status != subscription.StatusPending {
		t.Errorf("got plan = %+v, status %s, want = a pending downgrade onto an override at 5000", child, pc.Subscription.Subscription.Status)
	}

	// The requested plan decides the direction, not its override
	// (create_service.rb:136-148): 1000 against 3000 is a downgrade.
	mid := f.Plan(period.Monthly, 3000)
	sub = f.Subscription(f.Customer(), mid)
	pc, err = f.svc.ChangePlan(f.Ctx, subscriptions.ChangePlanInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: sub.ID}, Plan: refs.ByID(f.plan.ID),
		PlanOverrides: &plans.OverrideInput{Amount: core.Some(int64(5000))},
	})
	if err != nil {
		t.Fatalf("ChangePlan(mid) error = %v", err)
	}
	if got := pc.Subscription.Subscription; got.Status != subscription.StatusPending || f.planOf(got.PlanID).Amount != 5000 {
		t.Errorf("got %s on %s, want = a pending downgrade onto an override at 5000", got.Status, got.PlanID)
	}

	// source: app/services/subscriptions/plan_upgrade_service.rb:77-80
	// source: app/services/subscriptions/plan_downgrade_service.rb:82-84
	future := f.Subscription(f.Customer(), large, billingtest.SubscribedAt(f.Now().AddDate(0, 0, 7)))
	pc, err = f.svc.ChangePlan(f.Ctx, subscriptions.ChangePlanInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: future.ID}, Plan: refs.ByID(f.plan.ID),
		PlanOverrides: &plans.OverrideInput{Amount: core.Some(int64(5000))},
	})
	if err != nil {
		t.Fatalf("ChangePlan(future) error = %v", err)
	}
	if got := pc.Subscription.Subscription; got.ID != future.ID || got.PlanID != f.plan.ID {
		t.Errorf("got %s on %s, want = %s moved to the original plan %s", got.ID, got.PlanID, future.ID, f.plan.ID)
	}
}
