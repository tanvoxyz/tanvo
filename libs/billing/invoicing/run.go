package invoicing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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

// Delays of the second generation of a reserved invoice whose first one
// failed (lago BillSubscriptionJob#perform,
// app/jobs/bill_subscription_job.rb:44-55): longer on a billing day,
// when the system is under load.
const (
	periodicRetryDelay = 5 * time.Minute
	otherRetryDelay    = 3 * time.Second
)

// BillSubscription runs a [kernel.JobBillSubscription] (lago
// BillSubscriptionJob and Invoices::SubscriptionService). A first run
// commits the generating invoice of each group with its invoice
// subscriptions, then generates each in its own transaction, so that a
// failed generation leaves the record that billing is owed:
//
//   - a transient error ([kernel.Deps.IsTransient]) is returned, for the
//     job's retry policy to run it again, which reuses the reservation;
//   - another error on the first run enqueues one retry of that invoice,
//     after 5 minutes for a periodic bill and 3 seconds otherwise, and
//     succeeds;
//   - another error on a retry (a.InvoiceID set) records an
//     invoice_generation_error detail on the invoice and is returned.
//
// An invoice left generating without a detail, a job discarded after its
// transient retries for one, is generated again by
// [Service.RetryGeneratingInvoices] a day later. The errors of the groups
// are returned joined.
func (s *Service) BillSubscription(ctx context.Context, org core.OrganizationID, a kernel.BillSubscriptionArgs) ([]*invoice.Invoice, error) {
	reason := invoice.InvoicingReason(a.Reason)
	if reason == "" {
		reason = invoice.InvoicingReasonSubscriptionPeriodic
	}
	items := func(ids []core.SubscriptionID) []BillItem {
		out := make([]BillItem, len(ids))
		for i, id := range ids {
			out[i] = BillItem{SubscriptionID: id, Reason: reason}
		}
		return out
	}
	type target struct {
		inv  core.InvoiceID
		subs []core.SubscriptionID
	}
	targets := []target{{a.InvoiceID, a.SubscriptionIDs}}
	if a.InvoiceID.IsZero() {
		targets = nil
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			invs, err := s.bill(ctx, r, BillRequest{OrganizationID: org, BillingAt: a.BillingAt, Items: items(a.SubscriptionIDs)}, true)
			if err != nil {
				return err
			}
			for _, inv := range invs {
				rows, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
				if err != nil {
					return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
				}
				t := target{inv: inv.ID}
				for _, is := range rows {
					t.subs = append(t.subs, is.SubscriptionID)
				}
				targets = append(targets, t)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	var out []*invoice.Invoice
	var errs []error
	for _, t := range targets {
		var invs []*invoice.Invoice
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			invs, err = s.Bill(ctx, r, BillRequest{OrganizationID: org, BillingAt: a.BillingAt, Items: items(t.subs), InvoiceID: t.inv})
			return err
		})
		if err == nil {
			out = append(out, invs...)
			continue
		}
		if s.d.IsTransient(err) {
			errs = append(errs, err)
			continue
		}
		s.d.Logger.ErrorContext(ctx, "invoice generation failed", slog.String("organization_id", org.String()),
			slog.String("invoice_id", t.inv.String()), slog.Bool("retry", !a.InvoiceID.IsZero()), slog.Any("error", err))
		if a.InvoiceID.IsZero() {
			delay := otherRetryDelay
			if reason == invoice.InvoicingReasonSubscriptionPeriodic {
				delay = periodicRetryDelay
			}
			retry := kernel.BillSubscriptionArgs{SubscriptionIDs: t.subs, BillingAt: a.BillingAt, Reason: string(reason), InvoiceID: t.inv}
			if qerr := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				return kernel.Enqueue(ctx, r, org, kernel.JobBillSubscription, retry, RetryGenerationKey(t.inv), s.d.Now().Add(delay))
			}); qerr != nil {
				errs = append(errs, fmt.Errorf("invoice %s: %w; enqueue its retry: %w", t.inv, err, qerr))
			}
			continue
		}
		if derr := s.recordGenerationError(ctx, org, t.inv, t.subs, err); derr != nil {
			err = fmt.Errorf("%w; %w", err, derr)
		}
		errs = append(errs, fmt.Errorf("invoice %s: %w", t.inv, err))
	}
	return out, errors.Join(errs...)
}

// RetryGenerationKey is the unique key of the [kernel.JobBillSubscription]
// that generates the reserved invoice id again.
func RetryGenerationKey(id core.InvoiceID) string { return "bill_retry:" + id.String() }

// recordGenerationError records, in its own transaction, the
// invoice_generation_error detail of the invoice id still generating,
// replacing a former one (lago ErrorDetail.create_generation_error_for,
// app/models/error_detail.rb:19-31).
func (s *Service) recordGenerationError(ctx context.Context, org core.OrganizationID, id core.InvoiceID, subs []core.SubscriptionID, cause error) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		if inv.Status != invoice.StatusGenerating {
			return nil
		}
		if err := r.ErrorDetails.DeleteByInvoice(ctx, org, id, invoice.ErrorCodeInvoiceGenerationError); err != nil {
			return fmt.Errorf("delete error details of invoice %s: %w", id, err)
		}
		details, err := json.Marshal(struct {
			Error         string                `json:"error"`
			Subscriptions []core.SubscriptionID `json:"subscriptions"`
		}{cause.Error(), subs})
		if err != nil {
			return fmt.Errorf("encode error detail: %w", err)
		}
		e := &invoice.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](s.d.IDs), OrganizationID: org, InvoiceID: id,
			Code: invoice.ErrorCodeInvoiceGenerationError, Details: details, CreatedAt: s.d.Now(),
		}
		if err := r.ErrorDetails.Create(ctx, e); err != nil {
			return fmt.Errorf("save error detail of invoice %s: %w", id, err)
		}
		return nil
	})
}

// RetryGeneratingInvoices enqueues a [kernel.JobBillSubscription] for every
// subscription invoice of org still generating [invoice.GeneratingRetryAge]
// before at without an invoice_generation_error detail, at the timestamp
// of its first invoice subscription, and returns their number (lago
// Clock::RetryGeneratingSubscriptionInvoicesJob,
// app/jobs/clock/retry_generating_subscription_invoices_job.rb:9-27).
// Invoices without invoice subscriptions, or billing in-advance charges,
// are left out, as lago does (:13-17).
func (s *Service) RetryGeneratingInvoices(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		invs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
			return r.Invoices.List(ctx, org, invoice.Filter{
				Statuses: []invoice.Status{invoice.StatusGenerating}, IncludeGenerating: true, Types: []invoice.Type{invoice.TypeSubscription},
			}, p)
		})
		if err != nil {
			return fmt.Errorf("list generating invoices: %w", err)
		}
		for _, inv := range invs {
			if !inv.StuckGenerating(at) {
				continue
			}
			details, err := r.ErrorDetails.ListByInvoice(ctx, org, inv.ID)
			if err != nil {
				return fmt.Errorf("load error details of invoice %s: %w", inv.ID, err)
			}
			if slices.ContainsFunc(details, func(e *invoice.ErrorDetail) bool { return e.Code == invoice.ErrorCodeInvoiceGenerationError }) {
				continue
			}
			rows, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
			if err != nil {
				return fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
			}
			if len(rows) == 0 {
				continue
			}
			var reasons []invoice.InvoicingReason
			args := kernel.BillSubscriptionArgs{BillingAt: rows[0].Timestamp, InvoiceID: inv.ID}
			for _, is := range rows {
				args.SubscriptionIDs = append(args.SubscriptionIDs, is.SubscriptionID)
				if !slices.Contains(reasons, is.Reason) {
					reasons = append(reasons, is.Reason)
				}
			}
			// Only the periodic job reserves invoices, with one reason
			// for all its subscriptions; lago also retries upgrade
			// invoices mixing reasons, which Tanvo bills in the
			// transaction of the upgrade, so none is left generating.
			if len(reasons) != 1 || reasons[0] == invoice.InvoicingReasonInAdvanceCharge {
				continue
			}
			args.Reason = string(reasons[0])
			if err := kernel.Enqueue(ctx, r, org, kernel.JobBillSubscription, args, RetryGenerationKey(inv.ID), s.d.Now()); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
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
