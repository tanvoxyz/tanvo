package customers_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

var june22 = time.Date(2022, 6, 22, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *customers.Service) {
	t.Helper()
	h := billingtest.New(t, june22)
	svc, err := customers.New(h.Deps, customers.Relations{
		Lifecycle: lifecycle.New(h.Deps),
		Coupons:   deduct.New(h.Deps),
		Wallets:   wallets.New(h.Deps),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, svc
}

func fieldCode(err error, field string) string {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for _, fe := range ve.Errors {
		if fe.Field == field {
			return fe.Code
		}
	}
	return ""
}

func date(s string) core.Date {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return core.DateOf(d, time.UTC)
}

func draft(h *billingtest.Harness, c *customer.Customer, issuing, expected string, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	return h.StoredInvoice(c, append([]func(*invoice.Invoice){func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusDraft
		inv.IssuingDate = date(issuing)
		inv.ExpectedFinalizationDate = date(expected)
		inv.PaymentDueDate = date(issuing)
	}}, opts...)...)
}

func reloadInvoice(h *billingtest.Harness, id core.InvoiceID) *invoice.Invoice {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return r.Invoices.Get(ctx, h.OrgID(), id)
	})
}

func jobs(h *billingtest.Harness, kind string) []port.Job {
	return slices.DeleteFunc(h.Store.Jobs(), func(j port.Job) bool { return j.Kind != kind })
}

func TestCreate(t *testing.T) {
	t.Run("creates the customer with its attributes", func(t *testing.T) {
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:79")
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:98")
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:239")
		h, svc := setup(t)
		c, err := svc.Create(h.Ctx, h.OrgID(), " cus_1 ", customers.Input{
			Name:  core.Some(new("Foo\x00 Bar")),
			Email: core.Some(new("foo\u200b@example.com")),
			BillingAddress: customers.Address{
				Country: core.Some("fr"), City: core.Some(""),
			},
			Metadata: core.Some(core.Metadata{{Key: "k", Value: "v"}}),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if c.ExternalID != "cus_1" || *c.Name != "Foo Bar" || *c.Email != "foo@example.com" {
			t.Errorf("got = %q %q %q, want = cus_1, Foo Bar, foo@example.com", c.ExternalID, *c.Name, *c.Email)
		}
		if c.BillingAddress.Country != "FR" || c.BillingEntityID != h.BillingEntity.ID {
			t.Errorf("got = %+v, want = country FR of the default entity", c)
		}
		if want := h.Org.DocumentNumberPrefix + "-001"; c.SequentialID != 1 || c.Slug != want {
			t.Errorf("got = %d %q, want = 1 %s", c.SequentialID, c.Slug, want)
		}
		if !slices.ContainsFunc(h.Emitted.Events(), func(e webhook.Event) bool { return e.Type == webhook.EventTypeCustomerCreated }) {
			t.Errorf("got = no customer.created, want = one")
		}
	})

	tests := []struct {
		name, source, field, code string
		in                        func(h *billingtest.Harness) customers.Input
		external                  string
	}{
		{
			name: "external_id taken", source: "app/services/customers/upsert_from_api_service.rb:173",
			external: "taken", field: "external_id", code: core.CodeAlreadyExists,
			in: func(*billingtest.Harness) customers.Input { return customers.Input{} },
		},
		{
			name: "skip sections with sections", source: "spec/services/customers/manage_invoice_custom_sections_service_spec.rb:45",
			external: "cus_2", field: "invoice_custom_sections", code: customers.CodeSkipSectionsAndSelectedIDs,
			in: func(*billingtest.Harness) customers.Input {
				return customers.Input{SkipInvoiceCustomSections: core.Some(true), InvoiceCustomSections: core.Some([]refs.Ref{refs.ByCode("x")})}
			},
		},
		{
			name: "too many metadata", source: "spec/services/customers/upsert_from_api_service_spec.rb:755",
			external: "cus_3", field: "metadata.items", code: core.CodeTooMany,
			in: func(*billingtest.Harness) customers.Input {
				m := make(core.Metadata, 6)
				for i := range m {
					m[i] = core.MetadataItem{Key: string(rune('a' + i)), Value: "v"}
				}
				return customers.Input{Metadata: core.Some(m)}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := setup(t)
			h.Customer(func(c *customer.Customer) { c.ExternalID = "taken" })
			_, err := svc.Create(h.Ctx, h.OrgID(), tt.external, tt.in(h))
			if got := fieldCode(err, tt.field); got != tt.code {
				t.Errorf("got = %q (%v), want = %q", got, err, tt.code)
			}
		})
	}

	t.Run("archived billing entity", func(t *testing.T) {
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:154")
		h, svc := setup(t)
		be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "old", "Old", h.Now())
		be.ArchivedAt = new(h.Now())
		h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
		for _, ref := range []refs.Ref{refs.ByCode("old"), refs.ByID(be.ID), refs.ByCode("nope")} {
			_, err := svc.Create(h.Ctx, h.OrgID(), "cus", customers.Input{BillingEntity: core.Some(ref)})
			var nf *core.NotFoundError
			if !errors.As(err, &nf) || nf.Resource != customers.ResourceBillingEntity {
				t.Errorf("%s: got = %v, want = billing_entity not found", ref, err)
			}
		}
	})

	t.Run("partner accounts are excluded from dunning", func(t *testing.T) {
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:533")
		h, svc := setup(t)
		c, err := svc.Create(h.Ctx, h.OrgID(), "partner", customers.Input{AccountType: core.Some(customer.AccountTypePartner)})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !c.ExcludeFromDunningCampaign {
			t.Errorf("got = %v, want = excluded", c.ExcludeFromDunningCampaign)
		}
	})
}

func TestUpsert(t *testing.T) {
	t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:602")
	t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:623")
	h, svc := setup(t)
	old := h.Customer(func(c *customer.Customer) { c.ExternalID = "cus" })
	c, created, err := svc.Upsert(h.Ctx, h.OrgID(), "cus", customers.Input{Name: core.Some(new("New"))})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if created || c.ID != old.ID || *c.Name != "New" {
		t.Errorf("got = created %v id %s name %q, want = updated %s New", created, c.ID, *c.Name, old.ID)
	}
	if !slices.ContainsFunc(h.Emitted.Events(), func(e webhook.Event) bool { return e.Type == webhook.EventTypeCustomerUpdated }) {
		t.Errorf("got = no customer.updated, want = one")
	}
	if _, created, err = svc.Upsert(h.Ctx, h.OrgID(), "other", customers.Input{}); err != nil || !created {
		t.Errorf("got = %v %v, want = created", created, err)
	}
}

func TestUpdateFromPortal(t *testing.T) {
	t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:37-65")
	t.Log("source: app/services/customer_portal/customer_update_service.rb:14-74")
	h, svc := setup(t)
	old := h.Customer(func(c *customer.Customer) { c.ExternalID = "cus"; c.Phone = new("1") })
	c, err := svc.UpdateFromPortal(h.Ctx, h.OrgID(), old.ID, customers.Input{
		Name:       core.Some(new("Updated customer name")),
		ExternalID: core.Some("renamed"),
		Phone:      core.Some(new("2")),
	})
	if err != nil {
		t.Fatalf("UpdateFromPortal: %v", err)
	}
	if *c.Name != "Updated customer name" || c.ExternalID != "cus" || *c.Phone != "1" {
		t.Errorf("got = %q %s %q, want = the name changed only", *c.Name, c.ExternalID, *c.Phone)
	}
	if slices.ContainsFunc(h.Emitted.Events(), func(e webhook.Event) bool { return e.Type == webhook.EventTypeCustomerUpdated }) {
		t.Errorf("got = customer.updated, want = no webhook")
	}
}

func TestUpdateEditability(t *testing.T) {
	tests := []struct {
		name, source string
		docs         func(h *billingtest.Harness, c *customer.Customer)
		editable     bool
	}{
		{"editable", "spec/models/customer_spec.rb:721", func(*billingtest.Harness, *customer.Customer) {}, true},
		{"with a subscription", "spec/services/customers/update_service_spec.rb:293", func(h *billingtest.Harness, c *customer.Customer) {
			h.Subscription(c, h.Plan(period.Monthly, 100))
		}, false},
		{"with a wallet", "spec/models/customer_spec.rb:679", func(h *billingtest.Harness, c *customer.Customer) {
			h.Wallet(c, "10")
		}, false},
		{"with a percentage coupon", "app/models/customer.rb:281", func(h *billingtest.Harness, c *customer.Customer) {
			h.ApplyCoupon(h.PercentageCoupon("10"), c)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := setup(t)
			c := h.Customer()
			tt.docs(h, c)
			got, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{
				ExternalID:  core.Some("renamed"),
				AccountType: core.Some(customer.AccountTypePartner),
				Name:        core.Some(new("Updated customer name")),
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			wantID, wantType := c.ExternalID, customer.AccountTypeCustomer
			if tt.editable {
				wantID, wantType = "renamed", customer.AccountTypePartner
			}
			if got.ExternalID != wantID || got.AccountType != wantType || *got.Name != "Updated customer name" {
				t.Errorf("got = %s %s %q, want = %s %s %q", got.ExternalID, got.AccountType, *got.Name, wantID, wantType, "Updated customer name")
			}
			if !tt.editable && got.ExcludeFromDunningCampaign {
				t.Errorf("got = excluded from dunning, want = not (the account type stays customer, update_service_spec.rb:222)")
			}
		})
	}
}

func TestUpdatePaymentDueDate(t *testing.T) {
	tests := []struct {
		name, source string
		customerTerm *int
		term         *int
		wantDue      string
		wantTerm     int
	}{
		{"updates invoice payment_due_date", "spec/services/customers/update_invoice_payment_due_date_service_spec.rb:32", nil, new(30), "2022-07-21", 30},
		{"falls back to the billing entity term", "spec/services/customers/update_invoice_payment_due_date_service_spec.rb:42", new(20), nil, "2022-07-06", 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := setup(t)
			h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.NetPaymentTermDays = 15 })
			c := h.Customer(func(c *customer.Customer) { c.NetPaymentTermDays = tt.customerTerm })
			inv := draft(h, c, "2022-06-21", "2022-06-30")
			if _, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{NetPaymentTermDays: core.Some(tt.term)}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := reloadInvoice(h, inv.ID)
			if got.PaymentDueDate != date(tt.wantDue) || got.NetPaymentTermDays != tt.wantTerm {
				t.Errorf("got = %s %d, want = %s %d", got.PaymentDueDate, got.NetPaymentTermDays, tt.wantDue, tt.wantTerm)
			}
		})
	}
}

func TestUpdateIssuingDateSettings(t *testing.T) {
	t.Run("moves and finalizes drafts", func(t *testing.T) {
		t.Log("source: spec/services/customers/update_invoice_issuing_date_settings_service_spec.rb:50")
		t.Log("source: spec/services/customers/update_invoice_issuing_date_settings_service_spec.rb:62")
		t.Log("source: spec/services/customers/update_invoice_issuing_date_settings_service_spec.rb:76")
		h, svc := setup(t)
		c := h.Customer(func(c *customer.Customer) { c.NetPaymentTermDays = new(3) })
		due := draft(h, c, "2022-06-19", "2022-06-19")
		later := draft(h, c, "2022-06-21", "2022-06-21")
		_, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{
			InvoiceGracePeriodDays: core.Some(new(2)),
			IssuingDateAnchor:      core.Some(core.IssuingDateAnchorNextPeriodStart),
			IssuingDateAdjustment:  core.Some(core.IssuingDateAdjustmentAlignWithFinalizationDate),
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := reloadInvoice(h, later.ID)
		if got.IssuingDate != date("2022-06-23") || got.PaymentDueDate != date("2022-06-26") {
			t.Errorf("got = %s %s, want = 2022-06-23 2022-06-26", got.IssuingDate, got.PaymentDueDate)
		}
		fin := jobs(h, kernel.JobFinalizeDraft)
		if len(fin) != 1 || fin[0].UniqueKey != "invoice:"+due.ID.String() {
			t.Errorf("got = %+v, want = the finalization of %s only", fin, due.ID)
		}
	})

	t.Run("same settings change nothing", func(t *testing.T) {
		t.Log("source: spec/services/customers/update_invoice_issuing_date_settings_service_spec.rb:93")
		t.Log("source: spec/services/customers/update_invoice_issuing_date_settings_service_spec.rb:103")
		h, svc := setup(t)
		c := h.Customer()
		later := draft(h, c, "2022-06-21", "2022-06-21")
		if _, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{InvoiceGracePeriodDays: core.Some[*int](nil)}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := reloadInvoice(h, later.ID); got.IssuingDate != date("2022-06-21") || len(jobs(h, kernel.JobFinalizeDraft)) != 0 {
			t.Errorf("got = %s %v, want = unchanged, no job", got.IssuingDate, h.JobKinds())
		}
	})
}

func TestApplyTaxes(t *testing.T) {
	t.Log("source: spec/services/customers/apply_taxes_service_spec.rb:15")
	t.Log("source: spec/services/customers/apply_taxes_service_spec.rb:19")
	t.Log("source: spec/services/customers/apply_taxes_service_spec.rb:62")
	h, svc := setup(t)
	t1 := h.Tax("tax1", "10")
	c := h.Customer()
	inv := draft(h, c, "2022-06-21", "2022-06-30")
	got, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("tax1"), refs.ByID(t1.ID)})})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Equal(got.TaxIDs, []core.TaxID{t1.ID}) {
		t.Errorf("got = %v, want = [%s]", got.TaxIDs, t1.ID)
	}
	if !reloadInvoice(h, inv.ID).RefreshRequested {
		t.Errorf("got = not flagged, want = draft flagged for refresh")
	}

	t.Run("unknown tax", func(t *testing.T) {
		t.Log("source: spec/services/customers/apply_taxes_service_spec.rb:44")
		_, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("nope")})})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != "tax" {
			t.Errorf("got = %v, want = tax not found", err)
		}
	})
}

func TestEUTaxes(t *testing.T) {
	t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:1406")
	t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:359")
	h, svc := setup(t)
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.EUTaxManagement = true; be.Address.Country = "FR" })
	fr := h.Tax(tax.EUStandardTaxCode("FR"), "20")
	de := h.Tax(tax.EUStandardTaxCode("DE"), "19")
	other := h.Tax("other", "5")
	c, err := svc.Create(h.Ctx, h.OrgID(), "eu", customers.Input{BillingAddress: customers.Address{Country: core.Some("FR")}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !slices.Equal(c.TaxIDs, []core.TaxID{fr.ID}) {
		t.Errorf("got = %v, want = [%s]", c.TaxIDs, fr.ID)
	}
	c, err = svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{
		BillingAddress: customers.Address{Country: core.Some("DE")},
		Taxes:          core.Some([]refs.Ref{refs.ByID(other.ID)}),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Equal(c.TaxIDs, []core.TaxID{other.ID, de.ID}) {
		t.Errorf("got = %v, want = [%s %s]", c.TaxIDs, other.ID, de.ID)
	}
}

func TestSections(t *testing.T) {
	h, svc := setup(t)
	sec := &invoice.CustomSection{ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: "sec", Name: "Sec", CreatedAt: h.Now(), UpdatedAt: h.Now()}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, sec) })
	c := h.Customer(func(c *customer.Customer) { c.SkipInvoiceCustomSections = true })

	t.Log("source: spec/services/customers/manage_invoice_custom_sections_service_spec.rb:106")
	got, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{InvoiceCustomSections: core.Some([]refs.Ref{refs.ByCode("sec"), refs.ByCode("unknown")})})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.SkipInvoiceCustomSections || !slices.Equal(got.InvoiceCustomSectionIDs, []core.InvoiceCustomSectionID{sec.ID}) {
		t.Errorf("got = %v %v, want = not skipped, [%s]", got.SkipInvoiceCustomSections, got.InvoiceCustomSectionIDs, sec.ID)
	}

	t.Log("source: spec/services/customers/manage_invoice_custom_sections_service_spec.rb:188")
	got, err = svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{SkipInvoiceCustomSections: core.Some(true)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.SkipInvoiceCustomSections || len(got.InvoiceCustomSectionIDs) != 0 {
		t.Errorf("got = %v %v, want = skipped, no sections", got.SkipInvoiceCustomSections, got.InvoiceCustomSectionIDs)
	}
}

func TestDunning(t *testing.T) {
	t.Log("source: spec/services/customers/update_service_spec.rb:794")
	t.Log("source: spec/services/customers/update_service_spec.rb:819")
	h, svc := setup(t)
	camp := &dunning.Campaign{ID: core.NewID[core.DunningCampaignID](h.IDs), OrganizationID: h.OrgID(), Code: "camp", Name: "Camp", CreatedAt: h.Now(), UpdatedAt: h.Now()}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.DunningCampaigns.Create(ctx, camp) })
	c := h.Customer(func(c *customer.Customer) {
		c.ExcludeFromDunningCampaign = true
		c.DunningCampaignAttempts = map[string]int{"USD": 2}
	})
	got, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{AppliedDunningCampaign: core.Some(refs.ByCode("camp"))})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.AppliedDunningCampaignID != camp.ID || got.ExcludeFromDunningCampaign || len(got.DunningCampaignAttempts) != 0 {
		t.Errorf("got = %+v, want = campaign applied, included, attempts reset", got)
	}
	got, err = svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{ExcludeFromDunningCampaign: core.Some(true)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.AppliedDunningCampaignID.IsZero() || !got.ExcludeFromDunningCampaign {
		t.Errorf("got = %+v, want = excluded without campaign", got)
	}
}

func TestDelete(t *testing.T) {
	t.Log("source: spec/services/customers/destroy_service_spec.rb:17")
	t.Log("source: spec/services/customers/destroy_service_spec.rb:24")
	h, svc := setup(t)
	c := h.Customer()
	got, err := svc.Delete(h.Ctx, h.OrgID(), c.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !got.Deleted() || !h.ReloadCustomer(c.ID).Deleted() {
		t.Errorf("got = not deleted, want = deleted")
	}
	if j := jobs(h, customers.JobTerminateRelations); len(j) != 1 {
		t.Errorf("got = %v, want = one %s job", h.JobKinds(), customers.JobTerminateRelations)
	}

	t.Log("source: spec/services/customers/destroy_service_spec.rb:40")
	if _, err := svc.Delete(h.Ctx, h.OrgID(), c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got = %v, want = not found", err)
	}
}

func TestTerminateRelations(t *testing.T) {
	t.Log("source: spec/services/customers/terminate_relations_service_spec.rb:15")
	t.Log("source: spec/services/customers/terminate_relations_service_spec.rb:29")
	t.Log("source: spec/services/customers/terminate_relations_service_spec.rb:47")
	t.Log("source: spec/services/customers/terminate_relations_service_spec.rb:59")
	t.Log("source: spec/services/customers/terminate_relations_service_spec.rb:71")
	h, svc := setup(t)
	c := h.Customer()
	plan := h.Plan(period.Monthly, 100)
	active := h.Subscription(c, plan, func(s *subscription.Subscription) { s.OnTerminationInvoice = subscription.TerminationInvoiceSkip })
	pending := h.Subscription(c, plan, billingtest.SubscribedAt(june22.AddDate(0, 1, 0)))
	inv := draft(h, c, "2022-06-21", "2022-06-30")
	ac := h.ApplyCoupon(h.FixedAmountCoupon(100), c)
	w := h.Wallet(c, "10")
	if _, err := svc.Delete(h.Ctx, h.OrgID(), c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	job := jobs(h, customers.JobTerminateRelations)[0]
	if handled, err := svc.HandleJob(h.Ctx, job); !handled || err != nil {
		t.Fatalf("HandleJob: %v %v", handled, err)
	}
	subs := billingtest.Read(h, func(ctx context.Context, r port.Repos) (map[core.SubscriptionID]subscription.Status, error) {
		out := map[core.SubscriptionID]subscription.Status{}
		all, err := r.Subscriptions.ListByCustomer(ctx, h.OrgID(), c.ID)
		for _, s := range all {
			out[s.ID] = s.Status
		}
		return out, err
	})
	if subs[active.ID] != subscription.StatusTerminated || subs[pending.ID] != subscription.StatusCanceled {
		t.Errorf("got = %v, want = active terminated, pending canceled", subs)
	}
	fin := jobs(h, kernel.JobFinalizeDraft)
	if len(fin) != 1 || fin[0].UniqueKey != "invoice:"+inv.ID.String() || !fin[0].RunAt.Equal(june22.Add(5*time.Minute)) {
		t.Errorf("got = %+v, want = the finalization of %s in 5 minutes", fin, inv.ID)
	}
	if got := h.ReloadAppliedCoupon(ac.ID); got.Status != coupon.StatusTerminated {
		t.Errorf("got = %s, want = terminated coupon", got.Status)
	}
	gotW := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*wallet.Wallet, error) {
		return r.Wallets.Get(ctx, h.OrgID(), w.ID)
	})
	if gotW.Status != wallet.StatusTerminated {
		t.Errorf("got = %s, want = terminated wallet", gotW.Status)
	}
}

func TestConnections(t *testing.T) {
	h, svc := setup(t)
	newIntegration := func(code string, cat integration.Category, p integration.Provider) *integration.Integration {
		in := &integration.Integration{ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code, Category: cat, Provider: p, CreatedAt: h.Now(), UpdatedAt: h.Now()}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, in) })
		return in
	}
	stripe := newIntegration("stripe", integration.CategoryPayment, integration.ProviderStripe)
	newIntegration("adyen", integration.CategoryPayment, integration.ProviderAdyen)
	netsuite := newIntegration("netsuite", integration.CategoryAccounting, integration.ProviderNetsuite)
	xero := newIntegration("xero", integration.CategoryAccounting, integration.ProviderXero)
	c := h.Customer()

	t.Log("source: spec/services/payment_provider_customers/create_or_update_batch_service_spec.rb:27")
	got, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe"), customers.ConnectionInput{ExternalCustomerID: core.Some("cus_stripe")})
	if err != nil || got.ExternalCustomerID != "cus_stripe" || got.Integration.ID != stripe.ID {
		t.Fatalf("got = %+v %v, want = stripe connection", got, err)
	}
	if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("adyen"), customers.ConnectionInput{SyncWithProvider: core.Some(true)}); fieldCode(err, "connection") != customers.CodeOnlyOneConnectionAllowed {
		t.Errorf("got = %v, want = only_one_connection_allowed", err)
	}

	t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:117")
	t.Log("source: spec/services/integration_customers/set_as_default_service_spec.rb:26")
	a, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByID(netsuite.ID), customers.ConnectionInput{ExternalCustomerID: core.Some("ns_1")})
	if err != nil || !a.IsDefault {
		t.Fatalf("got = %+v %v, want = default netsuite connection", a, err)
	}
	b, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByID(xero.ID), customers.ConnectionInput{ExternalCustomerID: core.Some("x_1"), IsDefault: core.Some(true)})
	if err != nil || !b.IsDefault {
		t.Fatalf("got = %+v %v, want = default xero connection", b, err)
	}
	list, err := svc.ListConnections(h.Ctx, h.OrgID(), c.ID)
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	var defaults []string
	for _, cn := range list {
		if cn.IsDefault {
			defaults = append(defaults, cn.Integration.Code)
		}
	}
	if !slices.Equal(defaults, []string{"stripe", "xero"}) {
		t.Errorf("got = %v, want = [stripe xero]", defaults)
	}

	if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe")); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if _, err := svc.UpsertConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("adyen"), customers.ConnectionInput{SyncWithProvider: core.Some(true)}); err != nil {
		t.Errorf("got = %v, want = adyen connection once stripe is gone", err)
	}
	if err := svc.DeleteConnection(h.Ctx, h.OrgID(), c.ID, refs.ByCode("stripe")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got = %v, want = not found", err)
	}

	t.Log("source: spec/services/customers/generate_checkout_url_service_spec.rb:46")
	other := h.Customer()
	if _, _, err := svc.CheckoutURL(h.Ctx, h.OrgID(), other.ID, refs.Ref{}); fieldCode(err, "base") != customers.CodeNoLinkedPaymentProvider {
		t.Errorf("got = %v, want = no_linked_payment_provider", err)
	}
	if _, _, err := svc.CheckoutURL(h.Ctx, h.OrgID(), c.ID, refs.Ref{}); !errors.Is(err, customers.ErrCheckoutUnavailable) {
		t.Errorf("got = %v, want = ErrCheckoutUnavailable", err)
	}
}

func TestSequence(t *testing.T) {
	t.Log("source: spec/models/customer_spec.rb:1122")
	t.Log("source: spec/models/customer_spec.rb:1139")
	h, svc := setup(t)
	if _, err := svc.Create(h.Ctx, h.OrgID(), "cus_1", customers.Input{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The sequence is the organization's: it counts deleted customers and
	// those of every billing entity (customer.rb:140-141).
	deleted, err := svc.Create(h.Ctx, h.OrgID(), "cus_2", customers.Input{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Delete(h.Ctx, h.OrgID(), deleted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", h.Now())
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	if _, err := svc.Create(h.Ctx, h.OrgID(), "cus_3", customers.Input{BillingEntity: core.Some(refs.ByID(be.ID))}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The slug takes the organization's prefix, not the billing entity's.
	h.Do(func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, h.OrgID())
		if err != nil {
			return err
		}
		o.DocumentNumberPrefix = "ORG-55"
		return r.Organizations.Update(ctx, o)
	})
	c, err := svc.Create(h.Ctx, h.OrgID(), "cus_4", customers.Input{BillingEntity: core.Some(refs.ByID(be.ID))})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.SequentialID != 4 || c.Slug != "ORG-55-004" {
		t.Errorf("got = %d %q, want = 4 ORG-55-004", c.SequentialID, c.Slug)
	}
}
