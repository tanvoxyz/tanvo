package invoicing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// BillSubscriptions runs the periodic billing of the organization at
// billingAt (lago Subscriptions::OrganizationBillingService). Every active
// subscription [billable] that day is either swapped, when it has a
// pending downgrade, by a [kernel.JobSwapDowngrade] (lago
// Subscriptions::TerminateJob), or billed: the others are grouped per
// invoice (domain §22.2) and each group gets a [kernel.JobBillSubscription]
// and a [kernel.JobBillAdvanceCharges]. The unique keys of the jobs name
// the local billing date, so the ticks of one day enqueue them once.
//
// Each subscription is read, and each job enqueued, in its own
// transaction: a failure is logged and stays isolated, the other
// subscriptions are still billed, and the failures are returned joined.
// It returns how many groups were enqueued.
func (s *Service) BillSubscriptions(ctx context.Context, org core.OrganizationID, billingAt time.Time) (int, error) {
	at := billingAt.UTC()
	var subs []*subscription.Subscription
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		subs, err = kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
			return r.Subscriptions.List(ctx, org, subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive}}, p)
		})
		if err != nil {
			return fmt.Errorf("list active subscriptions: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var errs []error
	fail := func(err error) {
		s.d.Logger.ErrorContext(ctx, "periodic billing", slog.String("organization_id", org.String()), slog.Any("error", err))
		errs = append(errs, err)
	}
	type swap struct {
		id  core.SubscriptionID
		key string
	}
	var swaps []swap
	var keys []subscription.GroupingKey
	groups := map[subscription.GroupingKey][]core.SubscriptionID{}
	locals := map[subscription.GroupingKey]core.Date{}
	for _, sub := range subs {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			env, err := kernel.LoadSubscriptionEnv(ctx, r, org, sub.ID)
			if err != nil {
				return err
			}
			ok, err := billable(ctx, r, env, at)
			if err != nil || !ok {
				return err
			}
			today := core.DateOf(at, env.Location())
			if env.Next != nil && env.Next.Status == subscription.StatusPending {
				swaps = append(swaps, swap{id: sub.ID, key: fmt.Sprintf("swap:%s:%s", sub.ID, today)})
				return nil
			}
			k := subscription.Grouping(env.Subscription, env.Plan.Currency, env.Customer.Customer.BillingEntityID)
			if _, ok := groups[k]; !ok {
				locals[k] = today
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], sub.ID)
			return nil
		})
		if err != nil {
			fail(fmt.Errorf("subscription %s: %w", sub.ID, err))
		}
	}
	now := s.d.Now()
	for _, sw := range swaps {
		args := kernel.SwapDowngradeArgs{SubscriptionID: sw.id, BillingAt: at}
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			return kernel.Enqueue(ctx, r, org, kernel.JobSwapDowngrade, args, sw.key, now)
		})
		if err != nil {
			fail(fmt.Errorf("subscription %s: %w", sw.id, err))
		}
	}
	n := 0
	for _, k := range keys {
		ids := groups[k]
		// The key is the billing date in the customer's time zone, so
		// that the ticks of one day enqueue one job (lago
		// BillSubscriptionJob#lock_key_arguments).
		key := fmt.Sprintf("periodic:%s:%s", ids[0], locals[k])
		args := kernel.BillSubscriptionArgs{SubscriptionIDs: ids, BillingAt: at, Reason: string(invoice.InvoicingReasonSubscriptionPeriodic)}
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			if err := kernel.Enqueue(ctx, r, org, kernel.JobBillSubscription, args, key, now); err != nil {
				return err
			}
			return kernel.Enqueue(ctx, r, org, kernel.JobBillAdvanceCharges, args, "advance:"+key, now)
		})
		if err != nil {
			fail(fmt.Errorf("subscriptions %v: %w", ids, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// billable reports whether the subscription of env is billed, or swapped
// to its pending downgrade, by the periodic run at billingAt (lago
// OrganizationBillingService#billable_subscriptions,
// app/services/subscriptions/organization_billing_service.rb:111-121): it
// is a billing day in its time zone, the subscription started before that
// local date, was created on or before that date (UTC dates, as lago's
// DATE(created_at) <= DATE(:today)), does not end that local date (its
// termination bills it), and was not already billed that local date.
func billable(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, billingAt time.Time) (bool, error) {
	sub, loc := env.Subscription, env.Location()
	today := core.DateOf(billingAt, loc)
	switch {
	case !env.Period().IsBillingDay(billingAt):
		return false, nil
	case sub.StartedAt == nil || !core.DateOf(*sub.StartedAt, loc).Before(today):
		return false, nil
	case core.DateOf(billingAt, time.UTC).Before(core.DateOf(sub.CreatedAt, time.UTC)):
		return false, nil
	case sub.EndingAt != nil && core.DateOf(*sub.EndingAt, loc) == today:
		return false, nil
	}
	billed, err := billedToday(ctx, r, sub, billingAt, loc)
	return !billed, err
}

// BillSubscription runs a [kernel.JobBillSubscription]: it bills the
// subscriptions of a with their reason in one transaction.
func (s *Service) BillSubscription(ctx context.Context, org core.OrganizationID, a kernel.BillSubscriptionArgs) ([]*invoice.Invoice, error) {
	req := BillRequest{OrganizationID: org, BillingAt: a.BillingAt, InvoiceID: a.InvoiceID}
	reason := invoice.InvoicingReason(a.Reason)
	if reason == "" {
		reason = invoice.InvoicingReasonSubscriptionPeriodic
	}
	for _, id := range a.SubscriptionIDs {
		req.Items = append(req.Items, BillItem{SubscriptionID: id, Reason: reason})
	}
	var out []*invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = s.Bill(ctx, r, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// billedToday reports whether sub already has a periodic invoice row whose
// timestamp falls on the local date of billingAt (lago
// OrganizationBillingService#already_billed_today, at
// app/services/subscriptions/organization_billing_service.rb:512), so that
// the hourly ticks of a billing day bill a subscription once.
func billedToday(ctx context.Context, r port.Repos, sub *subscription.Subscription, billingAt time.Time, loc *time.Location) (bool, error) {
	rows, err := r.InvoiceSubscriptions.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return false, fmt.Errorf("load invoices of subscription %s: %w", sub.ID, err)
	}
	today := core.DateOf(billingAt, loc)
	for _, is := range rows {
		if is.Recurring && !is.Timestamp.IsZero() && core.DateOf(is.Timestamp, loc) == today {
			return true, nil
		}
	}
	return false, nil
}
