package taxes_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/taxes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *taxes.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := taxes.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

// appliedTax creates a tax applied to the default billing entity (lago
// factory trait :applied_to_billing_entity).
func appliedTax(h *billingtest.Harness, code string) *catalog.Tax {
	t := h.Tax(code, "20")
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = append(be.TaxIDs, t.ID) })
	return t
}

func draft(h *billingtest.Harness, c *customer.Customer) *invoice.Invoice {
	return h.StoredInvoice(c, func(i *invoice.Invoice) { i.Status = invoice.StatusDraft })
}

func refreshRequested(h *billingtest.Harness, id core.InvoiceID) bool {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return r.Invoices.Get(ctx, h.OrgID(), id)
	}).RefreshRequested
}

func entity(h *billingtest.Harness, id core.BillingEntityID) *organization.BillingEntity {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*organization.BillingEntity, error) {
		return r.BillingEntities.Get(ctx, h.OrgID(), id)
	})
}

// otherEntity creates a second, non-default billing entity.
func otherEntity(h *billingtest.Harness) *organization.BillingEntity {
	be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", now)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	return be
}

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

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != taxes.Resource {
		t.Fatalf("error = %v, want tax_not_found", err)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/taxes/create_service_spec.rb:21
	// source: spec/services/taxes/create_service_spec.rb:30
	t.Run("creates a tax not applied to the organization", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		got, err := s.Create(t.Context(), h.OrgID(), taxes.CreateInput{
			Code: "tax_code", Name: "Tax", Rate: ptr(money.MustParse("15.0")), Description: ptr("Tax Description"),
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		stored, err := s.Get(t.Context(), h.OrgID(), got.ID)
		if err != nil || stored.Code != "tax_code" || stored.Rate.Cmp(money.MustParse("15")) != 0 || *stored.Description != "Tax Description" {
			t.Fatalf("stored = %+v, %v, want tax_code at 15", stored, err)
		}
		if ids := entity(h, h.BillingEntity.ID).TaxIDs; len(ids) != 0 {
			t.Errorf("default entity taxes = %v, want none", ids)
		}
	})
	// source: spec/services/taxes/create_service_spec.rb:45
	// source: spec/services/taxes/create_service_spec.rb:54
	t.Run("applied to the organization, attaches it to the default entity only", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		other := otherEntity(h)
		d := draft(h, h.Customer())
		got, err := s.Create(t.Context(), h.OrgID(), taxes.CreateInput{
			Code: "tax_code", Name: "Tax", Rate: ptr(money.MustParse("15")), AppliedToOrganization: true,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if ids := entity(h, h.BillingEntity.ID).TaxIDs; !slices.Equal(ids, []core.TaxID{got.ID}) {
			t.Errorf("default entity taxes = %v, want = [%s]", ids, got.ID)
		}
		if ids := entity(h, other.ID).TaxIDs; len(ids) != 0 {
			t.Errorf("other entity taxes = %v, want none", ids)
		}
		if !refreshRequested(h, d.ID) {
			t.Errorf("draft refresh requested = false, want = true")
		}
		applied, err := s.AppliedToOrganization(t.Context(), got)
		if err != nil || !applied {
			t.Errorf("AppliedToOrganization() = %v, %v, want = true", applied, err)
		}
	})
	// source: spec/services/taxes/create_service_spec.rb:64
	t.Run("a used code is value_already_exist", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		h.Tax("tax_code", "10")
		_, err := s.Create(t.Context(), h.OrgID(), taxes.CreateInput{Code: "tax_code", Name: "Tax", Rate: ptr(money.MustParse("15"))})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
	t.Run("a missing rate is value_is_mandatory", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Create(t.Context(), h.OrgID(), taxes.CreateInput{Code: "tax_code", Name: "Tax"})
		wantFieldError(t, err, "rate", core.CodeRequired)
	})
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	// source: spec/services/taxes/update_service_spec.rb:25
	// source: spec/services/taxes/update_service_spec.rb:110
	t.Run("updates the tax and flags the drafts of its customers", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := appliedTax(h, "vat")
		d := draft(h, h.Customer())
		got, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{
			Code: core.Some("updated code"), Rate: core.Some(money.MustParse("15.0")), Description: core.Some(ptr("updated desc")),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Code != "updated code" || got.Rate.Cmp(money.MustParse("15")) != 0 || *got.Description != "updated desc" || got.Name != "vat" {
			t.Errorf("tax = %+v, want the updated fields", got)
		}
		if !refreshRequested(h, d.ID) {
			t.Errorf("draft refresh requested = false, want = true")
		}
	})
	// source: spec/services/taxes/update_service_spec.rb:47
	// source: spec/services/taxes/update_service_spec.rb:53
	// source: spec/services/taxes/update_service_spec.rb:63
	t.Run("applied_to_organization false detaches it from the default entity", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := appliedTax(h, "vat")
		other := otherEntity(h)
		other.TaxIDs = []core.TaxID{x.ID}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Update(ctx, other) })
		d := draft(h, h.Customer())
		if _, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{AppliedToOrganization: core.Some(false)}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if ids := entity(h, h.BillingEntity.ID).TaxIDs; len(ids) != 0 {
			t.Errorf("default entity taxes = %v, want none", ids)
		}
		if ids := entity(h, other.ID).TaxIDs; !slices.Equal(ids, []core.TaxID{x.ID}) {
			t.Errorf("other entity taxes = %v, want = [%s]", ids, x.ID)
		}
		if !refreshRequested(h, d.ID) {
			t.Errorf("draft refresh requested = false, want = true")
		}
	})
	// source: spec/services/taxes/update_service_spec.rb:76
	// source: spec/services/taxes/update_service_spec.rb:82
	// source: spec/services/taxes/update_service_spec.rb:92
	// source: spec/services/taxes/update_service_spec.rb:102
	t.Run("applied_to_organization true attaches it to the default entity once", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := h.Tax("vat", "20")
		other := otherEntity(h)
		d := draft(h, h.Customer())
		for range 2 {
			if _, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{AppliedToOrganization: core.Some(true)}); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
		}
		if ids := entity(h, h.BillingEntity.ID).TaxIDs; !slices.Equal(ids, []core.TaxID{x.ID}) {
			t.Errorf("default entity taxes = %v, want = [%s]", ids, x.ID)
		}
		if ids := entity(h, other.ID).TaxIDs; len(ids) != 0 {
			t.Errorf("other entity taxes = %v, want none", ids)
		}
		if !refreshRequested(h, d.ID) {
			t.Errorf("draft refresh requested = false, want = true")
		}
	})
	t.Run("leaves the drafts of the customers the tax does not apply to", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := appliedTax(h, "vat")
		own := h.Tax("own", "5")
		applying := draft(h, h.Customer(func(c *customer.Customer) { c.TaxIDs = []core.TaxID{x.ID} }))
		other := draft(h, h.Customer(func(c *customer.Customer) { c.TaxIDs = []core.TaxID{own.ID} }))
		if _, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{Rate: core.Some(money.MustParse("21"))}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if !refreshRequested(h, applying.ID) {
			t.Errorf("draft of a customer with the tax: refresh requested = false, want = true")
		}
		if refreshRequested(h, other.ID) {
			t.Errorf("draft of a customer with its own taxes: refresh requested = true, want = false")
		}
	})
	// source: spec/services/taxes/update_service_spec.rb:119
	t.Run("a missing tax is tax_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Update(t.Context(), h.OrgID(), core.NewID[core.TaxID](h.IDs), taxes.UpdateInput{})
		wantNotFound(t, err)
	})
	// source: spec/services/taxes/update_service_spec.rb:138
	t.Run("a blank name is value_is_mandatory", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := h.Tax("vat", "20")
		_, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{Name: core.Some(""), Code: core.Some("code")})
		wantFieldError(t, err, "name", core.CodeRequired)
	})
	t.Run("a code of another tax is value_already_exist", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := h.Tax("vat", "20")
		h.Tax("gst", "10")
		_, err := s.Update(t.Context(), h.OrgID(), x.ID, taxes.UpdateInput{Code: core.Some("gst")})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
	})
}

func TestDelete(t *testing.T) {
	t.Parallel()
	// source: spec/services/taxes/destroy_service_spec.rb:16
	// source: spec/services/taxes/destroy_service_spec.rb:20
	// source: spec/services/taxes/destroy_service_spec.rb:26
	t.Run("deletes the tax, detaches it and flags the drafts", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := appliedTax(h, "vat")
		kept := appliedTax(h, "kept")
		d := draft(h, h.Customer())
		got, err := s.Delete(t.Context(), h.OrgID(), x.ID)
		if err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if got.ID != x.ID {
			t.Errorf("deleted = %s, want = %s", got.ID, x.ID)
		}
		_, err = s.Get(t.Context(), h.OrgID(), x.ID)
		wantNotFound(t, err)
		if ids := entity(h, h.BillingEntity.ID).TaxIDs; !slices.Equal(ids, []core.TaxID{kept.ID}) {
			t.Errorf("default entity taxes = %v, want = [%s]", ids, kept.ID)
		}
		if !refreshRequested(h, d.ID) {
			t.Errorf("draft refresh requested = false, want = true")
		}
	})
	// source: spec/services/taxes/destroy_service_spec.rb:30
	t.Run("detaches it from customers, plans, charges, fixed charges, add-ons and commitments", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		x := h.Tax("vat", "20")
		withTax := func(ids *[]core.TaxID) { *ids = []core.TaxID{x.ID} }
		c := h.Customer(func(c *customer.Customer) { withTax(&c.TaxIDs) })
		p := h.Plan(period.Monthly, 1000, func(p *catalog.Plan) { withTax(&p.TaxIDs) })
		a := h.AddOn(100, func(a *catalog.AddOn) { withTax(&a.TaxIDs) })
		f := h.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"1"}`, "1", func(f *catalog.FixedCharge) { withTax(&f.TaxIDs) })
		m := h.Commitment(p, 100, func(m *catalog.Commitment) { withTax(&m.TaxIDs) })
		if _, err := s.Delete(t.Context(), h.OrgID(), x.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		h.Do(func(ctx context.Context, r port.Repos) error {
			gotC, err := r.Customers.Get(ctx, h.OrgID(), c.ID)
			if err != nil {
				return err
			}
			gotP, err := r.Plans.Get(ctx, h.OrgID(), p.ID)
			if err != nil {
				return err
			}
			gotA, err := r.AddOns.Get(ctx, h.OrgID(), a.ID)
			if err != nil {
				return err
			}
			gotF, err := r.FixedCharges.Get(ctx, h.OrgID(), f.ID)
			if err != nil {
				return err
			}
			gotM, err := r.Commitments.Get(ctx, h.OrgID(), m.ID)
			if err != nil {
				return err
			}
			for name, ids := range map[string][]core.TaxID{
				"customer": gotC.TaxIDs, "plan": gotP.TaxIDs, "add-on": gotA.TaxIDs, "fixed charge": gotF.TaxIDs, "commitment": gotM.TaxIDs,
			} {
				if len(ids) != 0 {
					t.Errorf("%s taxes = %v, want none", name, ids)
				}
			}
			return nil
		})
	})
	// source: spec/services/taxes/destroy_service_spec.rb:103
	t.Run("a missing tax is tax_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Delete(t.Context(), h.OrgID(), core.NewID[core.TaxID](h.IDs))
		wantNotFound(t, err)
	})
}

func TestList(t *testing.T) {
	t.Parallel()
	// source: app/queries/taxes_query.rb:7-20
	t.Run("lists by name, then newest first, filtered and searched", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		b := h.Tax("b_code", "1", func(x *catalog.Tax) { x.Name = "B" })
		a := h.Tax("a_code", "1", func(x *catalog.Tax) { x.Name = "A"; x.AutoGenerated = true })
		c := h.Tax("c_code", "1", func(x *catalog.Tax) { x.Name = "C" })
		list := func(f catalog.TaxFilter) []core.TaxID {
			p, err := s.List(t.Context(), h.OrgID(), f, core.PageRequest{})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			var out []core.TaxID
			for _, x := range p.Items {
				out = append(out, x.ID)
			}
			return out
		}
		if got := list(catalog.TaxFilter{}); !slices.Equal(got, []core.TaxID{a.ID, b.ID, c.ID}) {
			t.Errorf("List() = %v, want = A, B, C", got)
		}
		if got := list(catalog.TaxFilter{AutoGenerated: ptr(false)}); !slices.Equal(got, []core.TaxID{b.ID, c.ID}) {
			t.Errorf("List(manual) = %v, want = B, C", got)
		}
		if got := list(catalog.TaxFilter{Search: "c_c"}); !slices.Equal(got, []core.TaxID{c.ID}) {
			t.Errorf("List(search) = %v, want = C", got)
		}
	})
}
