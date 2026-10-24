package cashfree

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Webhook headers: Cashfree sends the X-Webhook-* pair, older integrations
// the X-Cashfree-* pair (lago webhooks_controller.rb cashfree).
const (
	SignatureHeader       = "X-Webhook-Signature"
	TimestampHeader       = "X-Webhook-Timestamp"
	LegacySignatureHeader = "X-Cashfree-Signature"
	LegacyTimestampHeader = "X-Cashfree-Timestamp"
)

// EventPaymentLink is the payment link webhook type (lago
// cashfree/handle_event_service.rb EVENT_MAPPING).
const EventPaymentLink = "PAYMENT_LINK_EVENT"

// VerifyWebhook implements [payment.WebhookGateway]: the signature header
// must be the base64 HMAC-SHA256, keyed by the connection's client secret,
// of the timestamp header followed by the body (lago
// cashfree/handle_incoming_webhook_service.rb). The X-Cashfree-* headers
// are read first, as in lago.
func (g *Gateway) VerifyWebhook(conn payment.Connection, payload []byte, header http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.ClientSecret == "" {
		return fmt.Errorf("cashfree: missing client secret: %w", payment.ErrInvalidSignature)
	}
	ts := first(header.Get(LegacyTimestampHeader), header.Get(TimestampHeader))
	sig := first(header.Get(LegacySignatureHeader), header.Get(SignatureHeader))
	if sig == "" || !hmac.Equal([]byte(sig), []byte(Sign(payload, ts, sc.ClientSecret))) {
		return fmt.Errorf("cashfree: %w", payment.ErrInvalidSignature)
	}
	return nil
}

// Sign returns the signature of a delivery: the base64 HMAC-SHA256 under
// secret of the timestamp followed by the body.
func Sign(body []byte, timestamp, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// delivery is a webhook body.
type delivery struct {
	Type      string `json:"type"`
	EventTime string `json:"event_time"`
	Data      struct {
		LinkID         string            `json:"link_id"`
		LinkStatus     string            `json:"link_status"`
		LinkCurrency   string            `json:"link_currency"`
		LinkAmountPaid json.RawMessage   `json:"link_amount_paid"`
		LinkNotes      map[string]string `json:"link_notes"`
	} `json:"data"`
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// cashfree/webhooks/payment_link_event_service.rb): a PAYMENT_LINK_EVENT
// whose link is PAID and whose notes name an invoice or a payable is a
// [payment.PaymentEvent] of the link, succeeded, with the paid amount in
// minor units. A link created by CreatePaymentURL is one-time
// ([payment.PaymentEvent.OneTime]): the handler creates its payment.
//
// Lago takes the invoice or payable ID as the payment's provider ID; the
// link ID, Cashfree's own, is used here, the payable being in Refs. Links
// of other statuses and events of other types yield no event (lago raises
// on an unknown type). An unknown payable type is [payment.ErrUnknownPayable]
// (lago raises NameError). Cashfree deliveries carry no event ID: an event
// is identified by its link and status.
func (g *Gateway) ParseWebhook(_ context.Context, conn payment.Connection, payload []byte) ([]payment.Event, error) {
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("cashfree: decode event: %w: %w", payment.ErrInvalidPayload, err)
	}
	if d.Type == "" {
		return nil, fmt.Errorf("cashfree: decode event: no type: %w", payment.ErrInvalidPayload)
	}
	st, _, err := parseConn(conn)
	if err != nil {
		return nil, err
	}
	if d.Type != EventPaymentLink || d.Data.LinkStatus != statusPaid {
		return nil, nil
	}
	notes := d.Data.LinkNotes
	if notes[noteInvoiceID] == "" && notes[notePayableID] == "" {
		return nil, nil
	}
	if d.Data.LinkID == "" {
		return nil, fmt.Errorf("cashfree: payment link event without a link: %w", payment.ErrInvalidPayload)
	}
	refs, err := refsOf(notes)
	if err != nil {
		return nil, err
	}
	amount, cur, err := amountPaid(d.Data.LinkAmountPaid, d.Data.LinkCurrency)
	if err != nil {
		return nil, err
	}
	meta := payment.EventMeta{
		ProviderEventID: d.Data.LinkID + "." + d.Data.LinkStatus,
		Type:            d.Type,
		Livemode:        !st.Sandbox,
	}
	if t, err := time.Parse(time.RFC3339, d.EventTime); err == nil {
		meta.OccurredAt = t.UTC()
	}
	return []payment.Event{payment.PaymentEvent{
		EventMeta:         meta,
		ProviderPaymentID: d.Data.LinkID,
		ProviderStatus:    d.Data.LinkStatus,
		Status:            StatusOf(d.Data.LinkStatus),
		Amount:            amount,
		Currency:          cur,
		OneTime:           notes[notePaymentType] == paymentTypeOnce,
		Refs:              refs,
	}}, nil
}

// refsOf reads the Tanvo identifiers of link notes; malformed IDs are
// ignored. An absent payable type is the invoice, as in lago.
func refsOf(n map[string]string) (payment.Refs, error) {
	var r payment.Refs
	// A malformed ID parses as zero, the absent value.
	r.CustomerID, _ = core.ParseID[core.CustomerID](n[noteCustomerID])
	switch t := payment.PayableType(n[notePayableType]); t {
	case payment.PayableTypePaymentRequest:
		if id, _ := core.ParseID[core.PaymentRequestID](n[notePayableID]); !id.IsZero() {
			r.Payable = payment.RequestPayable(id)
		}
	case "", payment.PayableTypeInvoice:
		s := n[noteInvoiceID]
		if s == "" {
			s = n[notePayableID]
		}
		if id, _ := core.ParseID[core.InvoiceID](s); !id.IsZero() {
			r.Payable = payment.InvoicePayable(id)
		}
	default:
		return r, fmt.Errorf("cashfree: payable type %q: %w", string(t), payment.ErrUnknownPayable)
	}
	return r, nil
}

// amountPaid converts link_amount_paid, a decimal string or number in
// major units, to minor units, rounding half to even as Money.from_amount
// does (lago payment_link_event_service.rb link_amount_paid_cents). An
// absent amount or currency is zero.
func amountPaid(raw json.RawMessage, code string) (int64, money.Currency, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(bytes.TrimSpace(raw))
	}
	if s == "" || s == "null" || code == "" {
		return 0, money.Currency{}, nil
	}
	cur, err := money.LookupCurrency(strings.ToUpper(code))
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("cashfree: currency %q: %w: %w", code, payment.ErrInvalidPayload, err)
	}
	d, err := money.Parse(s)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("cashfree: amount %q: %w: %w", s, payment.ErrInvalidPayload, err)
	}
	n, err := cur.ToMinor(d, money.HalfEven)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("cashfree: amount %q: %w: %w", s, payment.ErrInvalidPayload, err)
	}
	return n, cur, nil
}
