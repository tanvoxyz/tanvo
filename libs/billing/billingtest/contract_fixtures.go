package billingtest

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// Contract creates a contract of the customer, priced by the plan p (nil
// for a contract without one), started at the clock's instant and active
// when that instant has come. Options run before activation.
func (h *Harness) Contract(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	h.T.Helper()
	id := core.NewID[core.SubscriptionID](h.IDs)
	var plan core.PlanID
	if p != nil {
		plan = p.ID
	}
	s := subscription.New(h.OrgID(), id, c.ID, plan, "contract_"+id.String()[30:], h.Now(), subscription.PlanFacts{}, h.Now())
	s.Contract = true
	apply(s, opts)
	if !s.SubscriptionAt.After(h.Now()) && s.Status == subscription.StatusPending {
		if err := s.Activate(s.SubscriptionAt, h.Now()); err != nil {
			h.T.Fatalf("activate contract: %v", err)
		}
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Create(ctx, s) })
	return s
}

// ContractRateCard applies the rate card to the contract from its start
// day in UTC (the time zone of the harness customers), on its start day's
// anchor, with a single indefinite phase coded "default". Options run
// before the card is stored.
func (h *Harness) ContractRateCard(s *subscription.Subscription, c *catalog.RateCard, opts ...func(*subscription.RateCard)) *subscription.RateCard {
	h.T.Helper()
	start := core.DateOf(s.SubscriptionAt, time.UTC)
	next := s.SubscriptionAt
	card := &subscription.RateCard{
		ID:                core.NewID[core.SubscriptionRateCardID](h.IDs),
		OrganizationID:    h.OrgID(),
		SubscriptionID:    s.ID,
		RateCardID:        c.ID,
		EffectiveDate:     start,
		BillingAnchorDate: start,
		NextBillingAt:     &next,
		CreatedAt:         h.Now(),
		UpdatedAt:         h.Now(),
	}
	apply(card, opts)
	phase := &catalog.RatePhase{
		ID:                     core.NewID[core.RatePhaseID](h.IDs),
		OrganizationID:         h.OrgID(),
		SubscriptionRateCardID: card.ID,
		Code:                   "default",
		Position:               1,
		CreatedAt:              h.Now(),
		UpdatedAt:              h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.ContractRateCards.Create(ctx, card, []*catalog.RatePhase{phase})
	})
	return card
}
