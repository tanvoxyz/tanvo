package plans_test

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestContractLock checks that a plan a contract uses can be neither
// destroyed nor recoded nor moved to another currency.
func TestContractLock(t *testing.T) {
	t.Parallel()
	wantLocked := func(t *testing.T, err error) {
		t.Helper()
		ve, ok := errors.AsType[*core.ValidationError](err)
		if !ok || !ve.Has("plan", catalog.CodePlanLocked) {
			t.Fatalf("error = %v, want plan plan_locked", err)
		}
	}
	// source: spec/services/catalog_plans/destroy_service_spec.rb:49
	t.Run("blocks the deletion", func(t *testing.T) {
		t.Parallel()
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		h.Contract(h.Customer(), p)
		_, err := e.Plans.Destroy(h.Ctx, h.OrgID(), p.ID)
		wantLocked(t, err)
		if got := reloadPlan(h, p.ID); got.Status != catalog.PlanStatusActive {
			t.Errorf("plan = %+v, want it kept", got)
		}
	})
	// source: spec/services/catalog_plans/update_service_spec.rb:84
	// source: spec/services/catalog_plans/update_service_spec.rb:99
	// source: spec/services/catalog_plans/update_service_spec.rb:107
	// source: spec/services/catalog_plans/update_service_spec.rb:114
	t.Run("freezes the code and the currency", func(t *testing.T) {
		t.Parallel()
		h, e := setup(t)
		p := h.Plan(period.Monthly, 1000)
		h.Contract(h.Customer(), p)
		_, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Currency: core.Some(money.MustCurrency("EUR"))})
		wantLocked(t, err)
		_, err = e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Code: core.Some(p.Code + "-v2")})
		wantLocked(t, err)
		if got := reloadPlan(h, p.ID); got.Code != p.Code {
			t.Errorf("code = %q, want %q", got.Code, p.Code)
		}
		got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Code: core.Some(p.Code), Name: core.Some("Renamed"), Description: core.Some(new("New"))})
		if err != nil || got.Name != "Renamed" {
			t.Errorf("Update(name) = %+v, %v, want the name changed", got, err)
		}
	})
}

// TestRateCardsCurrencyLock checks that a plan applying rate cards keeps
// its currency, before any contract uses it.
func TestRateCardsCurrencyLock(t *testing.T) {
	t.Parallel()
	h, e := setup(t)
	p := h.Plan(period.Monthly, 1000)
	cat, err := products.New(h.Deps)
	if err != nil {
		t.Fatalf("products.New() error = %v", err)
	}
	prod, err := cat.CreateProduct(h.Ctx, h.OrgID(), products.ProductInput{Code: "seats", Name: "Seats", ProductType: catalog.ProductTypeFixed})
	if err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	rc, err := cat.CreateRateCard(h.Ctx, h.OrgID(), products.RateCardInput{Product: refs.ByID(prod.ID), Code: "seats", Name: "Seats", Currency: billingtest.USD})
	if err != nil {
		t.Fatalf("CreateRateCard() error = %v", err)
	}
	if _, err := cat.CreatePlanRateCard(h.Ctx, h.OrgID(), refs.ByID(p.ID), products.PlanRateCardInput{RateCard: refs.ByID(rc.ID)}); err != nil {
		t.Fatalf("CreatePlanRateCard() error = %v", err)
	}
	// source: spec/services/catalog_plans/update_service_spec.rb:62
	_, err = e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Currency: core.Some(money.MustCurrency("EUR"))})
	if ve, ok := errors.AsType[*core.ValidationError](err); !ok || !ve.Has("currency", catalog.CodeNotEditableWithAppliedRateCards) {
		t.Fatalf("error = %v, want currency not_editable_with_applied_rate_cards", err)
	}
	if got := reloadPlan(h, p.ID); got.Currency != billingtest.USD {
		t.Errorf("currency = %s, want = USD", got.Currency)
	}
	// source: spec/services/catalog_plans/update_service_spec.rb:67
	got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Currency: core.Some(billingtest.USD), Name: core.Some("Renamed")})
	if err != nil || got.Name != "Renamed" {
		t.Errorf("Update(name) = %+v, %v, want the name changed", got, err)
	}
}
