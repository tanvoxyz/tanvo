package porttest

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// ViewQuery runs query with args through tr, in a transaction bound to org,
// and returns the first column of each row as text (NULL as "<nil>").
type ViewQuery func(t *testing.T, tr port.Transactor, org core.OrganizationID, query string, args ...any) []string

// ExportViews are the export views of parity §1.43.
var ExportViews = []string{
	"exports_alert_thresholds", "exports_applied_coupons", "exports_billable_metrics", "exports_billing_entities",
	"exports_charges", "exports_coupons", "exports_credit_note_applied_taxes", "exports_credit_notes",
	"exports_customers", "exports_daily_usages", "exports_entitlements", "exports_features", "exports_fees",
	"exports_integration_customers", "exports_invoice_applied_taxes", "exports_invoice_settlements",
	"exports_invoice_subscriptions", "exports_invoices", "exports_payment_requests", "exports_payments",
	"exports_plans", "exports_record_deletions", "exports_subscriptions", "exports_taxes",
	"exports_triggered_alerts", "exports_usage_thresholds", "exports_wallet_transaction_consumptions",
	"exports_wallet_transactions", "exports_wallets",
}

// RunExportViews runs the export view tests (parity §1.43) against h, whose
// SQL q runs. The views are SQL of the PostgreSQL store, so the conformance
// suite [Run] leaves them out.
func RunExportViews(t *testing.T, h Harness, q ViewQuery) {
	for _, g := range []struct {
		name string
		run  func(*testing.T, Harness, ViewQuery)
	}{
		{"isolation", testExportViewsIsolation},
		{"invoices", testExportViewsInvoices},
		{"fees", testExportViewsFees},
		{"credit notes", testExportViewsCreditNotes},
		{"payments", testExportViewsPayments},
		{"record deletions", testExportViewsRecordDeletions},
	} {
		t.Run(g.name, func(t *testing.T) {
			if h.Concurrent {
				t.Parallel()
			}
			g.run(t, h, q)
		})
	}
}

// testExportViewsIsolation checks that every export view exists and shows
// the rows of the bound organization only (security_invoker views).
func testExportViewsIsolation(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	pa := invoiceFixtureParents(e, a)
	invoiceFixtureParents(e, b)
	got := q(t, e.tr, a.OrganizationID, `SELECT table_name::text FROM information_schema.views
		WHERE table_schema = 'public' AND table_name LIKE 'exports\_%' ORDER BY 1`)
	equal(t, "export views", got, ExportViews)
	for _, v := range ExportViews {
		others := q(t, e.tr, a.OrganizationID, fmt.Sprintf(`SELECT count(*)::text FROM %s WHERE organization_id <> $1`, v), a.OrganizationID.UUID())
		equal(t, v+" rows of another organization", others, []string{"0"})
	}
	customers := q(t, e.tr, a.OrganizationID, `SELECT id::text FROM exports_customers ORDER BY id`)
	equal(t, "exports_customers", customers, sortedIDs(pa.customers[:]...))
}

// testExportViewsInvoices checks the statuses exported and the derived
// amount due (db/views/exports_invoices_v04.sql).
func testExportViewsInvoices(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	byStatus := map[string]*invoice.Invoice{}
	var invs []*invoice.Invoice
	for _, s := range []struct {
		name   string
		status invoice.Status
		tax    invoice.TaxStatus
	}{
		{"draft", invoice.StatusDraft, ""},
		{"finalized", invoice.StatusFinalized, ""},
		{"voided", invoice.StatusVoided, ""},
		{"failed", invoice.StatusFailed, invoice.TaxStatusFailed},
		{"deleted", invoice.StatusDeleted, ""},
		{"generating", invoice.StatusGenerating, ""},
		{"closed", invoice.StatusClosed, ""},
		{"open", invoice.StatusOpen, ""},
		{"open taxes pending", invoice.StatusOpen, invoice.TaxStatusPending},
	} {
		inv := invoiceFixtureMinimal(e, p, now)
		inv.Status, inv.TaxStatus = s.status, s.tax
		inv.TotalAmount, inv.TotalPaidAmount = 1000, 400
		byStatus[s.name] = inv
		invs = append(invs, inv)
	}
	invoiceFixtureCreate(e, invs...)
	got := q(t, e.tr, p.org, `SELECT id::text FROM exports_invoices ORDER BY id`)
	equal(t, "exported invoices", got, sortedIDs(
		byStatus["draft"].ID, byStatus["finalized"].ID, byStatus["voided"].ID, byStatus["failed"].ID,
		byStatus["deleted"].ID, byStatus["open taxes pending"].ID,
	))
	due := q(t, e.tr, p.org, `SELECT total_due_amount::text FROM exports_invoices WHERE id = $1`, byStatus["draft"].ID.UUID())
	equal(t, "total_due_amount", due, []string{"600"})
}

// testExportViewsFees checks the fees exported, their customer and item,
// and the rows of deleted invoices left out
// (db/views/exports_fees_v04.sql, exports_invoice_subscriptions_v02.sql,
// exports_invoices_taxes_v03.sql).
func testExportViewsFees(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	live, deleted, oneOff := invoiceFixtureMinimal(e, p, now), invoiceFixtureMinimal(e, p, now), invoiceFixtureMinimal(e, p, now)
	deleted.Status = invoice.StatusDeleted
	oneOff.CustomerID, oneOff.Type = p.customers[1], invoice.TypeOneOff
	invoiceFixtureCreate(e, live, deleted, oneOff)
	addOn := catalogFixtureAddOn(e, p.org, "setup")
	sub := invoiceFixtureFee(e, p, live.ID, now)
	sub.Type, sub.SubscriptionID = core.FeeTypeSubscription, p.subscriptions[0]
	sub.Properties = invoice.FeeProperties{From: now, To: at(time.Hour), Timestamp: now}
	ofDeleted := invoiceFixtureFee(e, p, deleted.ID, now)
	addOnFee := invoiceFixtureFee(e, p, oneOff.ID, now)
	addOnFee.Type, addOnFee.AddOnID = core.FeeTypeAddOn, addOn.ID
	uninvoiced := invoiceFixtureFee(e, p, core.InvoiceID{}, now)
	uninvoiced.SubscriptionID = p.subscriptions[1]
	subOf := func(inv core.InvoiceID) *invoice.Subscription {
		return &invoice.Subscription{
			ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: inv,
			SubscriptionID: p.subscriptions[0], Reason: invoice.InvoicingReasonSubscriptionPeriodic,
			Period: period.Period{From: now, To: at(time.Hour)}, Timestamp: now, CreatedAt: now,
		}
	}
	taxOf := func(inv core.InvoiceID) *invoice.AppliedTax {
		return &invoice.AppliedTax{
			ID: core.NewID[core.InvoiceAppliedTaxID](e.ids), OrganizationID: p.org, InvoiceID: inv, TaxCode: "vat", TaxName: "VAT",
			TaxRate: money.MustParse("20"), PreciseAmount: money.MustParse("0"), Currency: invoiceEUR, CreatedAt: now,
		}
	}
	liveSub, deletedSub := subOf(live.ID), subOf(deleted.ID)
	liveTax := taxOf(live.ID)
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, addOn))
		for _, f := range []*invoice.Fee{sub, ofDeleted, addOnFee, uninvoiced} {
			noErr(t, "Fees.Create()", r.Fees.Create(ctx, f))
		}
		for _, s := range []*invoice.Subscription{liveSub, deletedSub} {
			noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, s))
		}
		noErr(t, "ReplaceForInvoice()", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, deleted.ID, []*invoice.AppliedTax{taxOf(deleted.ID)}))
		return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, live.ID, []*invoice.AppliedTax{liveTax})
	})

	equal(t, "exported fees", q(t, e.tr, p.org, `SELECT id::text FROM exports_fees ORDER BY id`), sortedIDs(sub.ID, addOnFee.ID, uninvoiced.ID))
	// The customer is the subscription's, else the invoice's.
	row := func(f *invoice.Fee, cols string) []string {
		return q(t, e.tr, p.org, `SELECT concat_ws('|', `+cols+`) FROM exports_fees WHERE id = $1`, f.ID.UUID())
	}
	equal(t, "subscription fee", row(sub, "customer_id, item->>'item_type', item->>'item_id', from_datetime IS NOT NULL"),
		[]string{fmt.Sprintf("%s|subscription|%s|t", p.customers[0], p.subscriptions[0])})
	equal(t, "add-on fee", row(addOnFee, "customer_id, item->>'item_type', item->>'item_id', item->>'code', item->>'name', from_datetime IS NULL"),
		[]string{fmt.Sprintf("%s|add_on|%s|setup|Add-on setup|t", p.customers[1], addOn.ID)})
	equal(t, "exported invoice subscriptions", q(t, e.tr, p.org, `SELECT id::text FROM exports_invoice_subscriptions`), []string{liveSub.ID.String()})
	equal(t, "exported invoice taxes", q(t, e.tr, p.org, `SELECT id::text FROM exports_invoice_applied_taxes`), []string{liveTax.ID.String()})
}

// testExportViewsCreditNotes checks that deleted credit notes and their
// taxes are left out (db/views/exports_credit_notes_v05.sql).
func testExportViewsCreditNotes(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := creditNoteFixtureParents(e, be)
	kept := creditNoteFixtureFull(e, p, "CN-1")
	gone := creditNoteFixtureMinimal(e, p, now)
	gone.Status = creditnote.StatusDeleted
	gone.AppliedTaxes = slices.Clone(kept.AppliedTaxes)
	for i := range gone.AppliedTaxes {
		gone.AppliedTaxes[i].ID = core.NewID[core.CreditNoteAppliedTaxID](e.ids)
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "CreditNotes.Create()", r.CreditNotes.Create(ctx, kept))
		return r.CreditNotes.Create(ctx, gone)
	})
	equal(t, "exported credit notes", q(t, e.tr, p.org, `SELECT id::text FROM exports_credit_notes`), []string{kept.ID.String()})
	taxes := q(t, e.tr, p.org, `SELECT DISTINCT credit_note_id::text FROM exports_credit_note_applied_taxes`)
	equal(t, "exported credit note taxes", taxes, []string{kept.ID.String()})
	items := q(t, e.tr, p.org, `SELECT json_array_length(items)::text FROM exports_credit_notes WHERE id = $1`, kept.ID.UUID())
	equal(t, "credit note items", items, []string{fmt.Sprint(len(kept.Items))})
}

// testExportViewsPayments checks the invoices of payments, of an invoice or
// of a payment request (db/views/exports_payments_v01.sql,
// exports_payment_requests_v01.sql).
func testExportViewsPayments(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := paymentFixtureParents(e, be)
	req := paymentFixtureRequest(e, p, p.invoices[1], p.invoices[2])
	direct := paymentFixtureManual(e, p, p.invoices[0])
	viaRequest := paymentFixtureManual(e, p, core.InvoiceID{})
	viaRequest.Payable = payment.RequestPayable(req.ID)
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "PaymentRequests.Create()", r.PaymentRequests.Create(ctx, req))
		noErr(t, "Payments.Create() of an invoice", r.Payments.Create(ctx, direct))
		return r.Payments.Create(ctx, viaRequest)
	})
	invoices := func(view string, id fmt.Stringer) []string {
		return q(t, e.tr, p.org, `SELECT invoice_ids::text FROM `+view+` WHERE id::text = $1`, id.String())
	}
	equal(t, "invoices of an invoice payment", invoices("exports_payments", direct.ID), []string{fmt.Sprintf(`["%s"]`, p.invoices[0])})
	both := []string{fmt.Sprintf(`["%s","%s"]`, p.invoices[1], p.invoices[2])}
	equal(t, "invoices of a request payment", invoices("exports_payments", viaRequest.ID), both)
	equal(t, "invoices of the request", invoices("exports_payment_requests", req.ID), both)
	payments := q(t, e.tr, p.org, `SELECT payment_ids::text FROM exports_payment_requests WHERE id = $1`, req.ID.UUID())
	equal(t, "payments of the request", payments, []string{fmt.Sprintf(`["%s"]`, viaRequest.ID)})
}

// testExportViewsRecordDeletions checks the deletion records view
// (db/views/exports_record_deletions_v01.sql).
func testExportViewsRecordDeletions(t *testing.T, h Harness, q ViewQuery) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	draft := invoiceFixtureMinimal(e, p, now)
	invoiceFixtureCreate(e, draft)
	fee := invoiceFixtureFee(e, p, draft.ID, now)
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Fees.Create(ctx, fee) })
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Invoices.DeleteDraft(ctx, p.org, draft.ID) })
	got := q(t, e.tr, p.org, `SELECT concat_ws('|', table_name, record_id) FROM exports_record_deletions`)
	equal(t, "exported deletions", got, []string{"fees|" + fee.ID.String()})
}

// sortedIDs returns the text of ids in UUID order, as PostgreSQL sorts them.
func sortedIDs[I fmt.Stringer](ids ...I) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	slices.Sort(out)
	return out
}
