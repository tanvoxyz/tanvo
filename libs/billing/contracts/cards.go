package contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CardInput is the request of [Service.CreateCard].
type CardInput struct {
	// RateCardCode is the code of the applied rate card.
	RateCardCode string
	// Units is the quantity billed, for fixed products.
	Units *money.Decimal
	// BillingAnchorDate is the card's own billing anchor; nil inherits
	// the contract's.
	BillingAnchorDate *core.Date
	// Phases, when not nil, are the card's phases; nil gives it the single
	// indefinite phase coded "default".
	Phases *[]products.PhaseInput
}

// CardUpdate is the request of [Service.UpdateCard]: only the set fields
// change.
type CardUpdate struct {
	// Units is the quantity billed; nil bills none.
	Units core.Optional[*money.Decimal]
	// BillingAnchorDate is the card's billing anchor; it cannot be
	// cleared.
	BillingAnchorDate core.Optional[*core.Date]
}

// findCard returns the kept card of the live contract with the external
// ID applying the rate card with the code (lago
// ContractRateCardsController#find_contract_rate_card); a missing contract
// or card is core.NotFound("applied_rate_card").
func findCard(ctx context.Context, r port.Repos, org core.OrganizationID, externalID, code string) (*subscription.Subscription, *subscription.RateCard, error) {
	c, err := live(ctx, r, org, externalID)
	if err != nil {
		return nil, nil, core.NotFound(ResourceAppliedRateCard)
	}
	rc, err := r.RateCards.GetByCode(ctx, org, code)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil, core.NotFound(ResourceAppliedRateCard)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load rate card %q: %w", code, err)
	}
	card, err := r.ContractRateCards.GetByRateCard(ctx, org, c.ID, rc.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil, core.NotFound(ResourceAppliedRateCard)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load contract rate card: %w", err)
	}
	return c, card, nil
}

// Card returns the card of the live contract with the external ID
// applying the rate card with the code.
//
// source: app/controllers/api/v2/contract_rate_cards_controller.rb:23-29
func (s *Service) Card(ctx context.Context, org core.OrganizationID, externalID, code string) (*CardView, error) {
	var out *CardView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_, card, err := findCard(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		out, err = cardView(ctx, r, card)
		return err
	})
	return out, err
}

// ListCards returns the kept cards of the live contract with the external
// ID by effective date.
//
// source: app/controllers/api/v2/contract_rate_cards_controller.rb:59-82
func (s *Service) ListCards(ctx context.Context, org core.OrganizationID, externalID string, page core.PageRequest) (core.Page[*CardView], error) {
	var out core.Page[*CardView]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := live(ctx, r, org, externalID)
		if err != nil {
			return err
		}
		p, err := r.ContractRateCards.List(ctx, org, subscription.RateCardQuery{SubscriptionID: c.ID}, page)
		if err != nil {
			return fmt.Errorf("list rate cards of %s: %w", c.ID, err)
		}
		out = core.Page[*CardView]{NextToken: p.NextToken, Items: make([]*CardView, 0, len(p.Items))}
		for _, card := range p.Items {
			v, err := cardView(ctx, r, card)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

// CreateCard applies a rate card to the pending live contract with the
// external ID (lago ContractRateCards::CreateService): in the contract's
// currency, at most one card per product slice, with its phases, seeded on
// its anchor.
//
// source: app/services/contract_rate_cards/create_service.rb:13-62
func (s *Service) CreateCard(ctx context.Context, org core.OrganizationID, externalID string, in CardInput) (*CardView, error) {
	var out *CardView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := live(ctx, r, org, externalID)
		if err != nil {
			return err
		}
		if !c.Editable() {
			return contractLocked()
		}
		rc, err := r.RateCards.GetByCode(ctx, org, in.RateCardCode)
		if errors.Is(err, core.ErrNotFound) {
			return core.NotFound(ResourceRateCard)
		}
		if err != nil {
			return fmt.Errorf("load rate card %q: %w", in.RateCardCode, err)
		}
		cenv, err := customerEnv(ctx, r, c)
		if err != nil {
			return err
		}
		plan, err := planOf(ctx, r, c)
		if err != nil {
			return err
		}
		if rc.Currency != currencyOf(plan, cenv) {
			return fieldError("currency", CodeCurrencyDoesNotMatch)
		}
		if c, err = lockContract(ctx, r, c); err != nil {
			return err
		}
		if !c.Editable() {
			return contractLocked()
		}
		if priced, err := slicePriced(ctx, r, c, rc); err != nil {
			return err
		} else if priced {
			code := catalog.CodeProductAlreadyPriced
			if !rc.ProductFilterID.IsZero() {
				code = catalog.CodeProductFilterAlreadyPriced
			}
			return fieldError("rate_card", code)
		}
		now := s.d.Now()
		card := &subscription.RateCard{
			ID: core.NewID[core.SubscriptionRateCardID](s.d.IDs), OrganizationID: org, SubscriptionID: c.ID,
			RateCardID: rc.ID, Units: in.Units, CreatedAt: now, UpdatedAt: now,
		}
		loc := cenv.Location
		defaultLifecycle(card, c, loc, in.BillingAnchorDate)
		phases, err := s.phases.BuildPhasesOf(ctx, r, org, rc.ID, func(ph *catalog.RatePhase) { ph.SubscriptionRateCardID = card.ID }, in.Phases)
		if err != nil {
			return err
		}
		if err := s.createCard(ctx, r, card, phases); err != nil {
			return err
		}
		if err := s.seedLifecycle(ctx, r, c, loc, card, in.BillingAnchorDate, now); err != nil {
			return err
		}
		out, err = cardView(ctx, r, card)
		return err
	})
	return out, err
}

// UpdateCard changes the units or the anchor of a card of the pending
// live contract with the external ID (lago
// ContractRateCards::UpdateService); a moved anchor reseeds the card.
//
// source: app/services/contract_rate_cards/update_service.rb:13-38
func (s *Service) UpdateCard(ctx context.Context, org core.OrganizationID, externalID, code string, in CardUpdate) (*CardView, error) {
	var out *CardView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, card, err := findCard(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		if in.BillingAnchorDate.Set && (in.BillingAnchorDate.Value == nil || in.BillingAnchorDate.Value.IsZero()) {
			return fieldError("billing_anchor_date", CodeValueIsInvalid)
		}
		if c, err = lockContract(ctx, r, c); err != nil {
			return err
		}
		if !c.Editable() {
			return contractLocked()
		}
		now := s.d.Now()
		in.Units.Apply(&card.Units)
		moved := false
		if a := in.BillingAnchorDate; a.Set && *a.Value != card.BillingAnchorDate {
			card.BillingAnchorDate = *a.Value
			moved = true
		}
		if err := s.saveCard(ctx, r, card, now); err != nil {
			return err
		}
		if moved {
			cenv, err := customerEnv(ctx, r, c)
			if err != nil {
				return err
			}
			anchor := card.BillingAnchorDate
			if err := s.seedLifecycle(ctx, r, c, cenv.Location, card, &anchor, now); err != nil {
				return err
			}
		}
		out, err = cardView(ctx, r, card)
		return err
	})
	return out, err
}

// DeleteCard discards a card of the pending live contract with the
// external ID and its phases (lago ContractRateCards::DestroyService).
//
// source: app/services/contract_rate_cards/destroy_service.rb:13-31
func (s *Service) DeleteCard(ctx context.Context, org core.OrganizationID, externalID, code string) (*CardView, error) {
	var out *CardView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, card, err := findCard(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		if out, err = cardView(ctx, r, card); err != nil {
			return err
		}
		return s.discardCard(ctx, r, c, card, s.d.Now())
	})
	return out, err
}

// discardCard discards card of contract c and its phases, under the
// contract's lock, while c is editable.
func (s *Service) discardCard(ctx context.Context, r port.Repos, c *subscription.Subscription, card *subscription.RateCard, now time.Time) error {
	c, err := lockContract(ctx, r, c)
	if err != nil {
		return err
	}
	if !c.Editable() {
		return contractLocked()
	}
	if err := r.Locks.Lock(ctx, c.OrganizationID, cardLock(card.ID)); err != nil {
		return fmt.Errorf("lock contract rate card %s: %w", card.ID, err)
	}
	if err := r.ContractRateCards.Discard(ctx, c.OrganizationID, card.ID, now); err != nil {
		return fmt.Errorf("discard contract rate card %s: %w", card.ID, err)
	}
	return nil
}

// planOf returns contract c's plan, or nil.
func planOf(ctx context.Context, r port.Repos, c *subscription.Subscription) (*catalog.Plan, error) {
	if c.PlanID.IsZero() {
		return nil, nil
	}
	p, err := r.Plans.Get(ctx, c.OrganizationID, c.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", c.PlanID, err)
	}
	return p, nil
}

// slicePriced reports whether a kept card of contract c already prices
// the product slice (product and filter) of rc.
//
// source: app/services/contract_rate_cards/create_service.rb:78-87
func slicePriced(ctx context.Context, r port.Repos, c *subscription.Subscription, rc *catalog.RateCard) (bool, error) {
	cards, err := kernel.All(func(page core.PageRequest) (core.Page[*subscription.RateCard], error) {
		return r.ContractRateCards.List(ctx, c.OrganizationID, subscription.RateCardQuery{SubscriptionID: c.ID, ProductID: rc.ProductID}, page)
	})
	if err != nil {
		return false, fmt.Errorf("list rate cards of %s: %w", c.ID, err)
	}
	for _, card := range cards {
		other, err := r.RateCards.Get(ctx, c.OrganizationID, card.RateCardID)
		if err != nil {
			return false, fmt.Errorf("load rate card %s: %w", card.RateCardID, err)
		}
		if other.ProductFilterID == rc.ProductFilterID {
			return true, nil
		}
	}
	return false, nil
}

// phaseOwner returns the owner of the phases of a card of the live
// contract with the external ID; its guard fails with rate_phase
// contract_locked once the contract is no longer pending (lago
// ContractRateCard#edit_error_code).
//
// source: app/models/contract_rate_card.rb:53-55
func phaseOwner(ctx context.Context, r port.Repos, org core.OrganizationID, externalID, code string) (products.PhaseOwner, error) {
	c, card, err := findCard(ctx, r, org, externalID, code)
	if err != nil {
		return products.PhaseOwner{}, err
	}
	return products.PhaseOwner{
		Lock: cardLock(card.ID),
		Guard: func(ctx context.Context, r port.Repos) error {
			fresh, err := r.Subscriptions.Get(ctx, org, c.ID)
			if err != nil {
				return fmt.Errorf("reload contract %s: %w", c.ID, err)
			}
			if !fresh.Editable() {
				return fieldError("rate_phase", subscription.CodeContractLocked)
			}
			return nil
		},
		RateCardID: card.RateCardID,
		List: func(ctx context.Context, r port.Repos) ([]*catalog.RatePhase, error) {
			return r.ContractRateCards.ListPhases(ctx, org, card.ID)
		},
		Save: func(ctx context.Context, r port.Repos, phases []*catalog.RatePhase, at time.Time) error {
			return r.ContractRateCards.SavePhases(ctx, org, card.ID, phases, at)
		},
		Own: func(ph *catalog.RatePhase) { ph.SubscriptionRateCardID = card.ID },
	}, nil
}

// ListPhases returns the phases of a card of the live contract with the
// external ID by position.
//
// source: app/controllers/api/v2/contract_rate_cards/rate_phases_controller.rb:7-11
func (s *Service) ListPhases(ctx context.Context, org core.OrganizationID, externalID, code string) ([]*catalog.RatePhase, error) {
	var out []*catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := phaseOwner(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		out, err = o.List(ctx, r)
		return err
	})
	return out, err
}

// CreatePhase inserts a phase into the ramp of a card of the live
// contract with the external ID (products.Service.CreatePhaseOf).
//
// source: app/services/rate_phases/create_service.rb:17-80
func (s *Service) CreatePhase(ctx context.Context, org core.OrganizationID, externalID, code string, in products.PhaseInput) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := phaseOwner(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		out, err = s.phases.CreatePhaseOf(ctx, r, org, o, in)
		return err
	})
	return out, err
}

// UpdatePhase changes a phase of a card of the live contract with the
// external ID (products.Service.UpdatePhaseOf).
//
// source: app/services/rate_phases/update_service.rb:15-80
func (s *Service) UpdatePhase(ctx context.Context, org core.OrganizationID, externalID, code string, phase refs.Ref, in products.PhaseUpdate) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := phaseOwner(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		out, err = s.phases.UpdatePhaseOf(ctx, r, org, o, phase, in)
		return err
	})
	return out, err
}

// DeletePhase removes a phase of a card of the live contract with the
// external ID (products.Service.DeletePhaseOf).
//
// source: app/services/rate_phases/destroy_service.rb:13-50
func (s *Service) DeletePhase(ctx context.Context, org core.OrganizationID, externalID, code string, phase refs.Ref) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := phaseOwner(ctx, r, org, externalID, code)
		if err != nil {
			return err
		}
		out, err = s.phases.DeletePhaseOf(ctx, r, org, o, phase)
		return err
	})
	return out, err
}
