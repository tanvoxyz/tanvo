package billing_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// twAccepts makes a charge accept a target wallet.
func twAccepts(c *catalog.Charge) { c.AcceptsTargetWallet = true }

// twSetup is the common setup of spec/scenarios/events_targeting_wallets_spec.rb:
// a free monthly plan and a sum metric on "value".
func twSetup(t *testing.T) (*scenario, *catalog.BillableMetric, *catalog.Plan, *customer.Customer) {
	t.Helper()
	s := walScenario(t, billingtest.Date(2023, 1, 15), 0)
	m := s.Metric("tw_metric", catalog.AggregationTypeSum, "value")
	p := s.Plan(period.Monthly, 0)
	return s, m, p, s.Customer()
}

// twWallet creates the wallet code of c with credits granted, at
// priority (0 for the default).
func (s *scenario) twWallet(c *customer.Customer, code, credits string, priority int) *wallet.Wallet {
	s.T.Helper()
	in := wallets.CreateInput{Name: new(code), Code: new(code), GrantedCredits: walDec(credits)}
	if priority > 0 {
		in.Priority = new(priority)
	}
	return s.walCreate(c, in)
}

// twChargeFees returns the charge fees of the first invoice of c.
func (s *scenario) twChargeFees(c *customer.Customer) (*invoice.Invoice, []*invoice.Fee) {
	s.T.Helper()
	invs := s.Invoices(c.ID)
	if len(invs) == 0 {
		s.T.Fatalf("invoices: got = 0, want ≥ 1")
	}
	inv := invs[0]
	var out []*invoice.Fee
	for _, f := range s.Fees(inv.ID) {
		if f.Type == core.FeeTypeCharge {
			out = append(out, f)
		}
	}
	return inv, out
}

// twFee returns the fee whose group matches want (a missing key matches
// a fee without it).
func twFee(t *testing.T, fees []*invoice.Fee, want map[string]string) *invoice.Fee {
	t.Helper()
	for _, f := range fees {
		if len(f.GroupedBy) != len(want) {
			continue
		}
		match := true
		for k, v := range want {
			if f.GroupedBy[k] != v {
				match = false
			}
		}
		if match {
			return f
		}
	}
	t.Fatalf("fee grouped by %v: got = none, want = 1", want)
	return nil
}

// twWantFee checks the units and amount of a fee.
func twWantFee(t *testing.T, f *invoice.Fee, units string, amount int64) {
	t.Helper()
	if !rootDecEq(f.Units, units) {
		t.Errorf("fee %v units: got = %s, want = %s", f.GroupedBy, f.Units, units)
	}
	if f.Amount != amount {
		t.Errorf("fee %v amount: got = %d, want = %d", f.GroupedBy, f.Amount, amount)
	}
}

// twInvoiceTxs checks the transactions of wallet w on invoice inv.
func (s *scenario) twInvoiceTxs(w *wallet.Wallet, inv *invoice.Invoice, amounts ...int64) {
	s.T.Helper()
	var got []int64
	for _, t := range s.walTransactions(w) {
		if t.InvoiceID == inv.ID {
			got = append(got, t.Amount)
		}
	}
	if len(got) != len(amounts) {
		s.T.Fatalf("wallet %s transactions on invoice: got = %v, want = %v", walName(w), got, amounts)
	}
	for i := range got {
		if got[i] != amounts[i] {
			s.T.Errorf("wallet %s transaction %d: got = %d, want = %d", walName(w), i, got[i], amounts[i])
		}
	}
}

// twBalance checks the balance and ongoing usage balance of w; a
// negative want skips the check.
func (s *scenario) twBalance(w *wallet.Wallet, balance, ongoingUsage int64) {
	s.T.Helper()
	got := s.ReloadWallet(w.ID)
	if balance >= 0 && got.BalanceAmount != balance {
		s.T.Errorf("wallet %s balance: got = %d, want = %d", walName(got), got.BalanceAmount, balance)
	}
	if ongoingUsage >= 0 && got.OngoingUsageBalanceAmount != ongoingUsage {
		s.T.Errorf("wallet %s ongoing usage balance: got = %d, want = %d", walName(got), got.OngoingUsageBalanceAmount, ongoingUsage)
	}
}

// TestTargetingWalletsInArrears groups the fees by target wallet and pays
// each group from its wallet.
//
// source: spec/scenarios/events_targeting_wallets_spec.rb:31
func TestTargetingWalletsInArrears(t *testing.T) {
	s, m, p, c := twSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, twAccepts)
	w1 := s.twWallet(c, "wallet_1", "200", 0)
	w2 := s.twWallet(c, "wallet_2", "250", 0)
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2023, 1, 16))
	s.ingest(sub, m.Code, "value", "10", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "5", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "20", catalog.EventTargetWalletCode, "wallet_2")
	s.walRefresh(c)
	s.twBalance(w1, -1, 15_000) // events_targeting_wallets_spec.rb:75
	s.twBalance(w2, -1, 20_000)

	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	inv, fees := s.twChargeFees(c)
	if len(fees) != 2 {
		t.Fatalf("charge fees: got = %d, want = 2", len(fees))
	}
	twWantFee(t, twFee(t, fees, map[string]string{catalog.EventTargetWalletCode: "wallet_1"}), "15", 15_000)
	twWantFee(t, twFee(t, fees, map[string]string{catalog.EventTargetWalletCode: "wallet_2"}), "20", 20_000)
	inv = s.Invoice(inv.ID)
	if inv.PrepaidCreditAmount != 35_000 {
		t.Errorf("prepaid credit amount: got = %d, want = 35000", inv.PrepaidCreditAmount)
	}
	s.twBalance(w1, 5_000, -1)
	s.twBalance(w2, 5_000, -1)
	s.twInvoiceTxs(w1, inv, 15_000)
	s.twInvoiceTxs(w2, inv, 20_000)
}

// TestTargetingWalletsWithPricingGroupKeys groups the fees by the pricing
// group keys and the target wallet together.
//
// source: spec/scenarios/events_targeting_wallets_spec.rb:135
func TestTargetingWalletsWithPricingGroupKeys(t *testing.T) {
	s, m, p, c := twSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"5","pricing_group_keys":["region"]}`, twAccepts)
	w1 := s.twWallet(c, "wallet_1", "200", 0)
	w2 := s.twWallet(c, "wallet_2", "250", 0)
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2023, 1, 16))
	s.ingest(sub, m.Code, "value", "10", "region", "eu", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "15", "region", "us", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "20", "region", "eu", catalog.EventTargetWalletCode, "wallet_2")
	s.walRefresh(c)
	s.twBalance(w1, -1, 12_500) // events_targeting_wallets_spec.rb:181
	s.twBalance(w2, -1, 10_000)

	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	inv, fees := s.twChargeFees(c)
	if len(fees) != 3 {
		t.Fatalf("charge fees: got = %d, want = 3", len(fees))
	}
	key := func(region, code string) map[string]string {
		return map[string]string{"region": region, catalog.EventTargetWalletCode: code}
	}
	twWantFee(t, twFee(t, fees, key("eu", "wallet_1")), "10", 5_000)
	twWantFee(t, twFee(t, fees, key("us", "wallet_1")), "15", 7_500)
	twWantFee(t, twFee(t, fees, key("eu", "wallet_2")), "20", 10_000)
	s.twBalance(w1, 7_500, -1)
	s.twBalance(w2, 15_000, -1)
	s.twInvoiceTxs(w1, inv, 12_500)
	s.twInvoiceTxs(w2, inv, 10_000)
}

// TestTargetingWalletsInAdvance bills each in-advance event in its target
// wallet's group and pays its invoice from that wallet.
//
// source: spec/scenarios/events_targeting_wallets_spec.rb:254
func TestTargetingWalletsInAdvance(t *testing.T) {
	s, m, p, c := twSetup(t)
	ch := s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, twAccepts, walInAdvance)
	w1 := s.twWallet(c, "wallet_1", "150", 0)
	w2 := s.twWallet(c, "wallet_2", "100", 0)
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2023, 1, 16))
	s.ingest(sub, m.Code, "value", "10", catalog.EventTargetWalletCode, "wallet_1")
	fees := s.rootChargeFees(ch.ID)
	if len(fees) != 1 {
		t.Fatalf("fees: got = %d, want = 1", len(fees))
	}
	f1 := fees[0]
	if !f1.PayInAdvance || f1.GroupedBy[catalog.EventTargetWalletCode] != "wallet_1" {
		t.Errorf("fee 1: got = pay_in_advance %t grouped by %v, want = true wallet_1", f1.PayInAdvance, f1.GroupedBy)
	}
	twWantFee(t, f1, "10", 10_000)
	s.twBalance(w1, 5_000, -1) // events_targeting_wallets_spec.rb:288
	s.twBalance(w2, 10_000, -1)

	s.travelTo(billingtest.Date(2023, 1, 17))
	s.ingest(sub, m.Code, "value", "5", catalog.EventTargetWalletCode, "wallet_2")
	fees = s.rootChargeFees(ch.ID)
	if len(fees) != 2 {
		t.Fatalf("fees: got = %d, want = 2", len(fees))
	}
	f2 := fees[0]
	if f2.ID == f1.ID {
		f2 = fees[1]
	}
	if !f2.PayInAdvance || f2.GroupedBy[catalog.EventTargetWalletCode] != "wallet_2" {
		t.Errorf("fee 2: got = pay_in_advance %t grouped by %v, want = true wallet_2", f2.PayInAdvance, f2.GroupedBy)
	}
	twWantFee(t, f2, "5", 5_000)
	s.twBalance(w1, 5_000, -1) // events_targeting_wallets_spec.rb:310
	s.twBalance(w2, 5_000, -1)
}

// TestTargetingWalletsWithoutCode pays the fee of the events naming no
// wallet from the wallets that pay untargeted usage.
//
// source: spec/scenarios/events_targeting_wallets_spec.rb:339
func TestTargetingWalletsWithoutCode(t *testing.T) {
	s, m, p, c := twSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`, twAccepts)
	w1 := s.twWallet(c, "wallet_1", "150", 0)
	def := s.twWallet(c, "default_wallet", "100", 1)
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2023, 1, 16))
	s.ingest(sub, m.Code, "value", "10", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "5")
	s.walRefresh(c)
	s.twBalance(w1, -1, 10_000) // events_targeting_wallets_spec.rb:376
	s.twBalance(def, -1, 5_000)

	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	inv, fees := s.twChargeFees(c)
	if len(fees) != 2 {
		t.Fatalf("charge fees: got = %d, want = 2", len(fees))
	}
	twWantFee(t, twFee(t, fees, map[string]string{catalog.EventTargetWalletCode: "wallet_1"}), "10", 10_000)
	var untargeted *invoice.Fee
	for _, f := range fees {
		if f.GroupedBy[catalog.EventTargetWalletCode] == "" {
			untargeted = f
		}
	}
	if untargeted == nil {
		t.Fatalf("untargeted fee: got = none, want = 1")
	}
	twWantFee(t, untargeted, "5", 5_000)
	s.twBalance(w1, 5_000, -1)
	s.twBalance(def, 5_000, -1)
	s.twInvoiceTxs(w1, inv, 10_000)
	s.twInvoiceTxs(def, inv, 5_000)
}

// TestTargetingWalletsChargeNotAccepting ignores the target wallet codes
// of the events of a charge that does not accept one: one fee, paid by
// the wallets in priority order. Lago gates the feature per
// organization; Tanvo has the charge flag only.
//
// source: spec/scenarios/events_targeting_wallets_spec.rb:441
func TestTargetingWalletsChargeNotAccepting(t *testing.T) {
	s, m, p, c := twSetup(t)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
	w1 := s.twWallet(c, "wallet_1", "200", 1)
	w2 := s.twWallet(c, "wallet_2", "250", 2)
	sub := s.walSubscribe(c, p)

	s.travelTo(billingtest.Date(2023, 1, 16))
	s.ingest(sub, m.Code, "value", "10", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "5", catalog.EventTargetWalletCode, "wallet_1")
	s.ingest(sub, m.Code, "value", "20", catalog.EventTargetWalletCode, "wallet_2")
	s.walRefresh(c)
	s.twBalance(w1, -1, 20_000) // events_targeting_wallets_spec.rb:485
	s.twBalance(w2, -1, 15_000)

	s.travelTo(billingtest.Date(2023, 2, 1))
	s.performBilling()
	inv, fees := s.twChargeFees(c)
	if len(fees) != 1 {
		t.Fatalf("charge fees: got = %d, want = 1", len(fees))
	}
	if len(fees[0].GroupedBy) != 0 {
		t.Errorf("grouped by: got = %v, want = empty", fees[0].GroupedBy)
	}
	twWantFee(t, fees[0], "35", 35_000)
	s.twBalance(w1, 0, -1)
	s.twBalance(w2, 10_000, -1)
	s.twInvoiceTxs(w1, inv, 20_000)
	s.twInvoiceTxs(w2, inv, 15_000)
}
