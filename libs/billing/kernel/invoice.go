package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// InvoiceKind describes how an invoice is dated and finalized (domain
// §22.12).
type InvoiceKind struct {
	// Type is the invoice type.
	Type invoice.Type
	// Recurring is true for periodic subscription invoices.
	Recurring bool
	// UsesBillingDate is true for gated and in-advance-charge invoices.
	UsesBillingDate bool
	// UsesGracePeriod is true for subscription and progressive billing
	// invoices.
	UsesGracePeriod bool
}

// IssuingFacts returns the issuing date facts of an invoice of kind k for
// the customer env.
func (k InvoiceKind) IssuingFacts(env *CustomerEnv) invoice.IssuingDateFacts {
	grace := 0
	if k.UsesGracePeriod {
		grace = env.GracePeriodDays()
	}
	return invoice.IssuingDateFacts{
		Recurring:       k.Recurring,
		UsesBillingDate: k.UsesBillingDate,
		Anchor:          env.IssuingDateAnchor(),
		Adjustment:      env.IssuingDateAdjustment(),
		GracePeriodDays: grace,
	}
}

// NewInvoice returns a generating invoice of kind k for the customer env,
// numbered as a draft and dated from the local billing date of billingAt.
func NewInvoice(d *Deps, env *CustomerEnv, k InvoiceKind, currency money.Currency, billingAt time.Time) *invoice.Invoice {
	now := d.Now()
	inv := &invoice.Invoice{
		ID:              core.NewID[core.InvoiceID](d.IDs),
		OrganizationID:  env.OrganizationID(),
		CustomerID:      env.Customer.ID,
		BillingEntityID: env.BillingEntity.ID,
		Type:            k.Type,
		Status:          invoice.StatusGenerating,
		PaymentStatus:   core.PaymentStatusPending,
		TaxStatus:       invoice.TaxStatusSucceeded,
		Number:          invoice.DraftNumber(env.BillingEntity.DocumentNumberPrefix),
		Currency:        currency,
		TaxesRate:       money.Zero,
		SelfBilled:      env.Customer.SelfBilled(),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	SetInvoiceDates(inv, env, k, env.Today(billingAt))
	return inv
}

// SetInvoiceDates sets the issuing date, the payment due date, the grace
// period and the expected finalization date of inv from its local billing
// date.
func SetInvoiceDates(inv *invoice.Invoice, env *CustomerEnv, k InvoiceKind, billingDate core.Date) {
	f := k.IssuingFacts(env)
	inv.AppliedGracePeriodDays = f.GracePeriodDays
	inv.NetPaymentTermDays = env.NetPaymentTermDays()
	inv.IssuingDate = invoice.IssuingDate(billingDate, f)
	inv.PaymentDueDate = invoice.PaymentDueDate(inv.IssuingDate, inv.NetPaymentTermDays)
	inv.ExpectedFinalizationDate = invoice.ExpectedFinalizationDate(billingDate, f.GracePeriodDays)
}

// RedateOnFinalization redates a draft being finalized, or an open or
// failed invoice whose taxes were just computed (domain §22.12): the
// issuing date moves to today unless it is kept, and the due date is
// always the issuing date plus the customer's applicable net payment term
// at that moment, not the invoice's snapshot, which keeps its value (lago
// app/services/invoices/refresh_draft_and_finalize_service.rb:22,36,74-87,
// app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:43-46,145-158).
func RedateOnFinalization(inv *invoice.Invoice, env *CustomerEnv, k InvoiceKind, now time.Time) {
	if !invoice.KeepsIssuingDateOnFinalization(k.IssuingFacts(env)) {
		inv.IssuingDate = env.Today(now)
	}
	inv.PaymentDueDate = invoice.PaymentDueDate(inv.IssuingDate, env.NetPaymentTermDays())
}

// RedateRegenerated dates an invoice regenerated from a voided one whose
// taxes are computed: it is issued today and due after the customer's
// applicable net payment term (lago
// app/services/invoices/regenerate_from_voided_service.rb:52-53,72-78).
func RedateRegenerated(inv *invoice.Invoice, env *CustomerEnv, now time.Time) {
	inv.IssuingDate = env.Today(now)
	inv.PaymentDueDate = invoice.PaymentDueDate(inv.IssuingDate, env.NetPaymentTermDays())
}

// FinalStatus returns the status a generated invoice leaves generating
// with.
func FinalStatus(inv *invoice.Invoice, env *CustomerEnv, k InvoiceKind, gated bool) invoice.Status {
	return invoice.FinalStatus(invoice.FinalStatusFacts{
		GracePeriodDays:    inv.AppliedGracePeriodDays,
		UsesGracePeriod:    k.UsesGracePeriod,
		Gated:              gated,
		TotalAmount:        inv.TotalAmount,
		TaxesPending:       inv.TaxStatus == invoice.TaxStatusPending,
		FeesAmount:         inv.FeesAmount,
		FinalizeZeroAmount: env.FinalizeZeroAmountInvoice(),
	})
}

// AssignNumber gives a finalizing invoice its gapless sequential
// numbers and its number (domain §22.11): per customer and billing entity
// always; per billing entity for every non-self-billed invoice, whatever
// the numbering mode, so an entity switching to per_billing_entity goes on
// from the invoices it already issued (lago counts them the same way). Only
// per_billing_entity entities number with it. Drafts keep their draft
// number.
func AssignNumber(ctx context.Context, r port.Repos, env *CustomerEnv, inv *invoice.Invoice, now time.Time) error {
	if inv.SequentialID != nil {
		return nil
	}
	org := inv.OrganizationID
	// The per-customer sequence is scoped to (customer, billing entity)
	// whatever the customer's current entity (lago Invoice sequenced
	// scope, app/models/invoice.rb:141): a customer moved to another
	// entity, or billed by an override entity, never reuses numbers.
	scope := ScopeUUID(env.Customer.ID.UUID(), inv.BillingEntityID.UUID())
	seq, err := r.Sequences.Next(ctx, org, port.SequenceKey{Name: port.SequenceInvoiceCustomer, Scope: scope})
	if err != nil {
		return fmt.Errorf("number invoice %s: %w", inv.ID, err)
	}
	inv.SequentialID = &seq
	be := env.BillingEntity
	prefix := be.DocumentNumberPrefix
	if inv.SelfBilled {
		inv.Number = invoice.PerCustomerNumber(prefix, env.Customer.SequentialID, seq)
		return nil
	}
	beSeq, err := r.Sequences.Next(ctx, org, port.SequenceKey{Name: port.SequenceInvoiceBillingEntity, Scope: be.ID.UUID()})
	if err != nil {
		return fmt.Errorf("number invoice %s: %w", inv.ID, err)
	}
	if be.DocumentNumbering == organization.DocumentNumberingPerBillingEntity {
		inv.BillingEntitySequentialID = &beSeq
		inv.Number = invoice.PerBillingEntityNumber(prefix, now, BillingEntityLocation(be), beSeq)
		return nil
	}
	inv.Number = invoice.PerCustomerNumber(prefix, env.Customer.SequentialID, seq)
	return nil
}

// BillingEntityLocation returns the billing entity's time zone, UTC when
// unset.
func BillingEntityLocation(be *organization.BillingEntity) *time.Location {
	if be.Timezone == nil {
		return time.UTC
	}
	return be.Timezone
}

// CreditNoteNumber assigns the next credit note sequence of an invoice and
// returns it.
func CreditNoteNumber(ctx context.Context, r port.Repos, inv *invoice.Invoice) (int64, error) {
	seq, err := r.Sequences.Next(ctx, inv.OrganizationID, port.SequenceKey{Name: port.SequenceCreditNote, Scope: inv.ID.UUID()})
	if err != nil {
		return 0, fmt.Errorf("number credit note of invoice %s: %w", inv.ID, err)
	}
	return seq, nil
}

// Finalize moves a generating, draft or open invoice to finalized: it
// numbers it, redates a draft and sets finalized_at. The number's month
// and a draft's new issuing date come from the same instant, as in lago,
// where both read the clock of the finalizing job (app/models/invoice.rb:738,
// app/services/invoices/refresh_draft_and_finalize_service.rb:76): the
// finalization time of ctx ([WithFinalizationTime]) when set, else now.
// finalized_at is always now.
func Finalize(ctx context.Context, r port.Repos, env *CustomerEnv, inv *invoice.Invoice, k InvoiceKind, now time.Time) error {
	on := now
	if at, ok := ctx.Value(finalizationTimeKey{}).(time.Time); ok && !at.IsZero() {
		on = at
	}
	if inv.Status == invoice.StatusDraft {
		RedateOnFinalization(inv, env, k, on)
	}
	if err := AssignNumber(ctx, r, env, inv, on); err != nil {
		return err
	}
	if err := inv.Transition(invoice.StatusFinalized, now); err != nil {
		return fmt.Errorf("finalize invoice %s: %w", inv.ID, err)
	}
	return nil
}

type finalizationTimeKey struct{}

// WithFinalizationTime returns ctx carrying the instant at which [Finalize]
// redates and numbers a draft, in place of the worker's clock: a draft
// finalized by the due-drafts sweep is issued on the sweep's local date,
// and numbered for its month, even when its job runs late, past midnight. Lago redates with the clock of the worker
// running Invoices::FinalizeJob
// (app/services/invoices/refresh_draft_and_finalize_service.rb:77), so a
// late job issues the invoice a day later there.
func WithFinalizationTime(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, finalizationTimeKey{}, at)
}

// ObjectID returns the UUID of any identifier, for webhook object IDs.
func ObjectID[K any](id core.ID[K]) uuid.UUID { return id.UUID() }
