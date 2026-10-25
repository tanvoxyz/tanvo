package payments_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// declined is a card declined by Stripe.
var declined = &payment.ProviderError{Provider: "stripe", Kind: payment.KindCardDeclined, Code: "card_declined", Message: "Your card was declined."}

// TestInvoiceFailureWebhooks ports Invoices::Payments::DeliverErrorWebhookService:
// a declined charge of a credit invoice announces the failure of its
// wallet purchase, and a visible invoice its own failure.
func TestInvoiceFailureWebhooks(t *testing.T) {
	tests := []struct {
		name   string
		credit bool
		gated  bool
		status invoice.Status
		want   []webhook.EventType
	}{
		{
			// source: spec/services/invoices/payments/deliver_error_webhook_service_spec.rb:22, 30
			name: "visible invoice", status: invoice.StatusFinalized,
			want: []webhook.EventType{webhook.EventTypeInvoicePaymentFailure},
		},
		{
			// source: spec/services/invoices/payments/deliver_error_webhook_service_spec.rb:40
			name: "invisible credit invoice", credit: true, status: invoice.StatusGenerating,
		},
		{
			// The open invoice activating a payment-gated subscription.
			// source: spec/services/invoices/payments/deliver_error_webhook_service_spec.rb:50
			name: "open subscription invoice", gated: true, status: invoice.StatusOpen,
		},
		{
			// source: spec/services/invoices/payments/deliver_error_webhook_service_spec.rb:68, 75
			name: "open credit invoice", credit: true, status: invoice.StatusOpen,
			want: []webhook.EventType{webhook.EventTypeWalletTransactionPaymentFailure},
		},
		{
			// source: spec/services/invoices/payments/deliver_error_webhook_service_spec.rb:85, 92
			name: "visible credit invoice", credit: true, status: invoice.StatusFinalized,
			want: []webhook.EventType{webhook.EventTypeInvoicePaymentFailure, webhook.EventTypeWalletTransactionPaymentFailure},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tx *wallet.Transaction
			f := newProviderFixture(t, func(inv *invoice.Invoice) {
				inv.Status = tt.status
				if tt.credit {
					inv.Type = invoice.TypeCredit
				}
			})
			if tt.credit {
				w := f.h.Wallet(f.cust, "0")
				tx = f.h.InboundTransaction(w, wallet.TransactionStatusPurchased, "100", func(t *wallet.Transaction) {
					t.Status = wallet.SettlementStatusPending
					t.SettledAt = nil
					t.InvoiceID = f.inv.ID
				})
				f.h.StoredFee(f.inv, 10000, func(fee *invoice.Fee) {
					fee.Type = core.FeeTypeCredit
					fee.WalletTransactionID = tx.ID
				})
			}
			if tt.gated {
				sub := f.h.Subscription(f.cust, f.h.Plan(period.Monthly, 10000), func(s *subscription.Subscription) {
					s.Status = subscription.StatusIncomplete
				})
				f.h.LinkSubscription(f.inv, sub, invoice.InvoicingReasonSubscriptionStarting, now, now.AddDate(0, 1, 0))
				rule := subscription.NewActivationRule(f.h.OrgID(), core.NewID[core.ActivationRuleID](f.h.IDs), sub.ID,
					subscription.ActivationRuleTypePayment, 0, f.h.Now())
				rule.Status = subscription.ActivationRuleStatusPending
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
				f.inv.Type = invoice.TypeSubscription
				f.h.SaveInvoice(f.inv)
			}
			f.gw.chargeErr, f.gw.charge = declined, payment.PaymentResult{}
			if err := f.pay(); err != nil {
				t.Fatalf("pay: %v", err)
			}
			if len(f.gw.charges) != 1 {
				t.Fatalf("charges: got = %d, want = 1", len(f.gw.charges))
			}
			var got []webhook.EventType
			for _, ev := range f.h.Emitted.Events() {
				switch ev.Type {
				case webhook.EventTypeInvoicePaymentFailure, webhook.EventTypeWalletTransactionPaymentFailure:
					got = append(got, ev.Type)
					var payload struct {
						ProviderCustomerID string `json:"provider_customer_id"`
						ProviderError      struct {
							ErrorCode    string `json:"error_code"`
							ErrorMessage string `json:"message"`
						} `json:"provider_error"`
					}
					if err := json.Unmarshal(ev.Payload, &payload); err != nil {
						t.Fatalf("payload: %v", err)
					}
					if payload.ProviderCustomerID != "cus_1" {
						t.Errorf("%s provider customer: got = %q, want = cus_1", ev.Type, payload.ProviderCustomerID)
					}
					if ev.Type == webhook.EventTypeWalletTransactionPaymentFailure && ev.ObjectID != tx.ID.UUID() {
						t.Errorf("wallet transaction: got = %s, want = %s", ev.ObjectID, tx.ID)
					}
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("webhooks: got = %v, want = %v", got, tt.want)
			}
		})
	}
}

// TestCreateInvoicePaymentRequiresAction checks that a charge waiting for
// the customer's action keeps its next action and is announced by
// payment.requires_action.
func TestCreateInvoicePaymentRequiresAction(t *testing.T) {
	// source: spec/services/payment_providers/stripe/payments/create_service_spec.rb:621, 629
	f := newProviderFixture(t)
	next := json.RawMessage(`{"redirect_to_url":{"url":"https://foo.bar"}}`)
	f.gw.charge = payment.PaymentResult{
		ProviderPaymentID: "pi_123456", ProviderStatus: payment.ProviderStatusRequiresAction, Status: payment.StateProcessing,
		RequiresAction: true, NextAction: next,
	}
	if err := f.pay(); err != nil {
		t.Fatalf("pay: %v", err)
	}
	ps := f.payments()
	if len(ps) != 1 {
		t.Fatalf("payments: got = %d, want = 1", len(ps))
	}
	if p := ps[0]; p.Status != payment.StateProcessing || deref(p.ProviderStatus) != payment.ProviderStatusRequiresAction || string(p.ProviderPaymentData) != string(next) {
		t.Errorf("payment: got = %s %s %s, want = processing requires_action %s", p.Status, deref(p.ProviderStatus), p.ProviderPaymentData, next)
	}
	var found bool
	for _, ev := range f.h.Emitted.Events() {
		if ev.Type == webhook.EventTypePaymentRequiresAction {
			found = ev.ObjectID == ps[0].ID.UUID()
		}
	}
	if !found {
		t.Errorf("webhooks: got = %v, want = payment.requires_action of %s", f.h.Webhooks(), ps[0].ID)
	}
}
