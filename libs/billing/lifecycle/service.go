package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Biller generates subscription invoices; invoicing.Service implements it.
type Biller interface {
	// Bill generates, in the transaction of r, the invoices of req.
	Bill(ctx context.Context, r port.Repos, req invoicing.BillRequest) ([]*invoice.Invoice, error)
}

// TerminationCrediter issues the credit note of a terminated in-advance
// subscription; creditnotes.Service implements it.
type TerminationCrediter interface {
	// CreateFromTermination issues, in the transaction of r, the credit
	// note returning the unused part of the last subscription fee, or nil.
	CreateFromTermination(ctx context.Context, r port.Repos, in creditnotes.TerminationInput) (*creditnote.CreditNote, error)
}

// Rule codes of the refused operations, carried by *core.RuleError.
const (
	// CodeSubscriptionIncomplete: the customer has an incomplete
	// subscription with that ID or external ID.
	CodeSubscriptionIncomplete = "subscription_incomplete"
	// CodeSubscriptionCanceled: a canceled subscription cannot be
	// terminated.
	CodeSubscriptionCanceled = "subscription_canceled"
	// CodeSubscriptionTerminated: a terminated subscription cannot be
	// terminated again.
	CodeSubscriptionTerminated = "subscription_terminated"
	// CodeNextSubscriptionIncomplete: the subscription's replacement waits
	// for an activation rule.
	CodeNextSubscriptionIncomplete = "next_subscription_incomplete"
	// CodeNotChangeable: only pending and active subscriptions change
	// plan.
	CodeNotChangeable = "subscription_not_changeable"
)

// Field error codes of the subscription checks.
const (
	// CodeValueAlreadyExist: another customer has an active subscription
	// with that external ID.
	CodeValueAlreadyExist = "value_already_exist"
)

// ErrNoBiller reports a billing step run before a [Biller] was wired.
var ErrNoBiller = errors.New("lifecycle: no biller")

// Service bills subscription lifecycle changes.
type Service struct {
	d       *kernel.Deps
	biller  Biller
	credits TerminationCrediter
}

// New returns the service over the checked dependencies d. Termination
// credit notes are issued by a creditnotes.Service over d until
// [Service.WithCreditNotes] replaces it; invoices need [Service.WithBiller].
func New(d *kernel.Deps) *Service {
	return &Service{d: d, credits: creditnotes.New(d)}
}

// WithBiller sets the invoice generator and returns s.
func (s *Service) WithBiller(b Biller) *Service {
	s.biller = b
	return s
}

// WithCreditNotes sets the termination credit note issuer and returns s.
func (s *Service) WithCreditNotes(c TerminationCrediter) *Service {
	s.credits = c
	return s
}

// bill bills items at billingAt; empty items bill nothing.
func (s *Service) bill(ctx context.Context, r port.Repos, org core.OrganizationID, billingAt time.Time, skipCharges bool, items ...invoicing.BillItem) error {
	if len(items) == 0 {
		return nil
	}
	if s.biller == nil {
		return ErrNoBiller
	}
	req := invoicing.BillRequest{OrganizationID: org, Items: items, BillingAt: billingAt.UTC(), SkipCharges: skipCharges}
	if _, err := s.biller.Bill(ctx, r, req); err != nil {
		return fmt.Errorf("bill subscriptions: %w", err)
	}
	return nil
}

// billAdvanceCharges enqueues the regrouping of the paid, non-invoiceable
// in-advance fees of the ended subscription sub at billingAt (lago
// BillNonInvoiceableFeesJob on termination and plan change).
func (s *Service) billAdvanceCharges(ctx context.Context, r port.Repos, sub *subscription.Subscription, billingAt time.Time) error {
	at := billingAt.UTC()
	args := kernel.BillSubscriptionArgs{SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: at, Reason: string(invoice.InvoicingReasonSubscriptionTerminating)}
	return kernel.Enqueue(ctx, r, sub.OrganizationID, kernel.JobBillAdvanceCharges, args, fmt.Sprintf("advance:ended:%s:%d", sub.ID, at.Unix()), s.d.Now())
}

// emit records the domain event t on sub.
func (s *Service) emit(ctx context.Context, t webhook.EventType, sub *subscription.Subscription) error {
	return kernel.Emit(ctx, s.d, sub.OrganizationID, t, "subscription", sub.ID.UUID(), sub)
}

// lock takes the customer's billing schedule lock then the subscription
// lock of each id.
func lock(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, ids ...core.SubscriptionID) error {
	if err := kernel.LockCustomer(ctx, r, org, cust, customer.LockScopeBillingSchedule); err != nil {
		return err
	}
	for _, id := range ids {
		if err := kernel.LockSubscription(ctx, r, org, id); err != nil {
			return err
		}
	}
	return nil
}

// update stores sub.
func update(ctx context.Context, r port.Repos, sub *subscription.Subscription) error {
	if err := r.Subscriptions.Update(ctx, sub); err != nil {
		return fmt.Errorf("update subscription %s: %w", sub.ID, err)
	}
	return nil
}

// item is a bill item.
func item(sub *subscription.Subscription, reason invoice.InvoicingReason) invoicing.BillItem {
	return invoicing.BillItem{SubscriptionID: sub.ID, Reason: reason}
}
