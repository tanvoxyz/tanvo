// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"strconv"
	"testing"

	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

func TestPrepaidCredits(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	a := o.customer(customerOpts{ExternalID: "a"})
	paris := o.customer(customerOpts{ExternalID: "paris", Timezone: "Europe/Paris"})
	w := o.wallet(a, "USD")
	o.walletTx(w, "inbound", "purchased", "settled", 1000, "10", at(2026, 1, 5, 9, 0))
	o.walletTx(w, "inbound", "granted", "settled", 500, "5", at(2026, 1, 6, 9, 0))
	o.walletTx(w, "inbound", "purchased", "pending", 9999, "99", at(2026, 1, 7, 9, 0))
	o.walletTx(w, "inbound", "purchased", "failed", 8888, "88", at(2026, 1, 7, 9, 0))
	o.walletTx(w, "outbound", "invoiced", "settled", 300, "3", at(2026, 2, 1, 9, 0))
	o.walletTx(w, "outbound", "voided", "settled", 100, "1", at(2026, 2, 2, 9, 0))
	// Settled on January 31 at 23:30 UTC, February 1 in Paris.
	wp := o.wallet(paris, "USD")
	o.walletTx(wp, "inbound", "purchased", "settled", 2000, "20", at(2026, 1, 31, 23, 30))

	got, err := e.store.PrepaidCredits(e.ctx, o.id, store.Filter{Periods: months(t, day(2026, 1, 1), day(2026, 2, 1))})
	if err != nil {
		t.Fatalf("PrepaidCredits() error = %v", err)
	}
	// January: the settled purchase and grant; pending and failed
	// transactions are not counted. February: Paris's purchase, the
	// consumption and the void.
	want := []store.PrepaidPoint{
		{
			Point:           store.Point{Period: 0, Currency: "USD"},
			PurchasedAmount: "1000", PurchasedCredits: "10", OfferedAmount: "500", OfferedCredits: "5",
			ConsumedAmount: "0", ConsumedCredits: "0", VoidedAmount: "0", VoidedCredits: "0",
		},
		{
			Point:           store.Point{Period: 1, Currency: "USD"},
			PurchasedAmount: "2000", PurchasedCredits: "20", OfferedAmount: "0", OfferedCredits: "0",
			ConsumedAmount: "300", ConsumedCredits: "3", VoidedAmount: "100", VoidedCredits: "1",
		},
	}
	checkRows(t, "PrepaidCredits()", got, want)
}

func TestUsage(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	o.metric("api")
	o.run("UPDATE billable_metrics SET recurring = true WHERE organization_id = $1 AND id = $2", o.id, o.metric("storage"))
	c := o.customer(customerOpts{ExternalID: "a"})
	s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
	usage := func(code, units string, amount int) string {
		return `{"billable_metric": {"code": "` + code + `"}, "units": "` + units + `", "amount_cents": ` +
			strconv.Itoa(amount) + `, "amount_currency": "USD"}`
	}
	o.dailyUsage(c, s, day(2026, 1, 10), `{"currency": "USD", "charges_usage": [`+usage("api", "10", 100)+`, `+usage("storage", "2.5", 50)+`]}`)
	// An empty diff (the first day of a history fill) and a snapshot
	// without charges, whose nil slice the core stores as null.
	o.dailyUsage(c, s, day(2026, 1, 15), `{}`)
	o.dailyUsage(c, s, day(2026, 1, 16), `{"currency": "USD", "amount_cents": 0, "charges_usage": null}`)
	o.dailyUsage(c, s, day(2026, 1, 20), `{"currency": "USD", "charges_usage": [`+usage("api", "5", 50)+`]}`)
	// legacy is no billable metric any more: deleted.
	o.dailyUsage(c, s, day(2026, 2, 3), `{"currency": "USD", "charges_usage": [`+usage("api", "1", 10)+`, `+usage("legacy", "7", 70)+`]}`)

	f := store.Filter{Periods: months(t, day(2026, 1, 1), day(2026, 2, 1))}
	point := func(p int, code string, deleted bool, units, amount string) store.UsagePoint {
		return store.UsagePoint{Point: store.Point{Period: p, Currency: "USD"}, MetricCode: code, MetricDeleted: deleted, Units: units, Amount: amount}
	}
	recurring := true
	tests := []struct {
		name  string
		u     store.UsageFilter
		total bool
		want  []store.UsagePoint
	}{
		{
			name: "by metric",
			// api: 10 + 5 units and 100 + 50 in January; storage 2.5 for
			// 50; February: api 1 for 10, legacy 7 for 70. By currency,
			// code and period.
			want: []store.UsagePoint{
				point(0, "api", false, "15", "150"),
				point(1, "api", false, "1", "10"),
				point(1, "legacy", true, "7", "70"),
				point(0, "storage", false, "2.5", "50"),
			},
		},
		{name: "one metric", u: store.UsageFilter{MetricCode: "api"}, want: []store.UsagePoint{
			point(0, "api", false, "15", "150"),
			point(1, "api", false, "1", "10"),
		}},
		{name: "recurring metrics", u: store.UsageFilter{Recurring: &recurring}, want: []store.UsagePoint{
			point(0, "storage", false, "2.5", "50"),
		}},
		{
			name: "total", total: true,
			// January 150 + 50, February 10 + 70; totals carry no units.
			want: []store.UsagePoint{
				point(0, "", false, "0", "200"),
				point(1, "", false, "0", "80"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.store.Usage(e.ctx, o.id, f, tt.u, tt.total)
			if err != nil {
				t.Fatalf("Usage() error = %v", err)
			}
			checkRows(t, "Usage()", got, tt.want)
		})
	}
}

func TestInvoicedUsage(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	jan, feb, mar := day(2026, 1, 1), day(2026, 2, 1), day(2026, 3, 1)
	plan := o.plan("basic", "monthly")
	api := o.charge(plan, o.metric("api"))
	storage := o.charge(plan, o.metric("storage"))
	westFilter := o.chargeFilter(api, `{"region": ["eu", "us"]}`)
	eastFilter := o.chargeFilter(api, `{"region": ["asia"]}`)
	c := o.customer(customerOpts{ExternalID: "a"})
	s := o.subscription(subOpts{Customer: c, Plan: plan})
	// Coupons do not reduce invoiced usage: it is the fees' amounts.
	i1 := o.invoice(invOpts{Customer: c, Issued: day(2026, 1, 10), Coupons: 100})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "charge", Amount: 100, Charge: api, ChargeFilter: westFilter, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "charge", Amount: 200, Charge: api, ChargeFilter: eastFilter, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "charge", Amount: 400, Charge: api, GroupedBy: `{"tier": "gold"}`, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "charge", Amount: 50, Charge: storage, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "subscription", Amount: 3000, From: jan, To: feb})
	i2 := o.invoice(invOpts{Customer: c, Issued: day(2026, 2, 10)})
	o.fee(feeOpts{Invoice: i2, Subscription: s, Type: "charge", Amount: 30, Charge: api, From: feb, To: mar})

	f := store.Filter{Periods: months(t, jan, feb)}
	point := func(p int, code, amount string) store.InvoicedUsagePoint {
		return store.InvoicedUsagePoint{Point: store.Point{Period: p, Currency: "USD"}, MetricCode: code, Amount: amount}
	}
	tests := []struct {
		name string
		u    store.InvoicedUsageFilter
		want []store.InvoicedUsagePoint
	}{
		{
			// api: 100 + 200 + 400 in January, 30 in February; storage 50.
			// The subscription fee is no usage.
			name: "all",
			want: []store.InvoicedUsagePoint{point(0, "api", "700"), point(1, "api", "30"), point(0, "storage", "50")},
		},
		{name: "metric", u: store.InvoicedUsageFilter{MetricCode: "storage"}, want: []store.InvoicedUsagePoint{point(0, "storage", "50")}},
		{name: "grouped by", u: store.InvoicedUsageFilter{GroupedBy: map[string]string{"tier": "gold"}}, want: []store.InvoicedUsagePoint{point(0, "api", "400")}},
		{name: "filter values", u: store.InvoicedUsageFilter{FilterValues: map[string]string{"region": "eu"}}, want: []store.InvoicedUsagePoint{point(0, "api", "100")}},
		{name: "filter values matching none", u: store.InvoicedUsageFilter{FilterValues: map[string]string{"region": "mars"}}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.store.InvoicedUsage(e.ctx, o.id, f, tt.u)
			if err != nil {
				t.Fatalf("InvoicedUsage() error = %v", err)
			}
			checkRows(t, "InvoicedUsage()", got, tt.want)
		})
	}
}

func TestRecognition(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	c := o.customer(customerOpts{ExternalID: "a"})
	s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
	// 3000 for January 16 to February 15 (31 days), issued January 16.
	i1 := o.invoice(invOpts{Customer: c, Issued: day(2026, 1, 16)})
	o.fee(feeOpts{Invoice: i1, Subscription: s, Type: "subscription", Amount: 3000, From: day(2026, 1, 16), To: day(2026, 2, 16)})
	// 600 without a service period, issued February 10.
	i2 := o.invoice(invOpts{Customer: c, Issued: day(2026, 2, 10), Type: "one_off"})
	o.fee(feeOpts{Invoice: i2, Type: "add_on", Amount: 600})

	got, err := e.store.Recognition(e.ctx, o.id, store.Filter{Periods: months(t, day(2026, 1, 1), day(2026, 3, 1))})
	if err != nil {
		t.Fatalf("Recognition() error = %v", err)
	}
	want := []store.RecognitionPoint{
		// January: 16 of the 31 days, 3000 × 16 / 31 = 1548.387097;
		// deferred 3000 - 1548.387097.
		{Point: store.Point{Period: 0, Currency: "USD"}, Invoiced: "3000", Recognized: "1548.387097", Deferred: "1451.612903"},
		// February: the other 15 days, 3000 × 15 / 31 = 1451.612903, and
		// the 600 on its issuing day; nothing deferred.
		{Point: store.Point{Period: 1, Currency: "USD"}, Invoiced: "600", Recognized: "2051.612903", Deferred: "0"},
		{Point: store.Point{Period: 2, Currency: "USD"}, Invoiced: "0", Recognized: "0", Deferred: "0"},
	}
	checkRows(t, "Recognition()", got, want)
}
