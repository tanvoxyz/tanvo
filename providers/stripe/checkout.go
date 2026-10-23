package stripe

import (
	"context"
	"slices"
	"strings"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// CreatePaymentURL implements [payment.ChargeGateway]: a Checkout session
// in payment mode with one line per invoice.
//
// For an invoice (lago invoices/payments/stripe_service.rb
// generate_payment_url) the session is keyed by the checkout link, expires
// with it and saves the method for off-session reuse, except for Indian
// customers and customers allowing crypto. For a payment request (lago
// payment_requests/payments/stripe_service.rb) it has no key, no expiry and
// saves nothing. Both mark the payment one-time and ask for terms of
// service consent when the connection requires it.
func (g *Gateway) CreatePaymentURL(ctx context.Context, conn payment.Connection, p payment.PaymentURLParams) (payment.PaymentURL, error) {
	var out payment.PaymentURL
	err := g.do(ctx, conn, "create_payment_url", func(ctx context.Context, c call) error {
		currency := strings.ToLower(p.Currency.Code())
		lines := make([]*stripe.CheckoutSessionCreateLineItemParams, 0, len(p.Lines))
		for _, l := range p.Lines {
			lines = append(lines, &stripe.CheckoutSessionCreateLineItemParams{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency:    stripe.String(currency),
					UnitAmount:  stripe.Int64(l.Amount),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(l.Name)},
				},
			})
		}
		meta := map[string]string{
			metaCustomerID:  p.CustomerID.String(),
			metaPayableType: string(p.Payable.Type),
			metaPayableID:   payableID(p.Payable),
			metaPaymentType: paymentTypeOneTime,
		}
		data := &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Description: optional(p.Description),
			Metadata:    meta,
		}
		params := &stripe.CheckoutSessionCreateParams{
			LineItems:          lines,
			Mode:               stripe.String("payment"),
			SuccessURL:         stripe.String(c.settings.successURL()),
			Customer:           stripe.String(p.ProviderCustomerID),
			PaymentMethodTypes: stripe.StringSlice(p.MethodTypes),
			PaymentIntentData:  data,
		}
		if p.Payable.Type == payment.PayableTypeInvoice {
			meta[metaInvoiceID] = p.Payable.InvoiceID.String()
			if !p.IssuingDate.IsZero() {
				meta[metaIssuingDate] = p.IssuingDate.String()
			}
			if p.InvoiceType != "" {
				meta[metaInvoiceType] = p.InvoiceType
			}
			if !strings.EqualFold(p.CustomerCountry, countryIndia) && !slices.Contains(p.MethodTypes, MethodCrypto) {
				data.SetupFutureUsage = stripe.String("off_session")
			}
			if !p.ExpiresAt.IsZero() {
				params.ExpiresAt = stripe.Int64(p.ExpiresAt.Unix())
			}
			if !p.IntentID.IsZero() {
				params.SetIdempotencyKey("payment-intent:" + p.IntentID.String())
			}
		}
		if c.settings.RequireTermsOfServiceConsent {
			params.ConsentCollection = &stripe.CheckoutSessionCreateConsentCollectionParams{TermsOfService: stripe.String("required")}
		}
		s, err := c.client.V1CheckoutSessions.Create(ctx, params)
		if err != nil {
			return err
		}
		out = payment.PaymentURL{URL: s.URL, SessionID: s.ID}
		return nil
	})
	return out, err
}

// ExpirePaymentURL implements [payment.ChargeGateway] (lago
// invoices/payments/stripe_service.rb expire_payment_url): only an open
// session is expired, and Stripe refusing to expire it is not an error.
func (g *Gateway) ExpirePaymentURL(ctx context.Context, conn payment.Connection, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	return g.do(ctx, conn, "expire_payment_url", func(ctx context.Context, c call) error {
		s, err := c.client.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
		if err != nil {
			if isKind(err, payment.KindInvalidRequest) || isKind(err, payment.KindNotFound) {
				return nil
			}
			return err
		}
		if s.Status != stripe.CheckoutSessionStatusOpen {
			return nil
		}
		_, err = c.client.V1CheckoutSessions.Expire(ctx, sessionID, nil)
		if err != nil && (isKind(err, payment.KindInvalidRequest) || isKind(err, payment.KindNotFound)) {
			return nil
		}
		return err
	})
}
