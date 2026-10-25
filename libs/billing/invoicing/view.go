package invoicing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// FeeView is a fee with what the API shows beside it (lago
// V1::FeeSerializer).
type FeeView struct {
	// Fee is the fee.
	Fee *invoice.Fee
	// CustomerID is the customer of the fee's invoice, else of its
	// subscription (lago Fee#customer).
	CustomerID core.CustomerID
	// BillableMetricCode is the code of the metric of a charge fee.
	BillableMetricCode string
	// TrueUpFeeID is the commitment true-up fee completing the fee.
	TrueUpFeeID core.FeeID
	// Adjusted marks a fee adjusted on its draft invoice.
	Adjusted bool
	// PresentationBreakdowns are the fee's breakdowns by the presentation
	// keys its charge displays on invoices (lago V1::FeeSerializer,
	// app/serializers/v1/fee_serializer.rb:60).
	PresentationBreakdowns []invoice.PresentationBreakdown
}

// SubscriptionView is an invoice subscription with the external ID of the
// subscription.
type SubscriptionView struct {
	// Subscription is the invoice subscription.
	Subscription *invoice.Subscription
	// ExternalID is the subscription's external ID.
	ExternalID string
}

// CreditView is an invoice credit with its label: the coupon code, the
// credit note number or the deducted invoice's number (lago
// V1::CreditSerializer item code).
type CreditView struct {
	// Credit is the credit.
	Credit *invoice.Credit
	// Label is the coupon code, credit note number or invoice number.
	Label string
}

// InvoiceView is an invoice with what the API shows beside it (lago
// V1::InvoiceSerializer with its subscriptions, fees, credits, applied
// taxes, error details and applied invoice custom sections).
type InvoiceView struct {
	// Invoice is the invoice.
	Invoice *invoice.Invoice
	// RegeneratedInvoiceID is the invoice regenerated from a voided one.
	RegeneratedInvoiceID core.InvoiceID
	// Subscriptions are the subscriptions billed.
	Subscriptions []SubscriptionView
	// Fees are the fees, in creation order.
	Fees []FeeView
	// Credits are the deductions.
	Credits []CreditView
	// AppliedTaxes are the invoice's applied taxes.
	AppliedTaxes []*invoice.AppliedTax
	// Settlements are the settlements.
	Settlements []*invoice.Settlement
	// Errors are the error details.
	Errors []*invoice.ErrorDetail
	// CustomSections are the custom sections printed on the invoice.
	CustomSections []*invoice.AppliedCustomSection
}

// viewer loads the related records of views in one transaction, caching
// metric codes, customers and labels.
type viewer struct {
	r       port.Repos
	org     core.OrganizationID
	codes   map[core.ChargeID]string
	keys    map[core.ChargeID][]string
	subCust map[core.SubscriptionID]core.CustomerID
	invCust map[core.InvoiceID]core.CustomerID
}

func newViewer(r port.Repos, org core.OrganizationID) *viewer {
	return &viewer{
		r: r, org: org,
		codes:   map[core.ChargeID]string{},
		keys:    map[core.ChargeID][]string{},
		subCust: map[core.SubscriptionID]core.CustomerID{},
		invCust: map[core.InvoiceID]core.CustomerID{},
	}
}

// ignoreNotFound turns core.ErrNotFound into nil.
func ignoreNotFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

// metricCode returns the code of the metric of charge id ("" when gone).
func (v *viewer) metricCode(ctx context.Context, id core.ChargeID) (string, error) {
	if code, ok := v.codes[id]; ok {
		return code, nil
	}
	code := ""
	c, err := v.r.Charges.Get(ctx, v.org, id)
	if err != nil {
		if err := ignoreNotFound(err); err != nil {
			return "", fmt.Errorf("get charge %s: %w", id, err)
		}
	} else {
		v.keys[id] = c.Properties.PresentationGroupKeysDisplayedInInvoice()
		ms, err := v.r.Metrics.ListByIDs(ctx, v.org, []core.BillableMetricID{c.BillableMetricID})
		if err := ignoreNotFound(err); err != nil {
			return "", fmt.Errorf("get metric %s: %w", c.BillableMetricID, err)
		}
		if len(ms) > 0 {
			code = ms[0].Code
		}
	}
	v.codes[id] = code
	return code, nil
}

// customer returns the customer of f.
func (v *viewer) customer(ctx context.Context, f *invoice.Fee) (core.CustomerID, error) {
	if !f.InvoiceID.IsZero() {
		if c, ok := v.invCust[f.InvoiceID]; ok {
			return c, nil
		}
		inv, err := v.r.Invoices.Get(ctx, v.org, f.InvoiceID)
		if err != nil {
			return core.CustomerID{}, fmt.Errorf("get invoice %s: %w", f.InvoiceID, err)
		}
		v.invCust[f.InvoiceID] = inv.CustomerID
		return inv.CustomerID, nil
	}
	if f.SubscriptionID.IsZero() {
		return core.CustomerID{}, nil
	}
	if c, ok := v.subCust[f.SubscriptionID]; ok {
		return c, nil
	}
	sub, err := v.r.Subscriptions.Get(ctx, v.org, f.SubscriptionID)
	if err != nil {
		return core.CustomerID{}, fmt.Errorf("get subscription %s: %w", f.SubscriptionID, err)
	}
	v.subCust[f.SubscriptionID] = sub.CustomerID
	return sub.CustomerID, nil
}

// fees returns the views of fs; siblings are the fees of their invoice
// (for true-up links) and adjusted the adjusted fee IDs, both loaded when
// nil.
func (v *viewer) fees(ctx context.Context, fs []*invoice.Fee, siblings map[core.InvoiceID][]*invoice.Fee, adjusted map[core.InvoiceID]map[core.FeeID]bool) ([]FeeView, error) {
	if siblings == nil {
		siblings = map[core.InvoiceID][]*invoice.Fee{}
	}
	if adjusted == nil {
		adjusted = map[core.InvoiceID]map[core.FeeID]bool{}
	}
	out := make([]FeeView, 0, len(fs))
	for _, f := range fs {
		fv := FeeView{Fee: f}
		var err error
		if fv.CustomerID, err = v.customer(ctx, f); err != nil {
			return nil, err
		}
		if f.Type == core.FeeTypeCharge && !f.ChargeID.IsZero() {
			if fv.BillableMetricCode, err = v.metricCode(ctx, f.ChargeID); err != nil {
				return nil, err
			}
			fv.PresentationBreakdowns = invoice.DisplayedPresentationBreakdowns(f.PresentationBreakdowns, v.keys[f.ChargeID])
		}
		if !f.InvoiceID.IsZero() {
			sib, ok := siblings[f.InvoiceID]
			if !ok {
				if sib, err = v.r.Fees.ListByInvoice(ctx, v.org, f.InvoiceID); err != nil {
					return nil, fmt.Errorf("list fees of invoice %s: %w", f.InvoiceID, err)
				}
				siblings[f.InvoiceID] = sib
			}
			for _, s := range sib {
				if s.TrueUpParentFeeID == f.ID {
					fv.TrueUpFeeID = s.ID
					break
				}
			}
			adj, ok := adjusted[f.InvoiceID]
			if !ok {
				as, err := v.r.AdjustedFees.ListByInvoice(ctx, v.org, f.InvoiceID)
				if err != nil {
					return nil, fmt.Errorf("list adjusted fees of invoice %s: %w", f.InvoiceID, err)
				}
				adj = map[core.FeeID]bool{}
				for _, a := range as {
					for _, s := range sib {
						if adjusts(a, s) {
							adj[s.ID] = true
						}
					}
				}
				adjusted[f.InvoiceID] = adj
			}
			fv.Adjusted = adj[f.ID]
		}
		out = append(out, fv)
	}
	return out, nil
}

// fee returns the view of f.
func (v *viewer) fee(ctx context.Context, f *invoice.Fee) (FeeView, error) {
	fvs, err := v.fees(ctx, []*invoice.Fee{f}, nil, nil)
	if err != nil {
		return FeeView{}, err
	}
	return fvs[0], nil
}

// invoice returns the view of inv; full also loads its fees, credits,
// applied taxes, settlements and error details (the show payload), else
// only its subscriptions (the list payload, lago
// V1::InvoiceSerializer without includes).
func (v *viewer) invoice(ctx context.Context, inv *invoice.Invoice, full bool) (*InvoiceView, error) {
	out := &InvoiceView{Invoice: inv}
	v.invCust[inv.ID] = inv.CustomerID
	if inv.Status == invoice.StatusVoided {
		re, err := v.r.Invoices.GetRegenerated(ctx, v.org, inv.ID)
		switch {
		case err == nil:
			out.RegeneratedInvoiceID = re.ID
		case !errors.Is(err, core.ErrNotFound):
			return nil, fmt.Errorf("get regenerated invoice of %s: %w", inv.ID, err)
		}
	}
	iss, err := v.r.InvoiceSubscriptions.ListByInvoice(ctx, v.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list invoice subscriptions of %s: %w", inv.ID, err)
	}
	for _, is := range iss {
		sv := SubscriptionView{Subscription: is}
		sub, err := v.r.Subscriptions.Get(ctx, v.org, is.SubscriptionID)
		if err != nil {
			if err := ignoreNotFound(err); err != nil {
				return nil, fmt.Errorf("get subscription %s: %w", is.SubscriptionID, err)
			}
		} else {
			sv.ExternalID = sub.ExternalID
		}
		out.Subscriptions = append(out.Subscriptions, sv)
	}
	if !full {
		return out, nil
	}
	fs, err := v.r.Fees.ListByInvoice(ctx, v.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
	}
	if out.Fees, err = v.fees(ctx, fs, map[core.InvoiceID][]*invoice.Fee{inv.ID: fs}, nil); err != nil {
		return nil, err
	}
	credits, err := v.r.Credits.ListByInvoice(ctx, v.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list credits of invoice %s: %w", inv.ID, err)
	}
	for _, c := range credits {
		label, err := v.creditLabel(ctx, c)
		if err != nil {
			return nil, err
		}
		out.Credits = append(out.Credits, CreditView{Credit: c, Label: label})
	}
	if out.AppliedTaxes, err = v.r.InvoiceAppliedTaxes.ListByInvoice(ctx, v.org, inv.ID); err != nil {
		return nil, fmt.Errorf("list applied taxes of invoice %s: %w", inv.ID, err)
	}
	if out.Settlements, err = v.r.InvoiceSettlements.ListByInvoice(ctx, v.org, inv.ID); err != nil {
		return nil, fmt.Errorf("list settlements of invoice %s: %w", inv.ID, err)
	}
	if out.Errors, err = v.r.ErrorDetails.ListByInvoice(ctx, v.org, inv.ID); err != nil {
		return nil, fmt.Errorf("list error details of invoice %s: %w", inv.ID, err)
	}
	if out.CustomSections, err = v.r.AppliedInvoiceCustomSections.ListByInvoice(ctx, v.org, inv.ID); err != nil {
		return nil, fmt.Errorf("list custom sections of invoice %s: %w", inv.ID, err)
	}
	return out, nil
}

// creditLabel returns the label of c ("" when its source is gone).
func (v *viewer) creditLabel(ctx context.Context, c *invoice.Credit) (string, error) {
	switch {
	case !c.AppliedCouponID.IsZero():
		ac, err := v.r.AppliedCoupons.Get(ctx, v.org, c.AppliedCouponID)
		if err != nil {
			return "", ignoreNotFound(err)
		}
		cp, err := v.r.Coupons.Get(ctx, v.org, ac.CouponID)
		if err != nil {
			return "", ignoreNotFound(err)
		}
		return cp.Code, nil
	case !c.CreditNoteID.IsZero():
		cn, err := v.r.CreditNotes.Get(ctx, v.org, c.CreditNoteID)
		if err != nil {
			return "", ignoreNotFound(err)
		}
		return cn.Number, nil
	case !c.ProgressiveBillingInvoiceID.IsZero():
		inv, err := v.r.Invoices.Get(ctx, v.org, c.ProgressiveBillingInvoiceID)
		if err != nil {
			return "", ignoreNotFound(err)
		}
		return inv.Number, nil
	}
	return "", nil
}
