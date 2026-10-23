package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v84"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// SignatureHeader carries a delivery's signature.
const SignatureHeader = "Stripe-Signature"

// SignatureTolerance is how old a signed timestamp may be (stripe-ruby
// Webhook::DEFAULT_TOLERANCE, used by lago validate_incoming_webhook_service.rb).
const SignatureTolerance = 300 * time.Second

// Stripe event types the adapter subscribes to and decodes.
const (
	EventSetupIntentSucceeded         = "setup_intent.succeeded"
	EventPaymentIntentSucceeded       = "payment_intent.succeeded"
	EventPaymentIntentPaymentFailed   = "payment_intent.payment_failed"
	EventPaymentIntentCanceled        = "payment_intent.canceled"
	EventPaymentIntentRequiresAction  = "payment_intent.requires_action"
	EventPaymentMethodAttached        = "payment_method.attached"
	EventPaymentMethodDetached        = "payment_method.detached"
	EventChargeRefundUpdated          = "charge.refund.updated"
	EventCustomerUpdated              = "customer.updated"
	EventChargeDisputeClosed          = "charge.dispute.closed"
	EventCustomerCashBalanceTxCreated = "customer_cash_balance_transaction.created"
)

// WebhookEvents are the events a registered endpoint receives: lago's nine
// (StripeProvider::WEBHOOKS_EVENTS) plus payment_intent.requires_action, so
// a payment that comes to need authentication after creation (a delayed
// 3-D Secure or bank redirect) reports its next action, and
// payment_method.attached, so methods attached outside a setup page (the
// Stripe dashboard, the API) join the customer's list; lago only learns of
// those when they become the default (customer.updated).
var WebhookEvents = []string{
	EventSetupIntentSucceeded,
	EventPaymentIntentPaymentFailed,
	EventPaymentIntentSucceeded,
	EventPaymentIntentCanceled,
	EventPaymentMethodDetached,
	EventChargeRefundUpdated,
	EventCustomerUpdated,
	EventChargeDisputeClosed,
	EventCustomerCashBalanceTxCreated,
	EventPaymentIntentRequiresAction,
	EventPaymentMethodAttached,
}

// RegisterWebhook implements [payment.ConnectionGateway] (lago
// payment_providers/stripe/register_webhook_service.rb): an endpoint
// delivering [WebhookEvents] to endpointURL in stripe-go's API version.
// The endpoint ID goes to the settings and its signing secret to the
// secrets. Lago turns authentication, permission and endpoint-limit
// failures into a payment_provider.error webhook and reports success;
// here they are returned, and apps/server emits that webhook.
//
// Lago registers `<api>/webhooks/stripe/<organization>?code=<code>` in
// the version of the STRIPE_API_VERSION setting; Tanvo routes deliveries by
// connection (architecture §7, POST /webhooks/{provider}/{connection_id})
// and pins the version of the client that decodes its responses.
func (g *Gateway) RegisterWebhook(ctx context.Context, conn payment.Connection, endpointURL string) (payment.ConnectionUpdate, error) {
	var up payment.ConnectionUpdate
	err := g.do(ctx, conn, "register_webhook", func(ctx context.Context, c call) error {
		ep, err := c.client.V1WebhookEndpoints.Create(ctx, &stripe.WebhookEndpointCreateParams{
			URL:           stripe.String(endpointURL),
			EnabledEvents: stripe.StringSlice(WebhookEvents),
			APIVersion:    stripe.String(stripe.APIVersion),
		})
		if err != nil {
			return err
		}
		if up.Settings, err = mergeObject(conn.Settings, "webhook_endpoint_id", ep.ID); err != nil {
			return err
		}
		up.Secrets, err = mergeObject(conn.Secrets, "webhook_secret", ep.Secret)
		return err
	})
	return up, err
}

// RefreshWebhook implements [payment.ConnectionGateway] (lago
// refresh_webhook_service.rb): the registered endpoint's URL and events
// are updated; without one it is [core.ErrNotFound].
func (g *Gateway) RefreshWebhook(ctx context.Context, conn payment.Connection, endpointURL string) error {
	st, _, err := parseConn(conn)
	if err != nil {
		return err
	}
	if st.WebhookEndpointID == "" {
		return fmt.Errorf("stripe refresh webhook: no endpoint registered: %w", core.ErrNotFound)
	}
	return g.do(ctx, conn, "refresh_webhook", func(ctx context.Context, c call) error {
		_, err := c.client.V1WebhookEndpoints.Update(ctx, st.WebhookEndpointID, &stripe.WebhookEndpointUpdateParams{
			URL:           stripe.String(endpointURL),
			EnabledEvents: stripe.StringSlice(WebhookEvents),
		})
		return err
	})
}

// mergeObject sets key to value in the JSON object raw, keeping its other
// members.
func mergeObject(raw json.RawMessage, key, value string) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("stripe: decode connection object: %w", core.ErrInvalidValue)
		}
	}
	v, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("stripe: encode %s: %w", key, err)
	}
	m[key] = v
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("stripe: encode connection object: %w", err)
	}
	return out, nil
}

// VerifyWebhook implements [payment.WebhookGateway] with Stripe's scheme
// (stripe-ruby Webhook::Signature.verify_header): the Stripe-Signature
// header holds t=<unix time> and one or more v1=<hex HMAC-SHA256 of
// "<t>.<payload>"> under the endpoint's signing secret; one must match,
// and t must be at most [SignatureTolerance] old.
func (g *Gateway) VerifyWebhook(conn payment.Connection, payload []byte, header http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.WebhookSecret == "" {
		return fmt.Errorf("stripe: no webhook secret: %w", payment.ErrInvalidSignature)
	}
	return verifySignature(payload, header.Get(SignatureHeader), sc.WebhookSecret, g.now(), SignatureTolerance)
}

func verifySignature(payload []byte, header, secret string, now time.Time, tolerance time.Duration) error {
	var ts int64 = -1
	var sigs [][]byte
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ts = n
			}
		case "v1":
			if b, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, b)
			}
		}
	}
	if ts < 0 {
		return fmt.Errorf("stripe: no timestamp in signature header: %w", payment.ErrInvalidSignature)
	}
	if len(sigs) == 0 {
		return fmt.Errorf("stripe: no v1 signature: %w", payment.ErrInvalidSignature)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := mac.Sum(nil)
	matched := false
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("stripe: signature mismatch: %w", payment.ErrInvalidSignature)
	}
	if tolerance > 0 && time.Unix(ts, 0).Before(now.Add(-tolerance)) {
		return fmt.Errorf("stripe: timestamp outside the tolerance zone: %w", payment.ErrInvalidSignature)
	}
	return nil
}

// event is the envelope of a Stripe event.
type event struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Livemode bool   `json:"livemode"`
	Created  int64  `json:"created"`
	Data     struct {
		Object             json.RawMessage `json:"object"`
		PreviousAttributes json.RawMessage `json:"previous_attributes"`
	} `json:"data"`
}

// expandable is a field holding an object ID or the expanded object.
type expandable string

// UnmarshalJSON accepts "id", {"id": …} and null.
func (e *expandable) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*e = expandable(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("stripe: expandable field: %w", err)
	}
	*e = expandable(o.ID)
	return nil
}

type intentObject struct {
	ID               string            `json:"id"`
	Status           string            `json:"status"`
	Amount           int64             `json:"amount"`
	Currency         string            `json:"currency"`
	Customer         expandable        `json:"customer"`
	PaymentMethod    expandable        `json:"payment_method"`
	Metadata         map[string]string `json:"metadata"`
	NextAction       json.RawMessage   `json:"next_action"`
	LastPaymentError *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

type setupIntentObject struct {
	Customer      expandable        `json:"customer"`
	PaymentMethod expandable        `json:"payment_method"`
	Metadata      map[string]string `json:"metadata"`
}

type methodObject struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Customer expandable        `json:"customer"`
	Metadata map[string]string `json:"metadata"`
	Card     *struct {
		DisplayBrand string `json:"display_brand"`
		Last4        string `json:"last4"`
		ExpMonth     int    `json:"exp_month"`
		ExpYear      int    `json:"exp_year"`
	} `json:"card"`
}

type refundObject struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
}

type customerObject struct {
	ID              string            `json:"id"`
	DefaultSource   expandable        `json:"default_source"`
	Metadata        map[string]string `json:"metadata"`
	InvoiceSettings struct {
		DefaultPaymentMethod expandable `json:"default_payment_method"`
	} `json:"invoice_settings"`
}

type disputeObject struct {
	PaymentIntent expandable `json:"payment_intent"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason"`
}

type cashBalanceTxObject struct {
	Type          string     `json:"type"`
	Customer      expandable `json:"customer"`
	EndingBalance int64      `json:"ending_balance"`
	Currency      string     `json:"currency"`
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// payment_providers/stripe/handle_event_service.rb and webhooks/*): each
// handled event type yields one typed event, other types none.
//
//   - payment_intent.succeeded, .payment_failed and .canceled report the
//     payment succeeded or failed (both failures as failed), with the amount,
//     the last error code and the metadata refs; .requires_action reports it
//     processing with its next action.
//   - setup_intent.succeeded reports the method saved, to become the
//     default; one without a customer is skipped.
//   - payment_method.attached and .detached report the method, the
//     detached customer coming from previous_attributes when the object no
//     longer has one.
//   - customer.updated reports the default method, else the default source.
//   - charge.refund.updated reports the refund status.
//   - charge.dispute.closed reports the outcome, closed at the event's
//     creation.
//   - customer_cash_balance_transaction.created reports funded transfers
//     leaving a positive balance only.
//
// A payment event naming an unknown payable type is
// [payment.ErrUnknownPayable] and a refund event with an unmapped status
// [payment.ErrUnknownStatus]; the handler acknowledges either with a
// logged warning and updates no record.
func (g *Gateway) ParseWebhook(ctx context.Context, conn payment.Connection, payload []byte) ([]payment.Event, error) {
	var ev event
	if err := json.Unmarshal(payload, &ev); err != nil || ev.Type == "" {
		return nil, fmt.Errorf("stripe: decode event: %w", payment.ErrInvalidPayload)
	}
	meta := payment.EventMeta{
		ProviderEventID: ev.ID,
		Type:            ev.Type,
		Livemode:        ev.Livemode,
		OccurredAt:      time.Unix(ev.Created, 0).UTC(),
	}
	decode := func(v any) error {
		if err := json.Unmarshal(ev.Data.Object, v); err != nil {
			return fmt.Errorf("stripe: decode %s object: %w", ev.Type, payment.ErrInvalidPayload)
		}
		return nil
	}
	switch ev.Type {
	case EventPaymentIntentSucceeded, EventPaymentIntentPaymentFailed, EventPaymentIntentCanceled, EventPaymentIntentRequiresAction:
		var o intentObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		if err := checkPayableType(o.Metadata); err != nil {
			return nil, fmt.Errorf("stripe: event %s: %w", ev.ID, err)
		}
		return []payment.Event{paymentEvent(meta, o)}, nil
	case EventSetupIntentSucceeded:
		var o setupIntentObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		if o.Customer == "" {
			return nil, nil
		}
		return []payment.Event{payment.PaymentMethodAttached{
			EventMeta:          meta,
			ProviderCustomerID: string(o.Customer),
			ProviderMethodID:   string(o.PaymentMethod),
			SetAsDefault:       true,
			Refs:               refsOf(o.Metadata),
		}}, nil
	case EventPaymentMethodAttached:
		var o methodObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		if o.Customer == "" {
			return nil, nil
		}
		return []payment.Event{payment.PaymentMethodAttached{
			EventMeta:          meta,
			ProviderCustomerID: string(o.Customer),
			ProviderMethodID:   o.ID,
			Details:            o.details(),
			Refs:               refsOf(o.Metadata),
		}}, nil
	case EventPaymentMethodDetached:
		var o methodObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		cus := o.Customer
		if cus == "" && len(ev.Data.PreviousAttributes) > 0 {
			var prev struct {
				Customer expandable `json:"customer"`
			}
			if json.Unmarshal(ev.Data.PreviousAttributes, &prev) == nil {
				cus = prev.Customer
			}
		}
		return []payment.Event{payment.PaymentMethodDetached{
			EventMeta:          meta,
			ProviderCustomerID: string(cus),
			ProviderMethodID:   o.ID,
			Refs:               refsOf(o.Metadata),
		}}, nil
	case EventCustomerUpdated:
		var o customerObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		def := o.InvoiceSettings.DefaultPaymentMethod
		if def == "" {
			def = o.DefaultSource
		}
		return []payment.Event{payment.CustomerUpdated{
			EventMeta:          meta,
			ProviderCustomerID: o.ID,
			DefaultMethodID:    string(def),
			Refs:               refsOf(o.Metadata),
		}}, nil
	case EventChargeRefundUpdated:
		var o refundObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		status, err := RefundStatusOf(o.Status)
		if err != nil {
			return nil, fmt.Errorf("stripe: event %s: %w", ev.ID, err)
		}
		return []payment.Event{payment.RefundEvent{
			EventMeta:        meta,
			ProviderRefundID: o.ID,
			ProviderStatus:   o.Status,
			Status:           status,
			Refs:             refsOf(o.Metadata),
		}}, nil
	case EventChargeDisputeClosed:
		var o disputeObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		return []payment.Event{payment.DisputeClosed{
			EventMeta:         meta,
			ProviderPaymentID: string(o.PaymentIntent),
			ProviderStatus:    o.Status,
			Lost:              o.Status == "lost",
			Reason:            o.Reason,
			ClosedAt:          meta.OccurredAt,
		}}, nil
	case EventCustomerCashBalanceTxCreated:
		var o cashBalanceTxObject
		if err := decode(&o); err != nil {
			return nil, err
		}
		if o.Type != "funded" || o.EndingBalance <= 0 {
			return nil, nil
		}
		return []payment.Event{payment.CustomerBalanceFunded{
			EventMeta:          meta,
			ProviderCustomerID: string(o.Customer),
			EndingBalance:      o.EndingBalance,
			Currency:           currencyOf(o.Currency),
		}}, nil
	default:
		g.logger.DebugContext(ctx, "stripe event ignored", "type", ev.Type, "provider_event_id", ev.ID)
		return nil, nil
	}
}

func paymentEvent(meta payment.EventMeta, o intentObject) payment.PaymentEvent {
	e := payment.PaymentEvent{
		EventMeta:          meta,
		ProviderPaymentID:  o.ID,
		ProviderStatus:     o.Status,
		Amount:             o.Amount,
		Currency:           currencyOf(o.Currency),
		ProviderCustomerID: string(o.Customer),
		ProviderMethodID:   string(o.PaymentMethod),
		OneTime:            o.Metadata[metaPaymentType] == paymentTypeOneTime,
		Refs:               refsOf(o.Metadata),
	}
	if o.LastPaymentError != nil {
		e.ErrorCode = o.LastPaymentError.Code
		e.ErrorMessage = o.LastPaymentError.Message
	}
	switch meta.Type {
	case EventPaymentIntentSucceeded:
		e.Status = payment.StateSucceeded
	case EventPaymentIntentRequiresAction:
		e.Status = payment.StateProcessing
		e.RequiresAction = true
		if len(o.NextAction) > 0 && string(o.NextAction) != "null" {
			e.NextAction = o.NextAction
			e.NextActionURL = nextActionURL(o.NextAction)
		}
	default:
		// Lago reports payment_failed and canceled as "failed" whatever
		// the intent's status (webhooks/payment_intent_payment_failed_service.rb).
		e.Status = payment.StateFailed
	}
	return e
}

func (o methodObject) details() payment.MethodDetails {
	d := payment.MethodDetails{Type: o.Type}
	if o.Card != nil {
		d.Brand, d.Last4, d.ExpMonth, d.ExpYear = o.Card.DisplayBrand, o.Card.Last4, o.Card.ExpMonth, o.Card.ExpYear
	}
	return d
}

// checkPayableType rejects a payment's metadata naming a payable type
// Tanvo does not know with [payment.ErrUnknownPayable]: lago raises
// NameError and updates nothing (webhooks/base_service.rb
// payment_service_klass, payment_intent_succeeded_service_spec.rb:187),
// and guessing the invoice could apply the payment to the wrong object.
// An absent type is the invoice, as in lago.
func checkPayableType(m map[string]string) error {
	switch t := payment.PayableType(m[metaPayableType]); t {
	case "", payment.PayableTypeInvoice, payment.PayableTypePaymentRequest:
		return nil
	default:
		return fmt.Errorf("%q: %w", string(t), payment.ErrUnknownPayable)
	}
}

// refsOf reads the Tanvo identifiers of a metadata object; malformed
// values are ignored. An absent payable type is the invoice, as lago's
// lago_payable_type is (webhooks/base_service.rb payment_service_klass);
// payment events check the type first ([checkPayableType]).
func refsOf(m map[string]string) payment.Refs {
	var r payment.Refs
	// A malformed ID parses as zero, the absent value.
	r.CustomerID, _ = core.ParseID[core.CustomerID](m[metaCustomerID])
	r.PaymentID, _ = core.ParseID[core.PaymentID](m[metaPaymentID])
	r.CreditNoteID, _ = core.ParseID[core.CreditNoteID](m[metaCreditNoteID])
	r.InvoiceID, _ = core.ParseID[core.InvoiceID](m[metaInvoiceID])
	switch payment.PayableType(m[metaPayableType]) {
	case payment.PayableTypePaymentRequest:
		if id, _ := core.ParseID[core.PaymentRequestID](m[metaPayableID]); !id.IsZero() {
			r.Payable = payment.RequestPayable(id)
		}
	default:
		id, _ := core.ParseID[core.InvoiceID](m[metaPayableID])
		if id.IsZero() {
			id = r.InvoiceID
		}
		if !id.IsZero() {
			r.Payable = payment.InvoicePayable(id)
		}
	}
	return r
}
