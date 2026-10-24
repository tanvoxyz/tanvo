package moneyhash

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// SignatureHeader carries a delivery's signature, "t=<unix time>,v3=<hex
// HMAC>" (lago webhooks_controller.rb moneyhash).
const SignatureHeader = "MoneyHash-Signature"

// Webhook event types (lago moneyhash/handle_event_service.rb).
const (
	EventIntentProcessed       = "intent.processed"
	EventIntentTimeExpired     = "intent.time_expired"
	EventPurchaseSuccessful    = "transaction.purchase.successful"
	EventPurchaseFailed        = "transaction.purchase.failed"
	EventPurchasePendingAuthen = "transaction.purchase.pending_authentication"
	EventCardTokenCreated      = "card_token.created"
	EventCardTokenUpdated      = "card_token.updated"
	EventCardTokenDeleted      = "card_token.deleted"
)

// eventStatus maps an intent or transaction event to the MoneyHash status
// it reports (lago handle_event_service.rb event_to_payment_status).
var eventStatus = map[string]string{
	EventIntentProcessed:       statusSuccessful,
	EventPurchaseSuccessful:    statusSuccessful,
	EventIntentTimeExpired:     statusFailed,
	EventPurchaseFailed:        statusFailed,
	EventPurchasePendingAuthen: statusPending,
}

// VerifyWebhook implements [payment.WebhookGateway]: the v3 part of the
// MoneyHash-Signature header must be the hex HMAC-SHA256, keyed by the
// connection's signature key, of the base64 body followed by the header's
// timestamp (lago moneyhash/validate_incoming_webhook_service.rb). Lago
// signs its re-serialization of the parsed body; the body as delivered is
// signed here, which is what MoneyHash signs. A connection without a
// signature key verifies nothing.
func (g *Gateway) VerifyWebhook(conn payment.Connection, payload []byte, header http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.SignatureKey == "" {
		return fmt.Errorf("moneyhash: missing signature key: %w", payment.ErrInvalidSignature)
	}
	var ts, sig string
	for part := range strings.SplitSeq(header.Get(SignatureHeader), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v3":
			sig = v
		}
	}
	if sig == "" || !hmac.Equal([]byte(sig), []byte(Sign(payload, ts, sc.SignatureKey))) {
		return fmt.Errorf("moneyhash: %w", payment.ErrInvalidSignature)
	}
	return nil
}

// Sign returns the v3 signature of a delivery: the hex HMAC-SHA256 under
// key of the base64 body followed by the timestamp.
func Sign(body []byte, timestamp, key string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(base64.StdEncoding.EncodeToString(body)))
	m.Write([]byte(timestamp))
	return hex.EncodeToString(m.Sum(nil))
}

// SignatureValue returns a MoneyHash-Signature header value for body.
func SignatureValue(body []byte, timestamp, key string) string {
	return "t=" + timestamp + ",v3=" + Sign(body, timestamp, key)
}

// delivery is a webhook body; intent and card events nest their object in
// data, transaction events carry the intent at the top level.
type delivery struct {
	Type     string `json:"type"`
	StatusID string `json:"status_id"`
	Intent   *struct {
		ID           string          `json:"id"`
		Amount       json.RawMessage `json:"amount"`
		CustomFields json.RawMessage `json:"custom_fields"`
	} `json:"intent"`
	Data struct {
		IntentID string `json:"intent_id"`
		Intent   *struct {
			Amount         json.RawMessage `json:"amount"`
			AmountCurrency string          `json:"amount_currency"`
			CustomFields   json.RawMessage `json:"custom_fields"`
		} `json:"intent"`
		CardToken *cardToken `json:"card_token"`
	} `json:"data"`
}

// cardToken is a saved card.
type cardToken struct {
	ID           string          `json:"id"`
	Brand        string          `json:"brand"`
	Last4        string          `json:"last_4"`
	ExpiryMonth  string          `json:"expiry_month"`
	ExpiryYear   string          `json:"expiry_year"`
	CustomFields json.RawMessage `json:"custom_fields"`
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// moneyhash/handle_event_service.rb):
//
//   - intent.processed and intent.time_expired: [payment.PaymentEvent] of
//     data.intent_id, SUCCESSFUL or FAILED, with the intent's scalar amount
//     and currency.
//   - transaction.purchase.successful, .failed and .pending_authentication:
//     [payment.PaymentEvent] of intent.id, SUCCESSFUL, FAILED or PENDING,
//     with the intent's {value, currency} amount.
//   - card_token.created and card_token.updated:
//     [payment.PaymentMethodAttached] of the card for the customer in its
//     custom fields, set as default (lago
//     payment_provider_customers/moneyhash_service.rb update_payment_method).
//   - card_token.deleted: [payment.PaymentMethodDetached]
//     (delete_payment_method).
//
// Lago reads an intent event's payable type from intent.custom_fields,
// which intent events lack, so it always takes them for invoices; the
// event's own data.intent.custom_fields are read here. An unknown payable
// type is [payment.ErrUnknownPayable] (lago raises NameError). Lago fails
// the job on any other event type; other events yield none here, as
// MoneyHash delivers events the account subscribes to beyond these.
// MoneyHash deliveries carry no event ID: a transaction event is
// identified by its status_id, an intent event by its intent and type.
func (g *Gateway) ParseWebhook(_ context.Context, conn payment.Connection, payload []byte) ([]payment.Event, error) {
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("moneyhash: decode event: %w: %w", payment.ErrInvalidPayload, err)
	}
	if d.Type == "" {
		return nil, fmt.Errorf("moneyhash: decode event: no type: %w", payment.ErrInvalidPayload)
	}
	st, _, err := parseConn(conn)
	if err != nil {
		return nil, err
	}
	meta := payment.EventMeta{Type: d.Type, Livemode: !st.Sandbox}
	switch d.Type {
	case EventIntentProcessed, EventIntentTimeExpired:
		in := d.Data.Intent
		if d.Data.IntentID == "" || in == nil {
			return nil, fmt.Errorf("moneyhash: %s without an intent: %w", d.Type, payment.ErrInvalidPayload)
		}
		meta.ProviderEventID = d.Data.IntentID + "." + d.Type
		amount, cur, err := amountOfScalar(in.Amount, in.AmountCurrency)
		if err != nil {
			return nil, err
		}
		return paymentEvent(meta, d.Data.IntentID, amount, cur, in.CustomFields)
	case EventPurchaseSuccessful, EventPurchaseFailed, EventPurchasePendingAuthen:
		in := d.Intent
		if in == nil || in.ID == "" {
			return nil, fmt.Errorf("moneyhash: %s without an intent: %w", d.Type, payment.ErrInvalidPayload)
		}
		meta.ProviderEventID = d.StatusID
		amount, cur, err := amountOfValue(in.Amount)
		if err != nil {
			return nil, err
		}
		return paymentEvent(meta, in.ID, amount, cur, in.CustomFields)
	case EventCardTokenCreated, EventCardTokenUpdated, EventCardTokenDeleted:
		return cardEvent(meta, d.Data.CardToken)
	default:
		return nil, nil
	}
}

// paymentEvent builds the payment event of an intent.
func paymentEvent(meta payment.EventMeta, intentID string, amount int64, cur money.Currency, raw json.RawMessage) ([]payment.Event, error) {
	f := fieldsOf(raw)
	if err := checkPayableType(f); err != nil {
		return nil, err
	}
	status := eventStatus[meta.Type]
	return []payment.Event{payment.PaymentEvent{
		EventMeta:          meta,
		ProviderPaymentID:  intentID,
		ProviderStatus:     status,
		Status:             StatusOf(status),
		Amount:             amount,
		Currency:           cur,
		ProviderCustomerID: f[fieldProviderCustomer],
		Refs:               refsOf(f),
	}}, nil
}

// cardEvent builds the event of a card token.
func cardEvent(meta payment.EventMeta, c *cardToken) ([]payment.Event, error) {
	if c == nil || c.ID == "" {
		return nil, fmt.Errorf("moneyhash: %s without a card token: %w", meta.Type, payment.ErrInvalidPayload)
	}
	f := fieldsOf(c.CustomFields)
	refs := payment.Refs{CustomerID: refsOf(f).CustomerID}
	if meta.Type == EventCardTokenDeleted {
		return []payment.Event{payment.PaymentMethodDetached{
			EventMeta:          meta,
			ProviderCustomerID: f[fieldProviderCustomer],
			ProviderMethodID:   c.ID,
			Refs:               refs,
		}}, nil
	}
	return []payment.Event{payment.PaymentMethodAttached{
		EventMeta:          meta,
		ProviderCustomerID: f[fieldProviderCustomer],
		ProviderMethodID:   c.ID,
		Details: payment.MethodDetails{
			Type:     "card",
			Brand:    c.Brand,
			Last4:    c.Last4,
			ExpMonth: atoi(c.ExpiryMonth),
			ExpYear:  year(c.ExpiryYear),
		},
		SetAsDefault: true,
		Refs:         refs,
	}}, nil
}

// fieldsOf reads custom fields as strings; non-string values (tanvo_mit)
// are rendered as JSON and malformed fields are empty.
func fieldsOf(raw json.RawMessage) map[string]string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		out[k] = string(bytes.TrimSpace(v))
	}
	return out
}

// checkPayableType rejects a payable type that is neither an invoice nor a
// payment request with [payment.ErrUnknownPayable] (lago
// handle_event_service.rb payment_service_klass raises NameError). An
// absent type is the invoice, as in lago.
func checkPayableType(f map[string]string) error {
	switch t := payment.PayableType(f[fieldPayableType]); t {
	case "", payment.PayableTypeInvoice, payment.PayableTypePaymentRequest:
		return nil
	default:
		return fmt.Errorf("moneyhash: payable type %q: %w", string(t), payment.ErrUnknownPayable)
	}
}

// refsOf reads the Tanvo identifiers of custom fields; malformed values are
// ignored.
func refsOf(f map[string]string) payment.Refs {
	var r payment.Refs
	// A malformed ID parses as zero, the absent value.
	r.CustomerID, _ = core.ParseID[core.CustomerID](f[fieldCustomerID])
	if payment.PayableType(f[fieldPayableType]) == payment.PayableTypePaymentRequest {
		if id, _ := core.ParseID[core.PaymentRequestID](f[fieldPayableID]); !id.IsZero() {
			r.Payable = payment.RequestPayable(id)
		}
		return r
	}
	if id, _ := core.ParseID[core.InvoiceID](f[fieldPayableID]); !id.IsZero() {
		r.Payable = payment.InvoicePayable(id)
	}
	return r
}

// amountOfScalar converts an intent event's amount, a number or a decimal
// string in major units, with its sibling currency to minor units (lago
// handle_event_service.rb intent_amount_cents). An absent amount or
// currency is zero.
func amountOfScalar(raw json.RawMessage, code string) (int64, money.Currency, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(bytes.TrimSpace(raw))
	}
	return minorOf(s, code)
}

// amountOfValue converts a transaction event's {value, currency} amount.
func amountOfValue(raw json.RawMessage) (int64, money.Currency, error) {
	var a struct {
		Value    json.RawMessage `json:"value"`
		Currency string          `json:"currency"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return 0, money.Currency{}, nil
	}
	return amountOfScalar(a.Value, a.Currency)
}

// minorOf converts a major-unit amount to minor units, rounding half to
// even as Money.from_amount does.
func minorOf(amount, code string) (int64, money.Currency, error) {
	if amount == "" || amount == "null" || code == "" {
		return 0, money.Currency{}, nil
	}
	cur, err := money.LookupCurrency(strings.ToUpper(code))
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("moneyhash: currency %q: %w: %w", code, payment.ErrInvalidPayload, err)
	}
	d, err := money.Parse(amount)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("moneyhash: amount %q: %w: %w", amount, payment.ErrInvalidPayload, err)
	}
	n, err := cur.ToMinor(d, money.HalfEven)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("moneyhash: amount %q: %w: %w", amount, payment.ErrInvalidPayload, err)
	}
	return n, cur, nil
}

// atoi parses a number; a malformed one is zero.
func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// year parses a card expiry year; MoneyHash sends two digits ("26").
func year(s string) int {
	n := atoi(s)
	if n > 0 && n < 100 {
		n += 2000
	}
	return n
}
