package rpc_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	analyticsv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1/analyticsv1connect"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

func analyticsClient(s *rpctest.Server, c rpctest.Credential) analyticsv1connect.AnalyticsServiceClient {
	return analyticsv1connect.NewAnalyticsServiceClient(s.Connect(c))
}

// analyticsInvoice stores a finalized EUR invoice of c, issued on issued
// and due on due, after edit.
func analyticsInvoice(t *testing.T, s *rpctest.Server, c *customer.Customer, issued time.Time, total int64, edit ...func(*invoice.Invoice)) *invoice.Invoice {
	t.Helper()
	inv := &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](core.UUIDv7Generator{}), OrganizationID: s.Org.ID, CustomerID: c.ID,
		BillingEntityID: c.BillingEntityID, Type: invoice.TypeOneOff, Status: invoice.StatusFinalized,
		PaymentStatus: core.PaymentStatusPending, Currency: money.MustCurrency("EUR"), TotalAmount: total,
		IssuingDate: core.DateOf(issued, time.UTC), CreatedAt: issued, UpdatedAt: issued,
	}
	for _, f := range edit {
		f(inv)
	}
	if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		return r.Invoices.Create(ctx, inv)
	}); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return inv
}

func scope(edit func(*analyticsv1.AnalyticsScope_builder)) *analyticsv1.AnalyticsScope {
	b := analyticsv1.AnalyticsScope_builder{}
	if edit != nil {
		edit(&b)
	}
	return b.Build()
}

func TestGetGrossRevenue(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := analyticsClient(s, s.APIKey)
	t.Run("empty", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/analytics/gross_revenues_controller_spec.rb:21")
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/analytics/gross_revenue", nil)
		if status != http.StatusOK {
			t.Fatalf("status: got = %d (%s), want = 200", status, body)
		}
		got := decode[struct {
			Points []any `json:"points"`
		}](t, body)
		if len(got.Points) != 0 {
			t.Errorf("points: got = %v, want = none", got.Points)
		}
	})
	cust := s.Fixtures.Customer()
	analyticsInvoice(t, s, cust, rpctest.Now.AddDate(0, 0, -10), 1000)
	analyticsInvoice(t, s, cust, rpctest.Now.AddDate(0, 0, -9), 2000)
	t.Run("invoices", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/analytics/gross_revenues_controller_spec.rb:28")
		res, err := c.GetGrossRevenue(t.Context(), analyticsv1.GetGrossRevenueRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetGrossRevenue() error got = %v, want = nil", err)
		}
		p := res.GetPoints()
		if len(p) != 1 || p[0].GetMonth().GetMonth() != 3 || p[0].GetMonth().GetDay() != 1 ||
			p[0].GetAmount().GetAmount() != "30.00" || p[0].GetAmount().GetCurrency() != "EUR" || p[0].GetInvoicesCount() != 2 ||
			p[0].GetBillingEntityId() != cust.BillingEntityID.String() {
			t.Errorf("points: got = %v, want = one point of 30.00 EUR, 2 invoices, in March, for the customer's billing entity", p)
		}
	})
	t.Run("billing entity code and lowercase currency", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/analytics/gross_revenues_controller_spec.rb:55")
		res, err := c.GetGrossRevenue(t.Context(), analyticsv1.GetGrossRevenueRequest_builder{Scope: scope(func(b *analyticsv1.AnalyticsScope_builder) {
			b.BillingEntityId = new("code:" + s.Fixtures.BillingEntity.Code)
			b.Currency = new("EUR")
		})}.Build())
		if err != nil || len(res.GetPoints()) != 1 {
			t.Errorf("GetGrossRevenue() got = %v, %v, want = one point", res.GetPoints(), err)
		}
	})
	t.Run("unknown billing entity", func(t *testing.T) {
		_, err := c.GetGrossRevenue(t.Context(), analyticsv1.GetGrossRevenueRequest_builder{Scope: scope(func(b *analyticsv1.AnalyticsScope_builder) {
			b.BillingEntityId = new("code:unknown")
		})}.Build())
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("error: got = %v, want = NotFound", err)
		}
	})
	t.Run("customer", func(t *testing.T) {
		t.Log("source: app/controllers/api/v1/analytics/gross_revenues_controller.rb:17")
		res, err := c.GetGrossRevenue(t.Context(), analyticsv1.GetGrossRevenueRequest_builder{
			CustomerId: new("external_id:" + cust.ExternalID),
		}.Build())
		if err != nil || len(res.GetPoints()) != 1 {
			t.Errorf("GetGrossRevenue() got = %v, %v, want = one point", res.GetPoints(), err)
		}
		res, err = c.GetGrossRevenue(t.Context(), analyticsv1.GetGrossRevenueRequest_builder{
			CustomerId: new("external_id:unknown"),
		}.Build())
		if err != nil || len(res.GetPoints()) != 0 {
			t.Errorf("unknown customer: got = %v, %v, want = no point", res.GetPoints(), err)
		}
	})
}

func TestGetMrr(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := analyticsClient(s, s.APIKey)
	t.Run("empty month", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/analytics/mrrs_controller_spec.rb:21")
		res, err := c.GetMrr(t.Context(), analyticsv1.GetMrrRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetMrr() error got = %v, want = nil", err)
		}
		if p := res.GetPoints(); len(p) != 1 || p[0].HasAmount() || p[0].GetMonth().GetMonth() != 3 {
			t.Errorf("points: got = %v, want = March without amount", p)
		}
	})
}

func TestGetInvoiceCollection(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := analyticsClient(s, s.APIKey)
	t.Run("empty month", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/analytics/invoice_collections_controller_spec.rb:21")
		res, err := c.GetInvoiceCollection(t.Context(), analyticsv1.GetInvoiceCollectionRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetInvoiceCollection() error got = %v, want = nil", err)
		}
		p := res.GetPoints()
		if len(p) != 1 || p[0].GetPaymentStatus() != commonv1.PaymentStatus_PAYMENT_STATUS_UNSPECIFIED || p[0].GetInvoicesCount() != 0 || p[0].HasAmount() {
			t.Errorf("points: got = %v, want = one empty point", p)
		}
	})
	t.Run("invoices", func(t *testing.T) {
		analyticsInvoice(t, s, s.Fixtures.Customer(), rpctest.Now.AddDate(0, 0, -3), 1250, func(i *invoice.Invoice) {
			i.PaymentStatus = core.PaymentStatusSucceeded
		})
		res, err := c.GetInvoiceCollection(t.Context(), analyticsv1.GetInvoiceCollectionRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetInvoiceCollection() error got = %v, want = nil", err)
		}
		p := res.GetPoints()
		if len(p) != 1 || p[0].GetPaymentStatus() != commonv1.PaymentStatus_PAYMENT_STATUS_SUCCEEDED || p[0].GetInvoicesCount() != 1 ||
			p[0].GetAmount().GetAmount() != "12.50" {
			t.Errorf("points: got = %v, want = one succeeded invoice of 12.50", p)
		}
	})
	t.Run("customer tin", func(t *testing.T) {
		t.Log("source: spec/models/analytics/invoice_collection_spec.rb:142-174")
		for _, tt := range []struct {
			empty bool
			want  int64
		}{{true, 1}, {false, 0}} {
			res, err := c.GetInvoiceCollection(t.Context(), analyticsv1.GetInvoiceCollectionRequest_builder{IsCustomerTinEmpty: new(tt.empty)}.Build())
			if err != nil {
				t.Fatalf("GetInvoiceCollection() error got = %v, want = nil", err)
			}
			if p := res.GetPoints(); len(p) != 1 || p[0].GetInvoicesCount() != tt.want {
				t.Errorf("is_customer_tin_empty = %v: got = %v, want = %d invoices", tt.empty, p, tt.want)
			}
		}
	})
}

func TestGetInvoicedUsage(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Log("source: spec/requests/api/v1/analytics/invoiced_usages_controller_spec.rb:21")
	res, err := analyticsClient(s, s.APIKey).GetInvoicedUsage(t.Context(), analyticsv1.GetInvoicedUsageRequest_builder{}.Build())
	if err != nil || len(res.GetPoints()) != 0 {
		t.Errorf("GetInvoicedUsage() got = %v, %v, want = no point", res.GetPoints(), err)
	}
}

func TestGetOverdueBalance(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Log("source: spec/requests/api/v1/analytics/overdue_balances_controller_spec.rb:20")
	cust := s.Fixtures.Customer()
	analyticsInvoice(t, s, cust, rpctest.Now, 500)
	var ids []string
	for _, total := range []int64{2000, 3000} {
		inv := analyticsInvoice(t, s, cust, rpctest.Now.AddDate(0, 0, -5), total, func(i *invoice.Invoice) {
			i.PaymentOverdue = true
			i.PaymentDueDate = core.DateOf(rpctest.Now.AddDate(0, 0, -5+int(total/1000)), time.UTC)
		})
		ids = append(ids, inv.ID.String())
	}
	res, err := analyticsClient(s, s.APIKey).GetOverdueBalance(t.Context(), analyticsv1.GetOverdueBalanceRequest_builder{}.Build())
	if err != nil {
		t.Fatalf("GetOverdueBalance() error got = %v, want = nil", err)
	}
	p := res.GetPoints()
	if len(p) != 1 || p[0].GetAmount().GetAmount() != "50.00" || len(p[0].GetInvoiceIds()) != 2 ||
		p[0].GetInvoiceIds()[0] != ids[0] || p[0].GetInvoiceIds()[1] != ids[1] || p[0].GetBillingEntityId() != cust.BillingEntityID.String() {
		t.Errorf("points: got = %v, want = 50.00 over %v for the customer's billing entity", p, ids)
	}
	for _, tt := range []struct {
		source string
		req    *analyticsv1.GetOverdueBalanceRequest
		want   int
	}{
		{
			"app/controllers/api/v1/analytics/overdue_balances_controller.rb:17",
			analyticsv1.GetOverdueBalanceRequest_builder{CustomerId: new("external_id:" + cust.ExternalID)}.Build(), 1,
		},
		{
			"spec/models/analytics/overdue_balance_spec.rb:339-352",
			analyticsv1.GetOverdueBalanceRequest_builder{CustomerId: new("external_id:unknown")}.Build(), 0,
		},
		{
			"spec/models/analytics/overdue_balance_spec.rb:318-326",
			analyticsv1.GetOverdueBalanceRequest_builder{IsCustomerTinEmpty: new(true)}.Build(), 1,
		},
		{
			"spec/models/analytics/overdue_balance_spec.rb:328-336",
			analyticsv1.GetOverdueBalanceRequest_builder{IsCustomerTinEmpty: new(false)}.Build(), 0,
		},
	} {
		t.Logf("source: %s", tt.source)
		res, err := analyticsClient(s, s.APIKey).GetOverdueBalance(t.Context(), tt.req)
		if err != nil || len(res.GetPoints()) != tt.want {
			t.Errorf("GetOverdueBalance(%v) got = %v, %v, want = %d points", tt.req, res.GetPoints(), err, tt.want)
		}
	}
}
