package moneyhash

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Custom field keys the adapter attaches to MoneyHash customers, intents
// and card tokens and reads back from events (lago's lago_* keys,
// renamed; moneyhash_customer.rb mh_custom_fields).
const (
	fieldConnectionID     = "tanvo_mh_connection_id"
	fieldConnectionCode   = "tanvo_mh_connection_code"
	fieldCustomerID       = "tanvo_customer_id"
	fieldExternalID       = "tanvo_customer_external_id"
	fieldCustomerName     = "tanvo_customer_name"
	fieldCurrency         = "tanvo_customer_currency"
	fieldLegalName        = "tanvo_customer_legal_name"
	fieldLegalNumber      = "tanvo_customer_legal_number"
	fieldTaxID            = "tanvo_customer_tax_identification_number"
	fieldProviderCustomer = "tanvo_customer_provider_customer_id"
	fieldOrganizationID   = "tanvo_organization_id"
	fieldPayableID        = "tanvo_payable_id"
	fieldPayableType      = "tanvo_payable_type"
	fieldInvoiceType      = "tanvo_payable_invoice_type"
	fieldMIT              = "tanvo_mit"
	fieldRequest          = "tanvo_request"
)

// Requests named in the tanvo_request custom field (lago lago_request).
const (
	requestCreateCustomer = "create_moneyhash_customer"
	requestCheckoutURL    = "generate_checkout_url"
	requestInvoicePayment = "invoice_automatic_payment"
	requestRequestPayment = "create_payment_request"
	requestPaymentURL     = "generate_payment_url"
)

// setupAmount is the amount, in major units, of the intent that tokenizes
// a card (lago payment_provider_customers/moneyhash_service.rb
// checkout_url_params amount: 5.0).
const setupAmount = 5

// customFields returns the customer's custom fields (lago
// moneyhash_customer.rb mh_custom_fields; lago_mh_service, a Ruby class
// name, is left out).
func customFields(conn payment.Connection, p payment.CustomerParams, providerCustomerID string) map[string]any {
	return map[string]any{
		fieldConnectionID:     conn.ID.String(),
		fieldConnectionCode:   conn.Code,
		fieldCustomerID:       p.CustomerID.String(),
		fieldExternalID:       p.ExternalID,
		fieldCustomerName:     p.CompanyName,
		fieldCurrency:         p.Currency.Code(),
		fieldLegalName:        p.LegalName,
		fieldLegalNumber:      p.LegalNumber,
		fieldTaxID:            p.TaxIdentificationNumber,
		fieldProviderCustomer: providerCustomerID,
		fieldOrganizationID:   conn.OrganizationID.String(),
	}
}

// billingData returns the customer's billing data, the present fields only
// (lago moneyhash_customer.rb mh_billing_data).
func billingData(p payment.CustomerParams) map[string]string {
	m := map[string]string{}
	for k, v := range map[string]string{
		"name":         p.CompanyName,
		"first_name":   p.Firstname,
		"last_name":    p.Lastname,
		"email":        p.Email,
		"phone_number": p.Phone,
		"address":      p.Address.Line1,
		"address1":     p.Address.Line2,
		"city":         p.Address.City,
		"state":        p.Address.State,
		"country":      p.Address.Country,
		"postal_code":  p.Address.Zipcode,
	} {
		if v = strings.TrimSpace(v); v != "" {
			m[k] = v
		}
	}
	return m
}

// amountOf renders minor units as the decimal major-unit amount MoneyHash
// takes. Lago sends amount_cents.div(100).to_f
// (moneyhash/payments/create_service.rb:43), which drops the cents and
// assumes two decimals; the exact amount in the currency's exponent is
// sent instead.
func amountOf(minor int64, c money.Currency) json.Number {
	return json.Number(c.FromMinor(minor).StringFixed(c.Exponent()))
}

// DefaultMethodTypes implements [payment.CustomerGateway]: MoneyHash's flow
// decides the methods offered.
func (g *Gateway) DefaultMethodTypes() []string { return nil }

// ValidateMethodTypes implements [payment.CustomerGateway]; any list is
// accepted and ignored.
func (g *Gateway) ValidateMethodTypes([]string) error { return nil }

// CreateCustomer implements [payment.CustomerGateway] (lago
// payment_provider_customers/moneyhash_service.rb create_moneyhash_customer).
// The tax ID is sent only when it is a number: lago sends
// tax_identification_number.to_i.to_s, "0" for any ID with letters.
func (g *Gateway) CreateCustomer(ctx context.Context, conn payment.Connection, p payment.CustomerParams) (string, error) {
	body := map[string]any{}
	if p.Type != "" {
		body["type"] = strings.ToUpper(string(p.Type))
	}
	for k, v := range map[string]string{
		"first_name":   p.Firstname,
		"last_name":    p.Lastname,
		"email":        p.Email,
		"phone_number": p.Phone,
		"company_name": p.LegalName,
	} {
		if v != "" {
			body[k] = v
		}
	}
	if n, err := strconv.ParseInt(p.TaxIdentificationNumber, 10, 64); err == nil {
		body["tax_id"] = strconv.FormatInt(n, 10)
	}
	if a := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(p.Address.Line1), strings.TrimSpace(p.Address.Line2)}, " ")); a != "" {
		body["address"] = a
	}
	if n := strings.TrimSpace(p.Name); n != "" {
		body["contact_person_name"] = n
	}
	fields := customFields(conn, p, "")
	fields[fieldRequest] = requestCreateCustomer
	body["custom_fields"] = fields
	var out struct {
		ID string `json:"id"`
	}
	err := g.do(ctx, conn, "create_customer", func(ctx context.Context, c call) error {
		return c.send(ctx, http.MethodPost, pathCustomers, p.IdempotencyKey, body, &out)
	})
	if err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("moneyhash create customer: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no customer id",
		})
	}
	return out.ID, nil
}

// UpdateCustomer implements [payment.CustomerGateway]; MoneyHash customers
// are not updated (lago payment_provider_customers/moneyhash_service.rb
// update).
func (g *Gateway) UpdateCustomer(context.Context, payment.Connection, string, payment.CustomerParams) error {
	return nil
}

// intent is the part of a MoneyHash intent the adapter reads.
type intent struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	EmbedURL          string `json:"embed_url"`
	ActiveTransaction *struct {
		Status string `json:"status"`
	} `json:"active_transaction"`
}

// status returns the intent's status, else its active transaction's, else
// PENDING (lago moneyhash/payments/create_service.rb:24).
func (i intent) status() string {
	switch {
	case i.Status != "":
		return i.Status
	case i.ActiveTransaction != nil && i.ActiveTransaction.Status != "":
		return i.ActiveTransaction.Status
	default:
		return statusPending
	}
}

// createIntent posts an intent and returns it.
func (g *Gateway) createIntent(ctx context.Context, conn payment.Connection, op, key string, body map[string]any) (intent, error) {
	var out intent
	err := g.do(ctx, conn, op, func(ctx context.Context, c call) error {
		body["flow_id"] = c.settings.FlowID
		if conn.WebhookURL != "" {
			body["webhook_url"] = conn.WebhookURL
		}
		return c.send(ctx, http.MethodPost, pathIntents, key, body, &out)
	})
	return out, err
}

// SetupURL implements [payment.MethodGateway]: a customer-initiated intent
// of 5 units of the customer's currency that tokenizes the card for
// unscheduled merchant-initiated payments (lago
// payment_provider_customers/moneyhash_service.rb generate_checkout_url).
// The saved card arrives with the card_token.created webhook. The URL is
// MoneyHash's embed URL as is; lago appends a lago_request marker.
func (g *Gateway) SetupURL(ctx context.Context, conn payment.Connection, p payment.SetupParams) (string, error) {
	var v core.Validator
	v.Currency("currency", p.Currency)
	if err := v.Err(); err != nil {
		return "", fmt.Errorf("moneyhash setup url: %w", err)
	}
	fields := customFields(conn, p.Customer, p.ProviderCustomerID)
	fields[fieldMIT] = false
	fields[fieldRequest] = requestCheckoutURL
	body := map[string]any{
		"amount":             json.Number(money.NewFromInt(setupAmount).StringFixed(p.Currency.Exponent())),
		"amount_currency":    p.Currency.Code(),
		"billing_data":       billingData(p.Customer),
		"merchant_initiated": false,
		"tokenize_card":      true,
		"payment_type":       paymentTypeUnscheduled,
		"recurring_data":     map[string]string{"agreement_id": p.CustomerID.String()},
		"custom_fields":      fields,
	}
	if p.ProviderCustomerID != "" {
		body["customer"] = p.ProviderCustomerID
	}
	in, err := g.createIntent(ctx, conn, "setup_url", "", body)
	if err != nil {
		return "", err
	}
	if in.EmbedURL == "" {
		return "", fmt.Errorf("moneyhash setup url: %w", &payment.ProviderError{
			Provider: string(Provider), Kind: payment.KindPermanent, Message: "no embed url",
		})
	}
	return in.EmbedURL, nil
}

// ListPaymentMethods implements [payment.MethodGateway]. Saved cards are
// known from card token webhooks, as in lago, which never lists them; it
// is [payment.ErrUnsupported].
func (g *Gateway) ListPaymentMethods(context.Context, payment.Connection, string) ([]payment.ProviderMethod, error) {
	return nil, fmt.Errorf("moneyhash list payment methods: %w", payment.ErrUnsupported)
}

// DefaultPaymentMethod implements [payment.MethodGateway]: MoneyHash keeps
// no default card; [payment.ErrUnsupported].
func (g *Gateway) DefaultPaymentMethod(context.Context, payment.Connection, string) (string, error) {
	return "", fmt.Errorf("moneyhash default payment method: %w", payment.ErrUnsupported)
}

// PaymentMethod implements [payment.MethodGateway]; [payment.ErrUnsupported]
// (see ListPaymentMethods).
func (g *Gateway) PaymentMethod(context.Context, payment.Connection, string) (payment.ProviderMethod, error) {
	return payment.ProviderMethod{}, fmt.Errorf("moneyhash payment method: %w", payment.ErrUnsupported)
}

// SetDefaultPaymentMethod implements [payment.MethodGateway]: the default
// card is Tanvo's own record; [payment.ErrUnsupported].
func (g *Gateway) SetDefaultPaymentMethod(context.Context, payment.Connection, string, string) error {
	return fmt.Errorf("moneyhash set default payment method: %w", payment.ErrUnsupported)
}

// DetachPaymentMethod implements [payment.MethodGateway]: lago only
// discards the local record (payment_methods/destroy_service.rb);
// [payment.ErrUnsupported].
func (g *Gateway) DetachPaymentMethod(context.Context, payment.Connection, string) error {
	return fmt.Errorf("moneyhash detach payment method: %w", payment.ErrUnsupported)
}
