package documents

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// Ports of spec/views/app/views/templates/invoices/v4/charge.slim_spec.rb:
// pay-in-advance charge invoices.

// chargeSpec is one pay-in-advance charge fee of the spec.
type chargeSpec struct {
	name      string
	amount    int64
	units     string
	unitCents int64
	precise   string
	charge    func(c *catalog.Charge)
	fee       func(f *invoice.Fee)
	metric    func(m *catalog.BillableMetric)
}

// chargeInvoice builds the spec's invoice (LAGO-202509-CH-001 unless inv
// changes it) with the fees of specs.
func chargeInvoice(inv func(inv *invoice.Invoice), specs ...chargeSpec) func(t *testing.T, g *ids) *Invoice {
	return func(t *testing.T, g *ids) *Invoice {
		plan := newPlan(g, "monthly", func(p *catalog.Plan) { p.InvoiceDisplayName = ptr("Monthly Plan") })
		sub := newSubscription(g, plan, periods(span("2025-09-01 00:00:00", "2025-09-30 23:59:59")))
		in := newInvoice(g, invoice.TypeSubscription, "LAGO-202509-CH-001", func(i *invoice.Invoice) {
			i.IssuingDate, i.PaymentDueDate = day("2025-09-01"), day("2025-09-15")
			i.FeesAmount, i.SubTotalExcludingTaxesAmount, i.SubTotalIncludingTaxesAmount, i.TotalAmount = 10000, 10000, 10000, 10000
			if inv != nil {
				inv(i)
			}
		})
		out := &Invoice{Party: staticParty(), Invoice: in, Subscriptions: []Subscription{sub}}
		for _, s := range specs {
			c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) {
				c.PayInAdvance = true
				if s.charge != nil {
					s.charge(c)
				}
			})
			if s.metric != nil {
				s.metric(&m)
			}
			precise := s.precise
			if precise == "" {
				precise = "0"
			}
			f := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) {
				f.PayInAdvance, f.InvoiceDisplayName = true, ptr(s.name)
				f.Amount, f.UnitAmount, f.PreciseUnitAmount = s.amount, s.unitCents, d(precise)
				if s.units != "" {
					f.Units = d(s.units)
				}
				if s.fee != nil {
					s.fee(f)
				}
			})
			out.Fees = append(out.Fees, f)
		}
		return out
	}
}

func percentageCharge(c *catalog.Charge) {
	c.ChargeModel, c.Properties = catalog.ChargeModelPercentage, catalog.MustProperties(`{"rate":"0.0555","fixed_amount":"2"}`)
}

func percentageDetails(t *testing.T, rate, perUnitTotal string, detailed bool) func(f *invoice.Fee) {
	return func(f *invoice.Fee) {
		m := map[string]any{"paid_units": "100", "rate": rate, "per_unit_total_amount": perUnitTotal}
		if detailed {
			f.EventsCount = 50
			m["fixed_fee_unit_amount"], m["fixed_fee_total_amount"] = "0.20", "20.00"
			m["min_max_adjustment_total_amount"], m["per_transaction_min_amount"], m["per_transaction_max_amount"] = "0.00", "0.00", "0.00"
		}
		f.AmountDetails = detailsJSON(t, m)
	}
}

func TestInvoiceV4Charge(t *testing.T) {
	money20 := d("20.0")
	apiCalls := chargeSpec{name: "API Calls", amount: 5000, units: "10", unitCents: 500, precise: "5"}
	percentage := func(t *testing.T, name string, amount int64, rate, total string, detailed bool) chargeSpec {
		s := chargeSpec{name: name, amount: amount, units: "100", charge: percentageCharge, fee: percentageDetails(t, rate, total, detailed)}
		if !detailed {
			s.unitCents, s.precise = 55, "0.555"
		}
		return s
	}
	runInvoiceCases(t, []invoiceCase{
		{name: "single standard charge", snapshot: "v4_charge/with_a_single_standard_charge_fee", build: chargeInvoice(nil, apiCalls)},
		{
			name: "multiple charges", snapshot: "v4_charge/with_multiple_charge_fees",
			build: chargeInvoice(nil, apiCalls, chargeSpec{name: "Storage GB", amount: 3000, units: "5", unitCents: 600, precise: "6"}),
		},
		{
			name: "percentage with basic rate", snapshot: "v4_charge/with_percentage_charge_with_basic_rate",
			build: chargeInvoice(nil, percentage(t, "Transaction Fee", 5550, "5.55", "55.50", false)),
		},
		{
			name: "percentage with basic rate, floating point artifact", snapshot: "v4_charge/with_percentage_charge_with_basic_rate__when_the_rate_contains_a_floating_point_artifact",
			build: chargeInvoice(nil, percentage(t, "Transaction Fee", 90, "0.8999999999999999", "0.90", false)),
		},
		{
			name: "percentage with detailed breakdown", snapshot: "v4_charge/with_percentage_charge_with_detailed_breakdown",
			build: chargeInvoice(nil, percentage(t, "Payment Processing Fee", 7550, "5.55", "55.50", true)),
		},
		{
			name: "percentage with detailed breakdown, floating point artifact", snapshot: "v4_charge/with_percentage_charge_with_detailed_breakdown__when_the_rate_contains_a_floating_point_artifact",
			build: chargeInvoice(nil, percentage(t, "Payment Processing Fee", 2070, "0.7000000000000001", "0.70", true)),
		},
		{
			name: "prorated charge", snapshot: "v4_charge/with_prorated_charge",
			build: func(t *testing.T, g *ids) *Invoice {
				d := chargeInvoice(nil, chargeSpec{
					name: "Prorated Seats", amount: 2500, units: "5", unitCents: 500, precise: "5",
					charge: func(c *catalog.Charge) { c.Prorated = true },
					metric: func(m *catalog.BillableMetric) {
						m.AggregationType, m.Recurring = catalog.AggregationTypeUniqueCount, true
					},
					fee: func(f *invoice.Fee) {
						p := span("2025-09-15 00:00:00", "2025-09-30 23:59:59")
						f.Properties.From, f.Properties.To, f.Properties.ChargesFrom, f.Properties.ChargesTo = p.From, p.To, p.From, p.To
					},
				})(t, g)
				// The event of 2025-09-15 bills 16 of September's 30 days.
				d.Fees[0].ProrationDays = &DayRange{Days: 16, Total: 30}
				return d
			},
		},
		{
			name: "non-invoiceable charge", snapshot: "v4_charge/with_non_invoiceable_charge",
			build: chargeInvoice(nil, chargeSpec{
				name: "One-time Setup", amount: 1000, units: "1", unitCents: 1000, precise: "10",
				charge: func(c *catalog.Charge) { c.Invoiceable = false },
				fee:    func(f *invoice.Fee) { f.SucceededAt = ptr(at("2025-09-05 10:30:00")) },
			}),
		},
		{
			name: "charge filter", snapshot: "v4_charge/with_charge_filter",
			build: func(t *testing.T, g *ids) *Invoice {
				d := chargeInvoice(nil, chargeSpec{name: "Filtered Charge", amount: 2000, units: "4", unitCents: 500, precise: "5"})(t, g)
				f := &d.Fees[0]
				filter := catalog.ChargeFilter{ID: g.filter(), ChargeID: f.Charge.ID, Properties: catalog.MustProperties(`{"amount":"100"}`)}
				f.ChargeFilter, f.Fee.ChargeFilterID = &filter, filter.ID
				return d
			},
		},
		{
			name: "coupon", snapshot: "v4_charge/with_coupon_applied",
			build: func(t *testing.T, g *ids) *Invoice {
				d := chargeInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount, inv.CouponsAmount = "LAGO-202509-CH-002", 4000, 5000, 1000
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 4000, 4000
				}, chargeSpec{name: "Charge with Coupon", amount: 5000, units: "10", unitCents: 500, precise: "5"})(t, g)
				// The coupon factory: a 2.00 EUR fixed amount coupon.
				d.Coupons = []CouponCredit{{CouponName: "20% Discount", FixedAmount: 200, Currency: eur(), Amount: 1000}}
				return d
			},
		},
		{
			name: "taxes", snapshot: "v4_charge/with_taxes_applied",
			build: func(t *testing.T, g *ids) *Invoice {
				d := chargeInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount, inv.TaxesAmount = "LAGO-202509-CH-003", 6000, 5000, 1000
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 5000, 6000
				}, chargeSpec{
					name: "Taxable Charge", amount: 5000, units: "10", unitCents: 500, precise: "5",
					fee: func(f *invoice.Fee) { f.TaxesAmount = 1000 },
				})(t, g)
				d.AppliedTaxes = []invoice.AppliedTax{{
					TaxID: g.tax(), TaxName: "VAT", TaxCode: "vat", TaxRate: money20, Amount: 1000, FeesAmount: 5000, Currency: usd(),
				}}
				return d
			},
		},
		{
			name: "presentation breakdowns", snapshot: "v4_charge/with_presentation_breakdowns",
			build: func(t *testing.T, g *ids) *Invoice {
				inv := chargeInvoice(nil, chargeSpec{
					name: "API Calls", amount: 10000, units: "100", unitCents: 100, precise: "1",
					charge: func(c *catalog.Charge) {
						c.InvoiceDisplayName = ptr("API Calls")
						c.Properties = catalog.MustProperties(`{"amount":"100","presentation_group_keys":[` +
							`{"value":"region","options":{"display_in_invoice":true}},{"value":"env","options":{"display_in_invoice":true}}]}`)
					},
				})(t, g)
				inv.Fees[0].PresentationBreakdowns = []PresentationBreakdown{
					{PresentationBy: map[string]string{"region": "us", "env": "prod"}, Units: d("60")},
					{PresentationBy: map[string]string{"region": "eu", "env": "prod"}, Units: d("40")},
				}
				return inv
			},
		},
		{
			name: "prepaid credits", snapshot: "v4_charge/with_prepaid_credits",
			build: func(t *testing.T, g *ids) *Invoice {
				d := chargeInvoice(func(inv *invoice.Invoice) {
					inv.Number, inv.TotalAmount, inv.FeesAmount = "LAGO-202509-CH-004", 4000, 5000
					inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 5000, 5000
					inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 1000, 400, 600
				}, apiCalls)(t, g)
				d.HasWalletTransactions = true
				return d
			},
		},
	})
}

// source: spec/views/app/views/templates/invoices/v4/charge.slim_spec.rb:95
func TestInvoiceV4ChargePurchaseOrderNumber(t *testing.T) {
	d := chargeInvoice(nil, chargeSpec{name: "API Calls", amount: 5000, units: "10", unitCents: 500, precise: "5"})(t, &ids{})
	d.PurchaseOrderNumber = "PO-123"
	w := newWriter(testCatalog(t), &d.Party, "", d.Invoice.Currency)
	got := strings.Join(w.invoiceLayout(d).texts(), "\n")
	for _, want := range []string{"Purchase Order Number", "PO-123"} {
		if !strings.Contains(got, want) {
			t.Errorf("texts: got = no %q, want = %q", want, want)
		}
	}
}
