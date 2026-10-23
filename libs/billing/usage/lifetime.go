package usage

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// LifetimeUsage returns the lifetime usage of the chain of subscription
// sub, moved to sub when sub is active, or a new one in the currency cur
// when the chain has none yet. Nothing is stored.
func (s *Service) LifetimeUsage(ctx context.Context, r port.Repos, sub *subscription.Subscription, cur money.Currency) (*subscription.LifetimeUsage, error) {
	lu, err := r.LifetimeUsages.GetByExternalID(ctx, sub.OrganizationID, sub.ExternalID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		now := s.d.Now()
		return &subscription.LifetimeUsage{
			ID: core.NewID[core.LifetimeUsageID](s.d.IDs), OrganizationID: sub.OrganizationID,
			SubscriptionID: sub.ID, ExternalSubscriptionID: sub.ExternalID, Currency: cur,
			CreatedAt: now, UpdatedAt: now,
		}, nil
	case err != nil:
		return nil, fmt.Errorf("load lifetime usage of %s: %w", sub.ExternalID, err)
	}
	if lu.SubscriptionID != sub.ID && sub.Status == subscription.StatusActive {
		same, err := sameChain(ctx, r, sub, lu.SubscriptionID)
		if err != nil {
			return nil, err
		}
		if !same {
			// A new chain reusing the external ID starts from a fresh
			// record (lago Subscription#mark_as_active! builds one when
			// there is no previous subscription): nothing imported,
			// invoiced or used yet.
			now := s.d.Now()
			*lu = subscription.LifetimeUsage{
				ID: lu.ID, OrganizationID: sub.OrganizationID, ExternalSubscriptionID: sub.ExternalID,
				Currency: cur, CreatedAt: now, UpdatedAt: now,
			}
		}
		// The chain's next subscription took over: its invoiced usage
		// spans the chain (lago Subscription#mark_as_active!).
		lu.SubscriptionID, lu.RecalculateInvoicedUsage = sub.ID, true
	}
	return lu, nil
}

// sameChain reports whether subscription owner belongs to the chain of
// sub: it is the subscription sub replaces, or shares its subscription_at
// (the chain lago sums the invoiced usage of). A deleted owner belongs to
// none.
func sameChain(ctx context.Context, r port.Repos, sub *subscription.Subscription, owner core.SubscriptionID) (bool, error) {
	if owner == sub.PreviousSubscriptionID {
		return true, nil
	}
	o, err := r.Subscriptions.Get(ctx, sub.OrganizationID, owner)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("load subscription %s: %w", owner, err)
	}
	return o.ExternalID == sub.ExternalID && o.SubscriptionAt.Equal(sub.SubscriptionAt), nil
}

// RefreshLifetimeUsage recomputes the lifetime usage of subscription id
// (lago LifetimeUsages::CalculateService): the invoiced usage when it is
// flagged, then always the current usage, without taxes. current is a
// usage already computed without taxes, or nil. The flags of a
// subscription that is not active are cleared without recomputation.
func (s *Service) RefreshLifetimeUsage(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID, current *Usage) (*subscription.LifetimeUsage, error) {
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, id)
	if err != nil {
		return nil, err
	}
	sub := env.Subscription
	lu, err := s.LifetimeUsage(ctx, r, sub, env.Plan.Currency)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	if sub.Status != subscription.StatusActive {
		lu.RecalculateInvoicedUsage, lu.RecalculateCurrentUsage, lu.UpdatedAt = false, false, now
		return lu, s.saveLifetimeUsage(ctx, r, lu)
	}
	if lu.RecalculateInvoicedUsage {
		amount, err := s.invoicedUsage(ctx, r, sub)
		if err != nil {
			return nil, err
		}
		lu.SetInvoicedUsage(amount, now)
	}
	if current == nil {
		current, err = s.EnvUsage(ctx, r, env, Options{SkipTaxes: true})
		if err != nil {
			return nil, err
		}
	}
	lu.SetCurrentUsage(current.Amount, now)
	return lu, s.saveLifetimeUsage(ctx, r, lu)
}

// saveLifetimeUsage validates and stores lu.
func (s *Service) saveLifetimeUsage(ctx context.Context, r port.Repos, lu *subscription.LifetimeUsage) error {
	if err := lu.Validate(); err != nil {
		return fmt.Errorf("lifetime usage of %s: %w", lu.ExternalSubscriptionID, err)
	}
	if err := r.LifetimeUsages.Upsert(ctx, lu); err != nil {
		return fmt.Errorf("store lifetime usage of %s: %w", lu.ExternalSubscriptionID, err)
	}
	return nil
}

// chain returns the subscriptions sharing the external ID and
// subscription_at of sub that are not canceled: the subscriptions of its
// chain.
func chain(ctx context.Context, r port.Repos, sub *subscription.Subscription) ([]*subscription.Subscription, error) {
	subs, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
		return r.Subscriptions.List(ctx, sub.OrganizationID, subscription.Filter{ExternalID: sub.ExternalID}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of %s: %w", sub.ExternalID, err)
	}
	return slices.DeleteFunc(subs, func(x *subscription.Subscription) bool {
		return x.Status == subscription.StatusCanceled || !x.SubscriptionAt.Equal(sub.SubscriptionAt)
	}), nil
}

// invoicedUsage sums the charge fees of the chain of sub on its finalized
// and draft subscription invoices.
func (s *Service) invoicedUsage(ctx context.Context, r port.Repos, sub *subscription.Subscription) (int64, error) {
	subs, err := chain(ctx, r, sub)
	if err != nil {
		return 0, err
	}
	var total int64
	type key struct {
		inv core.InvoiceID
		sub core.SubscriptionID
	}
	seen := map[key]bool{}
	for _, x := range subs {
		iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, x.OrganizationID, x.ID)
		if err != nil {
			return 0, fmt.Errorf("list invoice subscriptions of %s: %w", x.ID, err)
		}
		for _, is := range iss {
			k := key{is.InvoiceID, x.ID}
			if seen[k] {
				continue
			}
			seen[k] = true
			inv, err := r.Invoices.Get(ctx, x.OrganizationID, is.InvoiceID)
			if err != nil {
				return 0, fmt.Errorf("load invoice %s: %w", is.InvoiceID, err)
			}
			if inv.Type != invoice.TypeSubscription || (inv.Status != invoice.StatusFinalized && inv.Status != invoice.StatusDraft) {
				continue
			}
			fs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
				return r.Fees.List(ctx, x.OrganizationID, invoice.FeeFilter{InvoiceID: inv.ID, SubscriptionID: x.ID, Types: []core.FeeType{core.FeeTypeCharge}}, p)
			})
			if err != nil {
				return 0, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
			}
			for _, f := range fs {
				total += f.Amount
			}
		}
	}
	return total, nil
}

// FlagRefreshFromInvoice flags the invoiced usage of every subscription of
// the subscription invoice inv for recalculation, creating the lifetime
// usages that do not exist yet (lago
// LifetimeUsages::FlagRefreshFromInvoiceService). Other invoices are
// ignored.
func (s *Service) FlagRefreshFromInvoice(ctx context.Context, r port.Repos, inv *invoice.Invoice) ([]*subscription.LifetimeUsage, error) {
	if inv.Type != invoice.TypeSubscription {
		return nil, nil
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list invoice subscriptions of %s: %w", inv.ID, err)
	}
	var out []*subscription.LifetimeUsage
	for _, is := range iss {
		if slices.ContainsFunc(out, func(l *subscription.LifetimeUsage) bool { return l.SubscriptionID == is.SubscriptionID }) {
			continue
		}
		sub, err := r.Subscriptions.Get(ctx, inv.OrganizationID, is.SubscriptionID)
		if err != nil {
			return nil, fmt.Errorf("load subscription %s: %w", is.SubscriptionID, err)
		}
		lu, err := s.LifetimeUsage(ctx, r, sub, inv.Currency)
		if err != nil {
			return nil, err
		}
		lu.RecalculateInvoicedUsage, lu.UpdatedAt = true, s.d.Now()
		if err := s.saveLifetimeUsage(ctx, r, lu); err != nil {
			return nil, err
		}
		out = append(out, lu)
	}
	return out, nil
}

// FlagRefreshFromPlanUpdate flags the invoiced usage of the existing
// lifetime usages of the active subscriptions of plan p for recalculation
// and returns how many were flagged (lago
// LifetimeUsages::FlagRefreshFromPlanUpdateService).
func (s *Service) FlagRefreshFromPlanUpdate(ctx context.Context, r port.Repos, p *catalog.Plan) (int, error) {
	subs, err := r.Subscriptions.ListActiveOrPendingByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return 0, fmt.Errorf("list subscriptions of plan %s: %w", p.ID, err)
	}
	n := 0
	for _, sub := range subs {
		if sub.Status != subscription.StatusActive {
			continue
		}
		lu, err := r.LifetimeUsages.GetBySubscription(ctx, p.OrganizationID, sub.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("load lifetime usage of %s: %w", sub.ID, err)
		}
		lu.RecalculateInvoicedUsage, lu.UpdatedAt = true, s.d.Now()
		if err := s.saveLifetimeUsage(ctx, r, lu); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// UpdateHistoricalUsage sets the imported usage of the chain of the
// external subscription ID (lago LifetimeUsages::UpdateService). A missing
// lifetime usage fails with core.ErrNotFound, a negative amount with
// core.ErrInvalidValue.
func (s *Service) UpdateHistoricalUsage(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string, amount int64) (*subscription.LifetimeUsage, error) {
	lu, err := r.LifetimeUsages.GetByExternalID(ctx, org, externalID)
	if err != nil {
		return nil, fmt.Errorf("load lifetime usage of %s: %w", externalID, err)
	}
	lu.HistoricalUsageAmount, lu.UpdatedAt = amount, s.d.Now()
	if err := s.saveLifetimeUsage(ctx, r, lu); err != nil {
		return nil, err
	}
	return lu, nil
}
