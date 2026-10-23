package plans_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

const fcStandard = `{"amount":"10"}`

func reloadFixedCharge(h *billingtest.Harness, id core.FixedChargeID) *catalog.FixedCharge {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		return r.FixedCharges.Get(ctx, h.OrgID(), id)
	})
}

func planFixedCharges(h *billingtest.Harness, id core.PlanID) []*catalog.FixedCharge {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*catalog.FixedCharge, error) {
		return r.FixedCharges.ListByPlan(ctx, h.OrgID(), id)
	})
}

func fcEvents(h *billingtest.Harness, sub core.SubscriptionID) []*subscription.FixedChargeEvent {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*subscription.FixedChargeEvent, error) {
		return r.FixedChargeEvents.ListBySubscription(ctx, h.OrgID(), sub, core.FixedChargeID{})
	})
}

func unitsOverrides(h *billingtest.Harness, sub core.SubscriptionID) []*subscription.FixedChargeUnitsOverride {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*subscription.FixedChargeUnitsOverride, error) {
		return r.FixedChargeUnitsOverrides.ListBySubscription(ctx, h.OrgID(), sub)
	})
}

// fcJobs returns the pending jobs of the kinds.
func fcJobs(h *billingtest.Harness, kinds ...string) []port.Job {
	return slices.DeleteFunc(h.Store.Jobs(), func(j port.Job) bool { return !slices.Contains(kinds, j.Kind) })
}

// fcDrain runs the enqueued jobs, and those they enqueue, until none is
// left; jobs billing does not run are dropped.
func fcDrain(t *testing.T, h *billingtest.Harness, e *billing.Engine) {
	t.Helper()
	for range 100 {
		jobs := h.Store.TakeJobs()
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			if _, err := e.HandleJob(h.Ctx, j); err != nil {
				t.Fatalf("HandleJob(%s) error = %v", j.Kind, err)
			}
		}
	}
	t.Fatalf("jobs never settle")
}

func fcWantInvalid(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Fatalf("error = %v, want = %s %s", err, field, code)
	}
}

func fcWantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want = %s_not_found", err, resource)
	}
}

func fcWantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error = %v, want = %s", err, code)
	}
}

func fcProps(raw string) core.Optional[catalog.Properties] {
	return core.Some(catalog.MustProperties(raw))
}

func fcUnits(s string) core.Optional[money.Decimal] { return core.Some(money.MustParse(s)) }

func fcInput(a *catalog.AddOn, code string) plans.FixedChargeInput {
	return plans.FixedChargeInput{
		AddOn:       refs.ByID(a.ID),
		Code:        core.Some(code),
		ChargeModel: core.Some(catalog.FixedChargeModelStandard),
		Properties:  fcProps(fcStandard),
		Units:       fcUnits("2"),
	}
}

func payInAdvance(f *catalog.FixedCharge) { f.PayInAdvance = true }

func childOf(parent *catalog.FixedCharge) func(*catalog.FixedCharge) {
	return func(f *catalog.FixedCharge) { f.ParentID, f.Code = parent.ID, parent.Code }
}

func incomplete(s *subscription.Subscription) { s.Status = subscription.StatusIncomplete }

func TestCreateFixedCharge(t *testing.T) {
	// source: spec/services/fixed_charges/create_service_spec.rb:26
	t.Run("add-on not found", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := fcInput(&catalog.AddOn{ID: core.NewID[core.AddOnID](h.IDs)}, "seats")
		_, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		fcWantNotFound(t, err, "add_on")
		if got := planFixedCharges(h, p.ID); len(got) != 0 {
			t.Errorf("fixed charges = %d, want = 0", len(got))
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:44
	t.Run("add-on code not found", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := fcInput(&catalog.AddOn{}, "seats")
		in.AddOn = refs.ByCode("missing")
		_, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		fcWantNotFound(t, err, "add_on")
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:62
	t.Run("code already used on the plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1", func(f *catalog.FixedCharge) { f.Code = "seats" })
		_, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), fcInput(a, "seats"), false)
		fcWantInvalid(t, err, "code", core.CodeAlreadyExists)
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:83
	t.Run("invalid params", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := fcInput(h.AddOn(100), "seats")
		in.ChargeModel = core.Some(catalog.FixedChargeModelVolume)
		in.Properties = fcProps(`{"volume_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`)
		in.PayInAdvance = core.Some(true)
		_, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		fcWantInvalid(t, err, "pay_in_advance", catalog.CodeInvalidChargeModel)
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:107
	t.Run("by add-on ID with taxes", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		tax := h.Tax("vat", "20")
		in := fcInput(a, "seats")
		in.InvoiceDisplayName = core.Some(new("Seats"))
		in.Prorated = core.Some(true)
		in.Taxes = core.Some([]refs.Ref{refs.ByCode(tax.Code)})
		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByCode(p.Code), in, false)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		f := reloadFixedCharge(h, got.ID)
		if f.AddOnID != a.ID || f.Code != "seats" || f.ChargeModel != catalog.FixedChargeModelStandard || !f.Prorated || f.PayInAdvance {
			t.Errorf("fixed charge = %+v, want = seats on %s, standard, prorated", f, a.ID)
		}
		if !f.Units.Equal(money.MustParse("2")) {
			t.Errorf("units = %s, want = 2", f.Units)
		}
		if f.InvoiceDisplayName == nil || *f.InvoiceDisplayName != "Seats" {
			t.Errorf("invoice_display_name = %v, want = Seats", f.InvoiceDisplayName)
		}
		if !slices.Equal(f.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("taxes = %v, want = [%s]", f.TaxIDs, tax.ID)
		}
		if !f.Properties.Equal(catalog.MustProperties(fcStandard)) {
			t.Errorf("properties = %s, want = %s", f.Properties.Raw(), fcStandard)
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:155
	t.Run("by add-on code", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		in := fcInput(a, "seats")
		in.AddOn = refs.ByCode(a.Code)
		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		if got.AddOnID != a.ID {
			t.Errorf("add-on = %s, want = %s", got.AddOnID, a.ID)
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:198
	t.Run("default properties", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := fcInput(h.AddOn(100), "seats")
		in.Properties = core.Optional[catalog.Properties]{}
		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		if want := catalog.MustProperties(`{"amount":"0"}`); !got.Properties.Equal(want) {
			t.Errorf("properties = %s, want = %s", got.Properties.Raw(), want.Raw())
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:291
	t.Run("default values", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := plans.FixedChargeInput{
			AddOn:       refs.ByID(h.AddOn(100).ID),
			Code:        core.Some("seats"),
			ChargeModel: core.Some(catalog.FixedChargeModelStandard),
		}
		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		if got.PayInAdvance || got.Prorated || !got.Units.IsZero() || got.InvoiceDisplayName != nil {
			t.Errorf("fixed charge = %+v, want = defaults", got)
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:327
	t.Run("filters properties", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		in := fcInput(h.AddOn(100), "seats")
		in.Properties = fcProps(`{"amount":"10","graduated_ranges":[{"from_value":0}],"unknown":"1"}`)
		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		if want := catalog.MustProperties(`{"amount":"10"}`); !got.Properties.Equal(want) {
			t.Errorf("properties = %s, want = %s", got.Properties.Raw(), want.Raw())
		}
	})

	// source: spec/services/fixed_charges/create_service_spec.rb:353
	t.Run("events of the plan's subscriptions", func(t *testing.T) {
		for _, immediate := range []bool{true, false} {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 1000)
			c := h.Customer()
			active := h.Subscription(c, p)
			terminated := h.Subscription(c, p)
			h.UpdateSubscription(terminated, func(s *subscription.Subscription) {
				if err := s.Terminate(h.Now()); err != nil {
					t.Fatal(err)
				}
			})
			in := fcInput(h.AddOn(100), "seats")
			in.ApplyUnitsImmediately = immediate
			got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
			if err != nil {
				t.Fatalf("CreateFixedCharge() error = %v", err)
			}
			evs := fcEvents(h, active.ID)
			if len(evs) != 1 {
				t.Fatalf("immediate %v: events = %d, want = 1", immediate, len(evs))
			}
			ev := evs[0]
			if ev.FixedChargeID != got.ID || !ev.Units.Equal(money.MustParse("2")) || !ev.Timestamp.Equal(now) || ev.ApplyUnitsImmediately != immediate {
				t.Errorf("event = %+v, want = 2 units at %s, immediate %v", ev, now, immediate)
			}
			if evs := fcEvents(h, terminated.ID); len(evs) != 0 {
				t.Errorf("terminated subscription events = %d, want = 0", len(evs))
			}
		}
	})

	// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:41-56
	t.Run("code required and cascade", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		child := h.Plan(period.Monthly, 900, overrideOf(p))
		h.Subscription(h.Customer(), child)
		a := h.AddOn(100)
		in := fcInput(a, "")
		in.Code = core.Optional[string]{}
		_, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, true)
		fcWantInvalid(t, err, "code", core.CodeRequired)

		_, err = e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(child.ID), fcInput(a, "seats"), true)
		fcWantNotFound(t, err, "plan")

		got, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), fcInput(a, "seats"), true)
		if err != nil {
			t.Fatalf("CreateFixedCharge() error = %v", err)
		}
		if n := len(fcJobs(h, kernel.JobCreateFixedChargeChildren)); n != 1 {
			t.Fatalf("create children jobs = %d, want = 1", n)
		}
		fcDrain(t, h, e)
		fcs := planFixedCharges(h, child.ID)
		if len(fcs) != 1 || fcs[0].ParentID != got.ID || fcs[0].Code != "seats" {
			t.Errorf("child fixed charges = %+v, want = one copy of %s", fcs, got.ID)
		}
	})
}

func TestUpdateFixedCharge(t *testing.T) {
	// source: spec/services/fixed_charges/update_service_spec.rb:43
	t.Run("code already used on the plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1", func(f *catalog.FixedCharge) { f.Code = "taken" })
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.FixedChargeInput{Code: core.Some("taken")}
		_, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false)
		fcWantInvalid(t, err, "code", core.CodeAlreadyExists)
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:65
	t.Run("pay_in_advance and prorated are kept", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.FixedChargeInput{
			PayInAdvance: core.Some(true), Prorated: core.Some(true),
			Properties: fcProps(`{"amount":"20"}`), Units: fcUnits("5"),
			InvoiceDisplayName: core.Some(new("Seats")),
		}
		got, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByCode(f.Code), in, false)
		if err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if got.PayInAdvance || got.Prorated {
			t.Errorf("pay_in_advance, prorated = %v, %v, want = false, false", got.PayInAdvance, got.Prorated)
		}
		if !got.Units.Equal(money.MustParse("5")) || !got.Properties.Equal(catalog.MustProperties(`{"amount":"20"}`)) {
			t.Errorf("units, properties = %s, %s, want = 5, {amount: 20}", got.Units, got.Properties.Raw())
		}
		if got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != "Seats" {
			t.Errorf("invoice_display_name = %v, want = Seats", got.InvoiceDisplayName)
		}
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:77
	t.Run("plan attached to subscriptions", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		h.Subscription(h.Customer(), p)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		tax := h.Tax("vat", "20")
		in := plans.FixedChargeInput{
			Code:        core.Some("renamed"),
			ChargeModel: core.Some(catalog.FixedChargeModelGraduated),
			Taxes:       core.Some([]refs.Ref{refs.ByID(tax.ID)}),
		}
		got, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false)
		if err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if got.ChargeModel != catalog.FixedChargeModelStandard || got.Code != f.Code {
			t.Errorf("model, code = %s, %s, want = standard, %s", got.ChargeModel, got.Code, f.Code)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{tax.ID}) {
			t.Errorf("taxes = %v, want = [%s]", got.TaxIDs, tax.ID)
		}
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:105
	t.Run("plan not attached to subscriptions", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.FixedChargeInput{Code: core.Some("renamed"), ChargeModel: core.Some(catalog.FixedChargeModelGraduated)}
		got, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false)
		if err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if got.ChargeModel != catalog.FixedChargeModelGraduated || got.Code != "renamed" {
			t.Errorf("model, code = %s, %s, want = graduated, renamed", got.ChargeModel, got.Code)
		}
		// source: spec/services/fixed_charges/update_service_spec.rb:149
		raw := string(got.Properties.Raw())
		if want := string(fixedChargeDefaults(t, catalog.FixedChargeModelGraduated).Raw()); raw != want {
			t.Errorf("properties = %s, want = %s", raw, want)
		}
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:255
	t.Run("units changed", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		c := h.Customer()
		s1, s2 := h.Subscription(c, p), h.Subscription(c, p)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1", payInAdvance)
		h.Do(func(ctx context.Context, r port.Repos) error {
			return r.FixedChargeUnitsOverrides.Create(ctx, &subscription.FixedChargeUnitsOverride{
				ID: core.NewID[core.FixedChargeUnitsOverrideID](h.IDs), OrganizationID: h.OrgID(),
				SubscriptionID: s2.ID, FixedChargeID: f.ID, Units: money.MustParse("7"), CreatedAt: now, UpdatedAt: now,
			})
		})
		in := plans.FixedChargeInput{Units: fcUnits("3"), ApplyUnitsImmediately: true}
		if _, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false); err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if evs := fcEvents(h, s1.ID); len(evs) != 1 || !evs[0].Units.Equal(money.MustParse("3")) || !evs[0].ApplyUnitsImmediately {
			t.Errorf("events = %+v, want = one immediate event of 3 units", evs)
		}
		// source: spec/services/fixed_charges/emit_events_service_spec.rb:351
		if evs := fcEvents(h, s2.ID); len(evs) != 0 {
			t.Errorf("overridden subscription events = %d, want = 0", len(evs))
		}
		// source: spec/services/fixed_charges/update_service_spec.rb:290
		if n := len(fcJobs(h, kernel.JobCreateAllInAdvanceFixedCharges)); n != 1 {
			t.Fatalf("fan-out jobs = %d, want = 1", n)
		}
		fcDrain(t, h, e)
		// source: spec/services/fixed_charges/update_service_spec.rb:317
		invs := h.Invoices(c.ID)
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1 (the subscription without its own units)", len(invs))
		}
		fees := h.Fees(invs[0].ID)
		if len(fees) != 1 || fees[0].FixedChargeID != f.ID || !fees[0].Units.Equal(money.MustParse("3")) || fees[0].Amount != 3000 {
			t.Errorf("fees = %+v, want = one fee of 3 units, 3000", fees)
		}
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:335
	t.Run("units applied next period", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		sub := h.Subscription(h.Customer(), p)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1", payInAdvance)
		in := plans.FixedChargeInput{Units: fcUnits("3")}
		if _, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false); err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if evs := fcEvents(h, sub.ID); len(evs) != 1 || evs[0].ApplyUnitsImmediately {
			t.Errorf("events = %+v, want = one deferred event", evs)
		}
		if n := len(fcJobs(h, kernel.JobCreateAllInAdvanceFixedCharges)); n != 0 {
			t.Errorf("fan-out jobs = %d, want = 0", n)
		}
	})

	// source: spec/services/fixed_charges/emit_events_service_spec.rb:279
	t.Run("incomplete subscription defers immediate units", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		sub := h.Subscription(h.Customer(), p, incomplete)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.FixedChargeInput{Units: fcUnits("3"), ApplyUnitsImmediately: true}
		if _, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, false); err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		if evs := fcEvents(h, sub.ID); len(evs) != 1 || evs[0].ApplyUnitsImmediately {
			t.Errorf("events = %+v, want = one deferred event", evs)
		}
	})

	// source: spec/services/fixed_charges/update_children_service_spec.rb:62
	t.Run("cascade", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
		same := h.Plan(period.Monthly, 900, overrideOf(p))
		h.Subscription(h.Customer(), same)
		sameFC := h.FixedCharge(same, a, catalog.FixedChargeModelStandard, fcStandard, "1", childOf(f))
		own := h.Plan(period.Monthly, 900, overrideOf(p))
		h.Subscription(h.Customer(), own)
		ownFC := h.FixedCharge(own, a, catalog.FixedChargeModelStandard, `{"amount":"99"}`, "1", childOf(f))
		in := plans.FixedChargeInput{Properties: fcProps(`{"amount":"20"}`), Units: fcUnits("4"), InvoiceDisplayName: core.Some(new("Parent"))}
		if _, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), in, true); err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		fcDrain(t, h, e)
		got := reloadFixedCharge(h, sameFC.ID)
		if !got.Properties.Equal(catalog.MustProperties(`{"amount":"20"}`)) || !got.Units.Equal(money.MustParse("4")) {
			t.Errorf("unmodified override = %s, %s units, want = {amount: 20}, 4 units", got.Properties.Raw(), got.Units)
		}
		// source: spec/services/fixed_charges/update_service_spec.rb:180
		if got.InvoiceDisplayName != nil {
			t.Errorf("override invoice_display_name = %v, want = nil", *got.InvoiceDisplayName)
		}
		// source: spec/services/fixed_charges/update_children_service_spec.rb:79
		if got := reloadFixedCharge(h, ownFC.ID); !got.Properties.Equal(catalog.MustProperties(`{"amount":"99"}`)) || !got.Units.Equal(money.MustParse("1")) {
			t.Errorf("modified override = %s, %s units, want = unchanged", got.Properties.Raw(), got.Units)
		}
	})

	// source: spec/services/fixed_charges/update_service_spec.rb:168
	t.Run("cascade to an override of another model", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
		child := h.Plan(period.Monthly, 900, overrideOf(p))
		h.Subscription(h.Customer(), child)
		graduated := `{"graduated_ranges":[{"from_value":0,"to_value":null,"per_unit_amount":"1","flat_amount":"0"}]}`
		childFC := h.FixedCharge(child, a, catalog.FixedChargeModelGraduated, graduated, "1", childOf(f))
		if _, err := e.Plans.UpdateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), plans.FixedChargeInput{Units: fcUnits("4")}, true); err != nil {
			t.Fatalf("UpdateFixedCharge() error = %v", err)
		}
		fcDrain(t, h, e)
		if got := reloadFixedCharge(h, childFC.ID); !got.Units.Equal(money.MustParse("1")) {
			t.Errorf("override units = %s, want = 1", got.Units)
		}
	})
}

func fixedChargeDefaults(t *testing.T, model catalog.FixedChargeModel) catalog.Properties {
	t.Helper()
	h, e := setup(t)
	p := h.Plan(period.Monthly, 1000)
	in := plans.FixedChargeInput{AddOn: refs.ByID(h.AddOn(1).ID), Code: core.Some("x"), ChargeModel: core.Some(model)}
	f, err := e.Plans.CreateFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), in, false)
	if err != nil {
		t.Fatalf("CreateFixedCharge() error = %v", err)
	}
	return f.Properties
}

func TestDeleteFixedCharge(t *testing.T) {
	// source: spec/services/fixed_charges/destroy_service_spec.rb:15
	t.Run("soft deletes", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		got, err := e.Plans.DeleteFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByCode(f.Code), false)
		if err != nil {
			t.Fatalf("DeleteFixedCharge() error = %v", err)
		}
		if got.ID != f.ID || got.DeletedAt == nil || reloadFixedCharge(h, f.ID).DeletedAt == nil {
			t.Errorf("fixed charge = %+v, want = %s discarded", got, f.ID)
		}
		// source: spec/services/fixed_charges/destroy_service_spec.rb:72
		_, err = e.Plans.DeleteFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), false)
		fcWantNotFound(t, err, "fixed_charge")
	})

	// source: spec/services/fixed_charges/destroy_service_spec.rb:84
	t.Run("cascade", func(t *testing.T) {
		tests := []struct {
			name     string
			children bool
			cascade  bool
			want     int
		}{
			{"with children", true, true, 1},
			// source: spec/services/fixed_charges/destroy_service_spec.rb:100
			{"without children", false, true, 0},
			// source: spec/services/fixed_charges/destroy_service_spec.rb:111
			{"without cascade", true, false, 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 1000)
				a := h.AddOn(100)
				f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
				if tt.children {
					child := h.Plan(period.Monthly, 900, overrideOf(p))
					h.FixedCharge(child, a, catalog.FixedChargeModelStandard, fcStandard, "1", childOf(f))
				}
				if _, err := e.Plans.DeleteFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), tt.cascade); err != nil {
					t.Fatalf("DeleteFixedCharge() error = %v", err)
				}
				if got := len(fcJobs(h, kernel.JobDestroyFixedChargeChildren)); got != tt.want {
					t.Errorf("destroy children jobs = %d, want = %d", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/fixed_charges/destroy_children_service_spec.rb:31
	t.Run("destroys the overrides on live plans", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
		live := h.Plan(period.Monthly, 900, overrideOf(p))
		h.Subscription(h.Customer(), live)
		liveFC := h.FixedCharge(live, a, catalog.FixedChargeModelStandard, fcStandard, "1", childOf(f))
		dead := h.Plan(period.Monthly, 900, overrideOf(p))
		deadSub := h.Subscription(h.Customer(), dead)
		h.UpdateSubscription(deadSub, func(s *subscription.Subscription) {
			if err := s.Terminate(h.Now()); err != nil {
				t.Fatal(err)
			}
		})
		deadFC := h.FixedCharge(dead, a, catalog.FixedChargeModelStandard, fcStandard, "1", childOf(f))
		if _, err := e.Plans.DeleteFixedCharge(h.Ctx, h.OrgID(), refs.ByID(p.ID), refs.ByID(f.ID), true); err != nil {
			t.Fatalf("DeleteFixedCharge() error = %v", err)
		}
		fcDrain(t, h, e)
		if reloadFixedCharge(h, liveFC.ID).DeletedAt == nil {
			t.Errorf("override on a live plan kept, want = discarded")
		}
		// source: spec/services/fixed_charges/destroy_children_service_spec.rb:74
		if reloadFixedCharge(h, deadFC.ID).DeletedAt != nil {
			t.Errorf("override on a plan with terminated subscriptions discarded, want = kept")
		}
	})
}

func TestPlanFixedCharges(t *testing.T) {
	// source: spec/services/fixed_charges/generate_code_service_spec.rb:13
	t.Run("generated codes", func(t *testing.T) {
		tests := []struct {
			name     string
			existing []string
			child    bool
			want     string
		}{
			{"no fixed charge", nil, false, "seats"},
			// source: spec/services/fixed_charges/generate_code_service_spec.rb:19
			{"base code used", []string{"seats"}, false, "seats_2"},
			// source: spec/services/fixed_charges/generate_code_service_spec.rb:29
			{"suffixes used", []string{"seats", "seats_2"}, false, "seats_3"},
			// source: spec/services/fixed_charges/generate_code_service_spec.rb:40
			{"gaps in suffixes", []string{"seats", "seats_5"}, false, "seats_6"},
			// source: spec/services/fixed_charges/generate_code_service_spec.rb:52
			{"similar prefixes", []string{"seats_extra"}, false, "seats"},
			// source: spec/services/fixed_charges/generate_code_service_spec.rb:73
			{"child fixed charges", []string{"seats"}, true, "seats"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 1000)
				target := p
				if tt.child {
					target = h.Plan(period.Monthly, 900, overrideOf(p))
				}
				a := h.AddOn(100, func(a *catalog.AddOn) { a.Code = "seats" })
				var keep []plans.FixedChargeInput
				for _, code := range tt.existing {
					f := h.FixedCharge(target, a, catalog.FixedChargeModelStandard, fcStandard, "1", func(f *catalog.FixedCharge) {
						f.Code = code
						if tt.child {
							f.ParentID = core.NewID[core.FixedChargeID](h.IDs)
						}
					})
					keep = append(keep, plans.FixedChargeInput{ID: f.ID})
				}
				in := fcInput(a, "")
				in.Code = core.Optional[string]{}
				if tt.child {
					keep = nil
				}
				if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{FixedCharges: core.Some(append(keep, in))}); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				fcs := planFixedCharges(h, p.ID)
				if got := fcs[len(fcs)-1].Code; got != tt.want {
					t.Errorf("code = %q, want = %q", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/plans/update_service_spec.rb (fixed charges), app/services/plans/update_service.rb:278-313
	t.Run("replaces the fixed charges", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		sub := h.Subscription(h.Customer(), p)
		a := h.AddOn(100)
		kept := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1", payInAdvance)
		dropped := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1")
		in := []plans.FixedChargeInput{
			{ID: kept.ID, Units: fcUnits("2"), ApplyUnitsImmediately: true},
			fcInput(a, "new"),
		}
		if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{FixedCharges: core.Some(in)}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		fcs := planFixedCharges(h, p.ID)
		if len(fcs) != 2 || fcs[0].ID != kept.ID || fcs[1].Code != "new" {
			t.Fatalf("fixed charges = %+v, want = kept and new", fcs)
		}
		if !fcs[0].Units.Equal(money.MustParse("2")) {
			t.Errorf("kept units = %s, want = 2", fcs[0].Units)
		}
		if reloadFixedCharge(h, dropped.ID).DeletedAt == nil {
			t.Errorf("dropped fixed charge kept, want = discarded")
		}
		if n := len(fcJobs(h, kernel.JobCreateAllInAdvanceFixedCharges)); n != 1 {
			t.Errorf("fan-out jobs = %d, want = 1", n)
		}
		if n := len(fcEvents(h, sub.ID)); n != 2 {
			t.Errorf("events = %d, want = 2 (kept and new)", n)
		}
	})

	// source: spec/services/fixed_charges/cascade_child_plan_update_service_spec.rb:25
	t.Run("cascades to the override plans", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		a := h.AddOn(100)
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, fcStandard, "1", payInAdvance)
		child := h.Plan(period.Monthly, 900, overrideOf(p))
		c := h.Customer()
		childSub := h.Subscription(c, child)
		childFC := h.FixedCharge(child, a, catalog.FixedChargeModelStandard, fcStandard, "1", childOf(f), payInAdvance)
		in := []plans.FixedChargeInput{
			{ID: f.ID, Units: fcUnits("3"), Properties: fcProps(`{"amount":"20"}`), ApplyUnitsImmediately: true},
			fcInput(a, "new"),
		}
		if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{FixedCharges: core.Some(in), Cascade: true}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if n := len(fcJobs(h, kernel.JobCascadeFixedCharges)); n != 1 {
			t.Fatalf("cascade jobs = %d, want = 1", n)
		}
		fcDrain(t, h, e)
		fcs := planFixedCharges(h, child.ID)
		if len(fcs) != 2 {
			t.Fatalf("override plan fixed charges = %d, want = 2", len(fcs))
		}
		// source: spec/services/fixed_charges/cascade_child_plan_update_service_spec.rb:201
		if got := reloadFixedCharge(h, childFC.ID); !got.Units.Equal(money.MustParse("3")) || !got.Properties.Equal(catalog.MustProperties(`{"amount":"20"}`)) {
			t.Errorf("updated override = %s, %s units, want = {amount: 20}, 3 units", got.Properties.Raw(), got.Units)
		}
		// source: spec/services/fixed_charges/cascade_child_plan_update_service_spec.rb:54
		if fcs[1].Code != "new" || fcs[1].ParentID.IsZero() {
			t.Errorf("created override = %+v, want = a copy of new", fcs[1])
		}
		// source: spec/services/fixed_charges/cascade_child_plan_update_service_spec.rb:216
		invs := h.Invoices(c.ID)
		if len(invs) != 1 {
			t.Fatalf("override plan invoices = %d, want = 1", len(invs))
		}
		if evs := fcEvents(h, childSub.ID); len(evs) == 0 {
			t.Errorf("override plan subscription events = 0, want > 0")
		}
	})
}

func TestOverrideFixedCharge(t *testing.T) {
	override := func(h *billingtest.Harness, e *billing.Engine, p *catalog.Plan, in plans.OverrideInput, sub *subscription.Subscription) (*catalog.Plan, error) {
		var out *catalog.Plan
		err := h.Store.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			out, err = e.Plans.Override(ctx, r, p, in, sub)
			return err
		})
		return out, err
	}

	// source: spec/services/fixed_charges/override_service_spec.rb:51
	t.Run("copies the fixed charge", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		tax := h.Tax("vat", "20")
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1", func(f *catalog.FixedCharge) {
			f.TaxIDs = []core.TaxID{tax.ID}
		})
		in := plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{
			f.ID: {Properties: fcProps(`{"amount":"20"}`), InvoiceDisplayName: core.Some(new("Mine")), Units: fcUnits("4")},
		}}
		np, err := override(h, e, p, in, nil)
		if err != nil {
			t.Fatalf("Override() error = %v", err)
		}
		fcs := planFixedCharges(h, np.ID)
		if len(fcs) != 1 {
			t.Fatalf("fixed charges = %d, want = 1", len(fcs))
		}
		o := fcs[0]
		if o.ParentID != f.ID || o.AddOnID != f.AddOnID || o.Code != f.Code || !o.Units.Equal(money.MustParse("4")) {
			t.Errorf("override = %+v, want = a copy of %s with 4 units", o, f.ID)
		}
		if !o.Properties.Equal(catalog.MustProperties(`{"amount":"20"}`)) || o.InvoiceDisplayName == nil || *o.InvoiceDisplayName != "Mine" {
			t.Errorf("override = %s, %v, want = {amount: 20}, Mine", o.Properties.Raw(), o.InvoiceDisplayName)
		}
		// source: spec/services/fixed_charges/override_service_spec.rb:150
		if len(o.TaxIDs) != 0 {
			t.Errorf("override taxes = %v, want = none", o.TaxIDs)
		}
	})

	// source: spec/services/fixed_charges/override_service_spec.rb:190
	t.Run("another charge model", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{
			f.ID: {ChargeModel: core.Some(catalog.FixedChargeModelGraduated)},
		}}
		_, err := override(h, e, p, in, nil)
		fcWantRule(t, err, "cannot_override_charge_model")
	})

	// source: spec/services/fixed_charges/override_service_spec.rb:255
	t.Run("events of the subscription", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		c := h.Customer()
		sub := h.Subscription(c, p)
		other := h.Subscription(c, p)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1")
		in := plans.OverrideInput{FixedCharges: map[core.FixedChargeID]plans.FixedChargeInput{
			f.ID: {Units: fcUnits("5"), ApplyUnitsImmediately: true},
		}}
		np, err := override(h, e, p, in, sub)
		if err != nil {
			t.Fatalf("Override() error = %v", err)
		}
		o := planFixedCharges(h, np.ID)[0]
		evs := fcEvents(h, sub.ID)
		if len(evs) != 1 || evs[0].FixedChargeID != o.ID || !evs[0].Units.Equal(money.MustParse("5")) || !evs[0].ApplyUnitsImmediately {
			t.Errorf("events = %+v, want = one immediate event of 5 units on %s", evs, o.ID)
		}
		if n := len(fcEvents(h, other.ID)); n != 0 {
			t.Errorf("other subscription events = %d, want = 0", n)
		}
	})
}

func TestSubscriptionFixedCharges(t *testing.T) {
	type fixture struct {
		h   *billingtest.Harness
		e   *billing.Engine
		p   *catalog.Plan
		f   *catalog.FixedCharge
		c   core.CustomerID
		sub *subscription.Subscription
	}
	newFixture := func(t *testing.T, opts ...func(*catalog.FixedCharge)) fixture {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		c := h.Customer()
		sub := h.Subscription(c, p)
		f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1", opts...)
		return fixture{h, e, p, f, c.ID, sub}
	}
	update := func(x fixture, in plans.FixedChargeInput) (*catalog.FixedCharge, error) {
		return x.e.Plans.UpdateSubscriptionFixedCharge(x.h.Ctx, x.h.OrgID(), refs.SubscriptionRef{ExternalID: x.sub.ExternalID}, refs.ByCode(x.f.Code), in)
	}

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:37
	t.Run("incomplete subscription", func(t *testing.T) {
		x := newFixture(t)
		x.h.UpdateSubscription(x.sub, incomplete)
		_, err := x.e.Plans.UpdateSubscriptionFixedCharge(x.h.Ctx, x.h.OrgID(), refs.SubscriptionRef{ID: x.sub.ID}, refs.ByID(x.f.ID), plans.FixedChargeInput{Units: fcUnits("2")})
		fcWantInvalid(t, err, "base", "subscription_incomplete")
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:206
	t.Run("fixed charge not found", func(t *testing.T) {
		x := newFixture(t)
		_, err := x.e.Plans.UpdateSubscriptionFixedCharge(x.h.Ctx, x.h.OrgID(), refs.SubscriptionRef{ID: x.sub.ID}, refs.ByCode("missing"), plans.FixedChargeInput{Units: fcUnits("2")})
		fcWantNotFound(t, err, "fixed_charge")
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:49
	t.Run("overrides the plan", func(t *testing.T) {
		x := newFixture(t)
		got, err := update(x, plans.FixedChargeInput{InvoiceDisplayName: core.Some(new("Overridden Fixed Charge")), Units: fcUnits("10")})
		if err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() error = %v", err)
		}
		sub := reloadSubscription(x.h, x.sub.ID)
		np := reloadPlan(x.h, sub.PlanID)
		if np.ParentID != x.p.ID {
			t.Fatalf("subscription plan parent = %s, want = %s", np.ParentID, x.p.ID)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:60
		if got.ParentID != x.f.ID || got.PlanID != np.ID {
			t.Errorf("fixed charge = %s on %s, want = an override of %s on %s", got.ID, got.PlanID, x.f.ID, np.ID)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:80
		if *got.InvoiceDisplayName != "Overridden Fixed Charge" || !got.Units.Equal(money.MustParse("10")) {
			t.Errorf("fixed charge = %v, %s, want = Overridden Fixed Charge, 10", *got.InvoiceDisplayName, got.Units)
		}

		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:218
		again, err := update(x, plans.FixedChargeInput{InvoiceDisplayName: core.Some(new("Again"))})
		if err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() again error = %v", err)
		}
		if again.ID != got.ID || *again.InvoiceDisplayName != "Again" {
			t.Errorf("fixed charge = %s %v, want = %s updated in place", again.ID, *again.InvoiceDisplayName, got.ID)
		}
		if p := reloadSubscription(x.h, x.sub.ID).PlanID; p != np.ID {
			t.Errorf("subscription plan = %s, want = %s", p, np.ID)
		}
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:87
	t.Run("negative units", func(t *testing.T) {
		x := newFixture(t)
		_, err := update(x, plans.FixedChargeInput{Units: fcUnits("-1"), InvoiceDisplayName: core.Some(new("x"))})
		fcWantInvalid(t, err, "units", core.CodeNegative)
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:98
	t.Run("pay-in-advance units applied immediately", func(t *testing.T) {
		tests := []struct {
			name      string
			advance   bool
			immediate bool
			units     string
			want      int
		}{
			{"pay in advance", true, true, "3", 1},
			// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:128
			{"pay in arrears", false, true, "3", 0},
			// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:135
			{"next period", true, false, "3", 0},
			// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:157
			{"units unchanged", true, true, "1", 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				x := newFixture(t, func(f *catalog.FixedCharge) { f.PayInAdvance = tt.advance })
				x.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.FixedChargeEvents.Record(ctx, &subscription.FixedChargeEvent{
						ID: core.NewID[core.FixedChargeEventID](x.h.IDs), OrganizationID: x.h.OrgID(), SubscriptionID: x.sub.ID,
						FixedChargeID: x.f.ID, Units: money.MustParse("1"), Timestamp: now.Add(-time.Hour), ApplyUnitsImmediately: true, CreatedAt: now, UpdatedAt: now,
					}, time.UTC)
				})
				in := plans.FixedChargeInput{Units: fcUnits(tt.units), ApplyUnitsImmediately: tt.immediate, Properties: fcProps(fcStandard)}
				if _, err := update(x, in); err != nil {
					t.Fatalf("UpdateSubscriptionFixedCharge() error = %v", err)
				}
				if got := len(fcJobs(x.h, kernel.JobCreateInAdvanceFixedCharges)); got != tt.want {
					t.Errorf("billing jobs = %d, want = %d", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:324
	t.Run("units only", func(t *testing.T) {
		x := newFixture(t, payInAdvance)
		got, err := update(x, plans.FixedChargeInput{Units: fcUnits("5"), ApplyUnitsImmediately: true})
		if err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() error = %v", err)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:337
		if p := reloadSubscription(x.h, x.sub.ID).PlanID; p != x.p.ID {
			t.Errorf("subscription plan = %s, want = %s", p, x.p.ID)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:343
		if got.ID != x.f.ID || !got.Units.Equal(money.MustParse("5")) {
			t.Errorf("fixed charge = %s with %s units, want = %s with 5", got.ID, got.Units, x.f.ID)
		}
		if !reloadFixedCharge(x.h, x.f.ID).Units.Equal(money.MustParse("1")) {
			t.Errorf("plan fixed charge units changed, want = 1")
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:328
		ovs := unitsOverrides(x.h, x.sub.ID)
		if len(ovs) != 1 || ovs[0].FixedChargeID != x.f.ID || !ovs[0].Units.Equal(money.MustParse("5")) {
			t.Fatalf("units overrides = %+v, want = 5 units of %s", ovs, x.f.ID)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:350
		if evs := fcEvents(x.h, x.sub.ID); len(evs) != 1 || !evs[0].Units.Equal(money.MustParse("5")) {
			t.Errorf("events = %+v, want = one event of 5 units", evs)
		}
		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:371
		if n := len(fcJobs(x.h, kernel.JobCreateInAdvanceFixedCharges)); n != 1 {
			t.Errorf("billing jobs = %d, want = 1", n)
		}
		fcDrain(t, x.h, x.e)
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb
		invs := x.h.Invoices(x.c)
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		if fees := x.h.Fees(invs[0].ID); len(fees) != 1 || !fees[0].Units.Equal(money.MustParse("5")) || fees[0].Amount != 5000 {
			t.Errorf("fees = %+v, want = one fee of 5 units, 5000", fees)
		}

		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:357
		x.h.Clock.Advance(time.Hour)
		if _, err := update(x, plans.FixedChargeInput{Units: fcUnits("6")}); err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() again error = %v", err)
		}
		if ovs := unitsOverrides(x.h, x.sub.ID); len(ovs) != 1 || !ovs[0].Units.Equal(money.MustParse("6")) {
			t.Errorf("units overrides = %+v, want = one of 6 units", ovs)
		}

		// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:384
		before := len(fcEvents(x.h, x.sub.ID))
		if _, err := update(x, plans.FixedChargeInput{Units: fcUnits("6")}); err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() unchanged error = %v", err)
		}
		if got := len(fcEvents(x.h, x.sub.ID)); got != before {
			t.Errorf("events = %d, want = %d", got, before)
		}

		// source: app/controllers/api/v1/subscriptions/fixed_charges_controller.rb:11-29
		page, err := x.e.Plans.ListSubscriptionFixedCharges(x.h.Ctx, x.h.OrgID(), refs.SubscriptionRef{ID: x.sub.ID}, core.PageRequest{})
		if err != nil {
			t.Fatalf("ListSubscriptionFixedCharges() error = %v", err)
		}
		if len(page.Items) != 1 || !page.Items[0].Units.Equal(money.MustParse("6")) {
			t.Errorf("fixed charges = %+v, want = one with 6 units", page.Items)
		}
	})

	// source: spec/services/subscriptions/update_or_override_fixed_charge_service_spec.rb:460
	t.Run("promotes the units overrides", func(t *testing.T) {
		x := newFixture(t)
		other := x.h.FixedCharge(x.p, x.h.AddOn(50), catalog.FixedChargeModelStandard, fcStandard, "1")
		otherRef := refs.ByID(other.ID)
		if _, err := x.e.Plans.UpdateSubscriptionFixedCharge(x.h.Ctx, x.h.OrgID(), refs.SubscriptionRef{ID: x.sub.ID}, otherRef, plans.FixedChargeInput{Units: fcUnits("8")}); err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() units error = %v", err)
		}
		if _, err := update(x, plans.FixedChargeInput{Properties: fcProps(`{"amount":"30"}`)}); err != nil {
			t.Fatalf("UpdateSubscriptionFixedCharge() error = %v", err)
		}
		if ovs := unitsOverrides(x.h, x.sub.ID); len(ovs) != 0 {
			t.Errorf("units overrides = %d, want = 0", len(ovs))
		}
		np := reloadSubscription(x.h, x.sub.ID).PlanID
		for _, f := range planFixedCharges(x.h, np) {
			want := "1"
			if f.ParentID == other.ID {
				want = "8"
			}
			if !f.Units.Equal(money.MustParse(want)) {
				t.Errorf("override of %s units = %s, want = %s", f.ParentID, f.Units, want)
			}
		}
	})
}

func TestUnitsOverrideWrite(t *testing.T) {
	// source: spec/services/subscriptions/fixed_charge_units_overrides/write_service_spec.rb:63
	tests := []struct {
		name      string
		advance   bool
		immediate bool
		want      int
	}{
		{"pay in advance, immediately", true, true, 1},
		// source: spec/services/subscriptions/fixed_charge_units_overrides/write_service_spec.rb:74
		{"pay in arrears, immediately", false, true, 0},
		// source: spec/services/subscriptions/fixed_charge_units_overrides/write_service_spec.rb:83
		{"pay in advance, next period", true, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 1000)
			sub := h.Subscription(h.Customer(), p)
			f := h.FixedCharge(p, h.AddOn(100), catalog.FixedChargeModelStandard, fcStandard, "1", func(f *catalog.FixedCharge) { f.PayInAdvance = tt.advance })
			in := plans.FixedChargeInput{Units: fcUnits("4"), ApplyUnitsImmediately: tt.immediate}
			if _, err := e.Plans.UpdateSubscriptionFixedCharge(h.Ctx, h.OrgID(), refs.SubscriptionRef{ID: sub.ID}, refs.ByID(f.ID), in); err != nil {
				t.Fatalf("UpdateSubscriptionFixedCharge() error = %v", err)
			}
			// source: spec/services/subscriptions/fixed_charge_units_overrides/write_service_spec.rb:34
			if evs := fcEvents(h, sub.ID); len(evs) != 1 || !evs[0].Units.Equal(money.MustParse("4")) || evs[0].ApplyUnitsImmediately != tt.immediate {
				t.Errorf("events = %+v, want = one event of 4 units", evs)
			}
			if got := len(fcJobs(h, kernel.JobCreateInAdvanceFixedCharges)); got != tt.want {
				t.Errorf("billing jobs = %d, want = %d", got, tt.want)
			}
		})
	}
}
