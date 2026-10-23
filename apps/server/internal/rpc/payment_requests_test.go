package rpc_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// restPaymentRequest is the JSON of a payment request in a REST response.
type restPaymentRequest struct {
	ID                string   `json:"id"`
	CustomerID        string   `json:"customer_id"`
	InvoiceIDs        []string `json:"invoice_ids"`
	Amount            payMoney `json:"amount"`
	Email             string   `json:"email"`
	PaymentStatus     string   `json:"payment_status"`
	PaymentMethodType string   `json:"payment_method_type"`
	PaymentMethodID   string   `json:"payment_method_id"`
}

// preqFixture is a customer with two overdue invoices, 4.00 left to pay.
type preqFixture struct {
	s             *rpctest.Server
	cust          *customer.Customer
	first, second *invoice.Invoice
}

func newPreqFixture(t *testing.T) *preqFixture {
	t.Helper()
	s := rpctest.New(t)
	f := &preqFixture{s: s, cust: s.Fixtures.Customer()}
	overdue := func(inv *invoice.Invoice) { inv.PaymentOverdue = true }
	f.first = s.Fixtures.StoredInvoice(f.cust, payFinalized(200, 100), overdue)
	f.second = s.Fixtures.StoredInvoice(f.cust, payFinalized(500, 200), overdue)
	return f
}

func preqDecode(t *testing.T, body []byte) restPaymentRequest {
	t.Helper()
	return decode[struct {
		PaymentRequest restPaymentRequest `json:"payment_request"`
	}](t, body).PaymentRequest
}

func TestCreatePaymentRequestRPC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   func(f *preqFixture) string
		status int
		reason string
	}{
		{
			"creates a payment request", "spec/requests/api/v1/payment_requests_controller_spec.rb:37",
			func(f *preqFixture) string {
				return fmt.Sprintf(`{"customer_id":"external_id:%s","email":"john.doe@example.com","invoice_ids":[%q,%q]}`,
					f.cust.ExternalID, f.first.ID, f.second.ID)
			}, http.StatusOK, "",
		},
		{
			"with a manual payment method", "spec/requests/api/v1/payment_requests_controller_spec.rb:69",
			func(f *preqFixture) string {
				return fmt.Sprintf(`{"customer_id":%q,"invoice_ids":[%q],"payment_method_type":"PAYMENT_METHOD_TYPE_MANUAL"}`, f.cust.ID, f.first.ID)
			}, http.StatusOK, "",
		},
		{
			"unknown customer", "spec/services/payment_requests/create_service_spec.rb:50",
			func(f *preqFixture) string {
				return fmt.Sprintf(`{"customer_id":"external_id:unknown","invoice_ids":[%q]}`, f.first.ID)
			}, http.StatusNotFound, "customer_not_found",
		},
		{
			"no invoices", "spec/services/payment_requests/create_service_spec.rb:62",
			func(f *preqFixture) string { return fmt.Sprintf(`{"customer_id":%q,"invoice_ids":[]}`, f.cust.ID) },
			http.StatusBadRequest, "",
		},
		{
			"unknown invoice", "spec/services/payment_requests/create_service_spec.rb:62",
			func(f *preqFixture) string {
				return fmt.Sprintf(`{"customer_id":%q,"invoice_ids":[%q]}`, f.cust.ID, core.NewID[core.InvoiceID](core.UUIDv7Generator{}))
			}, http.StatusNotFound, "invoice_not_found",
		},
		{
			"manual payment method with an id", "spec/services/payment_requests/create_service_spec.rb:192",
			func(f *preqFixture) string {
				return fmt.Sprintf(`{"customer_id":%q,"invoice_ids":[%q],"payment_method_type":"PAYMENT_METHOD_TYPE_MANUAL","payment_method_id":%q}`,
					f.cust.ID, f.first.ID, core.NewID[core.PaymentMethodID](core.UUIDv7Generator{}))
			}, http.StatusBadRequest, "validation_errors",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newPreqFixture(t)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/payment_requests", tt.body(f))
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				if tt.reason != "" {
					if got := couponReason(t, body); got != tt.reason {
						t.Errorf("got reason = %q, want = %q", got, tt.reason)
					}
				}
				return
			}
			q := preqDecode(t, body)
			if q.ID == "" || q.CustomerID != f.cust.ID.String() || q.PaymentStatus != "PAYMENT_STATUS_PENDING" {
				t.Errorf("got %+v, want a pending request of %s", q, f.cust.ID)
			}
			switch len(q.InvoiceIDs) {
			case 2:
				if q.Amount != (payMoney{"USD", "4.00"}) || q.Email != "john.doe@example.com" {
					t.Errorf("got amount = %v, email = %q, want = 4.00 USD, john.doe@example.com", q.Amount, q.Email)
				}
			case 1:
				if q.Amount != (payMoney{"USD", "1.00"}) || q.PaymentMethodType != "PAYMENT_METHOD_TYPE_MANUAL" {
					t.Errorf("got amount = %v, method = %q, want = 1.00 USD, manual", q.Amount, q.PaymentMethodType)
				}
			default:
				t.Errorf("got invoices = %v, want one or two", q.InvoiceIDs)
			}
			if got := f.s.Webhooks(t); len(got) == 0 || got[len(got)-1] != "payment_request.created" {
				t.Errorf("got webhooks = %v, want payment_request.created last", got)
			}
		})
	}
}

func TestCreatePaymentRequestRulesRPC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		setup  func(inv *invoice.Invoice)
		reason string
	}{
		{"not overdue", "spec/services/payment_requests/create_service_spec.rb:89", func(inv *invoice.Invoice) { inv.PaymentOverdue = false }, "invoices_not_overdue"},
		{"not ready", "spec/services/payment_requests/create_service_spec.rb:127", func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false }, "invoices_not_ready_for_payment_processing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newPreqFixture(t)
			tt.setup(f.first)
			f.s.Fixtures.SaveInvoice(f.first)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/payment_requests",
				fmt.Sprintf(`{"customer_id":%q,"invoice_ids":[%q,%q]}`, f.cust.ID, f.first.ID, f.second.ID))
			if status != http.StatusBadRequest {
				t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusBadRequest)
			}
			if got := couponReason(t, body); got != tt.reason {
				t.Errorf("got reason = %q, want = %q", got, tt.reason)
			}
		})
	}
}

func TestGetPaymentRequestRPC(t *testing.T) {
	t.Parallel()
	f := newPreqFixture(t)
	q := f.s.Fixtures.StoredPaymentRequest(f.cust.ID, core.PaymentStatusPending, f.first)
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"returns the payment request", "spec/requests/api/v1/payment_requests_controller_spec.rb:188", q.ID.String(), http.StatusOK},
		{"unknown", "spec/requests/api/v1/payment_requests_controller_spec.rb:199", core.NewID[core.PaymentRequestID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/payment_requests/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status == http.StatusOK {
				if got := preqDecode(t, body); got.ID != q.ID.String() || len(got.InvoiceIDs) != 1 || got.InvoiceIDs[0] != f.first.ID.String() {
					t.Errorf("got %+v, want request %s of invoice %s", got, q.ID, f.first.ID)
				}
			}
		})
	}
}

func TestListPaymentRequestsRPC(t *testing.T) {
	t.Parallel()
	f := newPreqFixture(t)
	us := organization.NewBillingEntity(f.s.Fixtures.OrgID(), core.NewID[core.BillingEntityID](f.s.Fixtures.IDs), "US", "US", f.s.Fixtures.Now())
	f.s.Fixtures.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, us) })
	eu := f.s.Fixtures.BillingEntity.Code
	q1 := f.s.Fixtures.StoredPaymentRequest(f.cust.ID, core.PaymentStatusPending, f.first)
	f.s.Fixtures.Clock.Advance(time.Second)
	other := f.s.Fixtures.Customer()
	otherInv := f.s.Fixtures.StoredInvoice(other, payFinalized(100, 0), func(inv *invoice.Invoice) { inv.BillingEntityID = us.ID })
	q2 := f.s.Fixtures.StoredPaymentRequest(other.ID, core.PaymentStatusSucceeded, otherInv)
	ext := "external_id%3A" + f.cust.ExternalID
	be := func(codes ...string) string {
		var terms []string
		for _, c := range codes {
			terms = append(terms, fmt.Sprintf("billing_entity_id%%3D%%22code%%3A%s%%22", c))
		}
		return "/v1/payment_requests?filter=" + strings.Join(terms, "%20OR%20")
	}
	tests := []struct {
		name   string
		source string
		path   string
		status int
		want   []string
	}{
		{"all", "spec/requests/api/v1/payment_requests_controller_spec.rb:90", "/v1/payment_requests", http.StatusOK, []string{q2.ID.String(), q1.ID.String()}},
		{"customer", "spec/requests/api/v1/payment_requests_controller_spec.rb:101", "/v1/payment_requests?filter=customer_id%3D%22" + ext + "%22", http.StatusOK, []string{q1.ID.String()}},
		{"customer route", "spec/requests/api/v1/customers/payment_requests_controller_spec.rb:6", "/v1/customers/" + ext + "/payment_requests", http.StatusOK, []string{q1.ID.String()}},
		{"unknown customer", "spec/requests/api/v1/payment_requests_controller_spec.rb:118", "/v1/payment_requests?filter=customer_id%3D%22external_id%3Aunknown%22", http.StatusOK, nil},
		{"unknown customer field", "", "/v1/payment_requests?customer_id=external_id%3Aunknown", http.StatusNotFound, nil},
		{"unknown customer route", "spec/requests/api/v1/customers/payment_requests_controller_spec.rb:15", "/v1/customers/external_id%3Aunknown/payment_requests", http.StatusNotFound, nil},
		{"billing entity", "spec/requests/api/v1/payment_requests_controller_spec.rb:146", be(eu), http.StatusOK, []string{q1.ID.String()}},
		{"billing entities", "spec/requests/api/v1/payment_requests_controller_spec.rb:153", be(eu, "US"), http.StatusOK, []string{q2.ID.String(), q1.ID.String()}},
		{"unknown billing entity", "spec/requests/api/v1/payment_requests_controller_spec.rb:166", be(eu, "BOGUS"), http.StatusNotFound, nil},
		{"billing entity by ID", "", "/v1/payment_requests?filter=" + fmt.Sprintf("billing_entity_id%%3D%q", us.ID.String()), http.StatusOK, []string{q2.ID.String()}},
		{"currency", "spec/queries/payment_requests_query_spec.rb:79", "/v1/payment_requests?filter=currency%3DUSD", http.StatusOK, []string{q2.ID.String(), q1.ID.String()}},
		{"no currency match", "spec/queries/payment_requests_query_spec.rb:89", "/v1/payment_requests?filter=currency%3DGBP", http.StatusOK, nil},
		{"payment status", "", "/v1/payment_requests?filter=payment_status%3DSUCCEEDED", http.StatusOK, []string{q2.ID.String()}},
		{"processing payment status", "", "/v1/payment_requests?filter=payment_status%3DPROCESSING", http.StatusBadRequest, nil},
		{"unreleased filter", "", "/v1/payment_requests?filter=dunning_campaign_id%3D%22" + q1.ID.String() + "%22", http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, tt.path, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			var got []string
			for _, q := range decode[struct {
				PaymentRequests []restPaymentRequest `json:"payment_requests"`
			}](t, body).PaymentRequests {
				got = append(got, q.ID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got requests = %v, want = %v", got, tt.want)
			}
		})
	}
}
