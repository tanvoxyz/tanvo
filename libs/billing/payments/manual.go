package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ManualInput records a manual payment of an invoice.
type ManualInput struct {
	// OrganizationID is the invoice's organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the paid invoice.
	InvoiceID core.InvoiceID
	// Amount is the amount paid, in major units of the invoice currency.
	Amount money.Decimal
	// Reference identifies the payment (at most 40 characters).
	Reference string
	// PaidAt is when the payment was made; nil means now.
	PaidAt *time.Time
}

// CreateManual records a manual payment of an invoice (lago
// Payments::ManualCreateService). The amount must be positive; the invoice
// must exist and, unless it bills advance charges, be finalized or open
// (otherwise [core.ErrNotAllowed]). The payment must not exceed the amount
// due nor pay an invoice already paid through a payment request, and pays
// a credit invoice in full. A payment that, with the credit note offsets,
// settles the invoice marks it paid; every manual payment gets a receipt.
func (s *Service) CreateManual(ctx context.Context, in ManualInput) (*payment.Payment, error) {
	if !in.Amount.IsPositive() {
		return nil, fieldError("amount", core.CodeInvalid)
	}
	var paidCredit *invoice.Invoice
	p, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Payment, error) {
		p, inv, paid, err := s.createManual(ctx, r, in)
		if paid && inv.Type == invoice.TypeCredit {
			paidCredit = inv
		}
		return p, err
	})
	if err != nil {
		return nil, err
	}
	if paidCredit != nil && s.credits != nil {
		// lago runs Invoices::PrepaidCreditJob after the commit; a failure
		// leaves the payment recorded.
		if err := s.credits.HandleCreditInvoicePayment(ctx, paidCredit.OrganizationID, paidCredit.ID, true); err != nil {
			s.d.Logger.ErrorContext(ctx, "payments: settle credit invoice purchase",
				"organization_id", paidCredit.OrganizationID.String(), "invoice", paidCredit.ID.String(), "error", err)
		}
	}
	return p, nil
}

// createManual records the payment of in and reports whether it marked
// the invoice paid.
func (s *Service) createManual(ctx context.Context, r port.Repos, in ManualInput) (*payment.Payment, *invoice.Invoice, bool, error) {
	org := in.OrganizationID
	if err := kernel.LockInvoice(ctx, r, org, in.InvoiceID); err != nil {
		return nil, nil, false, err
	}
	inv, err := r.Invoices.Get(ctx, org, in.InvoiceID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil, false, notFound("invoice")
	}
	if err != nil {
		return nil, nil, false, wrap(err, "load invoice %s", in.InvoiceID)
	}
	minor := in.Amount.Mul(inv.Currency.SubunitToUnit())
	amount, ok := minor.Int64()
	if !minor.IsInteger() || !ok {
		return nil, nil, false, fieldError("amount", core.CodeInvalid)
	}
	if inv.Type != invoice.TypeAdvanceCharges && inv.Status != invoice.StatusFinalized && inv.Status != invoice.StatusOpen {
		return nil, nil, false, fmt.Errorf("manual payment of a %s invoice: %w", inv.Status, core.ErrNotAllowed)
	}
	now := s.d.Now()
	paidAt := now
	if in.PaidAt != nil {
		paidAt = in.PaidAt.UTC()
	}
	p := &payment.Payment{
		ID:             core.NewID[core.PaymentID](s.d.IDs),
		OrganizationID: org,
		Payable:        payment.InvoicePayable(inv.ID),
		CustomerID:     inv.CustomerID,
		Kind:           payment.KindManual,
		Status:         payment.StateSucceeded,
		Amount:         amount,
		Currency:       inv.Currency,
		Reference:      &in.Reference,
		PaidAt:         &paidAt,
		CreatedAt:      paidAt, // lago manual_create_service.rb:34
		UpdatedAt:      now,
	}
	if err := p.Validate(); err != nil {
		return nil, nil, false, err
	}
	reqs, err := r.PaymentRequests.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, nil, false, wrap(err, "list payment requests of invoice %s", inv.ID)
	}
	facts := payment.InvoiceFacts{
		Credit:                inv.Type == invoice.TypeCredit,
		TotalAmount:           inv.TotalAmount,
		TotalPaidAmount:       inv.TotalPaidAmount,
		FinalizedOffsetAmount: inv.OffsettedAmount,
	}
	for _, q := range reqs {
		facts.SucceededPaymentRequest = facts.SucceededPaymentRequest || q.PaymentStatus == core.PaymentStatusSucceeded
	}
	if inv.Status == invoice.StatusVoided {
		facts.TotalAmount, facts.TotalPaidAmount, facts.FinalizedOffsetAmount = 0, 0, 0
	}
	if err := p.ValidateManual(facts); err != nil {
		return nil, nil, false, err
	}
	if err := r.Payments.Create(ctx, p); err != nil {
		return nil, nil, false, wrap(err, "create payment")
	}

	paid, err := s.recordPaid(ctx, r, inv, now)
	if err != nil {
		return nil, nil, false, err
	}
	// The payment's log is anchored on what it pays
	// (manual_create_service.rb:13, activity_log.rb:174-176).
	if err := s.d.LogChange(ctx, r, kernel.Change{
		Type: auditlog.ActivityPaymentRecorded, OrganizationID: org, ResourceType: auditlog.ResourceInvoice,
		ResourceID: inv.ID.UUID(), Object: p, CustomerID: p.CustomerID,
	}); err != nil {
		return nil, nil, false, err
	}
	if _, err := s.createReceipt(ctx, r, p); err != nil {
		return nil, nil, false, err
	}
	return p, inv, paid, nil
}

// recordPaid sets the invoice's paid amount to the sum of its succeeded
// payments and, when that and the credit note offsets make its total,
// marks it paid; it reports whether it did.
func (s *Service) recordPaid(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) (bool, error) {
	org := inv.OrganizationID
	ps, err := r.Payments.ListByPayable(ctx, org, payment.InvoicePayable(inv.ID))
	if err != nil {
		return false, wrap(err, "list payments of invoice %s", inv.ID)
	}
	var paid int64
	for _, p := range ps {
		if p.Status == payment.StateSucceeded {
			paid += p.Amount
		}
	}
	sts, err := r.InvoiceSettlements.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return false, wrap(err, "list settlements of invoice %s", inv.ID)
	}
	var offset int64
	for _, st := range sts {
		if st.Type == invoice.SettlementTypeCreditNote {
			offset += st.Amount
		}
	}
	inv.TotalPaidAmount = paid
	inv.UpdatedAt = now
	if paid+offset != inv.TotalAmount {
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return false, wrap(err, "update invoice %s", inv.ID)
		}
		return false, nil
	}
	if err := s.markPaid(ctx, r, inv, now); err != nil {
		return false, err
	}
	return true, nil
}

// markPaid sets the invoice's payment status to succeeded and applies its
// effects (lago Invoices::UpdateService with payment_status): the overdue
// flag is cleared, resetting the customer's dunning attempts in the
// invoice's currency when a dunning campaign requested it; the fees take
// the payment status; an active checkout link expires; and a changed
// status of a visible invoice is announced by the
// invoice.payment_status_updated webhook.
func (s *Service) markPaid(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) error {
	org := inv.OrganizationID
	old := inv.PaymentStatus
	inv.PaymentStatus = core.PaymentStatusSucceeded
	inv.UpdatedAt = now
	if inv.PaymentOverdue {
		inv.PaymentOverdue = false
		if err := s.resetDunning(ctx, r, inv, now); err != nil {
			return err
		}
	}
	if err := r.Invoices.Update(ctx, inv); err != nil {
		return wrap(err, "update invoice %s", inv.ID)
	}
	if err := resolvePaymentGate(ctx, r, inv); err != nil {
		return err
	}
	fees, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return wrap(err, "list fees of invoice %s", inv.ID)
	}
	for _, f := range fees {
		if f.PaymentStatus == invoice.FeePaymentStatusSucceeded {
			continue
		}
		f.PaymentStatus = invoice.FeePaymentStatusSucceeded
		f.UpdatedAt = now
		if err := r.Fees.Update(ctx, f); err != nil {
			return wrap(err, "update fee %s", f.ID)
		}
	}
	if old != core.PaymentStatusSucceeded {
		if err := s.expireCheckout(ctx, r, inv, now); err != nil {
			return err
		}
	}
	if old != inv.PaymentStatus && inv.Visible() {
		// app/services/invoices/update_service.rb:76-79,105-107.
		if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoicePaymentStatusUpdated, inv); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoicePaymentStatusUpdated, "invoice", inv.ID.UUID(), inv)
	}
	return nil
}

// resetDunning resets the customer's dunning attempts in the invoice's
// currency when a dunning campaign requested the invoice's payment (lago
// Customer#reset_dunning_campaign_for_currency!).
func (s *Service) resetDunning(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) error {
	org := inv.OrganizationID
	reqs, err := r.PaymentRequests.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return wrap(err, "list payment requests of invoice %s", inv.ID)
	}
	dunned := false
	for _, q := range reqs {
		dunned = dunned || !q.DunningCampaignID.IsZero()
	}
	if !dunned {
		return nil
	}
	c, err := r.Customers.Get(ctx, org, inv.CustomerID)
	if err != nil {
		return wrap(err, "load customer %s", inv.CustomerID)
	}
	attempts := make(map[string]int, len(c.DunningCampaignAttempts)+1)
	allReset := true
	for cur, n := range c.DunningCampaignAttempts {
		if cur == inv.Currency.Code() {
			n = 0
		}
		attempts[cur] = n
		allReset = allReset && n == 0
	}
	attempts[inv.Currency.Code()] = 0
	c.DunningCampaignAttempts = attempts
	if allReset {
		c.LastDunningCampaignAttemptAt = nil
	}
	c.UpdatedAt = now
	if err := r.Customers.Update(ctx, c); err != nil {
		return wrap(err, "update customer %s", c.ID)
	}
	return nil
}

// expireCheckout expires the invoice's active checkout link (lago
// PaymentIntents::ExpireJob after a payment settled the invoice).
func (s *Service) expireCheckout(ctx context.Context, r port.Repos, inv *invoice.Invoice, now time.Time) error {
	it, err := r.PaymentIntents.GetActiveByInvoice(ctx, inv.OrganizationID, inv.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return wrap(err, "load checkout link of invoice %s", inv.ID)
	}
	if err := it.Expire(now); err != nil {
		return err
	}
	if err := r.PaymentIntents.Update(ctx, it); err != nil {
		return wrap(err, "expire checkout link of invoice %s", inv.ID)
	}
	return nil
}

// fieldError is a validation error of one field.
func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}
