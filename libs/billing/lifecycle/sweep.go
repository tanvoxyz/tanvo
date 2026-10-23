package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// maxDayAhead bounds how far ahead of an instant its local day can end
// (a day plus the largest UTC offset).
const maxDayAhead = 24*time.Hour + 14*time.Hour

// ActivatePending activates, each in its own transaction, the pending
// subscriptions of org that replace none and whose local subscription
// date is on or before the local date of at. It returns the number
// processed.
//
// source: app/services/subscriptions/activate_all_pending_service.rb
func (s *Service) ActivatePending(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	var due []core.SubscriptionID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := r.Subscriptions.ListPendingToActivate(ctx, org, at.Add(maxDayAhead))
		if err != nil {
			return fmt.Errorf("list pending subscriptions: %w", err)
		}
		for _, sub := range subs {
			env, err := kernel.LoadCustomerEnv(ctx, r, org, sub.CustomerID, sub.BillingEntityID)
			if err != nil {
				return err
			}
			if !env.Today(sub.SubscriptionAt).After(env.Today(at)) {
				due = append(due, sub.ID)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, id := range due {
		if _, err := s.Activate(ctx, org, id, at); err != nil {
			s.d.Logger.ErrorContext(ctx, "activate pending subscription", "subscription_id", id, "error", err)
			errs = append(errs, err)
		}
	}
	return len(due) - len(errs), errors.Join(errs...)
}

// EndTrials ends the free trials of the active subscriptions of org that
// ended by at (domain §9.4), each in its own transaction: trial_ended_at
// is set to the trial end, subscription.trial_ended is emitted and an
// in-advance plan bills its base fee for the rest of the period, unless
// the subscription was already invoiced periodically on the local day of
// at. It returns the number of trials ended.
//
// source: app/services/subscriptions/free_trial_billing_service.rb
func (s *Service) EndTrials(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	var due []core.SubscriptionID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
			return r.Subscriptions.List(ctx, org, subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive}}, p)
		})
		if err != nil {
			return fmt.Errorf("list active subscriptions: %w", err)
		}
		for _, sub := range subs {
			if sub.TrialEndedAt != nil {
				continue
			}
			env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
			if err != nil {
				return err
			}
			if end, ok := env.Period().TrialEnd(); ok && !end.After(at) {
				due = append(due, sub.ID)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, id := range due {
		if err := s.endTrial(ctx, org, id, at); err != nil {
			s.d.Logger.ErrorContext(ctx, "end trial", "subscription_id", id, "error", err)
			errs = append(errs, err)
		}
	}
	return len(due) - len(errs), errors.Join(errs...)
}

// endTrial ends the trial of the subscription id at at.
func (s *Service) endTrial(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, at time.Time) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		env, err := s.lockedEnv(ctx, r, org, id)
		if err != nil {
			return err
		}
		sub := env.Subscription
		end, ok := env.Period().TrialEnd()
		if sub.Status != subscription.StatusActive || sub.TrialEndedAt != nil || !ok || end.After(at) {
			return nil
		}
		billed, err := billedOn(ctx, r, env, at)
		if err != nil {
			return err
		}
		if env.Plan.PayInAdvance && !billed {
			if err := s.bill(ctx, r, org, at, true, item(sub, invoice.InvoicingReasonSubscriptionStarting)); err != nil {
				return err
			}
		}
		if err := sub.TrialEnded(end); err != nil {
			return fmt.Errorf("end trial of subscription %s: %w", sub.ID, err)
		}
		if err := update(ctx, r, sub); err != nil {
			return err
		}
		return s.emit(ctx, webhook.EventTypeSubscriptionTrialEnded, sub)
	})
}

// billedOn reports whether the subscription has a periodic invoice
// subscription whose timestamp is on the local day of at, whichever
// invoice came last (a progressive billing or in-advance charge invoice
// may follow the periodic one the same day).
//
// source: app/services/subscriptions/free_trial_billing_service.rb:101 (already_billed_today)
func billedOn(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, at time.Time) (bool, error) {
	sub := env.Subscription
	rows, err := r.InvoiceSubscriptions.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return false, fmt.Errorf("load invoices of subscription %s: %w", sub.ID, err)
	}
	today := env.Customer.Today(at)
	for _, row := range rows {
		if row.Recurring && !row.Timestamp.IsZero() && env.Customer.Today(row.Timestamp) == today {
			return true, nil
		}
	}
	return false, nil
}

// HandleJob runs a lifecycle job (pending activation, ended termination,
// trial end) and reports whether j is one.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobActivatePendingSubscriptions, kernel.JobTerminateEndedSubscriptions, kernel.JobEndTrials:
	default:
		return false, nil
	}
	var a kernel.InstantArgs
	if err := json.Unmarshal(j.Args, &a); err != nil {
		return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	if a.At.IsZero() {
		// The clock job's tick is the instant (libs/jobs Periodic); the
		// worker's clock may be a later day under a backlog.
		return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
	}
	var err error
	switch j.Kind {
	case kernel.JobActivatePendingSubscriptions:
		_, err = s.ActivatePending(ctx, j.OrganizationID, a.At)
	case kernel.JobTerminateEndedSubscriptions:
		_, err = s.TerminateEnded(ctx, j.OrganizationID, a.At)
	default:
		_, err = s.EndTrials(ctx, j.OrganizationID, a.At)
	}
	return true, err
}
