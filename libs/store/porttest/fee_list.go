package porttest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// feeListSkipped are the cases of the fee list spec that are not ported.
var feeListSkipped = []skippedCase{
	{"spec/queries/fees_query_spec.rb:44", "offset pagination (page, current_page); the cursor pagination is checked by the fee tests"},
	{"spec/queries/fees_query_spec.rb:242", "an invalid created_at_from string is a parsing error of the API layer; Go filters are typed (time.Time)"},
	{"spec/queries/fees_query_spec.rb:288", "an invalid succeeded_at_from string is a parsing error of the API layer; Go filters are typed (time.Time)"},
	{"spec/queries/fees_query_spec.rb:334", "an invalid failed_at_from string is a parsing error of the API layer; Go filters are typed (time.Time)"},
	{"spec/queries/fees_query_spec.rb:380", "an invalid refunded_at_from string is a parsing error of the API layer; Go filters are typed (time.Time)"},
}

// testFeeList checks the filters and the order of FeeRepository.List and
// FeeRepository.Discard.
//
// source: spec/queries/fees_query_spec.rb
func testFeeList(t *testing.T, h Harness) {
	checkSkipped(t, feeListSkipped)
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv := invoiceFixtureMinimal(e, p, now)
	inv.CustomerID = p.customers[1]
	invoiceFixtureCreate(e, inv)

	metric := catalogFixtureMetric(e, p.org, "fee_list_calls")
	plan := catalogFixturePlan(e, p.org, "fee-list-plan")
	charge := catalogFixtureCharge(e, p.org, plan.ID, metric.ID, "fee_list_calls")
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, metric))
		noErr(t, "Plans.Create()", r.Plans.Create(ctx, plan))
		noErr(t, "Charges.Create()", r.Charges.Create(ctx, charge))
		return nil
	})

	usd := money.MustCurrency("USD")
	// sub: an unattached fee of subscription 0 (customer 0).
	sub := invoiceFixtureFee(e, p, core.InvoiceID{}, now)
	sub.SubscriptionID = p.subscriptions[0]
	sub.ChargeID = charge.ID
	sub.PaymentStatus = invoice.FeePaymentStatusSucceeded
	sub.SucceededAt = ptr(at(time.Hour))
	sub.PayInAdvanceEventTransactionID = "transaction-id"
	// addOn: an add-on fee of the invoice of customer 1.
	addOn := invoiceFixtureFee(e, p, inv.ID, at(time.Minute))
	addOn.Type = core.FeeTypeAddOn
	addOn.PaymentStatus = invoice.FeePaymentStatusFailed
	addOn.FailedAt = ptr(at(2 * time.Hour))
	// regen: a regenerated fee of the same event transaction, in USD.
	regen := invoiceFixtureFee(e, p, core.InvoiceID{}, at(2*time.Minute))
	regen.SubscriptionID = p.subscriptions[1]
	regen.Currency = usd
	regen.PaymentStatus = invoice.FeePaymentStatusRefunded
	regen.RefundedAt = ptr(at(3 * time.Hour))
	regen.PayInAdvanceEventTransactionID = "transaction-id"
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, f := range []*invoice.Fee{sub, addOn, regen} {
			noErr(t, "Fees.Create()", r.Fees.Create(ctx, f))
		}
		return nil
	})

	list := func(f invoice.FeeFilter) ([]core.FeeID, error) {
		var got []core.FeeID
		err := e.txErr(func(ctx context.Context, r port.Repos) error {
			page, err := r.Fees.List(ctx, p.org, f, core.PageRequest{Size: 10})
			got = ids(page.Items, invoiceFeeID)
			return err
		})
		return got, err
	}
	all := []core.FeeID{sub.ID, addOn.ID, regen.ID}
	for _, c := range []struct {
		name   string
		filter invoice.FeeFilter
		want   []core.FeeID
	}{
		// source: spec/queries/fees_query_spec.rb:19
		{"none", invoice.FeeFilter{}, all},
		// source: spec/queries/fees_query_spec.rb:56
		{"subscription external id", invoice.FeeFilter{SubscriptionExternalID: "inv-sub-0"}, []core.FeeID{sub.ID}},
		{"subscription external id unknown", invoice.FeeFilter{SubscriptionExternalID: "unknown"}, nil},
		// source: spec/queries/fees_query_spec.rb:67
		{"customer external id of a subscription", invoice.FeeFilter{CustomerExternalID: "inv-cust-0"}, []core.FeeID{sub.ID, regen.ID}},
		// source: spec/queries/fees_query_spec.rb:77
		{"customer external id of an invoice", invoice.FeeFilter{CustomerExternalID: "inv-cust-1"}, []core.FeeID{addOn.ID}},
		{"customer id", invoice.FeeFilter{CustomerID: p.customers[1]}, []core.FeeID{addOn.ID}},
		// source: spec/queries/fees_query_spec.rb:94
		{"currency", invoice.FeeFilter{Currency: usd}, []core.FeeID{regen.ID}},
		// source: spec/queries/fees_query_spec.rb:105
		{"billable metric code", invoice.FeeFilter{BillableMetricCode: "fee_list_calls"}, []core.FeeID{sub.ID}},
		{"billable metric code unknown", invoice.FeeFilter{BillableMetricCode: "unknown"}, nil},
		// source: spec/queries/fees_query_spec.rb:122
		{"fee type", invoice.FeeFilter{Types: []core.FeeType{core.FeeTypeAddOn}}, []core.FeeID{addOn.ID}},
		// source: spec/queries/fees_query_spec.rb:145
		{"payment status", invoice.FeeFilter{PaymentStatuses: []invoice.FeePaymentStatus{invoice.FeePaymentStatusFailed}}, []core.FeeID{addOn.ID}},
		// source: spec/queries/fees_query_spec.rb:168-209
		{"event transaction id", invoice.FeeFilter{EventTransactionID: "transaction-id"}, []core.FeeID{sub.ID, regen.ID}},
		// source: spec/queries/fees_query_spec.rb:211-240
		{"created at", invoice.FeeFilter{CreatedFrom: at(-time.Minute), CreatedTo: at(time.Minute)}, []core.FeeID{sub.ID, addOn.ID}},
		{"created at not covered", invoice.FeeFilter{CreatedFrom: at(-2 * time.Minute), CreatedTo: at(-2 * time.Minute)}, nil},
		// source: spec/queries/fees_query_spec.rb:255-286
		{"succeeded at", invoice.FeeFilter{SucceededFrom: at(59 * time.Minute), SucceededTo: at(61 * time.Minute)}, []core.FeeID{sub.ID}},
		{"succeeded at not covered", invoice.FeeFilter{SucceededTo: at(time.Minute)}, nil},
		// source: spec/queries/fees_query_spec.rb:301-332
		{"failed at", invoice.FeeFilter{FailedFrom: at(time.Hour)}, []core.FeeID{addOn.ID}},
		{"failed at not covered", invoice.FeeFilter{FailedFrom: at(3 * time.Hour)}, nil},
		// source: spec/queries/fees_query_spec.rb:347-378
		{"refunded at", invoice.FeeFilter{RefundedFrom: at(time.Hour), RefundedTo: at(4 * time.Hour)}, []core.FeeID{regen.ID}},
		{"refunded at not covered", invoice.FeeFilter{RefundedTo: at(time.Hour)}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := list(c.filter)
			noErr(t, "List()", err)
			equal(t, "List() IDs", got, c.want)
		})
	}

	// source: spec/queries/fees_query_spec.rb:132,155
	for _, f := range []invoice.FeeFilter{
		{Types: []core.FeeType{"unknown"}},
		{PaymentStatuses: []invoice.FeePaymentStatus{"unknown"}},
	} {
		_, err := list(f)
		wantErr(t, "List() invalid", err, core.ErrInvalidValue)
	}

	// Another organization sees none of the fees.
	_, be2 := e.org("Other")
	noErr(t, "InTx()", e.txErr(func(ctx context.Context, r port.Repos) error {
		page, err := r.Fees.List(ctx, be2.OrganizationID, invoice.FeeFilter{}, core.PageRequest{Size: 10})
		equal(t, "List() other organization", len(page.Items), 0)
		return err
	}))

	// Oldest first, then by ID (fees_query.rb:33), across pages: early is
	// created after the others but dated before them.
	early := invoiceFixtureFee(e, p, core.InvoiceID{}, at(-time.Minute))
	early.SubscriptionID = p.subscriptions[0]
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Fees.Create(ctx, early) })
	var paged []core.FeeID
	req := core.PageRequest{Size: 1}
	for range 5 {
		noErr(t, "List() page", e.txErr(func(ctx context.Context, r port.Repos) error {
			page, err := r.Fees.List(ctx, p.org, invoice.FeeFilter{}, req)
			paged = append(paged, ids(page.Items, invoiceFeeID)...)
			req.Token = page.NextToken
			return err
		}))
		if req.Token == "" {
			break
		}
	}
	equal(t, "List() pages", paged, []core.FeeID{early.ID, sub.ID, addOn.ID, regen.ID})

	discard := func(org core.OrganizationID, id core.FeeID) error {
		return e.txErr(func(ctx context.Context, r port.Repos) error { return r.Fees.Discard(ctx, org, id, at(time.Hour)) })
	}
	wantErr(t, "Discard() missing", discard(p.org, core.NewID[core.FeeID](e.ids)), core.ErrNotFound)
	wantErr(t, "Discard() other organization", discard(be2.OrganizationID, sub.ID), core.ErrNotFound)
	wantErr(t, "Discard() invoiced", discard(p.org, addOn.ID), core.ErrConflict)
	noErr(t, "Discard()", discard(p.org, sub.ID))
	wantErr(t, "Discard() again", discard(p.org, sub.ID), core.ErrNotFound)
	// Fee's default scope skips discarded fees (app/models/fee.rb:9).
	got, err := list(invoice.FeeFilter{})
	noErr(t, "List() after Discard()", err)
	equal(t, "List() after Discard()", got, []core.FeeID{early.ID, addOn.ID, regen.ID})
	noErr(t, "reads after Discard()", e.txErr(func(ctx context.Context, r port.Repos) error {
		_, err := r.Fees.Get(ctx, p.org, sub.ID)
		wantErr(t, "Get() discarded", err, core.ErrNotFound)
		byTx, err := r.Fees.ListByEventTransaction(ctx, p.org, sub.SubscriptionID, "transaction-id")
		noErr(t, "ListByEventTransaction()", err)
		equal(t, "ListByEventTransaction() after Discard()", len(byTx), 0)
		byCharge, err := r.Fees.ListByCharge(ctx, p.org, charge.ID)
		noErr(t, "ListByCharge()", err)
		equal(t, "ListByCharge() after Discard()", len(byCharge), 0)
		return nil
	}))
}

// feesOldestFirst returns fees in the order of FeeRepository.List: oldest
// first, then by ID (fees_query.rb:33).
func feesOldestFirst(fees []*invoice.Fee) []*invoice.Fee {
	return slices.SortedStableFunc(slices.Values(fees), func(a, b *invoice.Fee) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID)
	})
}
