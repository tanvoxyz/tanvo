package products_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// TestContractLocks checks the catalog edits a contract forbids: the
// rate cards and phases of a plan a contract uses, the values of a filter
// and the active rates of a card a contract bills through.
func TestContractLocks(t *testing.T) {
	t.Parallel()
	// contracted applies a card on a plan with phases intro (2 cycles)
	// then standard (indefinite) and signs a contract on the plan.
	contracted := func(t *testing.T) (*fixture, *catalog.RateCard, refs.Ref, refs.Ref) {
		t.Helper()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		plan := f.attach(t, f.card(t, "api-base", func(in *products.RateCardInput) {
			in.Filter = refs.ByID(f.filter(t, "eu", "eu").ID)
		}))
		phases := []products.PhaseInput{{Code: "intro", Position: ptr(1), BillingIntervalCycleCount: ptr(2)}, {Code: "standard", Position: ptr(2)}}
		if _, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(c.ID), Phases: &phases}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		f.h.Contract(f.h.Customer(), plan)
		return f, c, refs.ByID(plan.ID), refs.ByCode(c.Code)
	}
	// source: spec/services/plan_rate_cards/create_service_spec.rb:144
	// source: spec/services/plan_rate_cards/update_service_spec.rb:22
	// source: spec/services/plan_rate_cards/destroy_service_spec.rb:31
	t.Run("locks the rate cards of a plan with contracts", func(t *testing.T) {
		t.Parallel()
		f, _, plan, prc := contracted(t)
		_, err := f.s.CreatePlanRateCard(t.Context(), f.h.OrgID(), plan, products.PlanRateCardInput{RateCard: refs.ByID(f.card(t, "api-new").ID)})
		wantFieldError(t, err, "plan", catalog.CodePlanLocked)
		_, err = f.s.UpdatePlanRateCard(t.Context(), f.h.OrgID(), plan, prc, products.PlanRateCardUpdate{Units: core.Some[*money.Decimal](nil)})
		wantFieldError(t, err, "plan", catalog.CodePlanLocked)
		_, err = f.s.DeletePlanRateCard(t.Context(), f.h.OrgID(), plan, prc)
		wantFieldError(t, err, "plan", catalog.CodePlanLocked)
		if _, err := f.s.PlanRateCard(t.Context(), f.h.OrgID(), plan, prc); err != nil {
			t.Errorf("PlanRateCard() error = %v, want the card kept", err)
		}
	})
	// source: spec/services/rate_phases/create_service_spec.rb:131
	// source: spec/services/rate_phases/update_service_spec.rb:259
	// source: spec/services/rate_phases/destroy_service_spec.rb:84
	t.Run("locks the phases of a plan with contracts", func(t *testing.T) {
		t.Parallel()
		f, _, plan, prc := contracted(t)
		_, err := f.s.CreatePhase(t.Context(), f.h.OrgID(), plan, prc, products.PhaseInput{Code: "trial", Position: ptr(1), BillingIntervalCycleCount: ptr(1)})
		wantFieldError(t, err, "rate_phase", catalog.CodePlanLocked)
		_, err = f.s.UpdatePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode("intro"), products.PhaseUpdate{Name: core.Some(ptr("Intro"))})
		wantFieldError(t, err, "rate_phase", catalog.CodePlanLocked)
		_, err = f.s.DeletePhase(t.Context(), f.h.OrgID(), plan, prc, refs.ByCode("intro"))
		wantFieldError(t, err, "rate_phase", catalog.CodePlanLocked)
	})
	// source: spec/services/product_filters/update_service_spec.rb:111
	// source: spec/services/product_filters/update_service_spec.rb:116
	// source: spec/services/product_filters/update_service_spec.rb:136
	t.Run("locks the values of a filter a contract card prices", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "us", "us")
		c := f.card(t, "api-us", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) })
		f.h.ContractRateCard(f.h.Contract(f.h.Customer(), nil), c)
		update := func(in products.FilterUpdate) error {
			_, err := f.s.UpdateFilter(t.Context(), f.h.OrgID(), f.product.ID, pf.ID, in)
			return err
		}
		eu := []products.FilterValueInput{{Key: "region", Value: ptr("eu")}}
		wantFieldError(t, update(products.FilterUpdate{Values: core.Some(eu)}), "values", catalog.CodeAttachedToSubscriptions)
		same := []products.FilterValueInput{{Key: "region", Value: ptr("us")}}
		if err := update(products.FilterUpdate{Name: core.Some("renamed"), Values: core.Some(same)}); err != nil {
			t.Errorf("UpdateFilter(current values) error = %v, want nil", err)
		}
		mixed := []products.FilterValueInput{{Key: "region"}, {Key: "region", Value: ptr("eu")}}
		wantFieldError(t, update(products.FilterUpdate{Values: core.Some(mixed)}), "values", catalog.CodeAttachedToSubscriptions)
	})
	// source: spec/services/rate_card_rates/update_service_spec.rb:171
	// source: spec/services/rate_card_rates/update_service_spec.rb:182
	t.Run("locks the active rates of a card a contract applies", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		active, err := f.s.CreateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), standard(t, "now", now))
		if err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		pending, err := f.s.CreateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), standard(t, "later", now.AddDate(0, 1, 0)))
		if err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		f.h.ContractRateCard(f.h.Contract(f.h.Customer(), nil), c)
		in := products.RateUpdate{Properties: core.Some(props(t, `{"amount":"25"}`))}
		got, err := f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(pending.ID), in)
		if err != nil || !got.Properties.Equal(props(t, `{"amount":"25"}`)) {
			t.Errorf("UpdateRate(pending) = %v, %v, want amount 25", got, err)
		}
		_, err = f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(active.ID), in)
		wantFieldError(t, err, "rate_card", catalog.CodeAttachedToSubscriptions)
	})
	// A card a plan applies is attached once a contract uses the plan.
	t.Run("locks the active rates of a card on a contracted plan", func(t *testing.T) {
		t.Parallel()
		f, c, _, _ := contracted(t)
		active, err := f.s.CreateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), standard(t, "now", now))
		if err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		_, err = f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(active.ID), products.RateUpdate{Properties: core.Some(props(t, `{"amount":"25"}`))})
		wantFieldError(t, err, "rate_card", catalog.CodeAttachedToSubscriptions)
	})
}
