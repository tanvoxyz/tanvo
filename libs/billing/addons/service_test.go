package addons_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/addons"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *addons.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := addons.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

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
		t.Fatalf("error = %v, want %s_not_found", err, resource)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/add_ons/create_service_spec.rb:27
	// source: spec/services/add_ons/apply_taxes_service_spec.rb:15
	// source: spec/services/add_ons/apply_taxes_service_spec.rb:56
	t.Run("creates an add-on with its taxes, each once", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		vat := h.Tax("vat", "20")
		gst := h.Tax("gst", "10")
		a, err := s.Create(t.Context(), h.OrgID(), addons.CreateInput{
			Code: "free-beer-for-us", Name: "Free beer for us", InvoiceDisplayName: ptr("Beer"), Description: ptr("desc"),
			Amount: 100, Currency: billingtest.USD,
			Taxes: []refs.Ref{refs.ByCode("vat"), refs.ByID(gst.ID), refs.ByCode("vat")},
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, err := s.Get(t.Context(), h.OrgID(), a.ID)
		if err != nil || got.Code != "free-beer-for-us" || got.Amount != 100 || got.Currency != billingtest.USD || *got.InvoiceDisplayName != "Beer" {
			t.Fatalf("stored = %+v, %v, want the created add-on", got, err)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{vat.ID, gst.ID}) {
			t.Errorf("taxes = %v, want = [vat gst]", got.TaxIDs)
		}
		taxes, err := s.Taxes(t.Context(), got)
		if err != nil || len(taxes) != 2 || taxes[0].Code != "vat" {
			t.Errorf("Taxes() = %v, %v, want vat and gst", taxes, err)
		}
	})
	// source: spec/services/add_ons/create_service_spec.rb:51
	t.Run("reuses the code of a deleted add-on", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		old := h.AddOn(100, func(a *catalog.AddOn) { a.Code = "free-beer-for-us" })
		if _, err := s.Delete(t.Context(), h.OrgID(), old.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := s.Create(t.Context(), h.OrgID(), addons.CreateInput{
			Code: "free-beer-for-us", Name: "Beer", Amount: 100, Currency: billingtest.USD,
		}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	})
	// source: spec/services/add_ons/create_service_spec.rb:71
	t.Run("a used code is value_already_exist", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		h.AddOn(100, func(a *catalog.AddOn) { a.Code = "free-beer-for-us" })
		_, err := s.Create(t.Context(), h.OrgID(), addons.CreateInput{
			Code: "free-beer-for-us", Name: "Beer", Amount: 100, Currency: billingtest.USD,
		})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	// source: spec/services/add_ons/apply_taxes_service_spec.rb:38
	t.Run("a missing tax is tax_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Create(t.Context(), h.OrgID(), addons.CreateInput{
			Code: "beer", Name: "Beer", Amount: 100, Currency: billingtest.USD, Taxes: []refs.Ref{refs.ByCode("unknown")},
		})
		wantNotFound(t, err, "tax")
	})
	t.Run("validates the amount and the currency", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Create(t.Context(), h.OrgID(), addons.CreateInput{Code: "beer", Name: "Beer"})
		wantFieldError(t, err, "amount", core.CodeNotPositive)
	})
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	// source: spec/services/add_ons/update_service_spec.rb:34
	// source: spec/services/add_ons/apply_taxes_service_spec.rb:47
	t.Run("updates the add-on and replaces its taxes", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		vat := h.Tax("vat", "20")
		gst := h.Tax("gst", "10")
		a := h.AddOn(500, func(a *catalog.AddOn) { a.TaxIDs = []core.TaxID{vat.ID} })
		eur := money.MustCurrency("EUR")
		got, err := s.Update(t.Context(), h.OrgID(), a.ID, addons.UpdateInput{
			Name: core.Some("new name"), InvoiceDisplayName: core.Some(ptr("new invoice name")), Code: core.Some("code"),
			Description: core.Some(ptr("desc")), Amount: core.Some(int64(100)), Currency: core.Some(eur),
			Taxes: core.Some([]refs.Ref{refs.ByCode("gst")}),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Name != "new name" || *got.InvoiceDisplayName != "new invoice name" || *got.Description != "desc" ||
			got.Amount != 100 || got.Currency != eur || got.Code != "code" {
			t.Errorf("add-on = %+v, want the updated fields", got)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{gst.ID}) {
			t.Errorf("taxes = %v, want = [gst]", got.TaxIDs)
		}
	})
	t.Run("keeps the taxes when they are not set", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		vat := h.Tax("vat", "20")
		a := h.AddOn(500, func(a *catalog.AddOn) { a.TaxIDs = []core.TaxID{vat.ID} })
		got, err := s.Update(t.Context(), h.OrgID(), a.ID, addons.UpdateInput{Name: core.Some("new name")})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if !slices.Equal(got.TaxIDs, []core.TaxID{vat.ID}) {
			t.Errorf("taxes = %v, want = [vat]", got.TaxIDs)
		}
	})
	// source: spec/services/add_ons/update_service_spec.rb:49
	t.Run("a missing tax is tax_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		a := h.AddOn(500)
		_, err := s.Update(t.Context(), h.OrgID(), a.ID, addons.UpdateInput{Taxes: core.Some([]refs.Ref{refs.ByCode("unknown")})})
		wantNotFound(t, err, "tax")
	})
	// source: spec/services/add_ons/update_service_spec.rb:68
	t.Run("a blank name is value_is_mandatory", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		a := h.AddOn(500)
		_, err := s.Update(t.Context(), h.OrgID(), a.ID, addons.UpdateInput{Name: core.Some(""), Code: core.Some("code")})
		wantFieldError(t, err, "name", core.CodeRequired)
	})
	t.Run("a missing add-on is add_on_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Update(t.Context(), h.OrgID(), core.NewID[core.AddOnID](h.IDs), addons.UpdateInput{})
		wantNotFound(t, err, addons.Resource)
	})
}

func TestDelete(t *testing.T) {
	t.Parallel()
	// source: spec/services/add_ons/destroy_service_spec.rb:15
	// source: spec/services/add_ons/destroy_service_spec.rb:25
	t.Run("deletes the add-on and its fixed charges", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		a := h.AddOn(500)
		p := h.Plan(period.Monthly, 1000)
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1")
		got, err := s.Delete(t.Context(), h.OrgID(), a.ID)
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if got.ID != a.ID {
			t.Errorf("deleted = %s, want = %s", got.ID, a.ID)
		}
		_, err = s.Get(t.Context(), h.OrgID(), a.ID)
		wantNotFound(t, err, addons.Resource)
		h.Do(func(ctx context.Context, r port.Repos) error {
			got, err := r.FixedCharges.Get(ctx, h.OrgID(), f.ID)
			if err != nil {
				t.Fatalf("fixed charge: error = %v", err)
			}
			if got.DeletedAt == nil {
				t.Errorf("fixed charge deleted_at = nil, want = set")
			}
			return nil
		})
	})
	// source: spec/services/add_ons/destroy_service_spec.rb:49
	t.Run("a missing add-on is add_on_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Delete(t.Context(), h.OrgID(), core.NewID[core.AddOnID](h.IDs))
		wantNotFound(t, err, addons.Resource)
	})
}

func TestList(t *testing.T) {
	t.Parallel()
	// source: app/queries/add_ons_query.rb:6-27
	t.Run("lists most recent first, searched by name or code", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		a := h.AddOn(100, func(a *catalog.AddOn) { a.Code, a.Name = "beer", "Beer" })
		h.Clock.Advance(time.Minute)
		b := h.AddOn(100, func(a *catalog.AddOn) { a.Code, a.Name = "wine", "Wine" })
		list := func(search string) []core.AddOnID {
			p, err := s.List(t.Context(), h.OrgID(), search, core.PageRequest{})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			var out []core.AddOnID
			for _, x := range p.Items {
				out = append(out, x.ID)
			}
			return out
		}
		if got := list(""); !slices.Equal(got, []core.AddOnID{b.ID, a.ID}) {
			t.Errorf("List() = %v, want = wine, beer", got)
		}
		if got := list("BEE"); !slices.Equal(got, []core.AddOnID{a.ID}) {
			t.Errorf("List(BEE) = %v, want = beer", got)
		}
	})
}
