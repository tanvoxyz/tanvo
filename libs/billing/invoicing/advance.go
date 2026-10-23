package invoicing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// AdvanceCharges regroups, in the transaction of r, the paid fees of
// non-invoiceable pay-in-advance charges regrouping their paid fees (lago
// Invoices::AdvanceChargesService) of the subscriptions subs (one
// customer, and the subscriptions sharing their external IDs) on one
// advance charges invoice at billingAt. The invoice sums the fees' own
// taxes, is already paid, and gets no deduction. It returns nil when
// there is nothing to regroup. On a regular periodic run, only the fees
// whose charges period ended are taken.
func (s *Service) AdvanceCharges(ctx context.Context, r port.Repos, org core.OrganizationID, subs []core.SubscriptionID, billingAt time.Time) (*invoice.Invoice, error) {
	if len(subs) == 0 {
		return nil, nil
	}
	if err := lockSubscriptions(ctx, r, org, subs); err != nil {
		return nil, err
	}
	first, err := r.Subscriptions.Get(ctx, org, subs[0])
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", subs[0], err)
	}
	asked := map[string]bool{}
	regular := true
	for _, id := range subs {
		sub, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load subscription %s: %w", id, err)
		}
		asked[sub.ExternalID] = true
		if sub.Status != subscription.StatusActive || terminated(sub) {
			regular = false
		} else if next, err := r.Subscriptions.GetNext(ctx, org, sub.ID); err == nil && next != nil {
			regular = false
		}
	}
	all, err := r.Subscriptions.ListByCustomer(ctx, org, first.CustomerID, subscription.StatusActive, subscription.StatusTerminated)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of customer %s: %w", first.CustomerID, err)
	}
	charges := map[core.ChargeID]*catalog.Charge{}
	var paid []*invoice.Fee
	for _, sub := range all {
		if !asked[sub.ExternalID] {
			continue
		}
		fs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
			return r.Fees.List(ctx, org, invoice.FeeFilter{SubscriptionID: sub.ID, Types: []core.FeeType{core.FeeTypeCharge}}, p)
		})
		if err != nil {
			return nil, fmt.Errorf("load fees of subscription %s: %w", sub.ID, err)
		}
		for _, f := range fs {
			if !f.InvoiceID.IsZero() || !f.PayInAdvance || f.PaymentStatus != invoice.FeePaymentStatusSucceeded ||
				f.SucceededAt == nil || f.SucceededAt.After(billingAt) {
				continue
			}
			if regular && !f.Properties.ChargesTo.IsZero() && f.Properties.ChargesTo.After(billingAt) {
				continue
			}
			c, ok := charges[f.ChargeID]
			if !ok {
				if c, err = r.Charges.Get(ctx, org, f.ChargeID); err != nil {
					return nil, fmt.Errorf("load charge %s: %w", f.ChargeID, err)
				}
				charges[f.ChargeID] = c
			}
			if c.Invoiceable || c.RegroupPaidFees != catalog.RegroupPaidFeesInvoice {
				continue
			}
			paid = append(paid, f)
		}
	}
	if len(paid) == 0 {
		return nil, nil
	}
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, first.CustomerID, first.ApplicableBillingEntity(core.BillingEntityID{}))
	if err != nil {
		return nil, err
	}
	inv := kernel.NewInvoice(s.d, cenv, kindAdvanceCharges, paid[0].Currency, billingAt)
	// The subscriptions share one grouping key, so one purchase order
	// number (lago advance_charges_service.rb:78,120).
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(first.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	if err := s.advanceChargesPeriods(ctx, r, inv, all, asked, paid, billingAt); err != nil {
		return nil, err
	}
	for _, f := range paid {
		f.InvoiceID = inv.ID
	}
	st, err := s.settle(ctx, r, cenv, inv, paid, settleOptions{aggregate: true})
	if err != nil {
		return nil, err
	}
	// advance_charges_service.rb:102: the customer's sections.
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil); err != nil {
		return nil, err
	}
	inv.PaymentStatus = core.PaymentStatusSucceeded
	if err := s.finish(ctx, r, cenv, inv, kindAdvanceCharges, st, modeNew); err != nil {
		return nil, err
	}
	return inv, nil
}

// advanceChargesPeriods links advance charges invoice inv to each
// subscription of the paid fees (lago
// Invoices::CreateAdvanceChargesInvoiceService): every link covers, as
// both its periods, the charges period at billingAt of the latest
// subscription of the asked external IDs, the latest started one not yet
// terminated, else the last terminated one.
func (s *Service) advanceChargesPeriods(ctx context.Context, r port.Repos, inv *invoice.Invoice, all []*subscription.Subscription, asked map[string]bool, paid []*invoice.Fee, billingAt time.Time) error {
	var latest *subscription.Subscription
	var ended *subscription.Subscription
	for _, sub := range all {
		if !asked[sub.ExternalID] {
			continue
		}
		if sub.TerminatedAt != nil && !sub.TerminatedAt.After(billingAt) {
			if ended == nil || sub.TerminatedAt.After(*ended.TerminatedAt) {
				ended = sub
			}
			continue
		}
		if latest == nil || (sub.StartedAt != nil && (latest.StartedAt == nil || sub.StartedAt.After(*latest.StartedAt))) {
			latest = sub
		}
	}
	if latest == nil {
		latest = ended
	}
	if latest == nil {
		return nil
	}
	env, err := kernel.LoadSubscriptionEnv(ctx, r, inv.OrganizationID, latest.ID)
	if err != nil {
		return err
	}
	d, err := env.Dates(billingAt, period.Billing)
	if err != nil {
		return err
	}
	cp, _ := d.ChargesPeriod()
	linked := map[core.SubscriptionID]bool{}
	for _, f := range paid {
		if linked[f.SubscriptionID] {
			continue
		}
		linked[f.SubscriptionID] = true
		is := &invoice.Subscription{
			ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
			SubscriptionID: f.SubscriptionID, Reason: invoice.InvoicingReasonInAdvanceChargePeriodic,
			Timestamp: billingAt.UTC(), Period: cp, ChargesPeriod: cp, CreatedAt: s.d.Now(),
		}
		if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
			return fmt.Errorf("create invoice subscription: %w", err)
		}
	}
	return nil
}

// InAdvanceCharge bills one event for the pay-in-advance charges of its
// subscription on the event's metric (lago Fees::CreatePayInAdvanceService
// and Invoices::CreatePayInAdvanceChargeService): the fees of an
// invoiceable charge are put on their own subscription invoice, issued
// on the event's date and finalized at once; the fees of other charges
// are stored without invoice, taxed, with a pending payment, and
// announced with fee.created. An event already billed for a charge is
// skipped. It returns the fees.
func (s *Service) InAdvanceCharge(ctx context.Context, org core.OrganizationID, a kernel.InAdvanceChargeArgs) ([]*invoice.Fee, error) {
	var out []*invoice.Fee
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = s.inAdvanceCharge(ctx, r, org, a)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// inAdvanceCharge is [Service.InAdvanceCharge] in the transaction of r.
func (s *Service) inAdvanceCharge(ctx context.Context, r port.Repos, org core.OrganizationID, a kernel.InAdvanceChargeArgs) ([]*invoice.Fee, error) {
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, a.SubscriptionID)
	if err != nil {
		return nil, err
	}
	charges, err := env.Charges(ctx, r)
	if err != nil {
		return nil, err
	}
	// The event lock makes the check below and the fees it guards one step
	// (lago's unique index idx_pay_in_advance_duplication_guard_charge).
	if err := kernel.LockEvent(ctx, r, org, a.SubscriptionID, a.TransactionID); err != nil {
		return nil, err
	}
	billed, err := r.Fees.ListByEventTransaction(ctx, org, a.SubscriptionID, a.TransactionID)
	if err != nil {
		return nil, fmt.Errorf("load fees of event %s: %w", a.TransactionID, err)
	}
	ev := metering.Event{
		OrganizationID: org.String(), SubscriptionID: env.Subscription.ExternalID, Code: a.Code,
		TransactionID: a.TransactionID, Timestamp: a.Timestamp.UTC(), Properties: metering.Properties(a.Properties),
		PreciseTotalAmountCents: a.PreciseTotalAmountCents,
	}
	var out []*invoice.Fee
	for _, c := range charges {
		if !c.PayInAdvance || alreadyBilled(billed, c.ID) {
			continue
		}
		m, err := r.Metrics.Get(ctx, org, c.BillableMetricID)
		if err != nil {
			return nil, fmt.Errorf("load billable metric %s: %w", c.BillableMetricID, err)
		}
		if m.Code != a.Code {
			continue
		}
		res, err := s.fees.InAdvanceChargeFees(ctx, r, env, c, fees.InAdvanceEvent{ID: a.EventID, Event: ev}, fees.Options{Cache: aggregationCache(r, env)})
		if err != nil {
			return nil, err
		}
		if err := s.cacheAggregations(ctx, r, org, res, true); err != nil {
			return nil, err
		}
		if res == nil || len(res.Fees) == 0 {
			continue
		}
		if c.Invoiceable {
			if _, err := s.inAdvanceInvoice(ctx, r, env, res.Fees, a.Timestamp); err != nil {
				return nil, err
			}
		} else {
			if err := s.taxStandalone(ctx, r, env.Customer, env, res.Fees); err != nil {
				return nil, err
			}
			for _, f := range res.Fees {
				f.PaymentStatus = invoice.FeePaymentStatusPending
				if err := r.Fees.Create(ctx, f); err != nil {
					return nil, fmt.Errorf("create fee: %w", err)
				}
			}
			if err := s.emitFees(ctx, res.Fees); err != nil {
				return nil, err
			}
		}
		out = append(out, res.Fees...)
	}
	return out, nil
}

// alreadyBilled reports whether fees hold a fee of the charge.
func alreadyBilled(fs []*invoice.Fee, c core.ChargeID) bool {
	for _, f := range fs {
		if f.ChargeID == c {
			return true
		}
	}
	return false
}

// inAdvanceInvoice issues the invoice of the pay-in-advance fees of one
// event (lago Invoices::CreatePayInAdvanceChargeService).
func (s *Service) inAdvanceInvoice(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, fs []*invoice.Fee, ts time.Time) (*invoice.Invoice, error) {
	cenv := env.Customer
	inv := kernel.NewInvoice(s.d, cenv, kindInAdvance, env.Plan.Currency, ts)
	inv.PaymentMethodType, inv.PaymentMethodID = env.Subscription.PaymentMethodType, env.Subscription.PaymentMethodID
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(env.Subscription.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	props := fs[0].Properties
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		SubscriptionID: env.Subscription.ID, Reason: invoice.InvoicingReasonInAdvanceCharge,
		Timestamp: ts.UTC(), CreatedAt: s.d.Now(),
	}
	is.Period.From, is.Period.To = props.From, props.To
	is.ChargesPeriod.From, is.ChargesPeriod.To = props.ChargesFrom, props.ChargesTo
	if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
		return nil, fmt.Errorf("create invoice subscription: %w", err)
	}
	for _, f := range fs {
		f.InvoiceID = inv.ID
		if err := r.Fees.Create(ctx, f); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
	}
	// The subscription's selection, applied before the taxes
	// (create_pay_in_advance_charge_service.rb:30-35).
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil, kernel.SubscriptionSectionResource(env.Subscription)); err != nil {
		return nil, err
	}
	resolver := kernel.NewTaxResolver(r, cenv)
	resolver.SetPlan(env.Subscription.ID, env.Plan)
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{resolver: resolver, vies: true})
	if err != nil {
		return nil, err
	}
	if err := s.finish(ctx, r, cenv, inv, kindInAdvance, st, modeNew); err != nil {
		return nil, err
	}
	return inv, kernel.Emit(ctx, s.d, inv.OrganizationID, webhook.EventTypeFeeCreated, "fee", kernel.ObjectID(fs[0].ID), fs[0])
}
