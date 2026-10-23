package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testOrganization(t *testing.T, h Harness) {
	t.Run("organizations", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		o := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme, Inc.", now)
		o.Email = ptr("billing@acme.test")
		o.Logo = "blob/logo.png"
		o.Address = core.Address{Line1: "1 Main St", City: "Paris", Zipcode: "75001", Country: "FR"}
		o.AuthenticationMethods = []organization.AuthenticationMethod{organization.AuthenticationMethodOkta}
		o.FromEmailEnabled = true
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Organizations.Create(ctx, o))
			wantErr(t, "Create() with the same ID", r.Organizations.Create(ctx, o), core.ErrConflict)
			got, err := r.Organizations.Get(ctx, o.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, o)
			got, err = r.Organizations.GetBySlug(ctx, "acme-inc")
			noErr(t, "GetBySlug()", err)
			equal(t, "GetBySlug()", got, o)
			_, err = r.Organizations.GetBySlug(ctx, "nope")
			wantErr(t, "GetBySlug() unknown", err, core.ErrNotFound)
			_, err = r.Organizations.Get(ctx, core.NewID[core.OrganizationID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			o.Name = "Acme"
			o.Email = nil
			o.MaxWalletsPerCustomer = 10
			o.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Organizations.Update(ctx, o))
			got, err = r.Organizations.Get(ctx, o.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, o)

			missing := organization.New(core.NewID[core.OrganizationID](e.ids), "Missing", now)
			wantErr(t, "Update() unknown", r.Organizations.Update(ctx, missing), core.ErrNotFound)
			return nil
		})
	})

	t.Run("billing entities", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		be := organization.NewBillingEntity(org, core.NewID[core.BillingEntityID](e.ids), "eu", "Acme Europe", now)
		be.LegalName = ptr("Acme Europe SAS")
		be.Address = core.Address{Country: "FR", City: "Paris"}
		be.Timezone = mustLoad(t, "Europe/Paris")
		be.DefaultCurrency = money.MustCurrency("EUR")
		be.EmailSettings = []organization.EmailSetting{organization.EmailSettingInvoiceFinalized, organization.EmailSettingCreditNoteCreated}
		be.EUTaxManagement = true
		be.InvoiceGracePeriodDays = 3
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.BillingEntities.Create(ctx, be))
			got, err := r.BillingEntities.Get(ctx, org, be.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, be)
			got, err = r.BillingEntities.GetByCode(ctx, org, "eu")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, be)
			got, err = r.BillingEntities.GetDefault(ctx, org)
			noErr(t, "GetDefault()", err)
			equal(t, "GetDefault()", got, a)

			// Isolation: another organization sees nothing.
			_, err = r.BillingEntities.Get(ctx, b.OrganizationID, be.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.BillingEntities.GetByCode(ctx, b.OrganizationID, "eu")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			other := *be
			other.OrganizationID = b.OrganizationID
			wantErr(t, "Update() through another organization", r.BillingEntities.Update(ctx, &other), core.ErrNotFound)

			// Uniqueness: codes among live entities, one default.
			dup := organization.NewBillingEntity(org, core.NewID[core.BillingEntityID](e.ids), "eu", "Dup", now)
			wantErr(t, "Create() with a taken code", r.BillingEntities.Create(ctx, dup), core.ErrConflict)
			dup.Code = "dup"
			dup.IsDefault = true
			wantErr(t, "Create() of a second default", r.BillingEntities.Create(ctx, dup), core.ErrConflict)
			dup.IsDefault = false
			noErr(t, "Create() after conflicts", r.BillingEntities.Create(ctx, dup))
			dup.Code = "eu"
			wantErr(t, "Update() to a taken code", r.BillingEntities.Update(ctx, dup), core.ErrConflict)

			// An archived entity frees its code.
			noErr(t, "Archive()", be.Archive(at(time.Hour)))
			noErr(t, "Update() archived", r.BillingEntities.Update(ctx, be))
			_, err = r.BillingEntities.GetByCode(ctx, org, "eu")
			wantErr(t, "GetByCode() archived", err, core.ErrNotFound)
			noErr(t, "Update() to a freed code", r.BillingEntities.Update(ctx, dup))

			live := listAll(t, 1, func(p core.PageRequest) (core.Page[*organization.BillingEntity], error) {
				return r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{}, p)
			})
			equal(t, "List()", ids(live, entityID), []core.BillingEntityID{a.ID, dup.ID})
			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*organization.BillingEntity], error) {
				return r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{IncludeArchived: true}, p)
			})
			equal(t, "List() with archived", all, []*organization.BillingEntity{a, be, dup})
			p, err := r.BillingEntities.List(ctx, b.OrganizationID, organization.BillingEntityFilter{IncludeArchived: true}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(p.Items, entityID), []core.BillingEntityID{b.ID})
			return nil
		})
	})

	t.Run("invoice custom sections", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		section := func(code string) *invoice.CustomSection {
			return &invoice.CustomSection{
				ID: core.NewID[core.InvoiceCustomSectionID](e.ids), OrganizationID: org, Code: code, Name: "Section " + code,
				DisplayName: ptr("Bank details"), Details: ptr("IBAN …"), SectionType: invoice.CustomSectionTypeManual,
				CreatedAt: now, UpdatedAt: now,
			}
		}
		s1, s2, s3 := section("s1"), section("s2"), section("s3")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, s := range []*invoice.CustomSection{s1, s2, s3} {
				noErr(t, "Create()", r.InvoiceCustomSections.Create(ctx, s))
			}
			wantErr(t, "Create() with a taken code", r.InvoiceCustomSections.Create(ctx, section("s1")), core.ErrConflict)
			got, err := r.InvoiceCustomSections.Get(ctx, org, s1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, s1)
			got, err = r.InvoiceCustomSections.GetByCode(ctx, org, "s2")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, s2)
			_, err = r.InvoiceCustomSections.Get(ctx, b.OrganizationID, s1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)

			s2.Details = nil
			s2.SectionType = invoice.CustomSectionTypeSystemGenerated
			noErr(t, "Update()", r.InvoiceCustomSections.Update(ctx, s2))
			s3.Code = "s1"
			wantErr(t, "Update() to a taken code", r.InvoiceCustomSections.Update(ctx, s3), core.ErrConflict)
			s3.Code = "s3"

			byIDs, err := r.InvoiceCustomSections.ListByIDs(ctx, org, []core.InvoiceCustomSectionID{s3.ID, s1.ID, core.NewID[core.InvoiceCustomSectionID](e.ids)})
			noErr(t, "ListByIDs()", err)
			equal(t, "ListByIDs()", byIDs, []*invoice.CustomSection{s1, s3})
			byIDs, err = r.InvoiceCustomSections.ListByIDs(ctx, b.OrganizationID, []core.InvoiceCustomSectionID{s1.ID})
			noErr(t, "ListByIDs() from another organization", err)
			equal(t, "ListByIDs() from another organization", byIDs, []*invoice.CustomSection{})

			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*invoice.CustomSection], error) {
				return r.InvoiceCustomSections.List(ctx, org, p)
			})
			equal(t, "List()", all, []*invoice.CustomSection{s1, s2, s3})

			// Billing entities keep their default sections in order.
			a.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s3.ID, s1.ID}
			noErr(t, "BillingEntities.Update()", r.BillingEntities.Update(ctx, a))
			be, err := r.BillingEntities.Get(ctx, org, a.ID)
			noErr(t, "BillingEntities.Get()", err)
			equal(t, "InvoiceCustomSectionIDs", be.InvoiceCustomSectionIDs, a.InvoiceCustomSectionIDs)

			wantErr(t, "Delete() from another organization", r.InvoiceCustomSections.Delete(ctx, b.OrganizationID, s2.ID), core.ErrNotFound)
			noErr(t, "Delete()", r.InvoiceCustomSections.Delete(ctx, org, s2.ID))
			wantErr(t, "Delete() again", r.InvoiceCustomSections.Delete(ctx, org, s2.ID), core.ErrNotFound)
			_, err = r.InvoiceCustomSections.Get(ctx, org, s2.ID)
			wantErr(t, "Get() deleted", err, core.ErrNotFound)
			return nil
		})
	})
}

func entityID(b *organization.BillingEntity) core.BillingEntityID { return b.ID }

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q) error got = %v, want = nil", name, err)
	}
	return loc
}
