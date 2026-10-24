package adyen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Metadata keys the adapter attaches to Adyen payment links and reads back
// from notifications, where they arrive as "metadata.<key>" additional
// data (lago's lago_* keys, renamed). Adyen limits keys to 20 characters.
const (
	metaCustomerID  = "tanvo_customer_id"
	metaPayableType = "tanvo_payable_type"
	metaPayableID   = "tanvo_payable_id"
	metaInvoiceID   = "tanvo_invoice_id"
	metaInvoiceType = "invoice_type"
	metaIssuingDate = "invoice_issuing_date"
	metaPaymentType = "payment_type"

	// paymentTypeOneTime marks a payment made through a payment link.
	paymentTypeOneTime = "one-time"

	// metadataPrefix prefixes metadata keys in a notification's
	// additional data.
	metadataPrefix = "metadata."
)

// Notification event codes (lago AdyenProvider::WEBHOOKS_EVENTS and
// IGNORED_WEBHOOK_EVENTS).
const (
	EventAuthorisation     = "AUTHORISATION"
	EventCancellation      = "CANCELLATION"
	EventRefund            = "REFUND"
	EventRefundFailed      = "REFUND_FAILED"
	EventChargeback        = "CHARGEBACK"
	EventOfferClosed       = "OFFER_CLOSED"
	EventReportAvailable   = "REPORT_AVAILABLE"
	EventRecurringContract = "RECURRING_CONTRACT"
)

// disputeLost is a chargeback's dispute status when the merchant lost.
const disputeLost = "Lost"

// acknowledgement is the body Adyen requires in answer to an accepted
// notification (lago webhooks_controller.rb adyen renders "[accepted]").
const acknowledgement = "[accepted]"

// WebhookAcknowledgement implements [payment.WebhookAcknowledger].
func (g *Gateway) WebhookAcknowledgement() (contentType string, body []byte) {
	return "application/json", []byte(acknowledgement)
}

// notification is an Adyen standard notification.
type notification struct {
	Live  string `json:"live"`
	Items []struct {
		Item item `json:"NotificationRequestItem"`
	} `json:"notificationItems"`
}

// item is one notification request item.
type item struct {
	AdditionalData      map[string]string `json:"additionalData"`
	Amount              amount            `json:"amount"`
	EventCode           string            `json:"eventCode"`
	EventDate           string            `json:"eventDate"`
	MerchantAccountCode string            `json:"merchantAccountCode"`
	MerchantReference   string            `json:"merchantReference"`
	OriginalReference   string            `json:"originalReference"`
	PaymentMethod       string            `json:"paymentMethod"`
	PspReference        string            `json:"pspReference"`
	Reason              string            `json:"reason"`
	Success             string            `json:"success"`
}

type amount struct {
	Currency string      `json:"currency"`
	Value    json.Number `json:"value"`
}

func (i item) succeeded() bool { return i.Success == "true" }

// data returns the item's additional data value of key.
func (i item) data(key string) string { return i.AdditionalData[key] }

// metadata returns the item's metadata, without the prefix.
func (i item) metadata() map[string]string {
	m := map[string]string{}
	for k, v := range i.AdditionalData {
		if rest, ok := strings.CutPrefix(k, metadataPrefix); ok {
			m[rest] = v
		}
	}
	return m
}

func decode(payload []byte) (notification, error) {
	var n notification
	if err := json.Unmarshal(payload, &n); err != nil {
		return n, fmt.Errorf("adyen: %w: %w", payment.ErrInvalidPayload, err)
	}
	if len(n.Items) == 0 {
		return n, fmt.Errorf("adyen: no notification items: %w", payment.ErrInvalidPayload)
	}
	return n, nil
}

// VerifyWebhook implements [payment.WebhookGateway]: every item's
// additionalData.hmacSignature must be the base64 HMAC-SHA256, keyed by the
// connection's hex HMAC key, of its signed fields (Adyen's HMAC
// validation, adyen-ruby Utils::HmacValidator). A connection without an
// HMAC key verifies nothing (lago handle_incoming_webhook_service.rb:30).
// Lago verifies the first item only; every item is verified here, as each
// is parsed.
func (g *Gateway) VerifyWebhook(conn payment.Connection, payload []byte, _ http.Header) error {
	_, sc, err := parseConn(conn)
	if err != nil {
		return err
	}
	if sc.HMACKey == "" {
		return fmt.Errorf("adyen: missing hmac key: %w", payment.ErrInvalidSignature)
	}
	key, err := hex.DecodeString(sc.HMACKey)
	if err != nil {
		return fmt.Errorf("adyen: hmac key is not hex: %w", payment.ErrInvalidSignature)
	}
	n, err := decode(payload)
	if err != nil {
		return err
	}
	for _, it := range n.Items {
		want := Sign(it.Item.signedData(), key)
		got := it.Item.data("hmacSignature")
		if !hmac.Equal([]byte(got), []byte(want)) {
			return fmt.Errorf("adyen: item %s: %w", it.Item.PspReference, payment.ErrInvalidSignature)
		}
	}
	return nil
}

// signedData is the colon-separated list of an item's signed fields:
// pspReference, originalReference, merchantAccountCode, merchantReference,
// amount value and currency, eventCode and success.
func (i item) signedData() string {
	return strings.Join([]string{
		i.PspReference, i.OriginalReference, i.MerchantAccountCode, i.MerchantReference,
		i.Amount.Value.String(), i.Amount.Currency, i.EventCode, i.Success,
	}, ":")
}

// Sign returns the base64 HMAC-SHA256 of data under key, the signature of
// a notification item.
func Sign(data string, key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// ParseWebhook implements [payment.WebhookGateway] (lago
// payment_providers/adyen/handle_event_service.rb), item by item:
//
//   - AUTHORISATION of a payment link (metadata payment_type one-time):
//     [payment.PaymentEvent], OneTime, succeeded or failed by success, with
//     its amount and metadata.
//   - AUTHORISATION of zero (a setup link): [payment.PaymentMethodAttached]
//     for the shopper reference, set as default, when it succeeded, else
//     [payment.PaymentMethodSetupFailed] with the event code and reason
//     (lago payment_provider_customers/adyen_service.rb preauthorise).
//     ProviderMethodID is the recurring detail reference, "" when Adyen
//     sent none.
//   - Other AUTHORISATIONs report charges whose state CreatePayment
//     returned: no event.
//   - CANCELLATION that succeeded: the original payment failed with status
//     "Cancelled".
//   - REFUND: the refund succeeded or failed by success; REFUND_FAILED that
//     succeeded: it failed.
//   - CHARGEBACK lost: [payment.DisputeClosed], lost, at the event date;
//     other chargebacks change nothing, as in lago
//     (webhooks/chargeback_service.rb). Lago's chargeback service reads the
//     items list from the item it was handed and finds no event
//     (webhooks/base_service.rb); the item is read directly here.
//
// Ignored and unknown event codes yield no event: lago ignores the first
// and fails the second without retrying, so neither changes anything.
// Lago handles the first item only (webhooks_controller.rb adyen_params);
// every item is handled here.
func (g *Gateway) ParseWebhook(_ context.Context, _ payment.Connection, payload []byte) ([]payment.Event, error) {
	n, err := decode(payload)
	if err != nil {
		return nil, err
	}
	var out []payment.Event
	for _, it := range n.Items {
		evs, err := parseItem(n.Live == "true", it.Item)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	return out, nil
}

func parseItem(live bool, it item) ([]payment.Event, error) {
	meta := payment.EventMeta{
		ProviderEventID: it.EventCode + ":" + it.PspReference + ":" + it.Success,
		Type:            it.EventCode,
		Livemode:        live,
	}
	if t, err := time.Parse(time.RFC3339, it.EventDate); err == nil {
		meta.OccurredAt = t.UTC()
	}
	switch it.EventCode {
	case EventAuthorisation:
		return authorisation(meta, it)
	case EventCancellation:
		if !it.succeeded() || it.OriginalReference == "" {
			return nil, nil
		}
		return []payment.Event{payment.PaymentEvent{
			EventMeta:         meta,
			ProviderPaymentID: it.OriginalReference,
			ProviderStatus:    statusCancelled,
			Status:            StatusOf(statusCancelled),
		}}, nil
	case EventRefund:
		status := core.RefundStatusFailed
		if it.succeeded() {
			status = core.RefundStatusSucceeded
		}
		return []payment.Event{payment.RefundEvent{
			EventMeta:        meta,
			ProviderRefundID: it.PspReference,
			ProviderStatus:   string(status),
			Status:           status,
		}}, nil
	case EventRefundFailed:
		if !it.succeeded() {
			return nil, nil
		}
		return []payment.Event{payment.RefundEvent{
			EventMeta:        meta,
			ProviderRefundID: it.PspReference,
			ProviderStatus:   string(core.RefundStatusFailed),
			Status:           core.RefundStatusFailed,
		}}, nil
	case EventChargeback:
		status := it.data("disputeStatus")
		if status != disputeLost || !it.succeeded() {
			return nil, nil
		}
		return []payment.Event{payment.DisputeClosed{
			EventMeta:         meta,
			ProviderPaymentID: it.PspReference,
			ProviderStatus:    status,
			Lost:              true,
			Reason:            it.Reason,
			ClosedAt:          meta.OccurredAt,
		}}, nil
	default:
		return nil, nil
	}
}

func authorisation(meta payment.EventMeta, it item) ([]payment.Event, error) {
	md := it.metadata()
	value, _ := it.Amount.Value.Int64()
	if md[metaPaymentType] == paymentTypeOneTime {
		if err := checkPayableType(md); err != nil {
			return nil, err
		}
		status, providerStatus := payment.StateFailed, "failed"
		if it.succeeded() {
			status, providerStatus = payment.StateSucceeded, "succeeded"
		}
		ev := payment.PaymentEvent{
			EventMeta:          meta,
			ProviderPaymentID:  it.PspReference,
			ProviderStatus:     providerStatus,
			Status:             status,
			Amount:             value,
			Currency:           currencyOf(it.Amount.Currency),
			ProviderCustomerID: shopperReference(it),
			ProviderMethodID:   it.data("recurring.recurringDetailReference"),
			OneTime:            true,
			Refs:               refsOf(md),
		}
		if !it.succeeded() {
			ev.ErrorMessage = it.Reason
		}
		return []payment.Event{ev}, nil
	}
	if value != 0 {
		return nil, nil
	}
	shopper := shopperReference(it)
	if shopper == "" {
		// Not a setup link of Tanvo's (lago handle_missing_customer).
		return nil, nil
	}
	refs := refsOf(md)
	if !it.succeeded() {
		return []payment.Event{payment.PaymentMethodSetupFailed{
			EventMeta:          meta,
			ProviderCustomerID: shopper,
			ErrorCode:          it.EventCode,
			ErrorMessage:       it.Reason,
			Refs:               refs,
		}}, nil
	}
	return []payment.Event{payment.PaymentMethodAttached{
		EventMeta:          meta,
		ProviderCustomerID: shopper,
		ProviderMethodID:   it.data("recurring.recurringDetailReference"),
		Details:            cardDetails(it),
		SetAsDefault:       true,
		Refs:               refs,
	}}, nil
}

// shopperReference is the item's shopper (lago adyen_service.rb:111-114).
func shopperReference(it item) string {
	if s := it.data("shopperReference"); s != "" {
		return s
	}
	return it.data("recurring.shopperReference")
}

// cardDetails describes the card an authorisation saved, from its
// additional data; Adyen setup links save cards only, which lago records
// as "card" methods.
func cardDetails(it item) payment.MethodDetails {
	d := payment.MethodDetails{
		Type:  methodCard,
		Brand: it.data("checkout.cardAddedBrand"),
		Last4: it.data("cardSummary"),
	}
	if d.Brand == "" {
		d.Brand = it.PaymentMethod
	}
	if m, y, ok := strings.Cut(it.data("expiryDate"), "/"); ok {
		d.ExpMonth, d.ExpYear = atoi(m), atoi(y)
	}
	return d
}

// checkPayableType rejects a metadata payable type Tanvo does not know
// with [payment.ErrUnknownPayable]: lago raises NameError and updates
// nothing (handle_event_service.rb payment_service_klass,
// handle_event_service_spec.rb:107). An absent type is the invoice, as in
// lago.
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
