package rpc_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// restPaymentReceipt is the JSON of a receipt in a REST response.
type restPaymentReceipt struct {
	ID              string      `json:"id"`
	Number          string      `json:"number"`
	Payment         restPayment `json:"payment"`
	BillingEntityID string      `json:"billing_entity_id"`
	FileAvailable   bool        `json:"file_available"`
}

// prcptPay records a manual payment of a new invoice of cust through the
// API and returns the payment, whose receipt is issued with it.
func prcptPay(t *testing.T, s *rpctest.Server) (restPayment, string) {
	t.Helper()
	cust := s.Fixtures.Customer()
	inv := s.Fixtures.StoredInvoice(cust, payFinalized(10000, 0))
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/payments",
		fmt.Sprintf(`{"invoice_id":%q,"amount":"100","reference":"ref"}`, inv.ID))
	if status != http.StatusOK {
		t.Fatalf("pay: got status = %d (%s), want = 200", status, body)
	}
	return payDecode(t, body), inv.ID.String()
}

func TestGetPaymentReceiptRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	p, inv := prcptPay(t, s)
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"returns the receipt", "spec/requests/api/v1/payment_receipts_controller_spec.rb:92", p.PaymentReceiptID, http.StatusOK},
		{"unknown", "spec/requests/api/v1/payment_receipts_controller_spec.rb:119", core.NewID[core.PaymentReceiptID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/payment_receipts/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			rc := decode[struct {
				PaymentReceipt restPaymentReceipt `json:"payment_receipt"`
			}](t, body).PaymentReceipt
			if rc.ID != p.PaymentReceiptID || rc.Payment.ID != p.ID || len(rc.Payment.InvoiceIDs) != 1 || rc.Payment.InvoiceIDs[0] != inv {
				t.Errorf("got %+v, want receipt %s of payment %s", rc, p.PaymentReceiptID, p.ID)
			}
			if rc.Number == "" || rc.BillingEntityID != s.Org.ID.String() && rc.BillingEntityID == "" || rc.FileAvailable {
				t.Errorf("got number = %q, billing entity = %q, file = %t, want a number, an entity, no file", rc.Number, rc.BillingEntityID, rc.FileAvailable)
			}
		})
	}
}

func TestListPaymentReceiptsRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	p1, inv1 := prcptPay(t, s)
	s.Clock.Advance(time.Second)
	p2, _ := prcptPay(t, s)
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   []string
	}{
		{"all", "spec/requests/api/v1/payment_receipts_controller_spec.rb:15", "/v1/payment_receipts", http.StatusOK, []string{p2.PaymentReceiptID, p1.PaymentReceiptID}},
		{
			"unknown invoice", "spec/requests/api/v1/payment_receipts_controller_spec.rb:48",
			"/v1/payment_receipts?filter=" + fmt.Sprintf("invoice_id%%3D%q", core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String()), http.StatusOK, nil,
		},
		{
			"invoice", "spec/requests/api/v1/payment_receipts_controller_spec.rb:70",
			"/v1/payment_receipts?filter=" + fmt.Sprintf("invoice_id%%3D%q", inv1), http.StatusOK,
			[]string{p1.PaymentReceiptID},
		},
		{"unreleased filter", "", "/v1/payment_receipts?filter=" + fmt.Sprintf("payment_id%%3D%q", p1.ID), http.StatusBadRequest, nil},
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
			var got []string
			for _, rc := range decode[struct {
				PaymentReceipts []restPaymentReceipt `json:"payment_receipts"`
			}](t, body).PaymentReceipts {
				got = append(got, rc.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got receipts = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestDownloadPaymentReceiptRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	p, _ := prcptPay(t, s)
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/payment_receipts/"+p.PaymentReceiptID+":download", `{}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := decode[map[string]any](t, body); got["pdf"] != nil || got["xml"] != nil {
		t.Errorf("got %v, want no document", got)
	}
	status, body = s.REST(t, s.APIKey, http.MethodPost, "/v1/payment_receipts/"+core.NewID[core.PaymentReceiptID](core.UUIDv7Generator{}).String()+":download", `{}`)
	if status != http.StatusNotFound {
		t.Fatalf("unknown: got status = %d (%s), want = 404", status, body)
	}
}
