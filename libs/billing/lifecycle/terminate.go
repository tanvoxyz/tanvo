package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// CodeTaxesPending: the last subscription invoice waits for its tax
// provider, so its unused part cannot be credited yet.
const CodeTaxesPending = "cannot_terminate_with_pending_taxes"

// TerminateInput asks for the termination of a subscription (domain
// §22.14).
type TerminateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the terminated subscription.
	SubscriptionID core.SubscriptionID
	// At is the termination instant; zero means now.
	At time.Time
	// OnTerminationInvoice overrides the subscription's setting when set.
	OnTerminationInvoice subscription.TerminationInvoice
	// OnTerminationCreditNote overrides the subscription's setting when
	// set (in-advance plans only).
	OnTerminationCreditNote subscription.TerminationCreditNote
}

// terminateOptions are the options of one termination.
type terminateOptions struct {
	at           time.Time
	upgrade      bool
	onInvoice    subscription.TerminationInvoice
	onCreditNote subscription.TerminationCreditNote
}

// Terminate terminates a subscription per its status (domain §22.14): an
// incomplete one is canceled with its rules declined, a pending one is
// canceled, an active one is terminated at in.At with its termination
// credit note and invoice, and its pending next subscription canceled.
// Canceled and terminated subscriptions are refused with a
// *core.RuleError (domain §22.14; lago returns a terminated one
// unchanged).
func (s *Service) Terminate(ctx context.Context, in TerminateInput) (*subscription.Subscription, error) {
	if in.OnTerminationInvoice != "" && !in.OnTerminationInvoice.Valid() {
		return nil, core.InvalidEnum("on_termination_invoice", string(in.OnTerminationInvoice))
	}
	if in.OnTerminationCreditNote != "" && !in.OnTerminationCreditNote.Valid() {
		return nil, core.InvalidEnum("on_termination_credit_note", string(in.OnTerminationCreditNote))
	}
	at := in.At
	if at.IsZero() {
		at = s.d.Now()
	}
	var out *subscription.Subscription
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		env, err := s.lockedEnv(ctx, r, in.OrganizationID, in.SubscriptionID)
		if err != nil {
			return err
		}
		out = env.Subscription
		_, err = s.terminate(ctx, r, env, terminateOptions{
			at: at.UTC(), onInvoice: in.OnTerminationInvoice, onCreditNote: in.OnTerminationCreditNote,
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("terminate subscription %s: %w", in.SubscriptionID, err)
	}
	return out, nil
}

// terminate terminates env's subscription in the transaction of r and
// reports whether it changed.
//
// source: app/services/subscriptions/terminate_service.rb
func (s *Service) terminate(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, o terminateOptions) (bool, error) {
	sub := env.Subscription
	switch subscription.TerminationFor(sub.Status) {
	case subscription.TerminationDecline:
		return true, s.decline(ctx, r, sub, o.at)
	case subscription.TerminationRefused:
		if sub.Status == subscription.StatusCanceled {
			return false, core.NewRuleError(CodeSubscriptionCanceled, "the subscription is canceled")
		}
		return false, core.NewRuleError(CodeSubscriptionTerminated, "the subscription is terminated")
	}
	if !o.upgrade && env.Next != nil && env.Next.Status == subscription.StatusIncomplete {
		return false, core.NewRuleError(CodeNextSubscriptionIncomplete, "the next subscription is incomplete")
	}
	if sub.Status == subscription.StatusPending {
		return true, s.cancelPending(ctx, r, env, o.at)
	}
	if o.onInvoice != "" {
		sub.OnTerminationInvoice = o.onInvoice
	}
	if o.onCreditNote != "" && env.Plan.PayInAdvance {
		sub.OnTerminationCreditNote = o.onCreditNote
	}
	if err := sub.Terminate(o.at); err != nil {
		return false, fmt.Errorf("terminate subscription %s: %w", sub.ID, err)
	}
	sub.NextBillingAt = nil
	if err := update(ctx, r, sub); err != nil {
		return false, err
	}
	if err := s.terminationCreditNote(ctx, r, env, o); err != nil {
		return false, err
	}
	if !o.upgrade && sub.OnTerminationInvoice != subscription.TerminationInvoiceSkip {
		if err := s.bill(ctx, r, sub.OrganizationID, o.at, false, item(sub, invoice.InvoicingReasonSubscriptionTerminating)); err != nil {
			return false, err
		}
	}
	if err := s.billAdvanceCharges(ctx, r, sub, o.at); err != nil {
		return false, err
	}
	if !o.upgrade && env.Next != nil && env.Next.Status == subscription.StatusPending {
		if err := env.Next.Cancel(o.at, subscription.CancellationReasonManual); err != nil {
			return false, fmt.Errorf("cancel next subscription %s: %w", env.Next.ID, err)
		}
		if err := update(ctx, r, env.Next); err != nil {
			return false, err
		}
		if err := s.emit(ctx, webhook.EventTypeSubscriptionCanceled, env.Next); err != nil {
			return false, err
		}
	}
	return true, s.emit(ctx, webhook.EventTypeSubscriptionTerminated, sub)
}

// decline cancels an incomplete subscription manually and declines its
// pending rules (domain §9.3).
//
// source: app/services/subscriptions/activation_rules/cancel_service.rb
func (s *Service) decline(ctx context.Context, r port.Repos, sub *subscription.Subscription, at time.Time) error {
	rules, err := r.ActivationRules.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
	}
	for _, rule := range rules {
		if rule.Status != subscription.ActivationRuleStatusPending {
			continue
		}
		if err := rule.Decline(at); err != nil {
			return fmt.Errorf("decline activation rule %s: %w", rule.ID, err)
		}
		if err := r.ActivationRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update activation rule %s: %w", rule.ID, err)
		}
	}
	if err := sub.Cancel(at, subscription.CancellationReasonManual); err != nil {
		return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	return s.emit(ctx, webhook.EventTypeSubscriptionCanceled, sub)
}

// cancelPending cancels a pending subscription (a future start or a
// scheduled downgrade, whose current subscription is then updated).
func (s *Service) cancelPending(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, at time.Time) error {
	sub := env.Subscription
	if err := sub.Cancel(at, subscription.CancellationReasonManual); err != nil {
		return fmt.Errorf("cancel subscription %s: %w", sub.ID, err)
	}
	if err := update(ctx, r, sub); err != nil {
		return err
	}
	if env.Previous != nil {
		if err := s.emit(ctx, webhook.EventTypeSubscriptionUpdated, env.Previous); err != nil {
			return err
		}
	}
	return s.emit(ctx, webhook.EventTypeSubscriptionCanceled, sub)
}

// terminationCreditNote credits the unused part of the base fee of an
// in-advance plan already invoiced for the current period.
func (s *Service) terminationCreditNote(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, o terminateOptions) error {
	sub := env.Subscription
	if !env.Plan.PayInAdvance {
		return nil
	}
	// The credit note takes the credit_notes lock and the termination
	// invoice billed next deducts coupons first: take the balance locks
	// in order now (kernel lock order).
	if err := kernel.LockCustomerBalances(ctx, r, sub.OrganizationID, sub.CustomerID); err != nil {
		return err
	}
	switch sub.OnTerminationCreditNote {
	case subscription.TerminationCreditNoteCredit, subscription.TerminationCreditNoteRefund,
		subscription.TerminationCreditNoteOffset, "":
	default:
		return nil
	}
	issued, err := s.inAdvanceInvoiced(ctx, r, env, o.at)
	if err != nil || !issued {
		return err
	}
	pending, err := lastSubscriptionFeeTaxesPending(ctx, r, sub)
	if err != nil {
		return err
	}
	if pending {
		return core.NewRuleError(CodeTaxesPending, "the last subscription fee's invoice waits for its taxes")
	}
	_, err = s.credits.CreateFromTermination(ctx, r, creditnotes.TerminationInput{
		OrganizationID: sub.OrganizationID, SubscriptionID: sub.ID,
		Reason: creditnote.ReasonOrderCancellation, Upgrade: o.upgrade, OnTermination: sub.OnTerminationCreditNote,
	})
	if err != nil {
		return fmt.Errorf("termination credit note of %s: %w", sub.ID, err)
	}
	return nil
}

// lastSubscriptionFeeTaxesPending reports whether the invoice of the
// subscription's latest base fee waits for its tax provider. It looks at
// the base fee, not at the latest invoice: a progressive billing or
// in-advance charge invoice may come after it.
//
// source: app/services/subscriptions/terminate_service.rb:224 (blocked_by_pending_taxes?), app/models/subscription.rb:311
func lastSubscriptionFeeTaxesPending(ctx context.Context, r port.Repos, sub *subscription.Subscription) (bool, error) {
	fees, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
		return r.Fees.List(ctx, sub.OrganizationID, invoice.FeeFilter{SubscriptionID: sub.ID, Types: []core.FeeType{core.FeeTypeSubscription}}, p)
	})
	if err != nil {
		return false, fmt.Errorf("load subscription fees of %s: %w", sub.ID, err)
	}
	var last *invoice.Fee
	for _, f := range fees {
		if last == nil || !f.CreatedAt.Before(last.CreatedAt) {
			last = f
		}
	}
	if last == nil || last.InvoiceID.IsZero() {
		return false, nil
	}
	inv, err := r.Invoices.Get(ctx, sub.OrganizationID, last.InvoiceID)
	if err != nil {
		return false, fmt.Errorf("load invoice %s: %w", last.InvoiceID, err)
	}
	return inv.TaxStatus == invoice.TaxStatusPending, nil
}

// inAdvanceInvoiced reports whether the in-advance base fee of the period
// holding at was invoiced: always in the first period (billed at the
// start), otherwise when an invoice billing the base fee covers the
// period's start ([billsBaseFee]). A
// subscription terminated by its ending date before the periodic run
// issued that invoice gets no credit note.
func (s *Service) inAdvanceInvoiced(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, at time.Time) (bool, error) {
	sub := env.Subscription
	if sub.StartedAt == nil {
		return false, nil
	}
	live := *sub
	live.Status, live.TerminatedAt = subscription.StatusActive, nil
	ps := live.PeriodSubscription(env.Plan.PeriodPlan(), env.Location(), env.ChainStart(), period.NoChange)
	dates, err := ps.Dates(at, period.CurrentUsage)
	if err != nil {
		return false, fmt.Errorf("dates of subscription %s: %w", sub.ID, err)
	}
	start := dates.CurrentPeriodStart()
	if start.Before(*sub.StartedAt) {
		return true, nil
	}
	rows, err := r.InvoiceSubscriptions.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return false, fmt.Errorf("load invoices of subscription %s: %w", sub.ID, err)
	}
	for _, row := range rows {
		if !billsBaseFee(row.Reason) {
			continue
		}
		if !row.Period.From.After(start) && row.Period.To.After(start) {
			return true, nil
		}
	}
	return false, nil
}

// billsBaseFee reports whether an invoice subscription row of reason r
// can bill the base fee. Lago matches any row on the period's boundaries
// (InvoiceSubscription.matching? with recurring: false,
// app/services/subscriptions/terminate_service.rb:183), so an in-advance
// charge or progressive billing invoice covering the period start counts
// there as the in-advance base fee being invoiced, and credits a base fee
// never billed. Tanvo looks only at the rows that bill the base fee.
func billsBaseFee(r invoice.InvoicingReason) bool {
	switch r {
	case invoice.InvoicingReasonSubscriptionStarting, invoice.InvoicingReasonSubscriptionPeriodic, invoice.InvoicingReasonSubscriptionTerminating:
		return true
	}
	return false
}

// TerminateEnded terminates the active subscriptions of org whose
// ending_at falls on the local day of at (lago's hourly
// TerminateEndedSubscriptionsJob), each at its ending_at or at, whichever
// comes first but not before its start, in its own transaction. It returns the number terminated.
//
// source: app/jobs/clock/terminate_ended_subscriptions_job.rb
func (s *Service) TerminateEnded(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	const prefilter = 3 * 24 * time.Hour
	var due []*kernel.SubscriptionEnv
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := r.Subscriptions.ListEndingBetween(ctx, org, at.Add(-prefilter), at.Add(prefilter))
		if err != nil {
			return fmt.Errorf("list ending subscriptions: %w", err)
		}
		for _, sub := range subs {
			env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
			if err != nil {
				return err
			}
			if sub.EndingAt != nil && env.Customer.Today(*sub.EndingAt) == env.Customer.Today(at) {
				due = append(due, env)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var errs []error
	n := 0
	for _, env := range due {
		end := *env.Subscription.EndingAt
		if end.After(at) {
			end = at
		}
		// Never before the start: a subscription started after its
		// ending_at is terminated at its start (lago terminates at
		// Time.current, which cannot precede it).
		if st := env.Subscription.StartedAt; st != nil && end.Before(*st) {
			end = *st
		}
		_, err := s.Terminate(ctx, TerminateInput{OrganizationID: org, SubscriptionID: env.Subscription.ID, At: end})
		var rule *core.RuleError
		if errors.As(err, &rule) && (rule.Code == CodeSubscriptionTerminated || rule.Code == CodeSubscriptionCanceled) {
			// Terminated or canceled between the scan and the lock: the
			// sweep is idempotent and skips it.
			continue
		}
		if err != nil {
			s.d.Logger.ErrorContext(ctx, "terminate ended subscription", "subscription_id", env.Subscription.ID, "error", err)
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
