package stripe

import (
	"context"
	"slices"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Payment method types a customer may allow (lago
// PaymentProviderCustomers::StripeCustomer).
const (
	MethodCard            = "card"
	MethodSEPADebit       = "sepa_debit"
	MethodUSBankAccount   = "us_bank_account"
	MethodBACSDebit       = "bacs_debit"
	MethodLink            = "link"
	MethodBoleto          = "boleto"
	MethodCrypto          = "crypto"
	MethodCustomerBalance = "customer_balance"
)

// MethodsWithSetup can be saved through a setup page (lago
// StripeCustomer::PAYMENT_METHODS_WITH_SETUP).
var MethodsWithSetup = []string{MethodCard, MethodSEPADebit, MethodUSBankAccount, MethodBACSDebit, MethodLink, MethodBoleto}

// MethodsWithoutSetup are charged without a saved method (lago
// StripeCustomer::PAYMENT_METHODS_WITHOUT_SETUP).
var MethodsWithoutSetup = []string{MethodCrypto, MethodCustomerBalance}

// CodeCustomerBalanceNotExclusive: customer_balance is combined with
// other types; lago reports it with a message instead of a code
// (stripe_customer.rb customer_balance_must_be_exclusive).
const CodeCustomerBalanceNotExclusive = "customer_balance_must_be_exclusive"

// Metadata keys written on Stripe objects and read back from events.
// Lago writes lago_* keys (payments/create_service.rb enriched_metadata);
// Tanvo needs no compatibility with them and owns its namespace.
const (
	metaCustomerID      = "tanvo_customer_id"
	metaExternalID      = "customer_id" // lago keeps the external ID under this key too
	metaPayableType     = "tanvo_payable_type"
	metaPayableID       = "tanvo_payable_id"
	metaInvoiceID       = "tanvo_invoice_id"
	metaPaymentID       = "tanvo_payment_id"
	metaOrganizationID  = "tanvo_organization_id"
	metaBillingEntityID = "tanvo_billing_entity_id"
	metaCreditNoteID    = "tanvo_credit_note_id"
	metaInvoiceNumber   = "tanvo_invoice_number"
	metaInvoiceType     = "invoice_type"
	metaIssuingDate     = "invoice_issuing_date"
	metaPaymentType     = "payment_type"
	paymentTypeOneTime  = "one-time"
)

// DefaultMethodTypes implements [payment.CustomerGateway]: card (lago
// stripe_customer.rb default provider_payment_methods).
func (g *Gateway) DefaultMethodTypes() []string { return []string{MethodCard} }

// ValidateMethodTypes implements [payment.CustomerGateway] with lago's
// three rules (stripe_customer.rb): known types only, link only with card
// ([core.CodeInvalid] each), customer_balance alone.
func (g *Gateway) ValidateMethodTypes(types []string) error {
	const field = "provider_payment_methods"
	var v core.Validator
	if !v.Check(len(types) > 0, field, core.CodeRequired) {
		return v.Err()
	}
	v.Check(!slices.ContainsFunc(types, func(t string) bool {
		return !slices.Contains(MethodsWithSetup, t) && !slices.Contains(MethodsWithoutSetup, t)
	}), field, core.CodeInvalid)
	v.Check(!slices.Contains(types, MethodLink) || slices.Contains(types, MethodCard), field, core.CodeInvalid)
	v.Check(!slices.Contains(types, MethodCustomerBalance) || customerBalanceOnly(types), field, CodeCustomerBalanceNotExclusive)
	return v.Err()
}

// customerBalanceOnly reports a customer paying only by bank transfer to
// its Stripe balance.
func customerBalanceOnly(types []string) bool {
	return len(types) == 1 && types[0] == MethodCustomerBalance
}

// setupMethods returns the types that can be saved through a setup page.
func setupMethods(types []string) []string {
	var out []string
	for _, t := range types {
		if slices.Contains(MethodsWithSetup, t) {
			out = append(out, t)
		}
	}
	return out
}

// CreateCustomer implements [payment.CustomerGateway] (lago
// payment_provider_customers/stripe_service.rb create). When the key was
// used with other parameters (an IdempotencyError) the single Stripe
// customer with the email is adopted; zero or several are an error.
func (g *Gateway) CreateCustomer(ctx context.Context, conn payment.Connection, p payment.CustomerParams) (string, error) {
	var id string
	err := g.do(ctx, conn, "create_customer", func(ctx context.Context, c call) error {
		params := &stripe.CustomerCreateParams{
			Address: addressParams(p.Address, false),
			Email:   optional(p.Email),
			Name:    optional(p.Name),
			Phone:   optional(p.Phone),
			Metadata: map[string]string{
				metaCustomerID: p.CustomerID.String(),
				metaExternalID: p.ExternalID,
			},
		}
		if p.IdempotencyKey != "" {
			params.SetIdempotencyKey(p.IdempotencyKey)
		}
		cus, err := c.client.V1Customers.Create(ctx, params)
		if err == nil {
			id = cus.ID
			return nil
		}
		if !isKind(err, payment.KindIdempotency) || p.Email == "" {
			return err
		}
		// Lago lists by email and keeps the only match
		// (stripe_service.rb:161); with no email it would list every
		// customer, which says nothing about this one.
		lp := &stripe.CustomerListParams{Email: stripe.String(p.Email)}
		lp.Limit = stripe.Int64(2)
		lp.Single = true
		var found []string
		for cus, lerr := range c.client.V1Customers.List(ctx, lp) {
			if lerr != nil {
				return lerr
			}
			found = append(found, cus.ID)
		}
		if len(found) != 1 {
			return err
		}
		id = found[0]
		return nil
	})
	return id, err
}

// UpdateCustomer implements [payment.CustomerGateway]: the create payload
// without metadata (lago stripe_service.rb stripe_update_payload).
func (g *Gateway) UpdateCustomer(ctx context.Context, conn payment.Connection, providerCustomerID string, p payment.CustomerParams) error {
	return g.do(ctx, conn, "update_customer", func(ctx context.Context, c call) error {
		// Blank fields are sent empty, which clears them at Stripe, as
		// lago's nil attributes are (stripe-ruby 6.5 util.rb
		// encode_parameters writes nil as "key=").
		params := &stripe.CustomerUpdateParams{
			Address: addressParams(p.Address, true),
			Email:   stripe.String(p.Email),
			Name:    stripe.String(p.Name),
			Phone:   stripe.String(p.Phone),
		}
		_, err := c.client.V1Customers.Update(ctx, providerCustomerID, params)
		return err
	})
}

func addressParams(a core.Address, clear bool) *stripe.AddressParams {
	f := optional
	if clear {
		f = stripe.String
	}
	return &stripe.AddressParams{
		City:       f(a.City),
		Country:    f(a.Country),
		Line1:      f(a.Line1),
		Line2:      f(a.Line2),
		PostalCode: f(a.Zipcode),
		State:      f(a.State),
	}
}

// optional returns nil for "" so the parameter is not sent.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
