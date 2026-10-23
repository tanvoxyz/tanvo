package invoicing

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Codes of the rule and validation errors of the fee operations.
const (
	// CodeInvoicedFee rejects the change of a fee attached to an invoice:
	// its payment status follows the invoice's.
	CodeInvoicedFee = "invoiced_fee"
	// CodeInvalidChargeModel rejects a units-only adjustment of a
	// percentage or prorated graduated charge, whose amount does not
	// follow from the units alone.
	CodeInvalidChargeModel = "invalid_charge_model"
)

// FeeAdjustment is the input of [Service.AdjustFee]: the fee FeeID of the
// draft invoice, or the fee of a charge or fixed charge of one of its
// subscriptions, created when the invoice has none (lago
// AdjustedFees::CreateService params).
type FeeAdjustment struct {
	// InvoiceID is the draft invoice.
	InvoiceID core.InvoiceID
	// FeeID is the fee to adjust; when zero, SubscriptionID with ChargeID
	// (and ChargeFilterID) or FixedChargeID select it.
	FeeID core.FeeID
	// SubscriptionID is the subscription of the fee.
	SubscriptionID core.SubscriptionID
	// ChargeID is the charge of the fee.
	ChargeID core.ChargeID
	// ChargeFilterID is the charge filter of the fee.
	ChargeFilterID core.ChargeFilterID
	// FixedChargeID is the fixed charge of the fee.
	FixedChargeID core.FixedChargeID
	// Mode is what the adjustment changes.
	Mode invoice.AdjustmentMode
	// InvoiceDisplayName is the new display name.
	InvoiceDisplayName *string
	// Units is the new quantity (units modes).
	Units money.Decimal
	// UnitPreciseAmount is the new unit price in currency units
	// (units_and_unit_price mode).
	UnitPreciseAmount money.Decimal
}

// GetFee returns the fee id (lago Api::V1::FeesController#show).
func (s *Service) GetFee(ctx context.Context, org core.OrganizationID, id core.FeeID) (FeeView, error) {
	var out FeeView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.Fees.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get fee %s: %w", id, asNotFound(err, "fee"))
		}
		out, err = newViewer(r, org).fee(ctx, f)
		return err
	})
	return out, err
}

// ListFees returns the fees matching f, oldest first then by ID (lago
// FeesQuery); an invalid filter fails with core.ErrInvalidValue.
func (s *Service) ListFees(ctx context.Context, org core.OrganizationID, f invoice.FeeFilter, page core.PageRequest) (core.Page[FeeView], error) {
	var out core.Page[FeeView]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Fees.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list fees: %w", err)
		}
		items, err := newViewer(r, org).fees(ctx, p.Items, nil, nil)
		if err != nil {
			return err
		}
		out = core.Page[FeeView]{Items: items, NextToken: p.NextToken}
		return nil
	})
	return out, err
}

// UpdateFeePaymentStatus sets the payment status of the fee id, which
// must not be attached to an invoice, and stamps the instant of the new
// status, clearing the others (lago Fees::UpdateService).
func (s *Service) UpdateFeePaymentStatus(ctx context.Context, org core.OrganizationID, id core.FeeID, status invoice.FeePaymentStatus) (FeeView, error) {
	var out FeeView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.Fees.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get fee %s: %w", id, asNotFound(err, "fee"))
		}
		if !f.InvoiceID.IsZero() {
			return core.NewRuleError(CodeInvoicedFee, "the payment status of an invoiced fee follows its invoice")
		}
		var v core.Validator
		if !v.Enum("payment_status", status) {
			return v.Err()
		}
		now := s.d.Clock.Now()
		f.PaymentStatus = status
		f.SucceededAt, f.FailedAt, f.RefundedAt = nil, nil, nil
		switch status {
		case invoice.FeePaymentStatusSucceeded:
			f.SucceededAt = &now
		case invoice.FeePaymentStatusFailed:
			f.FailedAt = &now
		case invoice.FeePaymentStatusRefunded:
			f.RefundedAt = &now
		}
		f.UpdatedAt = now
		if err := r.Fees.Update(ctx, f); err != nil {
			return fmt.Errorf("update fee %s: %w", id, err)
		}
		out, err = newViewer(r, org).fee(ctx, f)
		return err
	})
	return out, err
}

// DeleteFee discards the fee id, which must not be attached to an
// invoice, and returns it (lago Fees::DestroyService).
func (s *Service) DeleteFee(ctx context.Context, org core.OrganizationID, id core.FeeID) (FeeView, error) {
	var out FeeView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.Fees.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get fee %s: %w", id, asNotFound(err, "fee"))
		}
		if !f.InvoiceID.IsZero() {
			return core.NewRuleError(CodeInvoicedFee, "an invoiced fee is deleted with its invoice")
		}
		now := s.d.Clock.Now().UTC()
		if err := r.Fees.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard fee %s: %w", id, err)
		}
		f.DeletedAt, f.UpdatedAt = &now, now
		out, err = newViewer(r, org).fee(ctx, f)
		return err
	})
	return out, err
}

// AdjustFee records an adjustment of a fee of a draft invoice, refreshes
// the invoice and returns the regenerated fee (lago
// AdjustedFees::CreateService).
func (s *Service) AdjustFee(ctx context.Context, org core.OrganizationID, in FeeAdjustment) (FeeView, error) {
	var out FeeView
	_, err := s.onInvoice(ctx, org, in.InvoiceID, func(ctx context.Context, r port.Repos, inv *invoice.Invoice) error {
		if inv.Status != invoice.StatusDraft {
			return core.NewRuleError(CodeNotDraft, "only the fees of a draft invoice are adjusted")
		}
		if inv.Type != invoice.TypeSubscription {
			return core.NewRuleError(CodeNotRefreshable, "only the fees of a subscription invoice are adjusted")
		}
		fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		as, err := r.AdjustedFees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("list adjusted fees of invoice %s: %w", inv.ID, err)
		}
		target, err := s.adjustedTarget(ctx, r, inv, fs, in)
		if err != nil {
			return err
		}
		for _, a := range as {
			if adjusts(a, target) {
				var v core.Validator
				v.Add("adjusted_fee", core.CodeAlreadyExists)
				return v.Err()
			}
		}
		if in.Mode == invoice.AdjustmentModeUnits && !target.ChargeID.IsZero() {
			c, err := r.Charges.Get(ctx, org, target.ChargeID)
			if err := ignoreNotFound(err); err != nil {
				return fmt.Errorf("get charge %s: %w", target.ChargeID, err)
			}
			if c != nil && (c.ChargeModel == catalog.ChargeModelPercentage ||
				(c.Prorated && c.ChargeModel == catalog.ChargeModelGraduated)) {
				var v core.Validator
				v.Add("charge", CodeInvalidChargeModel)
				return v.Err()
			}
		}
		a := &invoice.AdjustedFee{
			ID: core.NewID[core.AdjustedFeeID](s.d.IDs), OrganizationID: org, InvoiceID: inv.ID, FeeID: target.ID,
			SubscriptionID: target.SubscriptionID, ChargeID: target.ChargeID, ChargeFilterID: target.ChargeFilterID,
			FixedChargeID: target.FixedChargeID, FeeType: target.Type, GroupedBy: maps.Clone(target.GroupedBy),
			ChargesFrom: target.Properties.ChargesFrom, ChargesTo: target.Properties.ChargesTo,
			FixedChargesFrom: target.Properties.FixedChargesFrom, FixedChargesTo: target.Properties.FixedChargesTo,
			Mode: in.Mode, InvoiceDisplayName: in.InvoiceDisplayName, Units: in.Units, UnitPreciseAmount: in.UnitPreciseAmount,
			CreatedAt: s.d.Clock.Now(),
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if err := r.AdjustedFees.Create(ctx, a); err != nil {
			return fmt.Errorf("create adjusted fee: %w", err)
		}
		if err := s.regenerate(ctx, r, inv, modeRefresh); err != nil {
			return err
		}
		f, err := regeneratedFee(ctx, r, inv, a, target)
		if err != nil {
			return err
		}
		out, err = newViewer(r, org).fee(ctx, f)
		return err
	})
	return out, asNotFound(err, "invoice")
}

// DeleteFeeAdjustment removes the adjustment of the fee id of a draft
// invoice, refreshes the invoice and returns the regenerated fee, or the
// removed one when the refresh drops it (lago
// AdjustedFees::DestroyService).
func (s *Service) DeleteFeeAdjustment(ctx context.Context, org core.OrganizationID, id core.FeeID) (FeeView, error) {
	var out FeeView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.Fees.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get fee %s: %w", id, asNotFound(err, "fee"))
		}
		if f.InvoiceID.IsZero() {
			return fmt.Errorf("fee %s of no invoice: %w", id, core.NotFound("fee"))
		}
		if err := kernel.LockInvoice(ctx, r, org, f.InvoiceID); err != nil {
			return err
		}
		inv, err := r.Invoices.Get(ctx, org, f.InvoiceID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", f.InvoiceID, err)
		}
		if inv.Status != invoice.StatusDraft {
			return fmt.Errorf("fee %s of a %s invoice: %w", id, inv.Status, core.NotFound("fee"))
		}
		// The fee may have been regenerated since the lock was taken.
		if f, err = r.Fees.Get(ctx, org, id); err != nil {
			return fmt.Errorf("get fee %s: %w", id, asNotFound(err, "fee"))
		}
		as, err := r.AdjustedFees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return fmt.Errorf("list adjusted fees of invoice %s: %w", inv.ID, err)
		}
		i := -1
		for j, a := range as {
			if adjusts(a, f) {
				i = j
				break
			}
		}
		if i < 0 {
			return fmt.Errorf("fee %s: %w", id, core.NotFound("adjusted_fee"))
		}
		a := as[i]
		if err := r.AdjustedFees.Delete(ctx, org, a.ID); err != nil {
			return fmt.Errorf("delete adjusted fee %s: %w", a.ID, err)
		}
		if inv.Type == invoice.TypeSubscription {
			if err := s.regenerate(ctx, r, inv, modeRefresh); err != nil {
				return err
			}
		}
		re, err := regeneratedFee(ctx, r, inv, a, f)
		if errors.Is(err, core.ErrNotFound) {
			re = f
		} else if err != nil {
			return err
		}
		out, err = newViewer(r, org).fee(ctx, re)
		return err
	})
	return out, err
}

// adjustedTarget returns the fee the adjustment in is for: the fee
// in.FeeID of the invoice, else the invoice's fee of the subscription's
// charge (and filter) or fixed charge, else an unsaved empty fee of it
// over the invoice subscription's periods, which the refresh creates
// because an adjustment matches it (lago
// AdjustedFees::CreateService#find_or_create_fee).
func (s *Service) adjustedTarget(ctx context.Context, r port.Repos, inv *invoice.Invoice, fs []*invoice.Fee, in FeeAdjustment) (*invoice.Fee, error) {
	org := inv.OrganizationID
	if !in.FeeID.IsZero() {
		for _, f := range fs {
			if f.ID == in.FeeID {
				return f, nil
			}
		}
		return nil, fmt.Errorf("fee %s of invoice %s: %w", in.FeeID, inv.ID, core.NotFound("fee"))
	}
	iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list invoice subscriptions of %s: %w", inv.ID, err)
	}
	var is *invoice.Subscription
	for _, x := range iss {
		if x.SubscriptionID == in.SubscriptionID {
			is = x
			break
		}
	}
	if is == nil || in.SubscriptionID.IsZero() {
		return nil, fmt.Errorf("subscription %s of invoice %s: %w", in.SubscriptionID, inv.ID, core.NotFound("subscription"))
	}
	sub, err := r.Subscriptions.Get(ctx, org, is.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", is.SubscriptionID, asNotFound(err, "subscription"))
	}
	empty := &invoice.Fee{
		OrganizationID: org, InvoiceID: inv.ID, SubscriptionID: sub.ID, Currency: inv.Currency,
		PaymentStatus: invoice.FeePaymentStatusPending, GroupedBy: map[string]string{},
	}
	empty.Properties.Timestamp = is.Timestamp
	if !in.FixedChargeID.IsZero() {
		fc, err := r.FixedCharges.Get(ctx, org, in.FixedChargeID)
		if err := ignoreNotFound(err); err != nil {
			return nil, fmt.Errorf("get fixed charge %s: %w", in.FixedChargeID, err)
		}
		if fc == nil || fc.PlanID != sub.PlanID || fc.DeletedAt != nil {
			return nil, fmt.Errorf("fixed charge %s of plan %s: %w", in.FixedChargeID, sub.PlanID, core.NotFound("fixed_charge"))
		}
		for _, f := range fs {
			if f.SubscriptionID == sub.ID && f.FixedChargeID == fc.ID {
				return f, nil
			}
		}
		empty.Type, empty.FixedChargeID = core.FeeTypeFixedCharge, fc.ID
		empty.Properties.FixedChargesFrom, empty.Properties.FixedChargesTo = is.FixedChargesPeriod.From, is.FixedChargesPeriod.To
		return empty, nil
	}
	c, err := r.Charges.Get(ctx, org, in.ChargeID)
	if err := ignoreNotFound(err); err != nil {
		return nil, fmt.Errorf("get charge %s: %w", in.ChargeID, err)
	}
	if c == nil || in.ChargeID.IsZero() || c.PlanID != sub.PlanID || c.DeletedAt != nil {
		return nil, fmt.Errorf("charge %s of plan %s: %w", in.ChargeID, sub.PlanID, core.NotFound("charge"))
	}
	if !in.ChargeFilterID.IsZero() && !hasFilter(c, in.ChargeFilterID) {
		return nil, fmt.Errorf("charge filter %s of charge %s: %w", in.ChargeFilterID, c.ID, core.NotFound("charge_filter"))
	}
	for _, f := range fs {
		if f.SubscriptionID == sub.ID && f.ChargeID == c.ID && f.ChargeFilterID == in.ChargeFilterID && f.TrueUpParentFeeID.IsZero() {
			return f, nil
		}
	}
	empty.Type, empty.ChargeID, empty.ChargeFilterID = core.FeeTypeCharge, c.ID, in.ChargeFilterID
	empty.Properties.ChargesFrom, empty.Properties.ChargesTo = is.ChargesPeriod.From, is.ChargesPeriod.To
	return empty, nil
}

// hasFilter reports whether id is a kept filter of c.
func hasFilter(c *catalog.Charge, id core.ChargeFilterID) bool {
	for _, f := range c.Filters {
		if f.ID == id {
			return true
		}
	}
	return false
}

// regeneratedFee returns the fee of the refreshed invoice that the
// adjustment a applies to, else the invoice's fee of the subscription and
// charge (and filter) or fixed charge of target (lago
// AdjustedFees::CreateService result.fee), or core.ErrNotFound.
func regeneratedFee(ctx context.Context, r port.Repos, inv *invoice.Invoice, a *invoice.AdjustedFee, target *invoice.Fee) (*invoice.Fee, error) {
	fs, err := r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
	}
	for _, f := range fs {
		if f.TrueUpParentFeeID.IsZero() && adjustsKey(a, f) {
			return f, nil
		}
	}
	for _, f := range fs {
		if f.ID == target.ID || f.TrueUpParentFeeID.IsZero() && f.SubscriptionID == target.SubscriptionID && f.Type == target.Type &&
			f.ChargeID == target.ChargeID && f.ChargeFilterID == target.ChargeFilterID && f.FixedChargeID == target.FixedChargeID {
			return f, nil
		}
	}
	return nil, fmt.Errorf("fee of invoice %s: %w", inv.ID, core.NotFound("fee"))
}

// adjusts reports whether the adjustment a applies to the fee f of its
// invoice: a links f, or a's key is f's ([adjustsKey]). The link goes
// stale when a refresh regenerates the fees, so the key decides then.
func adjusts(a *invoice.AdjustedFee, f *invoice.Fee) bool {
	if a.InvoiceID != f.InvoiceID {
		return false
	}
	if !a.FeeID.IsZero() && a.FeeID == f.ID {
		return true
	}
	return f.TrueUpParentFeeID.IsZero() && adjustsKey(a, f)
}

// adjustsKey reports whether a's key (fee type, subscription, charge,
// filter, grouping and period) is f's, as the fees service matches the
// adjustments of a draft invoice when it regenerates the fees.
func adjustsKey(a *invoice.AdjustedFee, f *invoice.Fee) bool {
	if a.FeeType != f.Type || a.SubscriptionID != f.SubscriptionID {
		return false
	}
	switch f.Type {
	case core.FeeTypeSubscription:
		return true
	case core.FeeTypeCharge:
		return a.ChargeID == f.ChargeID && a.ChargeFilterID == f.ChargeFilterID && maps.Equal(a.GroupedBy, f.GroupedBy) &&
			a.MatchesCharges(f.Properties.ChargesFrom, f.Properties.ChargesTo)
	case core.FeeTypeFixedCharge:
		return a.FixedChargeID == f.FixedChargeID && a.MatchesFixedCharges(f.Properties.FixedChargesFrom, f.Properties.FixedChargesTo)
	default:
		return false
	}
}

// asNotFound turns a missing row error into core.NotFound(resource),
// keeping a typed not found error as it is.
func asNotFound(err error, resource string) error {
	var nf *core.NotFoundError
	if !errors.Is(err, core.ErrNotFound) || errors.As(err, &nf) {
		return err
	}
	return fmt.Errorf("%w: %w", err, core.NotFound(resource))
}
