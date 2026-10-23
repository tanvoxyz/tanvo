package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// BillItem is one subscription billed by a [BillRequest] with its
// invoicing reason (an upgrade invoice mixes subscription_terminating and
// subscription_starting, domain §24 item 17).
type BillItem struct {
	// SubscriptionID is the billed subscription.
	SubscriptionID core.SubscriptionID
	// Reason is the invoicing reason of the subscription.
	Reason invoice.InvoicingReason
}

// BillRequest asks for the subscription invoices of one customer at one
// billing instant: the items are grouped per billing entity, currency and
// consolidation setting (domain §22.2) into one invoice per group.
type BillRequest struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Items are the billed subscriptions (one customer).
	Items []BillItem
	// BillingAt is the billing instant (the run's timestamp, or the
	// termination/start instant).
	BillingAt time.Time
	// InvoiceID retries the generation of an existing generating or
	// failed invoice instead of creating one.
	InvoiceID core.InvoiceID
	// SkipCharges leaves charges out (lago's skip_charges, used by
	// upgrade invoices of the new subscription).
	SkipCharges bool
}

// billed is one subscription of an invoice being generated.
type billed struct {
	item   BillItem
	env    *kernel.SubscriptionEnv
	dates  *period.Dates
	bounds period.Boundaries
	is     *invoice.Subscription
}

// Bill generates, in the transaction of r, the invoices of req and
// returns them (one per group; groups with nothing to bill give none).
// The invoices are finalized, drafted or closed per domain §22.12 and
// their follow-up jobs are enqueued. A periodic subscription already
// billed for its period is skipped (the idempotency key of §11.1). A tax
// provider failure is not an error: the invoice is returned failed (or
// draft) with a tax_error detail, and [Service.Retry] finishes it.
func (s *Service) Bill(ctx context.Context, r port.Repos, req BillRequest) ([]*invoice.Invoice, error) {
	ids := make([]core.SubscriptionID, 0, len(req.Items))
	for _, it := range req.Items {
		ids = append(ids, it.SubscriptionID)
	}
	if err := lockSubscriptions(ctx, r, req.OrganizationID, ids); err != nil {
		return nil, err
	}
	var existing *invoice.Invoice
	if !req.InvoiceID.IsZero() {
		inv, err := s.resetGenerating(ctx, r, req.OrganizationID, req.InvoiceID)
		if err != nil {
			return nil, err
		}
		if inv.Status == invoice.StatusFailed {
			if err := s.retry(ctx, r, inv); err != nil {
				return nil, err
			}
			return []*invoice.Invoice{inv}, nil
		}
		existing = inv
	}
	var keys []subscription.GroupingKey
	groups := map[subscription.GroupingKey][]*billed{}
	for _, it := range req.Items {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, req.OrganizationID, it.SubscriptionID)
		if err != nil {
			return nil, err
		}
		k := subscription.Grouping(env.Subscription, env.Plan.Currency, env.Customer.Customer.BillingEntityID)
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], &billed{item: it, env: env})
	}
	if existing != nil && len(keys) > 1 {
		return nil, fmt.Errorf("retry invoice %s: %w: subscriptions of several invoices", existing.ID, core.ErrInvalidValue)
	}
	var out []*invoice.Invoice
	for _, k := range keys {
		inv, err := s.billGroup(ctx, r, req, groups[k], existing)
		if err != nil {
			return nil, err
		}
		if inv != nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

// resetGenerating loads the invoice id to retry and, when it is still
// generating, deletes what a failed generation left on it.
func (s *Service) resetGenerating(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
		return nil, err
	}
	inv, err := r.Invoices.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	switch inv.Status {
	case invoice.StatusFailed:
		return inv, nil
	case invoice.StatusGenerating:
	default:
		return nil, fmt.Errorf("retry invoice %s: %w", id, core.NewRuleError("not_retryable", "only generating or failed invoices are retried"))
	}
	if err := s.clearInvoice(ctx, r, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// clearInvoice deletes the fees, invoice subscriptions, applied taxes and
// error details of inv.
func (s *Service) clearInvoice(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
	org := inv.OrganizationID
	if err := r.Fees.DeleteByInvoice(ctx, org, inv.ID); err != nil {
		return fmt.Errorf("delete fees of invoice %s: %w", inv.ID, err)
	}
	if err := r.InvoiceSubscriptions.DeleteByInvoice(ctx, org, inv.ID); err != nil {
		return fmt.Errorf("delete subscriptions of invoice %s: %w", inv.ID, err)
	}
	if err := r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, org, inv.ID, nil); err != nil {
		return fmt.Errorf("delete taxes of invoice %s: %w", inv.ID, err)
	}
	if err := r.ErrorDetails.DeleteByInvoice(ctx, org, inv.ID, ""); err != nil {
		return fmt.Errorf("delete error details of invoice %s: %w", inv.ID, err)
	}
	return nil
}

// billGroup generates the invoice of one group of subscriptions (lago
// Invoices::SubscriptionService), into existing when retrying.
func (s *Service) billGroup(ctx context.Context, r port.Repos, req BillRequest, group []*billed, existing *invoice.Invoice) (*invoice.Invoice, error) {
	org := req.OrganizationID
	recurring, active := true, false
	for _, b := range group {
		recurring = recurring && b.item.Reason == invoice.InvoicingReasonSubscriptionPeriodic
		active = active || b.env.Subscription.Status == subscription.StatusActive
	}
	if recurring && !active {
		return nil, nil
	}
	var rows []*billed
	for _, b := range group {
		ok, err := s.resolveBoundaries(ctx, r, b, req.BillingAt)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if existing == nil {
			billed, err := rowExists(ctx, r, org, b.is)
			if err != nil {
				return nil, err
			}
			if billed {
				continue
			}
		}
		rows = append(rows, b)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	cenv := rows[0].env.Customer
	k := subscriptionKind(recurring)
	inv := existing
	if inv == nil {
		sub := rows[0].env.Subscription
		inv = kernel.NewInvoice(s.d, cenv, k, rows[0].env.Plan.Currency, req.BillingAt)
		inv.PaymentMethodType, inv.PaymentMethodID = sub.PaymentMethodType, sub.PaymentMethodID
		if err := r.Invoices.Create(ctx, inv); err != nil {
			return nil, fmt.Errorf("create invoice: %w", err)
		}
	}
	for _, b := range rows {
		b.is.InvoiceID = inv.ID
		if err := r.InvoiceSubscriptions.Create(ctx, b.is); err != nil {
			return nil, fmt.Errorf("create invoice subscription of %s: %w", b.env.Subscription.ID, err)
		}
	}
	return s.generate(ctx, r, inv, cenv, k, rows, req.SkipCharges, modeNew, nil)
}

// generate computes the fees of the subscription invoice inv over rows,
// settles and concludes it per mode and announces the result.
// beforeFinish, when set, runs once the invoice is settled and before it
// is concluded (a refresh moves its draft credit notes to the new fees).
func (s *Service) generate(ctx context.Context, r port.Repos, inv *invoice.Invoice, cenv *kernel.CustomerEnv, k kernel.InvoiceKind, rows []*billed, skipCharges bool, mode genMode, beforeFinish func(context.Context, port.Repos) error) (*invoice.Invoice, error) {
	fs, standalone, err := s.subscriptionFees(ctx, r, inv, rows, skipCharges)
	if err != nil {
		return nil, err
	}
	resolver := kernel.NewTaxResolver(r, cenv)
	subs := make([]core.SubscriptionID, len(rows))
	for i, b := range rows {
		resolver.SetPlan(b.env.Subscription.ID, b.env.Plan)
		subs[i] = b.env.Subscription.ID
	}
	draft := mode != modeFinalize && inv.Status != invoice.StatusFailed && k.UsesGracePeriod && inv.AppliedGracePeriodDays > 0
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{draft: draft, resolver: resolver})
	if err != nil {
		return nil, err
	}
	if st.gated, err = gated(ctx, r, rows); err != nil {
		return nil, err
	}
	if err := s.emitFees(ctx, standalone); err != nil {
		return nil, err
	}
	if beforeFinish != nil {
		if err := beforeFinish(ctx, r); err != nil {
			return nil, err
		}
	}
	if err := s.finish(ctx, r, cenv, inv, k, st, mode); err != nil {
		return nil, err
	}
	if inv.Status != invoice.StatusDraft {
		if err := s.flagLifetimeUsage(ctx, r, inv.OrganizationID, subs); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

// gated reports whether a pending payment rule of a subscription of rows
// gates the invoice (lago Invoices::SubscriptionService gated invoices).
func gated(ctx context.Context, r port.Repos, rows []*billed) (bool, error) {
	for _, b := range rows {
		sub := b.env.Subscription
		if sub.Status != subscription.StatusIncomplete {
			continue
		}
		rules, err := r.ActivationRules.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
		if err != nil {
			return false, fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
		}
		if subscription.PaymentGated(sub, rules) {
			return true, nil
		}
	}
	return false, nil
}

// resolveBoundaries computes the periods b bills at billingAt and its
// invoice subscription row (lago Invoices::CreateInvoiceSubscriptionService).
// ok is false when the subscription has not started.
func (s *Service) resolveBoundaries(ctx context.Context, r port.Repos, b *billed, billingAt time.Time) (bool, error) {
	env := b.env
	var d *period.Dates
	var err error
	ps := env.Period()
	upgraded := ps.Terminated() && ps.Next == period.Upgrade
	if b.item.Reason == invoice.InvoicingReasonSubscriptionTerminating && !upgraded {
		var adjusted bool
		d, adjusted, err = env.Period().TerminationDates(billingAt)
		if err != nil {
			return false, fmt.Errorf("termination dates of subscription %s: %w", env.Subscription.ID, err)
		}
		if adjusted {
			covered, err := chargesCovered(ctx, r, env, d)
			if err != nil {
				return false, err
			}
			if covered {
				d = nil
			}
		}
	}
	if d == nil {
		// An upgraded subscription bills the period it ends in (lago
		// CalculateFeesService#date_service).
		mode := period.Billing
		if upgraded {
			mode = period.CurrentUsage
		}
		if d, err = env.Dates(billingAt, mode); err != nil {
			return false, err
		}
	}
	bounds, ok := d.Boundaries()
	if !ok {
		return false, nil
	}
	b.dates, b.bounds = d, bounds
	b.is = &invoice.Subscription{
		ID:                 core.NewID[core.InvoiceSubscriptionID](s.d.IDs),
		OrganizationID:     env.OrganizationID(),
		SubscriptionID:     env.Subscription.ID,
		Reason:             b.item.Reason,
		Period:             bounds.Subscription,
		ChargesPeriod:      bounds.Charges,
		FixedChargesPeriod: bounds.FixedCharges,
		Timestamp:          billingAt.UTC(),
		Recurring:          b.item.Reason == invoice.InvoicingReasonSubscriptionPeriodic,
		CreatedAt:          s.d.Now(),
	}
	return true, nil
}

// chargesCovered reports whether a recurring invoice already covers the
// adjusted termination dates d (period.TerminationDates): a recurring row
// with the same billing period and, when the plan splits charges or fixed
// charges monthly, the same split period (lago
// TerminatedDatesService#matching_invoice_subscription?,
// terminated_dates_service.rb:43-60).
func chargesCovered(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, d *period.Dates) (bool, error) {
	b, ok := d.Boundaries()
	if !ok {
		return false, nil
	}
	rows, err := r.InvoiceSubscriptions.ListBySubscription(ctx, env.OrganizationID(), env.Subscription.ID)
	if err != nil {
		return false, fmt.Errorf("load invoices of subscription %s: %w", env.Subscription.ID, err)
	}
	for _, is := range rows {
		if !is.Recurring || !samePeriod(is.Period, b.Subscription) {
			continue
		}
		if env.Plan.ChargesBilledMonthly() && !samePeriod(is.ChargesPeriod, b.Charges) {
			continue
		}
		if env.Plan.FixedChargesBilledMonthly() && !samePeriod(is.FixedChargesPeriod, b.FixedCharges) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// samePeriod reports whether a and b have the same bounds.
func samePeriod(a, b period.Period) bool { return a.From.Equal(b.From) && a.To.Equal(b.To) }

// rowExists reports whether a row with one of the idempotency keys of is
// exists (lago's invoice_subscriptions unique indexes).
func rowExists(ctx context.Context, r port.Repos, org core.OrganizationID, is *invoice.Subscription) (bool, error) {
	for _, k := range is.Keys() {
		_, err := r.InvoiceSubscriptions.Find(ctx, org, k)
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, core.ErrNotFound):
			return false, fmt.Errorf("find invoice of subscription %s: %w", is.SubscriptionID, err)
		}
	}
	return false, nil
}

// lockSubscriptions takes the billing_schedule lock of the customers of
// the subscriptions ids, then their subscription locks ordered by ID (lago
// Invoices::SubscriptionService#lock_subscriptions!, ordered by id), so
// that billing serializes with the lifecycle changes of the same
// subscriptions (kernel lock order). The subscriptions are read again by
// the caller, under the locks.
func lockSubscriptions(ctx context.Context, r port.Repos, org core.OrganizationID, ids []core.SubscriptionID) error {
	var custs []core.CustomerID
	for _, id := range ids {
		sub, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		if !slices.Contains(custs, sub.CustomerID) {
			custs = append(custs, sub.CustomerID)
		}
	}
	slices.SortFunc(custs, func(a, b core.CustomerID) int { return strings.Compare(a.String(), b.String()) })
	for _, c := range custs {
		if err := kernel.LockCustomer(ctx, r, org, c, customer.LockScopeBillingSchedule); err != nil {
			return err
		}
	}
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, func(a, b core.SubscriptionID) int { return strings.Compare(a.String(), b.String()) })
	for _, id := range slices.Compact(sorted) {
		if err := kernel.LockSubscription(ctx, r, org, id); err != nil {
			return err
		}
	}
	return nil
}
