package einvoice

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// builderCreditNote credits 10.00 of a 5 × 2 fee and 25.00 of a 1 × 25
// fee.
func builderCreditNote(mod func(in *CreditNote)) *CreditNote {
	in := newCreditNoteInput(newInvoice(invoice.TypeOneOff, "EUR"),
		cnLine(newFee(func(f *invoice.Fee) {
			f.Units, f.Amount, f.PreciseUnitAmount = dec("5"), 1000, dec("2")
		}), func(it *creditnote.Item) { it.PreciseAmount = dec("1000") }),
		cnLine(newFee(func(f *invoice.Fee) {
			f.Units, f.Amount, f.PreciseUnitAmount = dec("1"), 2500, dec("25")
		}), func(it *creditnote.Item) { it.PreciseAmount = dec("2500") }),
	)
	in.CreditNote.CreditAmount = 100
	if mod != nil {
		mod(in)
	}
	return in
}

// taxedCreditNote credits fees at 0 %, 5 % and 10 % with 0.80 of tax.
func taxedCreditNote() *CreditNote {
	item := func(rate, precise, taxPrecise string) CreditNoteLine {
		return cnLine(newFee(func(f *invoice.Fee) {
			f.TaxesRate, f.PreciseAmount, f.TaxesPreciseAmount = dec(rate), dec(precise), dec(taxPrecise)
		}), func(it *creditnote.Item) { it.PreciseAmount = dec(precise) })
	}
	in := newCreditNoteInput(newInvoice(invoice.TypeOneOff, "EUR"),
		item("0.0", "500", "0"), item("0.0", "500", "0"),
		item("5.0", "100", "5"), item("5.0", "300", "15"), item("10.0", "600", "60"))
	in.CreditNote.TaxesAmount, in.CreditNote.PreciseTaxesAmount = 80, dec("80")
	return in
}

// couponedCreditNote credits fees with 1.00 of coupons at 0 %, 5 % and 10 %.
func couponedCreditNote() *CreditNote {
	item := func(rate, coupons, feePrecise, taxPrecise, credited string) CreditNoteLine {
		return cnLine(newFee(func(f *invoice.Fee) {
			f.TaxesRate, f.PreciseCouponsAmount, f.PreciseAmount, f.TaxesPreciseAmount = dec(rate), dec(coupons), dec(feePrecise), dec(taxPrecise)
		}), func(it *creditnote.Item) { it.PreciseAmount = dec(credited) })
	}
	inv := newInvoice(invoice.TypeOneOff, "EUR")
	inv.CouponsAmount = 100
	in := newCreditNoteInput(inv,
		item("0.0", "100", "2000", "0", "1000"),
		item("5.0", "10", "100", "4.75", "100"),
		item("5.0", "10", "300", "14.25", "300"),
		item("10.0", "30", "600", "57", "600"))
	in.CreditNote.PreciseCouponsAdjustmentAmount = dec("100")
	return in
}

func cnCII(in *CreditNote) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return CreditNoteCII(in) })
}

func cnUBL(in *CreditNote) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return CreditNoteUBL(in) })
}

func TestCreditNoteCII(t *testing.T) {
	const src = "spec/serializers/e_invoices/credit_notes/cii/builder_spec.rb:"
	const doc = "//rsm:CrossIndustryInvoice/rsm:ExchangedDocument"
	const tax = "//ram:ApplicableHeaderTradeSettlement//ram:ApplicableTradeTax"
	const charge = "//ram:ApplicableHeaderTradeSettlement//ram:SpecifiedTradeAllowanceCharge"
	const sum = "//ram:ApplicableHeaderTradeSettlement//ram:SpecifiedTradeSettlementHeaderMonetarySummation"
	base := builderCreditNote(nil)

	runXMLCases(t, []xmlCase{
		{
			name: "document", source: src + "28", doc: cnCII(base),
			checks: []check{
				has("//rsm:CrossIndustryInvoice"),
				val(doc+"/ram:ID", "LAGO-TEST-123-CN001"),
				val(doc+"/ram:TypeCode", "381"),
				val(doc+"/ram:IssueDateTime/udt:DateTimeString", "20250317"),
				attrVal(doc+"/ram:IssueDateTime/udt:DateTimeString", "format", "102"),
				count(doc+"/ram:IncludedNote/ram:Content", 3),
				has("//rsm:CrossIndustryInvoice/rsm:SupplyChainTradeTransaction"),
			},
		},
		{
			name: "negative line items", source: src + "77", doc: cnCII(base),
			checks: []check{
				count("//rsm:SupplyChainTradeTransaction/ram:IncludedSupplyChainTradeLineItem", 2),
				val("//ram:IncludedSupplyChainTradeLineItem[1]//ram:BilledQuantity", "-5.0"),
				val("//ram:IncludedSupplyChainTradeLineItem[1]//ram:LineTotalAmount", "-10.00"),
			},
		},
		{
			name: "trade agreement", source: src + "109", doc: cnCII(base),
			checks: []check{
				val("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "BR987654321"),
				attrVal("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "schemeID", "VA"),
				none("//ram:BuyerOrderReferencedDocument"),
			},
		},
		{
			// The caller passes the credited invoice's purchase order number.
			name: "purchase order number of the invoice", source: src + "124",
			doc:    cnCII(builderCreditNote(func(in *CreditNote) { in.PurchaseOrderNumber = "PO-12345" })),
			checks: []check{val("//ram:BuyerOrderReferencedDocument/ram:IssuerAssignedID", "PO-12345")},
		},
		{
			name: "delivery and settlement", source: src + "135", doc: cnCII(base),
			checks: []check{
				val("//ram:ActualDeliverySupplyChainEvent/ram:OccurrenceDateTime/udt:DateTimeString", "20250317"),
				attrVal("//ram:ActualDeliverySupplyChainEvent/ram:OccurrenceDateTime/udt:DateTimeString", "format", "102"),
				val("//ram:ApplicableHeaderTradeSettlement/ram:InvoiceCurrencyCode", "EUR"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans//ram:TypeCode", "1"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans//ram:Information", "Standard payment"),
			},
		},
		{
			name: "trade tax per rate", source: src + "197", doc: cnCII(taxedCreditNote()),
			checks: []check{
				count(tax, 3),
				val(tax+"[1]/ram:CalculatedAmount", "0.00"),
				val(tax+"[1]/ram:BasisAmount", "-10.00"),
				val(tax+"[1]/ram:CategoryCode", "Z"),
				val(tax+"[1]/ram:RateApplicablePercent", "0.00"),
				val(tax+"[2]/ram:CalculatedAmount", "-0.20"),
				val(tax+"[2]/ram:BasisAmount", "-4.00"),
				val(tax+"[2]/ram:CategoryCode", "S"),
				val(tax+"[2]/ram:RateApplicablePercent", "5.00"),
				val(tax+"[3]/ram:CalculatedAmount", "-0.60"),
				val(tax+"[3]/ram:BasisAmount", "-6.00"),
				val(tax+"[3]/ram:CategoryCode", "S"),
				val(tax+"[3]/ram:RateApplicablePercent", "10.00"),
			},
		},
		{
			name: "allowances turned into charges", source: src + "246", doc: cnCII(couponedCreditNote()),
			checks: []check{
				count(charge, 3),
				val(charge+"[1]/ram:ChargeIndicator/udt:Indicator", "true"),
				val(charge+"[1]/ram:ActualAmount", "0.50"),
				has(charge + "[1]/ram:Reason"),
				val(charge+"[1]/ram:CategoryTradeTax/ram:CategoryCode", "Z"),
				val(charge+"[1]/ram:CategoryTradeTax/ram:RateApplicablePercent", "0.00"),
				val(charge+"[2]/ram:ChargeIndicator/udt:Indicator", "true"),
				val(charge+"[2]/ram:ActualAmount", "0.20"),
				val(charge+"[2]/ram:CategoryTradeTax/ram:CategoryCode", "S"),
				val(charge+"[2]/ram:CategoryTradeTax/ram:RateApplicablePercent", "5.00"),
				val(charge+"[3]/ram:ActualAmount", "0.30"),
				val(charge+"[3]/ram:CategoryTradeTax/ram:RateApplicablePercent", "10.00"),
			},
		},
		{
			name: "payment terms", source: src + "283", doc: cnCII(base),
			checks: []check{
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", "Credit note - immediate settlement"),
				val("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "20250317"),
				attrVal("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "format", "102"),
			},
		},
		{
			name: "monetary summation", source: src + "301",
			doc: cnCII(builderCreditNote(func(in *CreditNote) {
				in.Invoice.CouponsAmount = 100
				cn := in.CreditNote
				cn.PreciseCouponsAdjustmentAmount = dec("100")
				cn.TaxesAmount, cn.TotalAmount, cn.CreditAmount = 1000, 2000, 1000
			})),
			checks: []check{
				val(sum+"/ram:LineTotalAmount", "-35.00"),
				val(sum+"/ram:ChargeTotalAmount", "1.00"),
				val(sum+"/ram:AllowanceTotalAmount", "0.00"),
				val(sum+"/ram:TaxBasisTotalAmount", "-34.00"),
				val(sum+"/ram:TaxTotalAmount", "-10.00"),
				val(sum+"/ram:GrandTotalAmount", "-20.00"),
				val(sum+"/ram:DuePayableAmount", "-10.00"),
			},
		},
	})
}

func TestCreditNoteUBL(t *testing.T) {
	const src = "spec/serializers/e_invoices/credit_notes/ubl/builder_spec.rb:"
	const charge = "//cac:AllowanceCharge"
	const subtotal = "//cac:TaxTotal/cac:TaxSubtotal"
	const total = "//cac:LegalMonetaryTotal"
	const notes = "/CreditNote/cbc:Note"
	empty := func(mod func(in *CreditNote)) *CreditNote {
		in := newCreditNoteInput(newInvoice(invoice.TypeOneOff, "EUR"))
		in.CreditNote.CreditAmount = 100
		if mod != nil {
			mod(in)
		}
		return in
	}
	base := empty(nil)
	byRate := func(rate string) string { return subtotal + "[cac:TaxCategory/cbc:Percent='" + rate + "']" }

	runXMLCases(t, []xmlCase{
		{
			name: "header", source: src + "22", doc: cnUBL(base),
			checks: []check{
				has("/CreditNote"),
				val("//cbc:UBLVersionID", "2.1"),
				val("//cbc:CustomizationID", "urn:cen.eu:en16931:2017"),
				none("//cbc:ProfileID"),
				val("//cbc:ID", "LAGO-TEST-123-CN001"),
				val("//cbc:IssueDate", "2025-03-17"),
				val("//cbc:CreditNoteTypeCode", "381"),
				val("//cbc:DocumentCurrencyCode", "EUR"),
				count(notes, 3),
				val(notes+"[1]", "Credit Note ID: "+creditNoteUUID),
				val(notes+"[2]", "Original Invoice: LAGO-TEST-123"),
				val(notes+"[3]", "Reason: duplicated_charge"),
				none("//cac:OrderReference"),
			},
		},
		{
			name: "header of a German seller", source: src + "42",
			doc: cnUBL(empty(func(in *CreditNote) { in.BillingEntity.Address.Country = "DE" })),
			checks: []check{
				val("//cbc:CustomizationID", "urn:cen.eu:en16931:2017#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0"),
				val("//cbc:ProfileID", "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"),
			},
		},
		{
			name: "purchase order number of the invoice", source: src + "106",
			doc:    cnUBL(empty(func(in *CreditNote) { in.PurchaseOrderNumber = "PO-12345" })),
			checks: []check{val("//cac:OrderReference/cbc:ID", "PO-12345")},
		},
		{
			name: "billing reference and parties", source: src + "113", doc: cnUBL(base),
			checks: []check{
				has("//cac:BillingReference/cac:InvoiceDocumentReference"),
				val("//cac:InvoiceDocumentReference/cbc:ID", "LAGO-TEST-123"),
				val("//cac:InvoiceDocumentReference/cbc:IssueDate", "2025-03-15"),
				has("//cac:AccountingSupplierParty//cac:PartyTaxScheme"),
				has("//cac:AccountingCustomerParty"),
			},
		},
		{
			name: "parties of a credited credit invoice", source: src + "136",
			doc:    cnUBL(empty(func(in *CreditNote) { in.Invoice.Type = invoice.TypeCredit })),
			checks: []check{none("//cac:AccountingSupplierParty//cac:PartyTaxScheme")},
		},
		{
			name: "payment means and terms", source: src + "149", doc: cnUBL(base),
			checks: []check{
				val("//cac:PaymentMeans/cbc:PaymentMeansCode", "1"),
				count("//cac:PaymentMeans", 1),
				val("//cac:PaymentTerms/cbc:Note", "Credit note - immediate settlement"),
			},
		},
		{
			name: "allowances turned into charges", source: src + "187", doc: cnUBL(couponedCreditNote()),
			checks: []check{
				count(charge, 3),
				val(charge+"[1]/cbc:ChargeIndicator", "true"),
				has(charge + "[1]/cbc:AllowanceChargeReason"),
				val(charge+"[1]/cbc:Amount", "0.50"),
				attrVal(charge+"[1]/cbc:Amount", "currencyID", "EUR"),
				val(charge+"[1]/cac:TaxCategory/cbc:ID", "Z"),
				val(charge+"[1]/cac:TaxCategory/cbc:Percent", "0.00"),
				val(charge+"[2]/cbc:Amount", "0.20"),
				val(charge+"[2]/cac:TaxCategory/cbc:ID", "S"),
				val(charge+"[2]/cac:TaxCategory/cbc:Percent", "5.00"),
				val(charge+"[3]/cbc:Amount", "0.30"),
				val(charge+"[3]/cac:TaxCategory/cbc:Percent", "10.00"),
			},
		},
		{
			name: "tax total", source: src + "228",
			doc: cnUBL(empty(func(in *CreditNote) { in.CreditNote.PreciseTaxesAmount = dec("1000") })),
			checks: []check{
				val("//cac:TaxTotal/cbc:TaxAmount", "-10.00"),
				attrVal("//cac:TaxTotal/cbc:TaxAmount", "currencyID", "EUR"),
			},
		},
		{
			name: "tax subtotal per rate", source: src + "264", doc: cnUBL(taxedCreditNote()),
			checks: []check{
				count(subtotal, 3),
				val(byRate("0.00")+"/cbc:TaxableAmount", "-10.00"),
				val(byRate("0.00")+"/cbc:TaxAmount", "0.00"),
				val(byRate("0.00")+"/cac:TaxCategory/cbc:ID", "Z"),
				val(byRate("5.00")+"/cbc:TaxableAmount", "-4.00"),
				val(byRate("5.00")+"/cbc:TaxAmount", "-0.20"),
				val(byRate("5.00")+"/cac:TaxCategory/cbc:ID", "S"),
				val(byRate("10.00")+"/cbc:TaxableAmount", "-6.00"),
				val(byRate("10.00")+"/cbc:TaxAmount", "-0.60"),
				val(byRate("10.00")+"/cac:TaxCategory/cbc:ID", "S"),
			},
		},
		{
			name: "tax subtotal of a credit invoice", source: src + "308",
			doc: cnUBL(newCreditNoteInput(newInvoice(invoice.TypeCredit, "EUR"),
				cnLine(newFee(func(f *invoice.Fee) {
					f.Type, f.PreciseAmount, f.TaxesPreciseAmount = core.FeeTypeCredit, dec("500"), money.Zero
				}), func(it *creditnote.Item) { it.PreciseAmount = dec("500") }))),
			checks: []check{
				count(subtotal, 1),
				val(subtotal+"[1]/cbc:TaxableAmount", "-5.00"),
				val(subtotal+"[1]/cbc:TaxAmount", "0.00"),
				val(subtotal+"[1]/cac:TaxCategory/cbc:ID", "O"),
				val(subtotal+"[1]/cac:TaxCategory/cbc:TaxExemptionReasonCode", "VATEX-EU-O"),
				val(subtotal+"[1]/cac:TaxCategory/cbc:TaxExemptionReason", "Not subject to VAT"),
				none(subtotal + "[1]/cac:TaxCategory/cbc:Percent"),
			},
		},
		{
			name: "legal monetary total", source: src + "332",
			doc: cnUBL(empty(func(in *CreditNote) {
				five := func(it *creditnote.Item) { it.PreciseAmount = dec("500") }
				in.Lines = []CreditNoteLine{cnLine(newFee(nil), five), cnLine(newFee(nil), five)}
				in.CreditNote.PreciseCouponsAdjustmentAmount, in.CreditNote.PreciseTaxesAmount = dec("100"), dec("90")
			})),
			checks: []check{
				val(total+"/cbc:LineExtensionAmount", "-10.00"),
				attrVal(total+"/cbc:LineExtensionAmount", "currencyID", "EUR"),
				val(total+"/cbc:TaxExclusiveAmount", "-9.00"),
				val(total+"/cbc:TaxInclusiveAmount", "-9.90"),
				val(total+"/cbc:AllowanceTotalAmount", "0.00"),
				val(total+"/cbc:ChargeTotalAmount", "1.00"),
				val(total+"/cbc:PrepaidAmount", "0.00"),
				val(total+"/cbc:PayableAmount", "-9.90"),
				attrVal(total+"/cbc:PayableAmount", "currencyID", "EUR"),
			},
		},
		{
			name: "credit note lines", source: src + "402",
			doc: cnUBL(empty(func(in *CreditNote) {
				in.Lines = []CreditNoteLine{
					cnLine(newFee(func(f *invoice.Fee) {
						f.Units, f.PreciseAmount, f.PreciseUnitAmount = dec("5"), dec("100"), dec("2")
					}), func(it *creditnote.Item) { it.Amount, it.PreciseAmount = 1000, dec("100") }),
					cnLine(newFee(func(f *invoice.Fee) {
						f.Units, f.PreciseAmount, f.PreciseUnitAmount = dec("1"), dec("250"), dec("25")
					}), func(it *creditnote.Item) { it.Amount, it.PreciseAmount = 2500, dec("250") }),
				}
			})),
			checks: []check{
				count("//cac:CreditNoteLine", 2),
				val("//cac:CreditNoteLine[1]/cbc:ID", "1"),
				val("//cac:CreditNoteLine[1]/cbc:CreditedQuantity", "-5.00"),
				attrVal("//cac:CreditNoteLine[1]/cbc:CreditedQuantity", "unitCode", "C62"),
				val("//cac:CreditNoteLine[1]/cbc:LineExtensionAmount", "-10.00"),
				attrVal("//cac:CreditNoteLine[1]/cbc:LineExtensionAmount", "currencyID", "EUR"),
				val("//cac:CreditNoteLine[2]/cbc:ID", "2"),
				val("//cac:CreditNoteLine[2]/cbc:CreditedQuantity", "-1.00"),
				val("//cac:CreditNoteLine[2]/cbc:LineExtensionAmount", "-25.00"),
			},
		},
	})
}

// TestCreditNoteTaxReconciliation ports the credit note reconciliation
// spec, which runs against both syntaxes.
func TestCreditNoteTaxReconciliation(t *testing.T) {
	const src = "spec/serializers/e_invoices/credit_notes/common_spec.rb:"
	// Fees of 4 and 2 cents (or 8 and 4 with half deducted as coupons) at
	// 10 % and 20 %, each taxed 0.4 cents; the credit note books 1 cent.
	creditNote := func(amounts [2]int64, coupons [2]string) *CreditNote {
		var lines []CreditNoteLine
		for k, rate := range []string{"10", "20"} {
			f := newFee(func(f *invoice.Fee) {
				f.Amount, f.PreciseAmount, f.PreciseCouponsAmount = amounts[k], money.NewFromInt(amounts[k]), dec(coupons[k])
				f.TaxesRate, f.TaxesAmount, f.TaxesPreciseAmount = dec(rate), 0, dec("0.4")
			})
			lines = append(lines, cnLine(f, func(it *creditnote.Item) {
				it.Amount, it.PreciseAmount = amounts[k], money.NewFromInt(amounts[k])
			}))
		}
		in := newCreditNoteInput(newInvoice(invoice.TypeOneOff, "EUR"), lines...)
		in.CreditNote.TaxesAmount, in.CreditNote.PreciseTaxesAmount = 1, dec("0.8")
		return in
	}
	formats := []struct {
		name                   string
		render                 func(*CreditNote) ([]byte, error)
		subtotal, total, basis string
	}{
		{"ubl", CreditNoteUBL, "//cac:TaxSubtotal/cbc:TaxAmount", "//cac:TaxTotal/cbc:TaxAmount", "//cac:TaxSubtotal/cbc:TaxableAmount"},
		{
			"cii", CreditNoteCII, "//ram:ApplicableHeaderTradeSettlement/ram:ApplicableTradeTax/ram:CalculatedAmount",
			"//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TaxTotalAmount",
			"//ram:ApplicableHeaderTradeSettlement/ram:ApplicableTradeTax/ram:BasisAmount",
		},
	}
	for _, f := range formats {
		render := func(in *CreditNote) func(t *testing.T) []byte {
			return must(func() ([]byte, error) { return f.render(in) })
		}
		t.Run(f.name, func(t *testing.T) {
			runXMLCases(t, []xmlCase{
				{
					name: "fractional groups reconciled to the booked total", source: src + "44",
					doc: render(creditNote([2]int64{4, 2}, [2]string{"0", "0"})),
					checks: []check{
						sum(f.subtotal, "-0.01"),
						val(f.total, "-0.01"),
						values(f.basis, "-0.04", "-0.02"),
					},
				},
				{
					name: "coupons deducted before tax", source: src + "56",
					doc: render(creditNote([2]int64{8, 4}, [2]string{"4", "2"})),
					checks: []check{
						sum(f.subtotal, "-0.01"),
						val(f.total, "-0.01"),
						values(f.basis, "-0.04", "-0.02"),
					},
				},
			})
		})
	}
}
