package stripe

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// SupportedEUBankTransferCountries are the countries of EU bank transfers
// to a customer balance (lago StripeProvider::SUPPORTED_EU_BANK_TRANSFER_COUNTRIES).
var SupportedEUBankTransferCountries = []string{"BE", "DE", "ES", "FR", "IE", "NL"}

// countryIndia: Reserve Bank of India rules require Indian recurring
// payments to be authenticated on-session (lago create_service.rb
// off_session?, https://docs.stripe.com/india-recurring-payments).
const countryIndia = "IN"

// PaymentIntent statuses grouped by Tanvo payment state (lago
// StripeProvider PROCESSING_STATUSES, SUCCESS_STATUSES, FAILED_STATUSES).
var (
	processingStatuses = []string{"processing", "requires_capture", "requires_action", "requires_confirmation"}
	succeededStatuses  = []string{"succeeded"}
	failedStatuses     = []string{"canceled", "requires_payment_method"}
)

// StatusOf maps a PaymentIntent status to a payment state; an unknown
// status is pending (lago BaseProvider#determine_payment_status).
func StatusOf(providerStatus string) payment.State {
	switch {
	case slices.Contains(processingStatuses, providerStatus):
		return payment.StateProcessing
	case slices.Contains(succeededStatuses, providerStatus):
		return payment.StateSucceeded
	case slices.Contains(failedStatuses, providerStatus):
		return payment.StateFailed
	default:
		return payment.StatePending
	}
}

const statusRequiresAction = "requires_action"

// CreatePayment implements [payment.ChargeGateway] (lago
// payment_providers/stripe/payments/create_service.rb): a confirmed
// PaymentIntent keyed by [payment.PaymentParams.IdempotencyKey].
//
//   - A customer paying only by customer balance is charged by bank
//     transfer to its Stripe balance, with the transfer type of the
//     currency (EU transfers need the customer's or else the billing
//     entity's supported country, [payment.ErrMissingCountry]).
//   - Otherwise the requested method is charged when Stripe still has it
//     for the customer, else the customer's first listed method; a
//     requested method Stripe lost is reported in StaleMethodID.
//   - A customer with no method at all, no default and no default source
//     is charged with the shared payment token granted to its invoice
//     settings, when it has one. Lago gates this behind the
//     stripe_shared_payment_token feature flag; Tanvo has no such flag.
//   - Charges run off-session and fail rather than ask for authentication,
//     except for Indian customers, customer-balance charges and on-session
//     retries ([payment.PaymentParams.OnSession]).
//
// An authentication_required failure is worth an on-session retry
// ([payment.ProviderError.RetryOnSession]) when the connection supports
// 3-D Secure or the charge activates a payment-gated subscription (lago
// StripeProvider#retriable_authentication_failure?).
func (g *Gateway) CreatePayment(ctx context.Context, conn payment.Connection, p payment.PaymentParams) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	if err := p.Validate(); err != nil {
		return res, err
	}
	err := g.do(ctx, conn, "create_payment", func(ctx context.Context, c call) error {
		params := &stripe.PaymentIntentCreateParams{
			Amount:                stripe.Int64(p.Amount),
			Currency:              stripe.String(strings.ToLower(p.Currency.Code())),
			Customer:              stripe.String(p.ProviderCustomerID),
			PaymentMethodTypes:    stripe.StringSlice(p.MethodTypes),
			Confirm:               stripe.Bool(true),
			OffSession:            stripe.Bool(offSession(p)),
			ReturnURL:             stripe.String(c.settings.successURL()),
			ErrorOnRequiresAction: stripe.Bool(!strings.EqualFold(p.CustomerCountry, countryIndia)),
			Description:           optional(p.Description),
			Metadata:              paymentMetadata(conn, p),
		}
		switch {
		case customerBalanceOnly(p.MethodTypes):
			bt, err := bankTransfer(p.Currency.Code(), p.CustomerCountry, p.BillingEntityCountry)
			if err != nil {
				return err
			}
			params.PaymentMethodData = &stripe.PaymentIntentCreatePaymentMethodDataParams{Type: stripe.String(MethodCustomerBalance)}
			params.PaymentMethodOptions = &stripe.PaymentIntentCreatePaymentMethodOptionsParams{
				CustomerBalance: &stripe.PaymentIntentCreatePaymentMethodOptionsCustomerBalanceParams{
					FundingType:  stripe.String("bank_transfer"),
					BankTransfer: bt,
				},
			}
		default:
			method, stale, token, err := chargeMethod(ctx, c, p)
			if err != nil {
				return err
			}
			res.StaleMethodID = stale
			switch {
			case token != "":
				// stripe-go has no typed field for shared payment tokens.
				params.AddExtra("payment_method_data[shared_payment_granted_token]", token)
				params.ReturnURL = nil
				params.OffSession = nil
			case method != "":
				params.PaymentMethod = stripe.String(method)
			}
		}
		if p.OnSession {
			// Lago drops both once a payment of the payable failed with
			// authentication_required, so the customer can authenticate.
			params.OffSession = nil
			params.ErrorOnRequiresAction = nil
		}
		params.SetIdempotencyKey(p.IdempotencyKey())
		pi, err := c.client.V1PaymentIntents.Create(ctx, params)
		if err != nil {
			pe := providerError(classify(err))
			if pe != nil && pe.Kind == payment.KindAuthenticationRequired {
				pe.RetryOnSession = c.settings.Supports3DS || p.ActivatesSubscription
				return pe
			}
			return err
		}
		stale := res.StaleMethodID
		res = resultOf(pi)
		res.StaleMethodID = stale
		return nil
	})
	return res, err
}

// offSession reports an off-session charge (lago create_service.rb
// off_session?).
func offSession(p payment.PaymentParams) bool {
	return !strings.EqualFold(p.CustomerCountry, countryIndia) && !customerBalanceOnly(p.MethodTypes)
}

// paymentMetadata is the PaymentIntent metadata (lago enriched_metadata
// with the callers' invoice or payment request keys).
func paymentMetadata(conn payment.Connection, p payment.PaymentParams) map[string]string {
	m := map[string]string{
		metaPaymentID:      p.PaymentID.String(),
		metaPayableType:    string(p.Payable.Type),
		metaPayableID:      payableID(p.Payable),
		metaCustomerID:     p.CustomerID.String(),
		metaOrganizationID: conn.OrganizationID.String(),
	}
	if !p.BillingEntityID.IsZero() {
		m[metaBillingEntityID] = p.BillingEntityID.String()
	}
	if p.Payable.Type == payment.PayableTypeInvoice {
		m[metaInvoiceID] = p.Payable.InvoiceID.String()
		if p.InvoiceType != "" {
			m[metaInvoiceType] = p.InvoiceType
		}
		if !p.IssuingDate.IsZero() {
			m[metaIssuingDate] = p.IssuingDate.String()
		}
	}
	return m
}

func payableID(p payment.Payable) string {
	if p.Type == payment.PayableTypePaymentRequest {
		return p.PaymentRequestID.String()
	}
	return p.InvoiceID.String()
}

// chargeMethod picks what a charge uses: the requested method if Stripe
// still has it for the customer (lago check_payment_method_service.rb),
// else the first listed method, else the shared payment token (lago
// create_service.rb shared_payment_token).
func chargeMethod(ctx context.Context, c call, p payment.PaymentParams) (method, stale, token string, err error) {
	if p.ProviderMethodID != "" {
		pm, err := c.client.V1Customers.RetrievePaymentMethod(ctx, p.ProviderMethodID, &stripe.CustomerRetrievePaymentMethodParams{
			Customer: stripe.String(p.ProviderCustomerID),
		})
		switch {
		case err == nil:
			return pm.ID, "", "", nil
		case isKind(err, payment.KindInvalidRequest) || isKind(err, payment.KindNotFound):
			stale = p.ProviderMethodID
		default:
			return "", "", "", err
		}
	}
	methods, err := listMethods(ctx, c, p.ProviderCustomerID, 10)
	if err != nil {
		return "", "", "", err
	}
	if len(methods) > 0 {
		return methods[0].ID, stale, "", nil
	}
	cus, err := c.client.V1Customers.Retrieve(ctx, p.ProviderCustomerID, nil)
	if err != nil {
		return "", "", "", err
	}
	return "", stale, sharedPaymentToken(cus), nil
}

// sharedPaymentToken returns the customer's
// invoice_settings.default_shared_payment_token when it has no default
// method or source and is not deleted; stripe-go has no field for it.
func sharedPaymentToken(cus *stripe.Customer) string {
	if cus.Deleted || cus.DefaultSource != nil || (cus.InvoiceSettings != nil && cus.InvoiceSettings.DefaultPaymentMethod != nil) || cus.LastResponse == nil {
		return ""
	}
	var raw struct {
		InvoiceSettings struct {
			Token string `json:"default_shared_payment_token"`
		} `json:"invoice_settings"`
	}
	if json.Unmarshal(cus.LastResponse.RawJSON, &raw) != nil {
		return ""
	}
	return raw.InvoiceSettings.Token
}

// bankTransfer is the customer-balance bank transfer of a currency (lago
// create_service.rb bank_transfer_type); nil for a currency Stripe has no
// transfer type for, which Stripe then rejects.
func bankTransfer(currency, customerCountry, entityCountry string) (*stripe.PaymentIntentCreatePaymentMethodOptionsCustomerBalanceBankTransferParams, error) {
	t, country, err := transferType(currency, customerCountry, entityCountry)
	if err != nil || t == "" {
		return nil, err
	}
	bt := &stripe.PaymentIntentCreatePaymentMethodOptionsCustomerBalanceBankTransferParams{Type: stripe.String(t)}
	if country != "" {
		bt.EUBankTransfer = &stripe.PaymentIntentCreatePaymentMethodOptionsCustomerBalanceBankTransferEUBankTransferParams{Country: stripe.String(country)}
	}
	return bt, nil
}

// transferType returns the bank transfer type of a currency and, for EUR,
// the supported country of the customer or else of the billing entity.
func transferType(currency, customerCountry, entityCountry string) (typ, country string, err error) {
	switch strings.ToLower(currency) {
	case "eur":
		for _, c := range []string{customerCountry, entityCountry} {
			if c = strings.ToUpper(c); slices.Contains(SupportedEUBankTransferCountries, c) {
				return "eu_bank_transfer", c, nil
			}
		}
		return "", "", payment.ErrMissingCountry
	case "usd":
		return "us_bank_transfer", "", nil
	case "gbp":
		return "gb_bank_transfer", "", nil
	case "jpy":
		return "jp_bank_transfer", "", nil
	case "mxn":
		return "mx_bank_transfer", "", nil
	default:
		return "", "", nil
	}
}

// resultOf converts a PaymentIntent.
func resultOf(pi *stripe.PaymentIntent) payment.PaymentResult {
	r := payment.PaymentResult{
		ProviderPaymentID: pi.ID,
		ProviderStatus:    string(pi.Status),
		Status:            StatusOf(string(pi.Status)),
		RequiresAction:    string(pi.Status) == statusRequiresAction,
		Amount:            pi.Amount,
		Currency:          currencyOf(string(pi.Currency)),
	}
	if pi.PaymentMethod != nil {
		r.ProviderMethodID = pi.PaymentMethod.ID
		if pi.PaymentMethod.Type != "" {
			r.MethodDetails = detailsOf(pi.PaymentMethod)
		}
	}
	if r.RequiresAction && pi.LastResponse != nil {
		var raw struct {
			NextAction json.RawMessage `json:"next_action"`
		}
		if json.Unmarshal(pi.LastResponse.RawJSON, &raw) == nil && len(raw.NextAction) > 0 && string(raw.NextAction) != "null" {
			r.NextAction = raw.NextAction
			r.NextActionURL = nextActionURL(raw.NextAction)
		}
	}
	return r
}

// nextActionURL returns where the customer acts on a next_action:
// redirect_to_url.url, or the hosted page of the action's type
// (display_bank_transfer_instructions.hosted_instructions_url,
// boleto_display_details.hosted_voucher_url, …); "" for actions handled
// by Stripe.js (use_stripe_sdk).
func nextActionURL(nextAction json.RawMessage) string {
	var na map[string]json.RawMessage
	if json.Unmarshal(nextAction, &na) != nil {
		return ""
	}
	var typ string
	_ = json.Unmarshal(na["type"], &typ)
	if typ == "" {
		// Some objects carry the action without its type
		// ({"redirect_to_url": {"url": …}}, lago create_service_spec.rb:612).
		typ = "redirect_to_url"
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(na[typ], &detail) != nil {
		return ""
	}
	keys := []string{"url", "hosted_instructions_url", "hosted_voucher_url", "redirect_url"}
	for _, k := range keys {
		var u string
		if json.Unmarshal(detail[k], &u) == nil && u != "" {
			return u
		}
	}
	return ""
}

// GetPayment implements [payment.ChargeGateway] with the payment method
// expanded (lago payments/retrieve_service.rb).
func (g *Gateway) GetPayment(ctx context.Context, conn payment.Connection, providerPaymentID string) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	err := g.do(ctx, conn, "get_payment", func(ctx context.Context, c call) error {
		params := &stripe.PaymentIntentRetrieveParams{}
		params.AddExpand("payment_method")
		pi, err := c.client.V1PaymentIntents.Retrieve(ctx, providerPaymentID, params)
		if err != nil {
			return err
		}
		res = resultOf(pi)
		return nil
	})
	return res, err
}

// CancelPayment implements [payment.ChargeGateway] with reason abandoned,
// returning the canceled intent's state; an intent Stripe can no longer
// cancel is left as is (lago payments/cancel_service.rb).
func (g *Gateway) CancelPayment(ctx context.Context, conn payment.Connection, providerPaymentID string) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	err := g.do(ctx, conn, "cancel_payment", func(ctx context.Context, c call) error {
		pi, err := c.client.V1PaymentIntents.Cancel(ctx, providerPaymentID, &stripe.PaymentIntentCancelParams{
			CancellationReason: stripe.String("abandoned"),
		})
		if err != nil {
			if hasCode(err, codePaymentIntentUnexpected) {
				g.logger.InfoContext(ctx, "stripe payment intent not cancelable", "provider_payment_id", providerPaymentID, slogErr(err))
				return nil
			}
			return err
		}
		res = resultOf(pi)
		return nil
	})
	return res, err
}

// UpdatePaymentReference implements [payment.ChargeGateway] (lago
// payments/update_reference_service.rb): best effort, failures are logged.
func (g *Gateway) UpdatePaymentReference(ctx context.Context, conn payment.Connection, providerPaymentID, invoiceNumber string) error {
	if providerPaymentID == "" || invoiceNumber == "" {
		return nil
	}
	err := g.do(ctx, conn, "update_payment_reference", func(ctx context.Context, c call) error {
		_, err := c.client.V1PaymentIntents.Update(ctx, providerPaymentID, &stripe.PaymentIntentUpdateParams{
			Description: stripe.String(invoiceNumber),
			Metadata:    map[string]string{metaInvoiceNumber: invoiceNumber},
		})
		return err
	})
	if err != nil {
		g.logger.WarnContext(ctx, "stripe payment reference not updated", "provider_payment_id", providerPaymentID, slogErr(err))
	}
	return nil
}

// Authorize implements [payment.ChargeGateway] (lago
// payments/authorize_service.rb): a manual-capture PaymentIntent without
// redirects on the requested method, else the customer's default, else its
// newest card. The caller cancels it ([Gateway.CancelPayment]) once the
// subscription is activated.
func (g *Gateway) Authorize(ctx context.Context, conn payment.Connection, p payment.AuthorizeParams) (payment.PaymentResult, error) {
	var res payment.PaymentResult
	err := g.do(ctx, conn, "authorize", func(ctx context.Context, c call) error {
		method := p.ProviderMethodID
		if method == "" {
			method = defaultMethod(ctx, c, g, p.ProviderCustomerID)
		}
		if method == "" {
			return payment.ErrNoPaymentMethod
		}
		params := &stripe.PaymentIntentCreateParams{
			Amount:        stripe.Int64(p.Amount),
			Currency:      stripe.String(strings.ToLower(p.Currency.Code())),
			Confirm:       stripe.Bool(true),
			CaptureMethod: stripe.String("manual"),
			Customer:      stripe.String(p.ProviderCustomerID),
			PaymentMethod: stripe.String(method),
			Description:   stripe.String("Pre-authorization for subscription"),
			Metadata:      p.Metadata,
			// Lago sends the configured URL, nil when unset; redirects are
			// disabled, so Stripe needs none.
			ReturnURL: optional(c.settings.SuccessRedirectURL),
			AutomaticPaymentMethods: &stripe.PaymentIntentCreateAutomaticPaymentMethodsParams{
				Enabled:        stripe.Bool(true),
				AllowRedirects: stripe.String("never"),
			},
		}
		if p.IdempotencyKey != "" {
			params.SetIdempotencyKey(p.IdempotencyKey)
		}
		pi, err := c.client.V1PaymentIntents.Create(ctx, params)
		if err != nil {
			return err
		}
		res = resultOf(pi)
		return nil
	})
	return res, err
}
