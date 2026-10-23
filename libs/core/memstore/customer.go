package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
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

// customerRepo implements customer.Repository; st is the whole
// transaction state, for the filters on subscriptions.
type customerRepo struct {
	d  *customerData
	st *state
}

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
	if err := f.Validate(); err != nil {
		return core.Page[*customer.Customer]{}, fmt.Errorf("list customers: %w", err)
	}
	search := strings.ToLower(core.SearchTerm(f.Search))
	if f.ExternalID != "" {
		search = ""
	}
	lo, hi, byCount := f.ActiveSubscriptionsRange()
	rows := r.d.customers.find(func(x *customer.Customer) bool {
		switch {
		case x.OrganizationID != org,
			!f.IncludeDeleted && x.Deleted(),
			f.ExternalID != "" && x.ExternalID != f.ExternalID,
			len(f.BillingEntityIDs) > 0 && !slices.Contains(f.BillingEntityIDs, x.BillingEntityID),
			len(f.AccountTypes) > 0 && !slices.Contains(f.AccountTypes, x.AccountType),
			f.CustomerType != "" && x.CustomerType != f.CustomerType,
			f.HasCustomerType != nil && (x.CustomerType != "") != *f.HasCustomerType,
			len(f.Countries) > 0 && !slices.Contains(f.Countries, x.BillingAddress.Country),
			len(f.States) > 0 && !slices.Contains(f.States, x.BillingAddress.State),
			len(f.Zipcodes) > 0 && !slices.Contains(f.Zipcodes, x.BillingAddress.Zipcode),
			len(f.Currencies) > 0 && !slices.ContainsFunc(f.Currencies, func(c money.Currency) bool { return !x.Currency.IsZero() && c.Code() == x.Currency.Code() }),
			f.HasTaxIdentificationNumber != nil && (x.TaxIdentificationNumber != nil) != *f.HasTaxIdentificationNumber,
			!f.Metadata.Matches(x.Metadata):
			return false
		}
		if byCount {
			n := r.st.subscription.subscriptions.count(func(s *subscription.Subscription) bool {
				return s.OrganizationID == org && s.CustomerID == x.ID && s.Status == subscription.StatusActive
			})
			if n < lo || n > hi {
				return false
			}
		}
		return search == "" || customerMatches(x, search)
	})
	if f.Order == core.OrderRecent {
		// created_at, newest first, then id ascending (base_query.rb:111-112).
		if page.Token != "" {
			c, err := core.DecodeCursor(page.Token)
			if err != nil {
				return core.Page[*customer.Customer]{}, err
			}
			if _, err := time.Parse(time.RFC3339Nano, c.Key); err != nil {
				return core.Page[*customer.Customer]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
			}
		}
		slices.SortStableFunc(rows, func(a, b *customer.Customer) int {
			if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
				return c
			}
			return a.ID.Compare(b.ID)
		})
		cursor := func(x *customer.Customer) core.Cursor {
			return core.Cursor{Key: x.CreatedAt.UTC().Format(time.RFC3339Nano), ID: x.ID.UUID()}
		}
		return paginateSorted(rows, cursor, func(x *customer.Customer, c core.Cursor) bool {
			at, _ := time.Parse(time.RFC3339Nano, c.Key)
			if !x.CreatedAt.Equal(at) {
				return x.CreatedAt.Before(at)
			}
			return x.ID.UUID().String() > c.ID.String()
		}, page)
	}
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
