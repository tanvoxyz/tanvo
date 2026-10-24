package invoicing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// segmentsLock is the lock of a customer's segment invoicing (lago
// advisory lock billing_segment_process_customer_<id>).
func segmentsLock(c core.CustomerID) port.LockKey {
	return port.LockKey{Scope: "billing_segment_process", ID: c.UUID()}
}

// segmentGroupKey is what the segments sharing an invoice have in common
// (lago BillingSegments::ProcessService#invoice_key).
type segmentGroupKey struct {
	billingDate     core.Date
	segment         core.BillingSegmentID // zero when the contract consolidates
	currency        string
	billingEntity   core.BillingEntityID
	paymentMethodID core.PaymentMethodID
	paymentType     core.PaymentMethodType
	purchaseOrder   string
}

// segmentGroup is the segments billed on one invoice, in billing order.
type segmentGroup struct {
	key segmentGroupKey
	ids []core.BillingSegmentID
}

// ProcessSegments invoices the customer's billing segments awaiting it,
// one invoice per group sharing a billing date, currency, billing entity,
// payment method and purchase order number (each segment of a contract
// that does not consolidate alone), then finalizes the invoices of its
// invoiced segments still generating, so that a retry finalizes what a
// failed run built (lago BillingSegments::ProcessService). Each group is
// invoiced in its own transaction under the customer's segment lock.
//
// source: app/services/billing_segments/process_service.rb:12-30
func (s *Service) ProcessSegments(ctx context.Context, org core.OrganizationID, c core.CustomerID) ([]*invoice.Invoice, error) {
	var groups []segmentGroup
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.Locks.Lock(ctx, org, segmentsLock(c)); err != nil {
			return fmt.Errorf("lock segments of customer %s: %w", c, err)
		}
		var err error
		groups, err = s.segmentGroups(ctx, r, org, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	var out []*invoice.Invoice
	for _, g := range groups {
		var inv *invoice.Invoice
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Locks.Lock(ctx, org, segmentsLock(c)); err != nil {
				return fmt.Errorf("lock segments of customer %s: %w", c, err)
			}
			var err error
			inv, err = s.invoiceSegments(ctx, r, org, c, g)
			return err
		})
		if err != nil {
			return out, err
		}
		if inv != nil {
			out = append(out, inv)
		}
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.Locks.Lock(ctx, org, segmentsLock(c)); err != nil {
			return fmt.Errorf("lock segments of customer %s: %w", c, err)
		}
		return s.finalizeSegmentInvoices(ctx, r, org, c)
	})
	return out, err
}

// segmentGroups returns the customer's segments awaiting invoicing grouped
// by invoice, groups in the order of their first segment.
//
// source: app/services/billing_segments/process_service.rb:36-65
func (s *Service) segmentGroups(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) ([]segmentGroup, error) {
	segs, err := r.BillingSegments.ListAwaitingInvoicing(ctx, org, c)
	if err != nil || len(segs) == 0 {
		if err != nil {
			err = fmt.Errorf("list segments awaiting invoicing of %s: %w", c, err)
		}
		return nil, err
	}
	cenv, err := kernel.LoadCustomerEnv(ctx, r, org, c, core.BillingEntityID{})
	if err != nil {
		return nil, err
	}
	def, err := r.PaymentMethods.GetDefault(ctx, org, c)
	if errors.Is(err, core.ErrNotFound) {
		def, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load default payment method of %s: %w", c, err)
	}
	contracts := map[core.SubscriptionID]*subscription.Subscription{}
	var groups []segmentGroup
	for _, seg := range segs {
		ct, ok := contracts[seg.SubscriptionID]
		if !ok {
			if ct, err = r.Subscriptions.Get(ctx, org, seg.SubscriptionID); err != nil {
				return nil, fmt.Errorf("load contract %s: %w", seg.SubscriptionID, err)
			}
			contracts[seg.SubscriptionID] = ct
		}
		key := segmentGroupKey{
			billingDate:   core.DateOf(seg.BillingAt, cenv.Location),
			currency:      seg.Currency.Code(),
			billingEntity: ct.BillingEntityID,
		}
		if !ct.ConsolidateInvoice {
			key.segment = seg.ID
		}
		if key.billingEntity.IsZero() {
			key.billingEntity = cenv.Customer.BillingEntityID
		}
		if ct.PurchaseOrderNumber != nil {
			key.purchaseOrder = *ct.PurchaseOrderNumber
		}
		switch {
		case !ct.PaymentMethodID.IsZero():
			key.paymentMethodID, key.paymentType = ct.PaymentMethodID, ct.PaymentMethodType
		case ct.PaymentMethodType == core.PaymentMethodTypeManual:
			key.paymentType = core.PaymentMethodTypeManual
		case def != nil:
			key.paymentMethodID, key.paymentType = def.ID, core.PaymentMethodTypeProvider
		default:
			key.paymentType = ct.PaymentMethodType
		}
		i := slices.IndexFunc(groups, func(g segmentGroup) bool { return g.key == key })
		if i < 0 {
			groups = append(groups, segmentGroup{key: key})
			i = len(groups) - 1
		}
		groups[i].ids = append(groups[i].ids, seg.ID)
	}
	return groups, nil
}

// billedSegment is a segment being invoiced with what its fees need.
type billedSegment struct {
	fees.ProductSegment
}

// invoiceSegments builds the invoice of the group's segments still
// awaiting invoicing: a generating subscription invoice of the first
// segment's billing instant and contract, one invoice subscription per
// contract, the fees of the fixed products then of the metered ones, the
// local taxes and totals, the contracts' custom sections; the segments
// are then done with the invoice (lago
// BillingSegments::ProcessService#build_invoice). It returns nil when no
// segment of the group awaits invoicing any more.
//
// source: app/services/billing_segments/process_service.rb:67-128
func (s *Service) invoiceSegments(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, g segmentGroup) (*invoice.Invoice, error) {
	awaiting, err := r.BillingSegments.ListAwaitingInvoicing(ctx, org, c)
	if err != nil {
		return nil, fmt.Errorf("list segments awaiting invoicing of %s: %w", c, err)
	}
	awaiting = slices.DeleteFunc(awaiting, func(seg *subscription.BillingSegment) bool { return !slices.Contains(g.ids, seg.ID) })
	if len(awaiting) == 0 {
		return nil, nil
	}
	var (
		fixed, metered []billedSegment
		contracts      []*subscription.Subscription
		cenv           *kernel.CustomerEnv
	)
	for _, seg := range awaiting {
		bs, err := s.loadBilledSegment(ctx, r, seg, contracts)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(contracts, bs.Contract) {
			contracts = append(contracts, bs.Contract)
		}
		if bs.Product.ProductType == catalog.ProductTypeFixed {
			fixed = append(fixed, bs)
		} else {
			metered = append(metered, bs)
		}
	}
	first := awaiting[0]
	contract := contracts[slices.IndexFunc(contracts, func(ct *subscription.Subscription) bool { return ct.ID == first.SubscriptionID })]
	if cenv, err = kernel.LoadCustomerEnv(ctx, r, org, c, contract.BillingEntityID); err != nil {
		return nil, err
	}
	k := subscriptionKind(true)
	inv := kernel.NewInvoice(s.d, cenv, k, first.Currency, first.BillingAt)
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(contract.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	for _, ct := range contracts {
		if err := s.linkContract(ctx, r, inv, ct, awaiting); err != nil {
			return nil, err
		}
	}
	// The metered segments' events are read once for the group, so a
	// segment none of them reaches is priced without aggregating.
	//
	// source: app/services/billing_segments/process_service.rb:84-87, :138-144
	if len(metered) > 0 {
		pss := make([]fees.ProductSegment, len(metered))
		for i := range metered {
			metered[i].Location = cenv.Location
			pss[i] = metered[i].ProductSegment
		}
		events, err := s.fees.SegmentEvents(ctx, r, pss)
		if err != nil {
			return nil, err
		}
		for i := range metered {
			metered[i].Events = events
		}
	}
	var fs []*invoice.Fee
	for _, bs := range slices.Concat(fixed, metered) {
		bs.Location = cenv.Location
		segFees, err := s.fees.ProductFees(ctx, r, inv, bs.ProductSegment)
		if err != nil {
			return nil, err
		}
		for _, f := range segFees {
			if slices.ContainsFunc(fs, func(o *invoice.Fee) bool { return o.ID == f.ID }) {
				continue
			}
			if err := f.Validate(); err != nil {
				return nil, fmt.Errorf("fee of segment %s: %w", bs.Segment.ID, err)
			}
			if err := r.Fees.Create(ctx, f); err != nil {
				return nil, fmt.Errorf("create fee of segment %s: %w", bs.Segment.ID, err)
			}
			fs = append(fs, f)
		}
	}
	if _, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{segments: true}); err != nil {
		return nil, err
	}
	sections := make([]kernel.CustomSectionResource, len(contracts))
	for i, ct := range contracts {
		sections[i] = kernel.SubscriptionSectionResource(ct)
	}
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil, sections...); err != nil {
		return nil, err
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return nil, fmt.Errorf("save invoice %s: %w", inv.ID, err)
	}
	now := s.d.Now()
	for _, seg := range awaiting {
		seg.Status, seg.InvoiceID, seg.UpdatedAt = subscription.SegmentDone, inv.ID, now
		if err := r.BillingSegments.Update(ctx, seg); err != nil {
			return nil, fmt.Errorf("mark segment %s done: %w", seg.ID, err)
		}
	}
	return inv, nil
}

// loadBilledSegment loads the contract (from loaded when there), card,
// rate card and product of seg.
func (s *Service) loadBilledSegment(ctx context.Context, r port.Repos, seg *subscription.BillingSegment, loaded []*subscription.Subscription) (billedSegment, error) {
	org := seg.OrganizationID
	bs := billedSegment{ProductSegment: fees.ProductSegment{Segment: seg}}
	if i := slices.IndexFunc(loaded, func(ct *subscription.Subscription) bool { return ct.ID == seg.SubscriptionID }); i >= 0 {
		bs.Contract = loaded[i]
	} else {
		ct, err := r.Subscriptions.Get(ctx, org, seg.SubscriptionID)
		if err != nil {
			return bs, fmt.Errorf("load contract %s: %w", seg.SubscriptionID, err)
		}
		bs.Contract = ct
	}
	card, err := r.ContractRateCards.Get(ctx, org, seg.RateCardID)
	if err != nil {
		return bs, fmt.Errorf("load contract rate card %s: %w", seg.RateCardID, err)
	}
	bs.Card = card
	if bs.RateCard, err = r.RateCards.Get(ctx, org, card.RateCardID); err != nil {
		return bs, fmt.Errorf("load rate card %s: %w", card.RateCardID, err)
	}
	if bs.Product, err = r.Products.Get(ctx, org, bs.RateCard.ProductID); err != nil {
		return bs, fmt.Errorf("load product %s: %w", bs.RateCard.ProductID, err)
	}
	return bs, nil
}

// linkContract links inv to contract ct with a periodic invoice
// subscription spanning ct's segments on it, billed at the first one's
// instant. It has no charges or fixed charges period, so no idempotency
// key: the segments are what the invoice bills (domain §24, item 3).
func (s *Service) linkContract(ctx context.Context, r port.Repos, inv *invoice.Invoice, ct *subscription.Subscription, segs []*subscription.BillingSegment) error {
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		SubscriptionID: ct.ID, Reason: invoice.InvoicingReasonSubscriptionPeriodic, Recurring: true, CreatedAt: s.d.Now(),
	}
	var p period.Period
	for _, seg := range segs {
		if seg.SubscriptionID != ct.ID {
			continue
		}
		if p.From.IsZero() || seg.StartedAt.Before(p.From) {
			p.From = seg.StartedAt
		}
		if seg.EndedAt.After(p.To) {
			p.To = seg.EndedAt
		}
		if is.Timestamp.IsZero() {
			is.Timestamp = seg.BillingAt
		}
	}
	is.Period = p
	if err := is.Validate(); err != nil {
		return err
	}
	if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
		return fmt.Errorf("create invoice subscription of %s: %w", ct.ID, err)
	}
	return nil
}

// finalizeSegmentInvoices moves the still-generating invoices of the
// customer's invoiced segments to their final status: finalized, or
// closed for a zero-amount invoice the customer does not finalize, with
// no grace period (lago Invoices::TransitionToFinalStatusService from
// BillingSegments::ProcessService#finalize_generating_invoices).
//
// source: app/services/billing_segments/process_service.rb:145-158, app/services/invoices/transition_to_final_status_service.rb:13-29
func (s *Service) finalizeSegmentInvoices(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) error {
	ids, err := r.BillingSegments.GeneratingInvoiceIDs(ctx, org, c)
	if err != nil {
		return fmt.Errorf("list generating segment invoices of %s: %w", c, err)
	}
	for _, id := range ids {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		cenv, err := kernel.LoadCustomerEnv(ctx, r, org, c, inv.BillingEntityID)
		if err != nil {
			return err
		}
		if err := s.conclude(ctx, r, cenv, inv, subscriptionKind(true), settled{}, true); err != nil {
			return err
		}
	}
	return nil
}

// enqueueAwaitingCustomers enqueues the segment invoicing of every
// customer with a segment awaiting it (lago
// Clock::ProcessBillingSegmentsJob).
//
// source: app/jobs/clock/process_billing_segments_job.rb:7-19
func (s *Service) enqueueAwaitingCustomers(ctx context.Context, org core.OrganizationID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ids, err := r.BillingSegments.CustomersAwaitingInvoicing(ctx, org)
		if err != nil {
			return fmt.Errorf("list customers awaiting segment invoicing: %w", err)
		}
		for _, id := range ids {
			if err := kernel.EnqueueProcessSegments(ctx, r, org, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// HandleSegmentJob runs the segment invoicing jobs
// ([kernel.JobProcessBillingSegmentsAll] and
// [kernel.JobProcessBillingSegments]) and reports whether j is one.
func (s *Service) HandleSegmentJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobProcessBillingSegmentsAll:
		return true, s.enqueueAwaitingCustomers(ctx, j.OrganizationID)
	case kernel.JobProcessBillingSegments:
		var a kernel.CustomerArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		_, err := s.ProcessSegments(ctx, j.OrganizationID, a.CustomerID)
		return true, err
	}
	return false, nil
}
