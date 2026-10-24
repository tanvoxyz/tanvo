package porttest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// RunAnalytics runs the analytics report tests (parity §1.44) against h.
// The reports are SQL of the PostgreSQL store, so the conformance suite
// [Run] leaves them out and only that store runs them.
func RunAnalytics(t *testing.T, h Harness) {
	for _, g := range []group{
		{"gross_revenue", testAnalyticsGrossRevenue},
		{"mrr", testAnalyticsMRR},
		{"mrr_spread", testAnalyticsMRRSpread},
		{"invoice_collection", testAnalyticsInvoiceCollection},
		{"invoiced_usage", testAnalyticsInvoicedUsage},
		{"overdue_balance", testAnalyticsOverdueBalance},
	} {
		t.Run(g.name, func(t *testing.T) {
			if h.Concurrent {
				t.Parallel()
			}
			g.run(t, h)
		})
	}
}

var analyticsUSD = money.MustCurrency("USD")

// month returns the first day of the month m months from now's.
func month(m int) time.Time {
	return time.Date(now.Year(), now.Month()+time.Month(m), 1, 0, 0, 0, 0, time.UTC)
}

// analyticsEnv is an organization created three months before now, with
// two billing entities, two customers (one per entity; the second with a
// tax identification number) and a monthly plan with a subscription per
// customer.
type analyticsEnv struct {
	*env
	org       core.OrganizationID
	be        [2]core.BillingEntityID
	customers [2]core.CustomerID
	subs      [2]core.SubscriptionID
	plan      core.PlanID
}

func newAnalyticsEnv(t *testing.T, h Harness) *analyticsEnv {
	t.Helper()
	e := &analyticsEnv{env: newEnv(t, h)}
	o := organization.New(core.NewID[core.OrganizationID](e.ids), "Acme", now.AddDate(0, -3, 0))
	e.org = o.ID
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Organizations.Create(ctx, o); err != nil {
			return err
		}
		for i := range e.be {
			b := organization.NewBillingEntity(o.ID, core.NewID[core.BillingEntityID](e.ids), fmt.Sprintf("be%d", i), "Entity", now)
			b.IsDefault = i == 0
			if err := r.BillingEntities.Create(ctx, b); err != nil {
				return err
			}
			e.be[i] = b.ID
		}
		plan := catalogFixturePlan(e.env, o.ID, "monthly")
		if err := r.Plans.Create(ctx, plan); err != nil {
			return err
		}
		e.plan = plan.ID
		for i := range e.customers {
			c := customer.New(o.ID, core.NewID[core.CustomerID](e.ids), fmt.Sprintf("cust-%d", i), e.be[i], now)
			if i == 1 {
				c.TaxIdentificationNumber = ptr("123456789")
			}
			if err := r.Customers.Create(ctx, c); err != nil {
				return err
			}
			e.customers[i] = c.ID
			s := subscription.New(o.ID, core.NewID[core.SubscriptionID](e.ids), c.ID, plan.ID,
				fmt.Sprintf("sub-%d", i), now, subscription.PlanFacts{}, now)
			s.BillingEntityID = e.be[i]
			if err := r.Subscriptions.Create(ctx, s); err != nil {
				return err
			}
			e.subs[i] = s.ID
		}
		return nil
	})
	return e
}

// invoice returns a finalized EUR invoice of customer c, billing entity
// be, issued on issued with total.
func (e *analyticsEnv) invoice(c, be int, issued time.Time, total int64) *invoice.Invoice {
	return &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: e.org, CustomerID: e.customers[c], BillingEntityID: e.be[be],
		Type: invoice.TypeSubscription, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusPending,
		Currency: invoiceEUR, TotalAmount: total, IssuingDate: core.DateOf(issued, time.UTC),
		CreatedAt: issued, UpdatedAt: issued,
	}
}

// fee returns a fee of subscription s on inv (zero for none).
func (e *analyticsEnv) fee(s int, inv core.InvoiceID, typ core.FeeType, amount int64, created time.Time) *invoice.Fee {
	return &invoice.Fee{
		ID: core.NewID[core.FeeID](e.ids), OrganizationID: e.org, InvoiceID: inv, SubscriptionID: e.subs[s], Type: typ,
		Currency: invoiceEUR, Amount: amount, PreciseAmount: money.NewFromInt(amount),
		Properties: invoice.FeeProperties{From: created, To: created.AddDate(0, 1, 0)},
		CreatedAt:  created, UpdatedAt: created,
	}
}

// create stores the invoices, then the fees and credit notes.
func (e *analyticsEnv) create(invs []*invoice.Invoice, fees []*invoice.Fee, cns ...*creditnote.CreditNote) {
	e.t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, inv := range invs {
			if err := r.Invoices.Create(ctx, inv); err != nil {
				return fmt.Errorf("invoice: %w", err)
			}
		}
		for _, f := range fees {
			if err := r.Fees.Create(ctx, f); err != nil {
				return fmt.Errorf("fee: %w", err)
			}
		}
		for _, cn := range cns {
			if err := r.CreditNotes.Create(ctx, cn); err != nil {
				return fmt.Errorf("credit note: %w", err)
			}
		}
		return nil
	})
}

// creditNote returns a credit note on inv.
func (e *analyticsEnv) creditNote(inv *invoice.Invoice, status creditnote.Status, refund, offset int64) *creditnote.CreditNote {
	return &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: e.org, InvoiceID: inv.ID, CustomerID: inv.CustomerID,
		Status: status, Reason: creditnote.ReasonOther, Currency: inv.Currency,
		RefundAmount: refund, OffsetAmount: offset, TotalAmount: refund + offset,
		CreatedAt: now, UpdatedAt: now,
	}
}

// report runs fn and fails the test on error.
func report[T any](e *analyticsEnv, s analytics.Scope, fn func(analytics.Repository, context.Context, core.OrganizationID, analytics.Scope) ([]T, error)) []T {
	e.t.Helper()
	var out []T
	e.tx(func(ctx context.Context, r port.Repos) (err error) {
		out, err = fn(r.Analytics, ctx, e.org, s)
		return err
	})
	return out
}

func revenue(m int, c money.Currency, be core.BillingEntityID, amount, count int64) analytics.Revenue {
	return analytics.Revenue{Month: month(m), Currency: c, Amount: &amount, InvoicesCount: count, BillingEntityID: be}
}

// source: spec/models/analytics/gross_revenue_spec.rb:75-238
func testAnalyticsGrossRevenue(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	usd := e.invoice(0, 0, now.AddDate(0, -1, 0), 1000)
	usd.Currency = analyticsUSD
	refunded := e.invoice(1, 1, now.AddDate(0, -1, 0), 600)
	invs := []*invoice.Invoice{
		e.invoice(0, 0, now.AddDate(0, -1, 0), 1000),
		e.invoice(1, 1, now.AddDate(0, -1, 0), 2000),
		e.invoice(0, 0, now.AddDate(0, -2, 0), 3000),
		e.invoice(1, 1, now.AddDate(0, -2, 0), 4000),
		usd, refunded,
	}
	draft := e.invoice(0, 0, now.AddDate(0, -1, 0), 50000)
	draft.Status = invoice.StatusDraft
	selfBilled := e.invoice(0, 0, now.AddDate(0, -1, 0), 50000)
	selfBilled.SelfBilled = true
	disputed := e.invoice(0, 0, now.AddDate(0, -1, 0), 50000)
	disputed.PaymentDisputeLostAt = ptr(now)
	// Pay-in-advance fees outside invoices count in their creation month,
	// for the billing entity of their customer.
	advance := []*invoice.Fee{
		e.fee(0, core.InvoiceID{}, core.FeeTypeCharge, 500, now.AddDate(0, -1, 0)),
		e.fee(1, core.InvoiceID{}, core.FeeTypeCharge, 9999, now.AddDate(0, -1, 0)),
	}
	for _, f := range advance {
		f.PayInAdvance = true
	}
	// A fee outside invoices but not paid in advance does not count.
	arrears := e.fee(0, core.InvoiceID{}, core.FeeTypeCharge, 70000, now.AddDate(0, -1, 0))
	e.create(append(invs, draft, selfBilled, disputed), append(advance, arrears),
		e.creditNote(refunded, creditnote.StatusFinalized, 100, 0))

	eur := invoiceEUR
	for _, c := range []struct {
		name  string
		scope analytics.Scope
		want  []analytics.Revenue
	}{
		// source: spec/models/analytics/gross_revenue_spec.rb:96-125 (split
		// per month, currency and billing entity)
		{"all", analytics.Scope{Now: now}, []analytics.Revenue{
			revenue(-2, eur, e.be[0], 3000, 1),
			revenue(-2, eur, e.be[1], 4000, 1),
			revenue(-1, eur, e.be[0], 1000+500, 1),
			revenue(-1, eur, e.be[1], 2000+500+9999, 2),
			revenue(-1, analyticsUSD, e.be[0], 1000, 1),
		}},
		// source: spec/models/analytics/gross_revenue_spec.rb:193-238
		{"billing entity", analytics.Scope{Now: now, BillingEntityID: e.be[0]}, []analytics.Revenue{
			revenue(-2, eur, e.be[0], 3000, 1),
			revenue(-1, eur, e.be[0], 1000+500, 1),
			revenue(-1, analyticsUSD, e.be[0], 1000, 1),
		}},
		// source: spec/models/analytics/gross_revenue_spec.rb:127-164
		{"currency", analytics.Scope{Now: now, Currency: analyticsUSD}, []analytics.Revenue{
			revenue(-1, analyticsUSD, e.be[0], 1000, 1),
		}},
		{"customer", analytics.Scope{Now: now, CustomerID: e.customers[1]}, []analytics.Revenue{
			revenue(-2, eur, e.be[1], 4000, 1),
			revenue(-1, eur, e.be[1], 2000+500+9999, 2),
		}},
		{"months", analytics.Scope{Now: now, Months: 2}, []analytics.Revenue{
			revenue(-1, eur, e.be[0], 1000+500, 1),
			revenue(-1, eur, e.be[1], 2000+500+9999, 2),
			revenue(-1, analyticsUSD, e.be[0], 1000, 1),
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			equal(t, "GrossRevenue()", report(e, c.scope, analytics.Repository.GrossRevenue), c.want)
		})
	}
}

// source: spec/models/analytics/mrr_spec.rb:75-164
func testAnalyticsMRR(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	inv1 := e.invoice(0, 0, now.AddDate(0, -2, 0), 330)
	inv2 := e.invoice(0, 1, now.AddDate(0, -1, 0), 770)
	fees := []*invoice.Fee{
		e.fee(0, inv1.ID, core.FeeTypeSubscription, 100, now.AddDate(0, -2, 0)),
		e.fee(0, inv1.ID, core.FeeTypeSubscription, 200, now.AddDate(0, -2, 0)),
		e.fee(0, inv2.ID, core.FeeTypeSubscription, 300, now.AddDate(0, -1, 0)),
		e.fee(0, inv2.ID, core.FeeTypeSubscription, 400, now.AddDate(0, -1, 0)),
	}
	for _, f := range fees {
		f.TaxesAmount = f.Amount / 10
	}
	// Charge fees are not recurring revenue.
	fees = append(fees, e.fee(0, inv2.ID, core.FeeTypeCharge, 5000, now.AddDate(0, -1, 0)))
	e.create([]*invoice.Invoice{inv1, inv2}, fees)

	none := func(m int) analytics.Revenue { return analytics.Revenue{Month: month(m)} }
	mrr := func(m int, amount int64) analytics.Revenue {
		return analytics.Revenue{Month: month(m), Currency: invoiceEUR, Amount: &amount}
	}
	for _, c := range []struct {
		name  string
		scope analytics.Scope
		want  []analytics.Revenue
	}{
		// source: spec/models/analytics/mrr_spec.rb:113-137
		{"all", analytics.Scope{Now: now}, []analytics.Revenue{none(-3), mrr(-2, 330), mrr(-1, 770), none(0)}},
		// source: spec/models/analytics/mrr_spec.rb:139-163
		{"billing entity", analytics.Scope{Now: now, BillingEntityID: e.be[0]}, []analytics.Revenue{none(-3), mrr(-2, 330), none(-1), none(0)}},
		{"months", analytics.Scope{Now: now, Months: 2}, []analytics.Revenue{mrr(-1, 770), none(0)}},
		{"currency", analytics.Scope{Now: now, Currency: analyticsUSD}, []analytics.Revenue{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			equal(t, "MRR()", report(e, c.scope, analytics.Repository.MRR), c.want)
		})
	}
}

// testAnalyticsMRRSpread checks the spreading of a yearly fee paid in
// advance over the months it bills.
func testAnalyticsMRRSpread(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	yearly := catalogFixturePlan(e.env, e.org, "yearly")
	yearly.Interval = period.Yearly
	yearly.PayInAdvance = true
	var sub core.SubscriptionID
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Plans.Create(ctx, yearly); err != nil {
			return err
		}
		s := subscription.New(e.org, core.NewID[core.SubscriptionID](e.ids), e.customers[0], yearly.ID, "yearly", now, subscription.PlanFacts{}, now)
		s.BillingEntityID = e.be[0]
		sub = s.ID
		return r.Subscriptions.Create(ctx, s)
	})
	// Issued on the first of the month, for a year: 365 days, 11.99 months
	// of 30.44 days, 1000.77 a month.
	issued := month(-2)
	inv := e.invoice(0, 0, issued, 12000)
	f := e.fee(0, inv.ID, core.FeeTypeSubscription, 12000, issued)
	f.SubscriptionID = sub
	f.Properties = invoice.FeeProperties{From: issued, To: issued.AddDate(1, 0, 0)}
	e.create([]*invoice.Invoice{inv}, []*invoice.Fee{f})
	got := report(e, analytics.Scope{Now: now}, analytics.Repository.MRR)
	want := []analytics.Revenue{{Month: month(-3)}}
	for m := -2; m <= 0; m++ {
		want = append(want, analytics.Revenue{Month: month(m), Currency: invoiceEUR, Amount: ptr(int64(1000))})
	}
	equal(t, "MRR()", got, want)
}

// source: spec/models/analytics/invoice_collection_spec.rb:88-140
func testAnalyticsInvoiceCollection(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	draft1 := e.invoice(0, 0, now.AddDate(0, -2, 0), 100)
	draft1.Status = invoice.StatusDraft
	draft2 := e.invoice(1, 1, now.AddDate(0, -1, 0), 300)
	draft2.Status = invoice.StatusDraft
	failed := e.invoice(1, 1, now.AddDate(0, -1, 0), 50)
	failed.PaymentStatus = core.PaymentStatusFailed
	e.create([]*invoice.Invoice{
		draft1, e.invoice(0, 0, now.AddDate(0, -2, 0), 200),
		draft2, e.invoice(1, 1, now.AddDate(0, -1, 0), 400), failed,
	}, nil)

	none := func(m int) analytics.Collection { return analytics.Collection{Month: month(m)} }
	col := func(m int, st core.PaymentStatus, count, amount int64) analytics.Collection {
		return analytics.Collection{Month: month(m), PaymentStatus: st, Currency: invoiceEUR, InvoicesCount: count, Amount: amount}
	}
	for _, c := range []struct {
		name  string
		scope analytics.Scope
		want  []analytics.Collection
	}{
		// source: spec/models/analytics/invoice_collection_spec.rb:108-123
		{"all", analytics.Scope{Now: now}, []analytics.Collection{
			none(-3), col(-2, core.PaymentStatusPending, 1, 200),
			col(-1, core.PaymentStatusFailed, 1, 50), col(-1, core.PaymentStatusPending, 1, 400), none(0),
		}},
		// source: spec/models/analytics/invoice_collection_spec.rb:125-140
		{"billing entity", analytics.Scope{Now: now, BillingEntityID: e.be[0]}, []analytics.Collection{
			none(-3), col(-2, core.PaymentStatusPending, 1, 200), none(-1), none(0),
		}},
		// source: spec/models/analytics/invoice_collection_spec.rb:142-157
		{"customer tin empty", analytics.Scope{Now: now, CustomerTINEmpty: ptr(true)}, []analytics.Collection{
			none(-3), col(-2, core.PaymentStatusPending, 1, 200), none(-1), none(0),
		}},
		// source: spec/models/analytics/invoice_collection_spec.rb:159-174
		{"customer tin set", analytics.Scope{Now: now, CustomerTINEmpty: ptr(false)}, []analytics.Collection{
			none(-3), none(-2), col(-1, core.PaymentStatusFailed, 1, 50), col(-1, core.PaymentStatusPending, 1, 400), none(0),
		}},
		// source: app/models/analytics/invoice_collection.rb:13-16
		{"customer", analytics.Scope{Now: now, CustomerID: e.customers[0]}, []analytics.Collection{
			none(-3), col(-2, core.PaymentStatusPending, 1, 200), none(-1), none(0),
		}},
		{"months", analytics.Scope{Now: now, Months: 1}, []analytics.Collection{none(0)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			equal(t, "InvoiceCollection()", report(e, c.scope, analytics.Repository.InvoiceCollection), c.want)
		})
	}
}

// source: spec/models/analytics/invoiced_usage_spec.rb:78-149
func testAnalyticsInvoicedUsage(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	metrics := []*catalog.BillableMetric{catalogFixtureMetric(e.env, e.org, "api_calls"), catalogFixtureMetric(e.env, e.org, "storage")}
	var charges [2]core.ChargeID
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i, m := range metrics {
			if err := r.Metrics.Create(ctx, m); err != nil {
				return err
			}
			ch := catalogFixtureCharge(e.env, e.org, e.plan, m.ID, m.Code)
			if err := r.Charges.Create(ctx, ch); err != nil {
				return err
			}
			charges[i] = ch.ID
		}
		return nil
	})
	inv1 := e.invoice(0, 0, now.AddDate(0, -2, 0), 300)
	inv2 := e.invoice(0, 1, now.AddDate(0, -1, 0), 700)
	charge := func(inv core.InvoiceID, ch int, amount int64, created time.Time) *invoice.Fee {
		f := e.fee(0, inv, core.FeeTypeCharge, amount, created)
		f.ChargeID = charges[ch]
		return f
	}
	coupon := charge(inv2.ID, 1, 1000, now.AddDate(0, -1, 0))
	coupon.PreciseCouponsAmount = money.MustParse("900.5")
	e.create([]*invoice.Invoice{inv1, inv2}, []*invoice.Fee{
		charge(inv1.ID, 0, 100, now.AddDate(0, -2, 0)),
		charge(inv1.ID, 0, 200, now.AddDate(0, -2, 0)),
		charge(inv2.ID, 0, 300, now.AddDate(0, -1, 0)),
		charge(inv2.ID, 0, 400, now.AddDate(0, -1, 0)),
		coupon,
		// Subscription fees are not usage.
		e.fee(0, inv2.ID, core.FeeTypeSubscription, 5000, now.AddDate(0, -1, 0)),
	})

	usage := func(m int, code string, amount int64) analytics.Usage {
		return analytics.Usage{Month: month(m), MetricCode: code, Currency: invoiceEUR, Amount: amount}
	}
	for _, c := range []struct {
		name  string
		scope analytics.Scope
		want  []analytics.Usage
	}{
		// source: spec/models/analytics/invoiced_usage_spec.rb:119-135
		{"all", analytics.Scope{Now: now}, []analytics.Usage{
			usage(-1, "api_calls", 700), usage(-1, "storage", 99), usage(-2, "api_calls", 300),
		}},
		// source: spec/models/analytics/invoiced_usage_spec.rb:137-148
		{"billing entity", analytics.Scope{Now: now, BillingEntityID: e.be[0]}, []analytics.Usage{usage(-2, "api_calls", 300)}},
		{"months", analytics.Scope{Now: now, Months: 1}, []analytics.Usage{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			equal(t, "InvoicedUsage()", report(e, c.scope, analytics.Repository.InvoicedUsage), c.want)
		})
	}
}

// source: spec/models/analytics/overdue_balance_spec.rb:88-352
func testAnalyticsOverdueBalance(t *testing.T, h Harness) {
	e := newAnalyticsEnv(t, h)
	overdue := func(c, be int, due time.Time, total int64) *invoice.Invoice {
		inv := e.invoice(c, be, due, total)
		inv.PaymentDueDate = core.DateOf(due, time.UTC)
		inv.PaymentOverdue = true
		return inv
	}
	inv1 := overdue(0, 0, now.AddDate(0, -1, 0), 100)
	inv2 := overdue(0, 1, now.AddDate(0, -1, 0), 200)
	inv2.PaymentOverdue = false
	inv3 := overdue(0, 0, now.AddDate(0, -2, 0), 300)
	inv3.PaymentOverdue = false
	inv4 := overdue(0, 1, now.AddDate(0, -2, 0), 400)
	credited := overdue(0, 0, now.AddDate(0, -1, 0), 1000)
	draftCredit := overdue(0, 0, now.AddDate(0, -1, 0), 1000)
	selfBilled := overdue(0, 0, now.AddDate(0, -1, 0), 5000)
	selfBilled.SelfBilled = true
	partial := overdue(0, 0, now.AddDate(0, -1, 0), 1000)
	partial.TotalPaidAmount = 400
	usd := overdue(0, 0, now.AddDate(0, -1, 0), 500)
	usd.Currency = analyticsUSD
	other := overdue(1, 0, now.AddDate(0, -1, 0), 999)
	e.create(
		[]*invoice.Invoice{inv1, inv2, inv3, inv4, credited, draftCredit, selfBilled, partial, usd, other}, nil,
		e.creditNote(credited, creditnote.StatusFinalized, 0, 300),
		e.creditNote(draftCredit, creditnote.StatusDraft, 0, 200),
	)

	over := func(m int, c money.Currency, be int, amount int64, invs ...*invoice.Invoice) analytics.Overdue {
		ids := make([]core.InvoiceID, len(invs))
		for i, inv := range invs {
			ids[i] = inv.ID
		}
		// The IDs are ordered, and UUIDv7 IDs order by creation.
		return analytics.Overdue{Month: month(m), Currency: c, Amount: amount, InvoiceIDs: ids, BillingEntityID: e.be[be]}
	}
	for _, c := range []struct {
		name  string
		scope analytics.Scope
		want  []analytics.Overdue
	}{
		// source: spec/models/analytics/overdue_balance_spec.rb:120-139,
		// 189-253 (offsets of finalized credit notes only, payments
		// deducted, self-billed invoices left out)
		{"all", analytics.Scope{Now: now}, []analytics.Overdue{
			over(-2, invoiceEUR, 1, 400, inv4),
			over(-1, invoiceEUR, 0, 100+700+1000+600+999, inv1, credited, draftCredit, partial, other),
			over(-1, analyticsUSD, 0, 500, usd),
		}},
		// source: spec/models/analytics/overdue_balance_spec.rb:170-184
		{"billing entity", analytics.Scope{Now: now, BillingEntityID: e.be[1]}, []analytics.Overdue{over(-2, invoiceEUR, 1, 400, inv4)}},
		// source: spec/models/analytics/overdue_balance_spec.rb:271-290
		{"currency", analytics.Scope{Now: now, Currency: analyticsUSD}, []analytics.Overdue{over(-1, analyticsUSD, 0, 500, usd)}},
		// source: spec/models/analytics/overdue_balance_spec.rb:292-307
		{"customer", analytics.Scope{Now: now, CustomerID: e.customers[1]}, []analytics.Overdue{over(-1, invoiceEUR, 0, 999, other)}},
		// source: spec/models/analytics/overdue_balance_spec.rb:318-326
		{"customer tin empty", analytics.Scope{Now: now, CustomerTINEmpty: ptr(true)}, []analytics.Overdue{
			over(-2, invoiceEUR, 1, 400, inv4),
			over(-1, invoiceEUR, 0, 100+700+1000+600, inv1, credited, draftCredit, partial),
			over(-1, analyticsUSD, 0, 500, usd),
		}},
		// source: spec/models/analytics/overdue_balance_spec.rb:328-336
		{"customer tin set", analytics.Scope{Now: now, CustomerTINEmpty: ptr(false)}, []analytics.Overdue{over(-1, invoiceEUR, 0, 999, other)}},
		{"months", analytics.Scope{Now: now, Months: 1}, []analytics.Overdue{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			equal(t, "OverdueBalance()", report(e, c.scope, analytics.Repository.OverdueBalance), c.want)
		})
	}
}
