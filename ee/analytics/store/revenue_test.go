// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

// zeroRevenue returns the revenue point of period p in currency c without
// revenue.
func zeroRevenue(p int, c string) store.RevenuePoint {
	return store.RevenuePoint{
		Point: store.Point{Period: p, Currency: c}, Subscription: "0", UsageBased: "0", Commitment: "0", OneOff: "0",
		Gross: "0", Coupons: "0", CreditNotes: "0", FreeCredits: "0", Progressive: "0", Contra: "0", Net: "0", Prepaid: "0",
	}
}

// subscriptionRevenue returns the revenue point of period p in currency c
// made of subscription fees of amount, without credits.
func subscriptionRevenue(p int, c, amount string) store.RevenuePoint {
	r := zeroRevenue(p, c)
	r.Subscription, r.Gross, r.Net = amount, amount, amount
	return r
}

func TestRevenue(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	jan, feb, mar := day(2026, 1, 1), day(2026, 2, 1), day(2026, 3, 1)
	basic, pro := o.plan("basic", "monthly"), o.plan("pro", "monthly")
	a := o.customer(customerOpts{ExternalID: "a", Type: "company", TIN: "FR123"})
	b := o.customer(customerOpts{ExternalID: "b", Type: "individual"})
	subA := o.subscription(subOpts{Customer: a, Plan: basic})
	subB := o.subscription(subOpts{Customer: b, Plan: pro})

	// a's January invoice: 5000 of fees and 2000 of credits that reduce
	// revenue (500 + 250 + 1000 + 250), plus 2000 of purchased credits.
	i1 := o.invoice(invOpts{
		Customer: a, Issued: day(2026, 1, 10),
		Coupons: 500, CreditNotes: 250, PrepaidGranted: 1000, PrepaidPurchased: 2000, Progressive: 250,
	})
	o.fee(feeOpts{Invoice: i1, Subscription: subA, Type: "subscription", Amount: 3000, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: subA, Type: "charge", Amount: 1000, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Subscription: subA, Type: "commitment", Amount: 500, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i1, Type: "add_on", Amount: 500})
	// b's January invoice, without credits; the product fee has no
	// subscription.
	i2 := o.invoice(invOpts{Customer: b, Issued: day(2026, 1, 20)})
	o.fee(feeOpts{Invoice: i2, Subscription: subB, Type: "subscription", Amount: 2000, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i2, Subscription: subB, Type: "fixed_charge", Amount: 400, From: jan, To: feb})
	o.fee(feeOpts{Invoice: i2, Type: "product", Amount: 100})
	// A pay-in-advance fee outside invoices counts on its creation day; a
	// fee outside invoices not paid in advance does not.
	o.fee(feeOpts{Subscription: subB, Type: "charge", Amount: 300, PayInAdvance: true, Created: at(2026, 1, 25, 12, 0)})
	o.fee(feeOpts{Subscription: subB, Type: "charge", Amount: 999, Created: at(2026, 1, 25, 12, 0)})
	// Credit purchases and fees of invoices not finalized are no revenue.
	i3 := o.invoice(invOpts{Customer: a, Issued: day(2026, 1, 15), Type: "credit"})
	o.fee(feeOpts{Invoice: i3, Type: "credit", Amount: 7000})
	i4 := o.invoice(invOpts{Customer: a, Issued: day(2026, 1, 15), Status: "draft"})
	o.fee(feeOpts{Invoice: i4, Subscription: subA, Type: "subscription", Amount: 888, From: jan, To: feb})
	// EUR in January, USD in February.
	i5 := o.invoice(invOpts{Customer: a, Issued: day(2026, 1, 5), Currency: "EUR"})
	o.fee(feeOpts{Invoice: i5, Subscription: subA, Type: "subscription", Amount: 1500, Currency: "EUR", From: jan, To: feb})
	i6 := o.invoice(invOpts{Customer: b, Issued: day(2026, 2, 3)})
	o.fee(feeOpts{Invoice: i6, Subscription: subB, Type: "subscription", Amount: 2000, From: feb, To: mar})

	t.Run("by kind", func(t *testing.T) {
		got, err := e.store.Revenue(e.ctx, o.id, store.Filter{Periods: months(t, jan, feb)})
		if err != nil {
			t.Fatalf("Revenue() error = %v", err)
		}
		want := []store.RevenuePoint{
			subscriptionRevenue(0, "EUR", "1500"),
			zeroRevenue(1, "EUR"),
			{
				Point: store.Point{Period: 0, Currency: "USD"},
				// subscription 3000 + 2000, fixed charge 400, product 100.
				Subscription: "5500",
				// charge 1000 + 300 paid in advance.
				UsageBased: "1300",
				Commitment: "500",
				OneOff:     "500",
				// 5500 + 1300 + 500 + 500.
				Gross: "7800",
				// The credits of a's invoice, allocated over its fees in
				// full.
				Coupons: "500", CreditNotes: "250", FreeCredits: "1000", Progressive: "250",
				// 500 + 250 + 1000 + 250; net 7800 - 2000.
				Contra: "2000", Net: "5800",
				Prepaid: "2000",
			},
			subscriptionRevenue(1, "USD", "2000"),
		}
		checkRows(t, "Revenue()", got, want)
	})

	tinEmpty, tinSet := true, false
	// The January revenue under each filter, gross and net by currency.
	// a's invoice allocates its 2000 of contra revenue over 5000 of fees:
	// 0.4 per unit of fee amount.
	tests := []struct {
		name   string
		filter store.Filter
		want   map[string][2]string
	}{
		{name: "currency", filter: store.Filter{Currency: "EUR"}, want: map[string][2]string{"EUR": {"1500", "1500"}}},
		{
			// b: 2000 + 400 + 100 + 300, without credits.
			name: "external customer id", filter: store.Filter{ExternalCustomerID: "b"},
			want: map[string][2]string{"USD": {"2800", "2800"}},
		},
		{
			// subA's fees: 3000 + 1000 + 500 commitment, net of 0.4 ×
			// 4500 = 1800; the add-on has no plan.
			name: "plan code", filter: store.Filter{PlanCode: "basic"},
			want: map[string][2]string{"EUR": {"1500", "1500"}, "USD": {"4500", "2700"}},
		},
		{
			// subB's fees: 2000 + 400 + 300; the product has no plan.
			name: "other plan code", filter: store.Filter{PlanCode: "pro"},
			want: map[string][2]string{"USD": {"2700", "2700"}},
		},
		{name: "customer type", filter: store.Filter{CustomerType: "individual"}, want: map[string][2]string{"USD": {"2800", "2800"}}},
		{name: "without TIN", filter: store.Filter{CustomerTINEmpty: &tinEmpty}, want: map[string][2]string{"USD": {"2800", "2800"}}},
		{
			// a: 3000 + 1000 + 500 + 500 = 5000, net 5000 - 2000.
			name: "with TIN", filter: store.Filter{CustomerTINEmpty: &tinSet},
			want: map[string][2]string{"EUR": {"1500", "1500"}, "USD": {"5000", "3000"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.filter
			f.Periods = months(t, jan, jan)
			got, err := e.store.Revenue(e.ctx, o.id, f)
			if err != nil {
				t.Fatalf("Revenue() error = %v", err)
			}
			gotMap := map[string][2]string{}
			for _, r := range got {
				g, _ := norm(r.Gross)
				n, _ := norm(r.Net)
				gotMap[r.Currency] = [2]string{g, n}
			}
			if len(gotMap) != len(tt.want) {
				t.Errorf("Revenue() got = %v, want = %v", gotMap, tt.want)
			}
			for c, w := range tt.want {
				if gotMap[c] != w {
					t.Errorf("Revenue() %s gross, net got = %v, want = %v", c, gotMap[c], w)
				}
			}
		})
	}
}

// TestRevenueDiscardedFee checks that a discarded pay-in-advance fee
// (fees.deleted_at: "every read skips discarded fees") is no revenue.
func TestRevenueDiscardedFee(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	jan := day(2026, 1, 1)
	c := o.customer(customerOpts{ExternalID: "a"})
	s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
	o.fee(feeOpts{Subscription: s, Type: "charge", Amount: 300, PayInAdvance: true, Created: at(2026, 1, 25, 12, 0)})
	o.fee(feeOpts{Subscription: s, Type: "charge", Amount: 700, PayInAdvance: true, Created: at(2026, 1, 25, 12, 0), Deleted: true})
	got, err := e.store.Revenue(e.ctx, o.id, store.Filter{Periods: months(t, jan, jan)})
	if err != nil {
		t.Fatalf("Revenue() error = %v", err)
	}
	// Only the kept fee: 300.
	want := zeroRevenue(0, "USD")
	want.UsageBased, want.Gross, want.Net = "300", "300", "300"
	checkRows(t, "Revenue()", got, []store.RevenuePoint{want})
}

func TestRevenueLists(t *testing.T) {
	e := newEnv(t)
	o := e.org(t)
	jan, feb := day(2026, 1, 1), day(2026, 2, 1)
	px, py := o.plan("px", "monthly"), o.plan("py", "monthly")
	customers := map[string]string{}
	// x: gross 5000, net 1000 (4000 of coupons); y: 3000 net 3000; z:
	// 1500 net 1500. x is on px, y and z on py.
	for _, c := range []struct {
		ext     string
		plan    string
		amount  int64
		coupons int64
	}{
		{"x", px, 5000, 4000},
		{"y", py, 3000, 0},
		{"z", py, 1500, 0},
	} {
		id := o.customer(customerOpts{ExternalID: c.ext})
		customers[c.ext] = id
		s := o.subscription(subOpts{Customer: id, Plan: c.plan})
		inv := o.invoice(invOpts{Customer: id, Issued: day(2026, 1, 10), Coupons: c.coupons})
		o.fee(feeOpts{Invoice: inv, Subscription: s, Type: "subscription", Amount: c.amount, From: jan, To: feb})
	}
	f := store.Filter{Periods: months(t, jan, jan)}
	// Totals: gross 5000 + 3000 + 1500 = 9500, net 1000 + 3000 + 1500 =
	// 5500.
	cust := func(ext, gross, grossShare, net, netShare string) store.GroupRevenue {
		return store.GroupRevenue{
			Customer: store.Customer{ID: customers[ext], ExternalID: ext, Name: ext},
			Currency: "USD", Gross: gross, GrossShare: grossShare, Net: net, NetShare: netShare,
		}
	}
	x := cust("x", "5000", "0.526316", "1000", "0.181818") // 5000/9500, 1000/5500
	y := cust("y", "3000", "0.315789", "3000", "0.545455") // 3000/9500, 3000/5500
	z := cust("z", "1500", "0.157895", "1500", "0.272727") // 1500/9500, 1500/5500

	t.Run("by customer", func(t *testing.T) {
		tests := []struct {
			name  string
			order store.RevenueOrder
			page  store.Page
			want  []store.GroupRevenue
		}{
			{name: "by gross", order: store.ByGross, page: store.Page{Size: 10}, want: []store.GroupRevenue{x, y, z}},
			{name: "by net", order: store.ByNet, page: store.Page{Size: 10}, want: []store.GroupRevenue{y, z, x}},
			{name: "by net, second page", order: store.ByNet, page: store.Page{Offset: 1, Size: 1}, want: []store.GroupRevenue{z}},
			{name: "past the end", order: store.ByGross, page: store.Page{Offset: 3, Size: 2}, want: nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, total, err := e.store.RevenueByCustomer(e.ctx, o.id, f, tt.order, tt.page)
				if err != nil {
					t.Fatalf("RevenueByCustomer() error = %v", err)
				}
				if total != 3 {
					t.Errorf("RevenueByCustomer() total got = %d, want = 3", total)
				}
				checkRows(t, "RevenueByCustomer()", got, tt.want)
			})
		}
	})

	t.Run("by plan", func(t *testing.T) {
		plan := func(id, code string, customers int64, customersShare, gross, grossShare, net, netShare string) store.GroupRevenue {
			return store.GroupRevenue{
				Plan:      store.Plan{ID: id, Code: code, Name: "Plan " + code, Interval: "monthly"},
				Customers: customers, CustomersShare: customersShare,
				Currency: "USD", Gross: gross, GrossShare: grossShare, Net: net, NetShare: netShare,
			}
		}
		// px: x alone, 1 of 3 customers, gross 5000/9500, net 1000/5500.
		pxRow := plan(px, "px", 1, "0.333333", "5000", "0.526316", "1000", "0.181818")
		// py: y and z, 2 of 3, gross 4500/9500, net 4500/5500.
		pyRow := plan(py, "py", 2, "0.666667", "4500", "0.473684", "4500", "0.818182")
		tests := []struct {
			name  string
			order store.RevenueOrder
			page  store.Page
			want  []store.GroupRevenue
		}{
			{name: "by gross", order: store.ByGross, page: store.Page{Size: 10}, want: []store.GroupRevenue{pxRow, pyRow}},
			{name: "by net", order: store.ByNet, page: store.Page{Size: 10}, want: []store.GroupRevenue{pyRow, pxRow}},
			{name: "by net, first page", order: store.ByNet, page: store.Page{Size: 1}, want: []store.GroupRevenue{pyRow}},
			{name: "past the end", order: store.ByNet, page: store.Page{Offset: 2, Size: 1}, want: nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, total, err := e.store.RevenueByPlan(e.ctx, o.id, f, tt.order, tt.page)
				if err != nil {
					t.Fatalf("RevenueByPlan() error = %v", err)
				}
				if total != 2 {
					t.Errorf("RevenueByPlan() total got = %d, want = 2", total)
				}
				checkRows(t, "RevenueByPlan()", got, tt.want)
			})
		}
	})
}
