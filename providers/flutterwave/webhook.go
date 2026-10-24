package flutterwave

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// SignatureHeader carries the connection's webhook secret hash on every
// delivery (lago webhooks_controller.rb flutterwave: verif-hash).
const SignatureHeader = "verif-hash"

// EventChargeCompleted is the charge webhook type (lago
// flutterwave/handle_event_service.rb EVENT_MAPPING).
const EventChargeCompleted = "charge.completed"

// VerifyWebhook implements [payment.WebhookGateway]: the verif-hash header
// must equal the connection's webhook secret (lago
// flutterwave/handle_incoming_webhook_service.rb). A connection without a
// webhook secret accepts no delivery.
func (g *Gateway) VerifyWebhook(conn payment.Connection, _ []byte, header http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.WebhookSecret == "" {
		return fmt.Errorf("flutterwave: webhook secret is missing: %w", payment.ErrInvalidSignature)
	}
	got := header.Get(SignatureHeader)
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sc.WebhookSecret)) != 1 {
		return fmt.Errorf("flutterwave: invalid webhook secret: %w", payment.ErrInvalidSignature)
	}
	return nil
}

// delivery is a webhook body.
type delivery struct {
	Event string `json:"event"`
	Data  struct {
		ID        json.RawMessage            `json:"id"`
		TxRef     string                     `json:"tx_ref"`
		Status    string                     `json:"status"`
		CreatedAt string                     `json:"created_at"`
		Meta      map[string]json.RawMessage `json:"meta"`
	} `json:"data"`
}

// transaction is a verified transaction.
type transaction struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		ID       json.RawMessage `json:"id"`
		TxRef    string          `json:"tx_ref"`
		Amount   json.RawMessage `json:"amount"`
		Currency string          `json:"currency"`
		Status   string          `json:"status"`
	} `json:"data"`
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// flutterwave/webhooks/charge_completed_service.rb): a successful
// charge.completed naming an invoice or a payment request is verified with
// Flutterwave's transaction API, and a transaction Flutterwave confirms as
// successful is a [payment.PaymentEvent], one-time as lago marks every
// Flutterwave payment ([payment.PaymentEvent.OneTime]: the handler creates
// it), with the verified amount and currency.
//
// The payable is the meta's tanvo_invoice_id, else its tanvo_payable_id
// with tanvo_payable_type, else the transaction reference (an invoice ID,
// or tanvo_payment_request_<id>). Lago takes that ID as the payment's
// provider ID; Flutterwave's transaction ID is used here, the payable
// being in Refs. Other events and statuses, deliveries without a payable,
// a payable ID that is not a Tanvo ID (lago finds no record) and
// transactions Flutterwave does not confirm yield no event; an unknown
// payable type is [payment.ErrUnknownPayable] (lago raises NameError). A
// verification Flutterwave refuses yields no event, as in lago; one that
// fails retryably (network, 5xx, 429) is returned, so the delivery is
// retried, where lago logs it and drops the event.
func (g *Gateway) ParseWebhook(ctx context.Context, conn payment.Connection, payload []byte) ([]payment.Event, error) {
	var d delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("flutterwave: decode event: %w: %w", payment.ErrInvalidPayload, err)
	}
	if d.Event != EventChargeCompleted || d.Data.Status != statusSuccessful {
		return nil, nil
	}
	meta := strMap(d.Data.Meta)
	ref, ok, err := payableRef(meta, d.Data.TxRef)
	if err != nil || !ok {
		return nil, err
	}
	id := scalar(d.Data.ID)
	if id == "" {
		return nil, fmt.Errorf("flutterwave: charge without a transaction ID: %w", payment.ErrInvalidPayload)
	}
	if ref.Payable.Validate() != nil {
		g.logger.DebugContext(ctx, "flutterwave charge names no Tanvo payable",
			slog.String("tanvo.organization.id", conn.OrganizationID.String()), slog.String("transaction_id", id))
		return nil, nil
	}
	_, sc, err := parseConn(conn)
	if err != nil {
		return nil, err
	}
	var tx transaction
	err = g.do(ctx, conn, "verify_transaction", func(ctx context.Context, c call) error {
		return c.send(ctx, http.MethodGet, fmt.Sprintf(pathVerify, url.PathEscape(id)), nil, &tx)
	})
	if err != nil {
		if errors.Is(err, payment.ErrRetryable) || !isProviderError(err) {
			return nil, err
		}
		g.logger.WarnContext(ctx, "flutterwave transaction verification refused",
			slog.String("tanvo.organization.id", conn.OrganizationID.String()), slog.String("transaction_id", id),
			slog.Any("error", err))
		return nil, nil
	}
	if tx.Status != "success" || tx.Data.Status != statusSuccessful {
		g.logger.WarnContext(ctx, "flutterwave transaction verification failed",
			slog.String("tanvo.organization.id", conn.OrganizationID.String()), slog.String("transaction_id", id),
			slog.String("status", tx.Status), slog.String("message", tx.Message))
		return nil, nil
	}
	amount, cur, err := amountPaid(tx.Data.Amount, tx.Data.Currency)
	if err != nil {
		return nil, err
	}
	providerID := scalar(tx.Data.ID)
	if providerID == "" {
		providerID = id
	}
	ev := payment.PaymentEvent{
		EventMeta: payment.EventMeta{
			ProviderEventID: providerID + "." + tx.Data.Status,
			Type:            d.Event,
			Livemode:        sc.livemode(),
		},
		ProviderPaymentID: providerID,
		ProviderStatus:    tx.Data.Status,
		Status:            StatusOf(tx.Data.Status),
		Amount:            amount,
		Currency:          cur,
		OneTime:           true,
		Refs:              ref,
	}
	if t, err := time.Parse(time.RFC3339, d.Data.CreatedAt); err == nil {
		ev.OccurredAt = t.UTC()
	}
	return []payment.Event{ev}, nil
}

func isProviderError(err error) bool {
	_, ok := payment.AsProviderError(err)
	return ok
}

// payableRef resolves the payable of a charge; ok is false when it names
// none. A malformed ID leaves the payable zero.
func payableRef(meta map[string]string, txRef string) (payment.Refs, bool, error) {
	var r payment.Refs
	r.CustomerID, _ = core.ParseID[core.CustomerID](meta[metaCustomerID])
	id := meta[metaInvoiceID]
	if id == "" {
		id = meta[metaPayableID]
	}
	typ := payment.PayableType(meta[metaPayableType])
	if id == "" {
		id = txRef
		if rest, cut := strings.CutPrefix(txRef, requestTxRefPrefix); cut && typ == "" {
			id, typ = rest, payment.PayableTypePaymentRequest
		}
	}
	if id == "" {
		return r, false, nil
	}
	switch typ {
	case payment.PayableTypePaymentRequest:
		if pid, err := core.ParseID[core.PaymentRequestID](id); err == nil {
			r.Payable = payment.RequestPayable(pid)
		}
	case "", payment.PayableTypeInvoice:
		if iid, err := core.ParseID[core.InvoiceID](id); err == nil {
			r.Payable = payment.InvoicePayable(iid)
		}
	default:
		return r, false, fmt.Errorf("flutterwave: payable type %q: %w", string(typ), payment.ErrUnknownPayable)
	}
	return r, true, nil
}

// strMap renders the scalar values of a meta object as strings.
func strMap(m map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s := scalar(v); s != "" {
			out[k] = s
		}
	}
	return out
}

// scalar renders a JSON string or number as a string; "" otherwise.
func scalar(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// amountPaid converts a verified amount, in major units, to minor units,
// rounding half to even as Money.from_amount does (lago
// charge_completed_service.rb verified_amount_cents). An absent amount or
// currency is zero.
func amountPaid(raw json.RawMessage, code string) (int64, money.Currency, error) {
	s := scalar(raw)
	if s == "" || code == "" {
		return 0, money.Currency{}, nil
	}
	cur, err := money.LookupCurrency(strings.ToUpper(code))
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("flutterwave: currency %q: %w: %w", code, payment.ErrInvalidPayload, err)
	}
	d, err := money.Parse(s)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("flutterwave: amount %q: %w: %w", s, payment.ErrInvalidPayload, err)
	}
	n, err := cur.ToMinor(d, money.HalfEven)
	if err != nil {
		return 0, money.Currency{}, fmt.Errorf("flutterwave: amount %q: %w: %w", s, payment.ErrInvalidPayload, err)
	}
	return n, cur, nil
}
