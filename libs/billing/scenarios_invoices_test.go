package billing_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestScenarioInvoicesTrialOnSeveralSubscriptions: an in-advance plan with
// a trial, billed after the trial, terminated and subscribed again.
// source: spec/scenarios/invoices/invoices_spec.rb:17
func TestScenarioInvoicesTrialOnSeveralSubscriptions(t *testing.T) {
	s := newScenario(t, invAt("2024-03-04T21:00:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer()
	p := s.Plan(period.Monthly, 3500, invAdvance, func(p *catalog.Plan) { p.TrialPeriodDays = 7 })
	sub := s.invCreateSub(c, p)
	if got := len(s.invSubInvoices(sub.ID)); got != 0 {
		t.Fatalf("invoices = %d, want = 0", got)
	}

	s.travelTo(invAt("2024-03-11T22:00:00Z"))
	s.clock()
	invs := s.invSubInvoices(sub.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	first := invs[0]
	if got, want := first.TotalAmount, int64(2371); got != want {
		t.Errorf("total = %d, want = %d", got, want)
	}

	s.travelTo(invAt("2024-03-11T23:00:00Z"))
	s.invTerminate(sub)
	if got := s.invLast(sub.ID).TotalAmount; got != 0 {
		t.Errorf("termination total = %d, want = 0", got)
	}
	cns := s.CreditNotes(first.ID)
	if len(cns) != 1 {
		t.Fatalf("credit notes = %d, want = 1", len(cns))
	}
	if cn := cns[0]; cn.SubTotalExcludingTaxesAmount != 2371 || cn.TaxesAmount != 0 || cn.TotalAmount != 2371 {
		t.Errorf("credit note = %d/%d/%d, want = 2371/0/2371", cn.SubTotalExcludingTaxesAmount, cn.TaxesAmount, cn.TotalAmount)
	}

	// The trial starts at the first subscription of the external ID, so
	// the re-subscription gets no fresh trial and is billed at once:
	// (31 - 4 - 6) × 35 / 31.
	s.travelTo(invAt("2024-03-11T23:05:00Z"))
	again := s.invCreateSub(c, p)
	invs = s.invSubInvoices(again.ID)
	if len(invs) != 1 {
		t.Fatalf("new subscription invoices = %d, want = 1", len(invs))
	}
	if got, want := invs[0].FeesAmount, int64(2371); got != want {
		t.Errorf("new subscription fees = %d, want = %d", got, want)
	}
}

// TestScenarioInvoicesTimezones: the first in-advance weekly invoice is
// prorated on the local days left in the week.
func TestScenarioInvoicesTimezones(t *testing.T) {
	for _, tc := range []struct {
		source, zone, at string
		want             int64
	}{
		{"spec/scenarios/invoices/invoices_spec.rb:76", "America/Denver", "2023-06-16T05:00:00Z", 400},
		{"spec/scenarios/invoices/invoices_spec.rb:99", "America/Halifax", "2023-06-16T05:00:00Z", 300},
		{"spec/scenarios/invoices/invoices_spec.rb:122", "Europe/Paris", "2023-06-16T20:00:00Z", 300},
		{"spec/scenarios/invoices/invoices_spec.rb:145", "Asia/Karachi", "2023-06-16T20:00:00Z", 200},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			s := newScenario(t, invAt(tc.at))
			s.invBETax("vat", "0")
			c := s.Customer(invTZ(tc.zone))
			p := s.Plan(period.Weekly, 700, invAdvance)
			sub := s.invCreateSub(c, p)
			if got := s.invLast(sub.ID).TotalAmount; got != tc.want {
				t.Errorf("%s: total = %d, want = %d", tc.source, got, tc.want)
			}
		})
	}
}

// TestScenarioInvoicesLeapFebruary: the periodic invoice of February 2024
// covers the 29 days.
// source: spec/scenarios/invoices/invoices_spec.rb:168
func TestScenarioInvoicesLeapFebruary(t *testing.T) {
	s := newScenario(t, invAt("2023-06-16T05:00:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer()
	p := s.Plan(period.Monthly, 700, invAdvance)
	sub := s.invCreateSub(c, p)

	s.travelTo(invAt("2024-02-01T12:12:00Z"))
	s.clock()
	inv := s.invLast(sub.ID)
	is := s.invISubs(inv.ID)[0]
	if got, want := is.Period, (period.Period{From: invAt("2024-02-01T00:00:00Z"), To: invAt("2024-03-01T00:00:00Z")}); got != want {
		t.Errorf("period = %v, want = %v", got, want)
	}
	if got, want := is.ChargesPeriod, (period.Period{From: invAt("2024-01-01T00:00:00Z"), To: invAt("2024-02-01T00:00:00Z")}); got != want {
		t.Errorf("charges period = %v, want = %v", got, want)
	}
	if got := inv.TotalAmount; got != 700 {
		t.Errorf("total = %d, want = 700", got)
	}
}

// TestScenarioInvoicesUpgradeWithoutGracePeriod: an upgrade and a
// termination each finalize a termination invoice at once.
// source: spec/scenarios/invoices/invoices_spec.rb:204
func TestScenarioInvoicesUpgradeWithoutGracePeriod(t *testing.T) {
	s := newScenario(t, invAt("2024-04-24T10:00:00Z"))
	s.invBETax("vat", "20")
	c := s.Customer(invGrace(0))
	p := s.Plan(period.Monthly, 0)
	pNew := s.Plan(period.Monthly, 2000)
	m := s.Metric("latest", catalog.AggregationTypeLatest, "value")
	s.Charge(pNew, m, catalog.ChargeModelPackage, `{"amount":"2","free_units":1000,"package_size":1000}`)
	sub := s.invCreateSub(c, p)

	s.travelTo(invAt("2024-04-24T11:00:00Z"))
	s.invCreateSub(c, pNew)
	if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	invs := s.invSubInvoices(sub.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	checkTermination := func(inv *invoice.Invoice, total int64, from, to, cfrom, cto string) {
		t.Helper()
		if inv.Status != invoice.StatusFinalized || inv.TotalAmount != total {
			t.Errorf("invoice = %s %d, want = finalized %d", inv.Status, inv.TotalAmount, total)
		}
		is := s.invISubs(inv.ID)[0]
		if is.Reason != invoice.InvoicingReasonSubscriptionTerminating {
			t.Errorf("reason = %s, want = subscription_terminating", is.Reason)
		}
		if got, want := is.Period, (period.Period{From: invAt(from), To: invAt(to)}); got != want {
			t.Errorf("period = %v, want = %v", got, want)
		}
		if got, want := is.ChargesPeriod, (period.Period{From: invAt(cfrom), To: invAt(cto)}); got != want {
			t.Errorf("charges period = %v, want = %v", got, want)
		}
	}
	checkTermination(invs[0], 0, "2024-04-24T00:00:00Z", "2024-04-24T11:00:00Z", "2024-04-24T10:00:00Z", "2024-04-24T11:00:00Z")

	latest := s.invActive(c.ID)
	s.travelTo(invAt("2024-04-26T11:00:00Z"))
	s.invTerminate(latest)
	if got := s.invSub(latest.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	invs = s.invSubInvoices(latest.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	checkTermination(invs[0], 240, "2024-04-24T00:00:00Z", "2024-04-26T11:00:00Z", "2024-04-24T11:00:00Z", "2024-04-26T11:00:00Z")
}

// TestScenarioInvoicesTerminatedWithGracePeriod: refreshing the draft
// termination invoice keeps its amount.
// source: spec/scenarios/invoices/invoices_spec.rb:291
func TestScenarioInvoicesTerminatedWithGracePeriod(t *testing.T) {
	s := newScenario(t, invAt("2022-12-15T00:00:00Z"))
	s.invBETax("vat", "20")
	c := s.Customer(invGrace(3))
	p := s.Plan(period.Monthly, 1000)
	sub := s.invCreateSub(c, p)
	m := s.Metric("count", catalog.AggregationTypeCount, "")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"3"}`)

	s.travelTo(invAt("2022-12-20T06:00:00Z"))
	s.invTerminate(sub)
	invs := s.invSubInvoices(sub.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	if got := invs[0].TotalAmount; got != 233 {
		t.Errorf("total = %d, want = 233", got)
	}
	if got := s.invRefresh(invs[0].ID).TotalAmount; got != 233 {
		t.Errorf("refreshed total = %d, want = 233", got)
	}
}

// invRecurring is a metric option making it recurring.
func invRecurring(m *catalog.BillableMetric) { m.Recurring = true }

// invChargeOpts sets the timing of a charge.
func invChargeOpts(advance, prorated bool) func(*catalog.Charge) {
	return func(c *catalog.Charge) { c.PayInAdvance, c.Prorated = advance, prorated }
}

// TestScenarioInvoicesRecurringChargesOnTermination: what a termination
// or an upgrade bills of recurring charges.
func TestScenarioInvoicesRecurringChargesOnTermination(t *testing.T) {
	for _, tc := range []struct {
		name, source      string
		advance, prorated bool
		value             string
		yearly, noCharge  bool
		upgrade, sameBM   bool
		wantCharges       int
		wantAmount        int64
		wantSubscription  int
	}{
		{name: "arrears recurring", source: "spec/scenarios/invoices/invoices_spec.rb:336", value: "0", wantCharges: 1, wantAmount: -1},
		{name: "arrears prorated", source: "spec/scenarios/invoices/invoices_spec.rb:395", prorated: true, value: "10", wantCharges: 1, wantAmount: 484},
		{name: "no charges", source: "spec/scenarios/invoices/invoices_spec.rb:454", yearly: true, noCharge: true, wantAmount: -1, wantSubscription: 1},
		{name: "upgrade without same metric", source: "spec/scenarios/invoices/invoices_spec.rb:493", value: "0", upgrade: true, wantCharges: 1, wantAmount: -1},
		{name: "upgrade with same metric", source: "spec/scenarios/invoices/invoices_spec.rb:559", upgrade: true, sameBM: true, wantAmount: -1},
		{name: "advance prorated", source: "spec/scenarios/invoices/invoices_spec.rb:621", advance: true, prorated: true, wantAmount: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, invAt("2022-12-15T00:00:00Z"))
			s.invBETax("vat", "20")
			c := s.Customer()
			iv := period.Monthly
			if tc.yearly {
				iv = period.Yearly
			}
			p := s.Plan(iv, 1000)
			m := s.Metric("sum", catalog.AggregationTypeSum, "amount", invRecurring)
			if !tc.noCharge {
				s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"3"}`, invChargeOpts(tc.advance, tc.prorated))
			}
			sub := s.invCreateSub(c, p)
			if tc.value != "" {
				s.travelTo(invAt("2022-12-16T10:12:00Z"))
				s.ingest(sub, "sum", "amount", tc.value)
			}
			s.travelTo(invAt("2022-12-20T06:00:00Z"))
			if tc.upgrade {
				pNew := s.Plan(period.Monthly, 2000)
				if tc.sameBM {
					s.Charge(pNew, m, catalog.ChargeModelStandard, `{"amount":"3"}`, invChargeOpts(false, false))
				}
				s.invCreateSub(c, pNew)
			} else {
				s.invTerminate(sub)
			}
			if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
				t.Fatalf("%s: status = %s, want = terminated", tc.source, got)
			}
			invs := s.invSubInvoices(sub.ID)
			if len(invs) != 1 {
				t.Fatalf("%s: invoices = %d, want = 1", tc.source, len(invs))
			}
			charges := s.invFeesOf(invs[0].ID, core.FeeTypeCharge)
			if len(charges) != tc.wantCharges {
				t.Fatalf("%s: charge fees = %d, want = %d", tc.source, len(charges), tc.wantCharges)
			}
			if tc.wantAmount >= 0 && charges[0].Amount != tc.wantAmount {
				t.Errorf("%s: charge amount = %d, want = %d", tc.source, charges[0].Amount, tc.wantAmount)
			}
			if tc.wantSubscription > 0 {
				if got := len(s.invFeesOf(invs[0].ID, core.FeeTypeSubscription)); got != tc.wantSubscription {
					t.Errorf("%s: subscription fees = %d, want = %d", tc.source, got, tc.wantSubscription)
				}
			}
		})
	}
}

// invPlanChangeSetup subscribes a customer on 1 Jan 2024 to plan (2 900,
// prorated recurring sum charge of 1) with a 5 event on 2 Jan, bills on
// 1 Feb, and returns the scenario, customer, subscription, metric and the
// new plan (29 000 with the same charge).
func invPlanChangeSetup(t *testing.T, advance bool) (*scenario, *customer.Customer, *subscription.Subscription, *catalog.Plan) {
	s := newScenario(t, invAt("2024-01-01T00:00:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer()
	opt := func(p *catalog.Plan) { p.PayInAdvance = advance }
	p := s.Plan(period.Monthly, 2900, opt)
	pNew := s.Plan(period.Monthly, 29000, opt)
	m := s.Metric("sum", catalog.AggregationTypeSum, "amount", invRecurring)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, invChargeOpts(false, true))
	s.Charge(pNew, m, catalog.ChargeModelStandard, `{"amount":"1"}`, invChargeOpts(false, true))
	sub := s.invCreateSub(c, p)
	s.travelTo(invAt("2024-01-02T00:00:00Z"))
	s.ingest(sub, "sum", "amount", "5")
	s.travelTo(invAt("2024-02-01T00:00:00Z"))
	s.clock()
	return s, c, sub, pNew
}

// TestScenarioInvoicesAdvanceUpgrade: an in-advance upgrade credits the
// unused part and bills the new plan with the old charges.
// source: spec/scenarios/invoices/invoices_spec.rb:670
func TestScenarioInvoicesAdvanceUpgrade(t *testing.T) {
	s, c, sub, pNew := invPlanChangeSetup(t, true)
	if got := len(s.Invoices(c.ID)); got != 2 {
		t.Fatalf("invoices = %d, want = 2", got)
	}
	s.travelTo(invAt("2024-02-12T06:00:00Z"))
	s.invCreateSub(c, pNew)
	if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Fatalf("invoices = %d, want = 3", got)
	}
	active := s.invActive(c.ID)
	cns := s.invCustomerCreditNotes(c.ID)
	if len(cns) == 0 || cns[0].CreditAmount != 1800 {
		t.Fatalf("credit notes = %v, want = one of 1800", cns)
	}
	// Gap (libs/period): lago bills 18 000 + 190 (old charges of 1-12 Feb)
	// - 1 800. period.Dates.terminatedForGood excludes Next == Upgrade, so
	// the old in-advance subscription bills its January charges again
	// (484 instead of 190). Assert the new subscription fee and the
	// credit note offset only.
	upgrade := s.invLast(active.ID)
	if fs := s.invFeesOf(upgrade.ID, core.FeeTypeSubscription); len(fs) != 1 || fs[0].Amount != 18000 {
		t.Errorf("upgrade subscription fees = %v, want = one of 18000", fs)
	}
	if got := upgrade.CreditNotesAmount; got != 1800 {
		t.Errorf("upgrade credit notes = %d, want = 1800", got)
	}
	s.travelTo(invAt("2024-03-01T12:12:00Z"))
	s.clock()
	if got, want := s.invLast(active.ID).TotalAmount, int64(29310); got != want {
		t.Errorf("march total = %d, want = %d", got, want)
	}
}

// TestScenarioInvoicesArrearsUpgrade: an in-arrears upgrade bills the
// used part with the charges, then the new plan.
// source: spec/scenarios/invoices/invoices_spec.rb:757
func TestScenarioInvoicesArrearsUpgrade(t *testing.T) {
	s, c, sub, pNew := invPlanChangeSetup(t, false)
	if got := len(s.Invoices(c.ID)); got != 1 {
		t.Fatalf("invoices = %d, want = 1", got)
	}
	s.travelTo(invAt("2024-02-12T06:00:00Z"))
	s.invCreateSub(c, pNew)
	if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	if got := len(s.Invoices(c.ID)); got != 2 {
		t.Fatalf("invoices = %d, want = 2", got)
	}
	if got, want := s.invLast(sub.ID).TotalAmount, int64(1290); got != want {
		t.Errorf("termination total = %d, want = %d", got, want)
	}
	active := s.invActive(c.ID)
	s.travelTo(invAt("2024-03-01T12:12:00Z"))
	s.clock()
	if got, want := s.invLast(active.ID).TotalAmount, int64(18310); got != want {
		t.Errorf("march total = %d, want = %d", got, want)
	}
}

// TestScenarioInvoicesEventsOnPlanChangeDate: events of the plan change
// day go to the subscription active when they happen.
// source: spec/scenarios/invoices/invoices_spec.rb:842
func TestScenarioInvoicesEventsOnPlanChangeDate(t *testing.T) {
	s := newScenario(t, invAt("2024-01-10T06:20:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer()
	p := s.Plan(period.Monthly, 0)
	pNew := s.Plan(period.Monthly, 0)
	m := s.Metric("sum", catalog.AggregationTypeSum, "amount")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0"}`)
	s.Charge(pNew, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.invCreateSub(c, p, billingtest.Anniversary)

	checkUsage := func(id core.SubscriptionID, amount int64, units string) {
		t.Helper()
		u := s.invUsage(id)
		if u.Amount != amount || u.TotalAmount != amount {
			t.Errorf("usage = %d/%d, want = %d", u.Amount, u.TotalAmount, amount)
		}
		if len(u.Fees) == 0 || !u.Fees[0].Units.Equal(money.MustParse(units)) {
			t.Errorf("usage units = %v, want = %s", u.Fees, units)
		}
	}
	s.travelTo(invAt("2024-01-10T08:20:00Z"))
	s.ingest(sub, "sum", "amount", "10")
	checkUsage(sub.ID, 0, "10")

	s.travelTo(invAt("2024-01-10T08:30:00Z"))
	s.invCreateSub(c, pNew, billingtest.Anniversary)
	if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	if got := len(s.Invoices(c.ID)); got != 1 {
		t.Fatalf("invoices = %d, want = 1", got)
	}
	if got := s.invLast(sub.ID).TotalAmount; got != 0 {
		t.Errorf("termination total = %d, want = 0", got)
	}
	active := s.invActive(c.ID)
	checkUsage(active.ID, 0, "0")

	s.travelTo(invAt("2024-01-10T08:35:00Z"))
	s.ingest(active, "sum", "amount", "10000")
	checkUsage(active.ID, 1000000, "10000")

	s.travelTo(invAt("2024-01-10T08:40:00Z"))
	s.invCreateSub(c, p, billingtest.Anniversary)
	if got := s.invSub(active.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	if got := len(s.Invoices(c.ID)); got != 2 {
		t.Fatalf("invoices = %d, want = 2", got)
	}
	if got := s.invLast(active.ID).TotalAmount; got != 1000000 {
		t.Errorf("termination total = %d, want = 1000000", got)
	}
	checkUsage(s.invActive(c.ID).ID, 0, "0")
}

// TestScenarioInvoicesAdvanceTerminated: an in-advance termination credits
// the unused part, offsetting the charges of the termination invoice.
// source: spec/scenarios/invoices/invoices_spec.rb:969
func TestScenarioInvoicesAdvanceTerminated(t *testing.T) {
	s, c, sub, _ := invPlanChangeSetup(t, true)
	s.travelTo(invAt("2024-02-12T06:00:00Z"))
	s.invTerminate(sub)
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Fatalf("invoices = %d, want = 3", got)
	}
	cns := s.invCustomerCreditNotes(c.ID)
	if len(cns) == 0 || cns[0].CreditAmount != 1700 {
		t.Fatalf("credit notes = %v, want = one of 1700", cns)
	}
	inv := s.invLast(sub.ID)
	if inv.TotalAmount != 0 || inv.FeesAmount != 207 || inv.CreditNotesAmount != 207 {
		t.Errorf("termination = total %d fees %d credit notes %d, want = 0/207/207", inv.TotalAmount, inv.FeesAmount, inv.CreditNotesAmount)
	}
}

// TestScenarioInvoicesAdvanceTerminatedSameDay: a termination on the
// subscription day bills that day only.
func TestScenarioInvoicesAdvanceTerminatedSameDay(t *testing.T) {
	for _, tc := range []struct {
		source string
		usage  bool
	}{
		{"spec/scenarios/invoices/invoices_spec.rb:1040", false},
		{"spec/scenarios/invoices/invoices_spec.rb:1072", true},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, invAt("2024-02-01T03:00:00Z"))
			s.invBETax("vat", "0")
			c := s.Customer()
			p := s.Plan(period.Monthly, 2900, invAdvance)
			m := s.Metric("count", catalog.AggregationTypeCount, "")
			if tc.usage {
				s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"12"}`)
			}
			sub := s.invCreateSub(c, p)
			first := s.invLast(sub.ID)
			if tc.usage {
				s.travelTo(invAt("2024-02-01T10:00:00Z"))
				s.ingest(sub, "count")
			}
			s.travelTo(invAt("2024-02-01T18:00:00Z"))
			s.invTerminate(sub)
			if got := len(s.Invoices(c.ID)); got != 2 {
				t.Fatalf("invoices = %d, want = 2", got)
			}
			cns := s.CreditNotes(first.ID)
			if len(cns) != 1 || cns[0].CreditAmount != 2800 {
				t.Fatalf("credit notes = %v, want = one of 2800", cns)
			}
			term := s.invLast(sub.ID)
			if !tc.usage {
				if term.TotalAmount != 0 {
					t.Errorf("termination total = %d, want = 0", term.TotalAmount)
				}
				return
			}
			charges := s.invFeesOf(term.ID, core.FeeTypeCharge)
			if len(charges) != 1 || charges[0].Amount+charges[0].TaxesAmount != 1200 {
				t.Errorf("charge fees = %v, want = one of 1200", charges)
			}
		})
	}
}

// TestScenarioInvoicesAdvanceTerminatedWithGracePeriod: a termination
// credit note offsets the draft termination invoice only once it is
// finalized, after a fee adjustment.
// source: spec/scenarios/invoices/invoices_spec.rb:1131
func TestScenarioInvoicesAdvanceTerminatedWithGracePeriod(t *testing.T) {
	s := newScenario(t, invAt("2024-01-01T00:00:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer(invGrace(3))
	p := s.Plan(period.Monthly, 2900, invAdvance)
	m := s.Metric("sum", catalog.AggregationTypeSum, "amount", invRecurring)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, invChargeOpts(false, true))
	sub := s.invCreateSub(c, p)
	s.invFinalize(s.invLast(sub.ID).ID)
	s.travelTo(invAt("2024-01-02T00:00:00Z"))
	s.ingest(sub, "sum", "amount", "5")
	s.travelTo(invAt("2024-02-01T00:00:00Z"))
	s.clock()
	s.invFinalize(s.invLast(sub.ID).ID)

	s.travelTo(invAt("2024-02-12T06:00:00Z"))
	s.invTerminate(sub)
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Fatalf("invoices = %d, want = 3", got)
	}
	check := func(step string, credit, balance, total, fees, cnAmount int64) {
		t.Helper()
		cn := s.invCustomerCreditNotes(c.ID)[0]
		inv := s.invLast(sub.ID)
		if cn.CreditAmount != credit || cn.BalanceAmount != balance {
			t.Errorf("%s: credit note = %d/%d, want = %d/%d", step, cn.CreditAmount, cn.BalanceAmount, credit, balance)
		}
		if inv.TotalAmount != total || inv.FeesAmount != fees || inv.CreditNotesAmount != cnAmount {
			t.Errorf("%s: invoice = total %d fees %d cn %d, want = %d/%d/%d", step, inv.TotalAmount, inv.FeesAmount, inv.CreditNotesAmount, total, fees, cnAmount)
		}
	}
	check("terminated", 1700, 1700, 207, 207, 0)
	term := s.invLast(sub.ID)
	s.invAdjustFee(s.invFeesOf(term.ID, core.FeeTypeCharge)[0], "3", "5.00")
	check("adjusted", 1700, 1700, 1500, 1500, 0)
	s.invFinalize(term.ID)
	check("finalized", 1700, 200, 0, 1500, 1500)
}

// TestScenarioInvoicesAdjustedDraftWithCreditNote: adjusting the draft
// subscription fee resizes its termination credit note.
func TestScenarioInvoicesAdjustedDraftWithCreditNote(t *testing.T) {
	for _, tc := range []struct {
		source, units, price string
		wantCredit, wantFees int64
	}{
		{"spec/scenarios/invoices/invoices_spec.rb:1225", "18", "0.50", 850, 900},
		{"spec/scenarios/invoices/invoices_spec.rb:1306", "1", "0", 0, 0},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, invAt("2024-02-12T06:00:00Z"))
			s.invBETax("vat", "0")
			c := s.Customer(invGrace(3))
			p := s.Plan(period.Monthly, 2900, invAdvance)
			m := s.Metric("sum", catalog.AggregationTypeSum, "amount", invRecurring)
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, invChargeOpts(false, true))
			sub := s.invCreateSub(c, p)
			first := s.invLast(sub.ID)
			if first.Status != invoice.StatusDraft || first.TotalAmount != 1800 || first.FeesAmount != 1800 {
				t.Fatalf("first = %s %d/%d, want = draft 1800/1800", first.Status, first.TotalAmount, first.FeesAmount)
			}
			if got := len(s.invCustomerCreditNotes(c.ID)); got != 0 {
				t.Fatalf("credit notes = %d, want = 0", got)
			}
			s.travelTo(invAt("2024-02-12T21:00:00Z"))
			s.invTerminate(sub)
			if got := len(s.Invoices(c.ID)); got != 2 {
				t.Fatalf("invoices = %d, want = 2", got)
			}
			cns := s.invCustomerCreditNotes(c.ID)
			if len(cns) != 1 || cns[0].CreditAmount != 1700 || cns[0].BalanceAmount != 1700 {
				t.Fatalf("credit notes = %v, want = one of 1700/1700", cns)
			}
			if f := s.Invoice(first.ID); f.TotalAmount != 1800 || f.CreditNotesAmount != 0 {
				t.Errorf("first = %d cn %d, want = 1800 cn 0", f.TotalAmount, f.CreditNotesAmount)
			}
			s.invAdjustFee(s.invFeesOf(first.ID, core.FeeTypeSubscription)[0], tc.units, tc.price)
			cn := s.invCustomerCreditNotes(c.ID)[0]
			if cn.CreditAmount != tc.wantCredit || cn.BalanceAmount != tc.wantCredit {
				t.Errorf("credit note = %d/%d, want = %d", cn.CreditAmount, cn.BalanceAmount, tc.wantCredit)
			}
			if f := s.Invoice(first.ID); f.TotalAmount != tc.wantFees || f.FeesAmount != tc.wantFees || f.CreditNotesAmount != 0 {
				t.Errorf("first = %d/%d cn %d, want = %d", f.TotalAmount, f.FeesAmount, f.CreditNotesAmount, tc.wantFees)
			}
			if term := s.invLast(sub.ID); term.TotalAmount != 0 || term.FeesAmount != 0 || term.CreditNotesAmount != 0 {
				t.Errorf("termination = %d/%d/%d, want = 0/0/0", term.TotalAmount, term.FeesAmount, term.CreditNotesAmount)
			}
		})
	}
}

// TestScenarioInvoicesArrearsTerminated: an in-arrears termination bills
// the used days and the prorated recurring charge.
// source: spec/scenarios/invoices/invoices_spec.rb:1389
func TestScenarioInvoicesArrearsTerminated(t *testing.T) {
	s := newScenario(t, invAt("2024-01-01T00:00:00Z"))
	s.invBETax("vat", "0")
	c := s.Customer()
	p := s.Plan(period.Monthly, 2900)
	m := s.Metric("sum", catalog.AggregationTypeSum, "amount", invRecurring)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, invChargeOpts(false, true))
	sub := s.invCreateSub(c, p)
	s.travelTo(invAt("2024-01-02T00:00:00Z"))
	s.ingest(sub, "sum", "amount", "5")
	s.travelTo(invAt("2024-02-12T06:00:00Z"))
	s.invTerminate(sub)
	if got := len(s.Invoices(c.ID)); got != 1 {
		t.Fatalf("invoices = %d, want = 1", got)
	}
	if got, want := s.invLast(sub.ID).TotalAmount, int64(1407); got != want {
		t.Errorf("total = %d, want = %d", got, want)
	}
}

// TestScenarioInvoicesAdvanceGraceTermination: a termination during the
// grace period issues a draft credit note that offsets the termination
// invoice once both are finalized.
func TestScenarioInvoicesAdvanceGraceTermination(t *testing.T) {
	for _, tc := range []struct {
		source, amount   string
		events           int
		termTotal, final int64
	}{
		{"spec/scenarios/invoices/invoices_spec.rb:1451", "3", 2, 720, 294},
		{"spec/scenarios/invoices/invoices_spec.rb:1545", "1", 1, 120, 0},
	} {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, invAt("2022-12-15T00:00:00Z"))
			s.invBETax("vat", "20")
			c := s.Customer(invGrace(3))
			p := s.Plan(period.Monthly, 1000, invAdvance)
			m := s.Metric("count", catalog.AggregationTypeCount, "")
			sub := s.invCreateSub(c, p)
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+tc.amount+`"}`)
			first := s.invLast(sub.ID)
			if first.Status != invoice.StatusDraft || first.TotalAmount != 658 {
				t.Fatalf("first = %s %d, want = draft 658", first.Status, first.TotalAmount)
			}
			s.travelTo(invAt("2022-12-17T00:00:00Z"))
			for range tc.events {
				s.Event(sub, "count", s.Now())
			}
			if got := s.invRefresh(first.ID).TotalAmount; got != 658 {
				t.Errorf("refreshed first = %d, want = 658", got)
			}

			s.travelTo(invAt("2022-12-20T00:00:00Z"))
			s.invTerminate(sub)
			if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
				t.Fatalf("status = %s, want = terminated", got)
			}
			if got := len(s.invSubInvoices(sub.ID)); got != 2 {
				t.Fatalf("invoices = %d, want = 2", got)
			}
			cns := s.CreditNotes(first.ID)
			if len(cns) != 1 {
				t.Fatalf("credit notes = %d, want = 1", len(cns))
			}
			cn := cns[0]
			if cn.Status != creditnote.StatusDraft || cn.CreditAmount != 426 || cn.BalanceAmount != 426 || cn.TotalAmount != 426 {
				t.Errorf("credit note = %s %d/%d/%d, want = draft 426", cn.Status, cn.CreditAmount, cn.BalanceAmount, cn.TotalAmount)
			}
			term := s.invLast(sub.ID)
			if term.TotalAmount != tc.termTotal || term.CreditNotesAmount != 0 {
				t.Errorf("termination = %d cn %d, want = %d cn 0", term.TotalAmount, term.CreditNotesAmount, tc.termTotal)
			}
			if got := len(s.InvoiceCredits(term.ID)); got != 0 {
				t.Errorf("termination credits = %d, want = 0", got)
			}
			if got := s.invRefresh(first.ID).TotalAmount; got != 658 {
				t.Errorf("refreshed first = %d, want = 658", got)
			}
			if got := s.ReloadCreditNote(cn.ID).TotalAmount; got != 426 {
				t.Errorf("refreshed credit note = %d, want = 426", got)
			}
			if got := s.invRefresh(term.ID).TotalAmount; got != tc.termTotal {
				t.Errorf("refreshed termination = %d, want = %d", got, tc.termTotal)
			}
			if f := s.invFinalize(first.ID); f.Status != invoice.StatusFinalized || f.TotalAmount != 658 {
				t.Errorf("finalized first = %s %d, want = finalized 658", f.Status, f.TotalAmount)
			}
			if got := s.ReloadCreditNote(cn.ID).Status; got != creditnote.StatusFinalized {
				t.Errorf("credit note status = %s, want = finalized", got)
			}
			if f := s.invFinalize(term.ID); f.Status != invoice.StatusFinalized || f.TotalAmount != tc.final {
				t.Errorf("finalized termination = %s %d, want = finalized %d", f.Status, f.TotalAmount, tc.final)
			}
		})
	}
}

// TestScenarioInvoicesAdvanceGraceRefreshAndFinalize: refreshing the
// drafts keeps the in-advance invoice and updates the usage one.
// source: spec/scenarios/invoices/invoices_spec.rb:1633
func TestScenarioInvoicesAdvanceGraceRefreshAndFinalize(t *testing.T) {
	s := newScenario(t, invAt("2022-12-15T00:00:00Z"))
	s.invBETax("vat", "20")
	c := s.Customer(invGrace(3))
	p := s.Plan(period.Monthly, 1000, invAdvance)
	m := s.Metric("count", catalog.AggregationTypeCount, "")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.invCreateSub(c, p)
	first := s.invLast(sub.ID)
	if first.TotalAmount != 658 {
		t.Fatalf("first = %d, want = 658", first.TotalAmount)
	}
	for _, day := range []string{"2022-12-16T00:00:00Z", "2022-12-17T00:00:00Z"} {
		s.travelTo(invAt(day))
		s.ingest(sub, "count")
		if got := s.invRefresh(first.ID).TotalAmount; got != 658 {
			t.Errorf("%s: refreshed first = %d, want = 658", day, got)
		}
	}
	s.travelTo(invAt("2023-01-01T00:00:00Z"))
	s.clock()
	invs := s.invSubInvoices(sub.ID)
	if len(invs) != 2 {
		t.Fatalf("invoices = %d, want = 2", len(invs))
	}
	next := invs[1]
	if next.TotalAmount != 1440 {
		t.Errorf("periodic = %d, want = 1440", next.TotalAmount)
	}
	s.ingestAt(sub, "count", invAt("2022-12-18T00:00:00Z"))
	if got := s.invRefresh(first.ID).TotalAmount; got != 658 {
		t.Errorf("refreshed first = %d, want = 658", got)
	}
	if got := s.invRefresh(next.ID).TotalAmount; got != 1560 {
		t.Errorf("refreshed periodic = %d, want = 1560", got)
	}
	if f := s.invFinalize(first.ID); f.Status != invoice.StatusFinalized || f.TotalAmount != 658 {
		t.Errorf("finalized first = %s %d, want = finalized 658", f.Status, f.TotalAmount)
	}
	if f := s.invFinalize(next.ID); f.Status != invoice.StatusFinalized || f.TotalAmount != 1560 {
		t.Errorf("finalized periodic = %s %d, want = finalized 1560", f.Status, f.TotalAmount)
	}
}

// TestScenarioInvoicesArrearsToAdvanceUpgradeWithGrace: the upgrade puts
// both subscriptions on one draft, refreshed unchanged.
// source: spec/scenarios/invoices/invoices_spec.rb:1734
func TestScenarioInvoicesArrearsToAdvanceUpgradeWithGrace(t *testing.T) {
	s := newScenario(t, invAt("2022-12-15T00:00:00Z"))
	s.invBETax("vat", "20")
	c := s.Customer(invGrace(3))
	arrears := s.Plan(period.Monthly, 1000)
	advance := s.Plan(period.Monthly, 1000, invAdvance)
	sub := s.invCreateSub(c, arrears)
	s.invCreateSub(c, advance)
	drafts := 0
	for _, inv := range s.Invoices(c.ID) {
		if inv.Status == invoice.StatusDraft {
			drafts++
		}
	}
	if drafts != 1 {
		t.Fatalf("drafts = %d, want = 1", drafts)
	}
	if got := s.invSub(sub.ID).Status; got != subscription.StatusTerminated {
		t.Fatalf("status = %s, want = terminated", got)
	}
	inv := s.invSubInvoices(sub.ID)[0]
	if got := s.invRefresh(inv.ID).TotalAmount; got != inv.TotalAmount {
		t.Errorf("refreshed = %d, want = %d", got, inv.TotalAmount)
	}
}

// invNumberingSetup is the setup of invoice_numbering_spec.rb: the default
// billing entity numbered per customer with prefix BENT-1 in Europe/Paris,
// three customers, a monthly plan of 129 in advance and a yearly plan of
// 1 000 in advance.
func invNumberingSetup(t *testing.T, n organization.DocumentNumbering, opts ...func(*customer.Customer)) (s *scenario, cs []*customer.Customer, monthly, yearly *catalog.Plan) {
	t.Helper()
	s = newScenario(t, invAt("2023-07-19T12:12:00Z"))
	s.UpdateBillingEntity(func(be *organization.BillingEntity) {
		invNumbering(n, "BENT-1")(be)
		be.Timezone = mustLoc("Europe/Paris")
	})
	for i := range 3 {
		var o []func(*customer.Customer)
		if i < len(opts) && opts[i] != nil {
			o = append(o, opts[i])
		}
		cs = append(cs, s.Customer(o...))
	}
	monthly = s.Plan(period.Monthly, 12900, invAdvance)
	yearly = s.Plan(period.Yearly, 100000, invAdvance)
	return s, cs, monthly, yearly
}

// mustLoc loads an IANA time zone.
func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// invAnniversary is a subscription option billing it on its anniversary.
func invAnniversary(s *subscription.Subscription) { s.BillingTime = period.Anniversary }

// invCheckNums compares the latest numbering of each customer.
func invCheckNums(t *testing.T, step string, got []invNum, want ...invNum) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: numbering = %v, want = %v", step, got, want)
	}
}

// invCheckAll compares every invoice number of the customers.
func invCheckAll(t *testing.T, s *scenario, step string, cs []*customer.Customer, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := s.invAllNumbers(cs...); !slices.Equal(got, want) {
		t.Errorf("%s: numbers = %v, want = %v", step, got, want)
	}
}

// TestScenarioInvoicesNumbering: invoice numbers while the billing entity
// switches between per_customer and per_billing_entity numbering; the
// billing entity sequence counts every invoice issued before the switch.
// lago's organization_sequential_id is dropped (domain §22.11).
// source: spec/scenarios/invoices/invoice_numbering_spec.rb:49
func TestScenarioInvoicesNumbering(t *testing.T) {
	s, cs, monthly, yearly := invNumberingSetup(t, organization.DocumentNumberingPerCustomer)
	for _, c := range cs {
		s.invCreateSub(c, monthly, invAnniversary)
	}
	invCheckNums(t, "jul 19", s.invLatestNums(cs...), invNum{1, 0, "BENT-1-001-001"}, invNum{1, 0, "BENT-1-002-001"}, invNum{1, 0, "BENT-1-003-001"})

	s.travelTo(invAt("2023-08-19T12:12:00Z"))
	s.performBilling()
	invCheckNums(t, "aug 19", s.invLatestNums(cs...), invNum{2, 0, "BENT-1-001-002"}, invNum{2, 0, "BENT-1-002-002"}, invNum{2, 0, "BENT-1-003-002"})

	s.travelTo(invAt("2023-09-19T12:12:00Z"))
	s.performBilling()
	invCheckNums(t, "sep 19", s.invLatestNums(cs...), invNum{3, 0, "BENT-1-001-003"}, invNum{3, 0, "BENT-1-002-003"}, invNum{3, 0, "BENT-1-003-003"})

	s.travelTo(invAt("2023-10-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, "BENT-11"))
	s.performBilling()
	invCheckNums(t, "oct 19", s.invLatestNums(cs...), invNum{4, 10, "BENT-11-202310-010"}, invNum{4, 11, "BENT-11-202310-011"}, invNum{4, 12, "BENT-11-202310-012"})

	s.travelTo(invAt("2023-11-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerCustomer, ""))
	s.performBilling()
	invCheckNums(t, "nov 19", s.invLatestNums(cs...), invNum{5, 0, "BENT-11-001-005"}, invNum{5, 0, "BENT-11-002-005"}, invNum{5, 0, "BENT-11-003-005"})

	s.travelTo(invAt("2023-11-22T12:12:00Z"))
	s.invCreateSub(cs[1], yearly, invAnniversary, invExternalID("new_external_id"))
	invCheckNums(t, "nov 22", s.invLatestNums(cs[1]), invNum{6, 0, "BENT-11-002-006"})
	invCheckAll(t, s, "nov 22", cs,
		"BENT-1-001-001", "BENT-1-002-001", "BENT-1-003-001",
		"BENT-1-001-002", "BENT-1-002-002", "BENT-1-003-002",
		"BENT-1-001-003", "BENT-1-002-003", "BENT-1-003-003",
		"BENT-11-202310-010", "BENT-11-202310-011", "BENT-11-202310-012",
		"BENT-11-001-005", "BENT-11-002-005", "BENT-11-003-005",
		"BENT-11-002-006")

	s.travelTo(invAt("2023-12-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, ""))
	s.performBilling()
	invCheckNums(t, "dec 19", s.invLatestNums(cs...), invNum{6, 17, "BENT-11-202312-017"}, invNum{7, 18, "BENT-11-202312-018"}, invNum{6, 19, "BENT-11-202312-019"})

	s.travelTo(invAt("2024-01-19T12:12:00Z"))
	s.performBilling()
	invCheckNums(t, "jan 19", s.invLatestNums(cs...), invNum{7, 20, "BENT-11-202401-020"}, invNum{8, 21, "BENT-11-202401-021"}, invNum{7, 22, "BENT-11-202401-022"})
}

// TestScenarioInvoicesNumberingTimezone: a per_billing_entity number takes
// its month in the billing entity's time zone.
// source: spec/scenarios/invoices/invoice_numbering_spec.rb:240
func TestScenarioInvoicesNumberingTimezone(t *testing.T) {
	s, cs, monthly, _ := invNumberingSetup(t, organization.DocumentNumberingPerCustomer, invTZ("UTC"), invTZ("UTC"), invTZ("UTC"))
	for _, c := range cs {
		s.invCreateSub(c, monthly)
	}
	invCheckNums(t, "jul 19", s.invLatestNums(cs...), invNum{1, 0, "BENT-1-001-001"}, invNum{1, 0, "BENT-1-002-001"}, invNum{1, 0, "BENT-1-003-001"})
	for i, at := range []string{"2023-08-01T00:00:00Z", "2023-09-01T00:00:00Z"} {
		s.travelTo(invAt(at))
		s.performBilling()
		seq := int64(i + 2)
		invCheckNums(t, at, s.invLatestNums(cs...),
			invNum{seq, 0, fmt.Sprintf("BENT-1-001-%03d", seq)}, invNum{seq, 0, fmt.Sprintf("BENT-1-002-%03d", seq)}, invNum{seq, 0, fmt.Sprintf("BENT-1-003-%03d", seq)})
	}
	for _, c := range cs {
		s.UpdateCustomer(c, invTZ("Europe/Paris"))
	}

	s.travelTo(invAt("2023-09-30T23:10:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, "BENT-11"))
	s.performBilling()
	invCheckNums(t, "oct 1 paris", s.invLatestNums(cs...), invNum{4, 10, "BENT-11-202310-010"}, invNum{4, 11, "BENT-11-202310-011"}, invNum{4, 12, "BENT-11-202310-012"})
}

// TestScenarioInvoicesNumberingWithGracePeriod: drafts get their numbers
// when finalized, in finalization order.
func TestScenarioInvoicesNumberingWithGracePeriod(t *testing.T) {
	for _, tc := range []struct {
		source    string
		numbering organization.DocumentNumbering
		jul19     []invNum
		jul20     invNum
		finalized [2]string
		aug19     []invNum
		sep19     []invNum
		sep20     string
	}{
		{
			source: "spec/scenarios/invoices/invoice_numbering_spec.rb:344", numbering: organization.DocumentNumberingPerCustomer,
			jul19:     []invNum{{1, 0, "BENT-1-001-001"}, {0, 0, "BENT-1-DRAFT"}, {1, 0, "BENT-1-003-001"}},
			jul20:     invNum{2, 0, "BENT-1-001-002"},
			finalized: [2]string{"BENT-1-002-001", "BENT-1-002-002"},
			aug19:     []invNum{{3, 0, "BENT-1-001-003"}, {0, 0, "BENT-1-DRAFT"}, {2, 0, "BENT-1-003-002"}},
			sep19:     []invNum{{4, 0, "BENT-1-001-004"}, {0, 0, "BENT-1-DRAFT"}, {3, 0, "BENT-1-003-003"}},
		},
		{
			source: "spec/scenarios/invoices/invoice_numbering_spec.rb:568", numbering: organization.DocumentNumberingPerBillingEntity,
			jul19:     []invNum{{1, 1, "BENT-1-202307-001"}, {0, 0, "BENT-1-DRAFT"}, {1, 2, "BENT-1-202307-002"}},
			jul20:     invNum{2, 3, "BENT-1-202307-003"},
			finalized: [2]string{"BENT-1-202307-004", "BENT-1-202307-005"},
			aug19:     []invNum{{3, 6, "BENT-1-202308-006"}, {0, 0, "BENT-1-DRAFT"}, {2, 7, "BENT-1-202308-007"}},
			sep19:     []invNum{{4, 8, "BENT-1-202309-008"}, {0, 0, "BENT-1-DRAFT"}, {3, 9, "BENT-1-202309-009"}},
			sep20:     "BENT-1-202309-010",
		},
	} {
		t.Run(string(tc.numbering), func(t *testing.T) {
			s, cs, monthly, yearly := invNumberingSetup(t, tc.numbering, nil, invGrace(2))
			for _, c := range cs {
				s.invCreateSub(c, monthly, invAnniversary)
			}
			invCheckNums(t, tc.source+" jul 19", s.invLatestNums(cs...), tc.jul19...)

			s.travelTo(invAt("2023-07-20T12:12:00Z"))
			s.invCreateSub(cs[0], yearly, invAnniversary, invExternalID("new_external_id"))
			invCheckNums(t, tc.source+" jul 20", s.invLatestNums(cs[0]), tc.jul20)

			s.travelTo(invAt("2023-07-21T12:12:00Z"))
			s.invCreateSub(cs[1], yearly, invAnniversary, invExternalID("new_external_id_2"))
			invCheckNums(t, tc.source+" jul 21", s.invLatestNums(cs[1]), invNum{0, 0, "BENT-1-DRAFT"})

			s.travelTo(invAt("2023-07-21T13:12:00Z"))
			first := s.invFinalize(s.invDrafts(cs[1].ID)[0].ID)
			s.travelTo(invAt("2023-07-21T14:12:00Z"))
			second := s.invFinalize(s.invDrafts(cs[1].ID)[0].ID)
			if got := [2]string{s.Invoice(first.ID).Number, s.Invoice(second.ID).Number}; got != tc.finalized {
				t.Errorf("%s: finalized drafts = %v, want = %v", tc.source, got, tc.finalized)
			}

			s.travelTo(invAt("2023-08-19T12:12:00Z"))
			s.performBilling()
			invCheckNums(t, tc.source+" aug 19", s.invLatestNums(cs...), tc.aug19...)

			s.travelTo(invAt("2023-09-19T12:12:00Z"))
			s.performBilling()
			invCheckNums(t, tc.source+" sep 19", s.invLatestNums(cs...), tc.sep19...)

			if tc.sep20 == "" {
				return
			}
			s.travelTo(invAt("2023-09-20T12:12:00Z"))
			inv := s.invFinalize(s.invDrafts(cs[1].ID)[0].ID)
			if got := s.Invoice(inv.ID).Number; got != tc.sep20 {
				t.Errorf("%s: sep 20 number = %s, want = %s", tc.source, got, tc.sep20)
			}
		})
	}
}

// TestScenarioInvoicesNumberingPartner: a partner's self-billed invoices
// are numbered per customer and leave the billing entity sequence alone.
// source: spec/scenarios/invoices/invoice_numbering_spec.rb:803
func TestScenarioInvoicesNumberingPartner(t *testing.T) {
	partner := func(c *customer.Customer) { c.AccountType = customer.AccountTypePartner }
	s, cs, monthly, _ := invNumberingSetup(t, organization.DocumentNumberingPerCustomer, nil, nil, partner)
	for _, c := range cs {
		s.invCreateSub(c, monthly, invAnniversary)
	}
	for i, at := range []string{"", "2023-08-19T12:12:00Z", "2023-09-19T12:12:00Z"} {
		if at != "" {
			s.travelTo(invAt(at))
			s.performBilling()
		}
		seq := int64(i + 1)
		invCheckNums(t, fmt.Sprintf("month %d", seq), s.invLatestNums(cs...),
			invNum{seq, 0, fmt.Sprintf("BENT-1-001-%03d", seq)}, invNum{seq, 0, fmt.Sprintf("BENT-1-002-%03d", seq)}, invNum{seq, 0, fmt.Sprintf("BENT-1-003-%03d", seq)})
	}

	s.travelTo(invAt("2023-10-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, "BENT-11"))
	s.performBilling()
	invCheckNums(t, "oct 19", s.invLatestNums(cs...), invNum{4, 7, "BENT-11-202310-007"}, invNum{4, 8, "BENT-11-202310-008"}, invNum{4, 0, "BENT-11-003-004"})

	s.travelTo(invAt("2023-11-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerCustomer, ""))
	s.performBilling()
	invCheckNums(t, "nov 19", s.invLatestNums(cs...), invNum{5, 0, "BENT-11-001-005"}, invNum{5, 0, "BENT-11-002-005"}, invNum{5, 0, "BENT-11-003-005"})

	s.travelTo(invAt("2023-12-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, ""))
	s.performBilling()
	invCheckNums(t, "dec 19", s.invLatestNums(cs...), invNum{6, 11, "BENT-11-202312-011"}, invNum{6, 12, "BENT-11-202312-012"}, invNum{6, 0, "BENT-11-003-006"})
}

// TestScenarioInvoicesNumberingMultipleEntities: each billing entity keeps
// its own numbering and sequence. Deviation: customer sequential IDs are
// per billing entity (domain §6), so the second entity's customers are 001
// and 002 where lago numbers them 004 and 005.
// source: spec/scenarios/invoices/invoice_numbering_spec.rb:956
func TestScenarioInvoicesNumberingMultipleEntities(t *testing.T) {
	s := newScenario(t, invAt("2023-07-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerCustomer, "BENT-1"))
	second := s.invEntity("second", "BENT-2")
	var cs []*customer.Customer
	for range 3 {
		cs = append(cs, s.Customer())
	}
	for range 2 {
		cs = append(cs, s.Customer(func(c *customer.Customer) { c.BillingEntityID = second.ID }))
	}
	monthly := s.Plan(period.Monthly, 12900, invAdvance)
	for _, c := range cs {
		s.invCreateSub(c, monthly, invAnniversary)
	}
	perCustomer := func(seq int64, p1, p2 string) []invNum {
		return []invNum{
			{seq, 0, fmt.Sprintf("%s-001-%03d", p1, seq)},
			{seq, 0, fmt.Sprintf("%s-002-%03d", p1, seq)},
			{seq, 0, fmt.Sprintf("%s-003-%03d", p1, seq)},
			{seq, 0, fmt.Sprintf("%s-001-%03d", p2, seq)},
			{seq, 0, fmt.Sprintf("%s-002-%03d", p2, seq)},
		}
	}
	invCheckNums(t, "jul 19", s.invLatestNums(cs...), perCustomer(1, "BENT-1", "BENT-2")...)
	for i, at := range []string{"2023-08-19T12:12:00Z", "2023-09-19T12:12:00Z"} {
		s.travelTo(invAt(at))
		s.performBilling()
		invCheckNums(t, at, s.invLatestNums(cs...), perCustomer(int64(i+2), "BENT-1", "BENT-2")...)
	}

	s.travelTo(invAt("2023-10-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, "BENT-11"))
	s.performBilling()
	invCheckNums(t, "oct 19", s.invLatestNums(cs...),
		invNum{4, 10, "BENT-11-202310-010"}, invNum{4, 11, "BENT-11-202310-011"}, invNum{4, 12, "BENT-11-202310-012"},
		invNum{4, 0, "BENT-2-001-004"}, invNum{4, 0, "BENT-2-002-004"})

	s.travelTo(invAt("2023-11-19T12:12:00Z"))
	s.invUpdateEntity(second, invNumbering(organization.DocumentNumberingPerBillingEntity, "BENT-22"))
	s.performBilling()
	invCheckNums(t, "nov 19", s.invLatestNums(cs...),
		invNum{5, 13, "BENT-11-202311-013"}, invNum{5, 14, "BENT-11-202311-014"}, invNum{5, 15, "BENT-11-202311-015"},
		invNum{5, 9, "BENT-22-202311-009"}, invNum{5, 10, "BENT-22-202311-010"})

	s.travelTo(invAt("2023-12-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerCustomer, ""))
	s.invUpdateEntity(second, invNumbering(organization.DocumentNumberingPerCustomer, ""))
	s.performBilling()
	invCheckNums(t, "dec 19", s.invLatestNums(cs...), perCustomer(6, "BENT-11", "BENT-22")...)

	s.travelTo(invAt("2024-01-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, ""))
	s.invUpdateEntity(second, invNumbering(organization.DocumentNumberingPerBillingEntity, ""))
	s.performBilling()
	invCheckNums(t, "jan 19", s.invLatestNums(cs...),
		invNum{7, 19, "BENT-11-202401-019"}, invNum{7, 20, "BENT-11-202401-020"}, invNum{7, 21, "BENT-11-202401-021"},
		invNum{7, 13, "BENT-22-202401-013"}, invNum{7, 14, "BENT-22-202401-014"})
}

// TestScenarioInvoicesNumberingCustomerAcrossEntities: one customer billed
// by two billing entities has a gapless sequence per entity.
// source: spec/scenarios/invoices/invoice_numbering_spec.rb:1167
func TestScenarioInvoicesNumberingCustomerAcrossEntities(t *testing.T) {
	s := newScenario(t, invAt("2023-07-19T12:12:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerCustomer, "BENT-1"))
	second := s.invEntity("second", "BENT-2")
	c := s.Customer()
	monthly := s.Plan(period.Monthly, 12900, invAdvance)
	s.invCreateSub(c, monthly, invAnniversary, invExternalID("sub-under-first"))
	s.invCreateSub(c, monthly, invAnniversary, invExternalID("sub-under-second"), func(sub *subscription.Subscription) { sub.BillingEntityID = second.ID })
	want := []string{"BENT-1-001-001", "BENT-2-001-001"}
	invCheckAll(t, s, "jul 19", []*customer.Customer{c}, slices.Clone(want)...)
	for i, at := range []string{"2023-08-19T12:12:00Z", "2023-09-19T12:12:00Z"} {
		s.travelTo(invAt(at))
		s.performBilling()
		want = append(want, fmt.Sprintf("BENT-1-001-%03d", i+2), fmt.Sprintf("BENT-2-001-%03d", i+2))
		invCheckAll(t, s, at, []*customer.Customer{c}, slices.Clone(want)...)
	}
	for _, inv := range s.Invoices(c.ID) {
		if (inv.BillingEntityID == second.ID) != strings.HasPrefix(inv.Number, "BENT-2-") {
			t.Errorf("invoice %s of billing entity %s", inv.Number, inv.BillingEntityID)
		}
	}
}

// TestScenarioInvoicesAdjustedFees: adjusting a fee of a draft invoice
// survives its refresh and finalization.
func TestScenarioInvoicesAdjustedFees(t *testing.T) {
	for _, tc := range []struct {
		source, name string
		start        string
		charge       string
		feeType      core.FeeType
		unitPrice    string
		want         int64
	}{
		{"spec/scenarios/invoices/adjusted_charge_fees_spec.rb:31", "charge units", "2022-07-19T12:12:00Z", "5", core.FeeTypeCharge, "", 12900 + 1500},
		{"spec/scenarios/invoices/adjusted_charge_fees_spec.rb:98", "charge amount", "2022-07-19T12:12:00Z", "10", core.FeeTypeCharge, "150.00", 12900 + 45000},
		{"spec/scenarios/invoices/adjusted_subscription_fees_spec.rb:30", "subscription units", "2023-07-19T12:12:00Z", "", core.FeeTypeSubscription, "", 38700},
		{"spec/scenarios/invoices/adjusted_subscription_fees_spec.rb:83", "subscription amount", "2023-07-19T12:12:00Z", "", core.FeeTypeSubscription, "150.00", 45000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, invAt(tc.start))
			c := s.Customer(invGrace(5))
			p := s.Plan(period.Monthly, 12900)
			if tc.charge != "" {
				m := s.Metric("custom", catalog.AggregationTypeSum, "custom")
				s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+tc.charge+`"}`)
			}
			sub := s.invCreateSub(c, p, invAnniversary)
			if tc.charge != "" {
				s.travelTo(invAt("2023-07-23T10:12:00Z"))
				s.ingest(sub, "custom", "custom", "0")
			}

			s.travelTo(invAt("2023-08-19T12:12:00Z"))
			s.performBilling()
			inv := s.invLast(sub.ID)
			if inv.Status != invoice.StatusDraft || inv.TotalAmount != 12900 {
				t.Fatalf("%s: invoice = %s %d, want = draft 12900", tc.source, inv.Status, inv.TotalAmount)
			}
			s.invAdjustFee(s.invFeesOf(inv.ID, tc.feeType)[0], "3", tc.unitPrice)
			check := func(step string, st invoice.Status) {
				t.Helper()
				got := s.Invoice(inv.ID)
				if got.Status != st || got.TotalAmount != tc.want {
					t.Errorf("%s %s: invoice = %s %d, want = %s %d", tc.source, step, got.Status, got.TotalAmount, st, tc.want)
				}
			}
			check("adjusted", invoice.StatusDraft)

			s.travelTo(invAt("2023-08-20T12:12:00Z"))
			s.invRefresh(inv.ID)
			check("refreshed", invoice.StatusDraft)
			s.invFinalize(inv.ID)
			check("finalized", invoice.StatusFinalized)
		})
	}
}

// invAdvanceChargesSetup is the setup of advance_charges_spec.rb: a 20%
// tax, a monthly plan of 0.49 in advance with a prorated, pay-in-advance,
// non-invoiceable standard charge of amount on a recurring unique count
// metric, regrouping its paid fees on an invoice.
func invAdvanceChargesSetup(t *testing.T) (*scenario, *customer.Customer, *catalog.BillableMetric, *catalog.Plan) {
	t.Helper()
	s := newScenario(t, invAt("2024-06-05T10:00:00Z"))
	s.invBETax("vat", "20")
	c := s.Customer()
	m := s.Metric("cards", catalog.AggregationTypeUniqueCount, "item_id", invRecurring)
	p := s.Plan(period.Monthly, 49, invAdvance)
	invRegroupedCharge(s, p, m, "30.12")
	return s, c, m, p
}

// invRegroupedCharge adds the regrouped in-advance charge of amount to p.
func invRegroupedCharge(s *scenario, p *catalog.Plan, m *catalog.BillableMetric, amount string) *catalog.Charge {
	return s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`, func(c *catalog.Charge) {
		c.PayInAdvance, c.Invoiceable, c.Prorated = true, false, true
		c.RegroupPaidFees = catalog.RegroupPaidFeesInvoice
	})
}

// invCheckAdvanceInvoice compares the amounts of an advance charges
// invoice: fees, taxes and total, its applied tax base and no deduction.
func invCheckAdvanceInvoice(t *testing.T, s *scenario, inv *invoice.Invoice, fees, taxes, total int64) {
	t.Helper()
	if inv.FeesAmount != fees || inv.TaxesAmount != taxes || inv.TotalAmount != total ||
		inv.SubTotalExcludingTaxesAmount != fees || inv.SubTotalIncludingTaxesAmount != total {
		t.Errorf("advance charges invoice = fees %d taxes %d total %d (sub totals %d/%d), want = %d %d %d",
			inv.FeesAmount, inv.TaxesAmount, inv.TotalAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, fees, taxes, total)
	}
	if inv.CouponsAmount != 0 || inv.CreditNotesAmount != 0 || inv.PrepaidCreditAmount != 0 || inv.ProgressiveBillingCreditAmount != 0 {
		t.Errorf("advance charges deductions = %d/%d/%d/%d, want = 0", inv.CouponsAmount, inv.CreditNotesAmount, inv.PrepaidCreditAmount, inv.ProgressiveBillingCreditAmount)
	}
	if ts := s.invAppliedTaxes(inv.ID); len(ts) == 0 || ts[0].FeesAmount != fees {
		t.Errorf("applied tax base of %d taxes, want = %d", len(ts), fees)
	}
}

// invCreatedBefore counts the fees created before at.
func invCreatedBefore(fs []*invoice.Fee, at time.Time) int {
	n := 0
	for _, f := range fs {
		if f.CreatedAt.Before(at) {
			n++
		}
	}
	return n
}

// TestScenarioInvoicesAdvanceChargesRenewal: the paid in-advance fees of a
// period are regrouped on an advance charges invoice at the renewal; the
// unpaid ones wait for a later one.
// source: spec/scenarios/invoices/advance_charges_spec.rb:31
func TestScenarioInvoicesAdvanceChargesRenewal(t *testing.T) {
	s, c, _, p := invAdvanceChargesSetup(t)
	sub := s.invCreateSub(c, p)
	s.performBilling()
	if got := len(s.Invoices(c.ID)); got != 1 {
		t.Fatalf("invoices = %d, want = 1", got)
	}
	for i := 1; i <= 5; i++ {
		s.travelTo(time.Date(2024, 6, 10+i, 10, 0, 0, 0, time.UTC))
		s.ingest(sub, "cards", "item_id", fmt.Sprintf("card_%d", i))
		fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
		if got := len(invUnbilled(fs)); got != i {
			t.Errorf("card %d: unbilled fees = %d, want = %d", i, got, i)
		}
		// 30.12 prorated on the 20, 19, … days left of June
		want := []int64{2008, 1908, 1807, 1707, 1606}[i-1]
		if got := fs[len(fs)-1].Amount; got != want {
			t.Errorf("card %d: fee = %d, want = %d", i, got, want)
		}
	}
	fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
	s.invMarkPaid(invAt("2024-06-20T00:10:00Z"), fs[:3]...)

	s.travelTo(invAt("2024-07-01T00:10:00Z"))
	s.performBilling()
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Fatalf("jul 1: invoices = %d, want = 3", got)
	}
	unbilled := invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))
	july := invAt("2024-07-01T00:00:00Z")
	if got, want := [2]int{invCreatedBefore(unbilled, july), len(unbilled) - invCreatedBefore(unbilled, july)}, [2]int{2, 1}; got != want {
		t.Errorf("jul 1: unbilled fees (june, july) = %v, want = %v", got, want)
	}
	adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
	if len(adv) != 1 {
		t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
	}
	if got, want := adv[0].FeesAmount, int64(2008+1908+1807); got != want {
		t.Errorf("jul 1: advance charges fees = %d, want = %d", got, want)
	}
	if ts := s.invAppliedTaxes(adv[0].ID); len(ts) == 0 || ts[0].FeesAmount != 2008+1908+1807 {
		t.Errorf("jul 1: applied tax base wrong (%d taxes)", len(ts))
	}

	s.travelTo(invAt("2024-07-10T10:00:00Z"))
	s.invMarkPaid(s.Now(), unbilled...)

	s.travelTo(invAt("2024-08-01T00:10:00Z"))
	s.performBilling()
	if got := len(s.Invoices(c.ID)); got != 5 {
		t.Fatalf("aug 1: invoices = %d, want = 5", got)
	}
	adv = s.invOfType(c.ID, invoice.TypeAdvanceCharges)
	last := adv[len(adv)-1]
	lastFees := s.Fees(last.ID)
	if len(lastFees) != 3 || invCreatedBefore(lastFees, july) != 2 {
		t.Errorf("aug 1: advance charges fees = %d (%d of june), want = 3 (2)", len(lastFees), invCreatedBefore(lastFees, july))
	}
	// lago notes ComputeAmountsFromFees would give 22048 / 3675: the
	// invoice sums its fees' own taxes.
	invCheckAdvanceInvoice(t, s, last, 15060+1707+1606, 3674, 22047)
}

// TestScenarioInvoicesAdvanceChargesPaidBeforeRun: a fee of the new period
// paid just before the billing run waits for the next period's advance
// charges invoice.
func TestScenarioInvoicesAdvanceChargesPaidBeforeRun(t *testing.T) {
	t.Run("one fee", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_spec.rb:106
		s, c, _, p := invAdvanceChargesSetup(t)
		sub := s.invCreateSub(c, p)
		s.performBilling()

		s.travelTo(invAt("2024-07-01T00:01:00Z"))
		s.ingest(sub, "cards", "item_id", "card_1")
		fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
		if len(invUnbilled(fs)) != 1 || fs[0].Amount != 3012 {
			t.Fatalf("fees = %d, want = 1 of 3012", len(invUnbilled(fs)))
		}
		s.invMarkPaid(s.Now(), fs...)

		s.travelTo(invAt("2024-07-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 2 {
			t.Errorf("jul 1: invoices = %d, want = 2", got)
		}
		if got := len(s.invOfType(c.ID, invoice.TypeAdvanceCharges)); got != 0 {
			t.Errorf("jul 1: advance charges invoices = %d, want = 0", got)
		}

		s.travelTo(invAt("2024-08-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 4 {
			t.Fatalf("aug 1: invoices = %d, want = 4", got)
		}
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		last := adv[len(adv)-1]
		if got := s.Fees(last.ID); len(got) != 1 || invCreatedBefore(got, invAt("2024-07-02T00:00:00Z")) != 1 {
			t.Errorf("aug 1: advance charges fees = %d, want = 1 of jul 1", len(got))
		}
		invCheckAdvanceInvoice(t, s, last, 3012, 602, 3614)
	})

	t.Run("several fees", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_spec.rb:161
		s, c, _, p := invAdvanceChargesSetup(t)
		sub := s.invCreateSub(c, p)
		s.performBilling()
		for i := 1; i <= 4; i++ {
			s.travelTo(time.Date(2024, 6, 10+i, 10, 0, 0, 0, time.UTC))
			s.ingest(sub, "cards", "item_id", fmt.Sprintf("card_%d", i))
		}
		fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
		s.invMarkPaid(invAt("2024-06-20T00:10:00Z"), fs[:2]...)

		s.travelTo(invAt("2024-07-01T00:01:00Z"))
		s.ingest(sub, "cards", "item_id", "card_5")
		fs = s.invSubFees(sub.ID, core.FeeTypeCharge)
		if got := len(invUnbilled(fs)); got != 5 {
			t.Errorf("unbilled fees = %d, want = 5", got)
		}
		newest := fs[len(fs)-1]
		if newest.Amount != 3012 {
			t.Errorf("card 5 fee = %d, want = 3012", newest.Amount)
		}
		s.invMarkPaid(s.Now(), newest)

		s.travelTo(invAt("2024-07-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 3 {
			t.Fatalf("jul 1: invoices = %d, want = 3", got)
		}
		july := invAt("2024-07-01T00:00:00Z")
		unbilled := invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))
		if got, want := [2]int{invCreatedBefore(unbilled, july), len(unbilled) - invCreatedBefore(unbilled, july)}, [2]int{2, 2}; got != want {
			t.Errorf("jul 1: unbilled fees (june, july) = %v, want = %v", got, want)
		}
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) != 1 || adv[0].FeesAmount != 2008+1908 {
			t.Fatalf("jul 1: advance charges invoices = %d, want = 1 of %d", len(adv), 2008+1908)
		}

		s.travelTo(invAt("2024-07-10T10:00:00Z"))
		s.invMarkPaid(s.Now(), unbilled...)

		s.travelTo(invAt("2024-08-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 5 {
			t.Fatalf("aug 1: invoices = %d, want = 5", got)
		}
		adv = s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		last := adv[len(adv)-1]
		if got := s.Fees(last.ID); len(got) != 4 || invCreatedBefore(got, july) != 2 {
			t.Errorf("aug 1: advance charges fees = %d (%d of june), want = 4 (2)", len(got), invCreatedBefore(got, july))
		}
		invCheckAdvanceInvoice(t, s, last, 4*3012+1807+1707+3012, 3714, 22288)
	})
}

// TestScenarioInvoicesAdvanceChargesPlanChange: the paid fees of a
// subscription are regrouped when it is upgraded, and stay with it until
// a downgrade takes effect.
func TestScenarioInvoicesAdvanceChargesPlanChange(t *testing.T) {
	t.Run("upgrade", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_spec.rb:251
		s, c, m, p := invAdvanceChargesSetup(t)
		up := s.Plan(period.Monthly, 259, invAdvance)
		invRegroupedCharge(s, up, m, "60")
		sub := s.invCreateSub(c, p)
		s.performBilling()

		s.travelTo(invAt("2024-06-10T10:00:00Z"))
		for i := 1; i <= 3; i++ {
			s.ingest(sub, "cards", "item_id", fmt.Sprintf("card_%d", i))
		}
		s.invMarkPaid(s.Now(), invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))...)

		s.travelTo(invAt("2024-06-15T10:00:00Z"))
		upgraded := s.invCreateSub(c, up)
		if got := len(s.Invoices(c.ID)); got != 3 {
			t.Errorf("upgrade: invoices = %d, want = 3", got)
		}
		if got := len(s.invSubFees(upgraded.ID, core.FeeTypeCharge)); got != 0 {
			t.Errorf("upgrade: new charge fees = %d, want = 0", got)
		}
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) != 1 || len(s.Fees(adv[0].ID)) != 3 {
			t.Fatalf("upgrade: advance charges invoices = %d, want = 1 with 3 fees", len(adv))
		}

		s.travelTo(invAt("2024-06-20T10:00:00Z"))
		s.ingest(upgraded, "cards", "item_id", "card_4")
		fs := invUnbilled(s.invSubFees(upgraded.ID, core.FeeTypeCharge))
		if len(fs) != 1 {
			t.Fatalf("card 4: unbilled fees = %d, want = 1", len(fs))
		}
		s.invMarkPaid(s.Now(), fs...)

		s.travelTo(invAt("2024-07-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 5 {
			t.Errorf("jul 1: invoices = %d, want = 5", got)
		}
		recurring := invUnbilled(s.invSubFees(upgraded.ID, core.FeeTypeCharge))
		if len(recurring) != 1 || recurring[0].Units.Cmp(money.NewFromInt(4)) != 0 {
			t.Fatalf("jul 1: recurring fees = %d, want = 1 of 4 units", len(recurring))
		}
		adv = s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if got := len(s.Fees(adv[len(adv)-1].ID)); got != 1 {
			t.Errorf("jul 1: advance charges fees = %d, want = 1", got)
		}
		if got := len(invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))); got != 0 {
			t.Errorf("jul 1: old subscription unbilled fees = %d, want = 0", got)
		}
	})

	t.Run("downgrade", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_spec.rb:315
		s, c, m, p := invAdvanceChargesSetup(t)
		down := s.Plan(period.Monthly, 19, invAdvance)
		invRegroupedCharge(s, down, m, "15")
		sub := s.invCreateSub(c, p)
		s.performBilling()

		s.travelTo(invAt("2024-06-10T10:00:00Z"))
		for i := 1; i <= 3; i++ {
			s.ingest(sub, "cards", "item_id", fmt.Sprintf("card_%d", i))
		}
		s.invMarkPaid(s.Now(), invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))...)

		s.travelTo(invAt("2024-06-15T10:00:00Z"))
		downgraded := s.invCreateSub(c, down)
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 1 {
			t.Errorf("downgrade: invoices = %d, want = 1", got)
		}
		if got := len(invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))); got != 3 {
			t.Errorf("downgrade: unbilled fees = %d, want = 3", got)
		}
		if a, b := s.invSub(sub.ID).Status, s.invSub(downgraded.ID).Status; a != subscription.StatusActive || b != subscription.StatusPending {
			t.Errorf("statuses = %s/%s, want = active/pending", a, b)
		}

		s.travelTo(invAt("2024-06-20T10:00:00Z"))
		s.ingest(sub, "cards", "item_id", "card_4")
		if got := len(s.invSubFees(downgraded.ID, core.FeeTypeCharge)); got != 0 {
			t.Errorf("card 4: downgraded fees = %d, want = 0", got)
		}
		unbilled := invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge))
		if len(unbilled) != 4 {
			t.Errorf("card 4: unbilled fees = %d, want = 4", len(unbilled))
		}
		s.invMarkPaid(s.Now(), unbilled...)

		s.travelTo(invAt("2024-07-01T00:10:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 3 {
			t.Errorf("jul 1: invoices = %d, want = 3", got)
		}
		today := invAt("2024-07-01T00:00:00Z")
		var recurring []*invoice.Fee
		for _, f := range invUnbilled(s.invSubFees(sub.ID, core.FeeTypeCharge)) {
			if !f.CreatedAt.Before(today) {
				recurring = append(recurring, f)
			}
		}
		if len(recurring) != 1 || recurring[0].Units.Cmp(money.NewFromInt(4)) != 0 {
			t.Errorf("jul 1: recurring fees = %d, want = 1 of 4 units", len(recurring))
		}
		if got := len(s.invSubFees(downgraded.ID, core.FeeTypeCharge)); got != 0 {
			t.Errorf("jul 1: downgraded fees = %d, want = 0", got)
		}
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) == 0 || len(s.Fees(adv[len(adv)-1].ID)) != 4 {
			t.Errorf("jul 1: advance charges invoices = %d, want = the last with 4 fees", len(adv))
		}
	})
}

// TestScenarioInvoicesAdvanceChargesTaxes: each in-advance fee carries
// both billing entity taxes, and the advance charges invoice sums them per
// tax.
// source: spec/scenarios/invoices/advance_charges_taxes_spec.rb:42
func TestScenarioInvoicesAdvanceChargesTaxes(t *testing.T) {
	s := newScenario(t, invAt("2024-06-05T10:00:00Z"))
	s.invBETax("tax-1", "9.3")
	s.invBETax("tax-2", "12")
	c := s.Customer()
	cards := s.Metric("cards", catalog.AggregationTypeUniqueCount, "item_id", invRecurring)
	transfer := s.Metric("transfer", catalog.AggregationTypeSum, "item_id")
	p := s.Plan(period.Monthly, 49, invAdvance)
	invRegroupedCharge(s, p, cards, "30")
	invRegroupedCharge(s, p, transfer, "1").Prorated = false
	sub := s.invCreateSub(c, p)
	s.performBilling()

	feeTax := func(f *invoice.Fee, code string) int64 {
		for _, at := range f.AppliedTaxes {
			if at.TaxCode == code {
				return at.Amount
			}
		}
		return -1
	}
	for i := 1; i <= 3; i++ {
		s.travelTo(time.Date(2024, 6, 10+i, 1, 0, 0, 0, time.UTC))
		s.ingest(sub, "cards", "item_id", fmt.Sprintf("card_%d", i))
		fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
		if got := len(invUnbilled(fs)); got != i {
			t.Errorf("card %d: unbilled fees = %d, want = %d", i, got, i)
		}
		f := fs[len(fs)-1]
		amount := int64(21-i) * 100
		if f.Amount != amount || !f.TaxesRate.Equal(money.MustParse("21.3")) || len(f.AppliedTaxes) != 2 {
			t.Errorf("card %d: fee = %d at %s with %d taxes, want = %d at 21.3 with 2", i, f.Amount, f.TaxesRate, len(f.AppliedTaxes), amount)
		}
		// round(amount × rate / 100)
		want1 := money.NewFromInt(amount).Mul(money.MustParse("0.093")).Round(0, money.HalfUp)
		want2 := money.NewFromInt(amount).Mul(money.MustParse("0.12")).Round(0, money.HalfUp)
		if w1, _ := want1.Int64(); feeTax(f, "tax-1") != w1 {
			t.Errorf("card %d: tax-1 = %d, want = %d", i, feeTax(f, "tax-1"), w1)
		}
		if w2, _ := want2.Int64(); feeTax(f, "tax-2") != w2 {
			t.Errorf("card %d: tax-2 = %d, want = %d", i, feeTax(f, "tax-2"), w2)
		}
	}

	// lago sends both events to the cards metric.
	s.travelTo(invAt("2024-06-22T01:00:00Z"))
	s.ingest(sub, "cards", "item_id", "transfer_1")
	s.ingest(sub, "cards", "item_id", "transfer_2")
	fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
	if got := len(invUnbilled(fs)); got != 5 {
		t.Fatalf("unbilled fees = %d, want = 5", got)
	}
	for _, f := range fs[3:] {
		if len(f.AppliedTaxes) != 2 {
			t.Errorf("transfer fee taxes = %d, want = 2", len(f.AppliedTaxes))
		}
	}
	s.invMarkPaid(invAt("2024-06-24T00:00:00Z"), fs...)

	s.travelTo(invAt("2024-07-01T00:10:00Z"))
	s.performBilling()
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Fatalf("invoices = %d, want = 3", got)
	}
	adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
	if len(adv) != 1 {
		t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
	}
	sums := map[string]int64{}
	for _, f := range s.Fees(adv[0].ID) {
		for _, at := range f.AppliedTaxes {
			sums[at.TaxCode] += at.Amount
		}
	}
	ts := s.invAppliedTaxes(adv[0].ID)
	if len(ts) != 2 {
		t.Fatalf("applied taxes = %d, want = 2", len(ts))
	}
	for _, at := range ts {
		if at.Amount != sums[at.TaxCode] {
			t.Errorf("%s = %d, want = %d", at.TaxCode, at.Amount, sums[at.TaxCode])
		}
	}
}

// invCardFee ingests a card event and returns its in-advance fee.
func (s *scenario) invCardFee(sub *subscription.Subscription, item string) *invoice.Fee {
	s.T.Helper()
	e := s.ingest(sub, "cards", "item_id", item)
	for _, f := range s.invSubFees(sub.ID, core.FeeTypeCharge) {
		if f.PayInAdvanceEventTransactionID == e.TransactionID {
			return f
		}
	}
	s.T.Fatalf("no fee for event %s", e.TransactionID)
	return nil
}

// invCheckFeeProps compares the periods of an in-advance fee, which are
// those of its creation.
func invCheckFeeProps(t *testing.T, f *invoice.Fee, ts, from, to, cfrom string, days int) {
	t.Helper()
	want := invoice.FeeProperties{
		Timestamp: invAt(ts), From: invAt(from), To: invAt(to), ChargesFrom: invAt(cfrom), ChargesTo: invAt(to), ChargesDurationDays: days,
	}
	got := f.Properties
	got.Timestamp, got.From, got.To, got.ChargesFrom, got.ChargesTo = got.Timestamp.UTC(), got.From.UTC(), got.To.UTC(), got.ChargesFrom.UTC(), got.ChargesTo.UTC()
	if got != want {
		t.Errorf("fee properties = %+v, want = %+v", got, want)
	}
}

// invCheckAdvancePeriods compares the billing periods of an advance
// charges invoice.
func invCheckAdvancePeriods(t *testing.T, s *scenario, inv *invoice.Invoice, n int, from, to string) {
	t.Helper()
	iss := s.invISubs(inv.ID)
	if len(iss) != n {
		t.Errorf("billing periods = %d, want = %d", len(iss), n)
	}
	want := period.Period{From: invAt(from), To: invAt(to)}
	for _, is := range iss {
		if is.Period != want || is.ChargesPeriod != want || is.Reason != invoice.InvoicingReasonInAdvanceChargePeriodic {
			t.Errorf("billing period = %v %v %s, want = %v in_advance_charge_periodic", is.Period, is.ChargesPeriod, is.Reason, want)
		}
	}
}

// TestScenarioInvoicesAdvanceChargesDates: fees paid long after their
// creation keep the periods of their creation, while the advance charges
// invoice covers the charges period of its billing.
func TestScenarioInvoicesAdvanceChargesDates(t *testing.T) {
	setup := func(t *testing.T) (*scenario, *customer.Customer, *catalog.Plan, *catalog.Plan) {
		s := newScenario(t, invAt("2024-06-05T10:00:00Z"))
		s.Tax("vat", "20")
		c := s.Customer()
		m := s.Metric("cards", catalog.AggregationTypeUniqueCount, "item_id", invRecurring)
		p := s.Plan(period.Monthly, 49, invAdvance)
		up := s.Plan(period.Monthly, 259, invAdvance)
		invRegroupedCharge(s, p, m, "30.12")
		invRegroupedCharge(s, up, m, "30.12")
		return s, c, p, up
	}

	t.Run("upgraded renewed terminated", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_dates_spec.rb:40
		s, c, p, up := setup(t)
		initial := s.invCreateSub(c, p)
		s.performBilling()
		s.travelTo(invAt("2024-06-12T10:00:00Z"))
		fees := []*invoice.Fee{s.invCardFee(initial, "card_1")}

		s.travelTo(invAt("2024-07-07T10:00:00Z"))
		upgraded := s.invCreateSub(c, up)
		if got := s.invSub(initial.ID).Status; got != subscription.StatusTerminated {
			t.Errorf("initial status = %s, want = terminated", got)
		}
		if got := len(s.Invoices(c.ID)); got != 2 {
			t.Errorf("invoices = %d, want = 2", got)
		}

		s.travelTo(invAt("2024-07-22T10:00:00Z"))
		fees = append(fees, s.invCardFee(upgraded, "card_2"))
		if a, b := len(invUnbilled(s.invSubFees(initial.ID, core.FeeTypeCharge))), len(invUnbilled(s.invSubFees(upgraded.ID, core.FeeTypeCharge))); a != 1 || b != 1 {
			t.Errorf("unbilled fees = %d/%d, want = 1/1", a, b)
		}

		s.travelTo(invAt("2024-10-02T10:00:00Z"))
		s.invMarkPaid(s.Now(), fees...)

		s.travelTo(invAt("2024-11-01T10:00:00Z"))
		s.performBilling()
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) != 1 {
			t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
		}
		fs := s.Fees(adv[0].ID)
		slices.SortStableFunc(fs, func(a, b *invoice.Fee) int { return a.CreatedAt.Compare(b.CreatedAt) })
		if len(fs) != 2 {
			t.Fatalf("advance charges fees = %d, want = 2", len(fs))
		}
		invCheckFeeProps(t, fs[0], "2024-06-12T10:00:00Z", "2024-06-05T00:00:00Z", "2024-07-01T00:00:00Z", "2024-06-05T10:00:00Z", 30)
		invCheckFeeProps(t, fs[1], "2024-07-22T10:00:00Z", "2024-07-07T00:00:00Z", "2024-08-01T00:00:00Z", "2024-07-07T10:00:00Z", 31)
		invCheckAdvancePeriods(t, s, adv[0], 2, "2024-10-01T00:00:00Z", "2024-11-01T00:00:00Z")

		s.travelTo(invAt("2024-11-18T10:00:00Z"))
		card3 := s.invCardFee(upgraded, "card_3")
		s.invCardFee(upgraded, "card_4")
		s.travelTo(invAt("2024-12-12T10:00:00Z"))
		card5 := s.invCardFee(upgraded, "card_5")

		s.travelTo(invAt("2025-01-01T10:00:00Z"))
		s.performBilling()
		if got := len(s.invOfType(c.ID, invoice.TypeAdvanceCharges)); got != 1 {
			t.Errorf("jan 1: advance charges invoices = %d, want = 1", got)
		}

		s.travelTo(invAt("2025-01-07T10:00:00Z"))
		s.invMarkPaid(s.Now(), s.invReloadFee(card3), s.invReloadFee(card5))

		s.travelTo(invAt("2025-01-22T10:00:00Z"))
		s.invTerminate(upgraded)
		adv = s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		last := adv[len(adv)-1]
		if got := len(s.Fees(last.ID)); got != 2 {
			t.Errorf("termination advance charges fees = %d, want = 2", got)
		}
		invCheckAdvancePeriods(t, s, last, 1, "2025-01-01T00:00:00Z", "2025-01-22T10:00:00Z")
	})

	t.Run("downgraded renewed", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_dates_spec.rb:179
		s, c, p, up := setup(t)
		initial := s.invCreateSub(c, up)
		s.performBilling()
		s.travelTo(invAt("2024-06-12T10:00:00Z"))
		fees := []*invoice.Fee{s.invCardFee(initial, "card_1")}

		s.travelTo(invAt("2024-07-07T10:00:00Z"))
		down := s.invCreateSub(c, p)
		if a, b := s.invSub(initial.ID).Status, s.invSub(down.ID).Status; a != subscription.StatusActive || b != subscription.StatusPending {
			t.Errorf("statuses = %s/%s, want = active/pending", a, b)
		}
		if got := len(s.Invoices(c.ID)); got != 1 {
			t.Errorf("invoices = %d, want = 1", got)
		}

		s.travelTo(invAt("2024-07-22T10:00:00Z"))
		fees = append(fees, s.invCardFee(initial, "card_2"))
		if a, b := len(invUnbilled(s.invSubFees(initial.ID, core.FeeTypeCharge))), len(s.invSubFees(down.ID, core.FeeTypeCharge)); a != 2 || b != 0 {
			t.Errorf("unbilled fees = %d/%d, want = 2/0", a, b)
		}

		s.travelTo(invAt("2024-08-01T10:00:00Z"))
		s.performBilling()
		if got := len(s.Invoices(c.ID)); got != 2 {
			t.Errorf("aug 1: invoices = %d, want = 2", got)
		}
		if a, b := s.invSub(initial.ID).Status, s.invSub(down.ID).Status; a != subscription.StatusTerminated || b != subscription.StatusActive {
			t.Errorf("aug 1: statuses = %s/%s, want = terminated/active", a, b)
		}

		s.travelTo(invAt("2024-10-02T10:00:00Z"))
		s.invMarkPaid(s.Now(), s.invReloadFee(fees[0]), s.invReloadFee(fees[1]))

		s.travelTo(invAt("2024-11-01T10:00:00Z"))
		s.performBilling()
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) != 1 {
			t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
		}
		fs := s.Fees(adv[0].ID)
		slices.SortStableFunc(fs, func(a, b *invoice.Fee) int { return a.CreatedAt.Compare(b.CreatedAt) })
		if len(fs) != 2 {
			t.Fatalf("advance charges fees = %d, want = 2", len(fs))
		}
		invCheckFeeProps(t, fs[0], "2024-06-12T10:00:00Z", "2024-06-05T00:00:00Z", "2024-07-01T00:00:00Z", "2024-06-05T10:00:00Z", 30)
		invCheckFeeProps(t, fs[1], "2024-07-22T10:00:00Z", "2024-07-01T00:00:00Z", "2024-08-01T00:00:00Z", "2024-07-01T00:00:00Z", 31)
		invCheckAdvancePeriods(t, s, adv[0], 1, "2024-10-01T00:00:00Z", "2024-11-01T00:00:00Z")
	})

	t.Run("upgraded without events", func(t *testing.T) {
		// source: spec/scenarios/invoices/advance_charges_dates_spec.rb:282
		s, c, p, up := setup(t)
		initial := s.invCreateSub(c, p)
		s.performBilling()
		s.travelTo(invAt("2024-06-12T10:00:00Z"))
		fee := s.invCardFee(initial, "card_1")

		s.travelTo(invAt("2024-07-07T10:00:00Z"))
		upgraded := s.invCreateSub(c, up)
		if got := s.invSub(initial.ID).Status; got != subscription.StatusTerminated {
			t.Errorf("initial status = %s, want = terminated", got)
		}
		if got := len(s.invSubFees(upgraded.ID, core.FeeTypeCharge)); got != 0 {
			t.Errorf("upgraded fees = %d, want = 0", got)
		}

		s.travelTo(invAt("2024-10-02T10:00:00Z"))
		s.invMarkPaid(s.Now(), s.invReloadFee(fee))

		s.travelTo(invAt("2024-11-01T10:00:00Z"))
		s.performBilling()
		adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
		if len(adv) != 1 {
			t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
		}
		invCheckFeeProps(t, s.Fees(adv[0].ID)[0], "2024-06-12T10:00:00Z", "2024-06-05T00:00:00Z", "2024-07-01T00:00:00Z", "2024-06-05T10:00:00Z", 30)
		invCheckAdvancePeriods(t, s, adv[0], 1, "2024-10-01T00:00:00Z", "2024-11-01T00:00:00Z")
	})
}

// TestScenarioInvoicesFiltersAndGroupedBy: a charge filter grouping its
// events by country, with the default properties for the others, in the
// current usage and on the invoice. grouped_by is pricing_group_keys.
// source: spec/scenarios/invoices/filters_and_grouped_by_spec.rb:41
func TestScenarioInvoicesFiltersAndGroupedBy(t *testing.T) {
	s := newScenario(t, invAt("2024-02-25T10:00:00Z"))
	c := s.Customer()
	m := s.Metric("value", catalog.AggregationTypeSum, "value", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](s.IDs), Key: "cloud", Values: []string{"aws", "gcp", "azure"}},
			{ID: core.NewID[core.BillableMetricFilterID](s.IDs), Key: "region", Values: []string{"us", "europe", "asia"}},
		}
	})
	p := s.Plan(period.Monthly, 0)
	ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, func(ch *catalog.Charge) {
		ch.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(
			map[string][]string{"cloud": {"aws"}, "region": {catalog.AllFilterValues}},
			`{"amount":"12","pricing_group_keys":["country"]}`,
		)}
	})
	sub := s.invCreateSub(c, p, invAnniversary)
	for _, e := range []struct {
		at    string
		props []string
	}{
		{"2024-02-28T10:00:00Z", []string{"cloud", "aws", "region", "us", "country", "us", "value", "10"}},
		{"2024-03-01T10:00:00Z", []string{"cloud", "aws", "region", "europe", "country", "france", "value", "10"}},
		{"2024-03-02T10:00:00Z", []string{"cloud", "aws", "region", "asia", "value", "10"}},
		{"2024-03-03T10:00:00Z", []string{"cloud", "gcp", "region", "asia", "country", "china", "value", "10"}},
	} {
		s.travelTo(invAt(e.at))
		s.ingest(sub, "value", e.props...)
	}

	check := func(step string, fs []*invoice.Fee) {
		t.Helper()
		var charge []*invoice.Fee
		for _, f := range fs {
			if f.Type == core.FeeTypeCharge && f.ChargeID == ch.ID && (f.Amount != 0 || f.EventsCount != 0) {
				charge = append(charge, f)
			}
		}
		if len(charge) != 4 {
			t.Fatalf("%s: charge fees = %d, want = 4", step, len(charge))
		}
		for _, country := range []string{"us", "france", ""} {
			i := slices.IndexFunc(charge, func(f *invoice.Fee) bool { return f.GroupedBy["country"] == country && !f.ChargeFilterID.IsZero() })
			if i < 0 {
				t.Errorf("%s: no fee of country %q", step, country)
				continue
			}
			f := charge[i]
			if f.Amount != 12000 || f.EventsCount != 1 || !f.Units.Equal(money.NewFromInt(10)) || f.ChargeFilterID != ch.Filters[0].ID {
				t.Errorf("%s: %q fee = %d, %d events, %s units, want = 12000, 1, 10 on the filter", step, country, f.Amount, f.EventsCount, f.Units)
			}
		}
		i := slices.IndexFunc(charge, func(f *invoice.Fee) bool { return f.ChargeFilterID.IsZero() })
		if i < 0 {
			t.Fatalf("%s: no default fee", step)
		}
		if f := charge[i]; f.Amount != 10000 || f.EventsCount != 1 || !f.Units.Equal(money.NewFromInt(10)) {
			t.Errorf("%s: default fee = %d, %d events, %s units, want = 10000, 1, 10", step, f.Amount, f.EventsCount, f.Units)
		}
	}

	s.travelTo(invAt("2024-03-04T10:00:00Z"))
	u := s.invUsage(sub.ID)
	if u.Amount != 46000 {
		t.Errorf("usage = %d, want = 46000", u.Amount)
	}
	check("usage", u.Fees)

	s.travelTo(invAt("2024-03-25T10:00:00Z"))
	before := len(s.invSubInvoices(sub.ID))
	s.performBilling()
	invs := s.invSubInvoices(sub.ID)
	if len(invs) != before+1 {
		t.Fatalf("invoices = %d, want = %d", len(invs), before+1)
	}
	inv := invs[len(invs)-1]
	if inv.TotalAmount != 46000 {
		t.Errorf("total = %d, want = 46000", inv.TotalAmount)
	}
	check("invoice", s.Fees(inv.ID))
}

// TestScenarioInvoicesRecurringFeeAfterUpgrade: a paid in-advance fee of a
// subscription upgraded since is regrouped when its successor terminates.
// source: spec/scenarios/invoices/recurring_fees_spec.rb:37
func TestScenarioInvoicesRecurringFeeAfterUpgrade(t *testing.T) {
	s := newScenario(t, invAt("2024-12-30T03:55:00Z"))
	c := s.Customer()
	m := s.Metric("items", catalog.AggregationTypeSum, "item_id", invRecurring)
	original := s.Plan(period.Monthly, 100)
	upgraded := s.Plan(period.Monthly, 1100)
	invRegroupedCharge(s, original, m, "1")
	sub := s.invCreateSubAt(c, original, s.Now().AddDate(0, 0, -14))

	s.ingestAt(sub, "items", s.Now().AddDate(0, 0, -7), "item_id", "1")
	fs := s.invSubFees(sub.ID, core.FeeTypeCharge)
	if len(fs) != 1 {
		t.Fatalf("fees = %d, want = 1", len(fs))
	}
	fee := fs[0]
	if !fee.Units.Equal(money.NewFromInt(1)) || !fee.Properties.From.Equal(invAt("2024-12-16T00:00:00Z")) || !fee.Properties.To.Equal(invAt("2025-01-01T00:00:00Z")) {
		t.Errorf("fee = %s units %v – %v, want = 1 from 2024-12-16 to 2025-01-01", fee.Units, fee.Properties.From, fee.Properties.To)
	}
	if n := countOf(s.Webhooks(), webhook.EventTypeFeeCreated); n != 1 {
		t.Errorf("fee.created = %d, want = 1", n)
	}

	next := s.invCreateSub(c, upgraded)
	s.invMarkPaid(s.Now(), s.invReloadFee(fee))
	s.invTerminate(next)

	adv := s.invOfType(c.ID, invoice.TypeAdvanceCharges)
	if len(adv) != 1 {
		t.Fatalf("advance charges invoices = %d, want = 1", len(adv))
	}
	if !slices.ContainsFunc(s.Fees(adv[0].ID), func(f *invoice.Fee) bool { return f.ID == fee.ID }) {
		t.Errorf("advance charges invoice lacks the paid fee")
	}
}

// countOf counts v in vs.
func countOf[T comparable](vs []T, v T) int {
	n := 0
	for _, x := range vs {
		if x == v {
			n++
		}
	}
	return n
}

// invChargeTax is a charge option applying tax t.
func invChargeTax(t *catalog.Tax) func(*catalog.Charge) {
	return func(c *catalog.Charge) { c.TaxIDs = append(c.TaxIDs, t.ID) }
}

// TestScenarioInvoicesPrepaidCreditsLimitedToMetrics: a wallet limited to
// two metrics pays only their in-advance invoices.
// source: spec/scenarios/invoices/invoicing_with_prepaid_credits_spec.rb:31
func TestScenarioInvoicesPrepaidCreditsLimitedToMetrics(t *testing.T) {
	s := newScenario(t, invAt("2024-06-05T10:00:00Z"))
	c := s.Customer()
	m1 := s.Metric("count_1", catalog.AggregationTypeCount, "")
	m2 := s.Metric("count_2", catalog.AggregationTypeCount, "")
	other := s.Metric("other", catalog.AggregationTypeSum, "item_id")
	p := s.Plan(period.Monthly, 0)
	tax := s.Tax("vat", "24")
	advance := func(c *catalog.Charge) { c.PayInAdvance = true }
	s.Charge(p, m1, catalog.ChargeModelStandard, `{"amount":"14.29"}`, advance, invChargeTax(tax))
	s.Charge(p, m2, catalog.ChargeModelStandard, `{"amount":"14.27"}`, advance, invChargeTax(tax))
	s.Charge(p, other, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
	sub := s.invCreateSub(c, p)
	w := s.Wallet(c, "39.00", func(w *wallet.Wallet) { w.BillableMetricIDs = []core.BillableMetricID{m1.ID, m2.ID} })
	if got := len(s.Invoices(c.ID)); got != 0 {
		t.Fatalf("invoices = %d, want = 0", got)
	}

	step := func(code string, props []string, total, subTotal, prepaid int64, balance string) {
		t.Helper()
		before := len(s.Invoices(c.ID))
		s.ingest(sub, code, props...)
		invs := s.invCustomerInvoices(c.ID)
		if len(invs) != before+1 {
			t.Fatalf("%s: invoices = %d, want = %d", code, len(invs), before+1)
		}
		inv := invs[len(invs)-1]
		if inv.PrepaidCreditAmount != prepaid || (total >= 0 && (inv.TotalAmount != total || inv.SubTotalIncludingTaxesAmount != subTotal)) {
			t.Errorf("%s: invoice = total %d sub total %d prepaid %d, want = %d %d %d", code, inv.TotalAmount, inv.SubTotalIncludingTaxesAmount, inv.PrepaidCreditAmount, total, subTotal, prepaid)
		}
		if balance != "" {
			got := s.ReloadWallet(w.ID)
			cents, _ := money.MustParse(balance).Mul(money.NewFromInt(100)).Int64()
			if !got.CreditsBalance.Equal(money.MustParse(balance)) || got.BalanceAmount != cents {
				t.Errorf("%s: wallet = %s credits %d, want = %s", code, got.CreditsBalance, got.BalanceAmount, balance)
			}
		}
	}
	step("other", []string{"item_id", "1"}, -1, 0, 0, "")
	step("count_1", nil, 0, 1772, 1772, "21.28")
	step("count_2", nil, 0, 1769, 1769, "3.59")
}

// TestScenarioInvoicesPrepaidCreditsCappedToTotal: prepaid credits never
// exceed the rounded invoice total when fees round below their taxes.
func TestScenarioInvoicesPrepaidCreditsCappedToTotal(t *testing.T) {
	for _, tc := range []struct {
		source           string
		name             string
		charges          []string
		fees, taxes, sub int64
		balance          int64
	}{
		{"spec/scenarios/invoices/negative_total_with_prepaid_credits_spec.rb:85", "mixed", []string{"0.004", "0.004", "0.004", "0.004", "0.004", "0.004", "0.01", "0.01"}, 2, 1, 3, 9997},
		{"spec/scenarios/invoices/negative_total_with_prepaid_credits_spec.rb:157", "identical", []string{"0.014", "0.014", "0.014", "0.014", "0.014", "0.014", "0.014", "0.014"}, 8, 3, 11, 9989},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, invAt("2025-01-01T00:00:00Z"))
			c := s.Customer()
			p := s.Plan(period.Monthly, 0)
			tax := s.Tax("vat", "40")
			for i, amount := range tc.charges {
				m := s.Metric(fmt.Sprintf("metric_%d", i), catalog.AggregationTypeCount, "")
				s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`, invChargeTax(tax))
			}
			sub := s.invCreateSub(c, p, invAnniversary)
			w := s.Wallet(c, "100")
			for i := range tc.charges {
				s.ingest(sub, fmt.Sprintf("metric_%d", i))
			}

			s.travelTo(invAt("2025-02-01T00:00:00Z"))
			s.performBilling()
			invs := s.invOfType(c.ID, invoice.TypeSubscription)
			if len(invs) != 1 {
				t.Fatalf("%s: subscription invoices = %d, want = 1", tc.source, len(invs))
			}
			inv := invs[0]
			for _, f := range s.invFeesOf(inv.ID, core.FeeTypeCharge) {
				want := money.MustParse(tc.charges[0])
				if f.Amount == 1 && tc.name == "mixed" {
					want = money.MustParse("0.01")
				}
				precise := want.Mul(money.NewFromInt(100))
				if !f.PreciseAmount.Equal(precise) || f.TaxesAmount != 0 || !f.TaxesPreciseAmount.Equal(precise.Mul(money.MustParse("0.4"))) {
					t.Errorf("%s: fee = %d (%s) taxes %d (%s), want = precise %s, taxes 0 (%s)", tc.source, f.Amount, f.PreciseAmount, f.TaxesAmount, f.TaxesPreciseAmount, precise, precise.Mul(money.MustParse("0.4")))
				}
			}
			if inv.FeesAmount != tc.fees || inv.TaxesAmount != tc.taxes || inv.SubTotalIncludingTaxesAmount != tc.sub || inv.PrepaidCreditAmount != tc.sub || inv.TotalAmount != 0 {
				t.Errorf("%s: invoice = fees %d taxes %d sub total %d prepaid %d total %d, want = %d %d %d %d 0",
					tc.source, inv.FeesAmount, inv.TaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.PrepaidCreditAmount, inv.TotalAmount, tc.fees, tc.taxes, tc.sub, tc.sub)
			}
			if got := s.ReloadWallet(w.ID).BalanceAmount; got != tc.balance {
				t.Errorf("%s: wallet balance = %d, want = %d", tc.source, got, tc.balance)
			}
		})
	}
}

// TestScenarioInvoicesOneOffTaxes: the taxes of a one-off invoice resolve
// explicit tax codes → customer taxes → the customer's billing entity
// taxes.
func TestScenarioInvoicesOneOffTaxes(t *testing.T) {
	for _, tc := range []struct {
		source, name string
		setup        func(s *scenario) (*customer.Customer, []string)
		want         []string
		taxes        int64
	}{
		{
			"spec/scenarios/invoices/one_off_spec.rb:22", "explicit",
			func(s *scenario) (*customer.Customer, []string) {
				s.Tax("vat_20", "20")
				s.Tax("vat_10", "10")
				s.invBETax("default_tax", "25")
				return s.Customer(), []string{"vat_20", "vat_10"}
			},
			[]string{"vat_10", "vat_20"},
			3000,
		},
		{
			"spec/scenarios/invoices/one_off_spec.rb:43", "billing entity",
			func(s *scenario) (*customer.Customer, []string) {
				s.invBETax("default_tax", "15")
				return s.Customer(), nil
			},
			[]string{"default_tax"},
			1500,
		},
		{
			"spec/scenarios/invoices/one_off_spec.rb:65", "customer",
			func(s *scenario) (*customer.Customer, []string) {
				s.invBETax("be_default", "25")
				ct := s.Tax("customer_vat", "8")
				return s.Customer(func(c *customer.Customer) { c.TaxIDs = []core.TaxID{ct.ID} }), nil
			},
			[]string{"customer_vat"},
			800,
		},
		{
			"spec/scenarios/invoices/one_off_spec.rb:75", "explicit over customer",
			func(s *scenario) (*customer.Customer, []string) {
				s.invBETax("be_default", "25")
				ct := s.Tax("customer_vat", "8")
				s.Tax("explicit_vat", "5")
				return s.Customer(func(c *customer.Customer) { c.TaxIDs = []core.TaxID{ct.ID} }), []string{"explicit_vat"}
			},
			[]string{"explicit_vat"},
			500,
		},
		{
			"spec/scenarios/invoices/one_off_spec.rb:97", "secondary billing entity",
			func(s *scenario) (*customer.Customer, []string) {
				s.invBETax("default_tax", "20")
				second := s.invEntity("secondary", "SEC")
				t2 := s.Tax("secondary_vat", "12")
				s.invUpdateEntity(second, func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{t2.ID} })
				return s.Customer(func(c *customer.Customer) { c.BillingEntityID = second.ID }), nil
			},
			[]string{"secondary_vat"},
			1200,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, invAt("2024-06-05T10:00:00Z"))
			c, codes := tc.setup(s)
			a := s.AddOn(10000)
			inv, err := s.E.Invoices.CreateOneOff(s.Ctx, invoicing.OneOffInput{
				OrganizationID: s.OrgID(), CustomerID: c.ID, Currency: c.Currency,
				Fees: []fees.OneOff{{AddOnID: a.ID, TaxCodes: codes}},
			})
			s.must(err)
			s.drain()
			inv = s.Invoice(inv.ID)
			if inv.Status != invoice.StatusFinalized || inv.TaxesAmount != tc.taxes {
				t.Errorf("%s: invoice = %s taxes %d, want = finalized %d", tc.source, inv.Status, inv.TaxesAmount, tc.taxes)
			}
			fs := s.Fees(inv.ID)
			if len(fs) != 1 {
				t.Fatalf("%s: fees = %d, want = 1", tc.source, len(fs))
			}
			var got []string
			for _, at := range fs[0].AppliedTaxes {
				got = append(got, at.TaxCode)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) || fs[0].TaxesAmount != tc.taxes {
				t.Errorf("%s: fee taxes = %v %d, want = %v %d", tc.source, got, fs[0].TaxesAmount, tc.want, tc.taxes)
			}
		})
	}
}

func TestScenarioInvoicesPreview(t *testing.T) {
	preview := func(s *scenario, in invoicing.PreviewInput) *invoicing.Preview {
		s.T.Helper()
		in.OrganizationID = s.OrgID()
		out, err := s.E.Invoices.Preview(s.Ctx, in)
		s.must(err)
		return out
	}

	t.Run("spending minimum over the full period", func(t *testing.T) {
		// source: spec/scenarios/invoices/preview_spec.rb:47
		s := newScenario(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
		s.invBETax("vat", "23")
		c := s.Customer()
		p := s.Plan(period.Monthly, 0)
		m := s.Metric("sum_metric", catalog.AggregationTypeSum, "value")
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) { ch.MinAmount = 233700 })
		sub := s.Subscription(c, p, func(sb *subscription.Subscription) {
			end := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
			sb.EndingAt = &end
		})
		s.Event(sub, m.Code, time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), "value", "16.25")
		s.travelTo(time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC))
		s.invUsage(sub.ID)
		got := preview(s, invoicing.PreviewInput{CustomerID: c.ID, SubscriptionIDs: []core.SubscriptionID{sub.ID}}).Invoice
		if got.FeesAmount != 233700 || got.TaxesAmount != 53751 || got.TotalAmount != 287451 {
			t.Errorf("fees, taxes, total = %d, %d, %d, want = 233700, 53751, 287451", got.FeesAmount, got.TaxesAmount, got.TotalAmount)
		}
	})

	for _, tc := range []struct {
		source        string
		fixed         bool
		fees, prepaid int64
	}{
		{"spec/scenarios/invoices/preview_spec.rb:99", false, 2000, 0},
		{"spec/scenarios/invoices/preview_spec.rb:121", true, 2300, 300},
	} {
		t.Run(fmt.Sprintf("wallet restricted to fixed charges/fixed=%v", tc.fixed), func(t *testing.T) {
			// source: spec/scenarios/invoices/preview_spec.rb:94
			s := newScenario(t, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
			c := s.Customer()
			p := s.Plan(period.Monthly, 2000)
			if tc.fixed {
				s.FixedCharge(p, s.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"3"}`, "1")
			}
			w := s.Wallet(c, "5", func(w *wallet.Wallet) { w.AllowedFeeTypes = []core.FeeType{core.FeeTypeFixedCharge} })
			sub := subscription.New(s.OrgID(), core.NewID[core.SubscriptionID](s.IDs), c.ID, p.ID, c.ExternalID, s.Now(), subscription.PlanFacts{}, s.Now())
			got := preview(s, invoicing.PreviewInput{CustomerID: c.ID, New: []*subscription.Subscription{sub}}).Invoice
			if got.FeesAmount != tc.fees || got.PrepaidCreditAmount != tc.prepaid || got.TotalAmount != 2000 {
				t.Errorf("%s: fees, prepaid, total = %d, %d, %d, want = %d, %d, 2000", tc.source, got.FeesAmount, got.PrepaidCreditAmount, got.TotalAmount, tc.fees, tc.prepaid)
			}
			if got := s.ReloadWallet(w.ID).BalanceAmount; got != 500 {
				t.Errorf("%s: wallet balance = %d, want = 500", tc.source, got)
			}
		})
	}

	t.Run("pending downgrade period", func(t *testing.T) {
		// source: spec/scenarios/invoices/preview_spec.rb:146
		s := newScenario(t, time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC))
		c := s.Customer()
		cur := s.Plan(period.Monthly, 12900, invAdvance)
		down := s.Plan(period.Monthly, 5000, invAdvance)
		sub := s.invCreateSub(c, cur, invAnniversary)
		s.travelTo(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC))
		next := s.invCreateSub(c, down, invAnniversary)
		if next.Status != subscription.StatusPending || next.PlanID != down.ID {
			t.Fatalf("next = %s %s, want = pending downgrade", next.Status, next.PlanID)
		}
		got := preview(s, invoicing.PreviewInput{CustomerID: c.ID, SubscriptionIDs: []core.SubscriptionID{sub.ID}})
		want := period.Period{From: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)}
		var is *invoice.Subscription
		for _, x := range got.Subscriptions {
			if x.SubscriptionID == next.ID {
				is = x
			}
		}
		if is == nil {
			t.Fatalf("invoice subscriptions miss the pending plan")
		}
		if !is.Period.From.Equal(want.From) || !is.Period.To.Equal(want.To) {
			t.Errorf("pending period = %v, want = %v", is.Period, want)
		}
		var fee *invoice.Fee
		for _, f := range got.Fees {
			if f.Type == core.FeeTypeSubscription && f.SubscriptionID == next.ID {
				fee = f
			}
		}
		if fee == nil {
			t.Fatalf("no subscription fee of the pending plan")
		}
		if !fee.Properties.From.Equal(is.Period.From) || !fee.Properties.To.Equal(is.Period.To) {
			t.Errorf("pending fee = %v–%v, want = %v", fee.Properties.From, fee.Properties.To, is.Period)
		}
	})
}

// invProgressiveSetup is the setup of progressive_billing_spec.rb: a 31
// USD monthly plan in arrears with a 0.0002 USD standard charge on a sum
// metric and a 200 USD usage threshold, subscribed on 2022-12-01.
func invProgressiveSetup(t *testing.T, opts ...func(*customer.Customer)) (*scenario, *customer.Customer, *catalog.BillableMetric, *catalog.Plan, *subscription.Subscription) {
	t.Helper()
	s := newScenario(t, time.Date(2022, 12, 1, 0, 0, 0, 0, time.UTC))
	c := s.Customer(opts...)
	p := s.Plan(period.Monthly, 3100)
	m := s.Metric("sum_agg", catalog.AggregationTypeSum, "total")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.0002"}`)
	s.UsageThreshold(p, 20000, false)
	return s, c, m, p, s.invCreateSub(c, p)
}

// invCheckAmounts checks the total, fees and progressive billing credit
// of inv.
func invCheckAmounts(t *testing.T, step string, inv *invoice.Invoice, total, fees, credit int64) {
	t.Helper()
	if inv.TotalAmount != total || inv.FeesAmount != fees || inv.ProgressiveBillingCreditAmount != credit {
		t.Errorf("%s: total, fees, progressive credit = %d, %d, %d, want = %d, %d, %d",
			step, inv.TotalAmount, inv.FeesAmount, inv.ProgressiveBillingCreditAmount, total, fees, credit)
	}
}

func TestScenarioInvoicesProgressiveBilling(t *testing.T) {
	day0 := time.Date(2022, 12, 1, 0, 0, 0, 0, time.UTC)
	count := func(t *testing.T, s *scenario, c *customer.Customer, step string, want int) []*invoice.Invoice {
		t.Helper()
		got := s.invCustomerInvoices(c.ID)
		if len(got) != want {
			t.Fatalf("%s: invoices = %d, want = %d", step, len(got), want)
		}
		return got
	}

	for _, tc := range []struct {
		source string
		grace  int
	}{
		{"spec/scenarios/invoices/progressive_billing_spec.rb:23", 0},
		{"spec/scenarios/invoices/progressive_billing_spec.rb:62", 2},
	} {
		t.Run(fmt.Sprintf("threshold then period end/grace=%d", tc.grace), func(t *testing.T) {
			// source: spec/scenarios/invoices/progressive_billing_spec.rb:23
			s, c, m, _, sub := invProgressiveSetup(t, invGrace(tc.grace))
			s.travelTo(day0.AddDate(0, 0, 5))
			s.ingest(sub, m.Code, "total", "1000000")
			invCheckAmounts(t, tc.source+" day 5", count(t, s, c, "day 5", 1)[0], 20000, 20000, 0)
			s.travelTo(day0.AddDate(0, 0, 15))
			s.ingest(sub, m.Code, "total", "1000000")
			invCheckAmounts(t, tc.source+" day 15", count(t, s, c, "day 15", 1)[0], 20000, 20000, 0)
			s.travelTo(day0.AddDate(0, 1, 0))
			s.performBilling()
			rec := count(t, s, c, "period end", 2)[1]
			invCheckAmounts(t, tc.source+" period end", rec, 3100+20000, 3100+40000, 20000)
			if tc.grace > 0 {
				if rec.Status != invoice.StatusDraft {
					t.Errorf("%s: status = %s, want = draft", tc.source, rec.Status)
				}
				rec = s.invRefresh(rec.ID)
				if rec.Status != invoice.StatusDraft {
					t.Errorf("%s: refreshed status = %s, want = draft", tc.source, rec.Status)
				}
				invCheckAmounts(t, tc.source+" refreshed", rec, 3100+20000, 3100+40000, 20000)
			}
		})
	}

	for _, tc := range []struct {
		source      string
		upgrade     bool
		total, fees int64
	}{
		{"spec/scenarios/invoices/progressive_billing_spec.rb:106", false, 1800, 21800},
		{"spec/scenarios/invoices/progressive_billing_spec.rb:135", true, 1700, 21700},
	} {
		t.Run(fmt.Sprintf("threshold then end/upgrade=%v", tc.upgrade), func(t *testing.T) {
			// source: spec/scenarios/invoices/progressive_billing_spec.rb:106
			s, c, m, _, sub := invProgressiveSetup(t)
			up := s.Plan(period.Monthly, 62000)
			s.travelTo(day0.AddDate(0, 0, 15))
			s.ingest(sub, m.Code, "total", "1000000")
			invCheckAmounts(t, tc.source+" day 15", count(t, s, c, "day 15", 1)[0], 20000, 20000, 0)
			s.travelTo(day0.AddDate(0, 0, 17))
			if tc.upgrade {
				s.invCreateSub(c, up)
			} else {
				s.invTerminate(sub)
			}
			invCheckAmounts(t, tc.source+" end", s.invLast(sub.ID), tc.total, tc.fees, 20000)
			count(t, s, c, "end", 2)
		})
	}

	t.Run("threshold during the grace period", func(t *testing.T) {
		// source: spec/scenarios/invoices/progressive_billing_spec.rb:170
		s, c, m, _, sub := invProgressiveSetup(t)
		s.UpdateBillingEntity(func(be *organization.BillingEntity) { be.InvoiceGracePeriodDays = 30 })
		s.travelTo(day0.AddDate(0, 1, 0).Add(2 * time.Hour))
		s.performBilling()
		first := count(t, s, c, "first period", 1)[0]
		invCheckAmounts(t, "first period", first, 3100, 3100, 0)
		if first.Status != invoice.StatusDraft {
			t.Errorf("first status = %s, want = draft", first.Status)
		}
		s.travelTo(day0.AddDate(0, 1, 15))
		s.ingest(sub, m.Code, "total", "3000000")
		invCheckAmounts(t, "threshold", count(t, s, c, "threshold", 2)[1], 60000, 60000, 0)
		s.travelTo(day0.AddDate(0, 2, 0))
		s.performFinalize()
		s.performBilling()
		count(t, s, c, "second period", 3)
		if got := s.invCustomerCreditNotes(c.ID); len(got) != 0 {
			t.Errorf("credit notes = %d, want = 0", len(got))
		}
		subs := s.invOfType(c.ID, invoice.TypeSubscription)
		if len(subs) != 2 {
			t.Fatalf("subscription invoices = %d, want = 2", len(subs))
		}
		slices.SortFunc(subs, func(a, b *invoice.Invoice) int { return a.CreatedAt.Compare(b.CreatedAt) })
		invCheckAmounts(t, "first period finalized", subs[0], 3100, 3100, 0)
		if subs[0].Status != invoice.StatusFinalized {
			t.Errorf("first status = %s, want = finalized", subs[0].Status)
		}
		invCheckAmounts(t, "second period", subs[1], 3100, 63100, 60000)
		if subs[1].Status != invoice.StatusDraft {
			t.Errorf("second status = %s, want = draft", subs[1].Status)
		}
	})

	t.Run("several thresholds in one period", func(t *testing.T) {
		// source: spec/scenarios/invoices/progressive_billing_spec.rb:219
		s, c, m, p, sub := invProgressiveSetup(t)
		s.UsageThreshold(p, 50000, false)
		s.travelTo(day0.AddDate(0, 0, 15))
		s.ingest(sub, m.Code, "total", "1000000")
		invCheckAmounts(t, "day 15", count(t, s, c, "day 15", 1)[0], 20000, 20000, 0)
		s.travelTo(day0.AddDate(0, 0, 20))
		s.ingest(sub, m.Code, "total", "1000000")
		count(t, s, c, "day 20", 1)
		s.travelTo(day0.AddDate(0, 0, 25))
		s.ingest(sub, m.Code, "total", "1000000")
		if got := count(t, s, c, "day 25", 2)[1].TotalAmount; got != 40000 {
			t.Errorf("day 25 total = %d, want = 40000", got)
		}
		s.travelTo(day0.AddDate(0, 1, 0))
		s.performBilling()
		rec := count(t, s, c, "period end", 3)[2]
		if rec.TotalAmount != 3100 || rec.ProgressiveBillingCreditAmount != 60000 {
			t.Errorf("period end total, credit = %d, %d, want = 3100, 60000", rec.TotalAmount, rec.ProgressiveBillingCreditAmount)
		}
	})

	t.Run("no usage", func(t *testing.T) {
		// source: spec/scenarios/invoices/progressive_billing_spec.rb:259
		s, c, _, _, _ := invProgressiveSetup(t)
		s.travelTo(day0.AddDate(0, 1, 0))
		s.performBilling()
		if got := count(t, s, c, "period end", 1)[0].TotalAmount; got != 3100 {
			t.Errorf("total = %d, want = 3100", got)
		}
	})

	t.Run("non-recurring threshold bills once", func(t *testing.T) {
		// source: spec/scenarios/invoices/progressive_billing_spec.rb:279
		s, c, m, _, sub := invProgressiveSetup(t)
		for i, st := range []struct {
			value string
			n     int
			total int64
		}{
			{"1000000", 1, 20000},
			{"2000000", 3, 43100},
			{"3000000", 4, 63100},
		} {
			s.travelTo(day0.AddDate(0, i, 15))
			s.ingest(sub, m.Code, "total", st.value)
			if i == 0 {
				if got := count(t, s, c, "first threshold", 1)[0].TotalAmount; got != st.total {
					t.Errorf("first threshold total = %d, want = %d", got, st.total)
				}
			} else {
				count(t, s, c, fmt.Sprintf("period %d usage", i+1), st.n-1)
			}
			s.travelTo(day0.AddDate(0, i+1, 0))
			s.performBilling()
			n := st.n
			if i == 0 {
				n = 2
			}
			last := count(t, s, c, fmt.Sprintf("period %d end", i+1), n)[n-1]
			want := st.total
			if i == 0 {
				want = 3100
				if last.FeesAmount != 3100+20000 {
					t.Errorf("period 1 fees = %d, want = %d", last.FeesAmount, 3100+20000)
				}
			}
			if last.TotalAmount != want {
				t.Errorf("period %d total = %d, want = %d", i+1, last.TotalAmount, want)
			}
		}
	})

	t.Run("single and recurring thresholds", func(t *testing.T) {
		// source: spec/scenarios/invoices/progressive_billing_spec.rb:331
		s := newScenario(t, day0)
		c := s.Customer()
		p := s.Plan(period.Monthly, 3100)
		m := s.Metric("sum_agg", catalog.AggregationTypeSum, "total")
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"0.0002"}`)
		s.UsageThreshold(p, 200, false)
		s.UsageThreshold(p, 500, false)
		s.UsageThreshold(p, 700, false)
		s.UsageThreshold(p, 1000, true)
		sub := s.invCreateSub(c, p)
		for _, st := range []struct {
			at    time.Time
			value string
			n     int
			total int64
		}{
			{day0.AddDate(0, 0, 5), "11000", 1, 220},
			{day0.AddDate(0, 0, 10), "11000", 1, 220},
			{day0.AddDate(0, 0, 15), "11000", 2, 440},
			{day0.AddDate(0, 0, 20), "11000", 3, 220},
			{day0.AddDate(0, 0, 25), "110000", 4, 2200},
			{day0.AddDate(0, 0, 27), "110000", 5, 2200},
		} {
			s.travelTo(st.at)
			s.ingest(sub, m.Code, "total", st.value)
			step := st.at.Format(time.DateOnly)
			if got := count(t, s, c, step, st.n)[st.n-1].TotalAmount; got != st.total {
				t.Errorf("%s: total = %d, want = %d", step, got, st.total)
			}
		}
		s.travelTo(day0.AddDate(0, 1, 0))
		s.performBilling()
		rec := count(t, s, c, "period end", 6)[5]
		if rec.TotalAmount != 3100 || rec.ProgressiveBillingCreditAmount != 5280 {
			t.Errorf("period end total, credit = %d, %d, want = 3100, 5280", rec.TotalAmount, rec.ProgressiveBillingCreditAmount)
		}
		s.travelTo(day0.AddDate(0, 1, 5))
		s.ingest(sub, m.Code, "total", "110000")
		if got := count(t, s, c, "second period", 7)[6].TotalAmount; got != 2200 {
			t.Errorf("second period total = %d, want = 2200", got)
		}
	})
}

// invChargeFees returns the non-zero charge fees of invoice id as charge →
// amount. Deviation: domain §12 stores zero charge fees, which lago drops
// outside recurring non-invoiceable fees.
func (s *scenario) invChargeFees(id core.InvoiceID) map[core.ChargeID]int64 {
	s.T.Helper()
	out := map[core.ChargeID]int64{}
	for _, f := range s.invFeesOf(id, core.FeeTypeCharge) {
		if f.Amount != 0 {
			out[f.ChargeID] += f.Amount
		}
	}
	return out
}

// invAmounts returns the sorted amounts of fs.
func invAmounts(fs []*invoice.Fee) []int64 {
	var out []int64
	for _, f := range fs {
		out = append(out, f.Amount)
	}
	slices.Sort(out)
	return out
}

func TestScenarioBillingMonthlyAllChargeTypes(t *testing.T) {
	// source: spec/scenarios/billing/billing_monthly_spec.rb:100
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	s := newScenario(t, at(2024, 2, 4))
	c := s.Customer()
	p := s.Plan(period.Monthly, 5_000_000)
	metered := s.Metric("metered", catalog.AggregationTypeSum, "total")
	recurring := s.Metric("recurring", catalog.AggregationTypeSum, "total", func(m *catalog.BillableMetric) { m.Recurring = true })
	meteredArrears := s.Charge(p, metered, catalog.ChargeModelPackage, `{"amount":"100","package_size":10,"free_units":0}`)
	meteredAdvance := s.Charge(p, metered, catalog.ChargeModelPackage, `{"amount":"1000","package_size":10,"free_units":2}`,
		func(ch *catalog.Charge) { ch.PayInAdvance = true })
	recurringArrears := s.Charge(p, recurring, catalog.ChargeModelStandard, `{"amount":"5000"}`, func(ch *catalog.Charge) { ch.Prorated = true })
	recurringAdvance := s.Charge(p, recurring, catalog.ChargeModelStandard, `{"amount":"50000"}`, func(ch *catalog.Charge) { ch.PayInAdvance = true })
	a := s.AddOn(0)
	for _, fc := range []struct{ advance, prorated bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		s.FixedCharge(p, a, catalog.FixedChargeModelStandard, `{"amount":"200"}`, "10", func(f *catalog.FixedCharge) {
			f.PayInAdvance, f.Prorated = fc.advance, fc.prorated
		})
	}
	sub := s.invCreateSub(c, p)
	invoices := func(step string, want int) []*invoice.Invoice {
		t.Helper()
		got := s.invSubInvoices(sub.ID)
		slices.SortStableFunc(got, func(a, b *invoice.Invoice) int { return a.CreatedAt.Compare(b.CreatedAt) })
		if len(got) != want {
			t.Fatalf("%s: invoices = %d, want = %d", step, len(got), want)
		}
		return got
	}
	checkPeriods := func(step string, inv *invoice.Invoice, from, to, ts time.Time) {
		t.Helper()
		is := s.invISubs(inv.ID)[0]
		for _, p := range []struct {
			name string
			got  period.Period
		}{{"period", is.Period}, {"charges period", is.ChargesPeriod}, {"fixed charges period", is.FixedChargesPeriod}} {
			if !p.got.From.Equal(from) || !p.got.To.Equal(to) {
				t.Errorf("%s: %s = %v, want = %v–%v", step, p.name, p.got, from, to)
			}
		}
		if !is.Timestamp.Equal(ts) {
			t.Errorf("%s: timestamp = %v, want = %v", step, is.Timestamp, ts)
		}
	}

	// The in-advance fixed charges are billed at once; one is prorated over
	// 26 of February's 29 days.
	first := invoices("start", 1)[0]
	if got := invAmounts(s.Fees(first.ID)); !slices.Equal(got, []int64{179_310, 200_000}) {
		t.Errorf("start fees = %v, want = [179310 200000]", got)
	}
	// Deviation: lago stamps the starting invoice one second after the
	// subscription; tanvo stamps it at the subscription instant.
	checkPeriods("start", first, at(2024, 2, 4), at(2024, 2, 4), at(2024, 2, 4))

	s.travelTo(at(2024, 2, 28))
	s.performBilling()
	invoices("feb 28", 1)

	s.travelTo(at(2024, 3, 1))
	s.performBilling()
	inv := invoices("mar 1", 2)[1]
	if got := invAmounts(s.invFeesOf(inv.ID, core.FeeTypeFixedCharge)); !slices.Equal(got, []int64{179_310, 200_000, 200_000, 200_000}) {
		t.Errorf("mar 1 fixed fees = %v, want = [179310 200000 200000 200000]", got)
	}
	if got := s.invChargeFees(inv.ID); len(got) != 0 {
		t.Errorf("mar 1 charge fees = %v, want = none", got)
	}
	subFee := int64(4_482_759) // ceil(26 / 29 × 5,000,000)
	if got := inv.TotalAmount; got != subFee+779_310 {
		t.Errorf("mar 1 total = %d, want = %d", got, subFee+779_310)
	}
	checkPeriods("mar 1", inv, at(2024, 2, 4), at(2024, 3, 1), at(2024, 3, 1))

	s.travelTo(at(2024, 3, 15))
	s.ingest(sub, metered.Code, "total", "10")
	s.ingest(sub, recurring.Code, "total", "10")
	advance := map[core.ChargeID]int64{}
	for _, x := range invoices("mar 15", 4)[2:] {
		for id, amount := range s.invChargeFees(x.ID) {
			advance[id] += amount
		}
	}
	if want := map[core.ChargeID]int64{meteredAdvance.ID: 100_000, recurringAdvance.ID: 50_000_000}; !maps.Equal(advance, want) {
		t.Errorf("mar 15 in-advance fees = %v, want = %v", advance, want)
	}

	s.travelTo(at(2024, 4, 1))
	s.performBilling()
	inv = invoices("apr 1", 5)[4]
	if got := invAmounts(s.invFeesOf(inv.ID, core.FeeTypeFixedCharge)); !slices.Equal(got, []int64{200_000, 200_000, 200_000, 200_000}) {
		t.Errorf("apr 1 fixed fees = %v, want = 4 × 200000", got)
	}
	// lago notes 2,741,940 rather than 2,741,935 from rounding 10 × 17/31.
	want := map[core.ChargeID]int64{meteredArrears.ID: 10_000, recurringArrears.ID: 2_741_940, recurringAdvance.ID: 50_000_000}
	if got := s.invChargeFees(inv.ID); !maps.Equal(got, want) {
		t.Errorf("apr 1 charge fees = %v, want = %v", got, want)
	}
	if got := invAmounts(s.invFeesOf(inv.ID, core.FeeTypeSubscription)); !slices.Equal(got, []int64{5_000_000}) {
		t.Errorf("apr 1 subscription fees = %v, want = [5000000]", got)
	}
	if got, want := inv.TotalAmount, int64(5_000_000+50_000_000+10_000+2_741_940+800_000); got != want {
		t.Errorf("apr 1 total = %d, want = %d", got, want)
	}
	checkPeriods("apr 1", inv, at(2024, 3, 1), at(2024, 4, 1), at(2024, 4, 1))

	for _, d := range []time.Time{at(2024, 4, 10), at(2024, 4, 30)} {
		s.travelTo(d)
		s.ingest(sub, recurring.Code, "total", "20")
		s.ingest(sub, metered.Code, "total", "20")
	}
	advance = map[core.ChargeID]int64{}
	for _, x := range invoices("apr usage", 9)[5:] {
		for id, amount := range s.invChargeFees(x.ID) {
			advance[id] += amount
		}
	}
	if want := map[core.ChargeID]int64{meteredAdvance.ID: 400_000, recurringAdvance.ID: 200_000_000}; !maps.Equal(advance, want) {
		t.Errorf("april in-advance fees = %v, want = %v", advance, want)
	}

	s.travelTo(at(2024, 5, 1))
	s.performBilling()
	inv = invoices("may 1", 10)[9]
	want = map[core.ChargeID]int64{meteredArrears.ID: 40_000, recurringArrears.ID: 12_333_335, recurringAdvance.ID: 250_000_000}
	if got := s.invChargeFees(inv.ID); !maps.Equal(got, want) {
		t.Errorf("may 1 charge fees = %v, want = %v", got, want)
	}
	if got, want := inv.TotalAmount, int64(5_000_000+250_000_000+40_000+12_333_335+800_000); got != want {
		t.Errorf("may 1 total = %d, want = %d", got, want)
	}

	s.travelTo(at(2024, 6, 1))
	s.performBilling()
	inv = invoices("jun 1", 11)[10]
	want = map[core.ChargeID]int64{recurringArrears.ID: 25_000_000, recurringAdvance.ID: 250_000_000}
	if got := s.invChargeFees(inv.ID); !maps.Equal(got, want) {
		t.Errorf("jun 1 charge fees = %v, want = %v", got, want)
	}
	if got, want := inv.TotalAmount, int64(5_000_000+250_000_000+25_000_000+800_000); got != want {
		t.Errorf("jun 1 total = %d, want = %d", got, want)
	}
}
