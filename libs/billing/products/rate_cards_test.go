package products_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var eur = money.MustCurrency("EUR")

func (f *fixture) pricingUnit(t *testing.T, code string) *catalog.PricingUnit {
	t.Helper()
	u := &catalog.PricingUnit{
		ID: core.NewID[core.PricingUnitID](f.h.IDs), OrganizationID: f.h.OrgID(),
		Code: code, Name: code, ShortName: code, CreatedAt: now, UpdatedAt: now,
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PricingUnits.Create(ctx, u) })
	return u
}

func TestCreateRateCard(t *testing.T) {
	t.Parallel()
	// source: spec/services/rate_cards/create_service_spec.rb:24
	// source: spec/services/rate_cards/create_service_spec.rb:64
	t.Run("creates a card in arrears, displayed, without proration", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, " api-usd ")
		if c.Code != "api-usd" || c.BillingTiming != catalog.BillingTimingArrears || !c.DisplayOnInvoice || c.Proration || c.ProductID != f.product.ID {
			t.Errorf("card = %+v, want the defaults", c)
		}
	})
	// source: spec/services/rate_cards/create_service_spec.rb:118
	// source: spec/services/rate_cards/create_service_spec.rb:134
	t.Run("creates the card with its rates, or nothing with prefixed errors", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd", func(in *products.RateCardInput) {
			in.Rates = []products.RateInput{standard(t, "now", now), standard(t, "later", now.AddDate(0, 1, 0))}
		})
		rates, err := f.s.Rates(t.Context(), f.h.OrgID(), refs.ByID(c.ID))
		if err != nil || len(rates.Rates) != 2 {
			t.Fatalf("Rates() = %+v, %v, want two rates", rates, err)
		}
		bad := standard(t, "", now)
		_, err = f.s.CreateRateCard(t.Context(), f.h.OrgID(), products.RateCardInput{
			Product: refs.ByID(f.product.ID), Code: "api-eur", Name: "EUR", Currency: eur, Rates: []products.RateInput{bad},
		})
		wantFieldError(t, err, "rates.code", core.CodeRequired)
		_, err = f.s.RateCard(t.Context(), f.h.OrgID(), refs.ByCode("api-eur"))
		wantNotFound(t, err, "rate_card")
	})
	// source: spec/services/rate_cards/create_service_spec.rb:149
	// source: spec/services/rate_cards/create_service_spec.rb:159
	t.Run("applies the taxes, a missing one rolling the card back", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		vat := f.h.Tax("vat", "20")
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.Taxes = []refs.Ref{refs.ByCode("vat")} })
		if !slices.Equal(c.TaxIDs, []core.TaxID{vat.ID}) {
			t.Errorf("taxes = %v, want = [vat]", c.TaxIDs)
		}
		_, err := f.s.CreateRateCard(t.Context(), f.h.OrgID(), products.RateCardInput{
			Product: refs.ByID(f.product.ID), Code: "api-eur", Name: "EUR", Currency: eur, Taxes: []refs.Ref{refs.ByCode("nope")},
		})
		wantNotFound(t, err, "tax")
	})
	// source: spec/services/rate_cards/create_service_spec.rb:174
	// source: spec/services/rate_cards/create_service_spec.rb:181
	// source: spec/services/rate_cards/create_service_spec.rb:191
	// source: spec/services/rate_cards/create_service_spec.rb:200
	// source: spec/services/rate_cards/create_service_spec.rb:209
	t.Run("rejects a filter of another product, an unknown pricing unit, a used code and no product", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pf := f.filter(t, "eu", "eu")
		if c := f.card(t, "api-eu", func(in *products.RateCardInput) { in.Filter = refs.ByID(pf.ID) }); c.ProductFilterID != pf.ID {
			t.Errorf("filter = %v, want = %v", c.ProductFilterID, pf.ID)
		}
		other, err := f.s.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: "other", Name: "Other", ProductType: catalog.ProductTypeMetered, Metric: refs.ByID(f.metric.ID)})
		if err != nil {
			t.Fatalf("CreateProduct() error = %v", err)
		}
		create := func(in products.RateCardInput) error {
			in.Name, in.Currency = "X", billingtest.USD
			_, err := f.s.CreateRateCard(t.Context(), f.h.OrgID(), in)
			return err
		}
		wantNotFound(t, create(products.RateCardInput{Product: refs.ByID(other.ID), Filter: refs.ByID(pf.ID), Code: "x"}), "product_filter")
		wantFieldError(t, create(products.RateCardInput{Product: refs.ByID(f.product.ID), PricingUnit: refs.ByCode("credits"), Code: "x"}), "pricing_unit_id", core.CodeInvalid)
		wantFieldError(t, create(products.RateCardInput{Product: refs.ByID(f.product.ID), Code: "api-eu"}), "code", core.CodeAlreadyExists)
		wantNotFound(t, create(products.RateCardInput{Product: refs.ByCode("nope"), Code: "x"}), "product")
	})
}

func TestUpdateRateCard(t *testing.T) {
	t.Parallel()
	// source: spec/services/rate_cards/update_service_spec.rb:27
	// source: spec/services/rate_cards/update_service_spec.rb:36
	// source: spec/services/rate_cards/update_service_spec.rb:41
	// source: spec/services/rate_cards/update_service_spec.rb:61
	t.Run("locks the code and currency once a plan applies the card", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.Rates = []products.RateInput{standard(t, "r", now)} })
		got, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Code: core.Some("api-usd-2")})
		if err != nil || got.Code != "api-usd-2" {
			t.Fatalf("UpdateRateCard() = %+v, %v, want the code changed with rates", got, err)
		}
		f.attach(t, got)
		_, err = f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Code: core.Some("api-usd-3")})
		wantFieldError(t, err, "code", catalog.CodeAttachedToPlanOrSubscription)
		if _, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Code: core.Some("api-usd-2"), Currency: core.Some(billingtest.USD), Name: core.Some("N")}); err != nil {
			t.Errorf("UpdateRateCard() error = %v, want unchanged fields accepted", err)
		}
	})
	// source: spec/services/rate_cards/update_service_spec.rb:94
	// source: spec/services/rate_cards/update_service_spec.rb:115
	// source: spec/services/rate_cards/update_service_spec.rb:124
	// source: spec/services/rate_cards/update_service_spec.rb:133
	// source: spec/services/rate_cards/update_service_spec.rb:142
	// source: spec/services/rate_cards/update_service_spec.rb:187
	// source: spec/services/rate_cards/update_service_spec.rb:196
	t.Run("locks the billing fields once the card has rates", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		bare := f.card(t, "bare")
		if got, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), bare.ID, products.RateCardUpdate{Currency: core.Some(eur)}); err != nil || got.Currency != eur {
			t.Errorf("UpdateRateCard() = %+v, %v, want the currency changed", got, err)
		}
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.Rates = []products.RateInput{standard(t, "r", now)} })
		for field, in := range map[string]products.RateCardUpdate{
			"currency":           {Currency: core.Some(eur)},
			"billing_timing":     {BillingTiming: core.Some(catalog.BillingTimingAdvance)},
			"proration":          {Proration: core.Some(true)},
			"display_on_invoice": {DisplayOnInvoice: core.Some(false)},
		} {
			_, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, in)
			wantFieldError(t, err, field, catalog.CodeNotEditableWithRates)
		}
		if _, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{
			Currency: core.Some(billingtest.USD), BillingTiming: core.Some(catalog.BillingTimingArrears), Name: core.Some("Renamed"),
		}); err != nil {
			t.Errorf("UpdateRateCard() error = %v, want unchanged billing fields accepted", err)
		}
	})
	// source: spec/services/rate_cards/update_service_spec.rb:210
	// source: spec/services/rate_cards/update_service_spec.rb:218
	// source: spec/services/rate_cards/update_service_spec.rb:236
	t.Run("replaces, removes or keeps the taxes", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		vat := f.h.Tax("vat", "20")
		gst := f.h.Tax("gst", "10")
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.Taxes = []refs.Ref{refs.ByID(vat.ID)} })
		got, err := f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Name: core.Some("N")})
		if err != nil || !slices.Equal(got.TaxIDs, []core.TaxID{vat.ID}) {
			t.Errorf("UpdateRateCard() = %v, %v, want the taxes kept", got.TaxIDs, err)
		}
		got, err = f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Taxes: core.Some([]refs.Ref{refs.ByID(gst.ID)})})
		if err != nil || !slices.Equal(got.TaxIDs, []core.TaxID{gst.ID}) {
			t.Errorf("UpdateRateCard() = %v, %v, want = [gst]", got.TaxIDs, err)
		}
		got, err = f.s.UpdateRateCard(t.Context(), f.h.OrgID(), c.ID, products.RateCardUpdate{Taxes: core.Some([]refs.Ref{})})
		if err != nil || len(got.TaxIDs) != 0 {
			t.Errorf("UpdateRateCard() = %v, %v, want no taxes", got.TaxIDs, err)
		}
	})
	// source: spec/services/rate_cards/destroy_service_spec.rb:15
	// source: spec/services/rate_cards/destroy_service_spec.rb:40
	t.Run("deletes a card unless a plan applies it", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		f.attach(t, c)
		_, err := f.s.DeleteRateCard(t.Context(), f.h.OrgID(), c.ID)
		wantFieldError(t, err, "rate_card", catalog.CodeAttachedToPlanOrSubscription)
		free := f.card(t, "free", func(in *products.RateCardInput) { in.Rates = []products.RateInput{standard(t, "r", now)} })
		if _, err := f.s.DeleteRateCard(t.Context(), f.h.OrgID(), free.ID); err != nil {
			t.Fatalf("DeleteRateCard() error = %v", err)
		}
		_, err = f.s.Rates(t.Context(), f.h.OrgID(), refs.ByID(free.ID))
		wantNotFound(t, err, "rate_card")
	})
}

func TestRates(t *testing.T) {
	t.Parallel()
	create := func(t *testing.T, f *fixture, c *catalog.RateCard, in products.RateInput) (*catalog.Rate, error) {
		t.Helper()
		return f.s.CreateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), in)
	}
	// source: spec/services/rate_card_rates/create_service_spec.rb:22
	// source: spec/services/rate_card_rates/create_service_spec.rb:79
	// source: spec/services/rate_card_rates/create_service_spec.rb:146
	t.Run("appends rates, the latest effective one active", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		first, err := create(t, f, c, standard(t, "first", now))
		if err != nil || first.BillingIntervalCount != 1 || first.MinAmount != 0 {
			t.Fatalf("CreateRate() = %+v, %v, want the defaults", first, err)
		}
		next, err := create(t, f, c, standard(t, "next", now.AddDate(0, 0, 2)))
		if err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		rates, err := f.s.Rates(t.Context(), f.h.OrgID(), refs.ByID(c.ID))
		if err != nil || rates.Status(first) != catalog.RateStatusActive || rates.Status(next) != catalog.RateStatusPending {
			t.Fatalf("statuses = %v %v, %v, want active then pending", rates.Status(first), rates.Status(next), err)
		}
		f.h.Clock.Set(now.AddDate(0, 0, 3))
		rates, _ = f.s.Rates(t.Context(), f.h.OrgID(), refs.ByID(c.ID))
		if rates.Status(first) != catalog.RateStatusTerminated || rates.Status(next) != catalog.RateStatusActive {
			t.Errorf("statuses = %v %v, want terminated then active", rates.Status(first), rates.Status(next))
		}
	})
	// source: spec/services/rate_card_rates/create_service_spec.rb:44
	// source: spec/services/rate_card_rates/create_service_spec.rb:55
	t.Run("stores tiers starting where the previous one ends", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		in := standard(t, "tiers", now)
		in.RateModel = catalog.ChargeModelGraduated
		in.Properties = props(t, `{"graduated_ranges":[{"to_value":"10","flat_amount":"0","per_unit_amount":"1"},{"to_value":null,"flat_amount":"0","per_unit_amount":"0.5"}]}`)
		r, err := create(t, f, c, in)
		if err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		if b := bounds(t, r.Properties, "graduated_ranges"); len(b) != 2 || b[1] != [2]any{10.0, nil} {
			t.Errorf("bounds = %v, want [[0 10] [10 <nil>]]", b)
		}
		in.Code = "lower"
		in.Properties = props(t, `{"graduated_ranges":[{"from_value":0,"to_value":null,"flat_amount":"0","per_unit_amount":"1"}]}`)
		_, err = create(t, f, c, in)
		wantFieldError(t, err, "properties", catalog.CodeFromValueNotAllowed)
	})
	// source: spec/services/rate_card_rates/create_service_spec.rb:65
	// source: spec/services/rate_card_rates/create_service_spec.rb:197
	// source: spec/services/rate_card_rates/create_service_spec.rb:212
	// source: spec/services/rate_card_rates/create_service_spec.rb:234
	// source: spec/services/rate_card_rates/create_service_spec.rb:243
	t.Run("rejects no code, a past instant, an instant before the active rate and bad properties", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		if _, err := create(t, f, c, standard(t, "active", now)); err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		_, err := create(t, f, c, standard(t, "", now.AddDate(0, 0, 1)))
		wantFieldError(t, err, "code", core.CodeRequired)
		_, err = create(t, f, c, standard(t, "past", now.AddDate(0, 0, -1)))
		wantFieldError(t, err, "effective_from", catalog.CodeMustNotBeBeforeToday)
		bad := standard(t, "bad", now.AddDate(0, 0, 1))
		bad.Properties = props(t, `{"amount":"-1"}`)
		_, err = create(t, f, c, bad)
		wantFieldError(t, err, "properties", "invalid_amount")
		unit := f.pricingUnit(t, "credits")
		pc := f.card(t, "api-credits", func(in *products.RateCardInput) { in.PricingUnit = refs.ByID(unit.ID) })
		_, err = create(t, f, pc, standard(t, "no-rate", now))
		wantFieldError(t, err, "pricing_unit_conversion_rate", core.CodeRequired)
		advance := f.card(t, "api-advance", func(in *products.RateCardInput) { in.BillingTiming = catalog.BillingTimingAdvance })
		if _, err := create(t, f, advance, standard(t, "early", now.Add(-time.Hour))); err != nil {
			t.Fatalf("CreateRate() error = %v", err)
		}
		_, err = create(t, f, advance, standard(t, "earlier", now.Add(-2*time.Hour)))
		wantFieldError(t, err, "effective_from", catalog.CodeMustBeAfterActiveRate)
	})
	// source: spec/services/rate_card_rates/create_service_spec.rb:85
	// source: spec/services/rate_card_rates/create_service_spec.rb:99
	// source: spec/services/rate_card_rates/create_service_spec.rb:111
	t.Run("puts arrears rates at midnight and keeps the instant of advance ones", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		arrears := f.card(t, "arrears")
		at := now.AddDate(0, 0, 1).Add(3 * time.Hour)
		r, err := create(t, f, arrears, standard(t, "a", at))
		if err != nil || !r.EffectiveFrom.Equal(time.Date(2024, 3, 16, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("CreateRate() = %v, %v, want midnight", r.EffectiveFrom, err)
		}
		_, err = create(t, f, arrears, standard(t, "b", at.Add(time.Hour)))
		wantFieldError(t, err, "effective_from", core.CodeAlreadyExists)
		advance := f.card(t, "advance", func(in *products.RateCardInput) { in.BillingTiming = catalog.BillingTimingAdvance })
		r, err = create(t, f, advance, standard(t, "a", at))
		if err != nil || !r.EffectiveFrom.Equal(at) {
			t.Errorf("CreateRate() = %v, %v, want = %v", r.EffectiveFrom, err, at)
		}
	})
	// source: spec/services/rate_card_rates/create_service_spec.rb:159
	// source: spec/services/rate_card_rates/create_service_spec.rb:165
	// source: spec/services/rate_card_rates/create_service_spec.rb:180
	t.Run("reads today in the organization's time zone", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.h.Clock.Set(time.Date(2024, 3, 15, 2, 0, 0, 0, time.UTC))
		ny, _ := time.LoadLocation("America/New_York")
		f.h.UpdateBillingEntity(func(b *organization.BillingEntity) { b.Timezone = ny })
		c := f.card(t, "api-usd", func(in *products.RateCardInput) { in.BillingTiming = catalog.BillingTimingAdvance })
		if _, err := create(t, f, c, standard(t, "today", time.Date(2024, 3, 14, 12, 0, 0, 0, ny))); err != nil {
			t.Errorf("CreateRate() error = %v, want New York's today accepted", err)
		}
		_, err := create(t, f, c, standard(t, "yesterday", time.Date(2024, 3, 13, 23, 0, 0, 0, ny)))
		wantFieldError(t, err, "effective_from", catalog.CodeMustNotBeBeforeToday)
	})
	// source: spec/services/rate_card_rates/update_service_spec.rb:18
	// source: spec/services/rate_card_rates/update_service_spec.rb:90
	// source: spec/services/rate_card_rates/update_service_spec.rb:99
	// source: spec/services/rate_card_rates/update_service_spec.rb:118
	t.Run("edits pending rates fully, active ones' prices only, terminated ones never", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		old, _ := create(t, f, c, standard(t, "old", now))
		f.h.Clock.Set(now.AddDate(0, 0, 1))
		active, _ := create(t, f, c, standard(t, "active", now.AddDate(0, 0, 1)))
		pending, _ := create(t, f, c, standard(t, "pending", now.AddDate(0, 0, 5)))
		update := func(r *catalog.Rate, in products.RateUpdate) (*catalog.Rate, error) {
			return f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByCode(r.Code), in)
		}
		got, err := update(pending, products.RateUpdate{EffectiveFrom: core.Some(now.AddDate(0, 0, 6)), MinAmount: core.Some[int64](100)})
		if err != nil || got.MinAmount != 100 {
			t.Fatalf("UpdateRate() = %+v, %v, want the pending rate changed", got, err)
		}
		if _, err := update(active, products.RateUpdate{Properties: core.Some(props(t, `{"amount":"2"}`))}); err != nil {
			t.Errorf("UpdateRate() error = %v, want the active rate's prices changed", err)
		}
		_, err = update(active, products.RateUpdate{MinAmount: core.Some[int64](0)})
		wantFieldError(t, err, "min_amount", catalog.CodeNotEditableOnActiveRate)
		_, err = update(old, products.RateUpdate{Properties: core.Some(props(t, `{"amount":"2"}`))})
		wantFieldError(t, err, "status", catalog.CodeTerminatedRateNotEditable)
	})
	// source: spec/services/rate_card_rates/update_service_spec.rb:147
	t.Run("locks codes once attached", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		active, _ := create(t, f, c, standard(t, "active", now))
		pending, _ := create(t, f, c, standard(t, "pending", now.AddDate(0, 0, 5)))
		f.attach(t, c)
		_, err := f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(pending.ID), products.RateUpdate{Code: core.Some("renamed")})
		wantFieldError(t, err, "code", catalog.CodeAttachedToPlanOrSubscription)
		if _, err := f.s.UpdateRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(active.ID), products.RateUpdate{Properties: core.Some(props(t, `{"amount":"2"}`))}); err != nil {
			t.Errorf("UpdateRate() error = %v, want the active rate changed", err)
		}
	})
	// source: spec/services/rate_card_rates/destroy_service_spec.rb:16
	// source: spec/services/rate_card_rates/destroy_service_spec.rb:32
	// source: spec/services/rate_card_rates/destroy_service_spec.rb:41
	t.Run("deletes pending rates only", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		c := f.card(t, "api-usd")
		active, _ := create(t, f, c, standard(t, "active", now))
		pending, _ := create(t, f, c, standard(t, "pending", now.AddDate(0, 0, 5)))
		_, err := f.s.DeleteRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(active.ID))
		wantFieldError(t, err, "status", catalog.CodeOnlyPendingRatesCanBeDeleted)
		if _, err := f.s.DeleteRate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(pending.ID)); err != nil {
			t.Fatalf("DeleteRate() error = %v", err)
		}
		_, _, err = f.s.Rate(t.Context(), f.h.OrgID(), refs.ByID(c.ID), refs.ByID(pending.ID))
		wantNotFound(t, err, "rate_card_rate")
	})
}
