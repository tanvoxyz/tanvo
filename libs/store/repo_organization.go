package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindOrganization binds the repositories of package organization and the
// invoice custom sections (whose table lives with the organization's).
func (t *txState) bindOrganization(r *port.Repos) {
	r.Organizations = organizationRepo{t}
	r.BillingEntities = billingEntityRepo{t}
	r.InvoiceCustomSections = invoiceCustomSectionRepo{t}
}

// organizationRepo implements organization.Repository.
type organizationRepo struct{ t *txState }

var _ organization.Repository = organizationRepo{}

func organizationParams(o *organization.Organization) db.CreateOrganizationParams {
	return db.CreateOrganizationParams{
		ID:                     o.ID.UUID(),
		Name:                   o.Name,
		Slug:                   o.Slug,
		Email:                  o.Email,
		Logo:                   optString(string(o.Logo)),
		AddressLine1:           o.Address.Line1,
		AddressLine2:           o.Address.Line2,
		AddressCity:            o.Address.City,
		AddressState:           o.Address.State,
		AddressZipcode:         o.Address.Zipcode,
		AddressCountry:         o.Address.Country,
		AuthenticationMethods:  enumStrings(o.AuthenticationMethods),
		AuditLogsRetentionDays: int32(o.AuditLogsRetentionDays), // validated small
		MaxWalletsPerCustomer:  int32(o.MaxWalletsPerCustomer),  // validated small
		CreatedAt:              utc(o.CreatedAt),
		UpdatedAt:              utc(o.UpdatedAt),
	}
}

func organizationOf(r db.Organization) (*organization.Organization, error) {
	return &organization.Organization{
		ID:    idOf[core.OrganizationID](r.ID),
		Name:  r.Name,
		Slug:  r.Slug,
		Email: r.Email,
		Logo:  core.BlobRef(deref(r.Logo)),
		Address: core.Address{
			Line1: r.AddressLine1, Line2: r.AddressLine2, City: r.AddressCity,
			State: r.AddressState, Zipcode: r.AddressZipcode, Country: r.AddressCountry,
		},
		AuthenticationMethods:  enumsOf[organization.AuthenticationMethod](r.AuthenticationMethods),
		AuditLogsRetentionDays: int(r.AuditLogsRetentionDays),
		MaxWalletsPerCustomer:  int(r.MaxWalletsPerCustomer),
		CreatedAt:              utc(r.CreatedAt),
		UpdatedAt:              utc(r.UpdatedAt),
	}, nil
}

func (r organizationRepo) Create(ctx context.Context, o *organization.Organization) error {
	q, err := r.t.queries(ctx, o.ID)
	if err != nil {
		return err
	}
	n, err := q.CreateOrganization(ctx, organizationParams(o))
	return insertErr("organization", n, err)
}

func (r organizationRepo) Update(ctx context.Context, o *organization.Organization) error {
	q, err := r.t.queries(ctx, o.ID)
	if err != nil {
		return err
	}
	// The slug is unique: guard the update so that a conflict keeps the
	// transaction usable.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateOrganization(ctx, db.UpdateOrganizationParams(organizationParams(o)))
		return err
	})
	return updateErr("organization", o.ID, n, err)
}

func (r organizationRepo) Get(ctx context.Context, id core.OrganizationID) (*organization.Organization, error) {
	q, err := r.t.queries(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrganization(ctx, id.UUID())
	if err != nil {
		return nil, getErr("organization", id, err)
	}
	return organizationOf(row)
}

// GetBySlug is not organization-scoped: it finds the tenant.
func (r organizationRepo) GetBySlug(ctx context.Context, slug string) (*organization.Organization, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrganizationBySlug(ctx, slug)
	if err != nil {
		return nil, getErr("organization", slug, err)
	}
	return organizationOf(row)
}

// billingEntityRepo implements organization.BillingEntityRepository.
type billingEntityRepo struct{ t *txState }

var _ organization.BillingEntityRepository = billingEntityRepo{}

func billingEntityParams(b *organization.BillingEntity) db.CreateBillingEntityParams {
	return db.CreateBillingEntityParams{
		OrganizationID:            b.OrganizationID.UUID(),
		ID:                        b.ID.UUID(),
		Code:                      b.Code,
		Name:                      b.Name,
		LegalName:                 b.LegalName,
		LegalNumber:               b.LegalNumber,
		TaxID:                     b.TaxID,
		Email:                     b.Email,
		Phone:                     b.Phone,
		AddressLine1:              b.Address.Line1,
		AddressLine2:              b.Address.Line2,
		AddressCity:               b.Address.City,
		AddressState:              b.Address.State,
		AddressZipcode:            b.Address.Zipcode,
		AddressCountry:            b.Address.Country,
		Logo:                      optString(string(b.Logo)),
		Timezone:                  locationName(b.Timezone),
		DefaultCurrency:           currencyCode(b.DefaultCurrency),
		DocumentLocale:            b.DocumentLocale,
		DocumentNumbering:         string(b.DocumentNumbering),
		DocumentNumberPrefix:      b.DocumentNumberPrefix,
		InvoiceFooter:             b.InvoiceFooter,
		InvoiceGracePeriodDays:    int32(b.InvoiceGracePeriodDays), // validated small
		NetPaymentTermDays:        int32(b.NetPaymentTermDays),     // validated small
		FinalizeZeroAmountInvoice: b.FinalizeZeroAmountInvoice,
		IssuingDateAnchor:         string(b.IssuingDateAnchor),
		IssuingDateAdjustment:     string(b.IssuingDateAdjustment),
		EmailSettings:             enumStrings(b.EmailSettings),
		EuTaxManagement:           b.EUTaxManagement,
		Einvoicing:                b.EInvoicing,
		IsDefault:                 b.IsDefault,
		AppliedDunningCampaignID:  optUUID(b.AppliedDunningCampaignID),
		ArchivedAt:                optUTC(b.ArchivedAt),
		CreatedAt:                 utc(b.CreatedAt),
		UpdatedAt:                 utc(b.UpdatedAt),
	}
}

// billingEntityRow is the common shape of the billing entity reads; the
// row types sqlc generates for each query convert to it.
type billingEntityRow struct {
	BillingEntity           db.BillingEntity
	TaxIds                  []uuid.UUID
	InvoiceCustomSectionIds []uuid.UUID
}

func billingEntityOf(row billingEntityRow) (*organization.BillingEntity, error) {
	r := row.BillingEntity
	loc, err := location(r.Timezone)
	if err != nil {
		return nil, fmt.Errorf("billing entity %s: %w", r.ID, err)
	}
	cur, err := currency(r.DefaultCurrency)
	if err != nil {
		return nil, fmt.Errorf("billing entity %s: %w", r.ID, err)
	}
	return &organization.BillingEntity{
		ID:             idOf[core.BillingEntityID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Code:           r.Code,
		Name:           r.Name,
		LegalName:      r.LegalName,
		LegalNumber:    r.LegalNumber,
		TaxID:          r.TaxID,
		Email:          r.Email,
		Phone:          r.Phone,
		Address: core.Address{
			Line1: r.AddressLine1, Line2: r.AddressLine2, City: r.AddressCity,
			State: r.AddressState, Zipcode: r.AddressZipcode, Country: r.AddressCountry,
		},
		Logo:                      core.BlobRef(deref(r.Logo)),
		Timezone:                  loc,
		DefaultCurrency:           cur,
		DocumentLocale:            r.DocumentLocale,
		DocumentNumbering:         organization.DocumentNumbering(r.DocumentNumbering),
		DocumentNumberPrefix:      r.DocumentNumberPrefix,
		InvoiceFooter:             r.InvoiceFooter,
		InvoiceGracePeriodDays:    int(r.InvoiceGracePeriodDays),
		NetPaymentTermDays:        int(r.NetPaymentTermDays),
		FinalizeZeroAmountInvoice: r.FinalizeZeroAmountInvoice,
		IssuingDateAnchor:         core.IssuingDateAnchor(r.IssuingDateAnchor),
		IssuingDateAdjustment:     core.IssuingDateAdjustment(r.IssuingDateAdjustment),
		EmailSettings:             enumsOf[organization.EmailSetting](r.EmailSettings),
		EUTaxManagement:           r.EuTaxManagement,
		EInvoicing:                r.Einvoicing,
		IsDefault:                 r.IsDefault,
		AppliedDunningCampaignID:  optIDOf[core.DunningCampaignID](r.AppliedDunningCampaignID),
		TaxIDs:                    idsOf[core.TaxID](row.TaxIds),
		InvoiceCustomSectionIDs:   idsOf[core.InvoiceCustomSectionID](row.InvoiceCustomSectionIds),
		ArchivedAt:                optUTC(r.ArchivedAt),
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
	}, nil
}

// writeLinks replaces the default taxes and sections of b.
func (r billingEntityRepo) writeLinks(ctx context.Context, q *db.Queries, b *organization.BillingEntity, replace bool) error {
	org, id := b.OrganizationID.UUID(), b.ID.UUID()
	if replace {
		if err := q.DeleteBillingEntityTaxes(ctx, db.DeleteBillingEntityTaxesParams{OrganizationID: org, BillingEntityID: id}); err != nil {
			return err
		}
		if err := q.DeleteBillingEntitySections(ctx, db.DeleteBillingEntitySectionsParams{OrganizationID: org, BillingEntityID: id}); err != nil {
			return err
		}
	}
	if len(b.TaxIDs) > 0 {
		if err := q.InsertBillingEntityTaxes(ctx, db.InsertBillingEntityTaxesParams{OrganizationID: org, BillingEntityID: id, TaxIds: uuids(b.TaxIDs)}); err != nil {
			return err
		}
	}
	if len(b.InvoiceCustomSectionIDs) > 0 {
		if err := q.InsertBillingEntitySections(ctx, db.InsertBillingEntitySectionsParams{OrganizationID: org, BillingEntityID: id, SectionIds: uuids(b.InvoiceCustomSectionIDs)}); err != nil {
			return err
		}
	}
	return nil
}

func (r billingEntityRepo) Create(ctx context.Context, b *organization.BillingEntity) error {
	q, err := r.t.queries(ctx, b.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateBillingEntity(ctx, billingEntityParams(b))
	if err := insertErr("billing_entity", n, err); err != nil {
		return err
	}
	if err := r.writeLinks(ctx, q, b, false); err != nil {
		return fmt.Errorf("create billing_entity %s: %w", b.ID, mapErr(err))
	}
	return nil
}

func (r billingEntityRepo) Update(ctx context.Context, b *organization.BillingEntity) error {
	q, err := r.t.queries(ctx, b.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateBillingEntity(ctx, db.UpdateBillingEntityParams(billingEntityParams(b)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeLinks(ctx, q, b, true)
	})
	return updateErr("billing_entity", b.ID, n, err)
}

func (r billingEntityRepo) Get(ctx context.Context, org core.OrganizationID, id core.BillingEntityID) (*organization.BillingEntity, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetBillingEntity(ctx, db.GetBillingEntityParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("billing_entity", id, err)
	}
	return billingEntityOf(billingEntityRow(row))
}

func (r billingEntityRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*organization.BillingEntity, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetBillingEntityByCode(ctx, db.GetBillingEntityByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("billing_entity", code, err)
	}
	return billingEntityOf(billingEntityRow(row))
}

func (r billingEntityRepo) GetDefault(ctx context.Context, org core.OrganizationID) (*organization.BillingEntity, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDefaultBillingEntity(ctx, org.UUID())
	if err != nil {
		return nil, getErr("billing_entity", "default", err)
	}
	return billingEntityOf(billingEntityRow(row))
}

func (r billingEntityRepo) List(ctx context.Context, org core.OrganizationID, f organization.BillingEntityFilter, page core.PageRequest) (core.Page[*organization.BillingEntity], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*organization.BillingEntity]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*organization.BillingEntity]{}, err
	}
	rows, err := q.ListBillingEntities(ctx, db.ListBillingEntitiesParams{
		OrganizationID: org.UUID(), IncludeArchived: f.IncludeArchived, After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*organization.BillingEntity]{}, fmt.Errorf("list billing entities: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListBillingEntitiesRow) (*organization.BillingEntity, error) {
			return billingEntityOf(billingEntityRow(r))
		},
		func(b *organization.BillingEntity) string { return b.ID.String() })
}

// invoiceCustomSectionRepo implements invoice.CustomSectionRepository.
type invoiceCustomSectionRepo struct{ t *txState }

var _ invoice.CustomSectionRepository = invoiceCustomSectionRepo{}

func invoiceCustomSectionParams(s *invoice.CustomSection) db.CreateInvoiceCustomSectionParams {
	return db.CreateInvoiceCustomSectionParams{
		OrganizationID: s.OrganizationID.UUID(),
		ID:             s.ID.UUID(),
		Code:           s.Code,
		Name:           s.Name,
		DisplayName:    s.DisplayName,
		Details:        s.Details,
		SectionType:    string(s.SectionType),
		CreatedAt:      utc(s.CreatedAt),
		UpdatedAt:      utc(s.UpdatedAt),
	}
}

func invoiceCustomSectionOf(r db.InvoiceCustomSection) (*invoice.CustomSection, error) {
	return &invoice.CustomSection{
		ID:             idOf[core.InvoiceCustomSectionID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Code:           r.Code,
		Name:           r.Name,
		DisplayName:    r.DisplayName,
		Details:        r.Details,
		SectionType:    invoice.CustomSectionType(r.SectionType),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r invoiceCustomSectionRepo) Create(ctx context.Context, s *invoice.CustomSection) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoiceCustomSection(ctx, invoiceCustomSectionParams(s))
	return insertErr("invoice_custom_section", n, err)
}

func (r invoiceCustomSectionRepo) Update(ctx context.Context, s *invoice.CustomSection) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateInvoiceCustomSection(ctx, db.UpdateInvoiceCustomSectionParams(invoiceCustomSectionParams(s)))
		return err
	})
	return updateErr("invoice_custom_section", s.ID, n, err)
}

func (r invoiceCustomSectionRepo) Get(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInvoiceCustomSection(ctx, db.GetInvoiceCustomSectionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("invoice_custom_section", id, err)
	}
	return invoiceCustomSectionOf(row)
}

func (r invoiceCustomSectionRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*invoice.CustomSection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInvoiceCustomSectionByCode(ctx, db.GetInvoiceCustomSectionByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("invoice_custom_section", code, err)
	}
	return invoiceCustomSectionOf(row)
}

func (r invoiceCustomSectionRepo) ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.InvoiceCustomSectionID) ([]*invoice.CustomSection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceCustomSectionsByIDs(ctx, db.ListInvoiceCustomSectionsByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("list invoice custom sections: %w", mapErr(err))
	}
	return convAll(rows, invoiceCustomSectionOf)
}

func (r invoiceCustomSectionRepo) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*invoice.CustomSection], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*invoice.CustomSection]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*invoice.CustomSection]{}, err
	}
	rows, err := q.ListInvoiceCustomSections(ctx, db.ListInvoiceCustomSectionsParams{OrganizationID: org.UUID(), After: after, Lim: lim})
	if err != nil {
		return core.Page[*invoice.CustomSection]{}, fmt.Errorf("list invoice custom sections: %w", mapErr(err))
	}
	return pageOf(rows, page, invoiceCustomSectionOf, func(s *invoice.CustomSection) string { return s.ID.String() })
}

func (r invoiceCustomSectionRepo) Delete(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DeleteInvoiceCustomSection(ctx, db.DeleteInvoiceCustomSectionParams{OrganizationID: org.UUID(), ID: id.UUID()})
		return err
	})
	return updateErr("invoice_custom_section", id, n, err)
}
