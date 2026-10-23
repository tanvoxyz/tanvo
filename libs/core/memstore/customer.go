package memstore

import (
	"context"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
)

// customerData holds the tables of package customer.
type customerData struct {
	customers *table[core.CustomerID, customer.Customer]
}

func newCustomerData() *customerData {
	return &customerData{
		customers: newTable[core.CustomerID](func(c customer.Customer) customer.Customer {
			c.InvoiceCustomSectionIDs = slices.Clone(c.InvoiceCustomSectionIDs)
			c.SystemInvoiceCustomSectionIDs = slices.Clone(c.SystemInvoiceCustomSectionIDs)
			c.Metadata = c.Metadata.Clone()
			c.TaxIDs = slices.Clone(c.TaxIDs)
			return c
		}),
	}
}

func (d *customerData) snapshot() *customerData {
	return &customerData{customers: d.customers.snapshot()}
}

// customerRepo implements customer.Repository.
type customerRepo struct{ d *customerData }

var _ customer.Repository = customerRepo{}

func (r customerRepo) check(c *customer.Customer) error {
	if c.Deleted() {
		return nil
	}
	if r.d.customers.any(func(x *customer.Customer) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && !x.Deleted() && x.ExternalID == c.ExternalID
	}) {
		return conflict("customer", "external_id")
	}
	return nil
}

func (r customerRepo) Create(_ context.Context, c *customer.Customer) error {
	if r.d.customers.has(c.ID) {
		return conflict("customer", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.customers.put(c.ID, *c)
	return nil
}

func (r customerRepo) Update(_ context.Context, c *customer.Customer) error {
	cur, ok := r.d.customers.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("customer", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.customers.put(c.ID, *c)
	return nil
}

func (r customerRepo) Get(_ context.Context, org core.OrganizationID, id core.CustomerID) (*customer.Customer, error) {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("customer", id)
	}
	return c, nil
}

func (r customerRepo) GetByExternalID(_ context.Context, org core.OrganizationID, externalID string) (*customer.Customer, error) {
	c, ok := r.d.customers.first(func(x *customer.Customer) bool {
		return x.OrganizationID == org && !x.Deleted() && x.ExternalID == externalID
	})
	if !ok {
		return nil, notFound("customer", stringer(externalID))
	}
	return c, nil
}

func (r customerRepo) List(_ context.Context, org core.OrganizationID, f customer.Filter, page core.PageRequest) (core.Page[*customer.Customer], error) {
	search := strings.ToLower(f.Search)
	rows := r.d.customers.find(func(x *customer.Customer) bool {
		switch {
		case x.OrganizationID != org,
			!f.IncludeDeleted && x.Deleted(),
			len(f.BillingEntityIDs) > 0 && !slices.Contains(f.BillingEntityIDs, x.BillingEntityID),
			f.AccountType != "" && x.AccountType != f.AccountType,
			f.CustomerType != "" && x.CustomerType != f.CustomerType:
			return false
		}
		return search == "" || customerMatches(x, search)
	})
	return paginate(rows, customerKey, page)
}

func (r customerRepo) ListForDunning(_ context.Context, org core.OrganizationID, campaign core.DunningCampaignID, page core.PageRequest) (core.Page[*customer.Customer], error) {
	rows := r.d.customers.find(func(x *customer.Customer) bool {
		return x.OrganizationID == org && !x.Deleted() && !x.ExcludeFromDunningCampaign && x.AppliedDunningCampaignID == campaign
	})
	return paginate(rows, customerKey, page)
}

func (r customerRepo) ResetDunningCampaign(_ context.Context, org core.OrganizationID, campaign core.DunningCampaignID) (int, error) {
	if campaign.IsZero() {
		return 0, nil
	}
	rows := r.d.customers.find(func(x *customer.Customer) bool {
		return x.OrganizationID == org && x.AppliedDunningCampaignID == campaign
	})
	for _, c := range rows {
		c.AppliedDunningCampaignID = core.DunningCampaignID{}
		c.DunningCampaignAttempts = nil
		c.LastDunningCampaignAttemptAt = nil
		r.d.customers.put(c.ID, *c)
	}
	return len(rows), nil
}

func customerKey(c *customer.Customer) core.CustomerID { return c.ID }

// customerMatches reports whether the lower-cased text occurs in a
// searchable field of c.
func customerMatches(c *customer.Customer, text string) bool {
	fields := []string{c.ExternalID}
	for _, p := range []*string{c.Name, c.LegalName, c.Firstname, c.Lastname, c.Email} {
		if p != nil {
			fields = append(fields, *p)
		}
	}
	return slices.ContainsFunc(fields, func(s string) bool { return strings.Contains(strings.ToLower(s), text) })
}
