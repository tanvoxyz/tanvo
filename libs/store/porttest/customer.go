package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testCustomer(t *testing.T, h Harness) {
	t.Run("customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		c := customerFixtureFull(t, e, a, "cust-1")
		minimal := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust-2", a.ID, at(time.Second))
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Customers.Create(ctx, c))
			noErr(t, "Create() minimal", r.Customers.Create(ctx, minimal))
			wantErr(t, "Create() with the same ID", r.Customers.Create(ctx, c), core.ErrConflict)
			got, err := r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, c)
			got, err = r.Customers.Get(ctx, org, minimal.ID)
			noErr(t, "Get() minimal", err)
			equal(t, "Get() minimal", got, minimal)
			got, err = r.Customers.GetByExternalID(ctx, org, "cust-1")
			noErr(t, "GetByExternalID()", err)
			equal(t, "GetByExternalID()", got, c)
			_, err = r.Customers.Get(ctx, org, core.NewID[core.CustomerID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Customers.GetByExternalID(ctx, org, "nope")
			wantErr(t, "GetByExternalID() unknown", err, core.ErrNotFound)

			// Isolation: another organization sees nothing.
			_, err = r.Customers.Get(ctx, b.OrganizationID, c.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Customers.GetByExternalID(ctx, b.OrganizationID, "cust-1")
			wantErr(t, "GetByExternalID() from another organization", err, core.ErrNotFound)
			other := *c
			other.OrganizationID = b.OrganizationID
			other.BillingEntityID = b.ID
			wantErr(t, "Update() through another organization", r.Customers.Update(ctx, &other), core.ErrNotFound)
			p, err := r.Customers.List(ctx, b.OrganizationID, customer.Filter{IncludeDeleted: true}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(p.Items), 0)
			// The same external ID is free in another organization.
			noErr(t, "Create() in another organization", r.Customers.Create(ctx,
				customer.New(b.OrganizationID, core.NewID[core.CustomerID](e.ids), "cust-1", b.ID, now)))

			// Uniqueness of the external ID among live customers.
			dup := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust-1", a.ID, now)
			wantErr(t, "Create() with a taken external ID", r.Customers.Create(ctx, dup), core.ErrConflict)
			minimal.ExternalID = "cust-1"
			wantErr(t, "Update() to a taken external ID", r.Customers.Update(ctx, minimal), core.ErrConflict)
			minimal.ExternalID = "cust-2"

			// Update every field, then clear the optional ones.
			c.Name = ptr("Acme Renamed")
			c.CustomerType = customer.TypeIndividual
			c.AccountType = customer.AccountTypePartner
			c.Currency = money.MustCurrency("USD")
			c.Timezone = customerLoad(t, "America/New_York")
			c.InvoiceGracePeriodDays = ptr(0)
			c.DunningCampaignAttempts = map[string]int{"USD": 1}
			c.Metadata = core.Metadata{{Key: "tier", Value: "gold"}}
			c.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, c)

			cleared := customer.New(org, c.ID, "cust-1", a.ID, now)
			cleared.UpdatedAt = at(2 * time.Minute)
			noErr(t, "Update() clearing", r.Customers.Update(ctx, cleared))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() after clearing", err)
			equal(t, "Get() after clearing", got, cleared)
			*c = *cleared

			missing := customer.New(org, core.NewID[core.CustomerID](e.ids), "missing", a.ID, now)
			wantErr(t, "Update() unknown", r.Customers.Update(ctx, missing), core.ErrNotFound)

			// An archived customer frees its external ID and stays readable.
			noErr(t, "Archive()", c.Archive(at(time.Hour)))
			noErr(t, "Update() archived", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() archived", err)
			equal(t, "Get() archived", got, c)
			_, err = r.Customers.GetByExternalID(ctx, org, "cust-1")
			wantErr(t, "GetByExternalID() archived", err, core.ErrNotFound)
			noErr(t, "Create() with a freed external ID", r.Customers.Create(ctx, dup))
			got, err = r.Customers.GetByExternalID(ctx, org, "cust-1")
			noErr(t, "GetByExternalID() of the new customer", err)
			equal(t, "GetByExternalID() of the new customer", got, dup)
			// Two archived customers may share an external ID.
			noErr(t, "Archive() the second", dup.Archive(at(time.Hour)))
			noErr(t, "Update() the second archived", r.Customers.Update(ctx, dup))
			return nil
		})
	})

	t.Run("invoice custom sections", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		org := a.OrganizationID
		s1, s2, s3 := customerFixtureSection(e, org, "s1"), customerFixtureSection(e, org, "s2"), customerFixtureSection(e, org, "s3")
		c := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust", a.ID, now)
		c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s3.ID, s1.ID}
		c.SystemInvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s1.ID, s2.ID}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, s := range []*invoice.CustomSection{s1, s2, s3} {
				noErr(t, "InvoiceCustomSections.Create()", r.InvoiceCustomSections.Create(ctx, s))
			}
			noErr(t, "Create()", r.Customers.Create(ctx, c))
			got, err := r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, c)

			c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s2.ID}
			c.SystemInvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s3.ID, s2.ID, s1.ID}
			noErr(t, "Update()", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, c)

			// Deleting a section removes it from the customer. memstore keeps
			// the dangling ID (the ports do not document the cascade).
			noErr(t, "InvoiceCustomSections.Delete()", r.InvoiceCustomSections.Delete(ctx, org, s2.ID, now))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() after deleting a section", err)
			if h.Name != "memstore" {
				equal(t, "InvoiceCustomSectionIDs", got.InvoiceCustomSectionIDs, []core.InvoiceCustomSectionID{})
				equal(t, "SystemInvoiceCustomSectionIDs", got.SystemInvoiceCustomSectionIDs, []core.InvoiceCustomSectionID{s3.ID, s1.ID})
			}

			c.InvoiceCustomSectionIDs = nil
			c.SystemInvoiceCustomSectionIDs = nil
			noErr(t, "Update() without sections", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() without sections", err)
			equal(t, "Get() without sections", got, c)
			return nil
		})
	})

	t.Run("taxes", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		org := a.OrganizationID
		taxes := make([]*catalog.Tax, 3)
		for i := range taxes {
			taxes[i] = &catalog.Tax{
				ID: core.NewID[core.TaxID](e.ids), OrganizationID: org, Code: "vat-" + string(rune('a'+i)), Name: "VAT",
				Rate: money.MustParse("20"), CreatedAt: now, UpdatedAt: now,
			}
		}
		c := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust", a.ID, now)
		c.TaxIDs = []core.TaxID{taxes[2].ID, taxes[0].ID}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range taxes {
				noErr(t, "Taxes.Create()", r.Taxes.Create(ctx, x))
			}
			noErr(t, "Create()", r.Customers.Create(ctx, c))
			got, err := r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get()", err)
			equal(t, "TaxIDs", got.TaxIDs, c.TaxIDs)
			c.TaxIDs = []core.TaxID{taxes[1].ID, taxes[2].ID, taxes[0].ID}
			noErr(t, "Update()", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "TaxIDs after Update()", got.TaxIDs, c.TaxIDs)
			c.TaxIDs = nil
			noErr(t, "Update() without taxes", r.Customers.Update(ctx, c))
			got, err = r.Customers.Get(ctx, org, c.ID)
			noErr(t, "Get() without taxes", err)
			equal(t, "TaxIDs without taxes", got.TaxIDs, []core.TaxID{})
			return nil
		})
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		eu := organization.NewBillingEntity(org, core.NewID[core.BillingEntityID](e.ids), "eu", "Acme EU", now)
		mk := func(ext string, f func(c *customer.Customer)) *customer.Customer {
			c := customer.New(org, core.NewID[core.CustomerID](e.ids), ext, a.ID, now)
			f(c)
			return c
		}
		c1 := mk("alpha", func(c *customer.Customer) { c.Name = ptr("Alpha Industries"); c.CustomerType = customer.TypeCompany })
		c2 := mk("beta", func(c *customer.Customer) {
			c.BillingEntityID = eu.ID
			c.LegalName = ptr("Beta GmbH")
			c.AccountType = customer.AccountTypePartner
			c.CustomerType = customer.TypeCompany
		})
		c3 := mk("gamma", func(c *customer.Customer) {
			c.Firstname = ptr("Jane")
			c.Lastname = ptr("Doe_Smith")
			c.CustomerType = customer.TypeIndividual
		})
		c4 := mk("delta", func(c *customer.Customer) { c.Email = ptr("billing@DELTA.test") })
		c5 := mk("epsilon", func(c *customer.Customer) { c.BillingEntityID = eu.ID })
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, eu))
			for _, c := range []*customer.Customer{c1, c2, c3, c4, c5} {
				noErr(t, "Create()", r.Customers.Create(ctx, c))
			}
			noErr(t, "Create() in another organization", r.Customers.Create(ctx,
				customer.New(b.OrganizationID, core.NewID[core.CustomerID](e.ids), "alpha", b.ID, now)))
			noErr(t, "Archive()", c5.Archive(at(time.Hour)))
			noErr(t, "Update() archived", r.Customers.Update(ctx, c5))

			cid := func(c *customer.Customer) core.CustomerID { return c.ID }
			for _, tc := range []struct {
				name string
				f    customer.Filter
				want []*customer.Customer
			}{
				{"all live", customer.Filter{}, []*customer.Customer{c1, c2, c3, c4}},
				{"with deleted", customer.Filter{IncludeDeleted: true}, []*customer.Customer{c1, c2, c3, c4, c5}},
				{"billing entity", customer.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID}, IncludeDeleted: true}, []*customer.Customer{c2, c5}},
				{"billing entities", customer.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID, a.ID}}, []*customer.Customer{c1, c2, c3, c4}},
				{"account type", customer.Filter{AccountTypes: []customer.AccountType{customer.AccountTypePartner}}, []*customer.Customer{c2}},
				{"customer type", customer.Filter{CustomerType: customer.TypeCompany}, []*customer.Customer{c1, c2}},
				{"search external ID", customer.Filter{Search: "AMM"}, []*customer.Customer{c3}},
				{"search name", customer.Filter{Search: "industries"}, []*customer.Customer{c1}},
				{"search legal name", customer.Filter{Search: "gmbh"}, []*customer.Customer{c2}},
				{"search first name", customer.Filter{Search: "jan"}, []*customer.Customer{c3}},
				{"search last name with a wildcard", customer.Filter{Search: "e_s"}, []*customer.Customer{c3}},
				{"search email", customer.Filter{Search: "delta.test"}, []*customer.Customer{c4}},
				{"search percent", customer.Filter{Search: "%"}, nil},
				{"search and type", customer.Filter{Search: "a", CustomerType: customer.TypeIndividual}, []*customer.Customer{c3}},
			} {
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
					return r.Customers.List(ctx, org, tc.f, p)
				})
				equal(t, "List() "+tc.name, ids(got, cid), ids(tc.want, cid))
			}
			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
				return r.Customers.List(ctx, org, customer.Filter{IncludeDeleted: true}, p)
			})
			equal(t, "List() items", all, []*customer.Customer{c1, c2, c3, c4, c5})
			return nil
		})
	})

	t.Run("dunning", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		camp := customerFixtureCampaign(e, org, "camp-1")
		camp2 := customerFixtureCampaign(e, org, "camp-2")
		otherCamp := customerFixtureCampaign(e, b.OrganizationID, "camp-1")
		mk := func(ext string, campaign core.DunningCampaignID, f func(c *customer.Customer)) *customer.Customer {
			c := customer.New(org, core.NewID[core.CustomerID](e.ids), ext, a.ID, now)
			c.AppliedDunningCampaignID = campaign
			if !campaign.IsZero() {
				c.DunningCampaignAttempts = map[string]int{"EUR": 2, "USD": 1}
				c.LastDunningCampaignAttemptAt = ptr(at(-time.Hour))
			}
			f(c)
			return c
		}
		none := func(*customer.Customer) {}
		c1 := mk("c1", camp.ID, none)
		c2 := mk("c2", core.DunningCampaignID{}, none)
		c3 := mk("c3", camp.ID, func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })
		c4 := mk("c4", camp.ID, func(c *customer.Customer) { c.DeletedAt = ptr(at(time.Hour)) })
		c5 := mk("c5", camp2.ID, none)
		c6 := mk("c6", camp.ID, none)
		c7 := mk("c7", core.DunningCampaignID{}, func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })
		otherCust := customer.New(b.OrganizationID, core.NewID[core.CustomerID](e.ids), "c1", b.ID, now)
		otherCust.AppliedDunningCampaignID = otherCamp.ID
		otherCust.DunningCampaignAttempts = map[string]int{"EUR": 1}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*dunning.Campaign{camp, camp2, otherCamp} {
				noErr(t, "DunningCampaigns.Create()", r.DunningCampaigns.Create(ctx, x))
			}
			for _, c := range []*customer.Customer{c1, c2, c3, c4, c5, c6, c7, otherCust} {
				noErr(t, "Create()", r.Customers.Create(ctx, c))
			}
			cid := func(c *customer.Customer) core.CustomerID { return c.ID }
			got := listAll(t, 1, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
				return r.Customers.ListForDunning(ctx, org, camp.ID, p)
			})
			equal(t, "ListForDunning() of a campaign", got, []*customer.Customer{c1, c6})
			got = listAll(t, 1, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
				return r.Customers.ListForDunning(ctx, org, core.DunningCampaignID{}, p)
			})
			equal(t, "ListForDunning() without a campaign", ids(got, cid), []core.CustomerID{c2.ID})
			got = listAll(t, 5, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
				return r.Customers.ListForDunning(ctx, b.OrganizationID, camp.ID, p)
			})
			equal(t, "ListForDunning() from another organization", len(got), 0)

			n, err := r.Customers.ResetDunningCampaign(ctx, org, core.DunningCampaignID{})
			noErr(t, "ResetDunningCampaign() of no campaign", err)
			equal(t, "ResetDunningCampaign() of no campaign", n, 0)
			n, err = r.Customers.ResetDunningCampaign(ctx, b.OrganizationID, camp.ID)
			noErr(t, "ResetDunningCampaign() from another organization", err)
			equal(t, "ResetDunningCampaign() from another organization", n, 0)
			n, err = r.Customers.ResetDunningCampaign(ctx, org, camp.ID)
			noErr(t, "ResetDunningCampaign()", err)
			equal(t, "ResetDunningCampaign()", n, 4)
			for _, c := range []*customer.Customer{c1, c3, c4, c6} {
				c.AppliedDunningCampaignID = core.DunningCampaignID{}
				c.DunningCampaignAttempts = nil
				c.LastDunningCampaignAttemptAt = nil
			}
			for _, c := range []*customer.Customer{c1, c2, c3, c4, c5, c6, c7} {
				got, err := r.Customers.Get(ctx, org, c.ID)
				noErr(t, "Get() after ResetDunningCampaign()", err)
				equal(t, "Get() after ResetDunningCampaign()", got, c)
			}
			gotOther, err := r.Customers.Get(ctx, b.OrganizationID, otherCust.ID)
			noErr(t, "Get() of another organization's customer", err)
			equal(t, "Get() of another organization's customer", gotOther, otherCust)
			got = listAll(t, 2, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
				return r.Customers.ListForDunning(ctx, org, core.DunningCampaignID{}, p)
			})
			equal(t, "ListForDunning() without a campaign after reset", ids(got, cid), []core.CustomerID{c1.ID, c2.ID, c6.ID})
			return nil
		})
	})
}

// customerFixtureFull returns an unsaved customer of the billing entity be
// with every field set.
func customerFixtureFull(t *testing.T, e *env, be *organization.BillingEntity, ext string) *customer.Customer {
	t.Helper()
	c := customer.New(be.OrganizationID, core.NewID[core.CustomerID](e.ids), ext, be.ID, now)
	c.CustomerType = customer.TypeCompany
	c.Name = ptr("Acme Corp")
	c.Firstname = ptr("John")
	c.Lastname = ptr("Doe")
	c.LegalName = ptr("Acme Corporation SAS")
	c.LegalNumber = ptr("123 456 789")
	c.TaxIdentificationNumber = ptr("FR12345678901")
	c.Email = ptr("billing@acme.test,ceo@acme.test")
	c.Phone = ptr("+33 1 23 45 67 89")
	c.URL = ptr("https://acme.test")
	c.LogoURL = ptr("https://acme.test/logo.png")
	c.BillingAddress = core.Address{Line1: "1 Main St", Line2: "Floor 2", City: "Paris", State: "IDF", Zipcode: "75001", Country: "FR"}
	c.ShippingAddress = core.Address{Line1: "2 Dock Rd", City: "Lyon", Zipcode: "69001", Country: "FR"}
	c.Currency = money.MustCurrency("EUR")
	c.Timezone = customerLoad(t, "Europe/Paris")
	c.DocumentLocale = ptr("fr")
	c.InvoiceGracePeriodDays = ptr(3)
	c.NetPaymentTermDays = ptr(30)
	c.FinalizeZeroAmountInvoice = customer.ZeroAmountFinalizationSkip
	c.IssuingDateAnchor = core.IssuingDateAnchorNextPeriodStart
	c.IssuingDateAdjustment = core.IssuingDateAdjustmentAlignWithFinalizationDate
	c.SkipInvoiceCustomSections = true
	c.ExcludeFromDunningCampaign = true
	c.DunningCampaignAttempts = map[string]int{"EUR": 2}
	c.LastDunningCampaignAttemptAt = ptr(at(-time.Hour))
	c.AssignSequence(7, "ACM")
	c.PaymentReceiptCounter = 4
	c.Metadata = core.Metadata{{Key: "plan", Value: "pro", DisplayInInvoice: true}, {Key: "crm", Value: "42"}}
	return c
}

// customerFixtureSection returns an unsaved manual invoice custom section.
func customerFixtureSection(e *env, org core.OrganizationID, code string) *invoice.CustomSection {
	return &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](e.ids), OrganizationID: org, Code: code, Name: "Section " + code,
		SectionType: invoice.CustomSectionTypeManual, CreatedAt: now, UpdatedAt: now,
	}
}

// customerFixtureCampaign returns an unsaved dunning campaign with one
// threshold.
func customerFixtureCampaign(e *env, org core.OrganizationID, code string) *dunning.Campaign {
	c := dunning.New(org, core.NewID[core.DunningCampaignID](e.ids), code, "Campaign "+code, now)
	c.Thresholds = []dunning.Threshold{{ID: core.NewID[core.DunningCampaignThresholdID](e.ids), Currency: money.MustCurrency("EUR"), Amount: 1000}}
	return c
}

func customerLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q) error got = %v, want = nil", name, err)
	}
	return loc
}
