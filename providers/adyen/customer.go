package adyen

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/adyen/adyen-go-api-library/v21/src/checkout"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

const (
	// setupLinkTTL is how long a zero-amount authorization link stays
	// valid (lago payment_provider_customers/adyen_service.rb:128).
	setupLinkTTL = 69 * 24 * time.Hour

	// methodScheme is Adyen's type of card methods.
	methodScheme = "scheme"
	// methodCard is the Tanvo method type of a card.
	methodCard = "card"

	// recurringModel is the recurring processing model of every saved card
	// and card-on-file charge: merchant-initiated charges of varying amounts.
	recurringModel = "UnscheduledCardOnFile"
	// storeEnabled lets a payment link save the card used.
	storeEnabled = "enabled"
)

// DefaultMethodTypes implements [payment.CustomerGateway]: Adyen
// customers have no method types; lago ignores provider_payment_methods
// for Adyen.
func (g *Gateway) DefaultMethodTypes() []string { return nil }

// ValidateMethodTypes implements [payment.CustomerGateway]: any list is
// accepted and unused, as in lago.
func (g *Gateway) ValidateMethodTypes([]string) error { return nil }

// CreateCustomer implements [payment.CustomerGateway]: Adyen keeps no
// customer object, so it is [payment.ErrUnsupported]. Lago's create makes
// no customer either; it sends the zero-amount authorization link
// ([Gateway.SetupURL], payment_provider_customers/adyen_service.rb:18-33).
func (g *Gateway) CreateCustomer(context.Context, payment.Connection, payment.CustomerParams) (string, error) {
	return "", fmt.Errorf("adyen create customer: %w", payment.ErrUnsupported)
}

// UpdateCustomer implements [payment.CustomerGateway]: unsupported, as
// lago's update does nothing (adyen_service.rb:35-38).
func (g *Gateway) UpdateCustomer(context.Context, payment.Connection, string, payment.CustomerParams) error {
	return fmt.Errorf("adyen update customer: %w", payment.ErrUnsupported)
}

// SetupURL implements [payment.MethodGateway] (lago
// payment_provider_customers/adyen_service.rb generate_checkout_url): a
// payment link authorizing zero in the customer's currency that saves the
// card for the shopper reference, the customer's external ID, valid 69
// days. Its AUTHORISATION notification yields
// [payment.PaymentMethodAttached]. The link also carries the Tanvo
// customer ID as metadata, which lago does not send, so the notification
// names the customer without a lookup by external ID.
func (g *Gateway) SetupURL(ctx context.Context, conn payment.Connection, p payment.SetupParams) (string, error) {
	var v core.Validator
	v.Required("external_id", p.Customer.ExternalID)
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return "", err
	}
	var out string
	err := g.do(ctx, conn, "setup_url", func(ctx context.Context, c call) error {
		meta := map[string]string{}
		if id := customerIDOf(p); !id.IsZero() {
			meta[metaCustomerID] = id.String()
		}
		req := checkout.PaymentLinkRequest{
			Reference:                "authorization customer " + p.Customer.ExternalID,
			Amount:                   checkout.Amount{Currency: p.Currency.Code(), Value: 0},
			MerchantAccount:          c.settings.MerchantAccount,
			ReturnUrl:                ptr(c.settings.successURL()),
			ShopperReference:         ptr(p.Customer.ExternalID),
			StorePaymentMethodMode:   ptr(storeEnabled),
			RecurringProcessingModel: ptr(recurringModel),
			ExpiresAt:                ptr(g.now().UTC().Add(setupLinkTTL).Truncate(time.Second)),
			ShopperEmail:             optional(p.Customer.Email),
			ApplicationInfo:          applicationInfo(),
		}
		if len(meta) > 0 {
			req.Metadata = &meta
		}
		api := c.checkout.PaymentLinksApi
		res, hr, err := api.PaymentLinks(ctx, api.PaymentLinksInput().PaymentLinkRequest(req))
		if err != nil {
			return sdkErr(hr, err)
		}
		out = res.Url
		return nil
	})
	return out, err
}

// customerIDOf returns the setup's customer.
func customerIDOf(p payment.SetupParams) core.CustomerID {
	if !p.CustomerID.IsZero() {
		return p.CustomerID
	}
	return p.Customer.CustomerID
}

// ListPaymentMethods implements [payment.MethodGateway]: the shopper's
// stored methods (/paymentMethods, as lago's charge reads them,
// adyen/payments/create_service.rb:69-79), in Adyen's order.
func (g *Gateway) ListPaymentMethods(ctx context.Context, conn payment.Connection, providerCustomerID string) ([]payment.ProviderMethod, error) {
	var out []payment.ProviderMethod
	err := g.do(ctx, conn, "list_payment_methods", func(ctx context.Context, c call) error {
		var err error
		out, err = storedMethods(ctx, c, providerCustomerID)
		return err
	})
	return out, err
}

// storedMethods lists the shopper's stored methods.
func storedMethods(ctx context.Context, c call, shopperReference string) ([]payment.ProviderMethod, error) {
	api := c.checkout.PaymentsApi
	res, hr, err := api.PaymentMethods(ctx, api.PaymentMethodsInput().PaymentMethodsRequest(checkout.PaymentMethodsRequest{
		MerchantAccount:  c.settings.MerchantAccount,
		ShopperReference: ptr(shopperReference),
	}))
	if err != nil {
		return nil, sdkErr(hr, err)
	}
	out := make([]payment.ProviderMethod, 0, len(res.StoredPaymentMethods))
	for _, m := range res.StoredPaymentMethods {
		if deref(m.Id) == "" {
			continue
		}
		out = append(out, payment.ProviderMethod{
			ID:                 *m.Id,
			ProviderCustomerID: shopperReference,
			Details:            detailsOf(m),
		})
	}
	return out, nil
}

// detailsOf describes a stored method; Adyen's "scheme" is a card.
func detailsOf(m checkout.StoredPaymentMethod) payment.MethodDetails {
	d := payment.MethodDetails{
		Type:     deref(m.Type),
		Brand:    deref(m.Brand),
		Last4:    deref(m.LastFour),
		ExpMonth: atoi(deref(m.ExpiryMonth)),
		ExpYear:  atoi(deref(m.ExpiryYear)),
	}
	if d.Type == methodScheme {
		d.Type = methodCard
	}
	if d.ExpYear > 0 && d.ExpYear < 100 {
		// Adyen reports two-digit years ("30").
		d.ExpYear += 2000
	}
	return d
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// DefaultPaymentMethod implements [payment.MethodGateway]: the first
// stored method, as lago's charge picks it (create_service.rb:74).
func (g *Gateway) DefaultPaymentMethod(ctx context.Context, conn payment.Connection, providerCustomerID string) (string, error) {
	ms, err := g.ListPaymentMethods(ctx, conn, providerCustomerID)
	if err != nil || len(ms) == 0 {
		return "", err
	}
	return ms[0].ID, nil
}

// PaymentMethod implements [payment.MethodGateway]: Adyen has no lookup of
// one stored method by ID without its shopper, so it is
// [payment.ErrUnsupported]; [payment.PaymentMethodAttached] events carry
// what lago stores (the method ID).
func (g *Gateway) PaymentMethod(context.Context, payment.Connection, string) (payment.ProviderMethod, error) {
	return payment.ProviderMethod{}, fmt.Errorf("adyen payment method: %w", payment.ErrUnsupported)
}

// SetDefaultPaymentMethod implements [payment.MethodGateway]: Adyen has no
// default method; the charge names the method, so it is
// [payment.ErrUnsupported] and the default is kept in Tanvo only.
func (g *Gateway) SetDefaultPaymentMethod(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("adyen set default payment method: %w", payment.ErrUnsupported)
}

// DetachPaymentMethod implements [payment.MethodGateway]: unsupported, as
// in lago, which removes Adyen methods locally only.
func (g *Gateway) DetachPaymentMethod(context.Context, payment.Connection, string) error {
	return fmt.Errorf("adyen detach payment method: %w", payment.ErrUnsupported)
}
