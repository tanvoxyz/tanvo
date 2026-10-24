package contracts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Resource names of the not-found errors (lago's).
const (
	// Resource is the contract.
	Resource = "contract"
	// ResourceAppliedRateCard is a rate card applied to a contract.
	ResourceAppliedRateCard = "applied_rate_card"
	// ResourceCustomer is the contract's customer.
	ResourceCustomer = "customer"
	// ResourcePlan is the plan seeding the contract's rate cards.
	ResourcePlan = "plan"
	// ResourceRateCard is a rate card of the catalog.
	ResourceRateCard = "rate_card"
	// ResourceRate is a rate of a rate card.
	ResourceRate = "rate"
	// ResourceBillingEntity is the contract's billing entity.
	ResourceBillingEntity = "billing_entity"
	// ResourcePaymentMethod is the contract's payment method.
	ResourcePaymentMethod = "payment_method"
	// ResourcePricingUnit is a rate card's pricing unit.
	ResourcePricingUnit = "pricing_unit"
)

// Field error codes of the service (lago's).
const (
	// CodeValueAlreadyExists: a live contract has the external ID.
	CodeValueAlreadyExists = "value_already_exists"
	// CodeValueIsInvalid: a date does not parse or is cleared where it
	// cannot be.
	CodeValueIsInvalid = "value_is_invalid"
	// CodeCurrencyDoesNotMatch: the rate card's currency is not the
	// contract's.
	CodeCurrencyDoesNotMatch = "currency_does_not_match"
	// CodeInvalidPaymentMethod: the payment method type and the payment
	// method do not go together.
	CodeInvalidPaymentMethod = "invalid_payment_method"
)

// Service is the contract API service.
type Service struct {
	d      *kernel.Deps
	phases *products.Service
}

// New returns the service over d (it uses d.Tx, d.Clock and d.IDs),
// authoring the phases of contract rate cards through the product
// catalog's phase operations.
func New(d *kernel.Deps) (*Service, error) {
	p, err := products.New(d)
	if err != nil {
		return nil, err
	}
	return &Service{d: d, phases: p}, nil
}

// View is a contract with what its API representation shows.
type View struct {
	// Contract is the contract.
	Contract *subscription.Subscription
	// Customer is its customer.
	Customer *customer.Customer
	// Plan is the plan that seeded its rate cards, or nil.
	Plan *catalog.Plan
	// Currency is the currency its fees bill in.
	Currency money.Currency
	// RateCards are its kept rate cards by effective date.
	RateCards []*CardView
	// EffectiveBillingAnchorDate is its billing anchor in the customer's
	// applicable time zone (lago Contract#effective_billing_anchor_date).
	EffectiveBillingAnchorDate core.Date
}

// CardView is a rate card applied to a contract, with its rate card and
// phases.
type CardView struct {
	// Card is the contract rate card.
	Card *subscription.RateCard
	// RateCard is the applied rate card.
	RateCard *catalog.RateCard
	// Phases are its kept phases by position.
	Phases []*catalog.RatePhase
}

// fieldError returns the validation error of one field.
func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}

// contractLocked is the error of an edit the contract's status forbids.
func contractLocked() error { return fieldError("contract", subscription.CodeContractLocked) }

// lockContract takes the contract's lock (lago contract.lock!) and
// returns the contract as stored once it is held.
func lockContract(ctx context.Context, r port.Repos, c *subscription.Subscription) (*subscription.Subscription, error) {
	if err := kernel.LockSubscription(ctx, r, c.OrganizationID, c.ID); err != nil {
		return nil, err
	}
	fresh, err := r.Subscriptions.Get(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("reload contract %s: %w", c.ID, err)
	}
	return fresh, nil
}

// cardLock is the lock of a contract rate card's phases (lago
// applied_rate_card.with_lock).
func cardLock(id core.SubscriptionRateCardID) port.LockKey {
	return port.LockKey{Scope: "subscription_rate_card", ID: id.UUID()}
}

// findCustomer returns the kept customer with the external ID.
func findCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string) (*customer.Customer, error) {
	c, err := r.Customers.GetByExternalID(ctx, org, strings.TrimSpace(externalID))
	if err == nil && c.Deleted() {
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(ResourceCustomer)
	}
	if err != nil {
		return nil, fmt.Errorf("load customer: %w", err)
	}
	return c, nil
}

// findPlan returns the kept original plan with the code.
func findPlan(ctx context.Context, r port.Repos, org core.OrganizationID, code string) (*catalog.Plan, error) {
	p, err := r.Plans.GetByCode(ctx, org, code)
	if err == nil && (!p.ParentID.IsZero() || p.Deleted()) {
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(ResourcePlan)
	}
	if err != nil {
		return nil, fmt.Errorf("load plan %q: %w", code, err)
	}
	return p, nil
}

// live returns the live contract with the external ID (lago
// Contract.live_by_external_id), or core.NotFound("contract").
func live(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	c, err := r.Contracts.GetLive(ctx, org, externalID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(Resource)
	}
	if err != nil {
		return nil, fmt.Errorf("load contract %q: %w", externalID, err)
	}
	return c, nil
}

// Get returns the contract with the external ID: the live one when status
// is empty, else the latest started one in status, active for a status
// lago does not know (Api::V2::ContractsController#show).
//
// source: app/controllers/api/v2/contracts_controller.rb:80-98
func (s *Service) Get(ctx context.Context, org core.OrganizationID, externalID string, status subscription.Status) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var (
			c   *subscription.Subscription
			err error
		)
		if status == "" {
			c, err = live(ctx, r, org, externalID)
		} else {
			if !status.Valid() || status == subscription.StatusIncomplete {
				status = subscription.StatusActive
			}
			c, err = r.Contracts.GetByStatus(ctx, org, externalID, status)
			if errors.Is(err, core.ErrNotFound) {
				err = core.NotFound(Resource)
			}
		}
		if err != nil {
			return err
		}
		out, err = view(ctx, r, c)
		return err
	})
	return out, err
}

// List returns the contracts matching f, the active ones when f names no
// status (Api::V2::ContractsController#index).
//
// source: app/controllers/api/v2/contracts_controller.rb:47-78
func (s *Service) List(ctx context.Context, org core.OrganizationID, f subscription.ContractFilter, page core.PageRequest) (core.Page[*View], error) {
	if len(f.Statuses) == 0 {
		f.Statuses = []subscription.Status{subscription.StatusActive}
	}
	var out core.Page[*View]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Contracts.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list contracts: %w", err)
		}
		out = core.Page[*View]{NextToken: p.NextToken, Items: make([]*View, 0, len(p.Items))}
		for _, c := range p.Items {
			v, err := view(ctx, r, c)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

// view loads the view of contract c.
func view(ctx context.Context, r port.Repos, c *subscription.Subscription) (*View, error) {
	org := c.OrganizationID
	cenv, err := customerEnv(ctx, r, c)
	if err != nil {
		return nil, err
	}
	v := &View{Contract: c, Customer: cenv.Customer, EffectiveBillingAnchorDate: c.EffectiveBillingAnchorDate(cenv.Location)}
	if !c.PlanID.IsZero() {
		if v.Plan, err = r.Plans.Get(ctx, org, c.PlanID); err != nil {
			return nil, fmt.Errorf("load plan %s: %w", c.PlanID, err)
		}
	}
	v.Currency = currencyOf(v.Plan, cenv)
	cards, err := r.ContractRateCards.ListBySubscription(ctx, org, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list rate cards of %s: %w", c.ID, err)
	}
	for _, card := range cards {
		cv, err := cardView(ctx, r, card)
		if err != nil {
			return nil, err
		}
		v.RateCards = append(v.RateCards, cv)
	}
	return v, nil
}

// cardView loads the view of the contract rate card.
func cardView(ctx context.Context, r port.Repos, card *subscription.RateCard) (*CardView, error) {
	rc, err := r.RateCards.Get(ctx, card.OrganizationID, card.RateCardID)
	if err != nil {
		return nil, fmt.Errorf("load rate card %s: %w", card.RateCardID, err)
	}
	phases, err := r.ContractRateCards.ListPhases(ctx, card.OrganizationID, card.ID)
	if err != nil {
		return nil, fmt.Errorf("list phases of %s: %w", card.ID, err)
	}
	return &CardView{Card: card, RateCard: rc, Phases: phases}, nil
}

// currencyOf returns the currency a contract bills in: its plan's, else
// its customer's, else the billing entity's default (lago
// Contract#currency).
//
// source: app/models/contract.rb:109-113
func currencyOf(p *catalog.Plan, cenv *kernel.CustomerEnv) money.Currency {
	if p != nil {
		return p.Currency
	}
	return cenv.Currency()
}

// customerEnv loads the contract's customer with its own billing entity,
// whose time zone is the customer's applicable one (lago
// Customer#applicable_timezone), whatever entity the contract bills
// through.
func customerEnv(ctx context.Context, r port.Repos, c *subscription.Subscription) (*kernel.CustomerEnv, error) {
	return kernel.LoadCustomerEnv(ctx, r, c.OrganizationID, c.CustomerID, core.BillingEntityID{})
}
