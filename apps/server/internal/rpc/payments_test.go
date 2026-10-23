package rpc_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// payMoney is the JSON of a Money.
type payMoney struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// restPayment is the JSON of a payment in a REST response.
type restPayment struct {
	ID               string   `json:"id"`
	PayableType      string   `json:"payable_type"`
	InvoiceID        string   `json:"invoice_id"`
	PaymentRequestID string   `json:"payment_request_id"`
	InvoiceIDs       []string `json:"invoice_ids"`
	CustomerID       string   `json:"customer_id"`
	Kind             string   `json:"kind"`
	Status           string   `json:"status"`
	Amount           payMoney `json:"amount"`
	Reference        string   `json:"reference"`
	PaymentReceiptID string   `json:"payment_receipt_id"`
	PaidAt           string   `json:"paid_at"`
	CreatedAt        string   `json:"created_at"`
}

// payFinalized makes a stored invoice finalized with a total and a paid
// amount, ready for payment processing.
func payFinalized(total, paid int64) func(*invoice.Invoice) {
	return func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusFinalized
		inv.TotalAmount, inv.TotalPaidAmount = total, paid
		inv.ReadyForPaymentProcessing = true
	}
}

// payDecode decodes the payment of a response.
func payDecode(t *testing.T, body []byte) restPayment {
	t.Helper()
	return decode[struct {
		Payment restPayment `json:"payment"`
	}](t, body).Payment
}

// payIDs returns the IDs of the payments of a list response.
func payIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, p := range decode[struct {
		Payments []restPayment `json:"payments"`
	}](t, body).Payments {
		out = append(out, p.ID)
	}
	return out
}

func TestCreatePaymentRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	inv := s.Fixtures.StoredInvoice(cust, payFinalized(10000, 0))
	draft := s.Fixtures.StoredInvoice(cust, func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
	tests := []struct {
		name   string
		source string
		body   string
		status int
		reason string
	}{
		{
			"records a manual payment", "spec/requests/api/v1/payments_controller_spec.rb:38",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"1.00","reference":"ref1","paid_at":"2025-03-10T12:00:00Z"}`, inv.ID), http.StatusOK, "",
		},
		{
			"missing amount", "spec/requests/api/v1/payments_controller_spec.rb:60",
			fmt.Sprintf(`{"invoice_id":%q,"amount_in_cents":"100","reference":"ref1"}`, inv.ID), http.StatusBadRequest, "",
		},
		{
			"zero amount", "spec/services/payments/manual_create_service_spec.rb:78",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"0","reference":"ref1"}`, inv.ID), http.StatusBadRequest, "validation_errors",
		},
		{
			"missing reference", "spec/services/payments/manual_create_service_spec.rb:90",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"1.00"}`, inv.ID), http.StatusBadRequest, "",
		},
		{
			"unknown invoice", "spec/services/payments/manual_create_service_spec.rb:30",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"1.00","reference":"ref1"}`, core.NewID[core.InvoiceID](core.UUIDv7Generator{})),
			http.StatusNotFound, "invoice_not_found",
		},
		{
			"draft invoice", "spec/services/payments/manual_create_service_spec.rb:41",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"1.00","reference":"ref1"}`, draft.ID), http.StatusBadRequest, "forbidden",
		},
		{
			"more than due", "spec/services/payments/manual_create_service_spec.rb:67",
			fmt.Sprintf(`{"invoice_id":%q,"amount":"1000","reference":"ref1"}`, inv.ID), http.StatusBadRequest, "validation_errors",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/payments", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.status != http.StatusOK {
				if tt.reason != "" {
					if got := couponReason(t, body); got != tt.reason {
						t.Errorf("got reason = %q, want = %q", got, tt.reason)
					}
				}
				return
			}
			p := payDecode(t, body)
			if p.Kind != "PAYMENT_KIND_MANUAL" || p.Status != "PAYMENT_STATUS_SUCCEEDED" || p.Amount != (payMoney{"USD", "1.00"}) ||
				p.Reference != "ref1" || p.PayableType != "PAYABLE_TYPE_INVOICE" || p.InvoiceID != inv.ID.String() {
				t.Errorf("got %+v, want a succeeded manual payment of 1.00 USD on %s", p, inv.ID)
			}
			if len(p.InvoiceIDs) != 1 || p.InvoiceIDs[0] != inv.ID.String() || p.PaymentReceiptID == "" || p.PaidAt != "2025-03-10T12:00:00Z" {
				t.Errorf("got invoices = %v, receipt = %q, paid at = %q, want = [%s], a receipt, 2025-03-10T12:00:00Z",
					p.InvoiceIDs, p.PaymentReceiptID, p.PaidAt, inv.ID)
			}
			if got := s.Fixtures.Invoice(inv.ID).TotalPaidAmount; got != 100 {
				t.Errorf("got total paid = %d, want = 100", got)
			}
		})
	}
}

func TestGetPaymentRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	inv := s.Fixtures.StoredInvoice(cust, payFinalized(10000, 0))
	direct := s.Fixtures.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	q := s.Fixtures.StoredPaymentRequest(cust.ID, core.PaymentStatusPending, inv)
	viaRequest := s.Fixtures.StoredPayment(payment.RequestPayable(q.ID), cust.ID, 100, payment.StatePending, core.IntegrationID{})
	hidden := s.Fixtures.StoredPayment(payment.InvoicePayable(s.Fixtures.StoredInvoice(cust).ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"payment of an invoice", "spec/requests/api/v1/payments_controller_spec.rb:132", direct.ID.String(), http.StatusOK},
		{"payment of a payment request", "spec/requests/api/v1/payments_controller_spec.rb:169", viaRequest.ID.String(), http.StatusOK},
		{"unknown", "spec/requests/api/v1/payments_controller_spec.rb:180", core.NewID[core.PaymentID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
		{"payment of a generating invoice", "", hidden.ID.String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/payments/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			p := payDecode(t, body)
			if p.ID != tt.id || len(p.InvoiceIDs) != 1 || p.InvoiceIDs[0] != inv.ID.String() || p.CustomerID != cust.ID.String() {
				t.Errorf("got %+v, want payment %s of invoice %s", p, tt.id, inv.ID)
			}
		})
	}
}

// TestListPaymentsRPC ports the shared examples "a payment index
// endpoint" on both routes.
func TestListPaymentsRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust, other := s.Fixtures.Customer(), s.Fixtures.Customer()
	inv := s.Fixtures.StoredInvoice(cust, payFinalized(10000, 0))
	p1 := s.Fixtures.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	s.Clock.Advance(time.Second)
	q := s.Fixtures.StoredPaymentRequest(cust.ID, core.PaymentStatusPending, inv)
	p2 := s.Fixtures.StoredPayment(payment.RequestPayable(q.ID), cust.ID, 100, payment.StatePending, core.IntegrationID{})
	s.Clock.Advance(time.Second)
	otherInv := s.Fixtures.StoredInvoice(other, payFinalized(100, 0))
	p3 := s.Fixtures.StoredPayment(payment.InvoicePayable(otherInv.ID), other.ID, 100, payment.StateFailed, core.IntegrationID{})
	s.Fixtures.StoredPayment(payment.InvoicePayable(s.Fixtures.StoredInvoice(cust).ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	ext := "external_id%3A" + cust.ExternalID
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   []string
	}{
		{"all", "spec/support/shared_examples/payment_index.rb:11", "/v1/payments", http.StatusOK, []string{p3.ID.String(), p2.ID.String(), p1.ID.String()}},
		{"customer", "spec/requests/api/v1/payments_controller_spec.rb:88", "/v1/payments?filter=customer_id%3D%22" + ext + "%22", http.StatusOK, []string{p2.ID.String(), p1.ID.String()}},
		{"customer route", "spec/requests/api/v1/customers/payments_controller_spec.rb:7", "/v1/customers/" + ext + "/payments", http.StatusOK, []string{p2.ID.String(), p1.ID.String()}},
		{"invoice", "spec/support/shared_examples/payment_index.rb:40", "/v1/payments?filter=" + fmt.Sprintf("invoice_id%%3D%q", inv.ID.String()), http.StatusOK, []string{p2.ID.String(), p1.ID.String()}},
		{
			"invoice of another customer", "spec/requests/api/v1/customers/payments_controller_spec.rb:29",
			"/v1/customers/" + ext + "/payments?filter=" + fmt.Sprintf("invoice_id%%3D%q", otherInv.ID.String()), http.StatusOK, nil,
		},
		{"unknown customer", "spec/queries/payments_query_spec.rb:200", "/v1/payments?filter=customer_id%3D%22external_id%3Aunknown%22", http.StatusOK, nil},
		{"unknown customer field", "", "/v1/payments?customer_id=external_id%3Aunknown", http.StatusNotFound, nil},
		{"customer route filtered on another", "", "/v1/customers/" + ext + "/payments?filter=" + fmt.Sprintf("customer_id%%3D%q", other.ID.String()), http.StatusOK, nil},
		{"unknown customer route", "spec/requests/api/v1/customers/payments_controller_spec.rb:13", "/v1/customers/external_id%3Aunknown/payments", http.StatusNotFound, nil},
		{"currency", "spec/queries/payments_query_spec.rb:209", "/v1/payments?filter=currency%3DUSD", http.StatusOK, []string{p3.ID.String(), p2.ID.String(), p1.ID.String()}},
		{"no currency match", "spec/queries/payments_query_spec.rb:220", "/v1/payments?filter=currency%3DGBP", http.StatusOK, nil},
		{"unknown currency", "", "/v1/payments?filter=currency%3DXXX1", http.StatusBadRequest, nil},
		{"search", "spec/queries/payments_query_spec.rb:125", "/v1/payments?search=" + cust.ExternalID, http.StatusOK, []string{p2.ID.String(), p1.ID.String()}},
		{"search id", "spec/queries/payments_query_spec.rb:67", "/v1/payments?search=" + p3.ID.String(), http.StatusOK, []string{p3.ID.String()}},
		{"invalid invoice", "spec/queries/payments_query_spec.rb:229", "/v1/payments?filter=invoice_id%3D%22invalid-uuid%22", http.StatusBadRequest, nil},
		{"customer code", "", "/v1/payments?filter=customer_id%3D%22code%3Ax%22", http.StatusBadRequest, nil},
		{"unreleased filter", "", "/v1/payments?filter=kind%3DMANUAL", http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := s.REST(t, s.APIKey, http.MethodGet, tt.path, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			if got := payIDs(t, body); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got payments = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestListRefundsRPCUnimplemented(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	if status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/refunds", nil); status != http.StatusNotImplemented {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusNotImplemented)
	}
}
