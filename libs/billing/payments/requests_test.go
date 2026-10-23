package payments_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// requestFixture is a customer with two overdue invoices: 200 with 100
// paid and 500 with 200 paid, 400 left to pay.
type requestFixture struct {
	h             *billingtest.Harness
	svc           *payments.Service
	cust          *customer.Customer
	first, second *invoice.Invoice
}

func newRequestFixture(t *testing.T) *requestFixture {
	t.Helper()
	h, svc := newHarness(t)
	f := &requestFixture{h: h, svc: svc, cust: h.Customer()}
	overdue := func(inv *invoice.Invoice) { inv.PaymentOverdue = true }
	f.first = h.StoredInvoice(f.cust, finalized(200, 100), overdue)
	f.second = h.StoredInvoice(f.cust, finalized(500, 200), overdue)
	return f
}

func (f *requestFixture) input() payments.RequestInput {
	email := "john.doe@example.com"
	return payments.RequestInput{
		OrganizationID: f.h.OrgID(), Customer: payments.CustomerRef{ExternalID: f.cust.ExternalID},
		InvoiceIDs: []core.InvoiceID{f.first.ID, f.second.ID}, Email: &email,
	}
}

func (f *requestFixture) update(inv *invoice.Invoice, fn func(*invoice.Invoice)) {
	fn(inv)
	f.h.SaveInvoice(inv)
}

func TestCreateRequestRejects(t *testing.T) {
	cases := []struct {
		name, source string
		setup        func(f *requestFixture, in *payments.RequestInput)
		notFound     string
		rule         string
	}{
		{
			"missing customer", "spec/services/payment_requests/create_service_spec.rb:50",
			func(_ *requestFixture, in *payments.RequestInput) { in.Customer.ExternalID = "non-existing-id" }, "customer", "",
		},
		{
			"no invoices", "spec/services/payment_requests/create_service_spec.rb:62",
			func(_ *requestFixture, in *payments.RequestInput) { in.InvoiceIDs = nil }, "invoice", "",
		},
		{
			"invoices still generating", "spec/services/payment_requests/create_service_spec.rb:77",
			func(f *requestFixture, _ *payments.RequestInput) {
				for _, inv := range []*invoice.Invoice{f.first, f.second} {
					f.update(inv, func(inv *invoice.Invoice) { inv.Status = invoice.StatusGenerating })
				}
			}, "invoice", "",
		},
		{
			"invoice of another customer", "",
			func(f *requestFixture, in *payments.RequestInput) {
				other := f.h.StoredInvoice(f.h.Customer(), finalized(100, 0))
				in.InvoiceIDs = []core.InvoiceID{other.ID}
			}, "invoice", "",
		},
		{
			"not overdue", "spec/services/payment_requests/create_service_spec.rb:89",
			func(f *requestFixture, _ *payments.RequestInput) {
				f.update(f.first, func(inv *invoice.Invoice) { inv.PaymentOverdue = false })
			}, "", payment.CodeInvoicesNotOverdue,
		},
		{
			"different currencies", "spec/services/payment_requests/create_service_spec.rb:101",
			func(f *requestFixture, _ *payments.RequestInput) {
				f.update(f.second, func(inv *invoice.Invoice) { inv.Currency = money.MustCurrency("EUR") })
			}, "", payment.CodeInvoicesDifferentCurrencies,
		},
		{
			"different billing entities", "spec/services/payment_requests/create_service_spec.rb:115",
			func(f *requestFixture, _ *payments.RequestInput) {
				f.update(f.second, func(inv *invoice.Invoice) { inv.BillingEntityID = core.NewID[core.BillingEntityID](f.h.IDs) })
			}, "", payment.CodeInvoicesDifferentBillingEntities,
		},
		{
			"not ready for payment processing", "spec/services/payment_requests/create_service_spec.rb:127",
			func(f *requestFixture, _ *payments.RequestInput) {
				f.update(f.first, func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false })
			}, "", payment.CodeInvoicesNotReady,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.source != "" {
				t.Log("source: " + c.source)
			}
			f := newRequestFixture(t)
			in := f.input()
			c.setup(f, &in)
			_, err := f.svc.CreateRequest(f.h.Ctx, in)
			if c.notFound != "" {
				wantNotFound(t, err, c.notFound)
			} else {
				wantRule(t, err, c.rule)
			}
		})
	}
}

func TestCreateRequestPaymentMethod(t *testing.T) {
	cases := []struct {
		name, source string
		typ          core.PaymentMethodType
		method       func(f *requestFixture) core.PaymentMethodID
		valid        bool
	}{
		{
			"method of the customer", "spec/services/payment_requests/create_service_spec.rb:146", core.PaymentMethodTypeProvider,
			func(f *requestFixture) core.PaymentMethodID { return storedMethod(f.h, f.cust, false).ID }, true,
		},
		{
			"manual", "spec/services/payment_requests/create_service_spec.rb:161", core.PaymentMethodTypeManual,
			func(*requestFixture) core.PaymentMethodID { return core.PaymentMethodID{} }, true,
		},
		{
			"manual with an id", "spec/services/payment_requests/create_service_spec.rb:192", core.PaymentMethodTypeManual,
			func(f *requestFixture) core.PaymentMethodID { return storedMethod(f.h, f.cust, false).ID }, false,
		},
		{
			"missing method", "spec/services/payment_requests/create_service_spec.rb:206", core.PaymentMethodTypeProvider,
			func(f *requestFixture) core.PaymentMethodID { return core.NewID[core.PaymentMethodID](f.h.IDs) }, false,
		},
		{
			"method of another customer", "spec/services/payment_requests/create_service_spec.rb:226", core.PaymentMethodTypeProvider,
			func(f *requestFixture) core.PaymentMethodID { return storedMethod(f.h, f.h.Customer(), false).ID }, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source: " + c.source)
			f := newRequestFixture(t)
			in := f.input()
			in.PaymentMethodType, in.PaymentMethodID = c.typ, c.method(f)
			q, err := f.svc.CreateRequest(f.h.Ctx, in)
			if !c.valid {
				wantFields(t, err, "payment_method:"+payment.CodeInvalidPaymentMethod)
				if hasWebhook(f.h, webhook.EventTypePaymentRequestCreated) {
					t.Error("webhook: got = payment_request.created, want = none")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if q.PaymentMethodType != c.typ || q.PaymentMethodID != in.PaymentMethodID {
				t.Errorf("payment method: got = %s %s, want = %s %s", q.PaymentMethodType, q.PaymentMethodID, c.typ, in.PaymentMethodID)
			}
		})
	}
}

func TestCreateRequest(t *testing.T) {
	t.Log("source: spec/services/payment_requests/create_service_spec.rb:244")
	t.Log("source: spec/services/payment_requests/create_service_spec.rb:248")
	t.Log("source: spec/services/payment_requests/create_service_spec.rb:254")
	t.Log("source: spec/services/payment_requests/create_service_spec.rb:277")
	f := newRequestFixture(t)
	in := f.input()
	in.DunningCampaignID = core.NewID[core.DunningCampaignID](f.h.IDs)
	q, err := f.svc.CreateRequest(f.h.Ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if q.CustomerID != f.cust.ID || q.Amount != 400 || q.Currency.Code() != "USD" || q.DunningCampaignID != in.DunningCampaignID {
		t.Errorf("request: got = %s %d %s %s, want = %s 400 USD %s", q.CustomerID, q.Amount, q.Currency.Code(), q.DunningCampaignID, f.cust.ID, in.DunningCampaignID)
	}
	if q.Email == nil || *q.Email != "john.doe@example.com" || q.PaymentStatus != core.PaymentStatusPending {
		t.Errorf("request: got = %v %s, want = john.doe@example.com pending", q.Email, q.PaymentStatus)
	}
	if len(q.InvoiceIDs) != 2 {
		t.Errorf("invoices: got = %d, want = 2", len(q.InvoiceIDs))
	}
	if !hasWebhook(f.h, webhook.EventTypePaymentRequestCreated) {
		t.Errorf("webhooks: got = %v, want = payment_request.created", f.h.Webhooks())
	}
	got, err := f.svc.GetRequest(f.h.Ctx, f.h.OrgID(), q.ID)
	if err != nil || got.Amount != 400 {
		t.Fatalf("get: got = %v %v, want = the request", got, err)
	}
}

func TestCreateRequestDefaultsToCustomerEmail(t *testing.T) {
	f := newRequestFixture(t)
	email := "billing@example.com"
	f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Email = &email })
	in := f.input()
	in.Email = nil
	q, err := f.svc.CreateRequest(f.h.Ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Email == nil || *q.Email != email {
		t.Errorf("email: got = %v, want = %s", q.Email, email)
	}
}

func TestCreateRequestOffsets(t *testing.T) {
	type note struct {
		second bool
		offset int64
		credit int64
		draft  bool
	}
	cases := []struct {
		name, source string
		firstPaid    int64
		notes        []note
		want         int64
	}{
		{"finalized offsets", "spec/services/payment_requests/create_service_spec.rb:293", 100, []note{{false, 50, 0, false}, {true, 100, 0, false}}, 250},
		{"draft offsets ignored", "spec/services/payment_requests/create_service_spec.rb:304", 100, []note{{false, 50, 0, true}}, 400},
		{"finalized and draft", "spec/services/payment_requests/create_service_spec.rb:312", 100, []note{{false, 30, 0, false}, {true, 80, 0, true}}, 370},
		{"fully offset invoice", "spec/services/payment_requests/create_service_spec.rb:322", 0, []note{{false, 200, 0, false}}, 300},
		{"offset only, not credit", "spec/services/payment_requests/create_service_spec.rb:331", 100, []note{{false, 25, 25, false}}, 375},
		{"several offsets of one invoice", "spec/services/payment_requests/create_service_spec.rb:339", 100, []note{{false, 20, 0, false}, {false, 30, 0, false}}, 350},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source: " + c.source)
			f := newRequestFixture(t)
			f.update(f.first, func(inv *invoice.Invoice) { inv.TotalPaidAmount = c.firstPaid })
			for _, n := range c.notes {
				inv := f.first
				if n.second {
					inv = f.second
				}
				f.h.StoredCreditNote(inv, n.credit, func(cn *creditnote.CreditNote) {
					cn.OffsetAmount, cn.TotalAmount = n.offset, n.offset+n.credit
					if n.draft {
						cn.Status = creditnote.StatusDraft
					}
				})
			}
			q, err := f.svc.CreateRequest(f.h.Ctx, f.input())
			if err != nil {
				t.Fatal(err)
			}
			if q.Amount != c.want {
				t.Errorf("amount: got = %d, want = %d", q.Amount, c.want)
			}
		})
	}
}

func TestListRequests(t *testing.T) {
	f := newRequestFixture(t)
	first, err := f.svc.CreateRequest(f.h.Ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	f.h.Clock.Advance(time.Second)
	other := f.h.StoredPaymentRequest(f.h.Customer().ID, core.PaymentStatusSucceeded)
	page, err := f.svc.ListRequests(f.h.Ctx, f.h.OrgID(), payments.RequestListInput{}, core.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != other.ID || page.Items[1].ID != first.ID {
		t.Fatalf("requests: got = %d items, want = newest first", len(page.Items))
	}
	page, err = f.svc.ListRequests(f.h.Ctx, f.h.OrgID(), payments.RequestListInput{
		FilterCustomer: payments.CustomerRef{ExternalID: f.cust.ExternalID},
	}, core.PageRequest{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("customer requests: got = %v %v, want = the first", page.Items, err)
	}
	page, err = f.svc.ListRequests(f.h.Ctx, f.h.OrgID(), payments.RequestListInput{
		Filter: payment.RequestFilter{PaymentStatus: core.PaymentStatusSucceeded},
	}, core.PageRequest{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != other.ID {
		t.Fatalf("succeeded requests: got = %v %v, want = the other", page.Items, err)
	}
	page, err = f.svc.ListRequests(f.h.Ctx, f.h.OrgID(), payments.RequestListInput{
		FilterCustomer: payments.CustomerRef{ExternalID: "unknown"},
	}, core.PageRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unknown customer: got = %v %v, want = none", page.Items, err)
	}
}

func TestListRequestsFilters(t *testing.T) {
	h, svc := newHarness(t)
	eu := h.BillingEntity
	us := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "US", "US", h.Now())
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, us) })
	custEU, custUS := h.Customer(), h.Customer()
	invEU := h.StoredInvoice(custEU, finalized(100, 0))
	invEU2 := h.StoredInvoice(custEU, finalized(100, 0))
	invUS := h.StoredInvoice(custUS, finalized(100, 0), func(inv *invoice.Invoice) {
		inv.BillingEntityID = us.ID
		inv.Currency = money.MustCurrency("EUR")
	})
	qEU := h.StoredPaymentRequest(custEU.ID, core.PaymentStatusPending, invEU, invEU2)
	h.Clock.Advance(time.Second)
	qUS := h.StoredPaymentRequest(custUS.ID, core.PaymentStatusPending, invUS)
	qUS.Currency = money.MustCurrency("EUR")
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Update(ctx, qUS) })
	cases := []struct {
		name, source string
		in           payments.RequestListInput
		want         []core.PaymentRequestID
	}{
		{"currency", "spec/queries/payment_requests_query_spec.rb:79", payments.RequestListInput{Filter: payment.RequestFilter{Currency: money.MustCurrency("EUR")}}, []core.PaymentRequestID{qUS.ID}},
		{"no currency match", "spec/queries/payment_requests_query_spec.rb:89", payments.RequestListInput{Filter: payment.RequestFilter{Currency: money.MustCurrency("GBP")}}, nil},
		{"billing entity", "spec/queries/payment_requests_query_spec.rb:118", payments.RequestListInput{BillingEntities: []refs.Ref{{Code: eu.Code}}}, []core.PaymentRequestID{qEU.ID}},
		{"billing entities", "spec/queries/payment_requests_query_spec.rb:123", payments.RequestListInput{BillingEntities: []refs.Ref{{Code: eu.Code}, {ID: us.ID.UUID()}}}, []core.PaymentRequestID{qUS.ID, qEU.ID}},
		{"no billing entity match", "spec/queries/payment_requests_query_spec.rb:152", payments.RequestListInput{Filter: payment.RequestFilter{BillingEntityIDs: []core.BillingEntityID{core.NewID[core.BillingEntityID](h.IDs)}}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s", c.source)
			page, err := svc.ListRequests(h.Ctx, h.OrgID(), c.in, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			var got []core.PaymentRequestID
			for _, q := range page.Items {
				got = append(got, q.ID)
			}
			if len(got) != len(c.want) {
				t.Fatalf("requests: got = %v, want = %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("requests: got = %v, want = %v", got, c.want)
				}
			}
		})
	}
	t.Run("unknown billing entity", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/payment_requests_controller_spec.rb:163")
		_, err := svc.ListRequests(h.Ctx, h.OrgID(), payments.RequestListInput{
			BillingEntities: []refs.Ref{{Code: eu.Code}, {Code: "BOGUS"}},
		}, core.PageRequest{})
		wantNotFound(t, err, "billing_entity")
	})
	t.Run("unknown customer of the customer route", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/customers/payment_requests_controller_spec.rb:15")
		_, err := svc.ListRequests(h.Ctx, h.OrgID(), payments.RequestListInput{
			Customer: payments.CustomerRef{ExternalID: "unknown"},
		}, core.PageRequest{})
		wantNotFound(t, err, "customer")
	})
}
