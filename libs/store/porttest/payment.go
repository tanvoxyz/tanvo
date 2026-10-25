package porttest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// paymentParents are the rows of other aggregates the payment tests point
// at, all in one organization.
type paymentParents struct {
	org       core.OrganizationID
	be        core.BillingEntityID
	customers [2]core.CustomerID
	// integrations are two payment provider connections.
	integrations [2]core.IntegrationID
	invoices     [3]core.InvoiceID
	creditNotes  [2]core.CreditNoteID
	subscription core.SubscriptionID
	campaign     core.DunningCampaignID
}

var paymentEUR = money.MustCurrency("EUR")

// paymentFixtureParents creates the parent rows of the payment tests in the
// organization of be.
func paymentFixtureParents(e *env, be *organization.BillingEntity) paymentParents {
	e.t.Helper()
	p := paymentParents{org: be.OrganizationID, be: be.ID}
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i := range p.customers {
			c := customer.New(p.org, core.NewID[core.CustomerID](e.ids), fmt.Sprintf("pay-cust-%d", i), be.ID, now)
			if err := r.Customers.Create(ctx, c); err != nil {
				return fmt.Errorf("customer: %w", err)
			}
			p.customers[i] = c.ID
		}
		for i := range p.integrations {
			in := &integration.Integration{
				ID: core.NewID[core.IntegrationID](e.ids), OrganizationID: p.org,
				Code: fmt.Sprintf("stripe-%d", i), Name: fmt.Sprintf("Stripe %d", i),
				Category: integration.CategoryPayment, Provider: integration.ProviderStripe,
				IsDefault: i == 0, Settings: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
			}
			if err := r.Integrations.Create(ctx, in); err != nil {
				return fmt.Errorf("integration: %w", err)
			}
			p.integrations[i] = in.ID
		}
		for i := range p.invoices {
			due := core.NewDate(2025, time.March, 14)
			inv := &invoice.Invoice{
				ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: p.org,
				CustomerID: p.customers[0], BillingEntityID: be.ID,
				Type: invoice.TypeOneOff, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusPending,
				Number: fmt.Sprintf("PAY-%03d", i+1), SequentialID: ptr(int64(i + 1)), BillingEntitySequentialID: ptr(int64(i + 1)),
				Currency: paymentEUR, FeesAmount: 1000, SubTotalExcludingTaxesAmount: 1000, SubTotalIncludingTaxesAmount: 1000,
				TotalAmount: 1000, TaxesRate: money.MustParse("0"),
				IssuingDate: due, PaymentDueDate: due, ExpectedFinalizationDate: due,
				ReadyForPaymentProcessing: true, FinalizedAt: ptr(now), CreatedAt: now, UpdatedAt: now,
			}
			if err := r.Invoices.Create(ctx, inv); err != nil {
				return fmt.Errorf("invoice: %w", err)
			}
			p.invoices[i] = inv.ID
		}
		for i := range p.creditNotes {
			cn := &creditnote.CreditNote{
				ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: p.org,
				InvoiceID: p.invoices[0], CustomerID: p.customers[0],
				Number: fmt.Sprintf("PAY-001-CN%03d", i+1), SequentialID: int64(i + 1),
				Status: creditnote.StatusFinalized, RefundStatus: core.RefundStatusPending, Reason: creditnote.ReasonOther,
				RefundAmount: 100, SubTotalExcludingTaxesAmount: 100, TotalAmount: 100,
				PreciseCouponsAdjustmentAmount: money.MustParse("0"), PreciseTaxesAmount: money.MustParse("0"), TaxesRate: money.MustParse("0"),
				Currency: paymentEUR, IssuingDate: core.NewDate(2025, time.March, 14), CreatedAt: now, UpdatedAt: now,
			}
			if err := r.CreditNotes.Create(ctx, cn); err != nil {
				return fmt.Errorf("credit note: %w", err)
			}
			p.creditNotes[i] = cn.ID
		}
		plan := &catalog.Plan{
			ID: core.NewID[core.PlanID](e.ids), OrganizationID: p.org, Code: "pay-plan", Name: "Pay plan",
			Interval: period.Monthly, Currency: paymentEUR, Status: catalog.PlanStatusActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := r.Plans.Create(ctx, plan); err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		s := subscription.New(p.org, core.NewID[core.SubscriptionID](e.ids), p.customers[0], plan.ID, "pay-sub", now, subscription.PlanFacts{}, now)
		s.BillingEntityID = be.ID
		if err := r.Subscriptions.Create(ctx, s); err != nil {
			return fmt.Errorf("subscription: %w", err)
		}
		p.subscription = s.ID
		c := dunning.New(p.org, core.NewID[core.DunningCampaignID](e.ids), "pay-campaign", "Pay campaign", now)
		if err := r.DunningCampaigns.Create(ctx, c); err != nil {
			return fmt.Errorf("dunning campaign: %w", err)
		}
		p.campaign = c.ID
		return nil
	})
	return p
}

// paymentFixtureProvider returns a pending provider payment of an invoice,
// with every field set.
func paymentFixtureProvider(e *env, p paymentParents, inv core.InvoiceID) *payment.Payment {
	return &payment.Payment{
		ID: core.NewID[core.PaymentID](e.ids), OrganizationID: p.org,
		Payable: payment.InvoicePayable(inv), CustomerID: p.customers[0],
		Kind: payment.KindProvider, Status: payment.StatePending, ProviderStatus: ptr("requires_payment_method"),
		Amount: 1000, Currency: paymentEUR, IntegrationID: p.integrations[0],
		ProviderPaymentID: ptr("pi_" + inv.String()), ProviderPaymentMethodID: ptr("pm_1"),
		ProviderPaymentData:   json.RawMessage(`{"id": "pi_1", "amount": 1000}`),
		ProviderPaymentMethod: payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030},
		CreatedAt:             now, UpdatedAt: now,
	}
}

// paymentFixtureManual returns a succeeded manual payment of an invoice.
func paymentFixtureManual(e *env, p paymentParents, inv core.InvoiceID) *payment.Payment {
	return &payment.Payment{
		ID: core.NewID[core.PaymentID](e.ids), OrganizationID: p.org,
		Payable: payment.InvoicePayable(inv), CustomerID: p.customers[0],
		Kind: payment.KindManual, Status: payment.StateSucceeded, Amount: 500, Currency: paymentEUR,
		Reference: ptr("wire 42"), PaidAt: ptr(at(-time.Hour)), CreatedAt: now, UpdatedAt: now,
	}
}

func paymentCreate(t *testing.T, e *env, ps ...*payment.Payment) {
	t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, p := range ps {
			noErr(t, "Payments.Create()", r.Payments.Create(ctx, p))
		}
		return nil
	})
}

func paymentFixtureRequest(e *env, p paymentParents, invs ...core.InvoiceID) *payment.Request {
	return &payment.Request{
		ID: core.NewID[core.PaymentRequestID](e.ids), OrganizationID: p.org, CustomerID: p.customers[0],
		InvoiceIDs: invs, Amount: 2000, Currency: paymentEUR, PaymentStatus: core.PaymentStatusPending,
		CreatedAt: now, UpdatedAt: now,
	}
}

func testPayment(t *testing.T, h Harness) {
	t.Run("payments", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)

		prov := paymentFixtureProvider(e, pa, pa.invoices[0])
		man := paymentFixtureManual(e, pa, pa.invoices[0])
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() provider", r.Payments.Create(ctx, prov))
			noErr(t, "Create() manual", r.Payments.Create(ctx, man))
			wantErr(t, "Create() same ID", r.Payments.Create(ctx, prov), core.ErrConflict)
			got, err := r.Payments.Get(ctx, pa.org, prov.ID)
			noErr(t, "Get() provider", err)
			equal(t, "Get() provider", got, prov)
			got, err = r.Payments.Get(ctx, pa.org, man.ID)
			noErr(t, "Get() manual", err)
			equal(t, "Get() manual", got, man)
			_, err = r.Payments.Get(ctx, pa.org, core.NewID[core.PaymentID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Payments.Get(ctx, pb.org, prov.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)

			// Update with every optional field cleared.
			prov.Status = payment.StateSucceeded
			prov.ProviderStatus = nil
			prov.IntegrationID = core.IntegrationID{}
			prov.ProviderPaymentID = nil
			prov.ProviderPaymentMethodID = nil
			prov.ProviderPaymentData = nil
			prov.ProviderPaymentMethod = payment.MethodDetails{}
			prov.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Payments.Update(ctx, prov))
			got, err = r.Payments.Get(ctx, pa.org, prov.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, prov)

			other := *prov
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.Payments.Update(ctx, &other), core.ErrNotFound)
			missing := paymentFixtureManual(e, pa, pa.invoices[0])
			wantErr(t, "Update() unknown", r.Payments.Update(ctx, missing), core.ErrNotFound)
			return nil
		})
	})

	t.Run("payments one pending provider payment per payable", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		req := paymentFixtureRequest(e, pa, pa.invoices[1])
		e.tx(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Create(ctx, req) })

		first := paymentFixtureProvider(e, pa, pa.invoices[0])
		reqFirst := paymentFixtureProvider(e, pa, pa.invoices[0])
		reqFirst.Payable = payment.RequestPayable(req.ID)
		reqFirst.ProviderPaymentID = ptr("pi_req")
		paymentCreate(t, e, first, reqFirst)

		e.tx(func(ctx context.Context, r port.Repos) error {
			dup := paymentFixtureProvider(e, pa, pa.invoices[0])
			wantErr(t, "Create() second pending of invoice", r.Payments.Create(ctx, dup), core.ErrConflict)
			dup.Status = payment.StateProcessing
			wantErr(t, "Create() second processing of invoice", r.Payments.Create(ctx, dup), core.ErrConflict)
			dupReq := paymentFixtureProvider(e, pa, pa.invoices[0])
			dupReq.Payable = payment.RequestPayable(req.ID)
			wantErr(t, "Create() second pending of request", r.Payments.Create(ctx, dupReq), core.ErrConflict)

			// Final and manual payments do not count.
			failed := paymentFixtureProvider(e, pa, pa.invoices[0])
			failed.Status = payment.StateFailed
			noErr(t, "Create() failed provider payment", r.Payments.Create(ctx, failed))
			noErr(t, "Create() manual payment", r.Payments.Create(ctx, paymentFixtureManual(e, pa, pa.invoices[0])))
			// Another payable is free.
			other := paymentFixtureProvider(e, pa, pa.invoices[2])
			noErr(t, "Create() pending of another invoice", r.Payments.Create(ctx, other))

			// Reopening the failed payment conflicts; the transaction stays usable.
			failed.Status = payment.StatePending
			wantErr(t, "Update() reopening", r.Payments.Update(ctx, failed), core.ErrConflict)
			first.Status = payment.StateSucceeded
			noErr(t, "Update() first to succeeded", r.Payments.Update(ctx, first))
			noErr(t, "Update() reopening after", r.Payments.Update(ctx, failed))
			return nil
		})
	})

	t.Run("payments lookups and lists", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		req := paymentFixtureRequest(e, pa, pa.invoices[1])
		e.tx(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Create(ctx, req) })

		p1 := paymentFixtureProvider(e, pa, pa.invoices[0])
		p1.Status = payment.StateFailed
		p2 := paymentFixtureManual(e, pa, pa.invoices[0])
		p3 := paymentFixtureProvider(e, pa, pa.invoices[0])
		p3.ProviderPaymentID = ptr("pi_3")
		p4 := paymentFixtureProvider(e, pa, pa.invoices[0])
		p4.Payable = payment.RequestPayable(req.ID)
		p4.CustomerID = pa.customers[1]
		p4.IntegrationID = pa.integrations[1]
		p4.ProviderPaymentID = ptr("pi_3")
		pOther := paymentFixtureProvider(e, pb, pb.invoices[0])
		pOther.ProviderPaymentID = ptr("pi_3")
		// Creation times with a tie, for the recent order.
		p1.CreatedAt, p2.CreatedAt, p3.CreatedAt = at(-2*time.Minute), at(-time.Minute), at(-time.Minute)
		paymentCreate(t, e, p1, p2, p3, p4)
		paymentCreate(t, e, pOther)

		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Payments.GetByProviderPaymentID(ctx, pa.org, pa.integrations[0], "pi_3")
			noErr(t, "GetByProviderPaymentID()", err)
			equal(t, "GetByProviderPaymentID()", got.ID, p3.ID)
			got, err = r.Payments.GetByProviderPaymentID(ctx, pa.org, pa.integrations[1], "pi_3")
			noErr(t, "GetByProviderPaymentID() second connection", err)
			equal(t, "GetByProviderPaymentID() second connection", got.ID, p4.ID)
			got, err = r.Payments.GetByProviderPaymentID(ctx, pb.org, pb.integrations[0], "pi_3")
			noErr(t, "GetByProviderPaymentID() other org", err)
			equal(t, "GetByProviderPaymentID() other org", got.ID, pOther.ID)
			_, err = r.Payments.GetByProviderPaymentID(ctx, pa.org, pa.integrations[0], "pi_nope")
			wantErr(t, "GetByProviderPaymentID() unknown", err, core.ErrNotFound)
			_, err = r.Payments.GetByProviderPaymentID(ctx, pb.org, pa.integrations[0], "pi_3")
			wantErr(t, "GetByProviderPaymentID() other org's connection", err, core.ErrNotFound)

			list, err := r.Payments.ListByPayable(ctx, pa.org, payment.InvoicePayable(pa.invoices[0]))
			noErr(t, "ListByPayable() invoice", err)
			equal(t, "ListByPayable() invoice", list, []*payment.Payment{p1, p2, p3})
			list, err = r.Payments.ListByPayable(ctx, pa.org, payment.RequestPayable(req.ID))
			noErr(t, "ListByPayable() request", err)
			equal(t, "ListByPayable() request", list, []*payment.Payment{p4})
			list, err = r.Payments.ListByPayable(ctx, pb.org, payment.InvoicePayable(pa.invoices[0]))
			noErr(t, "ListByPayable() other org", err)
			equal(t, "ListByPayable() other org", len(list), 0)
			return nil
		})

		pid := func(p *payment.Payment) core.PaymentID { return p.ID }
		cases := []struct {
			name string
			f    payment.Filter
			want []*payment.Payment
		}{
			{"all", payment.Filter{}, []*payment.Payment{p1, p2, p3, p4}},
			{"customer", payment.Filter{CustomerID: pa.customers[1]}, []*payment.Payment{p4}},
			{"status", payment.Filter{Status: payment.StatePending}, []*payment.Payment{p3, p4}},
			{"customer and status", payment.Filter{CustomerID: pa.customers[0], Status: payment.StateSucceeded}, []*payment.Payment{p2}},
			{"invoice", payment.Filter{InvoiceID: pa.invoices[0]}, []*payment.Payment{p1, p2, p3}},
			{"invoice through a request", payment.Filter{InvoiceID: pa.invoices[1]}, []*payment.Payment{p4}},
			{"currency", payment.Filter{Currency: paymentEUR}, []*payment.Payment{p1, p2, p3, p4}},
			{"other currency", payment.Filter{Currency: money.MustCurrency("GBP")}, nil},
			{"visible", payment.Filter{Visible: true}, []*payment.Payment{p1, p2, p3, p4}},
			{"recent", payment.Filter{Order: core.OrderRecent}, []*payment.Payment{p4, p2, p3, p1}},
			{"search provider payment ID", payment.Filter{Search: " PI_3 "}, []*payment.Payment{p3, p4}},
			{"search reference", payment.Filter{Search: "WIRE"}, []*payment.Payment{p2}},
			{"search ID", payment.Filter{Search: p1.ID.String()}, []*payment.Payment{p1}},
			{"search partial ID", payment.Filter{Search: p1.ID.String()[24:]}, nil},
			{"search invoice number", payment.Filter{Search: "pay-001"}, []*payment.Payment{p1, p2, p3}},
			{"search invoice number with invoice", payment.Filter{Search: "PAY-001", InvoiceID: pa.invoices[1]}, nil},
			{"search customer external ID", payment.Filter{Search: "pay-cust-1"}, []*payment.Payment{p4}},
			{"search customer external ID with customer", payment.Filter{Search: "pay-cust-1", CustomerID: pa.customers[0]}, nil},
			{"search no match", payment.Filter{Search: "nothing"}, nil},
		}
		for _, c := range cases {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*payment.Payment], error) {
					return r.Payments.List(ctx, pa.org, c.f, pr)
				})
				equal(t, "List() "+c.name, ids(got, pid), ids(c.want, pid))
				return nil
			})
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			got := listAll(t, 10, func(pr core.PageRequest) (core.Page[*payment.Payment], error) {
				return r.Payments.List(ctx, pb.org, payment.Filter{}, pr)
			})
			equal(t, "List() other org", ids(got, pid), []core.PaymentID{pOther.ID})
			return nil
		})
	})

	t.Run("payment receipts", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		p1 := paymentFixtureManual(e, pa, pa.invoices[0])
		p2 := paymentFixtureManual(e, pa, pa.invoices[1])
		p3 := paymentFixtureManual(e, pa, pa.invoices[2])
		pOther := paymentFixtureManual(e, pb, pb.invoices[0])
		paymentCreate(t, e, p1, p2, p3)
		paymentCreate(t, e, pOther)

		r1 := &payment.Receipt{
			ID: core.NewID[core.PaymentReceiptID](e.ids), OrganizationID: pa.org, PaymentID: p1.ID,
			BillingEntityID: pa.be, Number: "CUST-RCPT-000001", File: "blob/r1.pdf", XMLFile: "blob/r1.xml",
			CreatedAt: at(-time.Minute), UpdatedAt: now,
		}
		r2 := &payment.Receipt{
			ID: core.NewID[core.PaymentReceiptID](e.ids), OrganizationID: pa.org, PaymentID: p2.ID,
			Number: "CUST-RCPT-000002", CreatedAt: now, UpdatedAt: now,
		}
		rOther := &payment.Receipt{
			ID: core.NewID[core.PaymentReceiptID](e.ids), OrganizationID: pb.org, PaymentID: pOther.ID,
			Number: "CUST-RCPT-000001", CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.PaymentReceipts.Create(ctx, r1))
			noErr(t, "Create() without optional fields", r.PaymentReceipts.Create(ctx, r2))
			noErr(t, "Create() same number in another org", r.PaymentReceipts.Create(ctx, rOther))
			dup := *r2
			dup.ID = core.NewID[core.PaymentReceiptID](e.ids)
			dup.Number = "CUST-RCPT-000003"
			wantErr(t, "Create() second receipt of a payment", r.PaymentReceipts.Create(ctx, &dup), core.ErrConflict)
			dup.PaymentID = p3.ID
			dup.Number = r1.Number
			wantErr(t, "Create() taken number", r.PaymentReceipts.Create(ctx, &dup), core.ErrConflict)

			got, err := r.PaymentReceipts.Get(ctx, pa.org, r1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, r1)
			got, err = r.PaymentReceipts.Get(ctx, pa.org, r2.ID)
			noErr(t, "Get() without optional fields", err)
			equal(t, "Get() without optional fields", got, r2)
			got, err = r.PaymentReceipts.GetByPayment(ctx, pa.org, p1.ID)
			noErr(t, "GetByPayment()", err)
			equal(t, "GetByPayment()", got, r1)
			_, err = r.PaymentReceipts.GetByPayment(ctx, pa.org, p3.ID)
			wantErr(t, "GetByPayment() without receipt", err, core.ErrNotFound)
			_, err = r.PaymentReceipts.Get(ctx, pb.org, r1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)
			_, err = r.PaymentReceipts.GetByPayment(ctx, pb.org, p1.ID)
			wantErr(t, "GetByPayment() other org", err, core.ErrNotFound)

			r1.File = ""
			r1.XMLFile = ""
			r1.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.PaymentReceipts.Update(ctx, r1))
			got, err = r.PaymentReceipts.Get(ctx, pa.org, r1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, r1)
			other := *r1
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.PaymentReceipts.Update(ctx, &other), core.ErrNotFound)
			taken := *r2
			taken.Number = r1.Number
			wantErr(t, "Update() taken number", r.PaymentReceipts.Update(ctx, &taken), core.ErrConflict)
			taken = *r2
			taken.PaymentID = r1.PaymentID
			wantErr(t, "Update() taken payment", r.PaymentReceipts.Update(ctx, &taken), core.ErrConflict)
			noErr(t, "Update() after conflict", r.PaymentReceipts.Update(ctx, r2))
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*payment.Receipt], error) {
				return r.PaymentReceipts.List(ctx, pa.org, payment.ReceiptFilter{}, pr)
			})
			equal(t, "List()", got, []*payment.Receipt{r1, r2})
			for _, c := range []struct {
				name string
				f    payment.ReceiptFilter
				want []*payment.Receipt
			}{
				{"invoice", payment.ReceiptFilter{InvoiceID: pa.invoices[0]}, []*payment.Receipt{r1}},
				{"recent", payment.ReceiptFilter{Order: core.OrderRecent}, []*payment.Receipt{r2, r1}},
			} {
				got = listAll(t, 1, func(pr core.PageRequest) (core.Page[*payment.Receipt], error) {
					return r.PaymentReceipts.List(ctx, pa.org, c.f, pr)
				})
				equal(t, "List() "+c.name, len(got), len(c.want))
				for i := range got {
					equal(t, "List() "+c.name, got[i].ID, c.want[i].ID)
				}
			}
			got = listAll(t, 5, func(pr core.PageRequest) (core.Page[*payment.Receipt], error) {
				return r.PaymentReceipts.List(ctx, pb.org, payment.ReceiptFilter{}, pr)
			})
			equal(t, "List() other org", got, []*payment.Receipt{rOther})
			return nil
		})
	})

	t.Run("refunds", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		p1 := paymentFixtureProvider(e, pa, pa.invoices[0])
		p1.Status = payment.StateSucceeded
		p2 := paymentFixtureProvider(e, pa, pa.invoices[1])
		p2.Status = payment.StateSucceeded
		paymentCreate(t, e, p1, p2)
		ppc := &payment.ProviderCustomer{
			ID: core.NewID[core.PaymentProviderCustomerID](e.ids), OrganizationID: pa.org,
			CustomerID: pa.customers[0], IntegrationID: pa.integrations[0], CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Create(ctx, ppc) })

		r1 := &payment.Refund{
			ID: core.NewID[core.RefundID](e.ids), OrganizationID: pa.org, PaymentID: p1.ID,
			Reason: payment.RefundReasonCreditNote, CreditNoteID: pa.creditNotes[0],
			IntegrationID: pa.integrations[0], PaymentProviderCustomerID: ppc.ID,
			Amount: 100, Currency: paymentEUR, Status: core.RefundStatusPending, ProviderRefundID: ptr("re_1"),
			CreatedAt: now, UpdatedAt: now,
		}
		r2 := &payment.Refund{
			ID: core.NewID[core.RefundID](e.ids), OrganizationID: pa.org, PaymentID: p1.ID,
			Reason: payment.RefundReasonSubscriptionActivationExpired, SubscriptionID: pa.subscription,
			Amount: 50, Currency: paymentEUR, Status: core.RefundStatusSucceeded, CreatedAt: now, UpdatedAt: now,
		}
		r3 := &payment.Refund{
			ID: core.NewID[core.RefundID](e.ids), OrganizationID: pa.org, PaymentID: p2.ID,
			CreditNoteID: pa.creditNotes[0], IntegrationID: pa.integrations[1], ProviderRefundID: ptr("re_1"),
			Amount: 25, Currency: paymentEUR, Status: core.RefundStatusFailed, CreatedAt: now, UpdatedAt: now,
		}
		r4 := &payment.Refund{
			ID: core.NewID[core.RefundID](e.ids), OrganizationID: pa.org, PaymentID: p2.ID,
			CreditNoteID: pa.creditNotes[1], Amount: 10, Currency: paymentEUR, Status: core.RefundStatusPending,
			CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*payment.Refund{r1, r2, r3, r4} {
				noErr(t, "Create()", r.Refunds.Create(ctx, x))
			}
			wantErr(t, "Create() same ID", r.Refunds.Create(ctx, r1), core.ErrConflict)
			// The integrations stay while payments, refunds and provider
			// customers reference them.
			wantErr(t, "Integrations.Delete() referenced", r.Integrations.Delete(ctx, pa.org, pa.integrations[0]), core.ErrConflict)
			_, err := r.Integrations.Get(ctx, pa.org, pa.integrations[0])
			noErr(t, "Integrations.Get() after a refused Delete()", err)
			for _, x := range []*payment.Refund{r1, r2, r3} {
				got, err := r.Refunds.Get(ctx, pa.org, x.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, x)
			}
			_, err = r.Refunds.Get(ctx, pa.org, core.NewID[core.RefundID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Refunds.Get(ctx, pb.org, r1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)

			got, err := r.Refunds.GetByProviderRefundID(ctx, pa.org, pa.integrations[0], "re_1")
			noErr(t, "GetByProviderRefundID()", err)
			equal(t, "GetByProviderRefundID()", got, r1)
			got, err = r.Refunds.GetByProviderRefundID(ctx, pa.org, pa.integrations[1], "re_1")
			noErr(t, "GetByProviderRefundID() second connection", err)
			equal(t, "GetByProviderRefundID() second connection", got, r3)
			_, err = r.Refunds.GetByProviderRefundID(ctx, pa.org, pa.integrations[0], "re_nope")
			wantErr(t, "GetByProviderRefundID() unknown", err, core.ErrNotFound)
			_, err = r.Refunds.GetByProviderRefundID(ctx, pb.org, pa.integrations[0], "re_1")
			wantErr(t, "GetByProviderRefundID() other org", err, core.ErrNotFound)

			list, err := r.Refunds.ListByCreditNote(ctx, pa.org, pa.creditNotes[0])
			noErr(t, "ListByCreditNote()", err)
			equal(t, "ListByCreditNote()", list, []*payment.Refund{r1, r3})
			list, err = r.Refunds.ListByPayment(ctx, pa.org, p2.ID)
			noErr(t, "ListByPayment()", err)
			equal(t, "ListByPayment()", list, []*payment.Refund{r3, r4})
			list, err = r.Refunds.ListByPayment(ctx, pb.org, p2.ID)
			noErr(t, "ListByPayment() other org", err)
			equal(t, "ListByPayment() other org", len(list), 0)
			list, err = r.Refunds.ListByCreditNote(ctx, pb.org, pa.creditNotes[0])
			noErr(t, "ListByCreditNote() other org", err)
			equal(t, "ListByCreditNote() other org", len(list), 0)

			r1.Status = core.RefundStatusSucceeded
			r1.ProviderRefundID = nil
			r1.PaymentProviderCustomerID = core.PaymentProviderCustomerID{}
			r1.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Refunds.Update(ctx, r1))
			got, err = r.Refunds.Get(ctx, pa.org, r1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, r1)
			other := *r1
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.Refunds.Update(ctx, &other), core.ErrNotFound)
			return nil
		})
	})

	t.Run("payment requests", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		m := &payment.Method{
			ID: core.NewID[core.PaymentMethodID](e.ids), OrganizationID: pa.org, CustomerID: pa.customers[0],
			IntegrationID: pa.integrations[0], ProviderMethodID: "pm_1", CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })

		r1 := paymentFixtureRequest(e, pa, pa.invoices[2], pa.invoices[0])
		r1.Email = ptr("billing@acme.test")
		r1.PaymentAttempts = 2
		r1.DunningCampaignID = pa.campaign
		r1.PaymentMethodType = core.PaymentMethodTypeProvider
		r1.PaymentMethodID = m.ID
		r1.ReadyForPaymentProcessing = true
		r1.CreatedAt = at(-time.Minute) // r2 and r3 tie, for the recent order
		r2 := paymentFixtureRequest(e, pa, pa.invoices[1])
		r2.CustomerID = pa.customers[1]
		r2.PaymentStatus = core.PaymentStatusFailed
		r3 := paymentFixtureRequest(e, pa, pa.invoices[0], pa.invoices[1])
		rOther := paymentFixtureRequest(e, pb, pb.invoices[0])
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*payment.Request{r1, r2, r3, rOther} {
				noErr(t, "Create()", r.PaymentRequests.Create(ctx, x))
			}
			wantErr(t, "Create() same ID", r.PaymentRequests.Create(ctx, r1), core.ErrConflict)
			for _, x := range []*payment.Request{r1, r2} {
				got, err := r.PaymentRequests.Get(ctx, pa.org, x.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, x)
			}
			_, err := r.PaymentRequests.Get(ctx, pa.org, core.NewID[core.PaymentRequestID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.PaymentRequests.Get(ctx, pb.org, r1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)

			list, err := r.PaymentRequests.ListByInvoice(ctx, pa.org, pa.invoices[0])
			noErr(t, "ListByInvoice()", err)
			equal(t, "ListByInvoice()", list, []*payment.Request{r1, r3})
			list, err = r.PaymentRequests.ListByInvoice(ctx, pb.org, pa.invoices[0])
			noErr(t, "ListByInvoice() other org", err)
			equal(t, "ListByInvoice() other org", len(list), 0)

			// Update replaces the invoices and clears the optional fields.
			r1.InvoiceIDs = []core.InvoiceID{pa.invoices[1]}
			r1.Email = nil
			r1.DunningCampaignID = core.DunningCampaignID{}
			r1.PaymentMethodType = ""
			r1.PaymentMethodID = core.PaymentMethodID{}
			r1.PaymentStatus = core.PaymentStatusSucceeded
			r1.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.PaymentRequests.Update(ctx, r1))
			got, err := r.PaymentRequests.Get(ctx, pa.org, r1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, r1)
			list, err = r.PaymentRequests.ListByInvoice(ctx, pa.org, pa.invoices[0])
			noErr(t, "ListByInvoice() after Update()", err)
			equal(t, "ListByInvoice() after Update()", list, []*payment.Request{r3})
			other := *r1
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.PaymentRequests.Update(ctx, &other), core.ErrNotFound)
			return nil
		})

		rid := func(x *payment.Request) core.PaymentRequestID { return x.ID }
		cases := []struct {
			name string
			f    payment.RequestFilter
			want []*payment.Request
		}{
			{"all", payment.RequestFilter{}, []*payment.Request{r1, r2, r3}},
			{"customer", payment.RequestFilter{CustomerID: pa.customers[0]}, []*payment.Request{r1, r3}},
			{"payment status", payment.RequestFilter{PaymentStatus: core.PaymentStatusFailed}, []*payment.Request{r2}},
			{"both", payment.RequestFilter{CustomerID: pa.customers[0], PaymentStatus: core.PaymentStatusPending}, []*payment.Request{r3}},
			{"currency", payment.RequestFilter{Currency: paymentEUR}, []*payment.Request{r1, r2, r3}},
			{"other currency", payment.RequestFilter{Currency: money.MustCurrency("GBP")}, nil},
			{"billing entity", payment.RequestFilter{BillingEntityIDs: []core.BillingEntityID{pa.be}}, []*payment.Request{r1, r2, r3}},
			{"other billing entity", payment.RequestFilter{BillingEntityIDs: []core.BillingEntityID{pb.be}}, nil},
			{"billing entities", payment.RequestFilter{BillingEntityIDs: []core.BillingEntityID{pb.be, pa.be}}, []*payment.Request{r1, r2, r3}},
			{"recent", payment.RequestFilter{Order: core.OrderRecent}, []*payment.Request{r2, r3, r1}},
		}
		for _, c := range cases {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*payment.Request], error) {
					return r.PaymentRequests.List(ctx, pa.org, c.f, pr)
				})
				equal(t, "List() "+c.name, ids(got, rid), ids(c.want, rid))
				return nil
			})
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			got := listAll(t, 10, func(pr core.PageRequest) (core.Page[*payment.Request], error) {
				return r.PaymentRequests.List(ctx, pb.org, payment.RequestFilter{}, pr)
			})
			equal(t, "List() other org", got, []*payment.Request{rOther})
			return nil
		})
	})

	t.Run("payment methods", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		ppc := &payment.ProviderCustomer{
			ID: core.NewID[core.PaymentProviderCustomerID](e.ids), OrganizationID: pa.org,
			CustomerID: pa.customers[0], IntegrationID: pa.integrations[0], CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Create(ctx, ppc) })
		method := func(cust core.CustomerID, providerID string, def bool) *payment.Method {
			return &payment.Method{
				ID: core.NewID[core.PaymentMethodID](e.ids), OrganizationID: pa.org, CustomerID: cust,
				IntegrationID: pa.integrations[0], ProviderMethodID: providerID, IsDefault: def,
				CreatedAt: now, UpdatedAt: now,
			}
		}
		m1 := method(pa.customers[0], "pm_1", true)
		m1.PaymentProviderCustomerID = ppc.ID
		m1.Details = payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 1, ExpYear: 2031}
		m2 := method(pa.customers[0], "pm_2", false)
		m2.IntegrationID = core.IntegrationID{}
		m3 := method(pa.customers[0], "pm_3", false)
		m4 := method(pa.customers[1], "pm_4", true)
		mOther := method(pb.customers[0], "pm_1", true)
		mOther.OrganizationID = pb.org
		mOther.IntegrationID = pb.integrations[0]
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, m := range []*payment.Method{m1, m2, m3, m4, mOther} {
				noErr(t, "Create()", r.PaymentMethods.Create(ctx, m))
			}
			wantErr(t, "Create() same ID", r.PaymentMethods.Create(ctx, m1), core.ErrConflict)
			dup := method(pa.customers[0], "pm_5", true)
			wantErr(t, "Create() second default", r.PaymentMethods.Create(ctx, dup), core.ErrConflict)
			dup.DeletedAt = ptr(now)
			noErr(t, "Create() deleted default", r.PaymentMethods.Create(ctx, dup))
			twin := method(pa.customers[0], "pm_1", false)
			twin.PaymentProviderCustomerID = ppc.ID
			wantErr(t, "Create() same provider customer and method", r.PaymentMethods.Create(ctx, twin), core.ErrConflict)

			for _, m := range []*payment.Method{m1, m2} {
				got, err := r.PaymentMethods.Get(ctx, pa.org, m.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, m)
			}
			got, err := r.PaymentMethods.Get(ctx, pa.org, dup.ID)
			noErr(t, "Get() deleted", err)
			equal(t, "Get() deleted", got, dup)
			_, err = r.PaymentMethods.Get(ctx, pb.org, m1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)
			got, err = r.PaymentMethods.GetDefault(ctx, pa.org, pa.customers[0])
			noErr(t, "GetDefault()", err)
			equal(t, "GetDefault()", got, m1)
			_, err = r.PaymentMethods.GetDefault(ctx, pb.org, pa.customers[0])
			wantErr(t, "GetDefault() other org", err, core.ErrNotFound)
			got, err = r.PaymentMethods.GetByProviderMethodID(ctx, pa.org, pa.integrations[0], "pm_3")
			noErr(t, "GetByProviderMethodID()", err)
			equal(t, "GetByProviderMethodID()", got, m3)
			_, err = r.PaymentMethods.GetByProviderMethodID(ctx, pa.org, pa.integrations[0], "pm_5")
			wantErr(t, "GetByProviderMethodID() deleted", err, core.ErrNotFound)
			_, err = r.PaymentMethods.GetByProviderMethodID(ctx, pa.org, pa.integrations[1], "pm_3")
			wantErr(t, "GetByProviderMethodID() other connection", err, core.ErrNotFound)
			_, err = r.PaymentMethods.GetByProviderMethodID(ctx, pb.org, pa.integrations[0], "pm_3")
			wantErr(t, "GetByProviderMethodID() other org", err, core.ErrNotFound)

			list, err := r.PaymentMethods.ListByCustomer(ctx, pa.org, pa.customers[0])
			noErr(t, "ListByCustomer()", err)
			equal(t, "ListByCustomer()", list, []*payment.Method{m1, m2, m3})
			list, err = r.PaymentMethods.ListByCustomer(ctx, pb.org, pa.customers[0])
			noErr(t, "ListByCustomer() other org", err)
			equal(t, "ListByCustomer() other org", len(list), 0)

			// Moving the default: a second default conflicts, the
			// transaction stays usable.
			m3.IsDefault = true
			wantErr(t, "Update() second default", r.PaymentMethods.Update(ctx, m3), core.ErrConflict)
			noErr(t, "Delete() default", m1.Delete(at(time.Minute)))
			noErr(t, "Update() deleting the default", r.PaymentMethods.Update(ctx, m1))
			m3.UpdatedAt = at(time.Minute)
			noErr(t, "Update() new default", r.PaymentMethods.Update(ctx, m3))
			got, err = r.PaymentMethods.GetDefault(ctx, pa.org, pa.customers[0])
			noErr(t, "GetDefault() after Update()", err)
			equal(t, "GetDefault() after Update()", got, m3)
			got, err = r.PaymentMethods.Get(ctx, pa.org, m1.ID)
			noErr(t, "Get() deleted after Update()", err)
			equal(t, "Get() deleted after Update()", got, m1)
			_, err = r.PaymentMethods.GetByProviderMethodID(ctx, pa.org, pa.integrations[0], "pm_1")
			wantErr(t, "GetByProviderMethodID() deleted after Update()", err, core.ErrNotFound)
			list, err = r.PaymentMethods.ListByCustomer(ctx, pa.org, pa.customers[0])
			noErr(t, "ListByCustomer() after Update()", err)
			equal(t, "ListByCustomer() after Update()", list, []*payment.Method{m2, m3})
			other := *m2
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.PaymentMethods.Update(ctx, &other), core.ErrNotFound)
			noErr(t, "Create() method of a deleted one", r.PaymentMethods.Create(ctx, twin))
			return nil
		})
	})

	t.Run("payment provider customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		pc := func(cust core.CustomerID, integ core.IntegrationID, providerID *string) *payment.ProviderCustomer {
			return &payment.ProviderCustomer{
				ID: core.NewID[core.PaymentProviderCustomerID](e.ids), OrganizationID: pa.org,
				CustomerID: cust, IntegrationID: integ, ProviderCustomerID: providerID,
				CreatedAt: now, UpdatedAt: now,
			}
		}
		c1 := pc(pa.customers[0], pa.integrations[0], ptr("cus_1"))
		c1.SyncWithProvider = true
		c1.ProviderPaymentMethods = []string{"card", "sepa_debit"}
		c1.IsDefault = true
		c2 := pc(pa.customers[0], pa.integrations[1], nil)
		c3 := pc(pa.customers[1], pa.integrations[0], ptr("cus_3"))
		cOther := pc(pb.customers[0], pb.integrations[0], ptr("cus_1"))
		cOther.OrganizationID = pb.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*payment.ProviderCustomer{c1, c2, c3, cOther} {
				noErr(t, "Create()", r.PaymentProviderCustomers.Create(ctx, c))
			}
			wantErr(t, "Create() same ID", r.PaymentProviderCustomers.Create(ctx, c1), core.ErrConflict)
			dup := pc(pa.customers[0], pa.integrations[0], ptr("cus_dup"))
			wantErr(t, "Create() second live row", r.PaymentProviderCustomers.Create(ctx, dup), core.ErrConflict)
			dup.DeletedAt = ptr(now)
			noErr(t, "Create() deleted row", r.PaymentProviderCustomers.Create(ctx, dup))

			for _, c := range []*payment.ProviderCustomer{c1, c2, dup} {
				got, err := r.PaymentProviderCustomers.Get(ctx, pa.org, c.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, c)
			}
			_, err := r.PaymentProviderCustomers.Get(ctx, pb.org, c1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)
			got, err := r.PaymentProviderCustomers.GetByCustomer(ctx, pa.org, pa.customers[0], pa.integrations[1])
			noErr(t, "GetByCustomer()", err)
			equal(t, "GetByCustomer()", got, c2)
			_, err = r.PaymentProviderCustomers.GetByCustomer(ctx, pa.org, pa.customers[1], pa.integrations[1])
			wantErr(t, "GetByCustomer() none", err, core.ErrNotFound)
			_, err = r.PaymentProviderCustomers.GetByCustomer(ctx, pb.org, pa.customers[0], pa.integrations[1])
			wantErr(t, "GetByCustomer() other org", err, core.ErrNotFound)
			got, err = r.PaymentProviderCustomers.GetByProviderCustomerID(ctx, pa.org, pa.integrations[0], "cus_1")
			noErr(t, "GetByProviderCustomerID()", err)
			equal(t, "GetByProviderCustomerID()", got, c1)
			_, err = r.PaymentProviderCustomers.GetByProviderCustomerID(ctx, pa.org, pa.integrations[0], "cus_dup")
			wantErr(t, "GetByProviderCustomerID() deleted", err, core.ErrNotFound)
			_, err = r.PaymentProviderCustomers.GetByProviderCustomerID(ctx, pa.org, pa.integrations[1], "cus_1")
			wantErr(t, "GetByProviderCustomerID() other connection", err, core.ErrNotFound)
			_, err = r.PaymentProviderCustomers.GetByProviderCustomerID(ctx, pb.org, pa.integrations[0], "cus_1")
			wantErr(t, "GetByProviderCustomerID() other org", err, core.ErrNotFound)

			list, err := r.PaymentProviderCustomers.ListByCustomer(ctx, pa.org, pa.customers[0])
			noErr(t, "ListByCustomer()", err)
			equal(t, "ListByCustomer()", list, []*payment.ProviderCustomer{c1, c2})
			list, err = r.PaymentProviderCustomers.ListByIntegration(ctx, pa.org, pa.integrations[0])
			noErr(t, "ListByIntegration()", err)
			equal(t, "ListByIntegration()", list, []*payment.ProviderCustomer{c1, c3})
			list, err = r.PaymentProviderCustomers.ListByIntegration(ctx, pb.org, pa.integrations[0])
			noErr(t, "ListByIntegration() other org", err)
			equal(t, "ListByIntegration() other org", len(list), 0)

			// A second live default conflicts.
			c2.IsDefault = true
			wantErr(t, "Update() second default", r.PaymentProviderCustomers.Update(ctx, c2), core.ErrConflict)
			c2.IsDefault = false

			// Reviving the deleted row conflicts; deleting the live one first
			// frees the connection.
			dup.DeletedAt = nil
			wantErr(t, "Update() reviving", r.PaymentProviderCustomers.Update(ctx, dup), core.ErrConflict)
			c1.DeletedAt = ptr(at(time.Minute))
			c1.ProviderCustomerID = nil
			c1.ProviderPaymentMethods = nil
			c1.UpdatedAt = at(time.Minute)
			noErr(t, "Update() deleting", r.PaymentProviderCustomers.Update(ctx, c1))
			noErr(t, "Update() reviving after", r.PaymentProviderCustomers.Update(ctx, dup))
			c2.IsDefault, c2.UpdatedAt = true, at(time.Minute)
			noErr(t, "Update() default after the default is deleted", r.PaymentProviderCustomers.Update(ctx, c2))
			got, err = r.PaymentProviderCustomers.Get(ctx, pa.org, c1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, c1)
			got, err = r.PaymentProviderCustomers.GetByCustomer(ctx, pa.org, pa.customers[0], pa.integrations[0])
			noErr(t, "GetByCustomer() after Update()", err)
			equal(t, "GetByCustomer() after Update()", got, dup)
			other := *c2
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.PaymentProviderCustomers.Update(ctx, &other), core.ErrNotFound)
			return nil
		})
	})

	t.Run("payment intents", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		pb := paymentFixtureParents(e, b)
		i1 := payment.NewIntent(pa.org, core.NewID[payment.IntentID](e.ids), pa.invoices[0], at(-25*time.Hour))
		i1.PaymentURL = ptr("https://checkout.test/1")
		i1.ProviderSessionID = ptr("cs_1")
		i2 := payment.NewIntent(pa.org, core.NewID[payment.IntentID](e.ids), pa.invoices[1], now)
		i3 := payment.NewIntent(pa.org, core.NewID[payment.IntentID](e.ids), pa.invoices[2], at(-24*time.Hour))
		iOther := payment.NewIntent(pb.org, core.NewID[payment.IntentID](e.ids), pb.invoices[0], at(-25*time.Hour))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, i := range []*payment.Intent{i1, i2, i3, iOther} {
				noErr(t, "Create()", r.PaymentIntents.Create(ctx, i))
			}
			wantErr(t, "Create() same ID", r.PaymentIntents.Create(ctx, i1), core.ErrConflict)
			dup := payment.NewIntent(pa.org, core.NewID[payment.IntentID](e.ids), pa.invoices[0], now)
			wantErr(t, "Create() second active link", r.PaymentIntents.Create(ctx, dup), core.ErrConflict)

			got, err := r.PaymentIntents.GetActiveByInvoice(ctx, pa.org, pa.invoices[0])
			noErr(t, "GetActiveByInvoice()", err)
			equal(t, "GetActiveByInvoice()", got, i1)
			got, err = r.PaymentIntents.GetActiveByInvoice(ctx, pa.org, pa.invoices[1])
			noErr(t, "GetActiveByInvoice() without optional fields", err)
			equal(t, "GetActiveByInvoice() without optional fields", got, i2)
			_, err = r.PaymentIntents.GetActiveByInvoice(ctx, pb.org, pa.invoices[0])
			wantErr(t, "GetActiveByInvoice() other org", err, core.ErrNotFound)

			list, err := r.PaymentIntents.ListAwaitingExpiration(ctx, pa.org, now)
			noErr(t, "ListAwaitingExpiration()", err)
			equal(t, "ListAwaitingExpiration()", list, []*payment.Intent{i1, i3})

			noErr(t, "Expire()", i1.Expire(at(time.Minute)))
			noErr(t, "Update() expiring", r.PaymentIntents.Update(ctx, i1))
			_, err = r.PaymentIntents.GetActiveByInvoice(ctx, pa.org, pa.invoices[0])
			wantErr(t, "GetActiveByInvoice() expired", err, core.ErrNotFound)
			noErr(t, "Create() new active link", r.PaymentIntents.Create(ctx, dup))
			i1.Status = payment.IntentStatusActive
			wantErr(t, "Update() reactivating", r.PaymentIntents.Update(ctx, i1), core.ErrConflict)
			list, err = r.PaymentIntents.ListAwaitingExpiration(ctx, pa.org, now)
			noErr(t, "ListAwaitingExpiration() after Update()", err)
			equal(t, "ListAwaitingExpiration() after Update()", list, []*payment.Intent{i3})
			list, err = r.PaymentIntents.ListAwaitingExpiration(ctx, pa.org, at(-time.Second))
			noErr(t, "ListAwaitingExpiration() before i3 expires", err)
			equal(t, "ListAwaitingExpiration() before i3 expires", len(list), 0)
			list, err = r.PaymentIntents.ListAwaitingExpiration(ctx, pb.org, now)
			noErr(t, "ListAwaitingExpiration() other org", err)
			equal(t, "ListAwaitingExpiration() other org", list, []*payment.Intent{iOther})

			other := *i2
			other.OrganizationID = pb.org
			wantErr(t, "Update() other org", r.PaymentIntents.Update(ctx, &other), core.ErrNotFound)
			missing := payment.NewIntent(pa.org, core.NewID[payment.IntentID](e.ids), pa.invoices[2], now)
			missing.Status = payment.IntentStatusExpired
			wantErr(t, "Update() unknown", r.PaymentIntents.Update(ctx, missing), core.ErrNotFound)
			return nil
		})
	})

	t.Run("inbound webhooks", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		pa := paymentFixtureParents(e, a)
		other := b
		// The payload is the raw body: whitespace and key order are kept.
		raw := json.RawMessage("{\"type\": \"charge.succeeded\",\n  \"a\":1, \"a2\": [1, 2]}")
		w1 := payment.NewInboundWebhook(pa.org, core.NewID[core.InboundWebhookID](e.ids), "stripe", "charge.succeeded", raw, now)
		w1.IntegrationID = pa.integrations[0]
		w1.Code = ptr("stripe-0")
		w1.Signature = ptr("t=1,v1=abc")
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.InboundWebhooks.Create(ctx, w1))
			wantErr(t, "Create() same ID", r.InboundWebhooks.Create(ctx, w1), core.ErrConflict)
			got, err := r.InboundWebhooks.Get(ctx, pa.org, w1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, w1)
			if string(got.Payload) != string(raw) {
				t.Errorf("Get() payload got = %q, want = %q", got.Payload, raw)
			}
			_, err = r.InboundWebhooks.Get(ctx, other.OrganizationID, w1.ID)
			wantErr(t, "Get() other org", err, core.ErrNotFound)
			_, err = r.InboundWebhooks.Get(ctx, pa.org, core.NewID[core.InboundWebhookID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			noErr(t, "Transition()", w1.Transition(payment.InboundWebhookStatusProcessing, at(time.Minute)))
			w1.IntegrationID = core.IntegrationID{}
			w1.Code = nil
			w1.Signature = nil
			noErr(t, "Update()", r.InboundWebhooks.Update(ctx, w1))
			got, err = r.InboundWebhooks.Get(ctx, pa.org, w1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, w1)
			o := *w1
			o.OrganizationID = other.OrganizationID
			wantErr(t, "Update() other org", r.InboundWebhooks.Update(ctx, &o), core.ErrNotFound)
			return nil
		})
	})

	t.Run("inbound webhooks retries and retention", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		body := json.RawMessage(`{}`)
		hook := func(org core.OrganizationID, status payment.InboundWebhookStatus, created time.Time, processing *time.Time) *payment.InboundWebhook {
			w := payment.NewInboundWebhook(org, core.NewID[core.InboundWebhookID](e.ids), "stripe", "event", body, created)
			w.Status = status
			w.ProcessingAt = processing
			if processing != nil {
				w.Attempts = 1
				w.UpdatedAt = *processing
			}
			return w
		}
		// Instants relative to now.
		fresh := hook(org, payment.InboundWebhookStatusPending, at(-time.Minute), nil)                                     // due at reception
		future := hook(org, payment.InboundWebhookStatusPending, at(time.Minute), nil)                                     // received after now
		failedDue := hook(org, payment.InboundWebhookStatusFailed, at(-time.Hour), ptr(at(-20*time.Minute)))               // retry at -5m
		failedEarly := hook(org, payment.InboundWebhookStatusFailed, at(-time.Hour), ptr(at(-10*time.Minute)))             // retry at +5m
		stuck := hook(org, payment.InboundWebhookStatusProcessing, at(-time.Hour), ptr(at(-15*time.Minute)))               // retry at now
		outOfWindow := hook(org, payment.InboundWebhookStatusFailed, at(-3*time.Hour), ptr(at(-time.Hour-10*time.Minute))) // retry past the window
		succeeded := hook(org, payment.InboundWebhookStatusSucceeded, at(-time.Hour), ptr(at(-30*time.Minute)))
		old := hook(org, payment.InboundWebhookStatusSucceeded, at(-100*24*time.Hour), nil)
		old.UpdatedAt = at(-payment.WebhookRetention)
		recent := hook(org, payment.InboundWebhookStatusSucceeded, at(-100*24*time.Hour), nil)
		recent.UpdatedAt = at(-payment.WebhookRetention + time.Second)
		otherOld := hook(b.OrganizationID, payment.InboundWebhookStatusPending, at(-100*24*time.Hour), nil)
		otherOld.UpdatedAt = at(-payment.WebhookRetention)
		all := []*payment.InboundWebhook{fresh, future, failedDue, failedEarly, stuck, outOfWindow, succeeded, old, recent, otherOld}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, w := range all {
				noErr(t, "Create()", r.InboundWebhooks.Create(ctx, w))
			}
			return nil
		})
		var want []core.InboundWebhookID
		for _, w := range all {
			if w.OrganizationID == org && w.Retriable(now) {
				want = append(want, w.ID)
			}
		}
		wid := func(w *payment.InboundWebhook) core.InboundWebhookID { return w.ID }
		equal(t, "domain Retriable()", want, []core.InboundWebhookID{fresh.ID, failedDue.ID, stuck.ID})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.InboundWebhooks.ListRetriable(ctx, org, now)
			noErr(t, "ListRetriable()", err)
			equal(t, "ListRetriable()", ids(got, wid), want)
			got, err = r.InboundWebhooks.ListRetriable(ctx, b.OrganizationID, now)
			noErr(t, "ListRetriable() other org", err)
			// A never-attempted pending webhook is due whatever its age.
			equal(t, "ListRetriable() other org", ids(got, wid), []core.InboundWebhookID{otherOld.ID})

			n, err := r.InboundWebhooks.DeleteExpired(ctx, org, now)
			noErr(t, "DeleteExpired()", err)
			equal(t, "DeleteExpired()", n, 1)
			_, err = r.InboundWebhooks.Get(ctx, org, old.ID)
			wantErr(t, "Get() expired", err, core.ErrNotFound)
			_, err = r.InboundWebhooks.Get(ctx, org, recent.ID)
			noErr(t, "Get() kept", err)
			_, err = r.InboundWebhooks.Get(ctx, b.OrganizationID, otherOld.ID)
			noErr(t, "Get() other org's expired", err)
			n, err = r.InboundWebhooks.DeleteExpired(ctx, org, now)
			noErr(t, "DeleteExpired() again", err)
			equal(t, "DeleteExpired() again", n, 0)
			return nil
		})
	})
}
