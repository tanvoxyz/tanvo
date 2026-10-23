package products_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func phaseCodes(phases []*catalog.RatePhase) []string {
	out := make([]string, len(phases))
	for i, p := range phases {
		out[i] = p.Code + ":" + string(rune('0'+p.Position))
	}
	return out
}

func TestCreatePlanRateCard(t *testing.T) {
	t.Parallel()
	// source: spec/services/plan_rate_cards/create_service_spec.rb:14
	// source: spec/services/plan_rate_cards/create_service_spec.rb:23
	t.Run("applies the card on a single indefinite default phase", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		plan := f.h.Plan(period.Monthly, 0)
		p, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByCode(plan.Code), products.PlanRateCardInput{RateCard: refs.ByCode(f.card(t, "api-usd").Code), Units: ptr(money.MustParse("2"))})
		if err != nil || p.PlanID != plan.ID || p.Units.String() != "2" {
			t.Fatalf("CreatePlanRateCard() = %+v, %v, want the card applied", p, err)
		}
		phases, err := f.s.ListPhases(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode("api-usd"))
		if err != nil || len(phases) != 1 || phases[0].Code != "default" || phases[0].Position != 1 || phases[0].BillingIntervalCycleCount != nil {
			t.Errorf("phases = %v, %v, want the default phase", phaseCodes(phases), err)
		}
	})
	// source: spec/services/plan_rate_cards/create_service_spec.rb:35
	// source: spec/services/plan_rate_cards/create_service_spec.rb:48
	// source: spec/services/plan_rate_cards/create_service_spec.rb:61
	t.Run("prices each slice of a product once", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		plan := f.attach(t, f.card(t, "whole"))
		apply := func(c *catalog.RateCard) error {
			_, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(c.ID)})
			return err
		}
		wantFieldError(t, apply(f.card(t, "whole-2")), "rate_card", catalog.CodeProductAlreadyPriced)
		if err := apply(f.card(t, "eu", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) })); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v, want another slice accepted", err)
		}
		wantFieldError(t, apply(f.card(t, "eu-2", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) })), "rate_card", catalog.CodeProductFilterAlreadyPriced)
	})
	// source: spec/services/plan_rate_cards/create_service_spec.rb:69
	// source: spec/services/plan_rate_cards/create_service_spec.rb:126
	// source: spec/services/plan_rate_cards/create_service_spec.rb:135
	t.Run("rejects another currency and missing references", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		plan := f.h.Plan(period.Monthly, 0)
		apply := func(planRef, card refs.Ref) error {
			_, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), planRef, products.PlanRateCardInput{RateCard: card})
			return err
		}
		eurCard := f.card(t, "api-eur", func(in *products.RateCardInput) { in.Currency = eur })
		wantFieldError(t, apply(refs.ByID(plan.ID), refs.ByID(eurCard.ID)), "currency", catalog.CodeCurrencyDoesNotMatch)
		wantNotFound(t, apply(refs.ByCode("nope"), refs.ByID(eurCard.ID)), "plan")
		wantNotFound(t, apply(refs.ByID(plan.ID), refs.ByCode("nope")), "rate_card")
	})
	// source: spec/services/plan_rate_cards/create_service_spec.rb:89
	// source: spec/services/plan_rate_cards/create_service_spec.rb:106
	// source: spec/services/plan_rate_cards/create_service_spec.rb:116
	t.Run("creates the given ramp, or nothing for an invalid one", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		plan := f.h.Plan(period.Monthly, 0)
		c := f.card(t, "api-usd")
		apply := func(phases []products.PhaseInput) error {
			_, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(c.ID), Phases: &phases})
			return err
		}
		wantFieldError(t, apply([]products.PhaseInput{}), "rate_phases", core.CodeRequired)
		wantFieldError(t, apply([]products.PhaseInput{
			{Code: "launch", Position: ptr(1), BillingIntervalCycleCount: ptr(3)},
			{Code: "standard", Position: ptr(3)},
		}), "rate_phases", catalog.CodePositionsMustBeContiguous)
		wantFieldError(t, apply([]products.PhaseInput{
			{Code: "launch", Position: ptr(1)}, {Code: "standard", Position: ptr(2)},
		}), "rate_phases", catalog.CodeIndefinitePhaseMustBeLast)
		wantFieldError(t, apply([]products.PhaseInput{
			{Code: "launch", Position: ptr(1), Override: &products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"x"}`)}},
		}), "rate_phases.rate_override.properties", "invalid_amount")
		if _, err := f.s.PlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(c.Code)); err == nil {
			t.Fatal("PlanRateCard() found a plan rate card, want the failed creates rolled back")
		}
		if err := apply([]products.PhaseInput{
			{Code: "standard", Position: ptr(2)},
			{Code: "launch", Position: ptr(1), BillingIntervalCycleCount: ptr(3), Override: &products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"0.5"}`)}},
		}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		phases, _ := f.s.ListPhases(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(c.Code))
		if got := phaseCodes(phases); len(got) != 2 || got[0] != "launch:1" || got[1] != "standard:2" || phases[0].Override == nil {
			t.Errorf("phases = %v, want launch (overridden) then standard", got)
		}
	})
}

func TestUpdateDeletePlanRateCard(t *testing.T) {
	t.Parallel()
	// source: spec/services/plan_rate_cards/update_service_spec.rb:14
	// source: spec/services/plan_rate_cards/update_service_spec.rb:31
	// source: spec/services/plan_rate_cards/destroy_service_spec.rb:12
	// source: spec/services/plan_rate_cards/destroy_service_spec.rb:18
	t.Run("changes the units and discards the card with its phases", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		plan := f.attach(t, c)
		got, err := f.s.UpdatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(c.Code), products.PlanRateCardUpdate{Units: core.Some(ptr(money.MustParse("5")))})
		if err != nil || got.Units.String() != "5" {
			t.Fatalf("UpdatePlanRateCard() = %+v, %v, want 5 units", got, err)
		}
		_, err = f.s.UpdatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode("nope"), products.PlanRateCardUpdate{})
		wantNotFound(t, err, "plan_rate_card")
		other := f.h.Plan(period.Monthly, 0)
		_, err = f.s.PlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(other.ID), refs.ByID(got.ID))
		wantNotFound(t, err, "plan_rate_card")
		free := f.attach(t, f.card(t, "free"))
		deleted, err := f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(free.ID), refs.ByCode("free"))
		if err != nil || deleted.DeletedAt == nil {
			t.Fatalf("DeletePlanRateCard() = %+v, %v, want the discarded card", deleted, err)
		}
		_, err = f.s.ListPhases(t.Context(), f.h.OrgID(), refs.ByID(free.ID), refs.ByCode("free"))
		wantNotFound(t, err, "plan_rate_card")
		_, err = f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(free.ID), refs.ByID(deleted.ID))
		wantNotFound(t, err, "plan_rate_card")
		if _, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(free.ID), products.PlanRateCardInput{RateCard: refs.ByCode("free")}); err != nil {
			t.Errorf("CreatePlanRateCard(card of discarded) error = %v", err)
		}
	})
}

func TestPhases(t *testing.T) {
	t.Parallel()
	// ramp applies a card on a plan with phases intro (2 cycles) then
	// standard (indefinite).
	ramp := func(t *testing.T) (*fixture, refs.Ref, refs.Ref) {
		t.Helper()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		plan := f.h.Plan(period.Monthly, 0)
		phases := []products.PhaseInput{{Code: "intro", Position: ptr(1), BillingIntervalCycleCount: ptr(2)}, {Code: "standard", Position: ptr(2)}}
		if _, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(c.ID), Phases: &phases}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		return f, refs.ByID(plan.ID), refs.ByCode(c.Code)
	}
	list := func(t *testing.T, f *fixture, plan, prc refs.Ref) []string {
		t.Helper()
		phases, err := f.s.ListPhases(t.Context(), f.h.OrgID(), plan, prc)
		if err != nil {
			t.Fatalf("ListPhases() error = %v", err)
		}
		return phaseCodes(phases)
	}
	// source: spec/services/rate_phases/create_service_spec.rb:16
	// source: spec/services/rate_phases/create_service_spec.rb:51
	// source: spec/services/rate_phases/create_service_spec.rb:73
	t.Run("inserts phases, shifting the later ones", func(t *testing.T) {
		t.Parallel()
		f, plan, prc := ramp(t)
		if _, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, products.PhaseInput{Code: "trial", Position: ptr(1), BillingIntervalCycleCount: ptr(1), Name: ptr("Trial")}); err != nil {
			t.Fatalf("CreatePhase() error = %v", err)
		}
		if _, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, products.PhaseInput{Code: "ramp", BillingIntervalCycleCount: ptr(4)}); err != nil {
			t.Fatalf("CreatePhase() error = %v", err)
		}
		if got := list(t, f, plan, prc); len(got) != 4 || got[0] != "trial:1" || got[1] != "intro:2" || got[2] != "ramp:3" || got[3] != "standard:4" {
			t.Errorf("phases = %v, want trial intro ramp standard", got)
		}
	})
	// source: spec/services/rate_phases/create_service_spec.rb:39
	// source: spec/services/rate_phases/create_service_spec.rb:82
	// source: spec/services/rate_phases/create_service_spec.rb:101
	// source: spec/services/rate_phases/create_service_spec.rb:113
	t.Run("keeps a single indefinite phase last", func(t *testing.T) {
		t.Parallel()
		f, plan, prc := ramp(t)
		create := func(in products.PhaseInput) error {
			_, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, in)
			return err
		}
		wantFieldError(t, create(products.PhaseInput{BillingIntervalCycleCount: ptr(1)}), "code", core.CodeRequired)
		// After the indefinite phase, it leaves an indefinite one before the
		// last, which is reported first.
		wantFieldError(t, create(products.PhaseInput{Code: "end", Position: ptr(3), BillingIntervalCycleCount: ptr(1)}), "billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		wantFieldError(t, create(products.PhaseInput{Code: "again"}), "billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		wantFieldError(t, create(products.PhaseInput{Code: "far", Position: ptr(4), BillingIntervalCycleCount: ptr(1)}), "position", catalog.CodePositionsMustBeContiguous)
		wantFieldError(t, create(products.PhaseInput{Code: "mid", Position: ptr(1)}), "billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		wantFieldError(t, create(products.PhaseInput{Code: "intro", Position: ptr(1), BillingIntervalCycleCount: ptr(1)}), "code", core.CodeAlreadyExists)
	})
	// source: spec/services/rate_phases/update_service_spec.rb:17
	// source: spec/services/rate_phases/update_service_spec.rb:44
	// source: spec/services/rate_phases/update_service_spec.rb:66
	// source: spec/services/rate_phases/update_service_spec.rb:75
	// source: spec/services/rate_phases/update_service_spec.rb:85
	// source: spec/services/rate_phases/update_service_spec.rb:110
	t.Run("updates a phase, its duration and its override", func(t *testing.T) {
		t.Parallel()
		f, plan, prc := ramp(t)
		update := func(code string, in products.PhaseUpdate) (*catalog.RatePhase, error) {
			return f.s.UpdatePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode(code), in)
		}
		override := &products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"0.5"}`)}
		got, err := update("intro", products.PhaseUpdate{Name: core.Some(ptr("Intro")), BillingIntervalCycleCount: core.Some(ptr(3)), Override: core.Some(override)})
		if err != nil || *got.Name != "Intro" || *got.BillingIntervalCycleCount != 3 || got.Override == nil {
			t.Fatalf("UpdatePhase() = %+v, %v, want the phase changed", got, err)
		}
		if got, err = update("intro", products.PhaseUpdate{Override: core.Some[*products.RateOverrideInput](nil)}); err != nil || got.Override != nil {
			t.Errorf("UpdatePhase() = %+v, %v, want the override cleared", got, err)
		}
		_, err = update("intro", products.PhaseUpdate{Override: core.Some(&products.RateOverrideInput{})})
		wantFieldError(t, err, "rate_override.rate_model", core.CodeRequired)
		_, err = update("intro", products.PhaseUpdate{BillingIntervalCycleCount: core.Some[*int](nil)})
		wantFieldError(t, err, "billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		_, err = update("standard", products.PhaseUpdate{BillingIntervalCycleCount: core.Some(ptr(1))})
		wantFieldError(t, err, "billing_interval_cycle_count", catalog.CodeLastPhaseMustBeIndefinite)
		_, err = f.s.UpdatePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode("nope"), products.PhaseUpdate{})
		wantNotFound(t, err, "rate_phase")
	})
	// source: spec/services/rate_phases/update_service_spec.rb:128
	// source: spec/services/rate_phases/update_service_spec.rb:138
	// source: spec/services/rate_phases/update_service_spec.rb:157
	// source: spec/services/rate_phases/update_service_spec.rb:168
	// source: spec/services/rate_phases/update_service_spec.rb:208
	t.Run("moves phases, the indefinite one staying last", func(t *testing.T) {
		t.Parallel()
		f, plan, prc := ramp(t)
		for _, code := range []string{"b", "c"} {
			if _, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, products.PhaseInput{Code: code, BillingIntervalCycleCount: ptr(1)}); err != nil {
				t.Fatalf("CreatePhase() error = %v", err)
			}
		}
		move := func(code string, to int) error {
			_, err := f.s.UpdatePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode(code), products.PhaseUpdate{Position: core.Some(to)})
			return err
		}
		if err := move("intro", 3); err != nil {
			t.Fatalf("move later error = %v", err)
		}
		if got := list(t, f, plan, prc); got[0] != "b:1" || got[1] != "c:2" || got[2] != "intro:3" || got[3] != "standard:4" {
			t.Errorf("phases = %v, want b c intro standard", got)
		}
		if err := move("c", 1); err != nil {
			t.Fatalf("move earlier error = %v", err)
		}
		if got := list(t, f, plan, prc); got[0] != "c:1" || got[1] != "b:2" || got[2] != "intro:3" {
			t.Errorf("phases = %v, want c b intro standard", got)
		}
		wantFieldError(t, move("b", 4), "position", catalog.CodeLastPhaseMustBeIndefinite)
		wantFieldError(t, move("standard", 1), "position", catalog.CodeIndefinitePhaseMustBeLast)
		wantFieldError(t, move("b", 0), "position", catalog.CodePositionsMustBeContiguous)
		wantFieldError(t, move("b", 5), "position", catalog.CodePositionsMustBeContiguous)
	})
	// source: spec/services/rate_phases/destroy_service_spec.rb:18
	// source: spec/services/rate_phases/destroy_service_spec.rb:46
	// source: spec/services/rate_phases/destroy_service_spec.rb:59
	t.Run("deletes phases but the indefinite tail and the only one", func(t *testing.T) {
		t.Parallel()
		f, plan, prc := ramp(t)
		if _, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, products.PhaseInput{Code: "b", BillingIntervalCycleCount: ptr(1)}); err != nil {
			t.Fatalf("CreatePhase() error = %v", err)
		}
		del := func(code string) error {
			_, err := f.s.DeletePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode(code))
			return err
		}
		if err := del("intro"); err != nil {
			t.Fatalf("DeletePhase() error = %v", err)
		}
		if got := list(t, f, plan, prc); len(got) != 2 || got[0] != "b:1" || got[1] != "standard:2" {
			t.Errorf("phases = %v, want b standard", got)
		}
		wantFieldError(t, del("standard"), "rate_phase", catalog.CodeIndefinitePhaseNotDeletable)
		if err := del("b"); err != nil {
			t.Fatalf("DeletePhase() error = %v", err)
		}
		wantFieldError(t, del("standard"), "rate_phase", catalog.CodeIndefinitePhaseNotDeletable)
	})
	// source: spec/services/rate_overrides/create_service_spec.rb
	t.Run("checks overrides against the card", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		unit := f.pricingUnit(t, "credits")
		c := f.card(t, "advance", func(in *products.RateCardInput) {
			in.BillingTiming = catalog.BillingTimingAdvance
			in.PricingUnit = refs.ByID(unit.ID)
		})
		plan := f.attach(t, c)
		create := func(o products.RateOverrideInput) error {
			_, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), refs.ByCode(c.Code), products.PhaseInput{Code: "x", Position: ptr(1), BillingIntervalCycleCount: ptr(1), Override: &o})
			return err
		}
		wantFieldError(t, create(products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"1"}`)}), "rate_override.pricing_unit_conversion_rate", core.CodeRequired)
		rate := ptr(money.MustParse("0.1"))
		wantFieldError(t, create(products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"1"}`), MinAmount: ptr[int64](100), PricingUnitConversionRate: rate}), "rate_override.min_amount", catalog.CodeNotAllowedForBillingTiming)
		if err := create(products.RateOverrideInput{RateModel: catalog.ChargeModelStandard, Properties: props(t, `{"amount":"1"}`), PricingUnitConversionRate: rate}); err != nil {
			t.Errorf("CreatePhase() error = %v, want the override accepted", err)
		}
	})
}
