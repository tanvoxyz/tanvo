package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// ExpireIncompleteSubscriptions enqueues the
// [kernel.JobExpireIncompleteSubscription] of every incomplete
// subscription of org with a pending activation rule expired at the clock
// instant at and returns their number (lago
// Clock::ExpireIncompleteSubscriptionsJob and Subscription.expirable,
// app/jobs/clock/expire_incomplete_subscriptions_job.rb:7-11,
// app/models/subscription/activation_rule.rb:35).
func (e *Engine) ExpireIncompleteSubscriptions(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := r.Subscriptions.ListIncompleteWithExpiredRules(ctx, org, at)
		if err != nil {
			return fmt.Errorf("list expirable subscriptions: %w", err)
		}
		for _, s := range subs {
			args := kernel.SubscriptionArgs{SubscriptionID: s.ID}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobExpireIncompleteSubscription, args, kernel.ExpireIncompleteKey(s.ID), time.Time{}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ExpireIncompleteSubscription cancels the incomplete subscription id
// whose activation rule timed out with
// [lifecycle.Service.ExpireIncomplete] (lago
// Subscriptions::ActivationRules::ExpireIncompleteJob).
func (e *Engine) ExpireIncompleteSubscription(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	return e.Lifecycle.ExpireIncomplete(ctx, org, id)
}
