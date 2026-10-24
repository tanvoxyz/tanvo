package invoicing

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
)

// ContractSegments resolves the in-advance metered segments of a
// contract that price an event (the contracts service).
type ContractSegments interface {
	// InAdvanceSegments returns the unsaved segments of contract ct that
	// price an event of metric m at ts.
	InAdvanceSegments(ctx context.Context, r port.Repos, ct *subscription.Subscription, m *catalog.BillableMetric, ts time.Time) ([]fees.ProductSegment, error)
}

// WithContractSegments makes the service bill the events of contracts in
// advance through c, and returns it.
func (s *Service) WithContractSegments(c ContractSegments) *Service {
	s.contractSegments = c
	return s
}

// canCreateFee reports whether an event with properties props can bill a
// fee of metric m in advance: a count needs no property, any other
// aggregation its field (lago Events::PayInAdvanceService#can_create_fee?,
// whose custom aggregation exemption has no counterpart: custom
// aggregation is not ported). It guards charges and contracts alike.
//
// source: app/services/events/pay_in_advance_service.rb:69-74
func canCreateFee(m *catalog.BillableMetric, props map[string]string) bool {
	if m.AggregationType == catalog.AggregationTypeCount {
		return true
	}
	return m.FieldName != nil && strings.TrimSpace(props[*m.FieldName]) != ""
}

// inAdvanceContract bills the event of a in advance on the contract ct
// (lago Events::PayInAdvanceService over the billing segment selections):
// one fee per in-advance segment of the event's metric whose card has
// not billed the event yet. An invoiceable fee (the rate card shows on
// invoices) is invoiced on its own; another is stored with its taxes.
//
// source: app/services/events/pay_in_advance_service.rb:12-84, app/services/events/pay_in_advance_metered_items_resolver.rb:40-49
func (s *Service) inAdvanceContract(ctx context.Context, r port.Repos, ct *subscription.Subscription, a kernel.InAdvanceChargeArgs) ([]*invoice.Fee, error) {
	if s.contractSegments == nil {
		return nil, nil
	}
	org := ct.OrganizationID
	m, err := r.Metrics.GetByCode(ctx, org, a.Code)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load billable metric %s: %w", a.Code, err)
	}
	if !canCreateFee(m, a.Properties) {
		return nil, nil
	}
	segs, err := s.contractSegments.InAdvanceSegments(ctx, r, ct, m, a.Timestamp)
	if err != nil || len(segs) == 0 {
		return nil, err
	}
	// The event lock makes the check below and the fees it guards one step
	// (lago's unique index idx_pay_in_advance_duplication_guard_contract).
	if err := kernel.LockEvent(ctx, r, org, ct.ID, a.TransactionID); err != nil {
		return nil, err
	}
	billed, err := r.Fees.ListByEventTransaction(ctx, org, ct.ID, a.TransactionID)
	if err != nil {
		return nil, fmt.Errorf("load fees of event %s: %w", a.TransactionID, err)
	}
	ev := fees.InAdvanceEvent{ID: a.EventID, Event: metering.Event{
		OrganizationID: org.String(), SubscriptionID: ct.ExternalID, Code: a.Code,
		TransactionID: a.TransactionID, Timestamp: a.Timestamp.UTC(), Properties: metering.Properties(a.Properties),
		PreciseTotalAmountCents: a.PreciseTotalAmountCents,
	}}
	var out []*invoice.Fee
	for _, ps := range segs {
		if cardBilled(billed, ps.Card.ID) {
			continue
		}
		fs, err := s.fees.InAdvanceProductFees(ctx, r, ps, ev)
		if err != nil {
			return nil, err
		}
		if len(fs) == 0 {
			continue
		}
		if ps.RateCard.DisplayOnInvoice {
			if _, err := s.inAdvanceContractInvoice(ctx, r, ps, fs, a.Timestamp); err != nil {
				return nil, err
			}
		} else {
			cenv, err := kernel.LoadCustomerEnv(ctx, r, org, ct.CustomerID, ct.BillingEntityID)
			if err != nil {
				return nil, err
			}
			if err := s.taxStandalone(ctx, r, cenv, nil, fs); err != nil {
				return nil, err
			}
			for _, f := range fs {
				if err := r.Fees.Create(ctx, f); err != nil {
					return nil, fmt.Errorf("create fee: %w", err)
				}
			}
			if err := s.emitFees(ctx, fs); err != nil {
				return nil, err
			}
		}
		out = append(out, fs...)
	}
	return out, nil
}

// cardBilled reports whether fees hold a fee of the contract rate card.
//
// source: app/services/events/pay_in_advance_service.rb:52-60
func cardBilled(fs []*invoice.Fee, card core.SubscriptionRateCardID) bool {
	for _, f := range fs {
		if f.SubscriptionRateCardID == card {
			return true
		}
	}
	return false
}

// inAdvanceContractInvoice issues the invoice of the in-advance fees of
// one event on a contract (lago Invoices::CreatePayInAdvanceChargeService
// with a contract billing context): in the segment's currency, through
// the contract's billing entity with its purchase order number and its
// custom sections, linked to the contract over the segment, with the
// coupons, taxes and credits of a pay-in-advance invoice.
//
// source: app/services/invoices/create_pay_in_advance_charge_service.rb:15-125
func (s *Service) inAdvanceContractInvoice(ctx context.Context, r port.Repos, ps fees.ProductSegment, fs []*invoice.Fee, ts time.Time) (*invoice.Invoice, error) {
	ct := ps.Contract
	cenv, err := kernel.LoadCustomerEnv(ctx, r, ct.OrganizationID, ct.CustomerID, ct.BillingEntityID)
	if err != nil {
		return nil, err
	}
	inv := kernel.NewInvoice(s.d, cenv, kindInAdvance, ps.Segment.Currency, ts)
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(ct.PurchaseOrderNumber)
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	seg := ps.Segment
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		SubscriptionID: ct.ID, Reason: invoice.InvoicingReasonInAdvanceCharge,
		Timestamp: ts.UTC(), CreatedAt: s.d.Now(),
	}
	is.Period.From, is.Period.To = seg.StartedAt, seg.EndedAt
	if err := r.InvoiceSubscriptions.Create(ctx, is); err != nil {
		return nil, fmt.Errorf("create invoice subscription: %w", err)
	}
	for _, f := range fs {
		f.InvoiceID = inv.ID
		if err := r.Fees.Create(ctx, f); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
	}
	// The contract's selection, applied before the taxes
	// (create_pay_in_advance_charge_service.rb:30-35).
	if err := s.applyCustomSections(ctx, r, cenv, inv, nil, kernel.SubscriptionSectionResource(ct)); err != nil {
		return nil, err
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{vies: true})
	if err != nil {
		return nil, err
	}
	if err := s.finish(ctx, r, cenv, inv, kindInAdvance, st, modeNew); err != nil {
		return nil, err
	}
	return inv, kernel.Emit(ctx, s.d, inv.OrganizationID, webhook.EventTypeFeeCreated, "fee", kernel.ObjectID(fs[0].ID), fs[0])
}
