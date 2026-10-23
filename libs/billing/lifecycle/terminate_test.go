package lifecycle_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func (f *fixture) terminate(sub *subscription.Subscription, opts ...func(*lifecycle.TerminateInput)) error {
	in := lifecycle.TerminateInput{OrganizationID: f.OrgID(), SubscriptionID: sub.ID}
	for _, o := range opts {
		o(&in)
	}
	_, err := f.svc.Terminate(f.Ctx, in)
	return err
}

// source: spec/services/subscriptions/terminate_service_spec.rb:15
func TestTerminateActive(t *testing.T) {
	f := newFixture(t, now)
	sub := f.name(f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, -5))), "sub")
	must(t, f.terminate(sub))
	got := f.reload(sub.ID)
	check(t, "status", got.Status, subscription.StatusTerminated)
	check(t, "terminated_at", stamp(*got.TerminatedAt), stamp(now))
	checkSlice(t, "bills", f.bills(), []string{"2026-03-10 12:00:00 sub:subscription_terminating"})
	check(t, "credit notes", len(f.credits.ins), 0)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionTerminated))
}

// source: spec/services/subscriptions/terminate_service_spec.rb:99
func TestTerminateFutureCancels(t *testing.T) {
	f := newFixture(t, now)
	sub := f.pending(f.Plan(period.Monthly, 1000))
	must(t, f.terminate(sub))
	got := f.reload(sub.ID)
	check(t, "status", got.Status, subscription.StatusCanceled)
	check(t, "reason", got.CancellationReason, subscription.CancellationReasonManual)
	checkSlice(t, "bills", f.bills(), nil)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionCanceled))
}

// source: spec/services/subscriptions/terminate_service_spec.rb:121
func TestTerminatePendingDowngrade(t *testing.T) {
	f := newFixture(t, now)
	cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
	next := f.downgrade(cur)
	must(t, f.terminate(next))
	check(t, "next status", f.reload(next.ID).Status, subscription.StatusCanceled)
	check(t, "current status", f.reload(cur.ID).Status, subscription.StatusActive)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionUpdated,
		webhook.EventTypeSubscriptionUpdated, webhook.EventTypeSubscriptionCanceled))
}

func TestTerminateRefused(t *testing.T) {
	tests := []struct {
		name   string
		source string
		set    func(*subscription.Subscription)
		code   string
	}{
		{
			name: "canceled", source: "spec/services/subscriptions/terminate_service_spec.rb:141",
			set:  func(s *subscription.Subscription) { s.Status, s.CanceledAt = subscription.StatusCanceled, &now },
			code: lifecycle.CodeSubscriptionCanceled,
		},
		{
			name: "terminated (Tanvo refuses, domain §22.14)",
			set:  func(s *subscription.Subscription) { s.Status, s.TerminatedAt = subscription.StatusTerminated, &now },
			code: lifecycle.CodeSubscriptionTerminated,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
			f.UpdateSubscription(sub, tc.set)
			checkRule(t, f.terminate(sub), tc.code)
			checkSlice(t, "bills", f.bills(), nil)
		})
	}
}

// source: spec/services/subscriptions/terminate_service_spec.rb:157
func TestTerminateIncompleteDeclines(t *testing.T) {
	f := newFixture(t, now)
	sub := f.pending(f.Plan(period.Monthly, 1000, inAdvance))
	f.UpdateSubscription(sub, func(s *subscription.Subscription) { must(t, s.MarkIncomplete(now)) })
	f.rule(sub, subscription.ActivationRuleStatusPending)
	must(t, f.terminate(sub, func(in *lifecycle.TerminateInput) {
		in.OnTerminationInvoice = subscription.TerminationInvoiceGenerate
	}))
	got := f.reload(sub.ID)
	check(t, "status", got.Status, subscription.StatusCanceled)
	check(t, "reason", got.CancellationReason, subscription.CancellationReasonManual)
	checkSlice(t, "rules", f.rules(sub), []subscription.ActivationRuleStatus{subscription.ActivationRuleStatusDeclined})
	checkSlice(t, "bills", f.bills(), nil)
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionCanceled))
}

// source: spec/services/subscriptions/terminate_service_spec.rb:280
func TestTerminateCancelsPendingNext(t *testing.T) {
	f := newFixture(t, now)
	cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
	next := f.downgrade(cur)
	must(t, f.terminate(cur))
	check(t, "current status", f.reload(cur.ID).Status, subscription.StatusTerminated)
	check(t, "next status", f.reload(next.ID).Status, subscription.StatusCanceled)
	// Tanvo emits subscription.canceled on every pending → canceled
	// transition (domain §9.2); lago's mark_as_canceled! emits nothing.
	checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionUpdated,
		webhook.EventTypeSubscriptionCanceled, webhook.EventTypeSubscriptionTerminated))
}

// source: spec/services/subscriptions/terminate_service_spec.rb:311
func TestTerminateIncompleteNextRefused(t *testing.T) {
	f := newFixture(t, now)
	cur := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
	next := f.downgrade(cur)
	f.UpdateSubscription(next, func(s *subscription.Subscription) { must(t, s.MarkIncomplete(now)) })
	checkRule(t, f.terminate(cur), lifecycle.CodeNextSubscriptionIncomplete)
	check(t, "status", f.reload(cur.ID).Status, subscription.StatusActive)
}

// inAdvanceSubscription is an anniversary in-advance subscription started
// on 2026-02-01 whose current period, from 2026-03-01, was invoiced.
func (f *fixture) inAdvanceSubscription(linked bool, opts ...func(*subscription.Subscription)) (*subscription.Subscription, *invoice.Invoice) {
	start := billingtest.Date(2026, time.February, 1)
	c := f.Customer()
	sub := f.name(f.Subscription(c, f.Plan(period.Monthly, 1000, inAdvance),
		append([]func(*subscription.Subscription){billingtest.SubscribedAt(start), billingtest.Anniversary}, opts...)...), "sub")
	inv := f.StoredInvoice(c)
	f.StoredFee(inv, 1000, billingtest.SubscriptionFee(sub))
	if linked {
		f.LinkSubscription(inv, sub, invoice.InvoicingReasonSubscriptionPeriodic, billingtest.Date(2026, time.March, 1), billingtest.Date(2026, time.April, 1))
	}
	return sub, inv
}

func TestTerminateInAdvanceCreditNote(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		stored   subscription.TerminationCreditNote
		given    subscription.TerminationCreditNote
		linked   bool
		want     subscription.TerminationCreditNote // "" for no credit note
		wantStay subscription.TerminationCreditNote
	}{
		{
			name: "default credit", source: "spec/services/subscriptions/terminate_service_spec.rb:408",
			stored: subscription.TerminationCreditNoteCredit, linked: true,
			want: subscription.TerminationCreditNoteCredit, wantStay: subscription.TerminationCreditNoteCredit,
		},
		{
			name: "skip", source: "spec/services/subscriptions/terminate_service_spec.rb:426",
			stored: subscription.TerminationCreditNoteCredit, given: subscription.TerminationCreditNoteSkip, linked: true,
			wantStay: subscription.TerminationCreditNoteSkip,
		},
		{
			name: "refund", source: "spec/services/subscriptions/terminate_service_spec.rb:443",
			stored: subscription.TerminationCreditNoteCredit, given: subscription.TerminationCreditNoteRefund, linked: true,
			want: subscription.TerminationCreditNoteRefund, wantStay: subscription.TerminationCreditNoteRefund,
		},
		{
			name: "offset", source: "spec/services/subscriptions/terminate_service_spec.rb:460",
			stored: subscription.TerminationCreditNoteCredit, given: subscription.TerminationCreditNoteOffset, linked: true,
			want: subscription.TerminationCreditNoteOffset, wantStay: subscription.TerminationCreditNoteOffset,
		},
		{
			name: "not given keeps the subscription's", source: "spec/services/subscriptions/terminate_service_spec.rb:477",
			stored: subscription.TerminationCreditNoteRefund, linked: true,
			want: subscription.TerminationCreditNoteRefund, wantStay: subscription.TerminationCreditNoteRefund,
		},
		{
			name: "period not invoiced yet", source: "spec/services/subscriptions/terminate_service_spec.rb:500",
			stored: subscription.TerminationCreditNoteCredit, wantStay: subscription.TerminationCreditNoteCredit,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub, _ := f.inAdvanceSubscription(tc.linked, func(s *subscription.Subscription) { s.OnTerminationCreditNote = tc.stored })
			must(t, f.terminate(sub, func(in *lifecycle.TerminateInput) { in.OnTerminationCreditNote = tc.given }))
			check(t, "on_termination_credit_note", f.reload(sub.ID).OnTerminationCreditNote, tc.wantStay)
			if tc.want == "" {
				check(t, "credit notes", len(f.credits.ins), 0)
				return
			}
			if len(f.credits.ins) != 1 {
				t.Fatalf("credit notes: got = %d, want = 1", len(f.credits.ins))
			}
			in := f.credits.ins[0]
			check(t, "on termination", in.OnTermination, tc.want)
			check(t, "reason", in.Reason, creditnote.ReasonOrderCancellation)
			check(t, "upgrade", in.Upgrade, false)
			checkSlice(t, "bills", f.bills(), []string{"2026-03-10 12:00:00 sub:subscription_terminating"})
		})
	}
}

// Only an invoice billing the base fee makes the period invoiced: an
// in-advance charge or progressive billing invoice covering the period
// start does not, so terminating gives no credit note for a base fee
// never billed.
//
// source: app/services/subscriptions/terminate_service.rb:183
func TestTerminateInAdvanceOnlyChargesInvoiced(t *testing.T) {
	for _, reason := range []invoice.InvoicingReason{
		invoice.InvoicingReasonInAdvanceCharge,
		invoice.InvoicingReasonInAdvanceChargePeriodic,
		invoice.InvoicingReasonProgressiveBilling,
	} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFixture(t, now)
			sub, inv := f.inAdvanceSubscription(false)
			f.LinkSubscription(inv, sub, reason, billingtest.Date(2026, time.March, 1), billingtest.Date(2026, time.April, 1))
			must(t, f.terminate(sub))
			check(t, "credit notes", len(f.credits.ins), 0)
		})
	}
}

// source: spec/services/subscriptions/terminate_service_spec.rb:508
func TestTerminatePendingTaxesRefused(t *testing.T) {
	f := newFixture(t, now)
	sub, inv := f.inAdvanceSubscription(true)
	inv.TaxStatus = invoice.TaxStatusPending
	f.SaveInvoice(inv)
	checkRule(t, f.terminate(sub), lifecycle.CodeTaxesPending)
	check(t, "status", f.reload(sub.ID).Status, subscription.StatusActive)
	check(t, "credit notes", len(f.credits.ins), 0)
}

// The pending-taxes refusal looks at the invoice of the latest base fee,
// not at the latest invoice: a later in-advance charge invoice waiting
// for its taxes does not block the termination.
//
// source: app/services/subscriptions/terminate_service.rb:224, app/models/subscription.rb:311
func TestTerminatePendingTaxesOfLaterChargeInvoice(t *testing.T) {
	f := newFixture(t, now)
	sub, inv := f.inAdvanceSubscription(true)
	charges := f.StoredInvoice(f.ReloadCustomer(inv.CustomerID), func(i *invoice.Invoice) { i.TaxStatus = invoice.TaxStatusPending })
	f.LinkSubscription(charges, sub, invoice.InvoicingReasonInAdvanceCharge, now.Add(-time.Hour), now)
	must(t, f.terminate(sub))
	check(t, "status", f.reload(sub.ID).Status, subscription.StatusTerminated)
	check(t, "credit notes", len(f.credits.ins), 1)
}

// source: spec/services/subscriptions/terminate_service_spec.rb:530
func TestTerminateArrearsNoCreditNote(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer()
	sub := f.Subscription(c, f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(billingtest.Date(2026, time.February, 1)))
	inv := f.StoredInvoice(c)
	f.LinkSubscription(inv, sub, invoice.InvoicingReasonSubscriptionPeriodic, billingtest.Date(2026, time.March, 1), billingtest.Date(2026, time.April, 1))
	must(t, f.terminate(sub, func(in *lifecycle.TerminateInput) {
		in.OnTerminationCreditNote = subscription.TerminationCreditNoteCredit
	}))
	check(t, "credit notes", len(f.credits.ins), 0)
	check(t, "on_termination_credit_note", f.reload(sub.ID).OnTerminationCreditNote, "")
}

func TestTerminateInvoiceOption(t *testing.T) {
	tests := []struct {
		name   string
		source string
		given  subscription.TerminationInvoice
		want   []string
	}{
		{
			name: "generate", source: "spec/services/subscriptions/terminate_service_spec.rb:550",
			given: subscription.TerminationInvoiceGenerate, want: []string{"2026-03-10 12:00:00 sub:subscription_terminating"},
		},
		{name: "skip", source: "spec/services/subscriptions/terminate_service_spec.rb:565", given: subscription.TerminationInvoiceSkip},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub := f.name(f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000)), "sub")
			must(t, f.terminate(sub, func(in *lifecycle.TerminateInput) { in.OnTerminationInvoice = tc.given }))
			check(t, "on_termination_invoice", f.reload(sub.ID).OnTerminationInvoice, tc.given)
			checkSlice(t, "bills", f.bills(), tc.want)
		})
	}
}

func TestTerminateAt(t *testing.T) {
	f := newFixture(t, now)
	sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, -1, 0)))
	at := now.Add(-2 * time.Hour)
	must(t, f.terminate(sub, func(in *lifecycle.TerminateInput) { in.At = at }))
	check(t, "terminated_at", stamp(*f.reload(sub.ID).TerminatedAt), stamp(at))
	checkSlice(t, "bills", f.bills(), []string{"2026-03-10 10:00:00 new:subscription_terminating"})
}
