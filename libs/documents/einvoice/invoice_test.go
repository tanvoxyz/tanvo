package einvoice

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// builderInvoice is the invoice of the builder specs: USD, 30.00 due on
// 2025-03-16, with fees of 5 × 2 = 10.00 and 1 × 25 = 25.00.
func builderInvoice(typ invoice.Type, mod func(in *Invoice)) *Invoice {
	in := newInvoiceInput(typ, "USD",
		newFee(func(f *invoice.Fee) {
			f.Units, f.Amount, f.PreciseUnitAmount = dec("5"), 1000, dec("2")
		}),
		newFee(func(f *invoice.Fee) {
			f.Units, f.Amount, f.PreciseUnitAmount = dec("1"), 2500, dec("25")
		}),
	)
	in.Invoice.TotalAmount = 3000
	in.Invoice.TaxesAmount = 2500
	in.Invoice.PaymentDueDate = date("2025-03-16")
	if mod != nil {
		mod(in)
	}
	return in
}

// taxedInvoice is the invoice of the tax breakdown specs: 1.00 of coupons
// over fees at 0 %, 5 % and 10 %.
func taxedInvoice(typ invoice.Type) *Invoice {
	fee := func(rate, precise, taxPrecise string, tax int64) invoice.Fee {
		return newFee(func(f *invoice.Fee) {
			f.TaxesRate, f.PreciseAmount, f.TaxesPreciseAmount, f.TaxesAmount = dec(rate), dec(precise), dec(taxPrecise), tax
		})
	}
	in := newInvoiceInput(typ, "EUR",
		fee("0.0", "1000", "0", 0),
		fee("5.0", "100", "4.75", 5),
		fee("5.0", "300", "14.25", 14),
		fee("10.0", "600", "57", 57),
	)
	in.Invoice.CouponsAmount = 100
	in.Invoice.TaxesAmount = 76
	return in
}

// zeroPreciseInvoice has a single fee with a zero precise amount.
func zeroPreciseInvoice(typ invoice.Type) *Invoice {
	in := newInvoiceInput(typ, "EUR", newFee(func(f *invoice.Fee) {
		f.PreciseAmount, f.TaxesPreciseAmount = money.Zero, money.Zero
	}))
	in.Invoice.CouponsAmount = 100
	return in
}

func withInvoice(in *Invoice, mod func(in *Invoice)) *Invoice {
	mod(in)
	return in
}

func ciiOf(in *Invoice) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return InvoiceCII(in) })
}

func ublOf(in *Invoice) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return InvoiceUBL(in) })
}

func TestInvoiceCII(t *testing.T) {
	const src = "spec/serializers/e_invoices/invoices/cii/builder_spec.rb:"
	const doc = "//rsm:CrossIndustryInvoice/rsm:ExchangedDocument"
	const tax = "//ram:ApplicableHeaderTradeSettlement//ram:ApplicableTradeTax"
	const charge = "//ram:ApplicableHeaderTradeSettlement//ram:SpecifiedTradeAllowanceCharge"
	const sum = "//ram:ApplicableHeaderTradeSettlement//ram:SpecifiedTradeSettlementHeaderMonetarySummation"
	oneOff := builderInvoice(invoice.TypeOneOff, nil)

	runXMLCases(t, []xmlCase{
		{
			name: "document", source: src + "24", doc: ciiOf(oneOff),
			checks: []check{
				has("//rsm:CrossIndustryInvoice"),
				has(doc),
				val(doc+"/ram:ID", "LAGO-TEST-123"),
				val(doc+"/ram:TypeCode", "380"),
				val(doc+"/ram:IssueDateTime/udt:DateTimeString", "20250315"),
				attrVal(doc+"/ram:IssueDateTime/udt:DateTimeString", "format", "102"),
				count(doc+"/ram:IncludedNote/ram:Content", 1),
				val(doc+"/ram:IncludedNote/ram:Content", "Invoice ID: "+invoiceUUID),
				has("//rsm:SupplyChainTradeTransaction"),
			},
		},
		{
			name: "type code of a credit invoice", source: src + "48",
			doc:    ciiOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{val(doc+"/ram:TypeCode", "386")},
		},
		{
			name: "type code of a self-billed invoice", source: src + "57",
			doc:    ciiOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) { in.Invoice.SelfBilled = true })),
			checks: []check{val(doc+"/ram:TypeCode", "389")},
		},
		{
			name: "line items", source: src + "94", doc: ciiOf(oneOff),
			checks: []check{
				count("//rsm:SupplyChainTradeTransaction/ram:IncludedSupplyChainTradeLineItem", 2),
				val("//ram:IncludedSupplyChainTradeLineItem[1]//ram:BilledQuantity", "5.0"),
				val("//ram:IncludedSupplyChainTradeLineItem[1]//ram:LineTotalAmount", "10.00"),
				val("//ram:IncludedSupplyChainTradeLineItem[2]//ram:LineTotalAmount", "25.00"),
			},
		},
		{
			name: "trade agreement", source: src + "124", doc: ciiOf(oneOff),
			checks: []check{
				has("//ram:ApplicableHeaderTradeAgreement"),
				val("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "BR987654321"),
				attrVal("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "schemeID", "VA"),
				none("//ram:BuyerOrderReferencedDocument"),
			},
		},
		{
			name: "trade agreement of a credit invoice", source: src + "133",
			doc:    ciiOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{none("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID")},
		},
		{
			name: "purchase order number", source: src + "147",
			doc:    ciiOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) { in.PurchaseOrderNumber = "PO-12345" })),
			checks: []check{val("//ram:BuyerOrderReferencedDocument/ram:IssuerAssignedID", "PO-12345")},
		},
		{
			name: "delivery of a one-off invoice", source: src + "161", doc: ciiOf(oneOff),
			checks: []check{
				has("//ram:ApplicableHeaderTradeDelivery"),
				val("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "20250315"),
				attrVal("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "format", "102"),
			},
		},
		{
			name: "delivery of a credit invoice", source: src + "171",
			doc:    ciiOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{val("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "20250315")},
		},
		{
			// The caller resolves the start of the current charges period.
			name: "delivery of a subscription invoice", source: src + "185",
			doc: ciiOf(builderInvoice(invoice.TypeSubscription, func(in *Invoice) {
				in.DeliveryDate = date("2025-04-01")
			})),
			checks: []check{val("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "20250401")},
		},
		{
			name: "trade settlement", source: src + "196", doc: ciiOf(oneOff),
			checks: []check{
				has("//ram:ApplicableHeaderTradeSettlement"),
				val("//ram:ApplicableHeaderTradeSettlement/ram:InvoiceCurrencyCode", "USD"),
			},
		},
		{
			name: "payment means", source: src + "211",
			doc: ciiOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) {
				in.Invoice.PrepaidCreditAmount, in.Invoice.CreditNotesAmount = 1000, 2000
			})),
			checks: []check{
				count("//ram:ApplicableHeaderTradeSettlement/ram:SpecifiedTradeSettlementPaymentMeans", 3),
				val("//ram:SpecifiedTradeSettlementPaymentMeans[1]/ram:TypeCode", "1"),
				has("//ram:SpecifiedTradeSettlementPaymentMeans[1]/ram:Information"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans[2]/ram:TypeCode", "57"),
				has("//ram:SpecifiedTradeSettlementPaymentMeans[2]/ram:Information"),
				val("//ram:SpecifiedTradeSettlementPaymentMeans[3]/ram:TypeCode", "97"),
				has("//ram:SpecifiedTradeSettlementPaymentMeans[3]/ram:Information"),
			},
		},
		{
			name: "trade tax per rate", source: src + "263", doc: ciiOf(taxedInvoice(invoice.TypeOneOff)),
			checks: []check{
				count(tax, 3),
				val(tax+"[1]/ram:CalculatedAmount", "0.00"),
				val(tax+"[1]/ram:BasisAmount", "9.50"),
				val(tax+"[1]/ram:CategoryCode", "Z"),
				val(tax+"[1]/ram:RateApplicablePercent", "0.00"),
				val(tax+"[2]/ram:CalculatedAmount", "0.19"),
				val(tax+"[2]/ram:BasisAmount", "3.80"),
				val(tax+"[2]/ram:CategoryCode", "S"),
				val(tax+"[2]/ram:RateApplicablePercent", "5.00"),
				val(tax+"[3]/ram:CalculatedAmount", "0.57"),
				val(tax+"[3]/ram:BasisAmount", "5.70"),
				val(tax+"[3]/ram:CategoryCode", "S"),
				val(tax+"[3]/ram:RateApplicablePercent", "10.00"),
			},
		},
		{
			name: "allowance charge per rate", source: src + "303", doc: ciiOf(taxedInvoice(invoice.TypeOneOff)),
			checks: []check{
				count(charge, 3),
				val(charge+"[1]/ram:ChargeIndicator/udt:Indicator", "false"),
				val(charge+"[1]/ram:ActualAmount", "0.50"),
				has(charge + "[1]/ram:Reason"),
				val(charge+"[1]/ram:CategoryTradeTax/ram:CategoryCode", "Z"),
				val(charge+"[1]/ram:CategoryTradeTax/ram:RateApplicablePercent", "0.00"),
				val(charge+"[2]/ram:ChargeIndicator/udt:Indicator", "false"),
				val(charge+"[2]/ram:ActualAmount", "0.20"),
				val(charge+"[2]/ram:CategoryTradeTax/ram:CategoryCode", "S"),
				val(charge+"[2]/ram:CategoryTradeTax/ram:RateApplicablePercent", "5.00"),
				val(charge+"[3]/ram:ActualAmount", "0.30"),
				val(charge+"[3]/ram:CategoryTradeTax/ram:CategoryCode", "S"),
				val(charge+"[3]/ram:CategoryTradeTax/ram:RateApplicablePercent", "10.00"),
			},
		},
		{
			name: "allowance charge when every precise amount is zero", source: src + "340",
			doc:    ciiOf(zeroPreciseInvoice(invoice.TypeOneOff)),
			checks: []check{count(charge, 0)},
		},
		{
			name: "payment terms", source: src + "351", doc: ciiOf(oneOff),
			checks: []check{
				has("//ram:SpecifiedTradePaymentTerms"),
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", "Payment term 0 days"),
				val("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "20250316"),
				attrVal("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "format", "102"),
			},
		},
		{
			name: "monetary summation", source: src + "382",
			doc: ciiOf(withInvoice(newInvoiceInput(invoice.TypeOneOff, "EUR"), func(in *Invoice) {
				inv := in.Invoice
				inv.CouponsAmount, inv.FeesAmount, inv.SubTotalExcludingTaxesAmount = 100, 3500, 3500
				inv.TaxesAmount, inv.SubTotalIncludingTaxesAmount = 500, 4000
				inv.PrepaidCreditAmount, inv.CreditNotesAmount, inv.TotalAmount = 500, 500, 3000
			})),
			checks: []check{
				has(sum),
				val(sum+"/ram:LineTotalAmount", "35.00"),
				val(sum+"/ram:ChargeTotalAmount", "0.00"),
				val(sum+"/ram:AllowanceTotalAmount", "1.00"),
				val(sum+"/ram:TaxBasisTotalAmount", "35.00"),
				val(sum+"/ram:TaxTotalAmount", "5.00"),
				val(sum+"/ram:GrandTotalAmount", "40.00"),
				val(sum+"/ram:TotalPrepaidAmount", "10.00"),
				val(sum+"/ram:DuePayableAmount", "30.00"),
			},
		},
	})
}

func TestInvoiceUBL(t *testing.T) {
	const src = "spec/serializers/e_invoices/invoices/ubl/builder_spec.rb:"
	const charge = "//cac:AllowanceCharge"
	const subtotal = "//cac:TaxTotal/cac:TaxSubtotal"
	const total = "//cac:LegalMonetaryTotal"
	oneOff := builderInvoice(invoice.TypeOneOff, nil)
	german := builderInvoice(invoice.TypeOneOff, func(in *Invoice) { in.BillingEntity.Address.Country = "DE" })
	applied := func(typ invoice.Type) *Invoice {
		return builderInvoice(typ, func(in *Invoice) {
			in.Invoice.NetPaymentTermDays, in.Invoice.PrepaidCreditAmount, in.Invoice.CreditNotesAmount = 2, 1000, 2000
		})
	}

	runXMLCases(t, []xmlCase{
		{
			name: "header", source: src + "24", doc: ublOf(oneOff),
			checks: []check{
				has("/Invoice"),
				val("//cbc:UBLVersionID", "2.1"),
				val("//cbc:CustomizationID", "urn:cen.eu:en16931:2017"),
				none("//cbc:ProfileID"),
				val("//cbc:ID", "LAGO-TEST-123"),
				val("//cbc:IssueDate", "2025-03-15"),
				val("//cbc:InvoiceTypeCode", "380"),
				val("//cbc:DocumentCurrencyCode", "USD"),
				none("//cac:OrderReference"),
			},
		},
		{
			name: "header of a German seller", source: src + "45", doc: ublOf(german),
			checks: []check{
				val("//cbc:CustomizationID", "urn:cen.eu:en16931:2017#compliant#urn:xeinkauf.de:kosit:xrechnung_3.0"),
				val("//cbc:ProfileID", "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"),
			},
		},
		{
			name: "type code of a credit invoice", source: src + "83",
			doc:    ublOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{val("//cbc:InvoiceTypeCode", "386")},
		},
		{
			name: "type code of a self-billed invoice", source: src + "92",
			doc:    ublOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) { in.Invoice.SelfBilled = true })),
			checks: []check{val("//cbc:InvoiceTypeCode", "389")},
		},
		{
			name: "purchase order number", source: src + "119",
			doc:    ublOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) { in.PurchaseOrderNumber = "PO-12345" })),
			checks: []check{val("//cac:OrderReference/cbc:ID", "PO-12345")},
		},
		{
			name: "parties", source: src + "139", doc: ublOf(oneOff),
			checks: []check{
				has("//cac:AccountingSupplierParty"),
				has("//cac:AccountingSupplierParty//cac:PartyTaxScheme"),
				has("//cac:AccountingCustomerParty"),
			},
		},
		{
			name: "parties of a credit invoice", source: src + "133",
			doc:    ublOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{none("//cac:AccountingSupplierParty//cac:PartyTaxScheme")},
		},
		{
			name: "delivery of a one-off invoice", source: src + "160", doc: ublOf(oneOff),
			checks: []check{val("//cac:Delivery/cbc:ActualDeliveryDate", "2025-03-15")},
		},
		{
			name: "delivery of a credit invoice", source: src + "170",
			doc:    ublOf(builderInvoice(invoice.TypeCredit, nil)),
			checks: []check{val("//cac:Delivery/cbc:ActualDeliveryDate", "2025-03-15")},
		},
		{
			// The caller resolves the earliest start of the current charges
			// periods.
			name: "delivery of a subscription invoice", source: src + "187",
			doc: ublOf(builderInvoice(invoice.TypeSubscription, func(in *Invoice) {
				in.DeliveryDate = date("2025-04-01")
			})),
			checks: []check{val("//cac:Delivery/cbc:ActualDeliveryDate", "2025-04-01")},
		},
		{
			name: "payment means", source: src + "198", doc: ublOf(oneOff),
			checks: []check{
				val("//cac:PaymentMeans//cbc:PaymentMeansCode", "1"),
				count("//cac:PaymentMeans", 1),
				has("//cac:PaymentTerms"),
			},
		},
		{
			name: "payment means and terms with credits applied", source: src + "215",
			doc: ublOf(applied(invoice.TypeOneOff)),
			checks: []check{
				count("//cac:PaymentMeans", 1),
				val("//cac:PaymentTerms/cbc:Note",
					"Payment term 2 days, Prepaid credits of USD 10.00 applied, and Credit notes of USD 20.00 applied"),
			},
		},
		{
			name: "allowance charge per rate", source: src + "255", doc: ublOf(taxedInvoice(invoice.TypeOneOff)),
			checks: []check{
				count(charge, 3),
				val(charge+"[1]/cbc:ChargeIndicator", "false"),
				has(charge + "[1]/cbc:AllowanceChargeReason"),
				val(charge+"[1]/cbc:Amount", "0.50"),
				val(charge+"[1]/cac:TaxCategory/cbc:ID", "Z"),
				val(charge+"[1]/cac:TaxCategory/cbc:Percent", "0.00"),
				val(charge+"[1]/cac:TaxCategory/cac:TaxScheme/cbc:ID", "VAT"),
				val(charge+"[2]/cbc:Amount", "0.20"),
				val(charge+"[2]/cac:TaxCategory/cbc:ID", "S"),
				val(charge+"[2]/cac:TaxCategory/cbc:Percent", "5.00"),
				val(charge+"[3]/cbc:Amount", "0.30"),
				val(charge+"[3]/cac:TaxCategory/cbc:ID", "S"),
				val(charge+"[3]/cac:TaxCategory/cbc:Percent", "10.00"),
			},
		},
		{
			name: "allowance charge when every precise amount is zero", source: src + "295",
			doc:    ublOf(zeroPreciseInvoice(invoice.TypeOneOff)),
			checks: []check{count(charge, 0)},
		},
		{
			name: "tax total", source: src + "310", doc: ublOf(oneOff),
			checks: []check{
				val("//cac:TaxTotal/cbc:TaxAmount", "25.00"),
				attrVal("//cac:TaxTotal/cbc:TaxAmount", "currencyID", "USD"),
			},
		},
		{
			name: "tax subtotal per rate", source: src + "333", doc: ublOf(taxedInvoice(invoice.TypeOneOff)),
			checks: []check{
				count(subtotal, 3),
				val(subtotal+"[1]/cbc:TaxableAmount", "9.50"),
				val(subtotal+"[1]/cbc:TaxAmount", "0.00"),
				val(subtotal+"[1]/cac:TaxCategory/cbc:ID", "Z"),
				val(subtotal+"[1]/cac:TaxCategory/cbc:Percent", "0.00"),
				val(subtotal+"[2]/cbc:TaxableAmount", "3.80"),
				val(subtotal+"[2]/cbc:TaxAmount", "0.19"),
				val(subtotal+"[2]/cac:TaxCategory/cbc:ID", "S"),
				val(subtotal+"[2]/cac:TaxCategory/cbc:Percent", "5.00"),
				val(subtotal+"[3]/cbc:TaxableAmount", "5.70"),
				val(subtotal+"[3]/cbc:TaxAmount", "0.57"),
				val(subtotal+"[3]/cac:TaxCategory/cbc:ID", "S"),
				val(subtotal+"[3]/cac:TaxCategory/cbc:Percent", "10.00"),
			},
		},
		{
			name: "legal monetary total", source: src + "375",
			doc: ublOf(builderInvoice(invoice.TypeOneOff, func(in *Invoice) {
				inv := in.Invoice
				inv.FeesAmount, inv.SubTotalExcludingTaxesAmount, inv.SubTotalIncludingTaxesAmount = 1000, 1100, 1200
				inv.CouponsAmount, inv.ProgressiveBillingCreditAmount = 600, 700
				inv.PrepaidCreditAmount, inv.CreditNotesAmount, inv.TotalAmount = 700, 700, 1500
			})),
			checks: []check{
				val(total+"/cbc:LineExtensionAmount", "10.00"),
				attrVal(total+"/cbc:LineExtensionAmount", "currencyID", "USD"),
				val(total+"/cbc:TaxExclusiveAmount", "11.00"),
				val(total+"/cbc:TaxInclusiveAmount", "12.00"),
				val(total+"/cbc:AllowanceTotalAmount", "13.00"),
				val(total+"/cbc:ChargeTotalAmount", "0.00"),
				val(total+"/cbc:PrepaidAmount", "14.00"),
				val(total+"/cbc:PayableAmount", "15.00"),
				attrVal(total+"/cbc:PayableAmount", "currencyID", "USD"),
			},
		},
		{
			name: "invoice lines", source: src + "435", doc: ublOf(oneOff),
			checks: []check{
				count("//cac:InvoiceLine", 2),
				val("//cac:InvoiceLine[1]/cbc:ID", "1"),
				val("//cac:InvoiceLine[1]/cbc:InvoicedQuantity", "5.00"),
				attrVal("//cac:InvoiceLine[1]/cbc:InvoicedQuantity", "unitCode", "C62"),
				val("//cac:InvoiceLine[1]/cbc:LineExtensionAmount", "10.00"),
				attrVal("//cac:InvoiceLine[1]/cbc:LineExtensionAmount", "currencyID", "EUR"),
				val("//cac:InvoiceLine[2]/cbc:ID", "2"),
				val("//cac:InvoiceLine[2]/cbc:InvoicedQuantity", "1.00"),
				val("//cac:InvoiceLine[2]/cbc:LineExtensionAmount", "25.00"),
				attrVal("//cac:InvoiceLine[2]/cbc:LineExtensionAmount", "currencyID", "EUR"),
			},
		},
	})
}

// TestInvoiceTaxReconciliation ports the tax reconciliation specs, which run
// against both syntaxes: the subtotals add up to the booked invoice tax.
func TestInvoiceTaxReconciliation(t *testing.T) {
	const src = "spec/serializers/e_invoices/invoices/common_provider_taxes_spec.rb:"
	fee := func(amount int64, rate string, tax int64, taxPrecise string) invoice.Fee {
		return newFee(func(f *invoice.Fee) {
			f.Amount, f.PreciseAmount = amount, money.NewFromInt(amount)
			f.TaxesRate, f.TaxesAmount, f.TaxesPreciseAmount = dec(rate), tax, dec(taxPrecise)
		})
	}
	invoiceOf := func(provider bool, taxes int64, fees ...invoice.Fee) *Invoice {
		in := newInvoiceInput(invoice.TypeOneOff, "EUR", fees...)
		in.Invoice.TaxesAmount, in.ProviderTaxes = taxes, provider
		return in
	}
	type format struct {
		name                          string
		render                        func(*Invoice) ([]byte, error)
		subtotal, total, basis, rated string
	}
	formats := []format{
		{
			"ubl", InvoiceUBL, "//cac:TaxSubtotal/cbc:TaxAmount", "//cac:TaxTotal/cbc:TaxAmount",
			"//cac:TaxSubtotal/cbc:TaxableAmount", "//cac:TaxSubtotal[cac:TaxCategory/cbc:Percent='10.00']/cbc:TaxAmount",
		},
		{
			"cii", InvoiceCII, "//ram:ApplicableHeaderTradeSettlement/ram:ApplicableTradeTax/ram:CalculatedAmount",
			"//ram:SpecifiedTradeSettlementHeaderMonetarySummation/ram:TaxTotalAmount",
			"//ram:ApplicableTradeTax/ram:BasisAmount", "//ram:ApplicableTradeTax[ram:RateApplicablePercent='10.00']/ram:CalculatedAmount",
		},
	}
	for _, f := range formats {
		render := func(in *Invoice) func(t *testing.T) []byte {
			return must(func() ([]byte, error) { return f.render(in) })
		}
		t.Run(f.name, func(t *testing.T) {
			runXMLCases(t, []xmlCase{
				{
					// 12 % + 5 % from the provider on 10.00.
					name: "provider taxes", source: src + "29",
					doc:    render(invoiceOf(true, 146, fee(1000, "17", 146, "146"))),
					checks: []check{sum(f.subtotal, "1.46"), val(f.total, "1.46")},
				},
				{
					name: "provider rounding the tax up", source: src + "46",
					doc:    render(invoiceOf(true, 1, fee(6, "10", 1, "1"))),
					checks: []check{val(f.basis, "0.06"), sum(f.subtotal, "0.01")},
				},
				{
					// Three 2.5 % jurisdictions split over two fees of a
					// grouped charge.
					name: "grouped charge rounding between fees", source: src + "66",
					doc:    render(invoiceOf(true, 15, fee(100, "7.5", 8, "7.5"), fee(100, "7.5", 7, "7.5"))),
					checks: []check{sum(f.subtotal, "0.15"), val(f.total, "0.15")},
				},
				{
					name: "precise jurisdiction total differing from the booked total", source: src + "79",
					doc:    render(invoiceOf(true, 8, fee(100, "7.5", 4, "3.75"), fee(100, "7.5", 4, "3.75"))),
					checks: []check{sum(f.subtotal, "0.08"), val(f.total, "0.08")},
				},
				{
					name: "independent fees rounding up at the same rate", source: src + "98",
					doc:    render(invoiceOf(true, 6, fee(100, "2.5", 3, "2.5"), fee(100, "2.5", 3, "2.5"))),
					checks: []check{sum(f.subtotal, "0.06"), val(f.total, "0.06")},
				},
				{
					name: "invoice-level rounding", source: src + "116",
					doc:    render(invoiceOf(false, 1, fee(4, "10", 0, "0.4"), fee(4, "10", 0, "0.4"))),
					checks: []check{sum(f.subtotal, "0.01"), val(f.total, "0.01")},
				},
				{
					name: "legacy provider invoice with zero booked tax per fee", source: src + "127",
					doc:    render(invoiceOf(true, 1, fee(4, "10", 0, "0.4"), fee(4, "10", 0, "0.4"))),
					checks: []check{sum(f.subtotal, "0.01"), val(f.total, "0.01")},
				},
				{
					name: "multiple native rates", source: src + "153",
					doc: render(invoiceOf(false, 21,
						fee(3, "10", 0, "0.3"), fee(3, "10", 0, "0.3"), fee(100, "20", 20, "20"))),
					checks: []check{
						val(f.rated, "0.01"),
						values(f.subtotal, "0.01", "0.20"),
						val(f.total, "0.21"),
					},
				},
			})
		})
	}
}
