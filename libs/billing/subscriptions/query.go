package subscriptions

import (
	"context"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Get returns the subscription ref designates: by ID, or the live
// subscription with that external ID, preferring active, then
// incomplete, then pending.
//
// source: app/controllers/api/v1/subscriptions_controller.rb:131
func (s *Service) Get(ctx context.Context, org core.OrganizationID, ref SubscriptionRef) (*View, error) {
	var out *View
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		sub, err := findSubscription(ctx, r, org, ref)
		if err != nil {
			return err
		}
		out, err = view(ctx, r, sub, s.d.Now())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return out, nil
}

// ListInput is the request of [Service.List].
type ListInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Customer, when not zero, keeps the customer's subscriptions.
	Customer CustomerRef
	// Plan, when not zero, keeps the subscriptions of the plan (by ID) or
	// of the plans with that code (an original plan and its overrides).
	Plan refs.Ref
	// ExternalID keeps the subscriptions with that external ID.
	ExternalID string
	// Statuses keeps the subscriptions in these statuses; active only when
	// empty.
	Statuses []subscription.Status
	// BillingEntities keeps the subscriptions of these billing entities.
	BillingEntities []refs.Ref
	// Overridden, when set, keeps the subscriptions of an override plan
	// (true) or of an original plan (false).
	Overridden *bool
	// Currency keeps the subscriptions whose plan has that currency.
	Currency money.Currency
	// Search is the free-text search.
	Search string
	// Page is the page.
	Page core.PageRequest
}

// List lists subscriptions, most recent subscription_at first (lago
// SubscriptionsQuery). A billing entity or customer that does not exist
// fails with core.NotFound.
//
// source: app/controllers/concerns/subscription_index.rb, app/queries/subscriptions_query.rb
func (s *Service) List(ctx context.Context, in ListInput) (core.Page[*View], error) {
	org := in.OrganizationID
	var out core.Page[*View]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		f := subscription.Filter{
			ExternalID: in.ExternalID, Statuses: in.Statuses, Overridden: in.Overridden,
			Currency: in.Currency, Search: strings.TrimSpace(in.Search), Order: core.OrderRecent,
		}
		if len(f.Statuses) == 0 {
			// subscription_index.rb:11.
			f.Statuses = []subscription.Status{subscription.StatusActive}
		}
		switch {
		case in.Customer.ExternalID != "":
			f.ExternalCustomerID = in.Customer.ExternalID
		case !in.Customer.ID.IsZero():
			f.CustomerID = in.Customer.ID
		}
		switch {
		case in.Plan.Code != "":
			f.PlanCode = in.Plan.Code
		case !in.Plan.IsZero():
			id, err := core.ParseID[core.PlanID](in.Plan.ID.String())
			if err != nil {
				return fmt.Errorf("plan id: %w", err)
			}
			f.PlanID = id
		}
		for _, ref := range in.BillingEntities {
			be, err := findBillingEntity(ctx, r, org, ref)
			if err != nil {
				return err
			}
			f.BillingEntityIDs = append(f.BillingEntityIDs, be.ID)
		}
		page, err := r.Subscriptions.List(ctx, org, f, in.Page)
		if err != nil {
			return fmt.Errorf("list subscriptions: %w", err)
		}
		now := s.d.Now()
		out.NextToken = page.NextToken
		out.Items = make([]*View, len(page.Items))
		for i, sub := range page.Items {
			if out.Items[i], err = view(ctx, r, sub, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return core.Page[*View]{}, fmt.Errorf("list subscriptions: %w", err)
	}
	return out, nil
}
