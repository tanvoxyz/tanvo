package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// incomplete creates an incomplete in-advance subscription with a pending
// payment rule and its gating subscription invoice in status st, as the
// cancel_service_spec.rb and expire_service_spec.rb setups do; with st
// empty it has no gating invoice.
func (f *fixture) incomplete(st invoice.Status) (*subscription.Subscription, *invoice.Invoice) {
	f.T.Helper()
	sub, inv, _ := f.incompleteOf(st)
	return sub, inv
}

func (f *fixture) incompleteOf(st invoice.Status) (*subscription.Subscription, *invoice.Invoice, *customer.Customer) {
	f.T.Helper()
	c := f.Customer()
	sub := f.Subscription(c, f.Plan(period.Monthly, 1000, inAdvance), billingtest.SubscribedAt(now.Add(time.Hour)))
	f.UpdateSubscription(sub, func(s *subscription.Subscription) { must(f.T, s.MarkIncomplete(now)) })
	rule := f.rule(sub, subscription.ActivationRuleStatusPending)
	f.Do(func(ctx context.Context, r port.Repos) error {
		e := now.Add(-time.Hour)
		rule.TimeoutHours, rule.ExpiresAt = 48, &e
		return r.ActivationRules.Update(ctx, rule)
	})
	if st == "" {
		return sub, nil, c
	}
	inv := f.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = st })
	f.LinkSubscription(inv, sub, invoice.InvoicingReasonSubscriptionStarting, now.Add(-time.Hour), now.AddDate(0, 1, 0))
	return sub, inv, c
}

func (f *fixture) expire(sub *subscription.Subscription) (*subscription.Subscription, error) {
	return f.svc.ExpireIncomplete(f.Ctx, f.OrgID(), sub.ID)
}

// source: spec/services/subscriptions/activation_rules/expire_service_spec.rb:20
func TestExpireIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		source string
		status invoice.Status
	}{
		{name: "open gating invoice", source: "expire_service_spec.rb:36", status: invoice.StatusOpen},
		// A tax provider failure left the invoice failed; it is closed too.
		{name: "failed gating invoice", source: "cancel_service_spec.rb:173", status: invoice.StatusFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub, inv := f.incomplete(tc.status)
			got, err := f.expire(sub)
			must(t, err)
			check(t, "result status", got.Status, subscription.StatusCanceled)
			stored := f.reload(sub.ID)
			check(t, "status", stored.Status, subscription.StatusCanceled)
			// source: expire_service_spec.rb:48
			check(t, "reason", stored.CancellationReason, subscription.CancellationReasonTimeout)
			checkSlice(t, "rules", f.rules(sub), []subscription.ActivationRuleStatus{subscription.ActivationRuleStatusExpired})
			// source: expire_service_spec.rb:42
			check(t, "invoice status", f.Invoice(inv.ID).Status, invoice.StatusClosed)
			// source: cancel_service_spec.rb:54
			checkSlice(t, "webhooks", f.Webhooks(), hooks(webhook.EventTypeSubscriptionCanceled))
			checkSlice(t, "bills", f.bills(), nil)
		})
	}
}

// source: spec/services/subscriptions/activation_rules/expire_service_spec.rb:66
// The coupon and credit-note credits of the gating invoice are given back
// (lago enqueues AppliedCoupons::RecreditJob and CreditNotes::RecreditJob;
// Tanvo restores them in the transaction, deduct.Service.Restore, whose
// wallet recredit expire_service_spec.rb:92 is ported in deduct).
func TestExpireIncompleteRecredits(t *testing.T) {
	f := newFixture(t, now)
	sub, inv, c := f.incompleteOf(invoice.StatusOpen)
	cp := f.FixedAmountCoupon(100)
	a := f.ApplyCoupon(cp, c, func(a *coupon.AppliedCoupon) {
		n := now
		a.Status, a.TerminatedAt, a.RemainingAmount = coupon.StatusTerminated, &n, new(int64)
	})
	src := f.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
	cn := f.StoredCreditNote(src, 30)
	f.CreateCredit(&invoice.Credit{
		ID: core.NewID[core.InvoiceCreditID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: inv.ID, AppliedCouponID: a.ID,
		Amount: 100, Currency: inv.Currency, BeforeTaxes: true, CreatedAt: now,
	})
	f.CreateCredit(&invoice.Credit{
		ID: core.NewID[core.InvoiceCreditID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: inv.ID, CreditNoteID: cn.ID,
		Amount: 10, Currency: inv.Currency, CreatedAt: now,
	})
	_, err := f.expire(sub)
	must(t, err)
	// source: expire_service_spec.rb:72
	gotCoupon := f.ReloadAppliedCoupon(a.ID)
	check(t, "coupon status", gotCoupon.Status, coupon.StatusActive)
	check(t, "coupon remaining", *gotCoupon.RemainingAmount, int64(100))
	// source: expire_service_spec.rb:85
	check(t, "credit note balance", f.ReloadCreditNote(cn.ID).BalanceAmount, int64(40))
}

// source: spec/services/subscriptions/activation_rules/expire_service_spec.rb:106
func TestExpireIncompleteResolved(t *testing.T) {
	f := newFixture(t, now)
	sub, inv := f.incomplete(invoice.StatusOpen)
	f.UpdateSubscription(sub, func(s *subscription.Subscription) { s.Status = subscription.StatusActive })
	got, err := f.expire(sub)
	must(t, err)
	check(t, "result status", got.Status, subscription.StatusActive)
	check(t, "status", f.reload(sub.ID).Status, subscription.StatusActive)
	checkSlice(t, "rules", f.rules(sub), []subscription.ActivationRuleStatus{subscription.ActivationRuleStatusPending})
	check(t, "invoice status", f.Invoice(inv.ID).Status, invoice.StatusOpen)
	checkSlice(t, "webhooks", f.Webhooks(), nil)
}

// source: spec/services/subscriptions/activation_rules/cancel_service_spec.rb:193
func TestCancelIncompleteWithoutGatingInvoice(t *testing.T) {
	tests := []struct {
		name   string
		status invoice.Status
		run    func(f *fixture, sub *subscription.Subscription) error
	}{
		{name: "expire, no invoice", run: func(f *fixture, sub *subscription.Subscription) error {
			_, err := f.expire(sub)
			return err
		}},
		{name: "terminate, no invoice", run: func(f *fixture, sub *subscription.Subscription) error { return f.terminate(sub) }},
		// Only an open or failed invoice gates (cancel_service.rb:60-65).
		{name: "expire, finalized invoice", status: invoice.StatusFinalized, run: func(f *fixture, sub *subscription.Subscription) error {
			_, err := f.expire(sub)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub, _ := f.incomplete(tc.status)
			err := tc.run(f, sub)
			var ve *core.ValidationError
			if !asValidation(err, &ve) || !ve.Has("subscription", lifecycle.CodeActivationInvoiceNotReady) {
				t.Fatalf("err got = %v, want = %s", err, lifecycle.CodeActivationInvoiceNotReady)
			}
			got := f.reload(sub.ID)
			check(t, "status", got.Status, subscription.StatusIncomplete)
			check(t, "reason", got.CancellationReason, "")
			checkSlice(t, "rules", f.rules(sub), []subscription.ActivationRuleStatus{subscription.ActivationRuleStatusPending})
			checkSlice(t, "webhooks", f.Webhooks(), nil)
		})
	}
}

// The pending or processing payment of the closed gating invoice is
// cancelled at its provider; a payment already final is not.
func TestCancelIncompleteCancelsPendingPayment(t *testing.T) {
	expire := func(f *fixture, sub *subscription.Subscription) error {
		_, err := f.expire(sub)
		return err
	}
	decline := func(f *fixture, sub *subscription.Subscription) error {
		return f.terminate(sub, func(in *lifecycle.TerminateInput) {
			in.OnTerminationInvoice = subscription.TerminationInvoiceGenerate
		})
	}
	tests := []struct {
		name     string
		source   string
		run      func(f *fixture, sub *subscription.Subscription) error
		payment  payment.State
		resolved bool
		want     int
	}{
		{name: "expired, pending payment", source: "expire_service_spec.rb:55", run: expire, payment: payment.StatePending, want: 1},
		{name: "declined, processing payment", source: "cancel_service_spec.rb:58", run: decline, payment: payment.StateProcessing, want: 1},
		{name: "expired, failed payment", source: "expire_service_spec.rb:149", run: expire, payment: payment.StateFailed},
		{name: "declined, no payment", source: "cancel_service_spec.rb:166", run: decline},
		// cancel_service_spec.rb:143 is the same check, reached here by expiry.
		{name: "already resolved", source: "expire_service_spec.rb:125", run: expire, payment: payment.StatePending, resolved: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: spec/services/subscriptions/activation_rules/" + tc.source)
			f := newFixture(t, now)
			sub, inv, c := f.incompleteOf(invoice.StatusOpen)
			if tc.payment != "" {
				f.StoredPayment(payment.InvoicePayable(inv.ID), c.ID, inv.TotalAmount, tc.payment, core.IntegrationID{})
			}
			if tc.resolved {
				f.UpdateSubscription(sub, func(s *subscription.Subscription) { s.Status = subscription.StatusActive })
			}
			_ = tc.run(f, sub)
			got := 0
			for _, k := range f.JobKinds() {
				if k == kernel.JobCancelPayment {
					got++
				}
			}
			check(t, "cancel payment jobs", got, tc.want)
		})
	}
}
