package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Jobs of the payments orchestration (libs/billing/payments); apps/server
// runs them with the payment provider adapters, on the payments queue.
const (
	// JobCreateRequestPayment collects a payment request through the
	// customer's payment provider (args [PaymentRequestArgs]; lago
	// PaymentRequests::Payments::CreateJob).
	JobCreateRequestPayment = "payments.create_payment_request_payment"
	// JobSendPaymentRequestEmail emails a payment request to its customer
	// (args [PaymentRequestArgs]; lago PaymentRequestMailer#requested).
	JobSendPaymentRequestEmail = "payments.send_payment_request_email"
	// JobSyncProviderCustomer creates or updates a customer at its payment
	// provider (args [ProviderCustomerArgs]; lago
	// PaymentProviderCustomers::*CreateJob).
	JobSyncProviderCustomer = "payments.sync_provider_customer"
	// JobSetPaymentMethod records the method a succeeded payment was made
	// with, then its receipt (args [PaymentMethodArgs]; lago
	// Payments::SetPaymentMethodAndCreateReceiptJob).
	JobSetPaymentMethod = "payments.set_payment_method"
	// JobExpireCheckout expires a checkout session at the provider (args
	// [CheckoutArgs]; lago PaymentIntents::ExpireJob).
	JobExpireCheckout = "payments.expire_checkout"
	// JobCancelAbandonedPayments enqueues a [JobCancelAbandonedPayment] for
	// the abandoned payments of an organization (args [InstantArgs]; lago
	// Clock::CancelAbandonedPaymentsJob).
	JobCancelAbandonedPayments = "payments.cancel_abandoned_payments"
	// JobCancelAbandonedPayment cancels one abandoned payment at its
	// provider (args [PaymentArgs]; lago Invoices::Payments::CancelAbandonedJob).
	JobCancelAbandonedPayment = "payments.cancel_abandoned_payment"
	// JobRegisterProviderWebhook registers the webhook endpoint of a
	// payment connection at its provider (args [IntegrationArgs]; lago
	// PaymentProviders::Stripe::RegisterWebhookJob).
	JobRegisterProviderWebhook = "payments.register_provider_webhook"
	// JobExpireConnectionCheckouts expires the active checkout links of the
	// customers of a payment connection (args [IntegrationArgs]; lago
	// PaymentProviders::Stripe::ExpirePaymentIntentsJob).
	JobExpireConnectionCheckouts = "payments.expire_connection_checkouts"
	// JobUpdatePaymentReference sets the invoice number on the provider
	// payment of an invoice finalized after it was paid (args
	// [InvoiceArgs]; lago PaymentProviders::UpdatePaymentReferenceJob).
	JobUpdatePaymentReference = "payments.update_payment_reference"
	// JobCancelPayment cancels a pending or processing payment at its
	// provider (args [PaymentArgs]; lago PaymentProviders::CancelPaymentJob).
	JobCancelPayment = "payments.cancel_payment"
	// JobCancelAuthorization releases a pre-authorization hold at its
	// provider (args [AuthorizationArgs]; lago
	// PaymentProviders::CancelPaymentAuthorizationJob).
	JobCancelAuthorization = "payments.cancel_authorization"
)

// PaymentJobKinds returns the kinds of the payments orchestration,
// [JobCreateInvoicePayment] included.
func PaymentJobKinds() []string {
	return []string{
		JobCreateInvoicePayment, JobCreateRequestPayment, JobSendPaymentRequestEmail, JobSyncProviderCustomer,
		JobSetPaymentMethod, JobExpireCheckout, JobCancelAbandonedPayments, JobCancelAbandonedPayment,
		JobRegisterProviderWebhook, JobExpireConnectionCheckouts, JobUpdatePaymentReference, JobCancelPayment,
		JobCancelAuthorization,
	}
}

// PaymentRequestArgs are the args of jobs about one payment request.
type PaymentRequestArgs struct {
	// PaymentRequestID is the request.
	PaymentRequestID core.PaymentRequestID `json:"payment_request_id"`
	// PaymentMethodType and PaymentMethodID override the request's
	// payment method (lago payment_method_params).
	PaymentMethodType core.PaymentMethodType `json:"payment_method_type,omitempty"`
	// PaymentMethodID is the override payment method.
	PaymentMethodID core.PaymentMethodID `json:"payment_method_id,omitzero"`
}

// ProviderCustomerArgs are the args of [JobSyncProviderCustomer].
type ProviderCustomerArgs struct {
	// ProviderCustomerID is the customer's provider connection.
	ProviderCustomerID core.PaymentProviderCustomerID `json:"provider_customer_id"`
	// Update pushes the customer's changes to an existing provider
	// customer instead of creating it.
	Update bool `json:"update,omitempty"`
	// Checkout generates the checkout URL where the customer saves a
	// payment method instead of creating it (lago *CheckoutUrlJob).
	Checkout bool `json:"checkout,omitempty"`
	// FetchMethod records the provider customer's default payment method
	// instead of creating it (lago FetchDefaultPaymentMethodJob).
	FetchMethod bool `json:"fetch_method,omitempty"`
	// Funding gives the customer the invoice section printing its bank
	// transfer funding instructions instead of creating it (lago
	// StripeSyncFundingInstructionsJob).
	Funding bool `json:"funding,omitempty"`
}

// PaymentMethodArgs are the args of [JobSetPaymentMethod].
type PaymentMethodArgs struct {
	// PaymentID is the succeeded payment.
	PaymentID core.PaymentID `json:"payment_id"`
	// ProviderMethodID is the provider's method it was made with.
	ProviderMethodID string `json:"provider_method_id,omitempty"`
}

// CheckoutArgs are the args of [JobExpireCheckout].
type CheckoutArgs struct {
	// InvoiceID is the invoice of the link.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// IntegrationID is the connection the session was made at.
	IntegrationID core.IntegrationID `json:"integration_id"`
	// SessionID is the provider's checkout session.
	SessionID string `json:"session_id"`
}

// PaymentArgs are the args of jobs about one payment.
type PaymentArgs struct {
	// PaymentID is the payment.
	PaymentID core.PaymentID `json:"payment_id"`
}

// AuthorizationArgs are the args of jobs about one pre-authorization.
type AuthorizationArgs struct {
	// IntegrationID is the connection that placed the hold.
	IntegrationID core.IntegrationID `json:"integration_id"`
	// ProviderPaymentID is the provider's payment holding the amount.
	ProviderPaymentID string `json:"provider_payment_id"`
}

// IntegrationArgs are the args of jobs about one connection.
type IntegrationArgs struct {
	// IntegrationID is the connection.
	IntegrationID core.IntegrationID `json:"integration_id"`
}

// requireConsentSetting is the payment connection setting requiring the
// terms of service consent on checkout pages (lago
// require_terms_of_service_consent).
const requireConsentSetting = "require_terms_of_service_consent"

// EnqueueUpdatePaymentReference enqueues, in the transaction of r, the
// update of the provider payment reference of the invoice id, finalized
// after its payment succeeded (lago
// subscriptions/activation_rules/payment/resolve_service.rb:48-53).
func EnqueueUpdatePaymentReference(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) error {
	return Enqueue(ctx, r, org, JobUpdatePaymentReference, InvoiceArgs{InvoiceID: id}, "invoice:"+id.String()+":payment-reference", time.Time{})
}

// EnqueueCancelPayment enqueues, in the transaction of r, the cancellation
// at its provider of the payment id, whose payable no longer needs it (lago
// subscriptions/activation_rules/cancel_service.rb:81-88).
func EnqueueCancelPayment(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PaymentID) error {
	return Enqueue(ctx, r, org, JobCancelPayment, PaymentArgs{PaymentID: id}, "payment:"+id.String()+":cancel", time.Time{})
}

// EnqueueCancelAuthorization enqueues, in the transaction of r, the
// release of the pre-authorization args (lago
// payment_providers/stripe/payments/authorize_service.rb:37-42).
func EnqueueCancelAuthorization(ctx context.Context, r port.Repos, org core.OrganizationID, args AuthorizationArgs) error {
	return Enqueue(ctx, r, org, JobCancelAuthorization, args, "authorization:"+args.IntegrationID.String()+":"+args.ProviderPaymentID+":cancel", time.Time{})
}

// EnqueuePaymentConnectionCreated enqueues, in the transaction of r, the
// registration of the webhook endpoint of integ when it is a new payment
// connection (lago payment_providers/stripe_service.rb:37-39); adapters
// without registration ignore the job.
func EnqueuePaymentConnectionCreated(ctx context.Context, r port.Repos, integ *integration.Integration) error {
	if integ.Category != integration.CategoryPayment {
		return nil
	}
	return Enqueue(ctx, r, integ.OrganizationID, JobRegisterProviderWebhook,
		IntegrationArgs{IntegrationID: integ.ID}, "integration:"+integ.ID.String()+":webhook", time.Time{})
}

// EnqueuePaymentConnectionUpdated enqueues, in the transaction of r, the
// expiry of the checkout links of a payment connection whose terms of
// service consent setting changed from prev to integ: the setting shapes
// the checkout pages (lago payment_providers/stripe_service.rb:40-45).
func EnqueuePaymentConnectionUpdated(ctx context.Context, r port.Repos, prev, integ *integration.Integration) error {
	if integ.Category != integration.CategoryPayment {
		return nil
	}
	before, _ := prev.Setting(requireConsentSetting)
	after, _ := integ.Setting(requireConsentSetting)
	if (before == "true") == (after == "true") {
		return nil
	}
	return Enqueue(ctx, r, integ.OrganizationID, JobExpireConnectionCheckouts,
		IntegrationArgs{IntegrationID: integ.ID}, "integration:"+integ.ID.String()+":checkouts", time.Time{})
}

// ConnectionChange is a customer's payment connection as it was before and
// after a create or update (lago PaymentProviders::*::Customers::CreateService
// with ActiveRecord's previous changes).
type ConnectionChange struct {
	// Created reports that the connection was created.
	Created bool
	// PreviousProviderCustomerID is the provider customer ID before the
	// change, "" when none.
	PreviousProviderCustomerID string
	// ProviderCustomerIDSet reports that the change assigned the provider
	// customer ID (params.key?(:provider_customer_id)).
	ProviderCustomerIDSet bool
}

// EnqueueConnectionSync enqueues, in the transaction of r, the provider
// side of a created or updated payment connection pc (lago
// PaymentProviders::*::Customers::CreateService, e.g.
// app/services/payment_providers/stripe/customers/create_service.rb:39-50,
// and PaymentProviderCustomers::UpdateConnectionService):
//
//   - a connection without a provider customer ID whose ID was not just
//     removed and that syncs with the provider has its customer created at
//     the provider;
//   - else an updated connection just given a provider customer ID, not
//     syncing, gets a checkout URL where the customer saves a payment
//     method;
//   - a connection with a provider customer ID, not created at the
//     provider here, whose customer has no payment method records the
//     provider customer's default method;
//   - an update giving a provider customer ID pushes the customer to the
//     provider.
func EnqueueConnectionSync(ctx context.Context, r port.Repos, pc *payment.ProviderCustomer, ch ConnectionChange) error {
	org := pc.OrganizationID
	id := liveProviderCustomerID(pc)
	changed := ch.ProviderCustomerIDSet && id != ch.PreviousProviderCustomerID
	key := "provider_customer:" + pc.ID.String()
	create := id == "" && !changed && pc.SyncWithProvider
	switch {
	case create:
		if err := Enqueue(ctx, r, org, JobSyncProviderCustomer,
			ProviderCustomerArgs{ProviderCustomerID: pc.ID}, key+":create", time.Time{}); err != nil {
			return err
		}
	case !ch.Created && changed && id != "" && !pc.SyncWithProvider:
		if err := Enqueue(ctx, r, org, JobSyncProviderCustomer,
			ProviderCustomerArgs{ProviderCustomerID: pc.ID, Checkout: true}, key+":checkout", time.Time{}); err != nil {
			return err
		}
	}
	if !create && id != "" {
		ms, err := r.PaymentMethods.ListByCustomer(ctx, org, pc.CustomerID)
		if err != nil {
			return fmt.Errorf("list payment methods of customer %s: %w", pc.CustomerID, err)
		}
		if len(ms) == 0 {
			if err := Enqueue(ctx, r, org, JobSyncProviderCustomer,
				ProviderCustomerArgs{ProviderCustomerID: pc.ID, FetchMethod: true}, key+":fetch_method", time.Time{}); err != nil {
				return err
			}
		}
	}
	if !ch.Created && ch.ProviderCustomerIDSet && id != "" {
		return EnqueueCustomerPush(ctx, r, pc)
	}
	return nil
}

// EnqueueCustomerPush enqueues, in the transaction of r, the push of the
// customer's changes to the provider customer pc (lago
// PaymentProviderCustomers::UpdateService); a connection without a
// provider customer ID is skipped.
func EnqueueCustomerPush(ctx context.Context, r port.Repos, pc *payment.ProviderCustomer) error {
	if pc.Deleted() || liveProviderCustomerID(pc) == "" {
		return nil
	}
	return Enqueue(ctx, r, pc.OrganizationID, JobSyncProviderCustomer,
		ProviderCustomerArgs{ProviderCustomerID: pc.ID, Update: true}, "", time.Time{})
}

// liveProviderCustomerID returns the provider's customer ID of pc, or "".
func liveProviderCustomerID(pc *payment.ProviderCustomer) string {
	if pc == nil || pc.ProviderCustomerID == nil {
		return ""
	}
	return *pc.ProviderCustomerID
}
