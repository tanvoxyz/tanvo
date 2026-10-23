package lifecycle

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// applyRules replaces the activation rules of the pending subscription sub
// with copies of rules (lago Subscriptions::ActivationRules::ApplyService):
// empty rules leave the stored ones unless replaceEmpty is set and rules
// is not nil.
//
// source: app/services/subscriptions/activation_rules/apply_service.rb
func applyRules(ctx context.Context, r port.Repos, sub *subscription.Subscription, rules []*subscription.ActivationRule, replaceEmpty bool) error {
	if len(rules) == 0 && (!replaceEmpty || rules == nil) {
		return nil
	}
	out := make([]*subscription.ActivationRule, len(rules))
	for i, rule := range rules {
		c := *rule
		c.OrganizationID, c.SubscriptionID = sub.OrganizationID, sub.ID
		if err := c.Validate(); err != nil {
			return err
		}
		out[i] = &c
	}
	if err := r.ActivationRules.ReplaceForSubscription(ctx, sub.OrganizationID, sub.ID, out); err != nil {
		return fmt.Errorf("store activation rules of subscription %s: %w", sub.ID, err)
	}
	return nil
}
