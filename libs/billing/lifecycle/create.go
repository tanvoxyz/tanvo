package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Create subscribes a customer (domain §9.2, §22.13). sub is the new
// subscription built with subscription.New and its options; its
// activation rules, when any, are stored by the caller beforehand. When
// the customer already has an active (or future) subscription with the
// same external ID, the call changes its plan instead (an upgrade or a
// downgrade; the same plan returns it unchanged). Otherwise sub is stored
// and, by its local subscription date: today, activated as by
// [Service.Activate]; in the past, activated back-dated without a starting
// invoice (from the last invoiced termination of the same external ID at
// the earliest); in the future, left pending. It returns the resulting
// subscription.
//
// source: app/services/subscriptions/create_service.rb
func (s *Service) Create(ctx context.Context, sub *subscription.Subscription) (*subscription.Subscription, error) {
	org := sub.OrganizationID
	out := sub
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockCustomer(ctx, r, org, sub.CustomerID, customer.LockScopeBillingSchedule); err != nil {
			return err
		}
		plan, err := r.Plans.Get(ctx, org, sub.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
		}
		if err := setCurrency(ctx, r, org, sub.CustomerID, plan); err != nil {
			return err
		}
		live, err := r.Subscriptions.ListByCustomer(ctx, org, sub.CustomerID,
			subscription.StatusPending, subscription.StatusIncomplete, subscription.StatusActive)
		if err != nil {
			return fmt.Errorf("load subscriptions of customer %s: %w", sub.CustomerID, err)
		}
		current, err := currentSubscription(live, sub.ExternalID)
		if err != nil {
			return err
		}
		if current == nil {
			if err := s.checkExternalID(ctx, r, org, sub.ExternalID); err != nil {
				return err
			}
			return s.create(ctx, r, sub, plan)
		}
		if current.PlanID == sub.PlanID {
			out = current
			return nil
		}
		out, err = s.changePlan(ctx, r, current, plan, ChangePlanInput{
			Name: sub.Name, EndingAt: sub.EndingAt, PurchaseOrderNumber: sub.PurchaseOrderNumber,
			BillingEntityID: sub.BillingEntityID,
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	return out, nil
}

// currentSubscription returns the customer's subscription the external ID
// designates: the active one, else the one starting in the future. An
// incomplete one is refused.
func currentSubscription(live []*subscription.Subscription, externalID string) (*subscription.Subscription, error) {
	var current *subscription.Subscription
	for _, l := range live {
		if l.ExternalID != externalID {
			continue
		}
		switch {
		case l.Status == subscription.StatusIncomplete:
			return nil, core.NewRuleError(CodeSubscriptionIncomplete, "the subscription is incomplete")
		case l.Status == subscription.StatusActive:
			if current == nil || current.Status != subscription.StatusActive || startedAfter(l, current) {
				current = l
			}
		case l.StartingInTheFuture() && current == nil:
			current = l
		}
	}
	return current, nil
}

func startedAfter(a, b *subscription.Subscription) bool {
	return a.StartedAt != nil && (b.StartedAt == nil || a.StartedAt.After(*b.StartedAt))
}

// checkExternalID refuses an external ID active for another customer.
func (s *Service) checkExternalID(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string) error {
	other, err := r.Subscriptions.GetByExternalID(ctx, org, externalID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("load subscription %q: %w", externalID, err)
	case other.Status == subscription.StatusActive:
		var v core.Validator
		v.Add("external_id", CodeValueAlreadyExist)
		return v.Err()
	}
	return nil
}

// setCurrency gives the customer the plan's currency when it has none.
// The customer's currency is only a default: a plan in another currency is
// accepted, the subscription billing in its plan's currency.
//
// source: app/services/customers/update_currency_service.rb:16
func setCurrency(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID, plan *catalog.Plan) error {
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return fmt.Errorf("load customer %s: %w", id, err)
	}
	if !c.Currency.IsZero() {
		return nil
	}
	c.Currency = plan.Currency
	if err := r.Customers.Update(ctx, c); err != nil {
		return fmt.Errorf("update customer %s: %w", id, err)
	}
	return nil
}

// create stores a new subscription and starts it per its local date.
func (s *Service) create(ctx context.Context, r port.Repos, sub *subscription.Subscription, plan *catalog.Plan) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	if err := sub.ValidateForPlan(subscription.PlanFacts{PayInAdvance: plan.PayInAdvance}); err != nil {
		return err
	}
	sub.Status = subscription.StatusPending
	if err := r.Subscriptions.Create(ctx, sub); err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
	if err != nil {
		return err
	}
	now := s.d.Now()
	day, today := env.Customer.Today(sub.SubscriptionAt), env.Customer.Today(now)
	switch {
	case day == today:
		if err := kernel.LockSubscription(ctx, r, sub.OrganizationID, sub.ID); err != nil {
			return err
		}
		return s.activate(ctx, r, env, sub.SubscriptionAt)
	case day.Before(today):
		started, err := backdatedStart(ctx, r, sub)
		if err != nil {
			return err
		}
		if err := sub.Activate(started, now); err != nil {
			return fmt.Errorf("activate subscription %s: %w", sub.ID, err)
		}
		if err := update(ctx, r, sub); err != nil {
			return err
		}
		return s.emit(ctx, webhook.EventTypeSubscriptionStarted, sub)
	default:
		return nil
	}
}

// backdatedStart is the start of a back-dated subscription: its
// subscription_at, but not before the last invoiced termination of a
// subscription with the same external ID, so a closed period is not
// billed again.
func backdatedStart(ctx context.Context, r port.Repos, sub *subscription.Subscription) (time.Time, error) {
	terminated, err := r.Subscriptions.ListByCustomer(ctx, sub.OrganizationID, sub.CustomerID, subscription.StatusTerminated)
	if err != nil {
		return time.Time{}, fmt.Errorf("load terminated subscriptions: %w", err)
	}
	start := sub.SubscriptionAt
	for _, t := range terminated {
		if t.ExternalID == sub.ExternalID && t.OnTerminationInvoice == subscription.TerminationInvoiceGenerate &&
			t.TerminatedAt != nil && t.TerminatedAt.After(start) {
			start = *t.TerminatedAt
		}
	}
	return start, nil
}

// ChangePlanInput asks for a plan change of a subscription (domain
// §22.13). The nil fields of the new subscription are copied from the
// current one.
type ChangePlanInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the active or future subscription.
	SubscriptionID core.SubscriptionID
	// PlanID is the new plan.
	PlanID core.PlanID
	// Name is the new subscription's name.
	Name *string
	// EndingAt is the new subscription's ending date.
	EndingAt *time.Time
	// PurchaseOrderNumber is the new subscription's purchase order number.
	PurchaseOrderNumber *string
	// ConsolidateInvoice is the new subscription's grouping setting.
	ConsolidateInvoice *bool
	// BillingEntityID is the new subscription's billing entity override.
	BillingEntityID core.BillingEntityID
}

// ChangePlan changes the plan of a subscription and returns the
// subscription on the new plan. A subscription starting in the future
// changes plan in place. Otherwise the yearly amounts decide (see
// kernel.PlanChange): an upgrade creates the next subscription and
// activates it now, terminating the current one; a downgrade schedules a
// pending next subscription that the periodic run swaps in at the end of
// the period ([Service.SwapDowngrade]). Either replaces a pending
// downgrade. The same plan returns the subscription unchanged.
//
// source: app/services/subscriptions/plan_upgrade_service.rb, plan_downgrade_service.rb
func (s *Service) ChangePlan(ctx context.Context, in ChangePlanInput) (*subscription.Subscription, error) {
	var out *subscription.Subscription
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cur, err := r.Subscriptions.Get(ctx, in.OrganizationID, in.SubscriptionID)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", in.SubscriptionID, err)
		}
		if err := lock(ctx, r, in.OrganizationID, cur.CustomerID, cur.ID); err != nil {
			return err
		}
		plan, err := r.Plans.Get(ctx, in.OrganizationID, in.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", in.PlanID, err)
		}
		if err := setCurrency(ctx, r, in.OrganizationID, cur.CustomerID, plan); err != nil {
			return err
		}
		out, err = s.changePlan(ctx, r, cur, plan, in)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("change plan of subscription %s: %w", in.SubscriptionID, err)
	}
	return out, nil
}

// changePlan moves cur to plan in the transaction of r.
func (s *Service) changePlan(ctx context.Context, r port.Repos, cur *subscription.Subscription, plan *catalog.Plan, in ChangePlanInput) (*subscription.Subscription, error) {
	if cur.Status != subscription.StatusActive && !cur.StartingInTheFuture() {
		return nil, core.NewRuleError(CodeNotChangeable, "only active and future subscriptions change plan")
	}
	// Create reaches here under the billing_schedule lock only: lock the
	// changed subscription before writing it (kernel lock order).
	if err := lock(ctx, r, cur.OrganizationID, cur.CustomerID, cur.ID); err != nil {
		return nil, err
	}
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, cur)
	if err != nil {
		return nil, err
	}
	if env.Next != nil {
		if err := kernel.LockSubscription(ctx, r, cur.OrganizationID, env.Next.ID); err != nil {
			return nil, err
		}
	}
	change := kernel.PlanChange(env.Plan, plan)
	if change == period.NoChange {
		return cur, nil
	}
	now := s.d.Now()
	if cur.StartingInTheFuture() {
		cur.PlanID, cur.UpdatedAt = plan.ID, now
		if in.Name != nil {
			cur.Name = in.Name
		}
		if !in.BillingEntityID.IsZero() {
			cur.BillingEntityID = in.BillingEntityID
		}
		if err := update(ctx, r, cur); err != nil {
			return nil, err
		}
		return cur, nil
	}
	if env.Next != nil && env.Next.Status == subscription.StatusPending {
		if err := env.Next.Cancel(now, subscription.CancellationReasonManual); err != nil {
			return nil, fmt.Errorf("cancel next subscription %s: %w", env.Next.ID, err)
		}
		if err := update(ctx, r, env.Next); err != nil {
			return nil, err
		}
		if err := s.emit(ctx, webhook.EventTypeSubscriptionCanceled, env.Next); err != nil {
			return nil, err
		}
	}
	next := nextSubscription(s.d, cur, plan, in, now)
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := r.Subscriptions.Create(ctx, next); err != nil {
		return nil, fmt.Errorf("create next subscription: %w", err)
	}
	if change == period.Downgrade {
		return next, s.emit(ctx, webhook.EventTypeSubscriptionUpdated, cur)
	}
	if err := kernel.LockSubscription(ctx, r, next.OrganizationID, next.ID); err != nil {
		return nil, err
	}
	nextEnv, err := kernel.LoadSubscriptionEnvOf(ctx, r, next)
	if err != nil {
		return nil, err
	}
	return next, s.activate(ctx, r, nextEnv, now)
}

// nextSubscription builds the pending subscription replacing cur on plan:
// same external ID, subscription date and billing time, the other
// settings from in or else from cur.
func nextSubscription(d *kernel.Deps, cur *subscription.Subscription, plan *catalog.Plan, in ChangePlanInput, now time.Time) *subscription.Subscription {
	next := subscription.New(cur.OrganizationID, core.NewID[core.SubscriptionID](d.IDs), cur.CustomerID, plan.ID,
		cur.ExternalID, cur.SubscriptionAt, subscription.PlanFacts{PayInAdvance: plan.PayInAdvance}, now)
	next.PreviousSubscriptionID = cur.ID
	next.BillingTime = cur.BillingTime
	next.Name = cur.Name
	if in.Name != nil {
		next.Name = in.Name
	}
	next.EndingAt = cur.EndingAt
	if in.EndingAt != nil {
		next.EndingAt = in.EndingAt
	}
	next.PurchaseOrderNumber = cur.PurchaseOrderNumber
	if in.PurchaseOrderNumber != nil {
		next.PurchaseOrderNumber = in.PurchaseOrderNumber
	}
	next.ConsolidateInvoice = cur.ConsolidateInvoice
	if in.ConsolidateInvoice != nil {
		next.ConsolidateInvoice = *in.ConsolidateInvoice
	}
	next.BillingEntityID = cur.BillingEntityID
	if !in.BillingEntityID.IsZero() {
		next.BillingEntityID = in.BillingEntityID
	}
	next.PaymentMethodType, next.PaymentMethodID = cur.PaymentMethodType, cur.PaymentMethodID
	next.ProgressiveBillingDisabled = cur.ProgressiveBillingDisabled
	return next
}
