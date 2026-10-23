package einvoice

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func englishSettlement(code string) settlement {
	return settlement{labels: EnglishLabels(), currency: cur(code), createdAt: instant("2025-10-22T10:00:00Z")}
}

func TestCIIComponents(t *testing.T) {
	be := newBillingEntity("BR")
	be.TaxID = ptr("BR987654321")
	c := newCustomer()
	c.Name = ptr("Its Mii")
	legal := newCustomer()
	legal.LegalName = ptr("Mii Industries SARL")
	agreement := func(p parties, taxReg bool, po string) func(t *testing.T) []byte {
		return fragment(func(n *node) { ciiTradeAgreement(n, p, taxReg, po) })
	}
	tradeTax := func(category string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			ciiTradeTax(n, taxSubtotal{category: category, rate: dec("20.00"), basis: dec("10"), tax: dec("2.0")})
		})
	}
	allowanceCharge := func(charge bool, rate string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			ciiAllowanceCharge(n, englishSettlement("EUR"), charge, allowance{rate: dec(rate), amount: dec("10.00")})
		})
	}
	means := func(m int, s settlement) func(t *testing.T) []byte {
		return fragment(func(n *node) { ciiPaymentMeans(n, s, m, dec("10.00")) })
	}
	card := englishSettlement("USD")
	card.cardDigits = "**** 1234"
	usd := cur("USD")
	monetary := func(prepaid *money.Decimal, code string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			a := ciiAmounts{
				lineTotal: dec("1000.00"), charges: dec("10.00"), allowances: dec("10.00"), taxBasis: dec("990.00"),
				tax: dec("198.84"), grandTotal: dec("1188.84"), prepaid: prepaid, duePayable: dec("1188.84"),
			}
			ciiMonetarySummation(n, cur(code), a)
		})
	}
	prepaid := dec("21.86")
	lineItem := func(rate string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			ciiLineItem(n, ciiLine{
				lineID: 1, name: "Premium", description: "Premium plan", chargeAmount: plain(dec("0.059")),
				billedQuantity: plain(money.Zero), category: categoryStandard, ratePercent: rate,
				lineTotal: units(200, cur("EUR")),
			})
		})
	}

	runXMLCases(t, []xmlCase{
		{
			name:   "cross industry invoice context",
			source: "spec/serializers/e_invoices/cii/cross_industry_invoice_spec.rb:16",
			doc:    func(*testing.T) []byte { return ciiDocument().document() },
			checks: []check{
				comment("Exchange Document Context"),
				val("//rsm:ExchangedDocumentContext/ram:GuidelineSpecifiedDocumentContextParameter/ram:ID", "urn:cen.eu:en16931:2017"),
				attrVal("/rsm:CrossIndustryInvoice", "xmlns:rsm", ciiNSRSM),
			},
		},
		{
			name:   "header of an invoice",
			source: "spec/serializers/e_invoices/cii/header_spec.rb:29",
			doc: fragment(func(n *node) {
				ciiHeader(n, "INV-1", typeCommercialInvoice, date("2025-03-16"), []string{"Invoice ID: x", "Allow multiple notes"})
			}),
			checks: []check{
				comment("Exchange Document Header"),
				val("//rsm:ExchangedDocument/ram:ID", "INV-1"),
				val("//rsm:ExchangedDocument/ram:TypeCode", "380"),
				val("//rsm:ExchangedDocument/ram:IssueDateTime/udt:DateTimeString", "20250316"),
				attrVal("//rsm:ExchangedDocument/ram:IssueDateTime/udt:DateTimeString", "format", "102"),
				val("//rsm:ExchangedDocument/ram:IncludedNote[1]/ram:Content", "Invoice ID: x"),
				val("//rsm:ExchangedDocument/ram:IncludedNote[2]/ram:Content", "Allow multiple notes"),
			},
		},
		{
			name:   "header of a credit note",
			source: "spec/serializers/e_invoices/cii/header_spec.rb:57",
			doc:    fragment(func(n *node) { ciiHeader(n, "CN-1", typeCreditNote, date("2025-03-17"), nil) }),
			checks: []check{
				val("//rsm:ExchangedDocument/ram:ID", "CN-1"),
				val("//rsm:ExchangedDocument/ram:TypeCode", "381"),
				val("//rsm:ExchangedDocument/ram:IssueDateTime/udt:DateTimeString", "20250317"),
			},
		},
		{
			name:   "header of a payment receipt",
			source: "spec/serializers/e_invoices/cii/header_spec.rb:78",
			doc:    fragment(func(n *node) { ciiHeader(n, "PR-1", typePaymentReceipt, date("2025-10-22"), nil) }),
			checks: []check{
				val("//rsm:ExchangedDocument/ram:ID", "PR-1"),
				val("//rsm:ExchangedDocument/ram:TypeCode", "202"),
				val("//rsm:ExchangedDocument/ram:IssueDateTime/udt:DateTimeString", "20251022"),
			},
		},
		{
			name:   "applicable trade tax",
			source: "spec/serializers/e_invoices/cii/applicable_trade_tax_spec.rb:22",
			doc:    tradeTax(categoryStandard),
			checks: []check{
				comment("Tax Information 20.00% VAT"),
				val("//ram:ApplicableTradeTax/ram:CalculatedAmount", "2.00"),
				val("//ram:ApplicableTradeTax/ram:TypeCode", "VAT"),
				val("//ram:ApplicableTradeTax/ram:BasisAmount", "10.00"),
				val("//ram:ApplicableTradeTax/ram:CategoryCode", "S"),
				val("//ram:ApplicableTradeTax/ram:RateApplicablePercent", "20.00"),
			},
		},
		{
			name:   "applicable trade tax out of scope",
			source: "spec/serializers/e_invoices/cii/applicable_trade_tax_spec.rb:46",
			doc:    tradeTax(categoryOutOfScope),
			checks: []check{
				val("//ram:ApplicableTradeTax/ram:ExemptionReasonCode", "VATEX-EU-O"),
				none("//ram:ApplicableTradeTax/ram:RateApplicablePercent"),
			},
		},
		{
			name:   "invoice reference",
			source: "spec/serializers/e_invoices/cii/invoice_reference_spec.rb:20",
			doc:    fragment(func(n *node) { ciiInvoiceReference(n, "TES-ABCD-202510-001") }),
			checks: []check{
				comment("Invoice reference"),
				val("//ram:InvoiceReferencedDocument/ram:IssuerAssignedID", "TES-ABCD-202510-001"),
			},
		},
		{
			name:   "line item",
			source: "spec/serializers/e_invoices/cii/line_item_spec.rb:36",
			doc:    lineItem(plain(dec("20.00"))),
			checks: []check{
				comment("Line Item 1: Premium plan"),
				val("//ram:IncludedSupplyChainTradeLineItem/ram:AssociatedDocumentLineDocument/ram:LineID", "1"),
				val("//ram:IncludedSupplyChainTradeLineItem/ram:SpecifiedTradeProduct/ram:Name", "Premium"),
				val("//ram:IncludedSupplyChainTradeLineItem/ram:SpecifiedTradeProduct/ram:Description", "Premium plan"),
				val("//ram:IncludedSupplyChainTradeLineItem/ram:SpecifiedLineTradeAgreement/ram:NetPriceProductTradePrice/ram:ChargeAmount", "0.059"),
				val("//ram:SpecifiedLineTradeDelivery/ram:BilledQuantity", "0.0"),
				attrVal("//ram:SpecifiedLineTradeDelivery/ram:BilledQuantity", "unitCode", "C62"),
				val("//ram:SpecifiedLineTradeSettlement/ram:ApplicableTradeTax/ram:CategoryCode", "S"),
				val("//ram:SpecifiedLineTradeSettlement/ram:ApplicableTradeTax/ram:RateApplicablePercent", "20.0"),
				val("//ram:SpecifiedTradeSettlementLineMonetarySummation/ram:LineTotalAmount", "2.00"),
			},
		},
		{
			name:   "line item without rate",
			source: "spec/serializers/e_invoices/cii/line_item_spec.rb:88",
			doc:    lineItem(""),
			checks: []check{none("//ram:SpecifiedLineTradeSettlement/ram:ApplicableTradeTax/ram:RateApplicablePercent")},
		},
		{
			name:   "monetary summation",
			source: "spec/serializers/e_invoices/cii/monetary_summation_spec.rb:32",
			doc:    monetary(&prepaid, "USD"),
			checks: []check{
				comment("Monetary Summation"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:LineTotalAmount", "1000.00"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:ChargeTotalAmount", "10.00"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:AllowanceTotalAmount", "10.00"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TaxBasisTotalAmount", "990.00"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TaxTotalAmount", "198.84"),
				attrVal("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TaxTotalAmount", "currencyID", "USD"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:GrandTotalAmount", "1188.84"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TotalPrepaidAmount", "21.86"),
				val("//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:DuePayableAmount", "1188.84"),
			},
		},
		{
			name:   "monetary summation of a credit note",
			source: "spec/serializers/e_invoices/cii/monetary_summation_spec.rb:74",
			doc:    monetary(&prepaid, "EUR"),
			checks: []check{attrVal("//ram:TaxTotalAmount", "currencyID", "EUR")},
		},
		{
			name:   "monetary summation without prepaid amount",
			source: "spec/serializers/e_invoices/cii/monetary_summation_spec.rb:84",
			doc: fragment(func(n *node) {
				ciiMonetarySummation(n, usd, ciiAmounts{lineTotal: dec("1000"), taxBasis: dec("990"), tax: dec("198.84"), grandTotal: dec("1188.84"), duePayable: dec("1188.84")})
			}),
			checks: []check{
				val("//ram:ChargeTotalAmount", "0.00"),
				val("//ram:AllowanceTotalAmount", "0.00"),
				none("//ram:TotalPrepaidAmount"),
			},
		},
		{
			name:   "payment terms",
			source: "spec/serializers/e_invoices/cii/payment_terms_spec.rb:21",
			doc: fragment(func(n *node) {
				ciiPaymentTerms(n, date("2025-03-16"), "This is just a description, I can write anything")
			}),
			checks: []check{
				comment("Payment Terms"),
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", "This is just a description, I can write anything"),
				val("//ram:SpecifiedTradePaymentTerms/ram:DueDateDateTime/udt:DateTimeString", "20250316"),
				attrVal("//ram:SpecifiedTradePaymentTerms/ram:DueDateDateTime/udt:DateTimeString", "format", "102"),
			},
		},
		{
			name:   "trade agreement seller and buyer",
			source: "spec/serializers/e_invoices/cii/trade_agreement_spec.rb:41",
			doc:    agreement(parties{be, c}, true, ""),
			checks: []check{
				comment("Applicable Header Trade Agreement"),
				val("//ram:ApplicableHeaderTradeAgreement/ram:SellerTradeParty/ram:ID", "BE_CODE"),
				val("//ram:SellerTradeParty/ram:Name", "BE Legal Name"),
				val("//ram:SellerTradeParty/ram:PostalTradeAddress/ram:PostcodeCode", "60192460"),
				val("//ram:SellerTradeParty/ram:PostalTradeAddress/ram:LineOne", "Rue quelque part"),
				val("//ram:SellerTradeParty/ram:PostalTradeAddress/ram:LineTwo", "Tourne au deuxième angle"),
				val("//ram:SellerTradeParty/ram:PostalTradeAddress/ram:CityName", "Eine Stadt"),
				val("//ram:SellerTradeParty/ram:PostalTradeAddress/ram:CountryID", "BR"),
				val("//ram:SellerTradeParty/ram:SpecifiedTaxRegistration/ram:ID", "BR987654321"),
				attrVal("//ram:SellerTradeParty/ram:SpecifiedTaxRegistration/ram:ID", "schemeID", "VA"),
				val("//ram:BuyerTradeParty/ram:Name", "Its Mii"),
				val("//ram:BuyerTradeParty/ram:PostalTradeAddress/ram:PostcodeCode", "123654789"),
				val("//ram:BuyerTradeParty/ram:PostalTradeAddress/ram:LineOne", "Streets of Tomorrow"),
				val("//ram:BuyerTradeParty/ram:PostalTradeAddress/ram:LineTwo", "on AC"),
				val("//ram:BuyerTradeParty/ram:PostalTradeAddress/ram:CityName", "SP"),
				val("//ram:BuyerTradeParty/ram:PostalTradeAddress/ram:CountryID", "BR"),
				none("//ram:ApplicableHeaderTradeAgreement/ram:BuyerOrderReferencedDocument"),
			},
		},
		{
			name:   "trade agreement without tax registration",
			source: "spec/serializers/e_invoices/cii/trade_agreement_spec.rb:84",
			doc:    agreement(parties{be, c}, false, ""),
			checks: []check{none("//ram:SellerTradeParty/ram:SpecifiedTaxRegistration/ram:ID")},
		},
		{
			name:   "trade agreement buyer legal name",
			source: "spec/serializers/e_invoices/cii/trade_agreement_spec.rb:105",
			doc:    agreement(parties{be, legal}, true, ""),
			checks: []check{val("//ram:BuyerTradeParty/ram:Name", "Mii Industries SARL")},
		},
		{
			name:   "trade agreement purchase order",
			source: "spec/serializers/e_invoices/cii/trade_agreement_spec.rb:132",
			doc:    agreement(parties{be, c}, true, "PO-12345"),
			checks: []check{val("//ram:ApplicableHeaderTradeAgreement/ram:BuyerOrderReferencedDocument/ram:IssuerAssignedID", "PO-12345")},
		},
		{
			name:   "allowance as discount",
			source: "spec/serializers/e_invoices/cii/trade_allowance_charge_spec.rb:23",
			doc:    allowanceCharge(indicatorDiscount, "19.00"),
			checks: []check{
				comment("Allowance/Charge - Discount 19.00% portion"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:ChargeIndicator/udt:Indicator", "false"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:ActualAmount", "10.00"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:Reason", "Discount 19.00% portion"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:CategoryTradeTax/ram:TypeCode", "VAT"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:CategoryTradeTax/ram:CategoryCode", "S"),
				val("//ram:SpecifiedTradeAllowanceCharge/ram:CategoryTradeTax/ram:RateApplicablePercent", "19.00"),
			},
		},
		{
			// The spec expects false through a matcher that ignores false
			// values; the indicator of a charge is true.
			name:   "allowance as charge",
			source: "spec/serializers/e_invoices/cii/trade_allowance_charge_spec.rb:34",
			doc:    allowanceCharge(indicatorCharge, "19.00"),
			checks: []check{val("//ram:ChargeIndicator/udt:Indicator", "true")},
		},
		{
			name:   "allowance at zero rate",
			source: "spec/serializers/e_invoices/cii/trade_allowance_charge_spec.rb:66",
			doc:    allowanceCharge(indicatorDiscount, "0.00"),
			checks: []check{val("//ram:CategoryTradeTax/ram:CategoryCode", "Z")},
		},
		{
			name:   "trade delivery",
			source: "spec/serializers/e_invoices/cii/trade_delivery_spec.rb:19",
			doc:    fragment(func(n *node) { ciiTradeDelivery(n, date("2025-03-16")) }),
			checks: []check{
				comment("Applicable Header Trade Delivery"),
				val("//ram:ApplicableHeaderTradeDelivery/ram:ActualDeliverySupplyChainEvent/ram:OccurrenceDateTime/udt:DateTimeString", "20250316"),
				attrVal("//ram:OccurrenceDateTime/udt:DateTimeString", "format", "102"),
			},
		},
		{
			name:   "standard payment means",
			source: "spec/serializers/e_invoices/cii/trade_settlement_payment_spec.rb:21",
			doc:    means(paymentStandard, englishSettlement("USD")),
			checks: []check{
				comment("Payment Means: Standard payment"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "1"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:Information", "Standard payment"),
			},
		},
		{
			name:   "prepaid payment means",
			source: "spec/serializers/e_invoices/cii/trade_settlement_payment_spec.rb:34",
			doc:    means(paymentPrepaid, englishSettlement("USD")),
			checks: []check{
				comment("Payment Means: Prepaid credits"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "57"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:Information", "Prepaid credits of USD 10.00 applied"),
			},
		},
		{
			name:   "credit note payment means",
			source: "spec/serializers/e_invoices/cii/trade_settlement_payment_spec.rb:47",
			doc:    means(paymentCreditNote, englishSettlement("USD")),
			checks: []check{
				comment("Payment Means: Credit notes"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "97"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:Information", "Credit notes of USD 10.00 applied"),
			},
		},
		{
			name:   "credit card payment means",
			source: "spec/serializers/e_invoices/cii/trade_settlement_payment_spec.rb:60",
			doc:    means(paymentCreditCard, card),
			checks: []check{
				comment("Payment Means: Credit Card Payment"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "48"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:Information", "Credit card payment received on 2025-10-22 10:00:00 UTC"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:ApplicableTradeSettlementFinancialCard/ram:ID", "**** 1234"),
			},
		},
		{
			name:   "trade settlement",
			source: "spec/serializers/e_invoices/cii/trade_settlement_spec.rb:20",
			doc:    fragment(func(n *node) { ciiTradeSettlement(n, cur("EUR"), func(*node) {}) }),
			checks: []check{
				comment("Applicable Header Trade Settlement"),
				val("//ram:ApplicableHeaderTradeSettlement/ram:InvoiceCurrencyCode", "EUR"),
			},
		},
	})
}

func TestCIIDateFormats(t *testing.T) {
	// source: spec/serializers/e_invoices/cii/header_spec.rb:38
	tests := []struct {
		date core.Date
		cii  string
		ubl  string
	}{
		{date("2025-03-16"), "20250316", "2025-03-16"},
		{date("0999-01-02"), "09990102", "0999-01-02"},
	}
	for _, tt := range tests {
		if got := ciiDate(tt.date); got != tt.cii {
			t.Errorf("ciiDate(%v): got = %q, want = %q", tt.date, got, tt.cii)
		}
		if got := ublDate(tt.date); got != tt.ubl {
			t.Errorf("ublDate(%v): got = %q, want = %q", tt.date, got, tt.ubl)
		}
	}
}
