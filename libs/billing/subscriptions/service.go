package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Resource names of the not-found errors.
const (
	// Resource is the subscription.
	Resource = "subscription"
	// ResourceCustomer is the customer.
	ResourceCustomer = "customer"
	// ResourcePlan is the plan.
	ResourcePlan = "plan"
	// ResourceBillingEntity is the billing entity.
	ResourceBillingEntity = "billing_entity"
	// ResourceLifetimeUsage is the lifetime usage.
	ResourceLifetimeUsage = "lifetime_usage"
	// ResourceCharge is the charge a usage filter selects.
	ResourceCharge = "charge"
	// ResourceBillableMetric is the billable metric of a past usage
	// filter.
	ResourceBillableMetric = "billable_metric"
)

// Service is the subscription API service.
type Service struct {
	d     *kernel.Deps
	life  *lifecycle.Service
	usage *usage.Service
	plans *plans.Service
}

// New returns the service over d (it uses d.Tx, d.Clock, d.IDs and
// d.Webhooks), running lifecycle changes with life, usage computations
// with u and plan overrides with p.
func New(d *kernel.Deps, life *lifecycle.Service, u *usage.Service, p *plans.Service) *Service {
	return &Service{d: d, life: life, usage: u, plans: p}
}

// CustomerRef references a customer by ID or by external ID: exactly one
// is set.
type CustomerRef struct {
	// ID is the customer's ID.
	ID core.CustomerID
	// ExternalID is the customer's external ID.
	ExternalID string
}

// SubscriptionRef references a subscription by ID or by external ID:
// exactly one is set.
type SubscriptionRef struct {
	// ID is the subscription's ID.
	ID core.SubscriptionID
	// ExternalID is the subscription's external ID.
	ExternalID string
}

// View is a subscription with what its API representation shows.
type View struct {
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// Plan is its plan.
	Plan *catalog.Plan
	// Customer is its customer.
	Customer *customer.Customer
	// Previous is the subscription it replaced, or nil.
	Previous *subscription.Subscription
	// Next is the subscription replacing it (a pending downgrade, or the
	// subscription of an upgrade), or nil.
	Next *subscription.Subscription
	// Downgrade reports that Next is a pending downgrade.
	Downgrade bool
	// ActivationRules are its activation rules in creation order.
	ActivationRules []*subscription.ActivationRule
	// CurrentPeriod is the charges period of an active subscription at
	// the clock's instant (lago current_billing_period_*), or nil.
	CurrentPeriod *period.Period
	// UsageThresholds are the usage thresholds that apply: its own, else
	// its plan's, else the original plan's; none when progressive billing
	// is disabled (lago Subscription#applicable_usage_thresholds).
	UsageThresholds []*catalog.UsageThreshold
}

// findCustomer returns the customer ref designates; a missing one is
// core.NotFound("customer").
func findCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, ref CustomerRef) (*customer.Customer, error) {
	var (
		c   *customer.Customer
		err error
	)
	if ref.ExternalID != "" {
		c, err = r.Customers.GetByExternalID(ctx, org, strings.TrimSpace(ref.ExternalID))
	} else {
		c, err = r.Customers.Get(ctx, org, ref.ID)
	}
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

// findSubscription returns the subscription ref designates: by ID, or the
// live subscription with that external ID, preferring active, then
// incomplete, then pending. A missing one is core.NotFound("subscription").
func findSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, ref SubscriptionRef) (*subscription.Subscription, error) {
	var (
		s   *subscription.Subscription
		err error
	)
	if ref.ExternalID != "" {
		s, err = r.Subscriptions.GetByExternalID(ctx, org, ref.ExternalID)
	} else {
		s, err = r.Subscriptions.Get(ctx, org, ref.ID)
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(Resource)
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription: %w", err)
	}
	return s, nil
}

// findSubscriptionIn returns the subscription ref designates among the
// statuses: by ID when in one of them, or the one with that external ID
// in the first status having one (lago subscriptions_matching_status).
func findSubscriptionIn(ctx context.Context, r port.Repos, org core.OrganizationID, ref SubscriptionRef, statuses ...subscription.Status) (*subscription.Subscription, error) {
	if ref.ExternalID == "" {
		s, err := findSubscription(ctx, r, org, ref)
		if err != nil {
			return nil, err
		}
		for _, st := range statuses {
			if s.Status == st {
				return s, nil
			}
		}
		return nil, core.NotFound(Resource)
	}
	for _, st := range statuses {
		page, err := r.Subscriptions.List(ctx, org, subscription.Filter{ExternalID: ref.ExternalID, Statuses: []subscription.Status{st}, Order: core.OrderRecent}, core.PageRequest{Size: 1})
		if err != nil {
			return nil, fmt.Errorf("list subscriptions %q: %w", ref.ExternalID, err)
		}
		if len(page.Items) > 0 {
			return page.Items[0], nil
		}
	}
	return nil, core.NotFound(Resource)
}

// findParentPlan returns the original (not overridden) plan ref
// designates (lago Plan.parents); a missing one is core.NotFound("plan").
func findParentPlan(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*catalog.Plan, error) {
	p, err := refs.Plans(r, org).One(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !p.ParentID.IsZero() {
		return nil, core.NotFound(ResourcePlan)
	}
	return p, nil
}

// findBillingEntity returns the billing entity ref designates, or nil for
// a zero ref; a missing one is core.NotFound("billing_entity")
// (billing_entity_resolution_concern.rb).
func findBillingEntity(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*organization.BillingEntity, error) {
	if ref.IsZero() {
		return nil, nil
	}
	f := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
		Resource: ResourceBillingEntity,
		Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
			return r.BillingEntities.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
			return r.BillingEntities.GetByCode(ctx, org, code)
		},
		ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
	}
	return f.One(ctx, ref)
}

// view loads the view of s at now.
func view(ctx context.Context, r port.Repos, s *subscription.Subscription, now time.Time) (*View, error) {
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, s)
	if err != nil {
		return nil, err
	}
	rules, err := r.ActivationRules.ListBySubscription(ctx, s.OrganizationID, s.ID)
	if err != nil {
		return nil, fmt.Errorf("list activation rules of %s: %w", s.ID, err)
	}
	v := &View{
		Subscription: s, Plan: env.Plan, Customer: env.Customer.Customer, Previous: env.Previous, Next: env.Next,
		Downgrade:       env.Next != nil && env.Next.Status == subscription.StatusPending && env.Change == period.Downgrade,
		ActivationRules: rules,
	}
	if v.UsageThresholds, err = applicableThresholds(ctx, r, s, env.Plan); err != nil {
		return nil, err
	}
	if s.Status == subscription.StatusActive && s.StartedAt != nil {
		// lago billing_reference_time: the later of now and started_at.
		at := now
		if s.StartedAt.After(at) {
			at = *s.StartedAt
		}
		dates, err := env.Dates(at, period.CurrentUsage)
		if err != nil {
			return nil, err
		}
		if b, ok := dates.Boundaries(); ok && b.ChargesBilled {
			v.CurrentPeriod = &b.Charges
		}
	}
	return v, nil
}

// applicableThresholds returns the usage thresholds that apply to s of
// plan p.
//
// source: app/models/subscription.rb:305-309
func applicableThresholds(ctx context.Context, r port.Repos, s *subscription.Subscription, p *catalog.Plan) ([]*catalog.UsageThreshold, error) {
	if s.ProgressiveBillingDisabled {
		return nil, nil
	}
	org := s.OrganizationID
	own, err := r.UsageThresholds.ListBySubscription(ctx, org, s.ID)
	if err != nil || len(own) > 0 {
		return own, wrapThresholds(err, s.ID.String())
	}
	plan, err := r.UsageThresholds.ListByPlan(ctx, org, p.ID)
	if err != nil || len(plan) > 0 || p.ParentID.IsZero() {
		return plan, wrapThresholds(err, p.ID.String())
	}
	parent, err := r.UsageThresholds.ListByPlan(ctx, org, p.ParentID)
	return parent, wrapThresholds(err, p.ParentID.String())
}

func wrapThresholds(err error, owner string) error {
	if err != nil {
		return fmt.Errorf("list usage thresholds of %s: %w", owner, err)
	}
	return nil
}

// read runs fn in a transaction of the service.
func (s *Service) read(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	return s.d.Tx.InTx(ctx, fn)
}
