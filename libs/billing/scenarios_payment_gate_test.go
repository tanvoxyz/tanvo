package billing_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestScenarioPaymentGateResolution resolves the payment rule of a gated
// subscription from its invoice's payment status (lago
// Invoices::UpdateService#handle_payment_gated_activation and
// Subscriptions::ActivationRules::Payment::ResolveService).
func TestScenarioPaymentGateResolution(t *testing.T) {
	setup := func(t *testing.T) (*scenario, *subscription.Subscription, *invoice.Invoice) {
		t.Helper()
		s := newScenario(t, subsAt("2026-03-01T10:00:00Z"))
		c := s.Customer()
		p := s.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = true })
		sub := s.subsCreate(c, p, subsParams{externalID: "gated-sub", billingTime: period.Calendar, paymentRule: subsPtr(48)})
		s.drain()
		subsCheck(t, "status", sub.Status, subscription.StatusIncomplete)
		invs := s.subsInvoices(sub)
		if len(invs) != 1 || invs[0].Status != invoice.StatusOpen {
			t.Fatalf("invoices: got = %v, want = one open", invs)
		}
		return s, sub, invs[0]
	}

	t.Run("a manual payment activates the subscription", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:123
		s, sub, inv := setup(t)
		_, err := payments.New(s.Deps).CreateManual(s.Ctx, payments.ManualInput{
			OrganizationID: s.OrgID(), InvoiceID: inv.ID, Amount: money.New(inv.TotalAmount, -2), Reference: "wire",
		})
		s.must(err)
		s.drain()
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusActive)
		subsCheck(t, "rules", s.subsRules(sub)[0], subscription.ActivationRuleStatusSatisfied)
		got := s.Invoice(inv.ID)
		subsCheck(t, "invoice status", got.Status, invoice.StatusFinalized)
		if got.Number == "" {
			t.Error("invoice number: got = none, want = set")
		}
		// source: app/services/subscriptions/activation_rules/payment/resolve_service.rb:42
		if !containsEvent(s.Webhooks(), webhook.EventTypeInvoiceCreated) {
			t.Errorf("webhooks = %v, want = invoice.created", s.Webhooks())
		}
	})

	t.Run("a failed payment cancels the subscription", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:285
		s, sub, inv := setup(t)
		s.must(s.E.Lifecycle.ResolvePaymentGate(s.Ctx, s.OrgID(), sub.ID, inv.ID, core.PaymentStatusFailed))
		got := s.subsReload(sub.ID)
		subsCheck(t, "status", got.Status, subscription.StatusCanceled)
		subsCheck(t, "cancellation reason", got.CancellationReason, subscription.CancellationReasonPaymentFailed)
		subsCheck(t, "rules", s.subsRules(sub)[0], subscription.ActivationRuleStatusFailed)
		subsCheck(t, "invoice status", s.Invoice(inv.ID).Status, invoice.StatusClosed)
	})

	t.Run("a pending status leaves the gate", func(t *testing.T) {
		s, sub, inv := setup(t)
		s.must(s.E.Lifecycle.ResolvePaymentGate(s.Ctx, s.OrgID(), sub.ID, inv.ID, core.PaymentStatusPending))
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusIncomplete)
		subsCheck(t, "invoice status", s.Invoice(inv.ID).Status, invoice.StatusOpen)
	})

	// The resolution of a subscription no longer incomplete is a no-op
	// (resolve_service.rb:34,50).
	t.Run("resolved twice", func(t *testing.T) {
		s, sub, inv := setup(t)
		s.must(s.E.Lifecycle.ResolvePaymentGate(s.Ctx, s.OrgID(), sub.ID, inv.ID, core.PaymentStatusSucceeded))
		s.must(s.E.Lifecycle.ResolvePaymentGate(s.Ctx, s.OrgID(), sub.ID, inv.ID, core.PaymentStatusFailed))
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusActive)
		subsCheck(t, "invoice status", s.Invoice(inv.ID).Status, invoice.StatusFinalized)
	})
}

// containsEvent reports whether the webhooks include e.
func containsEvent(hooks []webhook.EventType, e webhook.EventType) bool {
	for _, h := range hooks {
		if h == e {
			return true
		}
	}
	return false
}
