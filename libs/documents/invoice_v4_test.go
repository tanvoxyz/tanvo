package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Ports of spec/views/app/views/templates/invoices/v4.slim_spec.rb: each
// case builds the spec's invoice and checks the texts of its HTML snapshot.

type invoiceCase struct {
	name string
	// snapshot is the ported snapshot under testdata/lago.
	snapshot string
	build    func(t *testing.T, g *ids) *Invoice
	// skip lists snapshot texts printed differently on purpose.
	skip []string
}

func runInvoiceCases(t *testing.T, cases []invoiceCase) {
	t.Helper()
	cat := testCatalog(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.build(t, &ids{})
			w := newWriter(cat, &d.Party, "", d.Invoice.Currency)
			checkSnapshot(t, w.invoiceLayout(d), c.snapshot, c.skip...)
		})
	}
}

// prepaidCreditsInvoice is "when invoice_type is subscription with prepaid
// credits" (v4.slim_spec.rb:128).
func prepaidCreditsInvoice(granted, purchased int64) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		plan := newPlan(g, "basic_plan", func(p *catalog.Plan) {
			p.Name, p.InvoiceDisplayName, p.Amount = "Basic Plan", ptr("Basic Plan"), 5000
		})
		sub := newSubscription(g, plan, periods(span("2025-09-01 00:00:00", "2025-09-30 23:59:59")))
		inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-002", func(inv *invoice.Invoice) {
			inv.IssuingDate, inv.PaymentDueDate = day("2025-09-04"), day("2025-09-04")
			inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 5000, 5000, 5000
			inv.TotalAmount, inv.PrepaidCreditAmount = 4500, 500
			inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = granted, purchased
		})
		fee := newFee(g, core.FeeTypeSubscription, func(f *invoice.Fee) {
			f.SubscriptionID, f.Amount, f.Units = sub.Subscription.ID, 5000, d("1")
			f.InvoiceDisplayName = ptr("Basic Plan - Monthly")
		})
		return &Invoice{
			Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub},
			Fees: []Fee{{Fee: fee}}, HasWalletTransactions: true,
		}
	}
}

func TestInvoiceV4PrepaidCredits(t *testing.T) {
	runInvoiceCases(t, []invoiceCase{
		{
			name:     "only granted credits",
			snapshot: "v4/when_invoice_type_is_subscription_with_prepaid_credits__with_only_granted_credits",
			build:    prepaidCreditsInvoice(500, 0),
		},
		{
			name:     "only purchased credits",
			snapshot: "v4/when_invoice_type_is_subscription_with_prepaid_credits__with_only_purchased_credits",
			build:    prepaidCreditsInvoice(0, 500),
		},
		{
			name:     "granted and purchased credits",
			snapshot: "v4/when_invoice_type_is_subscription_with_prepaid_credits__with_both_granted_and_purchased_credits",
			build:    prepaidCreditsInvoice(300, 200),
		},
		{
			name:     "without breakdown",
			snapshot: "v4/when_invoice_type_is_subscription_with_prepaid_credits__without_breakdown__legacy_behavior_",
			build:    prepaidCreditsInvoice(0, 0),
		},
	})
}

// fixedChargesInvoice is "when invoice_type is subscription and plan is
// paid in arrears" (v4.slim_spec.rb:226) and "… paid in advance"
// (v4.slim_spec.rb:770): the same fixed charge fees, with the subscription
// fee in the arrears or the advance period.
func fixedChargesInvoice(advance bool) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		aug := span("2025-08-01 00:00:00", "2025-08-31 23:59:59")
		sep := span("2025-09-01 00:00:00", "2025-09-30 23:59:59")
		name, due, subName := "Pay in Arrears Premium Plan", "2025-10-01", "Pay in Arrears Subscription Fee"
		issuing, subPeriod := "2025-09-01", aug
		if advance {
			name, due, subName, issuing, subPeriod = "Premium Plan", "2025-09-19", "Pay in Advance Subscription Fee", "2025-09-04", sep
		}
		plan := newPlan(g, "premium", func(p *catalog.Plan) { p.InvoiceDisplayName, p.PayInAdvance = ptr(name), advance })
		sub := newSubscription(g, plan, func(is *invoice.Subscription) {
			is.Period, is.ChargesPeriod, is.FixedChargesPeriod, is.Timestamp = subPeriod, aug, aug, aug.To
		})
		inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-002", func(inv *invoice.Invoice) {
			inv.IssuingDate, inv.PaymentDueDate = day(issuing), day(due)
			inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 5000, 5000, 5000, 5000
		})
		subFee := newFee(g, core.FeeTypeSubscription, func(f *invoice.Fee) {
			f.SubscriptionID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount = sub.Subscription.ID, 1500, d("1"), 1500, d("15")
			f.InvoiceDisplayName, f.PayInAdvance = ptr(subName), advance
			f.Properties.From, f.Properties.To = subPeriod.From, subPeriod.To
		})
		commitmentFee := newFee(g, core.FeeTypeCommitment, func(f *invoice.Fee) {
			f.SubscriptionID, f.CommitmentID, f.Amount, f.InvoiceDisplayName = sub.Subscription.ID, g.commitment(), 200, ptr("Minimum Commitment Fee")
			f.Properties.From, f.Properties.To = aug.From, aug.To
		})
		fees := []Fee{{Fee: subFee}, {Fee: commitmentFee}}
		for _, s := range []fixedSpec{
			{name: "Standard Pay in Advance Fixed Charge Fee", advance: true, amount: 5000, units: "2", unitCents: 2500, precise: "25", period: sep},
			{name: "Standard Pay in Advance Prorated Fixed Charge Fee", advance: true, prorated: true, amount: 5000, units: "0.5", unitCents: 10000, precise: "100", period: sep},
			{name: "Graduated Pay in Advance Fixed Charge Fee", model: catalog.FixedChargeModelGraduated, advance: true, amount: 55500, units: "15", unitCents: 3700, precise: "37", details: graduatedFixedDetails(), period: sep},
			{name: "Zero Pay in Advance Fixed Charge Fee", advance: true, units: "1", period: sep},
			{name: "Standard Pay in Arrears Fixed Charge Fee", amount: 8500, units: "1", unitCents: 8500, precise: "85", period: aug},
			{name: "Graduated Pay in Arrears Fixed Charge Fee", model: catalog.FixedChargeModelGraduated, amount: 55500, units: "15", unitCents: 3700, precise: "37", details: graduatedFixedDetails(), period: aug},
			{name: "Graduated Pay in Arrears Prorated Fixed Charge Fee", model: catalog.FixedChargeModelGraduated, prorated: true, amount: 55500, units: "15", unitCents: 3700, precise: "37", details: graduatedFixedDetails(), period: aug},
			{name: "Volume Pay in Arrears Fixed Charge Fee", model: catalog.FixedChargeModelVolume, amount: 15100, units: "75", unitCents: 201, precise: "2.01", details: map[string]any{"per_unit_amount": "2.0", "per_unit_total_amount": "150.0", "flat_unit_amount": "1.0"}, period: aug},
			{name: "Volume Pay in Arrears Prorated Fixed Charge Fee", model: catalog.FixedChargeModelVolume, prorated: true, amount: 9200, units: "30", unitCents: 307, precise: "3.07", details: map[string]any{"per_unit_amount": "3.0", "per_unit_total_amount": "90.0", "flat_unit_amount": "2.0"}, period: aug},
		} {
			fees = append(fees, fixedFee(t, g, &sub, s))
		}
		return &Invoice{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: fees}
	}
}

// fixedChargeBoundariesInvoice is "when invoice has different boundaries
// for fixed charges" (v4.slim_spec.rb:672).
func fixedChargeBoundariesInvoice(t *testing.T, g *ids) *Invoice {
	plan := newPlan(g, "annual", func(p *catalog.Plan) {
		p.Interval, p.InvoiceDisplayName, p.Currency, p.BillFixedChargesMonthly = period.Yearly, ptr("Annual Plan"), eur(), ptr(true)
	})
	sub := newSubscription(g, plan, func(is *invoice.Subscription) {
		is.Period = span("2025-08-01 00:00:00", "2025-12-31 23:59:59")
		is.ChargesPeriod = is.Period
		is.FixedChargesPeriod = span("2025-12-01 00:00:00", "2025-12-31 23:59:59")
		is.Timestamp = is.Period.To
	})
	inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-003", func(inv *invoice.Invoice) {
		inv.Currency = eur()
		inv.IssuingDate, inv.PaymentDueDate = day("2026-01-01"), day("2026-01-01")
		inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 5000, 5000, 5000, 5000
	})
	subFee := newFee(g, core.FeeTypeSubscription, func(f *invoice.Fee) {
		f.SubscriptionID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount = sub.Subscription.ID, 99900, d("1"), 99900, d("999")
		f.Currency = eur()
		f.Properties.From, f.Properties.To = sub.InvoiceSubscription.Period.From, sub.InvoiceSubscription.Period.To
	})
	fixed := fixedFee(t, g, &sub, fixedSpec{name: "Monthly Fixed Charge", amount: 10000, units: "1", unitCents: 10000, precise: "100", cur: eur()})
	return &Invoice{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: []Fee{{Fee: subFee}, fixed}}
}

func TestInvoiceV4FixedCharges(t *testing.T) {
	runInvoiceCases(t, []invoiceCase{
		{name: "plan paid in arrears", snapshot: "v4/when_invoice_type_is_subscription_and_plan_is_paid_in_arrears", build: fixedChargesInvoice(false)},
		{name: "plan paid in advance", snapshot: "v4/when_invoice_type_is_subscription_and_plan_is_paid_in_advance", build: fixedChargesInvoice(true)},
		{name: "different boundaries for fixed charges", snapshot: "v4/when_invoice_has_different_boundaries_for_fixed_charges", build: fixedChargeBoundariesInvoice},
	})
}

// progressiveBillingInvoice is "when invoice_type is progressive_billing
// with prepaid credits" (v4.slim_spec.rb:1222).
func progressiveBillingInvoice(threshold bool) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		plan := newPlan(g, "progressive", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Progressive Billing Plan") })
		sub := newSubscription(g, plan, func(is *invoice.Subscription) {
			is.Period = span("2025-09-01 00:00:00", "2025-09-30 23:59:59")
			is.ChargesPeriod = span("2025-09-01 00:00:00", "2025-09-15 23:59:59")
			is.FixedChargesPeriod, is.Timestamp = is.ChargesPeriod, at("2025-09-15 12:00:00")
		})
		inv := newInvoice(g, invoice.TypeProgressiveBilling, "LAGO-202509-008", func(inv *invoice.Invoice) {
			inv.IssuingDate, inv.PaymentDueDate = day("2025-09-15"), day("2025-09-15")
			inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 10000, 10000, 10000, 9500
			inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 500, 200, 300
		})
		c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) { c.InvoiceDisplayName = ptr("Usage Charge") })
		fee := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
			f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount = 10000, d("100"), 100, d("1")
			f.InvoiceDisplayName = ptr("Usage Charge Fee")
		})
		out := &Invoice{
			Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: []Fee{fee},
			HasWalletTransactions: true,
		}
		if threshold {
			out.UsageThreshold = &AppliedUsageThreshold{LifetimeUsageAmount: 10000, PassedThresholdAmount: 10000}
		}
		return out
	}
}

// trueUpInvoice is "when charge has filters and minimum commitment
// (true_up fee)" (v4.slim_spec.rb:1347).
func trueUpInvoice(t *testing.T, g *ids) *Invoice {
	plan := newPlan(g, "filters", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Plan with Charge Filters") })
	aug := span("2025-08-01 00:00:00", "2025-08-31 23:59:59")
	sub := newSubscription(g, plan, periods(aug))
	inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-004", func(inv *invoice.Invoice) {
		inv.IssuingDate, inv.PaymentDueDate = day("2025-09-01"), day("2025-10-01")
		inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 10000, 10000, 10000, 10000
	})
	c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) {
		c.MinAmount, c.InvoiceDisplayName = 10000, ptr("Usage Charge with Minimum")
	})
	us := catalog.ChargeFilter{ID: g.filter(), ChargeID: c.ID, Values: map[string][]string{"region": {"us"}}, Properties: catalog.MustProperties(`{"amount":"10"}`)}
	eu := catalog.ChargeFilter{ID: g.filter(), ChargeID: c.ID, Values: map[string][]string{"region": {"eu"}}, Properties: catalog.MustProperties(`{"amount":"20"}`)}
	base := chargeFee(g, &sub, &c, &m, nil)
	f1 := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
		f.ChargeFilterID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount, f.TotalAggregatedUnits = us.ID, 3000, d("3"), 1000, d("10"), d("3")
	})
	f1.ChargeFilter = &us
	f2 := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
		f.ChargeFilterID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount, f.TotalAggregatedUnits = eu.ID, 4000, d("2"), 2000, d("20"), d("2")
	})
	f2.ChargeFilter = &eu
	trueUp := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
		f.TrueUpParentFeeID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount, f.TotalAggregatedUnits = base.Fee.ID, 3000, d("1"), 3000, d("30"), d("1")
	})
	base.TrueUpFee = &trueUp.Fee
	return &Invoice{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: []Fee{base, f1, f2, trueUp}}
}

func TestInvoiceV4Charges(t *testing.T) {
	runInvoiceCases(t, []invoiceCase{
		{name: "progressive billing", snapshot: "v4/when_invoice_type_is_progressive_billing_with_prepaid_credits", build: progressiveBillingInvoice(true)},
		{name: "charge filters and true-up", snapshot: "v4/when_charge_has_filters_and_minimum_commitment__true_up_fee_", build: trueUpInvoice},
	})
}

// source: spec/views/app/views/templates/invoices/v4.slim_spec.rb:1336
func TestInvoiceV4ProgressiveBillingWithoutThreshold(t *testing.T) {
	d := progressiveBillingInvoice(false)(t, &ids{})
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	for _, s := range w.invoiceLayout(d).texts() {
		if strings.Contains(s, "This progressive billing is generated because your cumulative usage has reached") {
			t.Fatalf("texts: got = %q, want = no reached usage threshold line", s)
		}
	}
}

// creditInvoice is "when invoice_type is credit" (v4.slim_spec.rb:76): a
// wallet top-up of 10.50 credits.
func creditInvoice(walletName, transactionName *string) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		inv := newInvoice(g, invoice.TypeCredit, "LAGO-202509-001", func(inv *invoice.Invoice) {
			inv.IssuingDate, inv.PaymentDueDate, inv.TotalAmount = day("2025-09-04"), day("2025-09-04"), 1050
		})
		w := wallet.Wallet{Name: walletName, Currency: usd(), RateAmount: d("1.0")}
		wt := wallet.Transaction{CreditAmount: d("10.50"), Name: transactionName}
		fee := newFee(g, core.FeeTypeCredit, func(f *invoice.Fee) { f.Amount = 1050 })
		return &Invoice{Party: staticParty(), Invoice: inv, Fees: []Fee{{Fee: fee, Wallet: &w, WalletTransaction: &wt}}}
	}
}

// subscriptionsInvoice is "when invoice has multiple subscriptions"
// (v4.slim_spec.rb:1522) and "… with prepaid credits" (v4.slim_spec.rb:1729):
// the plans are listed by name, not by creation.
func subscriptionsInvoice(prepaid bool) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		aug := span("2025-08-01 00:00:00", "2025-08-31 23:59:59")
		type plan struct {
			name   string
			amount int64
		}
		plans := []plan{{"Zebra", 5000}, {"Alpha", 3000}}
		number := "LAGO-202509-005"
		if prepaid {
			plans, number = []plan{{"Alpha", 5000}, {"Beta", 3000}}, "LAGO-202509-006"
		}
		inv := newInvoice(g, invoice.TypeSubscription, number, func(inv *invoice.Invoice) {
			inv.IssuingDate, inv.PaymentDueDate = day("2025-09-01"), day("2025-10-01")
			inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 8000, 8000, 8000, 8000
			if prepaid {
				inv.TotalAmount = 7000
				inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 1000, 400, 600
			}
		})
		out := &Invoice{Party: staticParty(), Invoice: inv, HasWalletTransactions: prepaid}
		for _, p := range plans {
			pl := newPlan(g, p.name, func(c *catalog.Plan) { c.InvoiceDisplayName = ptr(p.name + " Plan") })
			sub := newSubscription(g, pl, periods(aug))
			fee := newFee(g, core.FeeTypeSubscription, func(f *invoice.Fee) {
				f.SubscriptionID, f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount = sub.Subscription.ID, p.amount, d("1"), p.amount, d("1")
				f.InvoiceDisplayName = ptr(p.name + " Plan Subscription")
				f.Properties.From, f.Properties.To = aug.From, aug.To
			})
			out.Subscriptions = append(out.Subscriptions, sub)
			out.Fees = append(out.Fees, Fee{Fee: fee})
		}
		return out
	}
}

// presentationInvoice is "when charge fees have presentation breakdowns"
// (v4.slim_spec.rb:1642).
func presentationInvoice(t *testing.T, g *ids) *Invoice {
	plan := newPlan(g, "breakdowns", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Plan with Breakdowns") })
	sub := newSubscription(g, plan, periods(span("2025-08-01 00:00:00", "2025-08-31 23:59:59")))
	inv := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-009", func(inv *invoice.Invoice) {
		inv.IssuingDate, inv.PaymentDueDate = day("2025-09-01"), day("2025-10-01")
		inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = 10000, 10000, 10000, 10000
	})
	c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) {
		c.InvoiceDisplayName = ptr("Compute")
		c.Properties = catalog.MustProperties(`{"amount":"100","presentation_group_keys":[` +
			`{"value":"region","options":{"display_in_invoice":true}},{"value":"env","options":{"display_in_invoice":true}}]}`)
	})
	fee := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
		f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount, f.InvoiceDisplayName = 10000, d("100"), 100, d("1"), ptr("Compute")
	})
	fee.Fee.PresentationBreakdowns = []invoice.PresentationBreakdown{
		{PresentationBy: map[string]*string{"region": ptr("us"), "env": ptr("prod")}, Units: d("60")},
		{PresentationBy: map[string]*string{"region": ptr("eu"), "env": ptr("prod")}, Units: d("40")},
	}
	return &Invoice{Party: staticParty(), Invoice: inv, Subscriptions: []Subscription{sub}, Fees: []Fee{fee}}
}

func TestInvoiceV4Others(t *testing.T) {
	runInvoiceCases(t, []invoiceCase{
		{name: "credit, named transaction", snapshot: "v4/when_invoice_type_is_credit__when_wallet_transaction_has_a_name", build: creditInvoice(ptr("Premium Wallet"), ptr("Wallet Transaction Name"))},
		{name: "credit, named wallet", snapshot: "v4/when_invoice_type_is_credit__when_wallet_transaction_has_no_name__when_wallet_has_a_name", build: creditInvoice(ptr("Premium Wallet"), nil)},
		{name: "credit, no names", snapshot: "v4/when_invoice_type_is_credit__when_wallet_transaction_has_no_name__when_wallet_has_no_name", build: creditInvoice(nil, nil)},
		{
			name: "multiple subscriptions", snapshot: "v4/when_invoice_has_multiple_subscriptions", build: subscriptionsInvoice(false),
		},
		{
			name: "multiple subscriptions with prepaid credits", snapshot: "v4/when_invoice_has_multiple_subscriptions_with_prepaid_credits", build: subscriptionsInvoice(true),
		},
		{name: "presentation breakdowns", snapshot: "v4/when_charge_fees_have_presentation_breakdowns", build: presentationInvoice},
	})
}

// rowTexts returns the cells' texts of the first table row whose first cell
// starts with name.
func rowTexts(l *layout, name string) []string {
	for _, b := range l.Blocks {
		for _, r := range b.Rows {
			if len(r.Cells) == 0 || len(r.Cells[0].Text) == 0 || r.Cells[0].Text[0] != name {
				continue
			}
			var out []string
			for _, c := range r.Cells {
				out = append(out, strings.Join(c.Text, " "))
			}
			return out
		}
	}
	return nil
}

// source: spec/views/app/views/templates/invoices/v4.slim_spec.rb:629
func TestInvoiceV4FixedChargeWithZeroUnits(t *testing.T) {
	d := fixedChargesInvoice(false)(t, &ids{})
	for i := range d.Fees {
		if f := &d.Fees[i].Fee; present(f.InvoiceDisplayName) == "Standard Pay in Arrears Fixed Charge Fee" {
			f.Units, f.UnitAmount, f.PreciseUnitAmount = d0(), 0, d0()
		}
	}
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	got := strings.Join(rowTexts(w.invoiceLayout(d), "Standard Pay in Arrears Fixed Charge Fee"), "|")
	if want := "Standard Pay in Arrears Fixed Charge Fee|0|$0.00|0.0%|$85.00"; got != want {
		t.Errorf("fee row: got = %q, want = %q", got, want)
	}
}

// source: spec/views/app/views/templates/invoices/v4.slim_spec.rb:77
func TestInvoiceV4PurchaseOrderNumber(t *testing.T) {
	d := creditInvoice(nil, nil)(t, &ids{})
	d.Invoice.PurchaseOrderNumber = ptr("PO-123")
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	got := strings.Join(w.invoiceLayout(d).texts(), "\n")
	for _, want := range []string{"Purchase Order Number", "PO-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("texts: got = no %q, want = %q", want, want)
		}
	}
}
