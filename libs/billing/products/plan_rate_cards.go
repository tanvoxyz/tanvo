package products

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// PlanRateCardInput is a rate card to apply to a plan.
type PlanRateCardInput struct {
	// RateCard references the applied card.
	RateCard refs.Ref
	// Units is the quantity billed, for fixed products; nil when unset.
	Units *money.Decimal
	// Phases are the ramp; nil starts on a single indefinite phase coded
	// "default", an empty list is rate_phases value_is_mandatory.
	Phases *[]PhaseInput
}

// CreatePlanRateCard applies a rate card to the plan. A plan a
// contract uses is plan plan_locked; a missing card core.NotFound
// ("rate_card"); a card in another currency than the plan's currency
// currency_does_not_match; a plan pricing the card's product (or filter)
// already rate_card product_already_priced (product_filter_already_priced).
//
// source: app/services/plan_rate_cards/create_service.rb:13-62
func (s *Service) CreatePlanRateCard(ctx context.Context, org core.OrganizationID, planRef refs.Ref, in PlanRateCardInput) (*catalog.PlanRateCard, error) {
	var out *catalog.PlanRateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		plan, err := refs.Plans(r, org).One(ctx, planRef)
		if err != nil {
			return err
		}
		if err := planGuard(ctx, r, org, plan.ID, "plan"); err != nil {
			return err
		}
		c, err := rateCardsOf(r, org).One(ctx, in.RateCard)
		if err != nil {
			return err
		}
		if c.Currency != plan.Currency {
			return fieldError("currency", catalog.CodeCurrencyDoesNotMatch)
		}
		if err := lockKey(ctx, r, org, planLock(plan.ID)); err != nil {
			return err
		}
		priced, err := slicePriced(ctx, r, org, plan.ID, c)
		if err != nil {
			return err
		}
		if priced {
			code := catalog.CodeProductAlreadyPriced
			if !c.ProductFilterID.IsZero() {
				code = catalog.CodeProductFilterAlreadyPriced
			}
			return fieldError("rate_card", code)
		}
		now := s.d.Now()
		p := &catalog.PlanRateCard{
			ID:             core.NewID[core.PlanRateCardID](s.d.IDs),
			OrganizationID: org,
			PlanID:         plan.ID,
			RateCardID:     c.ID,
			Units:          in.Units,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := p.Validate(); err != nil {
			return err
		}
		k, err := cardOf(ctx, r, c)
		if err != nil {
			return err
		}
		var phases []*catalog.RatePhase
		if in.Phases != nil {
			if phases, err = s.buildPhases(k, p.ID, *in.Phases); err != nil {
				return err
			}
		} else {
			phases = []*catalog.RatePhase{s.newPhase(org, p.ID, "default", 1)}
		}
		if err := r.PlanRateCards.Create(ctx, p, phases); err != nil {
			return conflict(err, "rate_card")
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create plan rate card: %w", err)
	}
	return out, nil
}

// PlanRateCardUpdate is a partial update of a plan rate card.
type PlanRateCardUpdate struct {
	// Units is the quantity billed, nil to unset.
	Units core.Optional[*money.Decimal]
}

// UpdatePlanRateCard changes the units of the plan rate card ref of the
// plan. A plan a contract uses is plan plan_locked.
//
// source: app/services/plan_rate_cards/update_service.rb:15-31
func (s *Service) UpdatePlanRateCard(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref, in PlanRateCardUpdate) (*catalog.PlanRateCard, error) {
	var out *catalog.PlanRateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := planRateCardOf(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		if err := planGuard(ctx, r, org, p.PlanID, "plan"); err != nil {
			return err
		}
		in.Units.Apply(&p.Units)
		if err := p.Validate(); err != nil {
			return err
		}
		p.UpdatedAt = s.d.Now()
		if err := r.PlanRateCards.Update(ctx, p); err != nil {
			return fmt.Errorf("update: %w", err)
		}
		out = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update plan rate card: %w", err)
	}
	return out, nil
}

// DeletePlanRateCard removes the plan rate card ref from the plan with its
// phases. A plan a contract uses is plan plan_locked.
//
// source: app/services/plan_rate_cards/destroy_service.rb:15-33
func (s *Service) DeletePlanRateCard(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref) (*catalog.PlanRateCard, error) {
	var out *catalog.PlanRateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := planRateCardOf(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		if err := planGuard(ctx, r, org, p.PlanID, "plan"); err != nil {
			return err
		}
		now := s.d.Now()
		if err := r.PlanRateCards.Discard(ctx, org, p.ID, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		p.DeletedAt = &now
		out = p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete plan rate card: %w", err)
	}
	return out, nil
}

// PlanRateCard returns the plan rate card ref of the plan: its ID, or the
// code of its rate card.
//
// source: app/controllers/api/v2/plan_rate_cards_controller.rb:21-27, :86-91
func (s *Service) PlanRateCard(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref) (*catalog.PlanRateCard, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.PlanRateCard, error) {
		return planRateCardOf(ctx, r, org, planRef, ref)
	})
}

// ListPlanRateCards returns a page of the plan's rate cards in creation
// order. A missing plan is core.NotFound("plan").
//
// source: app/controllers/api/v2/plan_rate_cards_controller.rb:55-78, app/queries/plan_rate_cards_query.rb
func (s *Service) ListPlanRateCards(ctx context.Context, org core.OrganizationID, planRef refs.Ref, page core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.PlanRateCard], error) {
		plan, err := refs.Plans(r, org).One(ctx, planRef)
		if err != nil {
			return core.Page[*catalog.PlanRateCard]{}, err
		}
		return r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{PlanID: plan.ID}, page)
	})
}

// PlanRateCardDetails are what the plan rate card serializer shows beside
// the record: the applied card and the phases.
type PlanRateCardDetails struct {
	// RateCard is the applied card.
	RateCard *catalog.RateCard
	// Phases are the ramp by position.
	Phases []*catalog.RatePhase
}

// PlanRateCardDetails returns the card and phases of p.
//
// source: app/serializers/v2/plan_rate_card_serializer.rb
func (s *Service) PlanRateCardDetails(ctx context.Context, p *catalog.PlanRateCard) (PlanRateCardDetails, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (PlanRateCardDetails, error) {
		var d PlanRateCardDetails
		var err error
		if d.RateCard, err = getRateCard(ctx, r, p.OrganizationID, p.RateCardID); err != nil {
			return d, fmt.Errorf("load rate card: %w", err)
		}
		if d.Phases, err = r.PlanRateCards.ListPhases(ctx, p.OrganizationID, p.ID); err != nil {
			return d, fmt.Errorf("list phases: %w", err)
		}
		return d, nil
	})
}

// planRateCardOf returns the plan rate card ref of the plan planRef: by
// ID, or by the code of its rate card. A missing plan is
// core.NotFound("plan"), a missing plan rate card
// core.NotFound("plan_rate_card").
func planRateCardOf(ctx context.Context, r port.Repos, org core.OrganizationID, planRef, ref refs.Ref) (*catalog.PlanRateCard, error) {
	plan, err := refs.Plans(r, org).One(ctx, planRef)
	if err != nil {
		return nil, err
	}
	var p *catalog.PlanRateCard
	switch {
	case ref.Code != "":
		c, err := r.RateCards.GetByCode(ctx, org, ref.Code)
		if err != nil {
			return nil, notFound(err, ResourcePlanRateCard)
		}
		p, err = r.PlanRateCards.GetByRateCard(ctx, org, plan.ID, c.ID)
		if err != nil {
			return nil, notFound(err, ResourcePlanRateCard)
		}
	case !ref.IsZero():
		id, err := core.ParseID[core.PlanRateCardID](ref.ID.String())
		if err != nil {
			return nil, core.NotFound(ResourcePlanRateCard)
		}
		p, err = getPlanRateCard(ctx, r, org, id)
		if err != nil {
			return nil, notFound(err, ResourcePlanRateCard)
		}
		if p.PlanID != plan.ID {
			return nil, core.NotFound(ResourcePlanRateCard)
		}
	default:
		return nil, core.NotFound(ResourcePlanRateCard)
	}
	return p, nil
}

// planGuard fails with <field> plan_locked when a contract uses the plan:
// its pricing changes through a new plan (lago PlanRateCard#edit_error_code,
// CatalogPlan#attached_to_contracts?).
//
// source: app/models/plan_rate_card.rb:22-24
func planGuard(ctx context.Context, r port.Repos, org core.OrganizationID, plan core.PlanID, field string) error {
	locked, err := planContracted(ctx, r, org, plan)
	if err != nil {
		return err
	}
	if locked {
		return fieldError(field, catalog.CodePlanLocked)
	}
	return nil
}

// slicePriced reports whether the plan applies a card of the same product
// and filter as c.
//
// source: app/services/plan_rate_cards/create_service.rb:76-83
func slicePriced(ctx context.Context, r port.Repos, org core.OrganizationID, plan core.PlanID, c *catalog.RateCard) (bool, error) {
	var token string
	for {
		page, err := r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{PlanID: plan, ProductID: c.ProductID}, core.PageRequest{Size: 100, Token: token})
		if err != nil {
			return false, fmt.Errorf("list plan rate cards: %w", err)
		}
		for _, p := range page.Items {
			other, err := getRateCard(ctx, r, org, p.RateCardID)
			if err != nil {
				return false, fmt.Errorf("load rate card: %w", err)
			}
			if other.ProductFilterID == c.ProductFilterID {
				return true, nil
			}
		}
		if page.NextToken == "" {
			return false, nil
		}
		token = page.NextToken
	}
}
