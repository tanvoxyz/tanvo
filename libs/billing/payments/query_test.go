package payments_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestGetPayment(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer()
	inv := h.StoredInvoice(cust, finalized(10000, 0))
	p := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	got, err := svc.Get(h.Ctx, h.OrgID(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.InvoiceIDs) != 1 || got.InvoiceIDs[0] != inv.ID || !got.ReceiptID.IsZero() {
		t.Errorf("detail: got = %v %s, want = [%s] no receipt", got.InvoiceIDs, got.ReceiptID, inv.ID)
	}

	hidden := h.StoredInvoice(cust)
	hp := h.StoredPayment(payment.InvoicePayable(hidden.ID), cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	_, err = svc.Get(h.Ctx, h.OrgID(), hp.ID)
	wantNotFound(t, err, "payment")
	_, err = svc.Get(h.Ctx, h.OrgID(), core.NewID[core.PaymentID](h.IDs))
	wantNotFound(t, err, "payment")
}

func TestListPayments(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer()
	inv := h.StoredInvoice(cust, finalized(10000, 0))
	direct := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	h.Clock.Advance(time.Second)
	q := h.StoredPaymentRequest(cust.ID, core.PaymentStatusPending, inv)
	viaRequest := h.StoredPayment(payment.RequestPayable(q.ID), cust.ID, 1000, payment.StatePending, core.IntegrationID{})
	hidden := h.StoredInvoice(cust)
	h.StoredPayment(payment.InvoicePayable(hidden.ID), cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	h.Clock.Advance(time.Second)
	otherCust := h.Customer()
	other := h.StoredPayment(payment.InvoicePayable(h.StoredInvoice(otherCust, finalized(100, 0)).ID), otherCust.ID, 100, payment.StateFailed, core.IntegrationID{})

	cases := []struct {
		name string
		in   payments.ListInput
		want []core.PaymentID
	}{
		{"all visible, newest first", payments.ListInput{}, []core.PaymentID{other.ID, viaRequest.ID, direct.ID}},
		{"customer", payments.ListInput{FilterCustomer: payments.CustomerRef{ExternalID: cust.ExternalID}}, []core.PaymentID{viaRequest.ID, direct.ID}},
		{"unknown customer", payments.ListInput{FilterCustomer: payments.CustomerRef{ExternalID: "unknown"}}, nil},
		{"customer's own", payments.ListInput{Customer: payments.CustomerRef{ID: cust.ID}}, []core.PaymentID{viaRequest.ID, direct.ID}},
		{"customer's own, filtered on itself", payments.ListInput{Customer: payments.CustomerRef{ID: cust.ID}, FilterCustomer: payments.CustomerRef{ExternalID: cust.ExternalID}}, []core.PaymentID{viaRequest.ID, direct.ID}},
		{"customer's own, filtered on another", payments.ListInput{Customer: payments.CustomerRef{ID: cust.ID}, FilterCustomer: payments.CustomerRef{ID: otherCust.ID}}, nil},
		{"invoice, direct or requested", payments.ListInput{Filter: payment.Filter{InvoiceID: inv.ID}}, []core.PaymentID{viaRequest.ID, direct.ID}},
		{"status", payments.ListInput{Filter: payment.Filter{Status: payment.StateFailed}}, []core.PaymentID{other.ID}},
		{"currency", payments.ListInput{Filter: payment.Filter{Currency: billingtest.USD}}, []core.PaymentID{other.ID, viaRequest.ID, direct.ID}},
		{"other currency", payments.ListInput{Filter: payment.Filter{Currency: money.MustCurrency("GBP")}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, err := svc.List(h.Ctx, h.OrgID(), c.in, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			var got []core.PaymentID
			for _, d := range page.Items {
				got = append(got, d.ID)
			}
			if len(got) != len(c.want) {
				t.Fatalf("payments: got = %v, want = %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("payments: got = %v, want = %v", got, c.want)
				}
			}
		})
	}
}

func TestListPaymentsScopedCustomer(t *testing.T) {
	t.Log("source: spec/requests/api/v1/customers/payments_controller_spec.rb:13")
	h, svc := newHarness(t)
	_, err := svc.List(h.Ctx, h.OrgID(), payments.ListInput{
		Customer: payments.CustomerRef{ExternalID: "unknown"},
	}, core.PageRequest{})
	wantNotFound(t, err, "customer")
	deleted := h.Customer(func(c *customer.Customer) { c.DeletedAt = new(h.Now()) })
	_, err = svc.List(h.Ctx, h.OrgID(), payments.ListInput{
		Customer: payments.CustomerRef{ExternalID: deleted.ExternalID},
	}, core.PageRequest{})
	wantNotFound(t, err, "customer")
}

func TestListPaymentsOrder(t *testing.T) {
	t.Log("source: app/queries/base_query.rb:111")
	h, svc := newHarness(t)
	cust := h.Customer()
	inv := h.StoredInvoice(cust, finalized(10000, 0))
	var ps []*payment.Payment
	for range 3 {
		ps = append(ps, h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{}))
	}
	h.Clock.Advance(time.Second)
	newest := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	want := []core.PaymentID{newest.ID, ps[0].ID, ps[1].ID, ps[2].ID} // created_at desc, then id asc
	var got []core.PaymentID
	page := core.PageRequest{Size: 1}
	for {
		p, err := svc.List(h.Ctx, h.OrgID(), payments.ListInput{}, page)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range p.Items {
			got = append(got, d.ID)
		}
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	if len(got) != len(want) {
		t.Fatalf("payments: got = %v, want = %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("payments: got = %v, want = %v", got, want)
		}
	}
}

func TestListPaymentsSearch(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer(func(c *customer.Customer) {
		c.Firstname, c.Lastname, c.Name = new("first"), new("last"), new("The name")
		c.ExternalID, c.Email = "external_c_id", new("email@example.com")
	})
	other := h.Customer()
	inv := h.StoredInvoice(cust, finalized(10000, 0), func(inv *invoice.Invoice) { inv.Number = "number-test-123" })
	inv3 := h.StoredInvoice(cust, finalized(10000, 0))
	inv2 := h.StoredInvoice(other, finalized(10000, 0))
	q := h.StoredPaymentRequest(other.ID, core.PaymentStatusPending, inv2)
	one := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	two := h.StoredPayment(payment.InvoicePayable(inv3.ID), cust.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	h.StoredPayment(payment.InvoicePayable(inv2.ID), other.ID, 100, payment.StateSucceeded, core.IntegrationID{})
	h.StoredPayment(payment.RequestPayable(q.ID), other.ID, 100, payment.StatePending, core.IntegrationID{})
	byCustomer := []core.PaymentID{one.ID, two.ID}
	cases := []struct {
		name, source, term string
		want               []core.PaymentID
	}{
		{"id", "spec/queries/payments_query_spec.rb:67", one.ID.String(), []core.PaymentID{one.ID}},
		{"partial id", "spec/queries/payments_query_spec.rb:77", one.ID.String()[:13], nil},
		{"unknown uuid", "spec/queries/payments_query_spec.rb:86", "00000000-0000-0000-0000-000000000000", nil},
		{"invoice number", "spec/queries/payments_query_spec.rb:95", "number-test-123", []core.PaymentID{one.ID}},
		{"customer name", "spec/queries/payments_query_spec.rb:105", "The name", byCustomer},
		{"customer email", "spec/queries/payments_query_spec.rb:115", "email@example.com", byCustomer},
		{"customer external id", "spec/queries/payments_query_spec.rb:125", "external_c_id", byCustomer},
		{"customer firstname", "spec/queries/payments_query_spec.rb:135", "first", byCustomer},
		{"customer lastname", "spec/queries/payments_query_spec.rb:145", "last", byCustomer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s", c.source)
			p, err := svc.List(h.Ctx, h.OrgID(), payments.ListInput{Filter: payment.Filter{Search: c.term}}, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			got := map[core.PaymentID]bool{}
			for _, d := range p.Items {
				got[d.ID] = true
			}
			if len(got) != len(c.want) {
				t.Fatalf("payments: got = %v, want = %v", got, c.want)
			}
			for _, id := range c.want {
				if !got[id] {
					t.Fatalf("payments: got = %v, want = %v", got, c.want)
				}
			}
		})
	}
}
