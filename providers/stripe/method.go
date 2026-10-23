package stripe

import (
	"cmp"
	"context"
	"slices"
	"time"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// ListPaymentMethods implements [payment.MethodGateway].
func (g *Gateway) ListPaymentMethods(ctx context.Context, conn payment.Connection, providerCustomerID string) ([]payment.ProviderMethod, error) {
	var out []payment.ProviderMethod
	err := g.do(ctx, conn, "list_payment_methods", func(ctx context.Context, c call) error {
		var err error
		out, err = listMethods(ctx, c, providerCustomerID, 0)
		return err
	})
	return out, err
}

// listMethods lists a customer's methods, newest first; limit > 0 reads
// one page of that size.
func listMethods(ctx context.Context, c call, providerCustomerID string, limit int64) ([]payment.ProviderMethod, error) {
	lp := &stripe.CustomerListPaymentMethodsParams{Customer: stripe.String(providerCustomerID)}
	if limit > 0 {
		lp.Limit = stripe.Int64(limit)
		lp.Single = true
	}
	var out []payment.ProviderMethod
	for pm, err := range c.client.V1Customers.ListPaymentMethods(ctx, lp) {
		if err != nil {
			return nil, err
		}
		out = append(out, methodOf(pm))
	}
	slices.SortStableFunc(out, func(a, b payment.ProviderMethod) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

// DefaultPaymentMethod implements [payment.MethodGateway] (lago
// payment_provider_customers/stripe/retrieve_latest_payment_method_service.rb):
// the customer's invoice_settings.default_payment_method, else the newest
// card among the first 10 methods. Like lago it is best effort: errors of
// either lookup yield "".
func (g *Gateway) DefaultPaymentMethod(ctx context.Context, conn payment.Connection, providerCustomerID string) (string, error) {
	var id string
	err := g.do(ctx, conn, "default_payment_method", func(ctx context.Context, c call) error {
		id = defaultMethod(ctx, c, g, providerCustomerID)
		return nil
	})
	return id, err
}

func defaultMethod(ctx context.Context, c call, g *Gateway, providerCustomerID string) string {
	cus, err := c.client.V1Customers.Retrieve(ctx, providerCustomerID, nil)
	if err == nil && cus.InvoiceSettings != nil && cus.InvoiceSettings.DefaultPaymentMethod != nil && cus.InvoiceSettings.DefaultPaymentMethod.ID != "" {
		return cus.InvoiceSettings.DefaultPaymentMethod.ID
	}
	if err != nil {
		g.logger.DebugContext(ctx, "stripe default payment method: customer lookup failed", slogErr(err))
	}
	methods, err := listMethods(ctx, c, providerCustomerID, 10)
	if err != nil {
		g.logger.DebugContext(ctx, "stripe default payment method: list failed", slogErr(err))
		return ""
	}
	var cards []payment.ProviderMethod
	for _, m := range methods {
		if m.Details.Type == MethodCard {
			cards = append(cards, m)
		}
	}
	if len(cards) == 0 {
		return ""
	}
	return slices.MaxFunc(cards, func(a, b payment.ProviderMethod) int { return cmp.Compare(a.CreatedAt.Unix(), b.CreatedAt.Unix()) }).ID
}

// PaymentMethod implements [payment.MethodGateway].
func (g *Gateway) PaymentMethod(ctx context.Context, conn payment.Connection, providerMethodID string) (payment.ProviderMethod, error) {
	var out payment.ProviderMethod
	err := g.do(ctx, conn, "payment_method", func(ctx context.Context, c call) error {
		pm, err := c.client.V1PaymentMethods.Retrieve(ctx, providerMethodID, nil)
		if err != nil {
			return err
		}
		out = methodOf(pm)
		return nil
	})
	return out, err
}

// SetDefaultPaymentMethod implements [payment.MethodGateway] (lago
// setup_intent_succeeded_service.rb update_stripe_customer_default_payment_method).
func (g *Gateway) SetDefaultPaymentMethod(ctx context.Context, conn payment.Connection, providerCustomerID, providerMethodID string) error {
	return g.do(ctx, conn, "set_default_payment_method", func(ctx context.Context, c call) error {
		_, err := c.client.V1Customers.Update(ctx, providerCustomerID, &stripe.CustomerUpdateParams{
			InvoiceSettings: &stripe.CustomerUpdateInvoiceSettingsParams{DefaultPaymentMethod: stripe.String(providerMethodID)},
		})
		return err
	})
}

// DetachPaymentMethod implements [payment.MethodGateway]. Lago never
// detaches: deleting a payment method only discards its row
// (payment_methods/destroy_service.rb), so the card stays chargeable at
// Stripe. Tanvo detaches it so a deleted method cannot be charged; a method
// already detached is not an error.
func (g *Gateway) DetachPaymentMethod(ctx context.Context, conn payment.Connection, providerMethodID string) error {
	return g.do(ctx, conn, "detach_payment_method", func(ctx context.Context, c call) error {
		_, err := c.client.V1PaymentMethods.Detach(ctx, providerMethodID, nil)
		if err != nil && (hasCode(err, codePaymentMethodUnexpected) || isKind(err, payment.KindNotFound)) {
			return nil
		}
		return err
	})
}

// codePaymentMethodUnexpected is Stripe's error detaching a method that is
// not attached.
const codePaymentMethodUnexpected = "payment_method_unexpected_state"

// SetupURL implements [payment.MethodGateway] (lago
// payment_provider_customers/stripe_service.rb generate_checkout_url): a
// Checkout session in setup mode for the types that can be saved, with the
// terms of service consent when the connection requires it.
func (g *Gateway) SetupURL(ctx context.Context, conn payment.Connection, p payment.SetupParams) (string, error) {
	types := setupMethods(p.MethodTypes)
	if len(types) == 0 {
		return "", payment.ErrNoSetupMethods
	}
	var url string
	err := g.do(ctx, conn, "setup_url", func(ctx context.Context, c call) error {
		params := &stripe.CheckoutSessionCreateParams{
			SuccessURL:         stripe.String(c.settings.successURL()),
			Mode:               stripe.String("setup"),
			PaymentMethodTypes: stripe.StringSlice(types),
			Customer:           stripe.String(p.ProviderCustomerID),
		}
		if c.settings.RequireTermsOfServiceConsent {
			params.ConsentCollection = &stripe.CheckoutSessionCreateConsentCollectionParams{TermsOfService: stripe.String("required")}
		}
		s, err := c.client.V1CheckoutSessions.Create(ctx, params)
		if err != nil {
			return err
		}
		url = s.URL
		return nil
	})
	return url, err
}

// methodOf converts a Stripe payment method (lago
// payments/set_payment_method_data_service.rb, card details with
// display_brand).
func methodOf(pm *stripe.PaymentMethod) payment.ProviderMethod {
	m := payment.ProviderMethod{
		ID:        pm.ID,
		Details:   detailsOf(pm),
		CreatedAt: time.Unix(pm.Created, 0).UTC(),
	}
	if pm.Customer != nil {
		m.ProviderCustomerID = pm.Customer.ID
	}
	return m
}

func detailsOf(pm *stripe.PaymentMethod) payment.MethodDetails {
	if pm == nil {
		return payment.MethodDetails{}
	}
	d := payment.MethodDetails{Type: string(pm.Type)}
	if pm.Card != nil {
		d.Brand = pm.Card.DisplayBrand
		d.Last4 = pm.Card.Last4
		d.ExpMonth = int(pm.Card.ExpMonth)
		d.ExpYear = int(pm.Card.ExpYear)
	}
	return d
}
