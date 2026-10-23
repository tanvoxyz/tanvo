package billing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// defaultTerminationAlertDays returns the days before its ending_at a
// subscription gets subscription.termination_alert by default (lago
// LAGO_SUBSCRIPTION_TERMINATION_ALERT_SENT_AT_DAYS default "15,45",
// app/jobs/clock/subscriptions_to_be_terminated_job.rb:38).
func defaultTerminationAlertDays() []int { return []int{15, 45} }

// Option configures an [Engine].
type Option func(*Engine)

// WithTerminationAlertDays sets the days before its ending_at a
// subscription gets subscription.termination_alert, replacing
// the default 15 and 45 (lago
// LAGO_SUBSCRIPTION_TERMINATION_ALERT_SENT_AT_DAYS). With no days no alert
// is sent.
func WithTerminationAlertDays(days ...int) Option {
	return func(e *Engine) { e.terminationAlertDays = slices.Clone(days) }
}

// AlertTerminatingSubscriptions emits subscription.termination_alert for
// every active subscription of org whose ending_at falls, in UTC, on a
// configured number of days ([WithTerminationAlertDays]) after the clock
// instant at, unless it got one on at's UTC day already, and returns the
// number of alerts (lago Clock::SubscriptionsToBeTerminatedJob,
// app/jobs/clock/subscriptions_to_be_terminated_job.rb:7-29).
func (e *Engine) AlertTerminatingSubscriptions(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	today := core.DateOf(at, time.UTC)
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var subs []*subscription.Subscription
		seen := map[core.Date]bool{}
		for _, days := range e.terminationAlertDays {
			// (now + n.days).to_date: the UTC date n days after at.
			d := core.DateOf(at.AddDate(0, 0, days), time.UTC)
			if seen[d] {
				continue
			}
			seen[d] = true
			from := d.Midnight(time.UTC)
			got, err := r.Subscriptions.ListEndingBetween(ctx, org, from, from.AddDate(0, 0, 1))
			if err != nil {
				return fmt.Errorf("list subscriptions ending on %s: %w", d, err)
			}
			subs = append(subs, got...)
		}
		if len(subs) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(subs))
		for i, s := range subs {
			ids[i] = s.ID.UUID()
		}
		alerted, err := r.DomainEvents.ObjectsWithEventSince(ctx, org, webhook.EventTypeSubscriptionTerminationAlert, "subscription", ids, today.Midnight(time.UTC))
		if err != nil {
			return fmt.Errorf("list termination alerts of %s: %w", today, err)
		}
		for _, s := range subs {
			if slices.Contains(alerted, s.ID.UUID()) {
				continue
			}
			if err := kernel.Emit(ctx, e.d, org, webhook.EventTypeSubscriptionTerminationAlert, "subscription", s.ID.UUID(), s); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
