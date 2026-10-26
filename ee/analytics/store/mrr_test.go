// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

// mrrAt returns the USD MRR point of period p: starting, ending, new,
// expansion, contraction, churn.
func mrrAt(p int, starting, ending, nw, expansion, contraction, churn string) store.MRRPoint {
	return store.MRRPoint{
		Point: store.Point{Period: p, Currency: "USD"}, Starting: starting, Ending: ending,
		New: nw, Expansion: expansion, Contraction: contraction, Churn: churn,
	}
}

func TestMRR(t *testing.T) {
	e := newEnv(t)
	jan, feb, mar, apr := day(2026, 1, 1), day(2026, 2, 1), day(2026, 3, 1), day(2026, 4, 1)
	tests := []struct {
		name     string
		from, to time.Time // months
		filter   store.Filter
		seed     func(o *org)
		want     []store.MRRPoint
	}{
		{
			name: "monthly fee",
			from: jan, to: mar,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				o.subscriptionFee(c, s, 3000, jan, feb)
			},
			// 3000 over one month is 3000 a month, kept after the fee's
			// period while the subscription is active.
			want: []store.MRRPoint{
				mrrAt(0, "0", "3000", "3000", "0", "0", "0"),
				mrrAt(1, "3000", "3000", "0", "0", "0", "0"),
				mrrAt(2, "3000", "3000", "0", "0", "0", "0"),
			},
		},
		{
			name: "yearly fee",
			from: jan, to: feb,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("yearly", "yearly")})
				o.subscriptionFee(c, s, 120000, jan, day(2027, 1, 1))
			},
			// 120000 / 12 months = 10000.
			want: []store.MRRPoint{
				mrrAt(0, "0", "10000", "10000", "0", "0", "0"),
				mrrAt(1, "10000", "10000", "0", "0", "0", "0"),
			},
		},
		{
			name: "coupons reduce the fee",
			from: jan, to: jan,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				inv := o.invoice(invOpts{Customer: c, Issued: jan, Coupons: 500})
				o.fee(feeOpts{Invoice: inv, Subscription: s, Type: "subscription", Amount: 3000, Coupons: "500", From: jan, To: feb})
			},
			// 3000 - 500 coupons = 2500.
			want: []store.MRRPoint{mrrAt(0, "0", "2500", "2500", "0", "0", "0")},
		},
		{
			name: "termination mid-period churns",
			from: jan, to: mar,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly"), Terminated: at(2026, 2, 15, 10, 0)})
				o.subscriptionFee(c, s, 3000, jan, feb)
			},
			// 3000 from January 1 to February 14; zero from the
			// termination day, February 15: February starts at 3000 and
			// ends at 0, a churn of 3000.
			want: []store.MRRPoint{
				mrrAt(0, "0", "3000", "3000", "0", "0", "0"),
				mrrAt(1, "3000", "0", "0", "0", "0", "3000"),
				mrrAt(2, "0", "0", "0", "0", "0", "0"),
			},
		},
		{
			name: "upgrade expands",
			from: jan, to: mar,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				basic := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly"), Terminated: feb})
				o.subscriptionFee(c, basic, 3000, jan, feb)
				pro := o.subscription(subOpts{Customer: c, Plan: o.plan("pro", "monthly")})
				o.subscriptionFee(c, pro, 5000, feb, mar)
			},
			// The customer's MRR goes from 3000 (basic, to January 31) to
			// 5000 (pro, from February 1): an expansion of 5000 - 3000.
			want: []store.MRRPoint{
				mrrAt(0, "0", "3000", "3000", "0", "0", "0"),
				mrrAt(1, "3000", "5000", "0", "2000", "0", "0"),
				mrrAt(2, "5000", "5000", "0", "0", "0", "0"),
			},
		},
		{
			name: "downgrade contracts",
			from: feb, to: feb,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				pro := o.subscription(subOpts{Customer: c, Plan: o.plan("pro", "monthly"), Terminated: feb})
				o.subscriptionFee(c, pro, 5000, jan, feb)
				basic := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				o.subscriptionFee(c, basic, 3000, feb, mar)
			},
			// 5000 on January 31, 3000 on February 28: a contraction of
			// 2000.
			want: []store.MRRPoint{mrrAt(0, "5000", "3000", "0", "0", "2000", "0")},
		},
		{
			name: "new customer",
			from: feb, to: feb,
			seed: func(o *org) {
				plan := o.plan("basic", "monthly")
				a := o.customer(customerOpts{ExternalID: "a"})
				sa := o.subscription(subOpts{Customer: a, Plan: plan})
				o.subscriptionFee(a, sa, 3000, jan, feb)
				b := o.customer(customerOpts{ExternalID: "b"})
				sb := o.subscription(subOpts{Customer: b, Plan: plan})
				o.subscriptionFee(b, sb, 2000, feb, mar)
			},
			// a carries 3000 through February; b is new with 2000: 3000 +
			// 2000 = 5000 at the end.
			want: []store.MRRPoint{mrrAt(0, "3000", "5000", "2000", "0", "0", "0")},
		},
		{
			name: "carry-forward over a gap",
			from: jan, to: mar,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				o.subscriptionFee(c, s, 3000, jan, feb)
				o.subscriptionFee(c, s, 4500, mar, apr)
			},
			// No fee covers February: it keeps January's 3000. March's fee
			// raises it to 4500, an expansion of 1500.
			want: []store.MRRPoint{
				mrrAt(0, "0", "3000", "3000", "0", "0", "0"),
				mrrAt(1, "3000", "3000", "0", "0", "0", "0"),
				mrrAt(2, "3000", "4500", "0", "1500", "0", "0"),
			},
		},
		{
			name: "finalized invoices only",
			from: jan, to: jan,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				for _, inv := range []invOpts{
					{Status: "draft"},
					{Status: "voided"},
					{SelfBilled: true},
					{DisputeLost: true},
				} {
					inv.Customer, inv.Issued = c, jan
					id := o.invoice(inv)
					o.fee(feeOpts{Invoice: id, Subscription: s, Type: "subscription", Amount: 1000, From: jan, To: feb})
				}
				o.subscriptionFee(c, s, 3000, jan, feb)
			},
			// Only the fee of the finalized invoice counts.
			want: []store.MRRPoint{mrrAt(0, "0", "3000", "3000", "0", "0", "0")},
		},
		{
			name: "customer time zone sets the days",
			from: day(2025, 12, 1), to: jan,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "paris", Timezone: "Europe/Paris"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				// 2025-12-31T23:00Z to 2026-01-31T22:59:59Z is January in
				// Paris (UTC+1).
				o.subscriptionFee(c, s, 3000, at(2025, 12, 31, 23, 0), at(2026, 1, 31, 23, 0))
			},
			// In Paris the MRR starts on January 1: none on December 31.
			want: []store.MRRPoint{
				mrrAt(0, "0", "0", "0", "0", "0", "0"),
				mrrAt(1, "0", "3000", "3000", "0", "0", "0"),
			},
		},
		{
			name: "same instants in UTC",
			from: day(2025, 12, 1), to: jan,
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "utc"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				o.subscriptionFee(c, s, 3000, at(2025, 12, 31, 23, 0), at(2026, 1, 31, 23, 0))
			},
			// In UTC the period is December 31 to January 30, one month
			// (3000) from December 31.
			want: []store.MRRPoint{
				mrrAt(0, "0", "3000", "3000", "0", "0", "0"),
				mrrAt(1, "3000", "3000", "0", "0", "0", "0"),
			},
		},
		{
			name: "currency without MRR",
			from: jan, to: feb,
			filter: store.Filter{Currency: "EUR"},
			seed: func(o *org) {
				c := o.customer(customerOpts{ExternalID: "acme"})
				s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
				o.subscriptionFee(c, s, 3000, jan, feb)
			},
			// The USD MRR is filtered out; EUR gets zero rows.
			want: []store.MRRPoint{
				{Point: store.Point{Period: 0, Currency: "EUR"}, Starting: "0", Ending: "0", New: "0", Expansion: "0", Contraction: "0", Churn: "0"},
				{Point: store.Point{Period: 1, Currency: "EUR"}, Starting: "0", Ending: "0", New: "0", Expansion: "0", Contraction: "0", Churn: "0"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := e.org(t)
			tt.seed(o)
			f := tt.filter
			f.Periods = months(t, tt.from, tt.to)
			got, err := e.store.MRR(e.ctx, o.id, f)
			if err != nil {
				t.Fatalf("MRR() error = %v", err)
			}
			checkRows(t, "MRR()", got, tt.want)
		})
	}
}

func TestMRRByPlan(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	jan, feb := day(2026, 1, 1), day(2026, 2, 1)
	basic, yearly := o.plan("basic", "monthly"), o.plan("yearly", "yearly")
	for _, ext := range []string{"a", "b"} {
		c := o.customer(customerOpts{ExternalID: ext})
		s := o.subscription(subOpts{Customer: c, Plan: basic})
		o.subscriptionFee(c, s, 3000, jan, feb)
	}
	c := o.customer(customerOpts{ExternalID: "c"})
	s := o.subscription(subOpts{Customer: c, Plan: yearly})
	o.subscriptionFee(c, s, 120000, jan, day(2027, 1, 1))

	// On January 31: basic 2 × 3000 = 6000 from 2 of the 3 customers,
	// yearly 120000 / 12 = 10000 from 1; 16000 in all. Shares: 10000 /
	// 16000 = 0.625, 6000 / 16000 = 0.375; 1/3 and 2/3 of the customers.
	yearlyRow := store.PlanMRR{
		Plan:     store.Plan{ID: yearly, Code: "yearly", Name: "Plan yearly", Interval: "yearly"},
		Currency: "USD", MRR: "10000", Share: "0.625", Customers: 1, CustomersShare: "0.333333",
	}
	basicRow := store.PlanMRR{
		Plan:     store.Plan{ID: basic, Code: "basic", Name: "Plan basic", Interval: "monthly"},
		Currency: "USD", MRR: "6000", Share: "0.375", Customers: 2, CustomersShare: "0.666667",
	}
	tests := []struct {
		name      string
		page      store.Page
		want      []store.PlanMRR
		wantTotal int64
	}{
		{name: "all", page: store.Page{Size: 10}, want: []store.PlanMRR{yearlyRow, basicRow}, wantTotal: 2},
		{name: "first page", page: store.Page{Size: 1}, want: []store.PlanMRR{yearlyRow}, wantTotal: 2},
		{name: "second page", page: store.Page{Offset: 1, Size: 1}, want: []store.PlanMRR{basicRow}, wantTotal: 2},
		{name: "past the end", page: store.Page{Offset: 2, Size: 1}, want: nil, wantTotal: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total, err := e.store.MRRByPlan(e.ctx, o.id, store.Filter{Periods: months(t, jan, jan)}, tt.page)
			if err != nil {
				t.Fatalf("MRRByPlan() error = %v", err)
			}
			if total != tt.wantTotal {
				t.Errorf("MRRByPlan() total got = %d, want = %d", total, tt.wantTotal)
			}
			checkRows(t, "MRRByPlan()", got, tt.want)
		})
	}
}
