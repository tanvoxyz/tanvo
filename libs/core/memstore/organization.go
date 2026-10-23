package memstore

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// organizationData holds the tables of package organization.
type organizationData struct {
	organizations   *table[core.OrganizationID, organization.Organization]
	billingEntities *table[core.BillingEntityID, organization.BillingEntity]
}

func newOrganizationData() *organizationData {
	return &organizationData{
		organizations: newTable[core.OrganizationID](func(o organization.Organization) organization.Organization {
			o.AuthenticationMethods = slices.Clone(o.AuthenticationMethods)
			return o
		}),
		billingEntities: newTable[core.BillingEntityID](func(b organization.BillingEntity) organization.BillingEntity {
			b.EmailSettings = slices.Clone(b.EmailSettings)
			b.TaxIDs = slices.Clone(b.TaxIDs)
			b.InvoiceCustomSectionIDs = slices.Clone(b.InvoiceCustomSectionIDs)
			return b
		}),
	}
}

func (d *organizationData) snapshot() *organizationData {
	return &organizationData{
		organizations:   d.organizations.snapshot(),
		billingEntities: d.billingEntities.snapshot(),
	}
}

// organizationRepo implements organization.Repository.
type organizationRepo struct{ d *organizationData }

func (r organizationRepo) Create(_ context.Context, o *organization.Organization) error {
	if r.d.organizations.has(o.ID) {
		return conflict("organization", "id")
	}
	if r.d.organizations.any(func(x *organization.Organization) bool { return x.Slug == o.Slug }) {
		return conflict("organization", "slug")
	}
	r.d.organizations.put(o.ID, *o)
	return nil
}

func (r organizationRepo) Update(_ context.Context, o *organization.Organization) error {
	if !r.d.organizations.has(o.ID) {
		return notFound("organization", o.ID)
	}
	if r.d.organizations.any(func(x *organization.Organization) bool { return x.ID != o.ID && x.Slug == o.Slug }) {
		return conflict("organization", "slug")
	}
	r.d.organizations.put(o.ID, *o)
	return nil
}

func (r organizationRepo) Get(_ context.Context, id core.OrganizationID) (*organization.Organization, error) {
	o, ok := r.d.organizations.get(id)
	if !ok {
		return nil, notFound("organization", id)
	}
	return o, nil
}

func (r organizationRepo) GetBySlug(_ context.Context, slug string) (*organization.Organization, error) {
	o, ok := r.d.organizations.first(func(x *organization.Organization) bool { return x.Slug == slug })
	if !ok {
		return nil, notFound("organization", stringer(slug))
	}
	return o, nil
}

// billingEntityRepo implements organization.BillingEntityRepository.
type billingEntityRepo struct{ d *organizationData }

func (r billingEntityRepo) check(b *organization.BillingEntity) error {
	if b.Archived() {
		return nil
	}
	if r.d.billingEntities.any(func(x *organization.BillingEntity) bool {
		return x.ID != b.ID && x.OrganizationID == b.OrganizationID && !x.Archived() && x.Code == b.Code
	}) {
		return conflict("billing_entity", "code")
	}
	if b.IsDefault && r.d.billingEntities.any(func(x *organization.BillingEntity) bool {
		return x.ID != b.ID && x.OrganizationID == b.OrganizationID && x.IsDefault
	}) {
		return conflict("billing_entity", "is_default")
	}
	return nil
}

func (r billingEntityRepo) Create(_ context.Context, b *organization.BillingEntity) error {
	if r.d.billingEntities.has(b.ID) {
		return conflict("billing_entity", "id")
	}
	if err := r.check(b); err != nil {
		return err
	}
	r.d.billingEntities.put(b.ID, *b)
	return nil
}

func (r billingEntityRepo) Update(_ context.Context, b *organization.BillingEntity) error {
	cur, ok := r.d.billingEntities.get(b.ID)
	if !ok || cur.OrganizationID != b.OrganizationID {
		return notFound("billing_entity", b.ID)
	}
	if err := r.check(b); err != nil {
		return err
	}
	r.d.billingEntities.put(b.ID, *b)
	return nil
}

func (r billingEntityRepo) Get(_ context.Context, org core.OrganizationID, id core.BillingEntityID) (*organization.BillingEntity, error) {
	b, ok := r.d.billingEntities.get(id)
	if !ok || b.OrganizationID != org {
		return nil, notFound("billing_entity", id)
	}
	return b, nil
}

func (r billingEntityRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*organization.BillingEntity, error) {
	b, ok := r.d.billingEntities.first(func(x *organization.BillingEntity) bool {
		return x.OrganizationID == org && x.Code == code && !x.Archived()
	})
	if !ok {
		return nil, notFound("billing_entity", stringer(code))
	}
	return b, nil
}

func (r billingEntityRepo) GetDefault(_ context.Context, org core.OrganizationID) (*organization.BillingEntity, error) {
	b, ok := r.d.billingEntities.first(func(x *organization.BillingEntity) bool {
		return x.OrganizationID == org && x.IsDefault
	})
	if !ok {
		return nil, notFound("billing_entity", stringer("default"))
	}
	return b, nil
}

func (r billingEntityRepo) List(_ context.Context, org core.OrganizationID, f organization.BillingEntityFilter, page core.PageRequest) (core.Page[*organization.BillingEntity], error) {
	rows := r.d.billingEntities.find(func(x *organization.BillingEntity) bool {
		return x.OrganizationID == org && (f.IncludeArchived || !x.Archived())
	})
	return paginate(rows, func(b *organization.BillingEntity) core.BillingEntityID { return b.ID }, page)
}

var (
	_ organization.Repository              = organizationRepo{}
	_ organization.BillingEntityRepository = billingEntityRepo{}
)
