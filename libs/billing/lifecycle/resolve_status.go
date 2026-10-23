package lifecycle

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ResolveSubscriptionStatus moves the incomplete subscription of env, in
// the transaction of r, after one of its activation rules changed: it is
// activated when every rule is fulfilled and canceled (announced by
// subscription.canceled) when one is rejected; otherwise, or when it is
// not incomplete, it is left as it is.
//
// source: app/services/subscriptions/activation_rules/resolve_subscription_status_service.rb:13-27
func (s *Service) ResolveSubscriptionStatus(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv) error {
	sub := env.Subscription
	if sub.Status != subscription.StatusIncomplete {
		return nil
	}
	rules, err := r.ActivationRules.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
	}
	now := s.d.Now()
	switch {
	case !slices.ContainsFunc(rules, func(x *subscription.ActivationRule) bool { return !x.Fulfilled() }):
		return s.activate(ctx, r, env, now)
	case slices.ContainsFunc(rules, (*subscription.ActivationRule).Rejected):
		if err := sub.Cancel(now, ""); err != nil {
			return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
		}
		if err := update(ctx, r, sub); err != nil {
			return err
		}
		return s.emit(ctx, webhook.EventTypeSubscriptionCanceled, sub)
	}
	return nil
}
