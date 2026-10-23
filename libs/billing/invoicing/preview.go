package invoicing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Codes of the rule errors of previews.
const (
	// CodeNoSubscriptions rejects a preview without subscriptions.
	CodeNoSubscriptions = "no_subscriptions"
	// CodePreviewCurrencies rejects subscriptions in several currencies.
	CodePreviewCurrencies = "subscription_currencies_does_not_match"
	// CodePreviewBillingPeriods rejects subscriptions billed on different
	// days.
	CodePreviewBillingPeriods = "billing_periods_does_not_match"
	// CodePreviewBillingEntities rejects subscriptions of several billing
	// entities.
	CodePreviewBillingEntities = "subscription_billing_entities_do_not_match"
)

// PreviewInput asks for the next invoice of a customer (lago
// Invoices::PreviewService): either of stored subscriptions (their next
// periodic invoice, or the termination invoice of terminated ones) or of
// subscriptions not created yet (their first invoice: at the start for
// a plan paid in advance, else at the end of the first period).
type PreviewInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// SubscriptionIDs are stored subscriptions of the customer.
	SubscriptionIDs []core.SubscriptionID
	// New are subscriptions not stored (their PlanID, SubscriptionAt and
	// BillingTime at least); ignored when SubscriptionIDs are given.
	New []*subscription.Subscription

	// sim simulates a request of [Service.PreviewRequest] in the
	// rolled-back transaction; nil for a plain preview.
	sim *previewSim
}

// Preview is a computed invoice that was not stored.
type Preview struct {
	// Invoice is the invoice, with its amounts and taxes.
	Invoice *invoice.Invoice
	// Fees are its fees.
	Fees []*invoice.Fee
	// Taxes are its applied taxes.
	Taxes []*invoice.AppliedTax
	// Subscriptions are its invoice subscriptions.
	Subscriptions []*invoice.Subscription
	// Credits are its coupon and credit note credits (computed only).
	Credits []*invoice.Credit

	// view is the API view built in the transaction ([Service.PreviewRequest]).
	view *InvoiceView
}

// errPreview rolls back the transaction of a preview.
var errPreview = errors.New("invoicing: preview rollback")

// Preview computes the next invoice of in without storing anything: the
// invoice is generated in a transaction that is rolled back, coupons and
// credits are only simulated. It must not run inside a transaction.
func (s *Service) Preview(ctx context.Context, in PreviewInput) (*Preview, error) {
	var out *Preview
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if out, err = s.preview(ctx, r, in); err != nil {
			return err
		}
		return errPreview
	})
	if err != nil && !errors.Is(err, errPreview) {
		return nil, err
	}
	return out, nil
}

// preview is [Service.Preview] in the transaction of r.
func (s *Service) preview(ctx context.Context, r port.Repos, in PreviewInput) (*Preview, error) {
	org := in.OrganizationID
	persisted := len(in.SubscriptionIDs) > 0
	ids := in.SubscriptionIDs
	var simStarting map[core.SubscriptionID]bool
	if in.sim != nil {
		var err error
		if ids, simStarting, err = in.sim.prepare(ctx, r, s, in); err != nil {
			return nil, err
		}
		persisted = persisted || simStarting != nil
	}
	if !persisted {
		if len(in.New) == 0 {
			return nil, core.NewRuleError(CodeNoSubscriptions, "a preview needs subscriptions")
		}
		now := s.d.Now()
		for _, ns := range in.New {
			sub := *ns
			sub.ID = core.NewID[core.SubscriptionID](s.d.IDs)
			sub.OrganizationID, sub.CustomerID = org, in.CustomerID
			if sub.ExternalID == "" {
				sub.ExternalID = "preview-" + sub.ID.String()
			}
			if sub.SubscriptionAt.IsZero() {
				sub.SubscriptionAt = now
			}
			started := sub.SubscriptionAt
			sub.Status, sub.StartedAt = subscription.StatusActive, &started
			sub.CreatedAt, sub.UpdatedAt = now, now
			if err := r.Subscriptions.Create(ctx, &sub); err != nil {
				return nil, fmt.Errorf("create preview subscription: %w", err)
			}
			ids = append(ids, sub.ID)
		}
	}
	if len(ids) == 0 {
		return nil, core.NewRuleError(CodeNoSubscriptions, "a preview needs subscriptions")
	}
	rows := make([]*billed, len(ids))
	for i, id := range ids {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, id)
		if err != nil {
			return nil, err
		}
		if env.Subscription.CustomerID != in.CustomerID {
			return nil, fmt.Errorf("preview subscription %s: %w", id, core.ErrNotFound)
		}
		rows[i] = &billed{env: env}
	}
	starting := map[core.SubscriptionID]bool{}
	if simStarting != nil {
		starting = simStarting
	} else if persisted {
		var err error
		if rows, starting, err = s.previewDowngrades(ctx, r, org, rows); err != nil {
			return nil, err
		}
	}
	first := rows[0].env
	for _, b := range rows[1:] {
		switch {
		case b.env.Plan.Currency != first.Plan.Currency:
			return nil, core.NewRuleError(CodePreviewCurrencies, "the subscriptions' currencies differ")
		case b.env.Subscription.ApplicableBillingEntity(b.env.Customer.Customer.BillingEntityID) !=
			first.Subscription.ApplicableBillingEntity(first.Customer.Customer.BillingEntityID):
			return nil, core.NewRuleError(CodePreviewBillingEntities, "the subscriptions' billing entities differ")
		}
	}
	billingAt, reason, err := previewInstant(first, persisted, s.d.Now())
	if err != nil {
		return nil, err
	}
	for _, b := range rows[1:] {
		if starting[b.env.Subscription.ID] {
			continue
		}
		at, _, err := previewInstant(b.env, persisted, s.d.Now())
		if err != nil {
			return nil, err
		}
		if core.DateOf(at, b.env.Location()) != core.DateOf(billingAt, first.Location()) {
			return nil, core.NewRuleError(CodePreviewBillingPeriods, "the subscriptions are billed on different days")
		}
	}
	// The termination credit note returns the last stored subscription
	// fee, so it is computed before the preview's own fees.
	var note *previewNote
	if in.sim != nil {
		if note, err = in.sim.terminationNote(ctx, r, s); err != nil {
			return nil, err
		}
	}
	cenv := first.Customer
	k := subscriptionKind(reason == invoice.InvoicingReasonSubscriptionPeriodic)
	inv := kernel.NewInvoice(s.d, cenv, k, first.Plan.Currency, billingAt)
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(first.Subscription.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create preview invoice: %w", err)
	}
	plans := map[core.SubscriptionID]*catalog.Plan{}
	resolver := kernel.NewTaxResolver(r, cenv)
	var iss []*invoice.Subscription
	for _, b := range rows {
		b.item = BillItem{SubscriptionID: b.env.Subscription.ID, Reason: reason}
		if starting[b.env.Subscription.ID] {
			b.item.Reason = invoice.InvoicingReasonSubscriptionStarting
		}
		ok, err := s.resolveBoundaries(ctx, r, b, billingAt)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		b.is.InvoiceID = inv.ID
		if err := r.InvoiceSubscriptions.Create(ctx, b.is); err != nil {
			return nil, fmt.Errorf("create preview invoice subscription: %w", err)
		}
		plans[b.env.Subscription.ID] = b.env.Plan
		resolver.SetPlan(b.env.Subscription.ID, b.env.Plan)
		iss = append(iss, b.is)
	}
	var fs []*invoice.Fee
	if len(iss) > 0 {
		var used []*billed
		for _, b := range rows {
			if b.is != nil {
				used = append(used, b)
			}
		}
		// The fees of a termination are those of an invoice created when
		// the subscription terminates (lago PreviewService
		// #should_create_subscription_fee?: none for a plan paid in advance).
		created := inv.CreatedAt
		if reason == invoice.InvoicingReasonSubscriptionTerminating && billingAt.After(created) {
			inv.CreatedAt = billingAt
		}
		fs, _, err = s.subscriptionFees(ctx, r, inv, used, !persisted)
		inv.CreatedAt = created
		if err != nil {
			return nil, err
		}
	}
	opts := settleOptions{preview: true, plans: plans, resolver: resolver, previewCreditNote: note}
	st, err := s.settle(ctx, r, cenv, inv, fs, opts)
	if err != nil {
		return nil, err
	}
	if st.taxFailure != nil {
		return nil, st.taxFailure
	}
	p := &Preview{Invoice: inv, Fees: fs, Taxes: st.appliedTaxes, Subscriptions: iss, Credits: st.credits}
	if in.sim != nil {
		if p.view, err = in.sim.view(ctx, r, p, rows, opts.previewCreditNote); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// previewDowngrades simulates, in the rolled-back transaction of r, the
// swap of the pending downgrade of each row at the end of its current
// period (lago Invoices::Preview::FindSubscriptionsService): the
// subscription terminates then and its downgrade starts, previewed after
// it when its plan is paid in advance. It returns the rows and the IDs of
// the starting downgrades.
func (s *Service) previewDowngrades(ctx context.Context, r port.Repos, org core.OrganizationID, rows []*billed) ([]*billed, map[core.SubscriptionID]bool, error) {
	out := make([]*billed, 0, len(rows))
	starting := map[core.SubscriptionID]bool{}
	for _, b := range rows {
		next := b.env.Next
		if next == nil || next.Status != subscription.StatusPending {
			out = append(out, b)
			continue
		}
		d, err := b.env.Dates(s.d.Now(), period.CurrentUsage)
		if err != nil {
			return nil, nil, err
		}
		end := d.CurrentPeriodEnd()
		sub, ns := *b.env.Subscription, *next
		sub.Status, sub.TerminatedAt = subscription.StatusTerminated, &end
		ns.Status, ns.StartedAt = subscription.StatusActive, &end
		for _, x := range []*subscription.Subscription{&sub, &ns} {
			if err := r.Subscriptions.Update(ctx, x); err != nil {
				return nil, nil, fmt.Errorf("simulate downgrade of subscription %s: %w", sub.ID, err)
			}
		}
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, sub.ID)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, &billed{env: env})
		nenv, err := kernel.LoadSubscriptionEnv(ctx, r, org, ns.ID)
		if err != nil {
			return nil, nil, err
		}
		if nenv.Plan.PayInAdvance {
			out = append(out, &billed{env: nenv})
			starting[ns.ID] = true
		}
	}
	return out, starting, nil
}

// previewInstant returns the billing instant and reason of the preview of
// one subscription.
func previewInstant(env *kernel.SubscriptionEnv, persisted bool, now time.Time) (time.Time, invoice.InvoicingReason, error) {
	sub := env.Subscription
	switch {
	case sub.Status == subscription.StatusTerminated && sub.TerminatedAt != nil:
		return *sub.TerminatedAt, invoice.InvoicingReasonSubscriptionTerminating, nil
	case !persisted && env.Plan.PayInAdvance:
		return sub.SubscriptionAt, invoice.InvoicingReasonSubscriptionStarting, nil
	}
	from := now
	if !persisted {
		from = sub.SubscriptionAt
	}
	d, err := env.Dates(from, period.CurrentUsage)
	if err != nil {
		return time.Time{}, "", err
	}
	return d.CurrentPeriodEnd(), invoice.InvoicingReasonSubscriptionPeriodic, nil
}
