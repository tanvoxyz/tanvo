package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// Deps are the dependencies of every billing service. Tx, Events, Clock,
// IDs, Webhooks and Credits are required; Taxes, Refunds and Logger are
// optional.
type Deps struct {
	// Tx runs the multi-step writes.
	Tx port.Transactor
	// Events is the event store the aggregations read.
	Events metering.Store
	// Clock gives the current instant; everything time-dependent uses it.
	Clock core.Clock
	// IDs generates the identifiers of new rows.
	IDs core.IDGenerator
	// Webhooks records the domain events.
	Webhooks port.WebhookEmitter
	// Credits stores the invoice credits (coupons, credit notes and
	// progressive billing applied to invoices).
	Credits CreditRepository
	// Taxes computes the taxes of customers taxed by a tax provider; nil
	// means no provider.
	Taxes TaxProvider
	// Refunds sends credit-note refunds to payment providers; nil means
	// refunds stay pending until a provider is wired.
	Refunds RefundGateway
	// Logger logs; nil means slog.Default().
	Logger *slog.Logger
}

// ErrMissingDependency reports a required dependency left nil.
var ErrMissingDependency = errors.New("billing: missing dependency")

// Check reports the first required dependency left nil and fills Logger.
func (d *Deps) Check() error {
	switch {
	case d.Tx == nil:
		return fmt.Errorf("%w: Tx", ErrMissingDependency)
	case d.Events == nil:
		return fmt.Errorf("%w: Events", ErrMissingDependency)
	case d.Clock == nil:
		return fmt.Errorf("%w: Clock", ErrMissingDependency)
	case d.IDs == nil:
		return fmt.Errorf("%w: IDs", ErrMissingDependency)
	case d.Webhooks == nil:
		return fmt.Errorf("%w: Webhooks", ErrMissingDependency)
	case d.Credits == nil:
		return fmt.Errorf("%w: Credits", ErrMissingDependency)
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return nil
}

// Now returns the current instant in UTC.
func (d *Deps) Now() time.Time { return d.Clock.Now().UTC() }

// CreditID identifies an invoice credit. libs/core has no identifier kind
// for it yet (see [Credit]).
type CreditID = uuid.UUID

// Credit is an amount credited on an invoice before or after taxes: a
// coupon application, a credit-note consumption or a progressive-billing
// credit (domain §22.7). Lago stores these rows in `credits`; libs/core has
// no such entity, so billing defines the port (to move to libs/core).
type Credit struct {
	// ID identifies the credit.
	ID CreditID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the credited invoice.
	InvoiceID core.InvoiceID
	// AppliedCouponID is set for a coupon credit.
	AppliedCouponID core.AppliedCouponID
	// CreditNoteID is set for a credit-note credit.
	CreditNoteID core.CreditNoteID
	// ProgressiveBillingInvoiceID is set for a progressive-billing credit:
	// the progressive invoice whose fees are deducted.
	ProgressiveBillingInvoiceID core.InvoiceID
	// Amount is the credited amount, in minor units of Currency.
	Amount int64
	// Currency is the invoice currency.
	Currency money.Currency
	// BeforeTaxes is true for coupons and progressive billing.
	BeforeTaxes bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// CreditKind tells which deduction a [Credit] records.
type CreditKind int

// Credit kinds.
const (
	// CreditKindCoupon is a coupon application.
	CreditKindCoupon CreditKind = iota + 1
	// CreditKindCreditNote is a credit-note consumption.
	CreditKindCreditNote
	// CreditKindProgressiveBilling is a progressive-billing credit.
	CreditKindProgressiveBilling
)

// Kind returns the kind of the credit.
func (c *Credit) Kind() CreditKind {
	switch {
	case !c.AppliedCouponID.IsZero():
		return CreditKindCoupon
	case !c.CreditNoteID.IsZero():
		return CreditKindCreditNote
	default:
		return CreditKindProgressiveBilling
	}
}

// CreditRepository stores invoice credits. It joins the transaction carried
// by ctx like the repositories of [port.Repos].
type CreditRepository interface {
	// Create inserts c.
	Create(ctx context.Context, c *Credit) error
	// ListByInvoice returns the credits of an invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Credit, error)
	// ListByAppliedCoupon returns the credits of an applied coupon in
	// creation order.
	ListByAppliedCoupon(ctx context.Context, org core.OrganizationID, applied core.AppliedCouponID) ([]*Credit, error)
	// ListByCreditNote returns the credits consuming a credit note in
	// creation order.
	ListByCreditNote(ctx context.Context, org core.OrganizationID, cn core.CreditNoteID) ([]*Credit, error)
	// ListByProgressiveBillingInvoice returns the credits deducting a
	// progressive-billing invoice in creation order.
	ListByProgressiveBillingInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Credit, error)
	// DeleteByInvoice deletes the credits of an invoice (draft refresh).
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}

// TaxRequest asks a tax provider for the taxes of an invoice (or of
// standalone in-advance fees when Invoice is nil).
type TaxRequest struct {
	// Customer is the taxed customer.
	Customer *customer.Customer
	// Invoice is the taxed invoice; nil for standalone fees.
	Invoice *invoice.Invoice
	// Fees are the taxed fees, after coupons.
	Fees []*invoice.Fee
	// Draft is true for a draft invoice (the provider must not record the
	// transaction).
	Draft bool
}

// TaxProvider computes the taxes of customers taxed by an external
// provider (domain §17.4). libs/core has no such port yet; apps/server
// wires the integration.
type TaxProvider interface {
	// Enabled reports whether a provider computes the customer's taxes.
	Enabled(ctx context.Context, c *customer.Customer) (bool, error)
	// ComputeTaxes returns the provider's result per fee (ItemID is the fee
	// ID). An error fails the invoice (status failed, tax_error).
	ComputeTaxes(ctx context.Context, req TaxRequest) ([]tax.ProviderTaxResult, error)
	// VoidInvoice tells the provider that a finalized invoice was voided.
	VoidInvoice(ctx context.Context, c *customer.Customer, inv *invoice.Invoice) error
	// ReportCreditNote tells the provider about a finalized credit note.
	ReportCreditNote(ctx context.Context, c *customer.Customer, inv *invoice.Invoice, cn *creditnote.CreditNote) error
}

// RefundRequest asks a payment provider to refund part of a payment.
type RefundRequest struct {
	// IdempotencyKey identifies the refund at the provider and is always
	// set: it is the credit note's ID. A gateway must send it as the
	// provider's idempotency key (Stripe's Idempotency-Key header, lago
	// app/services/credit_notes/refunds/stripe_service.rb:105), so that
	// a retried or concurrent call for the same note returns the refund
	// already created instead of refunding twice.
	IdempotencyKey string
	// CreditNote is the refunding credit note.
	CreditNote *creditnote.CreditNote
	// Invoice is the credited invoice.
	Invoice *invoice.Invoice
	// PaymentID is the refunded payment.
	PaymentID core.PaymentID
	// Amount is the refunded amount, in minor units of Currency.
	Amount int64
	// Currency is the payment currency.
	Currency money.Currency
}

// RefundResult is a provider's answer to a refund.
type RefundResult struct {
	// ProviderRefundID is the provider's refund identifier.
	ProviderRefundID string
	// Status is the refund status (pending until the provider confirms).
	Status core.RefundStatus
}

// RefundGateway sends refunds to the payment provider that collected a
// payment. libs/core has no such port yet; apps/server wires providers.
type RefundGateway interface {
	// Refund refunds req.Amount of payment req.PaymentID. Calls with the
	// same req.IdempotencyKey must refund once and return the same
	// provider refund.
	Refund(ctx context.Context, req RefundRequest) (RefundResult, error)
}
