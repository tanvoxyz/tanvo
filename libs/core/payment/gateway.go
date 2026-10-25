package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// The interfaces below are the payment provider port (architecture §12):
// every provider adapter in providers/* implements [Gateway], and apps/server
// wires one per [integration.Provider]. They speak only core types; each
// adapter owns its settings and secret keys, its wire formats and its error
// mapping ([ProviderError]).

// Connection is a payment provider connection as an adapter sees it: the
// [integration.Integration] with its secrets decrypted by apps/server.
type Connection struct {
	// ID is the integration.
	ID core.IntegrationID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is the integration code, unique per organization.
	Code string
	// Provider is the vendor.
	Provider integration.Provider
	// Settings is the integration's non-secret settings object.
	Settings json.RawMessage
	// Secrets is the decrypted secrets object; adapters never log it.
	Secrets json.RawMessage
	// WebhookURL is the connection's inbound webhook endpoint, the URL
	// apps/server serves its deliveries at (the endpointURL of
	// [ConnectionGateway.RegisterWebhook]). Providers without registered
	// endpoints take it with every payment (MoneyHash webhook_url); ""
	// sends none.
	WebhookURL string
}

// ConnectionUpdate is what an adapter asks apps/server to store on the
// integration after registering its webhook endpoint: replacement settings
// and secrets objects (nil keeps the stored one).
type ConnectionUpdate struct {
	// Settings replaces the settings object when not nil.
	Settings json.RawMessage
	// Secrets replaces the secrets object when not nil.
	Secrets json.RawMessage
}

// ConnectionGateway manages the connection itself.
type ConnectionGateway interface {
	// Provider returns the vendor the adapter serves.
	Provider() integration.Provider
	// ValidateConnection checks the settings and secrets of a connection
	// without calling the provider; field errors are [core.ValidationError].
	ValidateConnection(conn Connection) error
	// RegisterWebhook creates the provider's webhook endpoint delivering to
	// endpointURL and returns the settings and secrets to store (endpoint
	// ID, signing secret).
	RegisterWebhook(ctx context.Context, conn Connection, endpointURL string) (ConnectionUpdate, error)
	// RefreshWebhook updates the registered endpoint's URL and subscribed
	// events.
	RefreshWebhook(ctx context.Context, conn Connection, endpointURL string) error
}

// CustomerParams describe a customer to create or update at a provider.
type CustomerParams struct {
	// CustomerID is the Tanvo customer, sent as metadata.
	CustomerID core.CustomerID
	// ExternalID is the customer's external ID, sent as metadata.
	ExternalID string
	// Name is the display name, or "<first> <last>" when it is blank.
	Name string
	// CompanyName is the customer's own name, "" when it is blank (no
	// fallback), for providers that take it apart from the person's names
	// (GoCardless company_name).
	CompanyName string
	// Firstname is the customer's first name.
	Firstname string
	// Lastname is the customer's last name.
	Lastname string
	// Email is the first address of the customer's comma-separated list.
	Email string
	// Phone is the contact phone.
	Phone string
	// Address is the billing address.
	Address core.Address
	// Type is the customer type, "" when unset.
	Type customer.Type
	// LegalName, LegalNumber and TaxIdentificationNumber are the
	// customer's legal identity, "" when blank (MoneyHash company_name and
	// tax_id).
	LegalName, LegalNumber, TaxIdentificationNumber string
	// Currency is the customer's currency, zero when unset.
	Currency money.Currency
	// IdempotencyKey makes creation idempotent; see
	// [CustomerIdempotencyKey].
	IdempotencyKey string
}

// CustomerParamsOf returns the provider customer parameters of c.
func CustomerParamsOf(c *customer.Customer) CustomerParams {
	p := CustomerParams{
		CustomerID:     c.ID,
		ExternalID:     c.ExternalID,
		Name:           deref(c.Name),
		CompanyName:    strings.TrimSpace(deref(c.Name)),
		Firstname:      strings.TrimSpace(deref(c.Firstname)),
		Lastname:       strings.TrimSpace(deref(c.Lastname)),
		Phone:          deref(c.Phone),
		Address:        c.BillingAddress,
		Type:           c.CustomerType,
		LegalName:      strings.TrimSpace(deref(c.LegalName)),
		LegalNumber:    strings.TrimSpace(deref(c.LegalNumber)),
		Currency:       c.Currency,
		IdempotencyKey: CustomerIdempotencyKey(c.ID, c.UpdatedAt),

		TaxIdentificationNumber: strings.TrimSpace(deref(c.TaxIdentificationNumber)),
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = strings.TrimSpace(deref(c.Firstname) + " " + deref(c.Lastname))
	}
	if e := deref(c.Email); e != "" {
		p.Email = strings.TrimSpace(strings.SplitN(e, ",", 2)[0])
	}
	return p
}

// CustomerIdempotencyKey returns the creation key of a provider customer:
// "customer:<id>:<updated_at unix>", so retries of one sync share a key and
// a later edit of the customer gets a new one (lago keys it
// "<id>-<updated_at.to_i>", payment_provider_customers/stripe_service.rb:157).
func CustomerIdempotencyKey(id core.CustomerID, updatedAt time.Time) string {
	return "customer:" + id.String() + ":" + strconv.FormatInt(updatedAt.Unix(), 10)
}

// CustomerGateway manages customers at the provider. Removing a provider
// customer is local ([ProviderCustomer.Remove]): like lago
// (payment_provider_customers/destroy_service), the provider's record is
// kept.
//
// A provider that keeps no customer object (Adyen identifies the shopper by
// the customer's external ID once a method is saved; Cashfree and
// Flutterwave only take payment links) returns [ErrUnsupported] from
// CreateCustomer and UpdateCustomer: the provider customer stays local, and
// the caller sends the customer to [MethodGateway.SetupURL] when the
// provider supports one.
type CustomerGateway interface {
	// DefaultMethodTypes returns the method types a new provider customer
	// allows when none are given.
	DefaultMethodTypes() []string
	// ValidateMethodTypes checks a provider customer's allowed method types
	// ([ProviderCustomer.ProviderPaymentMethods]); field errors are
	// [core.ValidationError] on "provider_payment_methods".
	ValidateMethodTypes(types []string) error
	// CreateCustomer creates the customer and returns the provider's
	// customer ID.
	CreateCustomer(ctx context.Context, conn Connection, p CustomerParams) (string, error)
	// UpdateCustomer updates an existing provider customer.
	UpdateCustomer(ctx context.Context, conn Connection, providerCustomerID string, p CustomerParams) error
}

// SyncAction is what syncing a provider customer requires.
type SyncAction int

// SyncAction values.
const (
	// SyncNone: nothing to do.
	SyncNone SyncAction = iota
	// SyncCreate: create the customer at the provider.
	SyncCreate
	// SyncUpdate: push the customer's changes to the provider.
	SyncUpdate
)

// SyncActionOf returns how to sync c after its customer changed: create it
// when it has no provider ID and syncs, update it when it has one, nothing
// once removed (lago payment_provider_customers/create_service.rb and
// customers/update_service).
func SyncActionOf(c *ProviderCustomer) SyncAction {
	switch {
	case c.Deleted():
		return SyncNone
	case c.ProviderCustomerID != nil && *c.ProviderCustomerID != "":
		return SyncUpdate
	case c.SyncWithProvider:
		return SyncCreate
	default:
		return SyncNone
	}
}

// Remove soft-deletes the provider customer at now; the provider's record
// is kept.
func (c *ProviderCustomer) Remove(now time.Time) error {
	if c.Deleted() {
		return &core.TransitionError{Entity: "payment_provider_customer", From: "deleted", To: "deleted"}
	}
	c.DeletedAt = &now
	c.UpdatedAt = now
	return nil
}

// ProviderMethod is a payment method as the provider reports it.
type ProviderMethod struct {
	// ID is the provider's method ID.
	ID string
	// ProviderCustomerID is the provider customer it is attached to; ""
	// when detached.
	ProviderCustomerID string
	// Details describe it.
	Details MethodDetails
	// CreatedAt is when the provider created it.
	CreatedAt time.Time
}

// SetupParams describe a hosted page where a customer saves a payment
// method.
type SetupParams struct {
	// CustomerID is the Tanvo customer.
	CustomerID core.CustomerID
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// MethodTypes are the customer's allowed method types.
	MethodTypes []string
	// Customer describes the customer, for providers whose setup page
	// carries its details (Adyen's shopper reference and email).
	Customer CustomerParams
	// Currency is the currency of a zero-amount authorization page: the
	// customer's currency, else the organization's default (Adyen).
	Currency money.Currency
}

// MethodGateway manages a customer's payment methods at the provider.
type MethodGateway interface {
	// ListPaymentMethods lists the methods attached to a provider customer,
	// newest first.
	ListPaymentMethods(ctx context.Context, conn Connection, providerCustomerID string) ([]ProviderMethod, error)
	// DefaultPaymentMethod returns the provider customer's default method,
	// else its newest card; "" when it has none.
	DefaultPaymentMethod(ctx context.Context, conn Connection, providerCustomerID string) (string, error)
	// PaymentMethod returns one method; a missing one is a
	// [ProviderError] of [KindNotFound].
	PaymentMethod(ctx context.Context, conn Connection, providerMethodID string) (ProviderMethod, error)
	// SetDefaultPaymentMethod makes a method the provider customer's
	// default.
	SetDefaultPaymentMethod(ctx context.Context, conn Connection, providerCustomerID, providerMethodID string) error
	// DetachPaymentMethod detaches a method from its customer at the
	// provider.
	DetachPaymentMethod(ctx context.Context, conn Connection, providerMethodID string) error
	// SetupURL returns the URL of a hosted page saving a payment method;
	// [ErrNoSetupMethods] when none of the allowed types can be saved.
	SetupURL(ctx context.Context, conn Connection, p SetupParams) (string, error)
}

// PaymentParams describe a charge of a payable: an invoice (including a
// wallet credit invoice) or a payment request.
type PaymentParams struct {
	// PaymentID is the Tanvo payment recording the attempt.
	PaymentID core.PaymentID
	// Payable is the settled document.
	Payable Payable
	// InvoiceType is the invoice's type ("credit" for a wallet top-up);
	// metadata only.
	InvoiceType string
	// IssuingDate is the invoice's issuing date; metadata only.
	IssuingDate core.Date
	// CustomerID is the paying customer.
	CustomerID core.CustomerID
	// BillingEntityID is the issuing billing entity.
	BillingEntityID core.BillingEntityID
	// ProviderCustomerID is the provider customer charged.
	ProviderCustomerID string
	// ProviderMethodID is the method to charge; "" lets the adapter use the
	// customer's default.
	ProviderMethodID string
	// MethodTypes are the customer's allowed method types.
	MethodTypes []string
	// Amount is the amount to collect, in minor units of Currency.
	Amount int64
	// Currency is the payable's currency.
	Currency money.Currency
	// Description is shown by the provider (the invoice number).
	Description string
	// CustomerCountry is the customer's billing country.
	CustomerCountry string
	// BillingEntityCountry is the billing entity's country, the fallback of
	// bank-transfer funding.
	BillingEntityCountry string
	// OnSession lets the customer authenticate the charge (a retry after
	// [KindAuthenticationRequired]).
	OnSession bool
	// ActivatesSubscription reports a charge that activates a
	// payment-gated subscription.
	ActivatesSubscription bool
	// Customer describes the paying customer, for providers whose charges
	// carry its details (Adyen's shopper email, Moneyhash's billing data).
	Customer CustomerParams
}

// IdempotencyKey returns the charge's provider idempotency key,
// "payment:<payable>:<payment ID>": derived from the payable and the
// attempt, so retries of one attempt never charge twice while a new
// attempt after a failure gets a new key (lago keys it "payment-<payment
// id>", payment_providers/stripe/payments/create_service.rb:129).
func (p PaymentParams) IdempotencyKey() string {
	return "payment:" + p.Payable.String() + ":" + p.PaymentID.String()
}

// Validate checks the parameters.
func (p PaymentParams) Validate() error {
	var v core.Validator
	v.Check(!p.PaymentID.IsZero(), "payment_id", core.CodeRequired)
	v.Merge("", p.Payable.Validate())
	v.Required("provider_customer_id", p.ProviderCustomerID)
	v.Positive("amount", p.Amount)
	v.Currency("currency", p.Currency)
	return v.Err()
}

// PaymentResult is the provider's view of a payment.
type PaymentResult struct {
	// ProviderPaymentID is the provider's payment ID.
	ProviderPaymentID string
	// ProviderStatus is the raw provider status.
	ProviderStatus string
	// Status maps ProviderStatus to a payment state.
	Status State
	// RequiresAction reports that the customer must act (3-D Secure, bank
	// redirect) before the payment proceeds; Status is then processing.
	RequiresAction bool
	// NextActionURL is where the customer acts, when RequiresAction.
	NextActionURL string
	// NextAction is the provider's raw next-action object, stored as the
	// payment's provider data.
	NextAction json.RawMessage
	// ProviderMethodID is the method charged.
	ProviderMethodID string
	// MethodDetails describe the method charged, when the provider
	// returned it.
	MethodDetails MethodDetails
	// StaleMethodID is the requested [PaymentParams.ProviderMethodID] when
	// the provider no longer has it; the caller deletes the customer's
	// method (lago check_payment_method_service.rb).
	StaleMethodID string
	// Amount is the amount, in minor units.
	Amount int64
	// AmountCapturable is the amount an authorization holds, in minor
	// units; zero for a payment that is not an authorization.
	AmountCapturable int64
	// Currency is the currency.
	Currency money.Currency
}

// AuthorizeParams describe a pre-authorization of a payment-gated
// subscription's first charge.
type AuthorizeParams struct {
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// ProviderMethodID is the method to authorize; "" uses the provider
	// customer's default, else its newest card.
	ProviderMethodID string
	// Amount is the amount, in minor units of Currency.
	Amount int64
	// Currency is the currency.
	Currency money.Currency
	// IdempotencyKey identifies the authorization attempt.
	IdempotencyKey string
	// Metadata is attached to the authorization.
	Metadata map[string]string
}

// PaymentURLParams describe a hosted checkout page paying a payable.
type PaymentURLParams struct {
	// IntentID is the checkout link, the idempotency key of an invoice's
	// page; zero for a payment request.
	IntentID IntentID
	// Payable is the paid document.
	Payable Payable
	// InvoiceType is the invoice's type; metadata only.
	InvoiceType string
	// IssuingDate is the invoice's issuing date; metadata only.
	IssuingDate core.Date
	// CustomerID is the paying customer.
	CustomerID core.CustomerID
	// ProviderCustomerID is the provider customer.
	ProviderCustomerID string
	// MethodTypes are the customer's allowed method types.
	MethodTypes []string
	// CustomerCountry is the customer's billing country.
	CustomerCountry string
	// Description is the page's payment description.
	Description string
	// Lines are the items shown, one per invoice.
	Lines []PaymentURLLine
	// Currency is the currency of every line.
	Currency money.Currency
	// ExpiresAt is when the page stops being valid; zero for the
	// provider's default.
	ExpiresAt time.Time
	// Customer describes the paying customer, for providers whose pages
	// carry its details (name, email, phone, external ID).
	Customer CustomerParams
	// OrganizationName is the billing organization's name, for providers
	// whose pages show the merchant (Flutterwave's page title).
	OrganizationName string
	// OrganizationLogoURL is the billing organization's logo, for
	// providers whose pages show the merchant; empty when it has none.
	OrganizationLogoURL string
}

// PaymentURLLine is one item of a hosted checkout page.
type PaymentURLLine struct {
	// InvoiceID is the invoice; providers that list a payment request's
	// invoices in their metadata read it.
	InvoiceID core.InvoiceID
	// Name is the invoice number.
	Name string
	// Amount is the amount due, in minor units.
	Amount int64
}

// PaymentURL is a hosted checkout page.
type PaymentURL struct {
	// URL is the page.
	URL string
	// SessionID is the provider's checkout session, kept to expire it.
	SessionID string
}

// ChargeGateway charges payables.
type ChargeGateway interface {
	// CreatePayment charges a payable with [PaymentParams.IdempotencyKey].
	CreatePayment(ctx context.Context, conn Connection, p PaymentParams) (PaymentResult, error)
	// GetPayment returns a payment's current state.
	GetPayment(ctx context.Context, conn Connection, providerPaymentID string) (PaymentResult, error)
	// CancelPayment cancels an uncaptured or unconfirmed payment and
	// returns its new state; one in a final state is left as is and the
	// result is zero. A provider that cancels asynchronously (Adyen) also
	// returns a zero result: the new state arrives with the cancellation
	// webhook.
	CancelPayment(ctx context.Context, conn Connection, providerPaymentID string) (PaymentResult, error)
	// UpdatePaymentReference sets a payment's description to the invoice
	// number once known; best effort.
	UpdatePaymentReference(ctx context.Context, conn Connection, providerPaymentID, invoiceNumber string) error
	// Authorize places a hold of the amount, released with CancelPayment;
	// [ErrNoPaymentMethod] when the customer has no method.
	Authorize(ctx context.Context, conn Connection, p AuthorizeParams) (PaymentResult, error)
	// CreatePaymentURL creates a hosted checkout page.
	CreatePaymentURL(ctx context.Context, conn Connection, p PaymentURLParams) (PaymentURL, error)
	// ExpirePaymentURL expires an open checkout page; one already completed
	// or expired is left as is.
	ExpirePaymentURL(ctx context.Context, conn Connection, sessionID string) error
}

// RefundParams describe the refund of a credit note.
type RefundParams struct {
	// CreditNoteID is the refunded credit note and the mandatory
	// idempotency key: one credit note is refunded at most once however
	// often the job runs.
	CreditNoteID core.CreditNoteID
	// InvoiceID is the credited invoice; metadata only.
	InvoiceID core.InvoiceID
	// CustomerID is the customer; metadata only.
	CustomerID core.CustomerID
	// ProviderPaymentID is the refunded provider payment.
	ProviderPaymentID string
	// Amount is the refund amount, in minor units of Currency.
	Amount int64
	// Currency is the payment's currency.
	Currency money.Currency
	// Reason is the credit note's reason, mapped to the provider's.
	Reason creditnote.Reason
}

// IdempotencyKey returns the credit note ID, the refund's key.
func (p RefundParams) IdempotencyKey() string { return p.CreditNoteID.String() }

// Validate checks the parameters; the credit note is mandatory.
func (p RefundParams) Validate() error {
	var v core.Validator
	v.Check(!p.CreditNoteID.IsZero(), "credit_note_id", core.CodeRequired)
	v.Required("provider_payment_id", p.ProviderPaymentID)
	v.Positive("amount", p.Amount)
	v.Currency("currency", p.Currency)
	return v.Err()
}

// RefundResult is the provider's view of a refund.
type RefundResult struct {
	// ProviderRefundID is the provider's refund ID.
	ProviderRefundID string
	// ProviderStatus is the raw provider status.
	ProviderStatus string
	// Status maps ProviderStatus.
	Status core.RefundStatus
	// Amount is the refunded amount, in minor units.
	Amount int64
	// Currency is the currency.
	Currency money.Currency
}

// RefundGateway refunds credit notes. A refusal is a [ProviderError] that
// matches [ErrRejected]. [ErrUnknownStatus] comes with a result whose
// refund exists at the provider but whose status is unmapped: the caller
// records the provider refund ID, keeps the refund status unchanged and
// waits for a status webhook. Any other error leaves the refund to be
// retried with the same key.
type RefundGateway interface {
	// Refund refunds a credit note with [RefundParams.IdempotencyKey].
	Refund(ctx context.Context, conn Connection, p RefundParams) (RefundResult, error)
}

// WebhookGateway authenticates and decodes inbound provider webhooks
// (domain §15.6).
type WebhookGateway interface {
	// VerifyWebhook checks the signature of a delivery against the
	// connection's signing secret; a bad one is [ErrInvalidSignature].
	VerifyWebhook(conn Connection, payload []byte, header http.Header) error
	// ParseWebhook decodes a verified delivery into provider events; event
	// types the adapter does not handle yield none. A malformed payload is
	// [ErrInvalidPayload]; an event that cannot be applied safely is
	// [ErrUnknownPayable] or [ErrUnknownStatus], which the handler
	// acknowledges with a logged warning without updating any record.
	ParseWebhook(ctx context.Context, conn Connection, payload []byte) ([]Event, error)
}

// FundingGateway is implemented by providers that collect through bank
// transfers to a virtual account (Stripe's customer balance).
type FundingGateway interface {
	// FundingInstructions returns the bank details a customer transfers
	// currency to, as the provider's raw object; transfers that need a
	// country use the customer's, else the billing entity's
	// ([ErrMissingCountry]).
	FundingInstructions(ctx context.Context, conn Connection, providerCustomerID string, currency money.Currency, customerCountry, billingEntityCountry string) (json.RawMessage, error)
}

// WebhookAcknowledger is implemented by providers that expect a specific
// answer to an accepted webhook delivery (Adyen requires the body
// "[accepted]", lago webhooks_controller.rb adyen); other providers are
// answered with an empty 200.
type WebhookAcknowledger interface {
	// WebhookAcknowledgement returns the content type and body answering
	// an accepted delivery.
	WebhookAcknowledgement() (contentType string, body []byte)
}

// Gateway is a payment provider adapter: every capability of the port.
// Optional capabilities ([FundingGateway], [WebhookAcknowledger]) are discovered with a type
// assertion; an operation a provider lacks returns [ErrUnsupported].
type Gateway interface {
	ConnectionGateway
	CustomerGateway
	MethodGateway
	ChargeGateway
	RefundGateway
	WebhookGateway
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
