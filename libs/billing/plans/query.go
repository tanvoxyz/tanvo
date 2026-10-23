package plans

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// View is a plan with what its API representation shows.
type View struct {
	// Plan is the plan.
	Plan *catalog.Plan
	// Charges are the plan's kept charges, in creation order.
	Charges []*catalog.Charge
	// FixedCharges are the plan's kept fixed charges, in creation order.
	FixedCharges []*catalog.FixedCharge
	// MinimumCommitment is the plan's minimum commitment, or nil.
	MinimumCommitment *catalog.Commitment
	// UsageThresholds are the plan's kept usage thresholds.
	UsageThresholds []*catalog.UsageThreshold
	// ActiveSubscriptions counts the active subscriptions of the plan and
	// of its override plans.
	ActiveSubscriptions int
	// DraftInvoices counts the draft invoices billing a subscription of
	// the plan or of its override plans.
	DraftInvoices int
}

// Get returns the plan id, an original or override plan that is not
// deleted.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		return kept(ctx, r, org, id)
	})
}

// GetByCode returns the original plan with code that is not deleted (the
// API finds plans by code, plans_controller.rb:19,36,52).
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.Plan, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Plan, error) {
		p, err := r.Plans.GetByCode(ctx, org, code)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(Resource)
		}
		return p, err
	})
}

// List returns a page of the organization's original plans, pending
// deletion included, whose name or code contains search, by name (lago
// PlansQuery with include_pending_deletion).
//
// source: app/queries/plans_query.rb:16-27, app/controllers/api/v1/plans_controller.rb:69-77
func (s *Service) List(ctx context.Context, org core.OrganizationID, search string, page core.PageRequest) (core.Page[*catalog.Plan], error) {
	f := catalog.PlanListFilter{
		Statuses: []catalog.PlanStatus{catalog.PlanStatusActive, catalog.PlanStatusPendingDeletion},
		Search:   search,
		Order:    core.OrderRecent,
	}
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Plan], error) {
		return r.Plans.List(ctx, org, f, page)
	})
}

// View returns p with its charges, fixed charges, minimum commitment,
// usage thresholds and counts.
//
// source: app/serializers/v1/plan_serializer.rb, app/models/plan.rb:124-149
func (s *Service) View(ctx context.Context, p *catalog.Plan) (*View, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*View, error) {
		return view(ctx, r, p)
	})
}

func view(ctx context.Context, r port.Repos, p *catalog.Plan) (*View, error) {
	org := p.OrganizationID
	v := &View{Plan: p}
	var err error
	if v.Charges, err = r.Charges.ListByPlan(ctx, org, p.ID); err != nil {
		return nil, fmt.Errorf("load charges: %w", err)
	}
	if v.FixedCharges, err = r.FixedCharges.ListByPlan(ctx, org, p.ID); err != nil {
		return nil, fmt.Errorf("load fixed charges: %w", err)
	}
	if v.UsageThresholds, err = r.UsageThresholds.ListByPlan(ctx, org, p.ID); err != nil {
		return nil, fmt.Errorf("load usage thresholds: %w", err)
	}
	c, err := r.Commitments.GetByPlan(ctx, org, p.ID, catalog.CommitmentTypeMinimumCommitment)
	switch {
	case err == nil:
		v.MinimumCommitment = c
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("load minimum commitment: %w", err)
	}
	plans := []core.PlanID{p.ID}
	if p.IsParent() {
		children, err := r.Plans.ListChildren(ctx, org, p.ID)
		if err != nil {
			return nil, fmt.Errorf("load override plans: %w", err)
		}
		for _, c := range children {
			plans = append(plans, c.ID)
		}
	}
	var ids []core.SubscriptionID
	for _, id := range plans {
		subs, err := kernel.All(func(page core.PageRequest) (core.Page[*subscription.Subscription], error) {
			return r.Subscriptions.List(ctx, org, subscription.Filter{PlanID: id}, page)
		})
		if err != nil {
			return nil, fmt.Errorf("load subscriptions: %w", err)
		}
		for _, sub := range subs {
			ids = append(ids, sub.ID)
			if sub.Status == subscription.StatusActive {
				v.ActiveSubscriptions++
			}
		}
	}
	if len(ids) == 0 {
		return v, nil
	}
	drafts, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, org, invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}, SubscriptionIDs: ids}, page)
	})
	if err != nil {
		return nil, fmt.Errorf("load draft invoices: %w", err)
	}
	seen := map[core.InvoiceID]bool{}
	for _, inv := range drafts {
		seen[inv.ID] = true
	}
	v.DraftInvoices = len(seen)
	return v, nil
}
