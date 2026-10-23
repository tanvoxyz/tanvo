package kernel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// CustomerEnv is a customer with the settings that apply to its documents:
// its billing entity (or a subscription's override) and the resolved
// defaults of domain §4.2 and §22.12.
type CustomerEnv struct {
	// Customer is the customer.
	Customer *customer.Customer
	// BillingEntity is the issuing billing entity.
	BillingEntity *organization.BillingEntity
	// Defaults are the billing entity's settings the customer inherits.
	Defaults customer.Defaults
	// Location is the applicable time zone: customer → billing entity →
	// UTC (domain §22.1).
	Location *time.Location
}

// DefaultsOf returns the customer defaults a billing entity provides.
func DefaultsOf(be *organization.BillingEntity) customer.Defaults {
	return customer.Defaults{
		Timezone:                  be.Timezone,
		DocumentLocale:            be.DocumentLocale,
		InvoiceGracePeriodDays:    be.InvoiceGracePeriodDays,
		NetPaymentTermDays:        be.NetPaymentTermDays,
		FinalizeZeroAmountInvoice: be.FinalizeZeroAmountInvoice,
		IssuingDateAnchor:         be.IssuingDateAnchor,
		IssuingDateAdjustment:     be.IssuingDateAdjustment,
		InvoiceCustomSectionIDs:   be.InvoiceCustomSectionIDs,
	}
}

// NewCustomerEnv builds the environment of c issued by be.
func NewCustomerEnv(c *customer.Customer, be *organization.BillingEntity) *CustomerEnv {
	d := DefaultsOf(be)
	return &CustomerEnv{Customer: c, BillingEntity: be, Defaults: d, Location: c.ApplicableTimezone(d)}
}

// LoadCustomerEnv loads the customer id with the billing entity
// billingEntity, or the customer's own when billingEntity is zero.
func LoadCustomerEnv(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID, billingEntity core.BillingEntityID) (*CustomerEnv, error) {
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load customer %s: %w", id, err)
	}
	if billingEntity.IsZero() {
		billingEntity = c.BillingEntityID
	}
	var be *organization.BillingEntity
	if billingEntity.IsZero() {
		be, err = r.BillingEntities.GetDefault(ctx, org)
	} else {
		be, err = r.BillingEntities.Get(ctx, org, billingEntity)
	}
	if err != nil {
		return nil, fmt.Errorf("load billing entity of customer %s: %w", id, err)
	}
	return NewCustomerEnv(c, be), nil
}

// OrganizationID returns the customer's organization.
func (e *CustomerEnv) OrganizationID() core.OrganizationID { return e.Customer.OrganizationID }

// GracePeriodDays returns the applicable grace period.
func (e *CustomerEnv) GracePeriodDays() int { return e.Customer.ApplicableGracePeriodDays(e.Defaults) }

// NetPaymentTermDays returns the applicable payment term.
func (e *CustomerEnv) NetPaymentTermDays() int {
	return e.Customer.ApplicableNetPaymentTermDays(e.Defaults)
}

// FinalizeZeroAmountInvoice returns the resolved zero-amount finalization.
func (e *CustomerEnv) FinalizeZeroAmountInvoice() bool {
	return e.Customer.ApplicableFinalizeZeroAmountInvoice(e.Defaults)
}

// IssuingDateAnchor returns the applicable issuing date anchor.
func (e *CustomerEnv) IssuingDateAnchor() core.IssuingDateAnchor {
	return e.Customer.ApplicableIssuingDateAnchor(e.Defaults)
}

// IssuingDateAdjustment returns the applicable issuing date adjustment.
func (e *CustomerEnv) IssuingDateAdjustment() core.IssuingDateAdjustment {
	return e.Customer.ApplicableIssuingDateAdjustment(e.Defaults)
}

// Today returns the local date of now in the applicable time zone.
func (e *CustomerEnv) Today(now time.Time) core.Date { return core.DateOf(now, e.Location) }

// Currency returns the customer's currency, else the billing entity's
// default currency.
func (e *CustomerEnv) Currency() money.Currency {
	if !e.Customer.Currency.IsZero() {
		return e.Customer.Currency
	}
	return e.BillingEntity.DefaultCurrency
}

// SubscriptionEnv is a subscription with what its billing needs: the plan,
// the customer environment, the subscription chain and how it is replaced.
type SubscriptionEnv struct {
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// Plan is the subscription's plan (an override plan when overridden).
	Plan *catalog.Plan
	// Customer is the customer environment, with the subscription's
	// billing entity.
	Customer *CustomerEnv
	// Chain is the subscription and its predecessors, oldest first.
	Chain []*subscription.Subscription
	// InitialStartedAt is the earliest started_at of the chain and of the
	// customer's other subscriptions with the subscription's external ID,
	// or zero when none started; see [SubscriptionEnv.ChainStart].
	InitialStartedAt time.Time
	// Previous is the subscription this one replaced, or nil.
	Previous *subscription.Subscription
	// Next is the subscription replacing this one (a pending downgrade, or
	// the subscription created by an upgrade), or nil.
	Next *subscription.Subscription
	// NextPlan is Next's plan, or nil.
	NextPlan *catalog.Plan
	// Change is how Next replaces the subscription.
	Change period.Change
	// LastInvoiceSubscription is the latest invoice subscription of the
	// subscription, or nil.
	LastInvoiceSubscription *invoice.Subscription
}

// LoadSubscriptionEnv loads the subscription id with its environment.
func LoadSubscriptionEnv(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID) (*SubscriptionEnv, error) {
	s, err := r.Subscriptions.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", id, err)
	}
	return LoadSubscriptionEnvOf(ctx, r, s)
}

// LoadSubscriptionEnvOf loads the environment of the subscription s.
func LoadSubscriptionEnvOf(ctx context.Context, r port.Repos, s *subscription.Subscription) (*SubscriptionEnv, error) {
	org := s.OrganizationID
	plan, err := r.Plans.Get(ctx, org, s.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan of subscription %s: %w", s.ID, err)
	}
	cust, err := r.Customers.Get(ctx, org, s.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("load customer of subscription %s: %w", s.ID, err)
	}
	cenv, err := LoadCustomerEnv(ctx, r, org, cust.ID, s.ApplicableBillingEntity(cust.BillingEntityID))
	if err != nil {
		return nil, err
	}
	chain, err := r.Subscriptions.Chain(ctx, org, s.ID)
	if err != nil {
		return nil, fmt.Errorf("load chain of subscription %s: %w", s.ID, err)
	}
	initial, err := initialStartedAt(ctx, r, s, chain)
	if err != nil {
		return nil, err
	}
	env := &SubscriptionEnv{Subscription: s, Plan: plan, Customer: cenv, Chain: chain, InitialStartedAt: initial}
	if !s.PreviousSubscriptionID.IsZero() {
		for _, c := range chain {
			if c.ID == s.PreviousSubscriptionID {
				env.Previous = c
			}
		}
	}
	next, err := nextSubscription(ctx, r, s)
	if err != nil {
		return nil, err
	}
	if next != nil {
		np, err := r.Plans.Get(ctx, org, next.PlanID)
		if err != nil {
			return nil, fmt.Errorf("load plan of next subscription %s: %w", next.ID, err)
		}
		env.Next, env.NextPlan, env.Change = next, np, PlanChange(plan, np)
	}
	last, err := r.InvoiceSubscriptions.Last(ctx, org, s.ID)
	switch {
	case err == nil:
		env.LastInvoiceSubscription = last
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("load last invoice of subscription %s: %w", s.ID, err)
	}
	return env, nil
}

// nextSubscription returns the subscription replacing s: the most recent
// non-canceled subscription pointing to it.
func nextSubscription(ctx context.Context, r port.Repos, s *subscription.Subscription) (*subscription.Subscription, error) {
	all, err := r.Subscriptions.ListByCustomer(ctx, s.OrganizationID, s.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("load next subscription of %s: %w", s.ID, err)
	}
	var next []*subscription.Subscription
	for _, c := range all {
		if c.PreviousSubscriptionID == s.ID {
			next = append(next, c)
		}
	}
	return subscription.NextSubscription(next), nil
}

// PlanChange compares two plans by yearly amount (domain §22.13): a new
// plan worth at least as much is an upgrade; the same plan is no change.
func PlanChange(from, to *catalog.Plan) period.Change {
	if from.ID == to.ID {
		return period.NoChange
	}
	c := period.Compare(from.Interval, money.NewFromInt(from.Amount), to.Interval, money.NewFromInt(to.Amount))
	if c == period.NoChange {
		return period.Upgrade
	}
	return c
}

// Location returns the applicable time zone of the subscription.
func (e *SubscriptionEnv) Location() *time.Location { return e.Customer.Location }

// OrganizationID returns the subscription's organization.
func (e *SubscriptionEnv) OrganizationID() core.OrganizationID {
	return e.Subscription.OrganizationID
}

// ChainStart returns the instant the subscription's trial starts from
// (domain §9.4): [SubscriptionEnv.InitialStartedAt], else the started_at
// of the first started subscription of the chain.
func (e *SubscriptionEnv) ChainStart() time.Time {
	if !e.InitialStartedAt.IsZero() {
		return e.InitialStartedAt
	}
	return subscription.ChainStart(e.Chain)
}

// initialStartedAt returns the earliest started_at of chain and of the
// customer's subscriptions sharing s's external ID. A subscription
// recreated with the external ID of a terminated one keeps its trial
// start, so it gets no fresh trial (lago Subscription#initial_started_at,
// app/models/subscription.rb:208, and Subscriptions::FreeTrialBillingService,
// app/services/subscriptions/free_trial_billing_service.rb:85).
func initialStartedAt(ctx context.Context, r port.Repos, s *subscription.Subscription, chain []*subscription.Subscription) (time.Time, error) {
	first := subscription.ChainStart(chain)
	t, err := r.Subscriptions.InitialStartedAt(ctx, s.OrganizationID, s.CustomerID, s.ExternalID)
	if err != nil {
		return time.Time{}, fmt.Errorf("load initial start of subscription %s: %w", s.ID, err)
	}
	if t != nil && (first.IsZero() || t.Before(first)) {
		first = *t
	}
	return first, nil
}

// Period returns the libs/period view of the subscription, with the
// boundaries of its last invoice so a time zone change is bridged
// (domain §22.1).
func (e *SubscriptionEnv) Period() period.Subscription {
	ps := e.Subscription.PeriodSubscription(e.Plan.PeriodPlan(), e.Location(), e.ChainStart(), e.Change)
	if last := e.LastInvoiceSubscription; last != nil && !last.ChargesPeriod.IsZero() {
		ps.Previous = &period.PreviousInvoice{
			Location:       previousLocation(last, e.Location()),
			ChargesTo:      last.ChargesPeriod.To,
			FixedChargesTo: last.FixedChargesPeriod.To,
		}
	}
	return ps
}

// previousLocation guesses the time zone a previous invoice was computed
// in: libs/core does not record it, so a recurring invoice whose charges
// period does not end at a local midnight of loc is taken as computed in
// another zone.
func previousLocation(last *invoice.Subscription, loc *time.Location) *time.Location {
	to := last.ChargesPeriod.To.In(loc)
	if !last.Recurring || (to.Hour() == 0 && to.Minute() == 0 && to.Second() == 0 && to.Nanosecond() == 0) {
		return loc
	}
	return time.FixedZone("previous", 0)
}

// Dates resolves the periods of a run at billingAt.
func (e *SubscriptionEnv) Dates(billingAt time.Time, mode period.Mode) (*period.Dates, error) {
	d, err := e.Period().Dates(billingAt, mode)
	if err != nil {
		return nil, fmt.Errorf("dates of subscription %s: %w", e.Subscription.ID, err)
	}
	return d, nil
}

// Charges returns the plan's charges.
func (e *SubscriptionEnv) Charges(ctx context.Context, r port.Repos) ([]*catalog.Charge, error) {
	cs, err := r.Charges.ListByPlan(ctx, e.OrganizationID(), e.Plan.ID)
	if err != nil {
		return nil, fmt.Errorf("load charges of plan %s: %w", e.Plan.ID, err)
	}
	return cs, nil
}

// FixedCharges returns the plan's fixed charges.
func (e *SubscriptionEnv) FixedCharges(ctx context.Context, r port.Repos) ([]*catalog.FixedCharge, error) {
	fs, err := r.FixedCharges.ListByPlan(ctx, e.OrganizationID(), e.Plan.ID)
	if err != nil {
		return nil, fmt.Errorf("load fixed charges of plan %s: %w", e.Plan.ID, err)
	}
	return fs, nil
}
