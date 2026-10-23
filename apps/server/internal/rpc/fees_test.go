package rpc_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restFee is the JSON of a fee in a REST response.
type restFee struct {
	ID                 string  `json:"id"`
	InvoiceID          string  `json:"invoice_id"`
	CustomerID         string  `json:"customer_id"`
	SubscriptionID     string  `json:"subscription_id"`
	FeeType            string  `json:"fee_type"`
	PaymentStatus      string  `json:"payment_status"`
	EventTransactionID string  `json:"event_transaction_id"`
	InvoiceDisplayName string  `json:"invoice_display_name"`
	Adjusted           bool    `json:"adjusted"`
	Amount             cnMoney `json:"amount"`
	SucceededAt        string  `json:"succeeded_at"`
}

// feeFixture is a customer with a subscription, an unattached paid in
// advance fee of it and an invoice with one fee.
type feeFixture struct {
	s          *rpctest.Server
	cust       *customer.Customer
	sub        *subscription.Subscription
	unattached *invoice.Fee
	inv        *invoice.Invoice
	invoiced   *invoice.Fee
}

func newFeeFixture(t *testing.T) *feeFixture {
	t.Helper()
	s := rpctest.New(t)
	h := s.Fixtures
	f := &feeFixture{s: s, cust: h.Customer()}
	f.sub = h.Subscription(f.cust, h.Plan(period.Monthly, 1000))
	f.inv = h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
	f.invoiced = h.StoredFee(f.inv, 100, billingtest.SubscriptionFee(f.sub))
	f.unattached = &invoice.Fee{
		ID: core.NewID[core.FeeID](h.IDs), OrganizationID: h.OrgID(), SubscriptionID: f.sub.ID, Type: core.FeeTypeCharge,
		Amount: 100, Currency: billingtest.USD, PayInAdvance: true, Invoiceable: true,
		PaymentStatus: invoice.FeePaymentStatusPending, PayInAdvanceEventTransactionID: "transaction-id",
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Fees.Create(ctx, f.unattached) })
	return f
}

func feeDecode(t *testing.T, body []byte) restFee {
	t.Helper()
	return decode[struct {
		Fee restFee `json:"fee"`
	}](t, body).Fee
}

func TestGetFee(t *testing.T) {
	t.Parallel()
	f := newFeeFixture(t)
	other := rpctest.New(t)
	tests := []struct {
		name, source string
		s            *rpctest.Server
		id           string
		status       int
	}{
		{"returns a fee", "spec/requests/api/v1/fees_controller_spec.rb:18", f.s, f.unattached.ID.String(), http.StatusOK},
		{"invoiced fee", "spec/requests/api/v1/fees_controller_spec.rb:40", f.s, f.invoiced.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/fees_controller_spec.rb:67", f.s, core.NewID[core.FeeID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
		{"other organization", "spec/requests/api/v1/fees_controller_spec.rb:76", other, f.unattached.ID.String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := tt.s.REST(t, tt.s.APIKey, http.MethodGet, "/v1/fees/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("status got = %d %s, want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			fee := feeDecode(t, body)
			if fee.ID != tt.id || fee.CustomerID != f.cust.ID.String() || fee.SubscriptionID != f.sub.ID.String() {
				t.Errorf("fee got = %+v, want = %s of customer %s", fee, tt.id, f.cust.ID)
			}
		})
	}
}

func TestUpdateFee(t *testing.T) {
	t.Parallel()
	f := newFeeFixture(t)
	body := `{"payment_status":"FEE_PAYMENT_STATUS_SUCCEEDED"}`
	tests := []struct {
		name, source string
		id           string
		status       int
		want         string
	}{
		{"not found", "spec/requests/api/v1/fees_controller_spec.rb:127", core.NewID[core.FeeID](core.UUIDv7Generator{}).String(), http.StatusNotFound, "fee_not_found"},
		{"invoiced fee", "spec/services/fees/update_service_spec.rb:39", f.invoiced.ID.String(), http.StatusBadRequest, invoicing.CodeInvoicedFee},
		{"updates the fee", "spec/requests/api/v1/fees_controller_spec.rb:100", f.unattached.ID.String(), http.StatusOK, `"payment_status":"FEE_PAYMENT_STATUS_SUCCEEDED"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, out := f.s.REST(t, f.s.APIKey, http.MethodPatch, "/v1/fees/"+tt.id, body)
			if status != tt.status || !strings.Contains(string(out), tt.want) {
				t.Fatalf("got = %d %s, want = %d with %q", status, out, tt.status, tt.want)
			}
			if status == http.StatusOK {
				if fee := feeDecode(t, out); fee.SucceededAt == "" {
					t.Errorf("succeeded_at got = empty, want = set")
				}
			}
		})
	}
}

func TestDeleteFee(t *testing.T) {
	t.Parallel()
	f := newFeeFixture(t)
	tests := []struct {
		name, source string
		id           string
		status       int
		want         string
	}{
		{"attached to an invoice", "spec/requests/api/v1/fees_controller_spec.rb:160", f.invoiced.ID.String(), http.StatusBadRequest, invoicing.CodeInvoicedFee},
		{"deletes the fee", "spec/requests/api/v1/fees_controller_spec.rb:153", f.unattached.ID.String(), http.StatusOK, `"id":"` + f.unattached.ID.String() + `"`},
		{"not found", "spec/requests/api/v1/fees_controller_spec.rb:170", f.unattached.ID.String(), http.StatusNotFound, "fee_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, out := f.s.REST(t, f.s.APIKey, http.MethodDelete, "/v1/fees/"+tt.id, nil)
			if status != tt.status || !strings.Contains(string(out), tt.want) {
				t.Fatalf("got = %d %s, want = %d with %q", status, out, tt.status, tt.want)
			}
		})
	}
	if status, _ := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/fees/"+f.invoiced.ID.String(), nil); status != http.StatusOK {
		t.Errorf("invoiced fee status got = %d, want = %d", status, http.StatusOK)
	}
	// A discarded fee is neither read nor listed (app/models/fee.rb:9).
	if status, _ := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/fees/"+f.unattached.ID.String(), nil); status != http.StatusNotFound {
		t.Errorf("discarded fee status got = %d, want = %d", status, http.StatusNotFound)
	}
	if status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/fees", nil); status != http.StatusOK || strings.Contains(string(body), f.unattached.ID.String()) {
		t.Errorf("fees got = %d %s, want = without %s", status, body, f.unattached.ID)
	}
}

func TestListFees(t *testing.T) {
	t.Parallel()
	f := newFeeFixture(t)
	tests := []struct {
		name, source string
		query        string
		status       int
		want         []string
	}{
		{"without params", "spec/requests/api/v1/fees_controller_spec.rb:187", "", http.StatusOK, []string{f.invoiced.ID.String(), f.unattached.ID.String()}},
		{"fee type", "spec/queries/fees_query_spec.rb:122", `fee_type = FEE_TYPE_CHARGE`, http.StatusOK, []string{f.unattached.ID.String()}},
		{"event transaction", "spec/queries/fees_query_spec.rb:168", `event_transaction_id = "transaction-id"`, http.StatusOK, []string{f.unattached.ID.String()}},
		{"customer external id", "spec/queries/fees_query_spec.rb:67", `customer_id = "external_id:` + f.cust.ExternalID + `"`, http.StatusOK, []string{f.invoiced.ID.String(), f.unattached.ID.String()}},
		{"subscription external id", "spec/queries/fees_query_spec.rb:56", `subscription_id = "external_id:` + f.sub.ExternalID + `"`, http.StatusOK, []string{f.invoiced.ID.String(), f.unattached.ID.String()}},
		{"payment status", "spec/queries/fees_query_spec.rb:145", `payment_status = FEE_PAYMENT_STATUS_SUCCEEDED`, http.StatusOK, nil},
		{
			"created at", "spec/queries/fees_query_spec.rb:211", fmt.Sprintf(`created_at >= %q AND created_at <= %q`,
				rpctest.Now.Add(-time.Minute).Format(time.RFC3339), rpctest.Now.Add(time.Minute).Format(time.RFC3339)), http.StatusOK,
			[]string{f.invoiced.ID.String(), f.unattached.ID.String()},
		},
		{"created at not covered", "spec/queries/fees_query_spec.rb:226", fmt.Sprintf(`created_at <= %q`,
			rpctest.Now.Add(-2*time.Minute).Format(time.RFC3339)), http.StatusOK, nil},
		{"invalid fee type", "spec/requests/api/v1/fees_controller_spec.rb:202", `fee_type = FOO`, http.StatusBadRequest, nil},
		{"invalid date", "spec/queries/fees_query_spec.rb:242", `created_at >= "invalid_date_value"`, http.StatusBadRequest, nil},
		{"pay in advance", "", `pay_in_advance = true`, http.StatusBadRequest, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			path := "/v1/fees"
			if tt.query != "" {
				path += "?filter=" + url.QueryEscape(tt.query)
			}
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, path, nil)
			if status != tt.status {
				t.Fatalf("status got = %d %s, want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			var got []string
			for _, fee := range decode[struct {
				Fees []restFee `json:"fees"`
			}](t, body).Fees {
				got = append(got, fee.ID)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("fees got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestAdjustFee(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	h.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	cust := h.Customer()
	start := time.Date(rpctest.Now.Year(), rpctest.Now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sub := h.Subscription(cust, h.Plan(period.Monthly, 1000), billingtest.SubscribedAt(start.AddDate(0, -1, 0)))
	invs := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Invoice, error) {
		return s.Billing.Invoices.Bill(ctx, r, invoicing.BillRequest{
			OrganizationID: h.OrgID(), BillingAt: start,
			Items: []invoicing.BillItem{{SubscriptionID: sub.ID, Reason: invoice.InvoicingReasonSubscriptionPeriodic}},
		})
	})
	if len(invs) != 1 || h.Invoice(invs[0].ID).Status != invoice.StatusDraft {
		t.Fatalf("invoices got = %d, want = one draft", len(invs))
	}
	draft := invs[0]
	fee := h.Fees(draft.ID)[0]

	tests := []struct {
		name, source string
		path, body   string
		status       int
		want         string
	}{
		{
			"invoice not found", "spec/services/adjusted_fees/create_service_spec.rb:125",
			"/v1/invoices/" + core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String() + "/fees:adjust",
			fmt.Sprintf(`{"fee_id":%q,"mode":"FEE_ADJUSTMENT_MODE_DISPLAY_NAME","invoice_display_name":"Base"}`, fee.ID),
			http.StatusNotFound, "invoice_not_found",
		},
		{
			"no target", "", "/v1/invoices/" + draft.ID.String() + "/fees:adjust",
			`{"mode":"FEE_ADJUSTMENT_MODE_DISPLAY_NAME","invoice_display_name":"Base"}`, http.StatusBadRequest, "",
		},
		{
			"adjusts the display name", "spec/services/adjusted_fees/create_service_spec.rb:88",
			"/v1/invoices/" + draft.ID.String() + "/fees:adjust",
			fmt.Sprintf(`{"fee_id":%q,"mode":"FEE_ADJUSTMENT_MODE_DISPLAY_NAME","invoice_display_name":"Base"}`, fee.ID),
			http.StatusOK, `"invoice_display_name":"Base"`,
		},
		{
			"already adjusted", "spec/services/adjusted_fees/create_service_spec.rb:137",
			"/v1/invoices/" + draft.ID.String() + "/fees:adjust",
			// The adjustment refreshed the draft: its fee is the regenerated one.
			fmt.Sprintf(`{"subscription_id":%q,"fee_id":"{current_fee}","mode":"FEE_ADJUSTMENT_MODE_DISPLAY_NAME","invoice_display_name":"Other"}`, sub.ID),
			http.StatusBadRequest, core.CodeAlreadyExists,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			req := strings.ReplaceAll(tt.body, "{current_fee}", h.Fees(draft.ID)[0].ID.String())
			status, body := s.REST(t, s.APIKey, http.MethodPost, tt.path, req)
			if status != tt.status || !strings.Contains(string(body), tt.want) {
				t.Fatalf("got = %d %s, want = %d with %q", status, body, tt.status, tt.want)
			}
			if status == http.StatusOK && !feeDecode(t, body).Adjusted {
				t.Errorf("adjusted got = false, want = true")
			}
		})
	}

	// The adjusted fee was regenerated: remove its adjustment by its new ID.
	adjusted := h.Fees(draft.ID)[0]
	// source: spec/services/adjusted_fees/destroy_service_spec.rb:22
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/fees/"+adjusted.ID.String()+":delete_adjustment", "{}")
	if status != http.StatusOK || feeDecode(t, body).Adjusted || strings.Contains(string(body), `"invoice_display_name":"Base"`) {
		t.Fatalf("DeleteFeeAdjustment() got = %d %s, want = 200 and an unadjusted fee", status, body)
	}
	// source: spec/services/adjusted_fees/destroy_service_spec.rb:32
	status, body = s.REST(t, s.APIKey, http.MethodPost, "/v1/fees/"+h.Fees(draft.ID)[0].ID.String()+":delete_adjustment", "{}")
	if status != http.StatusNotFound || !strings.Contains(string(body), "adjusted_fee_not_found") {
		t.Errorf("DeleteFeeAdjustment() again got = %d %s, want = 404 adjusted_fee_not_found", status, body)
	}
}
