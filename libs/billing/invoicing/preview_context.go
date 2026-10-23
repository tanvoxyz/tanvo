package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CodeMustBePersisted rejects a preview of stored subscriptions for a
// customer that does not exist yet.
const CodeMustBePersisted = "must_be_persisted"

// PreviewCustomer holds the customer attributes of a [PreviewRequest]
// (lago PreviewContextService#find_or_build_customer); nil or zero fields
// keep the stored value, except ShippingAddress.
type PreviewCustomer struct {
	// Name replaces the name.
	Name *string
	// TaxIdentificationNumber replaces the tax identification number.
	TaxIdentificationNumber *string
	// Currency replaces the currency.
	Currency money.Currency
	// Timezone replaces the time zone.
	Timezone *time.Location
	// BillingAddress replaces the billing address fields it sets.
	BillingAddress PreviewAddress
	// ShippingAddress replaces the whole shipping address.
	ShippingAddress core.Address
	// Integrations link the customer to these integrations (anrok,
	// avalara, xero, hubspot or salesforce).
	Integrations []refs.Ref
}

// PreviewAddress holds the address fields a preview replaces; nil keeps
// the stored field.
type PreviewAddress struct {
	// Line1 is the first address line.
	Line1 *string
	// Line2 is the second address line.
	Line2 *string
	// City is the city or locality.
	City *string
	// State is the state, province or region.
	State *string
	// Zipcode is the postal code.
	Zipcode *string
	// Country is the ISO 3166-1 alpha-2 country code.
	Country *string
}

// previewProviders are the integrations a preview customer can be linked
// to (lago PreviewContextService#integration_type).
var previewProviders = []integration.Provider{
	integration.ProviderAnrok, integration.ProviderAvalara, integration.ProviderXero,
	integration.ProviderHubspot, integration.ProviderSalesforce,
}

// previewCustomer finds the customer of in, or builds one with the
// organization's default billing entity when in names none, applies the
// attributes of in.Customer and stores the result in the rolled-back
// transaction of r. built reports a customer that does not exist yet.
func (s *Service) previewCustomer(ctx context.Context, r port.Repos, in PreviewRequest) (c *customer.Customer, built bool, err error) {
	org, now := in.OrganizationID, s.d.Now()
	switch {
	case !in.CustomerID.IsZero():
		c, err = r.Customers.Get(ctx, org, in.CustomerID)
	case in.CustomerExternalID != "":
		c, err = r.Customers.GetByExternalID(ctx, org, in.CustomerExternalID)
	default:
		built = true
		if c, err = s.newPreviewCustomer(ctx, r, org, now); err != nil {
			return nil, false, err
		}
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, false, core.NotFound("customer")
	}
	if err != nil {
		return nil, false, fmt.Errorf("find preview customer: %w", err)
	}
	pc := in.Customer
	if pc == nil {
		pc = &PreviewCustomer{}
	}
	pc.apply(c)
	c.UpdatedAt = now
	if err := c.Validate(); err != nil {
		return nil, false, err
	}
	if built {
		err = r.Customers.Create(ctx, c)
	} else {
		err = r.Customers.Update(ctx, c)
	}
	if err != nil {
		return nil, false, fmt.Errorf("simulate preview customer: %w", err)
	}
	if err := s.previewIntegrations(ctx, r, c, pc.Integrations, now); err != nil {
		return nil, false, err
	}
	return c, built, nil
}

// newPreviewCustomer builds a customer of the organization's default
// billing entity; its external ID is generated (the view shows none).
func (s *Service) newPreviewCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, now time.Time) (*customer.Customer, error) {
	be, err := r.BillingEntities.GetDefault(ctx, org)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("billing_entity")
	}
	if err != nil {
		return nil, fmt.Errorf("load default billing entity: %w", err)
	}
	id := core.NewID[core.CustomerID](s.d.IDs)
	c := customer.New(org, id, previewPrefix+id.String(), be.ID, now)
	seq, err := r.Sequences.Next(ctx, org, port.SequenceKey{Name: port.SequenceCustomer})
	if err != nil {
		return nil, fmt.Errorf("next customer sequence: %w", err)
	}
	c.AssignSequence(seq, be.DocumentNumberPrefix)
	return c, nil
}

// apply sets the attributes of p on c. The shipping address is always
// replaced, even when p sets none of it, as lago assigns each of its
// fields from the request.
func (p *PreviewCustomer) apply(c *customer.Customer) {
	if p.Name != nil {
		c.Name = p.Name
	}
	if p.TaxIdentificationNumber != nil {
		c.TaxIdentificationNumber = p.TaxIdentificationNumber
	}
	if !p.Currency.IsZero() {
		c.Currency = p.Currency
	}
	if p.Timezone != nil {
		c.Timezone = p.Timezone
	}
	a := &c.BillingAddress
	for _, f := range []struct {
		dst *string
		src *string
	}{
		{&a.Line1, p.BillingAddress.Line1},
		{&a.Line2, p.BillingAddress.Line2},
		{&a.City, p.BillingAddress.City},
		{&a.State, p.BillingAddress.State},
		{&a.Zipcode, p.BillingAddress.Zipcode},
		{&a.Country, p.BillingAddress.Country},
	} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	c.ShippingAddress = p.ShippingAddress
}

// previewIntegrations links c to the integrations of rs in the
// rolled-back transaction of r (lago
// PreviewContextService#build_customer_integration); a category the
// customer is already linked in keeps its stored link.
func (s *Service) previewIntegrations(ctx context.Context, r port.Repos, c *customer.Customer, rs []refs.Ref, now time.Time) error {
	org := c.OrganizationID
	find := refs.Finder[core.IntegrationID, *integration.Integration]{
		Resource: "integration",
		Get: func(ctx context.Context, id core.IntegrationID) (*integration.Integration, error) {
			return r.Integrations.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*integration.Integration, error) {
			return r.Integrations.GetByCode(ctx, org, code)
		},
		ID: func(i *integration.Integration) core.IntegrationID { return i.ID },
	}
	for i, ref := range rs {
		in, err := find.One(ctx, ref)
		if err != nil {
			return err
		}
		if !slices.Contains(previewProviders, in.Provider) {
			return fieldError(fmt.Sprintf("customer.integration_customers[%d].integration_id", i), core.CodeForbidden)
		}
		links, err := r.IntegrationCustomers.ListByCustomer(ctx, org, c.ID, in.Category)
		if err != nil {
			return fmt.Errorf("list integration customers of customer %s: %w", c.ID, err)
		}
		if len(links) > 0 {
			continue
		}
		ic := &integration.IntegrationCustomer{
			ID:               core.NewID[core.IntegrationCustomerID](s.d.IDs),
			OrganizationID:   org,
			IntegrationID:    in.ID,
			CustomerID:       c.ID,
			Category:         in.Category,
			SyncWithProvider: true,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := r.IntegrationCustomers.Create(ctx, ic); err != nil {
			return fmt.Errorf("simulate integration customer of %s: %w", in.Code, err)
		}
	}
	return nil
}

// builtCoupon returns the coupon lago builds from attributes it finds no
// stored coupon for (Coupon.new with the column defaults: fixed amount,
// once, reusable, no expiration), named and coded after its ID when the
// attributes give no code; nil when the attributes describe no valid
// coupon, which then has no effect.
func (s *Service) builtCoupon(org core.OrganizationID, attrs *coupon.Coupon, now time.Time) *coupon.Coupon {
	c := *attrs
	c.ID, c.OrganizationID = core.NewID[core.CouponID](s.d.IDs), org
	if c.Type == "" {
		c.Type = coupon.TypeFixedAmount
	}
	if c.Frequency == "" {
		c.Frequency = coupon.FrequencyOnce
	}
	if c.Expiration == "" {
		c.Expiration = coupon.ExpirationNoExpiration
	}
	if c.Code == "" {
		c.Code = previewPrefix + c.ID.String()
	}
	if c.Name == "" {
		c.Name = c.Code
	}
	// The discount reads only the attributes of its type.
	if c.Type == coupon.TypePercentage {
		c.Amount, c.Currency = nil, money.Currency{}
	} else {
		c.PercentageRate = nil
	}
	c.LimitedPlanIDs, c.LimitedBillableMetricIDs = nil, nil
	c.Status, c.TerminatedAt, c.DeletedAt = coupon.StatusActive, nil, nil
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Validate(time.Time{}) != nil {
		return nil
	}
	return &c
}
