package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// Deps are the dependencies of every billing service. Tx, Events, Clock,
// IDs and Webhooks are required; Taxes, Refunds and Logger are optional.
type Deps struct {
	// Tx runs the multi-step writes.
	Tx port.Transactor
	// Events is the event store the aggregations read.
	Events metering.Store
	// UsageCache keeps the current-usage fees of charge buckets, validated
	// against the ingestion watermark of Events; nil caches nothing.
	UsageCache UsageCache
	// Clock gives the current instant; everything time-dependent uses it.
	Clock core.Clock
	// IDs generates the identifiers of new rows.
	IDs core.IDGenerator
	// Webhooks records the domain events.
	Webhooks port.WebhookEmitter
	// Taxes computes the taxes of customers taxed by a tax provider; nil
	// means no provider.
	Taxes TaxProvider
	// Refunds sends credit-note refunds to payment providers; nil means
	// refunds stay pending until a provider is wired.
	Refunds RefundGateway
	// ActivityLogs records the activity logs of resource changes; nil
	// records none ([Deps.LogActivity]).
	ActivityLogs auditlog.ActivityLogger
	// Streaming writes the usage-refreshed events to the organizations'
	// streaming destinations (parity §1.40); nil streams nothing.
	Streaming port.StreamProducer
	// Logger logs; nil means slog.Default().
	Logger *slog.Logger
	// Transient reports an error the job's retry policy runs again with
	// the same args (lock, serialization and sequence conflicts, lago's
	// retry_on of BillSubscriptionJob, app/jobs/bill_subscription_job.rb:13-14);
	// nil means errors wrapping core.ErrConflict.
	Transient func(error) bool
	// DailyUsageJitter is the window the daily usage computations of one
	// run are spread over at random; zero or negative means
	// [DefaultDailyUsageJitter].
	DailyUsageJitter time.Duration
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
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return nil
}

// IsTransient reports whether err is transient ([Deps.Transient]).
func (d *Deps) IsTransient(err error) bool {
	if d.Transient != nil {
		return d.Transient(err)
	}
	return errors.Is(err, core.ErrConflict)
}

// Now returns the current instant in UTC.
func (d *Deps) Now() time.Time { return d.Clock.Now().UTC() }

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
