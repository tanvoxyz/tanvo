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
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// customerData holds the tables of package customer.
type customerData struct {
	customers *table[core.CustomerID, customer.Customer]
	// walletRefresh holds the wallet refresh flags of the customers.
	walletRefresh *table[core.CustomerID, walletRefresh]
	// pendingVIES holds the pending VIES checks of the customers.
	pendingVIES *table[core.CustomerID, customer.PendingVIESCheck]
}

// walletRefresh is the wallet refresh state of a customer.
type walletRefresh struct {
	awaiting   bool
	generation int64
	claimed    int64
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
		walletRefresh: newTable[core.CustomerID, walletRefresh](nil),
		pendingVIES:   newTable[core.CustomerID, customer.PendingVIESCheck](nil),
	}
}

func (d *customerData) snapshot() *customerData {
	return &customerData{customers: d.customers.snapshot(), walletRefresh: d.walletRefresh.snapshot(), pendingVIES: d.pendingVIES.snapshot()}
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

// hasActiveWallet reports whether customer id of org has an active wallet.
func (r customerRepo) hasActiveWallet(org core.OrganizationID, id core.CustomerID) bool {
	return r.st.wallet.wallets.any(func(w *wallet.Wallet) bool {
		return w.OrganizationID == org && w.CustomerID == id && w.Status == wallet.StatusActive
	})
}

func (r customerRepo) FlagWalletRefresh(_ context.Context, org core.OrganizationID, id core.CustomerID) error {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org || !r.hasActiveWallet(org, id) {
		return nil
	}
	f, _ := r.d.walletRefresh.get(id)
	if f == nil {
		f = &walletRefresh{}
	}
	if f.awaiting && f.claimed != f.generation {
		return nil
	}
	r.d.walletRefresh.put(id, walletRefresh{awaiting: true, generation: f.generation + 1, claimed: f.claimed})
	return nil
}

func (r customerRepo) ClaimWalletRefresh(_ context.Context, org core.OrganizationID, id core.CustomerID) (int64, error) {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org {
		return 0, notFound("customer", id)
	}
	f, ok := r.d.walletRefresh.get(id)
	if !ok {
		return 0, nil
	}
	r.d.walletRefresh.put(id, walletRefresh{awaiting: f.awaiting, generation: f.generation, claimed: f.generation})
	return f.generation, nil
}

func (r customerRepo) ClearWalletRefresh(_ context.Context, org core.OrganizationID, id core.CustomerID, gen int64) error {
	c, ok := r.d.customers.get(id)
	if !ok || c.OrganizationID != org {
		return nil
	}
	if f, ok := r.d.walletRefresh.get(id); ok && f.generation == gen {
		r.d.walletRefresh.put(id, walletRefresh{generation: gen, claimed: f.claimed})
	}
	return nil
}

func (r customerRepo) ListAwaitingWalletRefresh(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[core.CustomerID], error) {
	rows := r.d.customers.find(func(x *customer.Customer) bool {
		if x.OrganizationID != org || x.Deleted() {
			return false
		}
		f, ok := r.d.walletRefresh.get(x.ID)
		return ok && f.awaiting && r.hasActiveWallet(org, x.ID)
	})
	p, err := paginate(rows, customerKey, page)
	if err != nil {
		return core.Page[core.CustomerID]{}, err
	}
	ids := make([]core.CustomerID, len(p.Items))
	for i, c := range p.Items {
		ids[i] = c.ID
	}
	return core.Page[core.CustomerID]{Items: ids, NextToken: p.NextToken}, nil
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
