package kernel

import "github.com/tanvoxyz/tanvo/libs/core"

// JobResolvePaymentGate resolves the payment activation rule of an
// incomplete subscription after the payment status of its gating invoice
// became succeeded or failed (lago
// Subscriptions::ActivationRules::Payment::ResolveJob, enqueued by
// Invoices::UpdateService#handle_payment_gated_activation,
// app/services/invoices/update_service.rb:119-129); its args are
// [PaymentGateArgs].
const JobResolvePaymentGate = "billing.resolve_payment_gate"

// PaymentGateArgs are the args of [JobResolvePaymentGate].
type PaymentGateArgs struct {
	// SubscriptionID is the incomplete subscription.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// InvoiceID is its gating invoice.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// PaymentStatus is the invoice's new payment status.
	PaymentStatus core.PaymentStatus `json:"payment_status"`
}

// gateJobKinds returns the job kinds of the activation gates.
func gateJobKinds() []string { return []string{JobResolvePaymentGate} }
