package gocardless

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Metadata keys the adapter attaches to GoCardless payments and refunds
// and reads back from events (lago's lago_* keys, renamed). GoCardless
// accepts three keys of up to 50 characters.
const (
	metaCustomerID   = "tanvo_customer_id"
	metaPayableType  = "tanvo_payable_type"
	metaPayableID    = "tanvo_payable_id"
	metaInvoiceID    = "tanvo_invoice_id"
	metaCreditNoteID = "tanvo_credit_note_id"
	metaIssuingDate  = "invoice_issuing_date"
	metaReason       = "reason"
)

// Event resource types and actions (lago gocardless/handle_event_service.rb).
const (
	ResourcePayments = "payments"
	ResourceRefunds  = "refunds"
	ResourceMandates = "mandates"

	actionCreated   = "created"
	actionCancelled = "cancelled"

	// originAPI is the origin of a change made through the API or the
	// dashboard, as opposed to the bank or GoCardless.
	originAPI = "api"
)

// SignatureHeader carries a delivery's signature.
const SignatureHeader = "Webhook-Signature"

var (
	// paymentActions are the payment events that change a payment's state
	// (lago HandleEventService::PAYMENT_ACTIONS).
	paymentActions = []string{"paid_out", "failed", "cancelled", "customer_approval_denied", "charged_back"}
	// refundActions are the refund events that change a refund's state
	// (lago HandleEventService::REFUND_ACTIONS).
	refundActions = []string{"created", "funds_returned", "paid", "refund_settled", "failed"}
)

// delivery is a webhook body: a batch of events.
type delivery struct {
	Events []event `json:"events"`
}

// event is a GoCardless event (https://developer.gocardless.com/api-reference#core-endpoints-events).
type event struct {
	ID           string `json:"id"`
	CreatedAt    string `json:"created_at"`
	ResourceType string `json:"resource_type"`
	Action       string `json:"action"`
	Links        struct {
		Payment  string `json:"payment"`
		Refund   string `json:"refund"`
		Mandate  string `json:"mandate"`
		Customer string `json:"customer"`
	} `json:"links"`
	Details *struct {
		Origin string `json:"origin"`
		Cause  string `json:"cause"`
	} `json:"details"`
	// Metadata is the metadata sent with the API call that caused the
	// event; ResourceMetadata the metadata of the resource itself.
	Metadata         map[string]any `json:"metadata"`
	ResourceMetadata map[string]any `json:"resource_metadata"`
}

// metadata returns the event's string metadata: the resource's, then the
// event's, the latter winning. Lago reads the event's only
// (handle_event_service.rb payment_service_klass), which carries the
// metadata of the call that caused the event, not the resource's; a
// payment or refund event carries the Tanvo identifiers in its resource
// metadata.
func (e event) metadata() map[string]string {
	m := map[string]string{}
	for _, src := range []map[string]any{e.ResourceMetadata, e.Metadata} {
		for k, v := range src {
			if s, ok := v.(string); ok {
				m[k] = s
			}
		}
	}
	return m
}

// apiOriginated reports a change made through the API or the dashboard
// (lago handle_event_service.rb api_originated_event?).
func (e event) apiOriginated() bool { return e.Details != nil && e.Details.Origin == originAPI }

func decode(payload []byte) (delivery, error) {
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return d, fmt.Errorf("gocardless: decode events: %w: %w", payment.ErrInvalidPayload, err)
	}
	if d.Events == nil {
		return d, fmt.Errorf("gocardless: decode events: no events: %w", payment.ErrInvalidPayload)
	}
	return d, nil
}

// VerifyWebhook implements [payment.WebhookGateway]: the Webhook-Signature
// header must be the hex HMAC-SHA256 of the body keyed by the connection's
// webhook secret (gocardless-pro Webhook.signature_valid?, lago
// handle_incoming_webhook_service.rb). A connection without a secret
// verifies nothing.
func (g *Gateway) VerifyWebhook(conn payment.Connection, payload []byte, header http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.WebhookSecret == "" {
		return fmt.Errorf("gocardless: missing webhook secret: %w", payment.ErrInvalidSignature)
	}
	got := header.Get(SignatureHeader)
	want := Sign(payload, sc.WebhookSecret)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return fmt.Errorf("gocardless: %w", payment.ErrInvalidSignature)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of body under secret, a delivery's
// signature.
func Sign(body []byte, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// payment_providers/gocardless/handle_event_service.rb), event by event:
//
//   - payments paid_out, failed, cancelled, customer_approval_denied and
//     charged_back: [payment.PaymentEvent] with the action as status.
//   - refunds created, funds_returned, paid, refund_settled and failed:
//     [payment.RefundEvent] with the action as status.
//   - mandates created: the mandate is fetched and reported as
//     [payment.PaymentMethodAttached] for its customer, set as default
//     (lago webhooks/mandate_created_service.rb); a mandate GoCardless no
//     longer has yields no event.
//   - mandates cancelled through the API or the dashboard:
//     [payment.PaymentMethodDetached] (webhooks/mandate_cancelled_service.rb).
//
// Other events yield none. A payment event naming an unknown payable type
// cannot be applied (lago raises NameError, handle_event_service_spec.rb);
// as one delivery batches several events, it is skipped with a logged
// warning instead of failing the others with [payment.ErrUnknownPayable].
//
// Lago swallows every error fetching a created mandate
// (mandate_created_service.rb fetch_mandate); here only a missing mandate
// is skipped, and other failures fail the delivery so GoCardless
// redelivers it.
func (g *Gateway) ParseWebhook(ctx context.Context, conn payment.Connection, payload []byte) ([]payment.Event, error) {
	d, err := decode(payload)
	if err != nil {
		return nil, err
	}
	_, sc, err := parseConn(conn)
	if err != nil {
		return nil, err
	}
	var out []payment.Event
	for _, e := range d.Events {
		meta := payment.EventMeta{
			ProviderEventID: e.ID,
			Type:            e.ResourceType + "." + e.Action,
			Livemode:        sc.live(),
		}
		if t, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil {
			meta.OccurredAt = t.UTC()
		}
		ev, err := g.parseEvent(ctx, conn, meta, e)
		if errors.Is(err, payment.ErrUnknownPayable) {
			g.logger.WarnContext(ctx, "gocardless event of an unknown payable",
				slog.String("event_id", e.ID), slog.String("error", err.Error()))
			continue
		}
		if err != nil {
			return nil, err
		}
		if ev != nil {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (g *Gateway) parseEvent(ctx context.Context, conn payment.Connection, meta payment.EventMeta, e event) (payment.Event, error) {
	switch e.ResourceType {
	case ResourcePayments:
		if !slices.Contains(paymentActions, e.Action) {
			return nil, nil
		}
		m := e.metadata()
		if err := checkPayableType(m); err != nil {
			return nil, fmt.Errorf("gocardless: event %s: %w", e.ID, err)
		}
		return payment.PaymentEvent{
			EventMeta:         meta,
			ProviderPaymentID: e.Links.Payment,
			ProviderStatus:    e.Action,
			Status:            StatusOf(e.Action),
			Refs:              refsOf(m),
		}, nil
	case ResourceRefunds:
		if !slices.Contains(refundActions, e.Action) {
			return nil, nil
		}
		// Every refund action maps to a status.
		status, _ := RefundStatusOf(e.Action)
		return payment.RefundEvent{
			EventMeta:        meta,
			ProviderRefundID: e.Links.Refund,
			ProviderStatus:   e.Action,
			Status:           status,
			Refs:             refsOf(e.metadata()),
		}, nil
	case ResourceMandates:
		switch {
		case e.Action == actionCreated:
			return g.mandateCreated(ctx, conn, meta, e)
		case e.Action == actionCancelled && e.apiOriginated():
			return payment.PaymentMethodDetached{
				EventMeta:        meta,
				ProviderMethodID: e.Links.Mandate,
				Refs:             refsOf(e.metadata()),
			}, nil
		}
	}
	return nil, nil
}

// mandateCreated fetches a created mandate for its customer and scheme.
func (g *Gateway) mandateCreated(ctx context.Context, conn payment.Connection, meta payment.EventMeta, e event) (payment.Event, error) {
	pm, err := g.PaymentMethod(ctx, conn, e.Links.Mandate)
	if payment.ErrorKindOf(err) == payment.KindNotFound {
		g.logger.InfoContext(ctx, "gocardless mandate not found",
			slog.String("event_id", e.ID), slog.String("mandate_id", e.Links.Mandate))
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return payment.PaymentMethodAttached{
		EventMeta:          meta,
		ProviderCustomerID: pm.ProviderCustomerID,
		ProviderMethodID:   pm.ID,
		Details:            pm.Details,
		SetAsDefault:       true,
		Refs:               refsOf(e.metadata()),
	}, nil
}

// checkPayableType rejects a metadata payable type Tanvo does not know
// with [payment.ErrUnknownPayable]: lago raises NameError and updates
// nothing (handle_event_service.rb payment_service_klass). An absent type
// is the invoice, as in lago.
func checkPayableType(m map[string]string) error {
	switch t := payment.PayableType(m[metaPayableType]); t {
	case "", payment.PayableTypeInvoice, payment.PayableTypePaymentRequest:
		return nil
	default:
		return fmt.Errorf("%q: %w", string(t), payment.ErrUnknownPayable)
	}
}

// refsOf reads the Tanvo identifiers of a metadata object; malformed
// values are ignored. An absent payable type is the invoice.
func refsOf(m map[string]string) payment.Refs {
	var r payment.Refs
	// A malformed ID parses as zero, the absent value.
	r.CustomerID, _ = core.ParseID[core.CustomerID](m[metaCustomerID])
	r.InvoiceID, _ = core.ParseID[core.InvoiceID](m[metaInvoiceID])
	r.CreditNoteID, _ = core.ParseID[core.CreditNoteID](m[metaCreditNoteID])
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
