package billing_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// walBalanceSetup is the common setup of spec/scenarios/wallets/balance_spec.rb:
// a billing entity with a 10 day grace period, a sum metric on "total"
// and a monthly in-arrears plan of 1.00.
func walBalanceSetup(t *testing.T) (*scenario, *catalog.BillableMetric, *catalog.Plan) {
	t.Helper()
	s := walScenario(t, billingtest.Date(2022, 11, 30), 10)
	m := s.Metric("total_metric", catalog.AggregationTypeSum, "total")
	p := s.Plan(period.Monthly, 100)
	return s, m, p
}

// TestWalletBalanceRecalculation follows a wallet through usage, a draft
// invoice and its finalization.
//
// source: spec/scenarios/wallets/balance_spec.rb:63
func TestWalletBalanceRecalculation(t *testing.T) {
	s, m, p := walBalanceSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	c := s.Customer()

	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), GrantedCredits: walDec("10")})
	s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 1000, OngoingUsage: 0, Credits: "10", CreditsOngoingUsage: "0"})
	txs := s.walTransactions(w)
	s.walExpectTx(txs[0], walTopUpTx("10", "10"))

	s.travelTo(billingtest.Date(2022, 12, 1))
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2022, 12, 6))
	s.ingest(sub, m.Code, "total", "5")
	if got := len(s.walSubInvoices(c, sub)); got != 0 {
		t.Errorf("invoices: got = %d, want = 0", got)
	}
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 500, OngoingUsage: 500, Credits: "10", CreditsOngoing: "5", CreditsOngoingUsage: "5"})
	if got := len(s.walTransactions(w)); got != 1 {
		t.Errorf("transactions: got = %d, want = 1", got)
	}

	s.travelTo(billingtest.Date(2023, 1, 1))
	s.performBilling()
	invs := s.walSubInvoices(c, sub)
	if len(invs) != 1 || invs[0].Status != invoice.StatusDraft {
		t.Fatalf("invoices: got = %d, want = 1 draft", len(invs))
	}
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 400, OngoingUsage: 600, Credits: "10", CreditsOngoing: "4", CreditsOngoingUsage: "6"})

	s.travelTo(billingtest.Date(2023, 1, 6))
	s.ingest(sub, m.Code, "total", "3")
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 100, OngoingUsage: 900, Credits: "10", CreditsOngoing: "1", CreditsOngoingUsage: "9"})

	s.travelTo(billingtest.Date(2023, 1, 11))
	s.performFinalize()
	invs = s.walSubInvoices(c, sub)
	if len(invs) != 1 || invs[0].Status != invoice.StatusFinalized {
		t.Fatalf("invoices: got = %+v, want = 1 finalized", invs)
	}
	s.walExpect(w, walBalances{Balance: 400, OngoingBalance: 100, OngoingUsage: 300, Credits: "4", CreditsOngoing: "1", CreditsOngoingUsage: "3"})
	txs = s.walTransactions(w)
	if len(txs) != 2 {
		t.Fatalf("transactions: got = %d, want = 2", len(txs))
	}
	s.walExpectTx(txs[1], walInvoicedTx("6", "6"))
}

// walInAdvance makes a charge paid in advance.
func walInAdvance(c *catalog.Charge) { c.PayInAdvance = true }

// TestWalletBalanceInAdvanceChargesAndTaxes pays a taxed in-advance charge
// with the wallet, then keeps the draft subscription invoice out of the
// balance but in the ongoing balance.
//
// source: spec/scenarios/wallets/balance_spec.rb:181
func TestWalletBalanceInAdvanceChargesAndTaxes(t *testing.T) {
	s, m, p := walBalanceSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, walInAdvance)
	s.walBillingEntityTax("10")
	c := s.Customer()

	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), GrantedCredits: walDec("100")})
	s.walExpect(w, walBalances{Balance: 10000, OngoingBalance: 10000, Credits: "100", CreditsOngoingUsage: "0"})

	s.travelTo(billingtest.Date(2022, 12, 1))
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2022, 12, 6))
	s.ingest(sub, m.Code, "total", "50")
	if got := len(s.walSubInvoices(c, sub)); got != 1 {
		t.Errorf("invoices: got = %d, want = 1", got)
	}
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 4500, OngoingBalance: 4500, OngoingUsage: 0, Credits: "45", CreditsOngoing: "45", CreditsOngoingUsage: "0"})
	if got := len(s.walTransactions(w)); got != 2 {
		t.Errorf("transactions: got = %d, want = 2", got)
	}

	s.travelTo(billingtest.Date(2023, 1, 1))
	s.performBilling()
	if got := len(s.walSubInvoices(c, sub)); got != 2 {
		t.Errorf("invoices: got = %d, want = 2", got)
	}
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 4500, OngoingBalance: 4390, OngoingUsage: 110, Credits: "45", CreditsOngoing: "43.9", CreditsOngoingUsage: "1.1"})
}

// TestWalletBalanceThresholdsAcrossPlans pays an in-arrears plan, an
// in-advance charge and a recurring progressive billing threshold from
// one wallet at rate 10.
//
// source: spec/scenarios/wallets/balance_spec.rb:253
func TestWalletBalanceThresholdsAcrossPlans(t *testing.T) {
	s, m, _ := walBalanceSetup(t)
	p1 := s.Plan(period.Monthly, 0)
	s.Charge(p1, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	p2 := s.Plan(period.Monthly, 0)
	s.Charge(p2, m, catalog.ChargeModelStandard, `{"amount":"2"}`, walInAdvance)
	p3 := s.Plan(period.Monthly, 0)
	s.Charge(p3, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
	s.UsageThreshold(p3, 200_00, true)
	s.walBillingEntityTax("10")
	c := s.Customer()

	meta := core.Metadata{{Key: "transaction_id", Value: "123"}}
	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), RateAmount: money.MustParse("10"), GrantedCredits: walDec("100"), TransactionMetadata: meta})
	s.walExpect(w, walBalances{Balance: 1000_00, OngoingBalance: 1000_00, Credits: "100", CreditsOngoingUsage: "0"})
	txs := s.walTransactions(w)
	if len(txs) != 1 {
		t.Fatalf("transactions: got = %d, want = 1", len(txs))
	}
	s.walExpectTx(txs[0], walTopUpTx("1000", "100"))
	if !slices.Equal(txs[0].Metadata, meta) {
		t.Errorf("metadata: got = %v, want = %v", txs[0].Metadata, meta)
	}

	s.travelTo(billingtest.Date(2022, 12, 1))
	sub1 := s.walSubscribe(c, p1)
	sub2 := s.walSubscribe(c, p2)
	sub3 := s.walSubscribe(c, p3)

	s.travelTo(billingtest.Date(2022, 12, 5))
	s.ingest(sub1, m.Code, "total", "10")
	s.ingest(sub2, m.Code, "total", "10")
	s.ingest(sub3, m.Code, "total", "10")
	if got := len(s.Invoices(c.ID)); got != 1 {
		t.Errorf("customer invoices: got = %d, want = 1", got)
	}
	invs2 := s.walSubInvoices(c, sub2)
	if len(invs2) != 1 {
		t.Fatalf("sub2 invoices: got = %d, want = 1", len(invs2))
	}
	if got := invs2[0].TotalAmount; got != 0 {
		t.Errorf("sub2 invoice total: got = %d, want = 0", got)
	}
	if got := invs2[0].SubTotalIncludingTaxesAmount; got != 2200 {
		t.Errorf("sub2 invoice sub total incl. taxes: got = %d, want = 2200", got)
	}
	s.walRefresh(c)
	s.walExpect(w, walBalances{Balance: 978_00, OngoingBalance: 857_00, OngoingUsage: 121_00, Credits: "97.8", CreditsOngoing: "85.7", CreditsOngoingUsage: "12.1"})
	txs = s.walTransactions(w)
	if len(txs) != 2 {
		t.Fatalf("transactions: got = %d, want = 2", len(txs))
	}
	s.walExpectTx(txs[1], walInvoicedTx("22", "2.2"))

	s.travelTo(billingtest.Date(2022, 12, 10))
	s.ingest(sub1, m.Code, "total", "10")
	s.ingest(sub2, m.Code, "total", "10")
	s.ingest(sub3, m.Code, "total", "10")
	if got := len(s.Invoices(c.ID)); got != 3 {
		t.Errorf("customer invoices: got = %d, want = 3", got)
	}
	invs2 = s.walSubInvoices(c, sub2)
	invs3 := s.walSubInvoices(c, sub3)
	if len(invs2) != 2 || len(invs3) != 1 {
		t.Fatalf("invoices: got = %d/%d, want = 2/1", len(invs2), len(invs3))
	}
	if got := invs2[1].SubTotalIncludingTaxesAmount; got != 22_00 {
		t.Errorf("sub2 invoice: got = %d, want = 2200", got)
	}
	if got := invs3[0].SubTotalIncludingTaxesAmount; got != 220_00 {
		t.Errorf("sub3 invoice: got = %d, want = 22000", got)
	}
	s.walExpect(w, walBalances{Balance: 736_00, OngoingBalance: 714_00, OngoingUsage: 22_00, Credits: "73.6", CreditsOngoing: "71.4", CreditsOngoingUsage: "2.2"})
	txs = s.walTransactions(w)
	if len(txs) != 4 {
		t.Fatalf("transactions: got = %d, want = 4", len(txs))
	}
	s.walExpectTx(walTxOfInvoice(txs, invs2[1].ID), walInvoicedTx("22", "2.2"))
	s.walExpectTx(walTxOfInvoice(txs, invs3[0].ID), walInvoicedTx("220", "22"))

	s.travelTo(billingtest.Date(2022, 12, 15))
	s.ingest(sub3, m.Code, "total", "20")
	if got := len(s.Invoices(c.ID)); got != 4 {
		t.Errorf("customer invoices: got = %d, want = 4", got)
	}
	invs3 = s.walSubInvoices(c, sub3)
	if len(invs3) != 2 {
		t.Fatalf("sub3 invoices: got = %d, want = 2", len(invs3))
	}
	if got := invs3[1].SubTotalIncludingTaxesAmount; got != 220_00 {
		t.Errorf("sub3 invoice: got = %d, want = 22000", got)
	}
	s.walExpect(w, walBalances{Balance: 516_00, OngoingBalance: 494_00, OngoingUsage: 22_00, Credits: "51.6", CreditsOngoing: "49.4", CreditsOngoingUsage: "2.2"})
	txs = s.walTransactions(w)
	if len(txs) != 5 {
		t.Fatalf("transactions: got = %d, want = 5", len(txs))
	}
	s.walExpectTx(txs[4], walInvoicedTx("220", "22"))
}

// walTxOfInvoice returns the transaction of txs on invoice id, or nil.
func walTxOfInvoice(txs []*wallet.Transaction, id core.InvoiceID) *wallet.Transaction {
	for _, t := range txs {
		if t.InvoiceID == id {
			return t
		}
	}
	return nil
}

// TestWalletBalanceMultipleThresholds bills two fixed thresholds and a
// recurring one from the wallet, then credits the progressive billing on
// the period invoice.
//
// source: spec/scenarios/wallets/balance_spec.rb:416
func TestWalletBalanceMultipleThresholds(t *testing.T) {
	s, m, _ := walBalanceSetup(t)
	p := s.Plan(period.Monthly, 0)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
	s.UsageThreshold(p, 200_00, false)
	s.UsageThreshold(p, 500_00, false)
	s.UsageThreshold(p, 200_00, true)
	other := s.Metric("other_metric", catalog.AggregationTypeSum, "total")
	s.Charge(p, other, catalog.ChargeModelStandard, `{"amount":"10"}`)
	s.walBillingEntityTax("10")
	c := s.Customer()

	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), RateAmount: money.MustParse("10"), GrantedCredits: walDec("100")})
	s.walExpect(w, walBalances{Balance: 1000_00, OngoingBalance: 1000_00, Credits: "100", CreditsOngoingUsage: "0"})

	s.travelTo(billingtest.Date(2022, 12, 1))
	sub := s.walSubscribe(c, p)

	steps := []struct {
		day          int
		units        string
		invoices     int
		lastSubTotal int64
		refresh      bool
		want         walBalances
	}{
		{6, "10", 0, 0, true, walBalances{Balance: 1000_00, OngoingBalance: 890_00, OngoingUsage: 110_00, Credits: "100", CreditsOngoing: "89", CreditsOngoingUsage: "11"}},
		{11, "10", 1, 220_00, false, walBalances{Balance: 780_00, OngoingBalance: 780_00, OngoingUsage: 0, Credits: "78", CreditsOngoing: "78", CreditsOngoingUsage: "0"}},
		{16, "30", 2, 330_00, false, walBalances{Balance: 450_00, OngoingBalance: 450_00, OngoingUsage: 0, Credits: "45", CreditsOngoing: "45", CreditsOngoingUsage: "0"}},
		{21, "20", 3, 220_00, false, walBalances{Balance: 230_00, OngoingBalance: 230_00, OngoingUsage: 0, Credits: "23", CreditsOngoing: "23", CreditsOngoingUsage: "0"}},
	}
	for _, st := range steps {
		s.travelTo(billingtest.Date(2022, 12, st.day))
		s.ingest(sub, m.Code, "total", st.units)
		invs := s.walSubInvoices(c, sub)
		if len(invs) != st.invoices {
			t.Fatalf("day %d invoices: got = %d, want = %d", st.day, len(invs), st.invoices)
		}
		if st.invoices > 0 {
			if got := invs[len(invs)-1].SubTotalIncludingTaxesAmount; got != st.lastSubTotal {
				t.Errorf("day %d last invoice sub total incl. taxes: got = %d, want = %d", st.day, got, st.lastSubTotal)
			}
		}
		if st.refresh {
			s.walRefresh(c)
		}
		s.walExpect(w, st.want)
	}

	s.travelTo(billingtest.Date(2022, 12, 26))
	s.ingest(sub, m.Code, "total", "1")

	s.travelTo(billingtest.Date(2023, 1, 1))
	s.performBilling()
	s.walRefresh(c)
	var drafts []*invoice.Invoice
	for _, inv := range s.walSubInvoices(c, sub) {
		if inv.Status == invoice.StatusDraft {
			drafts = append(drafts, inv)
		}
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts: got = %d, want = 1", len(drafts))
	}
	if drafts[0].TotalAmount == 0 {
		t.Errorf("draft total: got = 0, want != 0")
	}
	if !slices.ContainsFunc(s.Fees(drafts[0].ID), func(f *invoice.Fee) bool { return !f.PreciseCouponsAmount.IsZero() }) {
		t.Errorf("draft fees: got = no fee with coupons, want = a fee with the progressive billing credit")
	}
	s.walExpect(w, walBalances{Balance: 230_00, OngoingBalance: 219_00, OngoingUsage: 11_00, Credits: "23", CreditsOngoing: "21.9", CreditsOngoingUsage: "1.1"})
}

// TestWalletBalanceMultipleWallets spreads the usage of four metrics over
// four prioritized wallets with limitations.
func TestWalletBalanceMultipleWallets(t *testing.T) {
	type limit struct {
		metrics  []int // indexes into storage, seats, api, sms
		feeTypes []core.FeeType
	}
	cases := []struct {
		name      string
		source    string
		limits    [4]limit
		filtered  bool
		threshold bool
		want      [4]walBalances
	}{
		{
			name:   "each wallet limited to 1 billable metric",
			source: "spec/scenarios/wallets/balance_spec.rb:569",
			limits: [4]limit{{metrics: []int{0}}, {metrics: []int{1}}, {metrics: []int{2}}, {metrics: []int{3}}},
			want: [4]walBalances{
				{Balance: 1000, OngoingUsage: 1000, OngoingBalance: 0, Credits: "10", CreditsOngoingUsage: "10", CreditsOngoing: "0"},
				{Balance: 2000, OngoingUsage: 2000, OngoingBalance: 0, Credits: "20", CreditsOngoingUsage: "20", CreditsOngoing: "0"},
				{Balance: 3000, OngoingUsage: 3000, OngoingBalance: 0, Credits: "30", CreditsOngoingUsage: "30", CreditsOngoing: "0"},
				{Balance: 4000, OngoingUsage: 4000, OngoingBalance: 0, Credits: "40", CreditsOngoingUsage: "40", CreditsOngoing: "0"},
			},
		},
		{
			name:   "each wallet limited to 2 billable metrics",
			source: "spec/scenarios/wallets/balance_spec.rb:644",
			limits: [4]limit{{metrics: []int{0, 1}}, {metrics: []int{1, 2}}, {metrics: []int{2, 3}}, {metrics: []int{3, 0}}},
			want:   walTwoMetricsWant,
		},
		{
			name:     "each wallet limited to 2 billable metrics with filtered events",
			source:   "spec/scenarios/wallets/balance_spec.rb:721",
			limits:   [4]limit{{metrics: []int{0, 1}}, {metrics: []int{1, 2}}, {metrics: []int{2, 3}}, {metrics: []int{3, 0}}},
			filtered: true,
			want:     walTwoMetricsWant,
		},
		{
			name:   "wallets limited to fee types",
			source: "spec/scenarios/wallets/balance_spec.rb:808",
			limits: [4]limit{
				{feeTypes: []core.FeeType{core.FeeTypeSubscription}},
				{feeTypes: []core.FeeType{core.FeeTypeCharge}},
				{feeTypes: []core.FeeType{core.FeeTypeCharge}},
				{feeTypes: []core.FeeType{core.FeeTypeCharge}},
			},
			want: [4]walBalances{
				{Balance: 1000, OngoingUsage: 0, OngoingBalance: 1000, Credits: "10", CreditsOngoingUsage: "0", CreditsOngoing: "10"},
				{Balance: 2000, OngoingUsage: 2000, OngoingBalance: 0, Credits: "20", CreditsOngoingUsage: "20", CreditsOngoing: "0"},
				{Balance: 3000, OngoingUsage: 3000, OngoingBalance: 0, Credits: "30", CreditsOngoingUsage: "30", CreditsOngoing: "0"},
				{Balance: 4000, OngoingUsage: 5000, OngoingBalance: -1000, Credits: "40", CreditsOngoingUsage: "50", CreditsOngoing: "-10"},
			},
		},
		{
			name:      "wallets without limitations and a progressive billing threshold",
			source:    "spec/scenarios/wallets/balance_spec.rb:891",
			threshold: true,
			want: [4]walBalances{
				{Balance: 0, OngoingUsage: 0, OngoingBalance: 0, Credits: "0", CreditsOngoingUsage: "0", CreditsOngoing: "0"},
				{Balance: 2000, OngoingUsage: 2000, OngoingBalance: 0, Credits: "20", CreditsOngoingUsage: "20", CreditsOngoing: "0"},
				{Balance: 3000, OngoingUsage: 3000, OngoingBalance: 0, Credits: "30", CreditsOngoingUsage: "30", CreditsOngoing: "0"},
				{Balance: 4000, OngoingUsage: 4000, OngoingBalance: 0, Credits: "40", CreditsOngoingUsage: "40", CreditsOngoing: "0"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2022, 12, 1), 10)
			p := s.Plan(period.Monthly, 100)
			var ms []*catalog.BillableMetric
			for _, code := range []string{"storage", "seats", "api", "sms"} {
				m := s.Metric(code, catalog.AggregationTypeSum, "total")
				s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
				ms = append(ms, m)
			}
			if tc.threshold {
				s.UsageThreshold(p, 10_00, false)
			}
			c := s.Customer()
			var ws []*wallet.Wallet
			for i, l := range tc.limits {
				in := wallets.CreateInput{
					Name: new(fmt.Sprintf("W%d", i+1)), GrantedCredits: walDec(fmt.Sprint(10 * (i + 1))),
					Priority: new(i + 1), AllowedFeeTypes: l.feeTypes,
				}
				for _, mi := range l.metrics {
					in.BillableMetricIDs = append(in.BillableMetricIDs, ms[mi].ID)
				}
				ws = append(ws, s.walCreate(c, in))
			}

			s.travelTo(billingtest.Date(2022, 12, 2))
			sub := s.walSubscribe(c, p)

			s.travelTo(billingtest.Date(2022, 12, 6))
			for i, m := range ms {
				total := 10 * (i + 1)
				if tc.filtered {
					s.ingest(sub, m.Code, "total", fmt.Sprint(total-3), "filter", "filter_01")
					s.ingest(sub, m.Code, "total", "3", "filter", "filter_02")
				} else {
					s.ingest(sub, m.Code, "total", fmt.Sprint(total))
				}
			}
			s.walRefresh(c)
			for i, w := range ws {
				s.walExpect(w, tc.want[i])
			}
		})
	}
}

// walTwoMetricsWant are the balances when each wallet pays two metrics:
// each metric fills its highest-priority applicable wallet, then spills to
// the next; the last applicable wallet absorbs the overflow.
var walTwoMetricsWant = [4]walBalances{
	{Balance: 1000, OngoingUsage: 1000, OngoingBalance: 0, Credits: "10", CreditsOngoingUsage: "10", CreditsOngoing: "0"},
	{Balance: 2000, OngoingUsage: 3000, OngoingBalance: -1000, Credits: "20", CreditsOngoingUsage: "30", CreditsOngoing: "-10"},
	{Balance: 3000, OngoingUsage: 4000, OngoingBalance: -1000, Credits: "30", CreditsOngoingUsage: "40", CreditsOngoing: "-10"},
	{Balance: 4000, OngoingUsage: 2000, OngoingBalance: 2000, Credits: "40", CreditsOngoingUsage: "20", CreditsOngoing: "20"},
}

// TestWalletRefreshCustomerWallets refreshes the ongoing balances of
// three wallets, two limited to one metric each and a catch-all, over the
// current usage of a long-running subscription.
func TestWalletRefreshCustomerWallets(t *testing.T) {
	cases := []struct {
		name   string
		source string
		setup  func(s *scenario, c *customer.Customer, sub *subscription.Subscription, ch2 *catalog.Charge, p *catalog.Plan, bm3 *catalog.BillableMetric)
		want   [3]walBalances
	}{
		{
			name:   "multiple wallets with restrictions",
			source: "spec/scenarios/wallets/customer_wallets_balance_refresh_spec.rb:90, :107",
			want:   walRefreshWant(500, 5, 500, 5, 2000, 20),
		},
		{
			name:   "pay in advance charges are already billed",
			source: "spec/scenarios/wallets/customer_wallets_balance_refresh_spec.rb:135",
			setup: func(s *scenario, _ *customer.Customer, _ *subscription.Subscription, _ *catalog.Charge, p *catalog.Plan, bm3 *catalog.BillableMetric) {
				s.Charge(p, bm3, catalog.ChargeModelStandard, `{"amount":"9999"}`, walInAdvance, func(c *catalog.Charge) { c.Invoiceable = false })
			},
			want: walRefreshWant(500, 5, 500, 5, 2000, 20),
		},
		{
			name:   "progressive billing invoice",
			source: "spec/scenarios/wallets/customer_wallets_balance_refresh_spec.rb:201",
			setup: func(s *scenario, c *customer.Customer, sub *subscription.Subscription, ch2 *catalog.Charge, p *catalog.Plan, bm3 *catalog.BillableMetric) {
				ch3 := s.Charge(p, bm3, catalog.ChargeModelStandard, `{"amount":"33"}`)
				inv := s.StoredInvoice(c, func(i *invoice.Invoice) {
					i.Type, i.Status = invoice.TypeProgressiveBilling, invoice.StatusFinalized
					i.FeesAmount, i.TotalAmount = 210, 210
				})
				s.LinkSubscription(inv, sub, invoice.InvoicingReasonProgressiveBilling, s.Now().AddDate(0, 0, -7), s.Now().AddDate(0, 0, 7))
				for _, ch := range []*catalog.Charge{ch2, ch3} {
					s.StoredFee(inv, 100, billingtest.ChargeFee(sub, ch.ID), walFeeCouponsTaxes(10, 10))
				}
			},
			want: walRefreshWant(400, 4, 600, 6, 5200, 52),
		},
		{
			name:   "draft invoice",
			source: "spec/scenarios/wallets/customer_wallets_balance_refresh_spec.rb:244",
			setup: func(s *scenario, c *customer.Customer, sub *subscription.Subscription, ch2 *catalog.Charge, _ *catalog.Plan, _ *catalog.BillableMetric) {
				inv := s.StoredInvoice(c, func(i *invoice.Invoice) {
					i.Status = invoice.StatusDraft
					i.FeesAmount, i.TotalAmount = 110, 110
				})
				s.StoredFee(inv, 100, billingtest.ChargeFee(sub, ch2.ID), walFeeCouponsTaxes(70, 10))
			},
			want: walRefreshWant(540, 5.4, 460, 4.6, 2000, 20),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2024, 3, 15, 12), 0)
			p := s.Plan(period.Monthly, 0)
			c := s.Customer()
			sub := s.Subscription(c, p, billingtest.SubscribedAt(billingtest.Date(2022, 3, 15)))
			bm1 := s.Metric("bm1", catalog.AggregationTypeCount, "")
			bm2 := s.Metric("bm2", catalog.AggregationTypeCount, "")
			bm3 := s.Metric("bm3", catalog.AggregationTypeCount, "")
			s.Charge(p, bm1, catalog.ChargeModelStandard, `{"amount":"10"}`)
			ch2 := s.Charge(p, bm2, catalog.ChargeModelStandard, `{"amount":"5"}`)
			walFunded := func(w *wallet.Wallet) {
				w.OngoingBalanceAmount, w.CreditsOngoingBalance = 0, money.Zero
			}
			w1 := s.Wallet(c, "10", walFunded, func(w *wallet.Wallet) { w.BillableMetricIDs = []core.BillableMetricID{bm1.ID} })
			w2 := s.Wallet(c, "10", walFunded, func(w *wallet.Wallet) { w.BillableMetricIDs = []core.BillableMetricID{bm2.ID} })
			w3 := s.Wallet(c, "10", walFunded, func(w *wallet.Wallet) { w.Name = new("wallet 3") })
			for range 3 {
				s.Event(sub, bm1.Code, s.Now())
			}
			s.Event(sub, bm2.Code, s.Now())
			s.Event(sub, bm3.Code, s.Now())
			if tc.setup != nil {
				tc.setup(s, c, sub, ch2, p, bm3)
			}
			got, err := s.E.Wallets.RefreshOngoingBalances(s.Ctx, s.OrgID(), c.ID)
			s.must(err)
			if len(got) != 3 {
				t.Errorf("refreshed wallets: got = %d, want = 3", len(got))
			}
			for i, w := range []*wallet.Wallet{w1, w2, w3} {
				s.walExpect(w, tc.want[i])
			}
		})
	}
}

// walFeeCouponsTaxes is a fee option setting its coupons and taxes.
func walFeeCouponsTaxes(coupons, taxes int64) func(*invoice.Fee) {
	return func(f *invoice.Fee) {
		f.PreciseCouponsAmount = money.NewFromInt(coupons)
		f.TaxesAmount, f.TaxesPreciseAmount = taxes, money.NewFromInt(taxes)
	}
}

// walRefreshWant are the balances of the refresh scenario: the bm1 wallet
// is always used up by its first $10, the others vary.
func walRefreshWant(usage2 int64, credits2 float64, ongoing2 int64, ongoingCredits2 float64, usage3 int64, credits3 float64) [3]walBalances {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return [3]walBalances{
		{Balance: 1000, OngoingUsage: 1000, CreditsOngoingUsage: "10", OngoingBalance: 0, CreditsOngoing: "0"},
		{Balance: 1000, OngoingUsage: usage2, CreditsOngoingUsage: f(credits2), OngoingBalance: ongoing2, CreditsOngoing: f(ongoingCredits2)},
		{Balance: 1000, OngoingUsage: usage3, CreditsOngoingUsage: f(credits3), OngoingBalance: 1000 - usage3, CreditsOngoing: f(10 - credits3)},
	}
}

// TestWalletTargetRuleTopUp tops a wallet up to its target when usage
// drops the ongoing balance below the threshold.
func TestWalletTargetRuleTopUp(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		grants  bool
		status  wallet.TransactionStatus
		credits string
	}{
		{"granted for free", "spec/scenarios/wallets/target_rule_granting_top_up_spec.rb:25", true, wallet.TransactionStatusGranted, "6"},
		{"paid, raised to the minimum", "spec/scenarios/wallets/target_rule_granting_top_up_spec.rb:82", false, wallet.TransactionStatusPurchased, "50"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2024, 6, 1), 0)
			m := s.Metric("total_metric", catalog.AggregationTypeSum, "total")
			p := s.Plan(period.Monthly, 0)
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			c := s.Customer()
			w := s.walCreate(c, wallets.CreateInput{
				Name: new("Wallet"), GrantedCredits: walDec("10"), PaidTopUpMinAmount: new(int64(50_00)),
				RecurringTransactionRules: []wallets.RuleInput{{
					Trigger: wallet.RecurringTriggerThreshold, Method: wallet.RecurringMethodTarget,
					ThresholdCredits: walDec("5"), TargetOngoingBalance: walDec("10"), GrantsTargetTopUp: new(tc.grants),
				}},
			})
			rules := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*wallet.RecurringTransactionRule, error) {
				return r.RecurringTransactionRules.ListByWallet(ctx, s.OrgID(), w.ID)
			})
			if len(rules) != 1 || rules[0].GrantsTargetTopUp != tc.grants {
				t.Fatalf("rules: got = %+v, want = one rule granting %v", rules, tc.grants)
			}
			sub := s.walSubscribe(c, p)

			s.travelTo(billingtest.Date(2024, 6, 6))
			s.ingest(sub, m.Code, "total", "6")
			s.walRefresh(c)

			var topUps []*wallet.Transaction
			for _, tx := range s.walTransactions(w) {
				if tx.Source == wallet.TransactionSourceThreshold {
					topUps = append(topUps, tx)
				}
			}
			if len(topUps) != 1 {
				t.Fatalf("threshold top-ups: got = %d, want = 1", len(topUps))
			}
			s.walExpectTx(topUps[0], walTx{Type: wallet.TransactionTypeInbound, Status: tc.status, Credits: tc.credits})
			if tc.grants {
				s.walExpect(w, walBalances{Balance: 16_00, OngoingBalance: 10_00, OngoingUsage: 6_00, Credits: "16"})
				if got := len(s.Invoices(c.ID)); got != 0 {
					t.Errorf("invoices: got = %d, want = 0", got)
				}
			}
		})
	}
}

// TestWalletRuleIgnoringLimits buys a threshold top-up below the paid
// top-up minimum when the rule ignores the limits.
//
// source: spec/scenarios/wallets/topup_rule_with_limits_spec.rb:11
func TestWalletRuleIgnoringLimits(t *testing.T) {
	s := walScenario(t, billingtest.Date(2025, 9, 1, 12), 0)
	// The spec stubs a succeeded Stripe payment intent.
	s.PaysCredits = true
	p := s.Plan(period.Monthly, 600, func(p *catalog.Plan) { p.PayInAdvance = true })
	c := s.Customer()
	w := s.walCreate(c, wallets.CreateInput{
		Name: new("Wallet1"), GrantedCredits: walDec("10"), PaidTopUpMinAmount: new(int64(100_00)),
		RecurringTransactionRules: []wallets.RuleInput{{
			Trigger: wallet.RecurringTriggerThreshold, Method: wallet.RecurringMethodFixed,
			ThresholdCredits: walDec("5"), PaidCredits: walDec("10"), IgnorePaidTopUpLimits: new(true),
		}},
	})
	s.travelTo(billingtest.Date(2025, 9, 1, 22))
	sub := s.walSubscribe(c, p)
	s.walSettle()
	invs := s.walSubInvoices(c, sub)
	if len(invs) != 1 {
		t.Fatalf("invoices: got = %d, want = 1", len(invs))
	}
	if got := invs[0].PrepaidCreditAmount; got != 600 {
		t.Errorf("prepaid credit amount: got = %d, want = 600", got)
	}
	s.walExpect(w, walBalances{Balance: 14_00, OngoingBalance: 14_00, Credits: "14"})
}

// TestWalletTransactionPriority sets the priority of top-ups.
func TestWalletTransactionPriority(t *testing.T) {
	cases := []struct {
		name   string
		source string
		create *int
		topUp  *int
		want   int
	}{
		{"wallet with transaction priority", "spec/scenarios/wallets/topup_with_priority_spec.rb:10", new(5), nil, 5},
		{"wallet without transaction priority", "spec/scenarios/wallets/topup_with_priority_spec.rb:26", nil, nil, 50},
		{"wallet transaction with priority", "spec/scenarios/wallets/topup_with_priority_spec.rb:41", nil, new(10), 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2025, 1, 1), 0)
			c := s.Customer()
			in := wallets.CreateInput{TransactionPriority: tc.create}
			if tc.topUp == nil {
				in.PaidCredits, in.GrantedCredits = walDec("100"), walDec("50")
			}
			w := s.walCreate(c, in)
			txs := s.walTransactions(w)
			if tc.topUp != nil {
				txs = s.walTopUp(wallets.TransactionInput{WalletID: w.ID, PaidCredits: walDec("100"), GrantedCredits: walDec("50"), Priority: tc.topUp})
			}
			if len(txs) != 2 {
				t.Fatalf("transactions: got = %d, want = 2", len(txs))
			}
			for _, tx := range txs {
				if tx.Priority != tc.want {
					t.Errorf("priority: got = %d, want = %d", tx.Priority, tc.want)
				}
			}
		})
	}
}

// walCreditInvoices returns the credit invoices of c, oldest first.
func (s *scenario) walCreditInvoices(c *customer.Customer) []*invoice.Invoice {
	var out []*invoice.Invoice
	for _, inv := range s.Invoices(c.ID) {
		if inv.Type == invoice.TypeCredit {
			out = append(out, inv)
		}
	}
	return out
}

// TestWalletTopUpRequiringPayment keeps a purchase requiring a successful
// payment pending on an open credit invoice until the payment outcome.
// The payment itself belongs to libs/payments; its outcome reaches billing
// through HandleCreditInvoicePayment.
func TestWalletTopUpRequiringPayment(t *testing.T) {
	cases := []struct {
		name      string
		source    string
		succeeded bool
	}{
		{"payment succeeds", "spec/scenarios/wallets/topup_with_open_invoices_spec.rb:10", true},
		{"payment fails", "spec/scenarios/wallets/topup_with_open_invoices_spec.rb:69", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2025, 1, 1), 0)
			c := s.Customer()
			w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), GrantedCredits: walDec("10")})
			s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 1000, Credits: "10"})
			txs := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, PaidCredits: walDec("15"), InvoiceRequiresSuccessfulPayment: new(true)})
			wt := txs[0]
			s.walExpectTx(wt, walTx{Status: wallet.TransactionStatusPurchased, Settled: wallet.SettlementStatusPending})
			if !wt.InvoiceRequiresSuccessfulPayment {
				t.Errorf("invoice requires successful payment: got = false, want = true")
			}
			invs := s.walCreditInvoices(c)
			if len(invs) != 1 {
				t.Fatalf("credit invoices: got = %d, want = 1", len(invs))
			}
			inv := invs[0]
			if inv.Status != invoice.StatusOpen || inv.PaymentStatus != core.PaymentStatusPending || !strings.HasSuffix(inv.Number, "-DRAFT") {
				t.Errorf("credit invoice: got = %s/%s/%s, want = open/pending/-DRAFT", inv.Status, inv.PaymentStatus, inv.Number)
			}
			if inv.TotalAmount != 1500 {
				t.Errorf("credit invoice total: got = %d, want = 1500", inv.TotalAmount)
			}

			s.must(s.E.Wallets.HandleCreditInvoicePayment(s.Ctx, s.OrgID(), inv.ID, tc.succeeded))
			s.walSettle()
			inv = s.Invoice(inv.ID)
			wt = s.ReloadWalletTransaction(wt.ID)
			if tc.succeeded {
				if inv.Status != invoice.StatusFinalized || !strings.HasSuffix(inv.Number, "-001-001") {
					t.Errorf("credit invoice: got = %s/%s, want = finalized/-001-001", inv.Status, inv.Number)
				}
				if !s.hasUnhandled(kernel.JobGenerateInvoiceDocument) {
					t.Errorf("document job: got = none, want = %s", kernel.JobGenerateInvoiceDocument)
				}
				if wt.Status != wallet.SettlementStatusSettled || wt.SettledAt == nil {
					t.Errorf("purchase: got = %s settled at %v, want = settled", wt.Status, wt.SettledAt)
				}
				s.walExpect(w, walBalances{Balance: 2500, OngoingBalance: 2500, Credits: "25"})
				return
			}
			if inv.Status != invoice.StatusOpen || !strings.HasSuffix(inv.Number, "-DRAFT") {
				t.Errorf("credit invoice: got = %s/%s, want = open/-DRAFT", inv.Status, inv.Number)
			}
			if wt.Status != wallet.SettlementStatusFailed || wt.SettledAt != nil {
				t.Errorf("purchase: got = %s settled at %v, want = failed, never settled", wt.Status, wt.SettledAt)
			}
			s.walExpect(w, walBalances{Balance: 1000, OngoingBalance: 1000, Credits: "10"})
		})
	}
}

// TestWalletTopUpRounding floors credits to 5 places and rounds amounts
// to cents.
func TestWalletTopUpRounding(t *testing.T) {
	const credits = "17.9699999999999988631316"
	meta := core.Metadata{{Key: "transaction_id", Value: "123"}}

	t.Run("paid credits", func(t *testing.T) {
		// source: spec/scenarios/wallets/topup_with_rounding_spec.rb:16
		s := walScenario(t, billingtest.Date(2025, 1, 1), 0)
		c := s.Customer()
		w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1")})
		if !w.RateAmount.Equal(money.One) {
			t.Errorf("rate: got = %s, want = 1", w.RateAmount)
		}
		// The purchase stays pending until its credit invoice is paid.
		wt := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, PaidCredits: walDec(credits), InvoiceRequiresSuccessfulPayment: new(false), Metadata: meta})[0]
		s.walExpectTx(wt, walTx{Status: wallet.TransactionStatusPurchased, Settled: wallet.SettlementStatusPending, Amount: "17.97", Credits: "17.97"})
		if wt.InvoiceRequiresSuccessfulPayment || !slices.Equal(wt.Metadata, meta) {
			t.Errorf("purchase: got = %v/%v, want = false/%v", wt.InvoiceRequiresSuccessfulPayment, wt.Metadata, meta)
		}
		inv := s.walCreditInvoices(c)[0]
		if inv.Status != invoice.StatusFinalized || inv.PaymentStatus != core.PaymentStatusPending || inv.TotalAmount != 1797 {
			t.Errorf("credit invoice: got = %s/%s/%d, want = finalized/pending/1797", inv.Status, inv.PaymentStatus, inv.TotalAmount)
		}
		s.must(s.E.Wallets.HandleCreditInvoicePayment(s.Ctx, s.OrgID(), inv.ID, true))
		s.walSettle()
		wt = s.ReloadWalletTransaction(wt.ID)
		if wt.Status != wallet.SettlementStatusSettled || wt.SettledAt == nil {
			t.Errorf("purchase: got = %s, want = settled", wt.Status)
		}
		s.walExpect(w, walBalances{Balance: 1797, OngoingBalance: 1797, Credits: "17.97"})
	})

	t.Run("granted credits", func(t *testing.T) {
		// source: spec/scenarios/wallets/topup_with_rounding_spec.rb:62
		s := walScenario(t, billingtest.Date(2025, 1, 1), 0)
		c := s.Customer()
		w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1")})
		wt := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, GrantedCredits: walDec(credits), InvoiceRequiresSuccessfulPayment: new(false)})[0]
		s.walExpectTx(wt, walTx{Status: wallet.TransactionStatusGranted, Settled: wallet.SettlementStatusSettled, Amount: "17.97", Credits: "17.97"})
		s.walSettle()
		s.walExpect(w, walBalances{Balance: 1797, OngoingBalance: 1797, Credits: "17.97"})
	})
}

// TestWalletTopUpName names the purchases; the rendered invoice is
// checked by libs/documents.
func TestWalletTopUpName(t *testing.T) {
	cases := []struct {
		name   string
		source string
		in     wallets.CreateInput
		want   *string
	}{
		{"transaction name provided", "spec/scenarios/wallets/topup_with_name_spec.rb:45", wallets.CreateInput{TransactionName: new("Initial top-up")}, new("Initial top-up")},
		{"wallet name only", "spec/scenarios/wallets/topup_with_name_spec.rb:75", wallets.CreateInput{Name: new("My wallet")}, nil},
		{"no name", "spec/scenarios/wallets/topup_with_name_spec.rb:81", wallets.CreateInput{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			s := walScenario(t, billingtest.Date(2025, 1, 2), 0)
			c := s.Customer()
			in := tc.in
			in.PaidCredits = walDec("100")
			w := s.walCreate(c, in)
			txs := s.walTransactions(w)
			if len(txs) != 1 {
				t.Fatalf("transactions: got = %d, want = 1", len(txs))
			}
			if !walSameName(txs[0].Name, tc.want) {
				t.Errorf("transaction name: got = %v, want = %v", txs[0].Name, tc.want)
			}
			if got := len(s.walCreditInvoices(c)); got != 1 {
				t.Errorf("credit invoices: got = %d, want = 1", got)
			}
			if tc.want == nil {
				return
			}
			s.travelTo(billingtest.Date(2025, 1, 3))
			wt := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, PaidCredits: walDec("200"), Metadata: core.Metadata{{Key: "transaction_id", Value: "123"}}, Name: new("Top-up")})[0]
			s.walExpectTx(wt, walTx{Status: wallet.TransactionStatusPurchased, Settled: wallet.SettlementStatusPending, Amount: "200", Credits: "200"})
			if wt.InvoiceRequiresSuccessfulPayment || !walSameName(wt.Name, new("Top-up")) || len(wt.Metadata) != 1 {
				t.Errorf("top-up: got = %v/%v/%v, want = false/Top-up/1 metadata", wt.InvoiceRequiresSuccessfulPayment, wt.Name, wt.Metadata)
			}
			if got := len(s.walCreditInvoices(c)); got != 2 {
				t.Errorf("credit invoices: got = %d, want = 2", got)
			}
		})
	}
}

// walSameName reports whether two optional names are equal.
func walSameName(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestWalletVoidCredits voids credits, floored to 5 places, with the
// amount rounded to cents.
//
// source: spec/scenarios/wallets/voiding_wallet_credits_spec.rb:14
func TestWalletVoidCredits(t *testing.T) {
	s := walScenario(t, billingtest.Date(2025, 1, 1), 0)
	c := s.Customer()
	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet1"), GrantedCredits: walDec("100")})
	txs := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, VoidedCredits: walDec("14.28444999")})
	if len(txs) != 1 {
		t.Fatalf("transactions: got = %d, want = 1", len(txs))
	}
	s.walExpectTx(txs[0], walTx{Status: wallet.TransactionStatusVoided, Settled: wallet.SettlementStatusSettled, Amount: "14.28", Credits: "14.28444"})
	s.walExpect(w, walBalances{Balance: 8572, Credits: "85.71556", OngoingBalance: 8572})
}

// walTraceSetup is the setup of spec/scenarios/wallets/wallet_traceability_spec.rb:
// a billing entity without grace period, a monthly plan of 0 with a
// standard charge of 1 per unit on a sum metric.
func walTraceSetup(t *testing.T) (*scenario, *catalog.BillableMetric, *catalog.Plan, *customer.Customer) {
	t.Helper()
	s := walScenario(t, billingtest.Date(2022, 12, 1), 0)
	m := s.Metric("total_metric", catalog.AggregationTypeSum, "total")
	p := s.Plan(period.Monthly, 0)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return s, m, p, s.Customer()
}

// walTraceWallet creates a wallet without credits and sets its
// traceability (lago create_traceable_wallet / create_non_traceable_wallet).
func (s *scenario) walTraceWallet(c *customer.Customer, rate string, traceable bool) *wallet.Wallet {
	s.T.Helper()
	w := s.walCreate(c, wallets.CreateInput{Name: new("Wallet"), RateAmount: money.MustParse(rate), GrantedCredits: walDec("0")})
	s.walSetTraceable(w, traceable)
	return s.ReloadWallet(w.ID)
}

// walGrant grants credits to w and returns the inbound transaction.
func (s *scenario) walGrant(w *wallet.Wallet, credits string) *wallet.Transaction {
	s.T.Helper()
	txs := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, GrantedCredits: walDec(credits)})
	return txs[0]
}

// walVoid voids credits of w and returns the voided transaction.
func (s *scenario) walVoid(w *wallet.Wallet, credits string) *wallet.Transaction {
	s.T.Helper()
	txs := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, VoidedCredits: walDec(credits)})
	for _, t := range txs {
		if t.TransactionStatus == wallet.TransactionStatusVoided {
			return t
		}
	}
	s.T.Fatalf("void: got = no voided transaction")
	return nil
}

// walBillMonth ingests units on sub five days after start and bills the
// period at start plus one month.
func (s *scenario) walBillMonth(sub *subscription.Subscription, code string, start time.Time, units string) {
	s.T.Helper()
	s.travelTo(start.AddDate(0, 0, 5))
	s.ingest(sub, code, "total", units)
	s.travelTo(start.AddDate(0, 1, 0))
	s.performBilling()
}

// walPriority sets the priority (and optionally the creation instant) of
// an inbound transaction.
func (s *scenario) walPriority(t *wallet.Transaction, priority int, createdAt *time.Time) {
	s.T.Helper()
	s.walUpdateTx(t, func(t *wallet.Transaction) {
		t.Priority = priority
		if createdAt != nil {
			t.CreatedAt = *createdAt
		}
	})
}

// TestWalletTraceabilityInvoices consumes traceable inbound transactions
// with invoices.
func TestWalletTraceabilityInvoices(t *testing.T) {
	start := billingtest.Date(2022, 12, 1)

	t.Run("simple consumption", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:104
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "100")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "40")
		if got := s.walSubInvoices(c, sub)[0].TotalAmount; got != 0 {
			t.Errorf("invoice total: got = %d, want = 0", got)
		}
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		s.walExpectTx(out[0], walTx{Amount: "40"})
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 4000})
		if got := s.walRemaining(tx1); got != 6000 {
			t.Errorf("remaining: got = %d, want = 6000", got)
		}
	})

	t.Run("consumption spanning multiple inbounds", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:160
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "30")
		s.travelTo(start.Add(time.Hour))
		tx2 := s.walGrant(w, "50")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "60")
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 3000, tx2.ID: 3000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("tx1 remaining: got = %d, want = 0", got)
		}
		if got := s.walRemaining(tx2); got != 2000 {
			t.Errorf("tx2 remaining: got = %d, want = 2000", got)
		}
	})

	t.Run("multiple outbounds from the same inbound", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:221
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "100")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "25")
		s.walBillMonth(sub, m.Code, start.AddDate(0, 1, 0), "35")
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 2 {
			t.Fatalf("invoiced transactions: got = %d, want = 2", len(out))
		}
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 2500})
		s.walExpectFundings(out[1], map[core.WalletTransactionID]int64{tx1.ID: 3500})
		if got := len(s.walConsumptions(tx1)); got != 2 {
			t.Errorf("consumptions: got = %d, want = 2", got)
		}
		if got := s.walRemaining(tx1); got != 4000 {
			t.Errorf("remaining: got = %d, want = 4000", got)
		}
	})

	t.Run("priority then FIFO", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:287
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "20")
		s.walPriority(tx1, 1, nil)
		tx2 := s.walGrant(w, "25")
		s.walPriority(tx2, 2, new(start.AddDate(0, 0, -3)))
		tx3 := s.walGrant(w, "25")
		s.walPriority(tx3, 2, new(start.AddDate(0, 0, -1)))
		tx4 := s.walGrant(w, "30")
		s.walPriority(tx4, 2, nil)
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "80")
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 2000, tx2.ID: 2500, tx3.ID: 2500, tx4.ID: 1000})
		for _, r := range []struct {
			tx   *wallet.Transaction
			want int64
		}{{tx1, 0}, {tx2, 0}, {tx3, 0}, {tx4, 2000}} {
			if got := s.walRemaining(r.tx); got != r.want {
				t.Errorf("remaining of %s: got = %d, want = %d", r.tx.ID, got, r.want)
			}
		}
	})

	t.Run("prepaid credit breakdown", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:374
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "30")
		s.travelTo(start.Add(time.Hour))
		tx2 := s.walTopUp(wallets.TransactionInput{WalletID: w.ID, PaidCredits: walDec("70")})[0]
		credit := s.walCreditInvoices(c)
		if len(credit) != 1 {
			t.Fatalf("credit invoices: got = %d, want = 1", len(credit))
		}
		s.must(s.E.Wallets.HandleCreditInvoicePayment(s.Ctx, s.OrgID(), credit[0].ID, true))
		s.walSettle()
		if got := s.ReloadWalletTransaction(tx2.ID).Status; got != wallet.SettlementStatusSettled {
			t.Fatalf("purchase: got = %s, want = settled", got)
		}
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "80")
		inv := s.walSubInvoices(c, sub)[0]
		if inv.PrepaidGrantedCreditAmount != 3000 || inv.PrepaidPurchasedCreditAmount != 5000 {
			t.Errorf("prepaid breakdown: got = %d/%d, want = 3000/5000", inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount)
		}
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 3000, tx2.ID: 5000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("tx1 remaining: got = %d, want = 0", got)
		}
		if got := s.walRemaining(tx2); got != 2000 {
			t.Errorf("tx2 remaining: got = %d, want = 2000", got)
		}
	})

	t.Run("exact balance consumption", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:578
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "50")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "50")
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		s.walExpectFundings(out[0], map[core.WalletTransactionID]int64{tx1.ID: 5000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("remaining: got = %d, want = 0", got)
		}
	})
}

// TestWalletTraceabilityVoids consumes traceable inbound transactions
// with voids.
func TestWalletTraceabilityVoids(t *testing.T) {
	t.Run("simple voiding", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:445
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "100")
		tx2 := s.walVoid(w, "40")
		s.walExpectFundings(tx2, map[core.WalletTransactionID]int64{tx1.ID: 4000})
		if got := s.walRemaining(tx1); got != 6000 {
			t.Errorf("remaining: got = %d, want = 6000", got)
		}
	})

	t.Run("voiding spanning multiple inbounds", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:476
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "30")
		s.travelTo(s.Now().Add(time.Minute))
		tx2 := s.walGrant(w, "50")
		tx3 := s.walVoid(w, "60")
		s.walExpectFundings(tx3, map[core.WalletTransactionID]int64{tx1.ID: 3000, tx2.ID: 3000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("tx1 remaining: got = %d, want = 0", got)
		}
		if got := s.walRemaining(tx2); got != 2000 {
			t.Errorf("tx2 remaining: got = %d, want = 2000", got)
		}
	})

	t.Run("multiple voids from the same inbound", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:513
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "100")
		tx2 := s.walVoid(w, "25")
		tx3 := s.walVoid(w, "35")
		s.walExpectFundings(tx2, map[core.WalletTransactionID]int64{tx1.ID: 2500})
		s.walExpectFundings(tx3, map[core.WalletTransactionID]int64{tx1.ID: 3500})
		if got := len(s.walConsumptions(tx1)); got != 2 {
			t.Errorf("consumptions: got = %d, want = 2", got)
		}
		if got := s.walRemaining(tx1); got != 4000 {
			t.Errorf("remaining: got = %d, want = 4000", got)
		}
	})

	t.Run("priority ordering", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:541
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "20")
		s.walPriority(tx1, 1, nil)
		tx2 := s.walGrant(w, "25")
		s.walPriority(tx2, 2, new(s.Now().AddDate(0, 0, -2)))
		tx3 := s.walGrant(w, "25")
		s.walPriority(tx3, 2, new(s.Now().AddDate(0, 0, -1)))
		v := s.walVoid(w, "50")
		s.walExpectFundings(v, map[core.WalletTransactionID]int64{tx1.ID: 2000, tx2.ID: 2500, tx3.ID: 500})
		for _, r := range []struct {
			tx   *wallet.Transaction
			want int64
		}{{tx1, 0}, {tx2, 0}, {tx3, 2000}} {
			if got := s.walRemaining(r.tx); got != r.want {
				t.Errorf("remaining of %s: got = %d, want = %d", r.tx.ID, got, r.want)
			}
		}
	})

	t.Run("voiding more than the balance", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:611
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", true)
		tx1 := s.walGrant(w, "30")
		_, err := s.E.Wallets.CreateTransactions(s.Ctx, s.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: walDec("50")})
		if err == nil {
			t.Fatalf("void: got = nil error, want = a validation error")
		}
		if got := len(s.walConsumptions(tx1)); got != 0 {
			t.Errorf("consumptions: got = %d, want = 0", got)
		}
	})

	t.Run("non-integer wallet rate", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:631
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "0.5", true)
		tx1 := s.walGrant(w, "100")
		if got := s.walRemaining(tx1); got != 5000 {
			t.Errorf("remaining: got = %d, want = 5000", got)
		}
		tx2 := s.walVoid(w, "60")
		tx3 := s.walVoid(w, "40")
		s.walExpectFundings(tx2, map[core.WalletTransactionID]int64{tx1.ID: 3000})
		s.walExpectFundings(tx3, map[core.WalletTransactionID]int64{tx1.ID: 2000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("remaining: got = %d, want = 0", got)
		}
	})
}

// TestWalletTraceabilityMultipleWallets consumes several wallets with one
// invoice.
func TestWalletTraceabilityMultipleWallets(t *testing.T) {
	start := billingtest.Date(2022, 12, 1)
	setup := func(t *testing.T) (*scenario, *catalog.BillableMetric, *catalog.BillableMetric, *catalog.Plan, *customer.Customer) {
		s, m, p, c := walTraceSetup(t)
		m2 := s.Metric("total_metric_2", catalog.AggregationTypeSum, "total")
		s.Charge(p, m2, catalog.ChargeModelStandard, `{"amount":"1"}`)
		return s, m, m2, p, c
	}
	limited := func(s *scenario, c *customer.Customer, m *catalog.BillableMetric, credits string, traceable bool) (*wallet.Wallet, *wallet.Transaction) {
		in := wallets.CreateInput{Name: new("Wallet"), BillableMetricIDs: []core.BillableMetricID{m.ID}}
		if traceable {
			in.GrantedCredits = walDec(credits)
		}
		w := s.walCreate(c, in)
		if !traceable {
			s.walSetTraceable(w, false)
			s.walGrant(w, credits)
		}
		var tx *wallet.Transaction
		if in := s.walInbound(w); len(in) > 0 {
			tx = in[0]
		}
		return w, tx
	}
	bill := func(s *scenario, sub *subscription.Subscription, m, m2 *catalog.BillableMetric, units string) {
		s.travelTo(start.AddDate(0, 0, 5))
		s.ingest(sub, m.Code, "total", units)
		s.ingest(sub, m2.Code, "total", units)
		s.travelTo(start.AddDate(0, 1, 0))
		s.performBilling()
	}

	t.Run("invoice spanning multiple wallets", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:708
		s, m, m2, p, c := setup(t)
		w1, tx1 := limited(s, c, m, "30", true)
		s.travelTo(start.Add(time.Hour))
		w2, tx2 := limited(s, c, m2, "50", true)
		sub := s.walSubscribe(c, p)
		bill(s, sub, m, m2, "25")
		inv := s.walSubInvoices(c, sub)[0]
		if inv.PrepaidCreditAmount != 5000 || inv.PrepaidGrantedCreditAmount != 5000 {
			t.Errorf("prepaid: got = %d/%d, want = 5000/5000", inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount)
		}
		tx3 := s.walOutbound(w1, wallet.TransactionStatusInvoiced)[0]
		tx4 := s.walOutbound(w2, wallet.TransactionStatusInvoiced)[0]
		s.walExpectTx(tx3, walTx{Amount: "25"})
		s.walExpectTx(tx4, walTx{Amount: "25"})
		s.walExpectFundings(tx3, map[core.WalletTransactionID]int64{tx1.ID: 2500})
		s.walExpectFundings(tx4, map[core.WalletTransactionID]int64{tx2.ID: 2500})
		if got := s.walRemaining(tx1); got != 500 {
			t.Errorf("tx1 remaining: got = %d, want = 500", got)
		}
		if got := s.walRemaining(tx2); got != 2500 {
			t.Errorf("tx2 remaining: got = %d, want = 2500", got)
		}
	})

	t.Run("mixed traceable and non-traceable wallets", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:784
		s, m, m2, p, c := setup(t)
		w1, _ := limited(s, c, m, "30", true)
		s.travelTo(start.Add(time.Hour))
		w2, _ := limited(s, c, m2, "50", false)
		sub := s.walSubscribe(c, p)
		bill(s, sub, m, m2, "25")
		inv := s.walSubInvoices(c, sub)[0]
		// lago leaves the breakdown nil; core stores it as 0.
		if inv.PrepaidCreditAmount != 5000 || inv.PrepaidGrantedCreditAmount != 0 || inv.PrepaidPurchasedCreditAmount != 0 {
			t.Errorf("prepaid: got = %d/%d/%d, want = 5000/0/0", inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount)
		}
		if got := len(s.walFundings(s.walOutbound(w1, wallet.TransactionStatusInvoiced)[0])); got != 1 {
			t.Errorf("traceable fundings: got = %d, want = 1", got)
		}
		if got := len(s.walFundings(s.walOutbound(w2, wallet.TransactionStatusInvoiced)[0])); got != 0 {
			t.Errorf("non-traceable fundings: got = %d, want = 0", got)
		}
	})

	t.Run("wallets in priority order", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:852
		s, m, p, c := walTraceSetup(t)
		w1 := s.walCreate(c, wallets.CreateInput{Name: new("Priority 1 Wallet"), GrantedCredits: walDec("30"), Priority: new(1)})
		tx1 := s.walInbound(w1)[0]
		s.travelTo(start.Add(time.Hour))
		w2 := s.walCreate(c, wallets.CreateInput{Name: new("Priority 2 Wallet"), GrantedCredits: walDec("50"), Priority: new(2)})
		tx2 := s.walInbound(w2)[0]
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "60")
		inv := s.walSubInvoices(c, sub)[0]
		if inv.PrepaidCreditAmount != 6000 || inv.PrepaidGrantedCreditAmount != 6000 {
			t.Errorf("prepaid: got = %d/%d, want = 6000/6000", inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount)
		}
		tx3 := s.walOutbound(w1, wallet.TransactionStatusInvoiced)[0]
		tx4 := s.walOutbound(w2, wallet.TransactionStatusInvoiced)[0]
		s.walExpectTx(tx3, walTx{Amount: "30"})
		s.walExpectTx(tx4, walTx{Amount: "30"})
		s.walExpectFundings(tx3, map[core.WalletTransactionID]int64{tx1.ID: 3000})
		s.walExpectFundings(tx4, map[core.WalletTransactionID]int64{tx2.ID: 3000})
		if got := s.walRemaining(tx1); got != 0 {
			t.Errorf("tx1 remaining: got = %d, want = 0", got)
		}
		if got := s.walRemaining(tx2); got != 2000 {
			t.Errorf("tx2 remaining: got = %d, want = 2000", got)
		}
	})
}

// TestWalletNonTraceable deducts from wallets that record no
// consumption.
func TestWalletNonTraceable(t *testing.T) {
	start := billingtest.Date(2022, 12, 1)

	t.Run("invoice consumption", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:936
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", false)
		s.walGrant(w, "100")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "40")
		inv := s.walSubInvoices(c, sub)[0]
		if inv.PrepaidCreditAmount != 4000 || inv.PrepaidGrantedCreditAmount != 0 || inv.PrepaidPurchasedCreditAmount != 0 {
			t.Errorf("prepaid: got = %d/%d/%d, want = 4000/0/0", inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount)
		}
		out := s.walOutbound(w, wallet.TransactionStatusInvoiced)
		if len(out) != 1 {
			t.Fatalf("invoiced transactions: got = %d, want = 1", len(out))
		}
		if out[0].Amount != 4000 || len(s.walFundings(out[0])) != 0 {
			t.Errorf("invoiced transaction: got = %d with %d fundings, want = 4000 with none", out[0].Amount, len(s.walFundings(out[0])))
		}
		if got := s.ReloadWallet(w.ID).BalanceAmount; got != 6000 {
			t.Errorf("balance: got = %d, want = 6000", got)
		}
	})

	t.Run("voiding", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:972
		s, _, _, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", false)
		s.walGrant(w, "100")
		v := s.walVoid(w, "40")
		if v.Amount != 4000 || len(s.walFundings(v)) != 0 {
			t.Errorf("voided transaction: got = %d with %d fundings, want = 4000 with none", v.Amount, len(s.walFundings(v)))
		}
		if got := s.ReloadWallet(w.ID).BalanceAmount; got != 6000 {
			t.Errorf("balance: got = %d, want = 6000", got)
		}
	})

	t.Run("multiple billing periods", func(t *testing.T) {
		// source: spec/scenarios/wallets/wallet_traceability_spec.rb:991
		s, m, p, c := walTraceSetup(t)
		w := s.walTraceWallet(c, "1", false)
		tx := s.walGrant(w, "100")
		sub := s.walSubscribe(c, p)
		s.walBillMonth(sub, m.Code, start, "25")
		inv1 := s.walSubInvoices(c, sub)[0]
		if inv1.PrepaidCreditAmount != 2500 || inv1.PrepaidGrantedCreditAmount != 0 {
			t.Errorf("first invoice prepaid: got = %d/%d, want = 2500/0", inv1.PrepaidCreditAmount, inv1.PrepaidGrantedCreditAmount)
		}
		s.walBillMonth(sub, m.Code, start.AddDate(0, 1, 0), "35")
		invs := s.walSubInvoices(c, sub)
		inv2 := invs[len(invs)-1]
		if inv2.PrepaidCreditAmount != 3500 || inv2.PrepaidGrantedCreditAmount != 0 {
			t.Errorf("second invoice prepaid: got = %d/%d, want = 3500/0", inv2.PrepaidCreditAmount, inv2.PrepaidGrantedCreditAmount)
		}
		if got := s.ReloadWallet(w.ID).BalanceAmount; got != 4000 {
			t.Errorf("balance: got = %d, want = 4000", got)
		}
		if got := len(s.walConsumptions(tx)); got != 0 {
			t.Errorf("consumptions: got = %d, want = 0", got)
		}
	})
}
