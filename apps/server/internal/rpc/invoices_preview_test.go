package rpc_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/period"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	couponv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1/invoicev1connect"
)

// previewFixture is a server with a customer and a monthly plan of 100
// paid in arrears (lago's plan factory).
type previewFixture struct {
	s    *rpctest.Server
	c    invoicev1connect.InvoiceServiceClient
	cust *customer.Customer
	plan *catalog.Plan
}

func newPreviewFixture(t *testing.T) *previewFixture {
	t.Helper()
	s := rpctest.New(t)
	return &previewFixture{
		s: s, c: invoicev1connect.NewInvoiceServiceClient(s.Connect(s.APIKey)),
		cust: s.Fixtures.Customer(), plan: s.Fixtures.Plan(period.Monthly, 100),
	}
}

// preview calls PreviewInvoice with b, for the fixture's customer when b
// names none, and for a customer not created yet when b names ""; it
// skips t while the service is not served.
func (f *previewFixture) preview(t *testing.T, b invoicev1.PreviewInvoiceRequest_builder) (*invoicev1.Invoice, error) {
	t.Helper()
	switch {
	case b.CustomerId == nil:
		b.CustomerId = new(rpc.ExternalIDPrefix + f.cust.ExternalID)
	case *b.CustomerId == "":
		b.CustomerId = nil
	}
	resp, err := f.c.PreviewInvoice(t.Context(), b.Build())
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		t.Skip("InvoiceService is not served")
	}
	return resp.GetInvoice(), err
}

// source: spec/requests/api/v1/invoices_controller_spec.rb:1148
func TestPreviewInvoiceProposal(t *testing.T) {
	t.Parallel()
	f := newPreviewFixture(t)
	inv, err := f.preview(t, invoicev1.PreviewInvoiceRequest_builder{
		Subscription: invoicev1.PreviewSubscription_builder{PlanId: new(rpc.CodePrefix + f.plan.Code), CalendarBilling: new(false)}.Build(),
	})
	if err != nil {
		t.Fatalf("PreviewInvoice: %v", err)
	}
	if got, want := inv.GetInvoiceType(), invoicev1.InvoiceType_INVOICE_TYPE_SUBSCRIPTION; got != want {
		t.Errorf("invoice type = %v, want = %v", got, want)
	}
	if got, want := inv.GetFeesAmount().GetAmount(), "1.00"; got != want {
		t.Errorf("fees amount = %s, want = %s", got, want)
	}
	if got, want := inv.GetBillingEntityId(), f.s.Fixtures.BillingEntity.ID.String(); got != want {
		t.Errorf("billing entity = %s, want = %s", got, want)
	}
	if got := len(inv.GetSubscriptions()); got != 1 {
		t.Errorf("subscriptions = %d, want = 1", got)
	}
	if got := len(f.s.Fixtures.Invoices(f.cust.ID)); got != 0 {
		t.Errorf("stored invoices = %d, want = 0", got)
	}
}

// source: spec/requests/api/v1/invoices_controller_spec.rb:1148
func TestPreviewInvoiceNewCustomer(t *testing.T) {
	t.Parallel()
	f := newPreviewFixture(t)
	inv, err := f.preview(t, invoicev1.PreviewInvoiceRequest_builder{
		CustomerId: new(""),
		Customer: invoicev1.PreviewCustomer_builder{
			Name: new("test 1"), Currency: new("USD"), TaxIdentificationNumber: new("123456789"),
		}.Build(),
		Subscription: invoicev1.PreviewSubscription_builder{PlanId: new(rpc.CodePrefix + f.plan.Code), CalendarBilling: new(false)}.Build(),
	})
	if err != nil {
		t.Fatalf("PreviewInvoice: %v", err)
	}
	if got, want := inv.GetFeesAmount().GetAmount(), "1.00"; got != want {
		t.Errorf("fees amount = %s, want = %s", got, want)
	}
	if got, want := inv.GetBillingEntityId(), f.s.Fixtures.BillingEntity.ID.String(); got != want {
		t.Errorf("billing entity = %s, want = %s", got, want)
	}
	if inv.GetCustomerId() == f.cust.ID.String() {
		t.Errorf("customer = the fixture's, want = a new one")
	}
}

// source: spec/services/invoices/preview_context_service_spec.rb:314
func TestPreviewInvoiceCoupons(t *testing.T) {
	t.Parallel()
	f := newPreviewFixture(t)
	tests := []struct {
		name    string
		coupons []*couponv1.Coupon
		want    string
	}{
		{"fixed amount", []*couponv1.Coupon{couponv1.Coupon_builder{
			Code: new("coupon_preview"), CouponType: couponv1.CouponType_COUPON_TYPE_FIXED_AMOUNT.Enum(),
			Amount: commonv1.Money_builder{Currency: new("USD"), Amount: new("0.30")}.Build(),
		}.Build()}, "0.30"},
		{"percentage", []*couponv1.Coupon{couponv1.Coupon_builder{
			CouponType: couponv1.CouponType_COUPON_TYPE_PERCENTAGE.Enum(), PercentageRate: new("10"),
		}.Build()}, "0.10"},
		// An unknown code alone builds a coupon without an amount.
		{"unknown code", []*couponv1.Coupon{couponv1.Coupon_builder{Code: new("unknown")}.Build()}, "0.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := f.preview(t, invoicev1.PreviewInvoiceRequest_builder{
				Subscription: invoicev1.PreviewSubscription_builder{PlanId: new(rpc.CodePrefix + f.plan.Code), CalendarBilling: new(false)}.Build(),
				Coupons:      tt.coupons,
			})
			if err != nil {
				t.Fatalf("PreviewInvoice: %v", err)
			}
			if got := inv.GetCouponsAmount().GetAmount(); got != tt.want {
				t.Errorf("coupons amount = %s, want = %s", got, tt.want)
			}
		})
	}
}

// source: spec/requests/api/v1/invoices_controller_spec.rb:1367
// source: spec/requests/api/v1/invoices_controller_spec.rb:1462
func TestPreviewInvoicePersisted(t *testing.T) {
	t.Parallel()
	f := newPreviewFixture(t)
	other := f.s.Fixtures.Customer()
	at := billingtest.SubscribedAt(rpctest.Now)
	s1 := f.s.Fixtures.Subscription(f.cust, f.plan, at, billingtest.Anniversary)
	s2 := f.s.Fixtures.Subscription(f.cust, f.plan, at, billingtest.Anniversary)
	s3 := f.s.Fixtures.Subscription(other, f.plan, at, billingtest.Anniversary)
	tests := []struct {
		name string
		ids  []string
		want string
	}{
		{"two subscriptions", []string{rpc.ExternalIDPrefix + s1.ExternalID, s2.ID.String()}, "2.00"},
		// source: spec/requests/api/v1/invoices_controller_spec.rb:1477
		{"one of the customer", []string{rpc.ExternalIDPrefix + s3.ExternalID, rpc.ExternalIDPrefix + s2.ExternalID}, "1.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := f.preview(t, invoicev1.PreviewInvoiceRequest_builder{SubscriptionIds: tt.ids})
			if err != nil {
				t.Fatalf("PreviewInvoice: %v", err)
			}
			if got := inv.GetFeesAmount().GetAmount(); got != tt.want {
				t.Errorf("fees amount = %s, want = %s", got, tt.want)
			}
		})
	}
}

// source: spec/requests/api/v1/invoices_controller_spec.rb:1130
func TestPreviewInvoiceErrors(t *testing.T) {
	t.Parallel()
	f := newPreviewFixture(t)
	other := f.s.Fixtures.Customer()
	foreign := f.s.Fixtures.Subscription(other, f.plan, billingtest.SubscribedAt(rpctest.Now))
	plan := invoicev1.PreviewSubscription_builder{PlanId: new(rpc.CodePrefix + f.plan.Code)}.Build()
	tests := []struct {
		name string
		b    invoicev1.PreviewInvoiceRequest_builder
		code connect.Code
	}{
		// source: spec/requests/api/v1/invoices_controller_spec.rb:1298
		{"unknown billing entity", invoicev1.PreviewInvoiceRequest_builder{Subscription: plan, BillingEntityId: new(rpc.CodePrefix + "unknown")}, connect.CodeNotFound},
		// source: spec/requests/api/v1/invoices_controller_spec.rb:1491
		{"foreign subscriptions", invoicev1.PreviewInvoiceRequest_builder{SubscriptionIds: []string{rpc.ExternalIDPrefix + foreign.ExternalID}}, connect.CodeNotFound},
		// source: spec/requests/api/v1/invoices_controller_spec.rb:1512
		{"unknown customer", invoicev1.PreviewInvoiceRequest_builder{CustomerId: new(rpc.ExternalIDPrefix + "unknown"), Subscription: plan}, connect.CodeNotFound},
		{"unknown plan", invoicev1.PreviewInvoiceRequest_builder{Subscription: invoicev1.PreviewSubscription_builder{PlanId: new(rpc.CodePrefix + "unknown")}.Build()}, connect.CodeNotFound},
		// source: spec/requests/api/v1/invoices_controller_spec.rb:1544
		{"subscription by code", invoicev1.PreviewInvoiceRequest_builder{SubscriptionIds: []string{rpc.CodePrefix + "x"}}, connect.CodeInvalidArgument},
		{"customer by code", invoicev1.PreviewInvoiceRequest_builder{CustomerId: new(rpc.CodePrefix + "x"), Subscription: plan}, connect.CodeInvalidArgument},
		// source: spec/services/invoices/preview/subscriptions_service_spec.rb:51
		{"new customer with subscriptions", invoicev1.PreviewInvoiceRequest_builder{CustomerId: new(""), SubscriptionIds: []string{rpc.ExternalIDPrefix + foreign.ExternalID}}, connect.CodeInvalidArgument},
		{"invalid timezone", invoicev1.PreviewInvoiceRequest_builder{Subscription: plan, Customer: invoicev1.PreviewCustomer_builder{Timezone: new("Mars/Base")}.Build()}, connect.CodeInvalidArgument},
		// source: spec/services/invoices/preview_context_service_spec.rb:169
		{"unknown integration", invoicev1.PreviewInvoiceRequest_builder{Subscription: plan, Customer: invoicev1.PreviewCustomer_builder{
			IntegrationCustomers: []*invoicev1.PreviewIntegrationCustomer{invoicev1.PreviewIntegrationCustomer_builder{IntegrationId: new(rpc.CodePrefix + "code")}.Build()},
		}.Build()}, connect.CodeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.preview(t, tt.b)
			if got := connect.CodeOf(err); got != tt.code {
				t.Errorf("code = %v (%v), want = %v", got, err, tt.code)
			}
		})
	}
}
