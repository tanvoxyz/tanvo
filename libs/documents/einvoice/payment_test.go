package einvoice

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

func paymentCII(in *Payment) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return PaymentCII(in) })
}

func paymentUBL(in *Payment) func(t *testing.T) []byte {
	return must(func() ([]byte, error) { return PaymentUBL(in) })
}

func TestPaymentCII(t *testing.T) {
	const src = "spec/serializers/e_invoices/payments/cii/builder_spec.rb:"
	const doc = "//rsm:CrossIndustryInvoice/rsm:ExchangedDocument"
	const item = "//ram:IncludedSupplyChainTradeLineItem[1]"
	const tax = "//ram:ApplicableHeaderTradeSettlement//ram:ApplicableTradeTax"
	const sum = "//ram:ApplicableHeaderTradeSettlement//ram:SpecifiedTradeSettlementHeaderMonetarySummation"
	// The spec's method data carries the last digits but no type.
	last4 := payment.MethodDetails{Last4: "4321"}
	manual := newPaymentInput(payment.KindManual, last4)

	runXMLCases(t, []xmlCase{
		{
			name: "document", source: src + "38", doc: paymentCII(manual),
			checks: []check{
				has("//rsm:CrossIndustryInvoice"),
				val(doc+"/ram:ID", "LAG-1234-001-001"),
				val(doc+"/ram:TypeCode", "202"),
				val(doc+"/ram:IssueDateTime/udt:DateTimeString", "20251022"),
				attrVal(doc+"/ram:IssueDateTime/udt:DateTimeString", "format", "102"),
				count(doc+"/ram:IncludedNote/ram:Content", 1),
				val(doc+"/ram:IncludedNote/ram:Content",
					"Receipt for payment of BRL 10.00 received via Manual for invoice INV-24680-OIC-E"),
				has("//rsm:SupplyChainTradeTransaction"),
			},
		},
		{
			name: "line item", source: src + "87", doc: paymentCII(manual),
			checks: []check{
				count("//rsm:SupplyChainTradeTransaction/ram:IncludedSupplyChainTradeLineItem", 1),
				val(item+"//ram:Name", "Payment Received"),
				val(item+"//ram:ChargeAmount", "10.00"),
				val(item+"//ram:BilledQuantity", "1"),
				val(item+"//ram:ApplicableTradeTax//ram:CategoryCode", "Z"),
				val(item+"//ram:ApplicableTradeTax//ram:RateApplicablePercent", "0.0"),
				val(item+"//ram:LineTotalAmount", "10.00"),
			},
		},
		{
			name: "trade agreement and delivery", source: src + "147", doc: paymentCII(manual),
			checks: []check{
				val("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "MAR1234BR"),
				attrVal("//ram:ApplicableHeaderTradeAgreement//ram:SpecifiedTaxRegistration/ram:ID", "schemeID", "VA"),
				val("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "20251022"),
				attrVal("//ram:ApplicableHeaderTradeDelivery//udt:DateTimeString", "format", "102"),
				val("//ram:ApplicableHeaderTradeSettlement/ram:InvoiceCurrencyCode", "BRL"),
			},
		},
		{
			name: "manual payment means", source: src + "190", doc: paymentCII(manual),
			checks: []check{
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "1"),
				none("//ram:ApplicableHeaderTradeSettlement/ram:ApplicableTradeSettlementFinancialCard"),
				none("//ram:ApplicableTradeSettlementFinancialCard"),
			},
		},
		{
			// Without a method type the card digits are unknown.
			name: "provider payment means", source: src + "204",
			doc: paymentCII(newPaymentInput(payment.KindProvider, last4)),
			checks: []check{
				val("//ram:SpecifiedTradeSettlementPaymentMeans/ram:TypeCode", "48"),
				val("//ram:ApplicableTradeSettlementFinancialCard/ram:ID", ""),
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", " payment received on  2025-10-22 10:00:00 UTC"),
			},
		},
		{
			name: "card payment means", source: src + "211",
			doc: paymentCII(newPaymentInput(payment.KindProvider, payment.MethodDetails{Type: "card", Last4: "4321"})),
			checks: []check{
				val("//ram:ApplicableTradeSettlementFinancialCard/ram:ID", "**** 4321"),
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", "Card payment received on  2025-10-22 10:00:00 UTC"),
				val("//rsm:ExchangedDocument/ram:IncludedNote/ram:Content",
					"Receipt for payment of BRL 10.00 received via stripe for invoice INV-24680-OIC-E"),
			},
		},
		{
			name: "trade tax", source: src + "222", doc: paymentCII(manual),
			checks: []check{
				val(tax+"[1]/ram:CalculatedAmount", "0.00"),
				val(tax+"[1]/ram:BasisAmount", "10.00"),
				val(tax+"[1]/ram:CategoryCode", "Z"),
				val(tax+"[1]/ram:RateApplicablePercent", "0.00"),
			},
		},
		{
			name: "payment terms", source: src + "233", doc: paymentCII(manual),
			checks: []check{
				val("//ram:SpecifiedTradePaymentTerms/ram:Description", "Manual payment received on  2025-10-22 10:00:00 UTC"),
				val("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "20251022"),
				attrVal("//ram:SpecifiedTradePaymentTerms//udt:DateTimeString", "format", "102"),
			},
		},
		{
			name: "monetary summation", source: src + "254", doc: paymentCII(manual),
			checks: []check{
				val(sum+"/ram:LineTotalAmount", "10.00"),
				val(sum+"/ram:ChargeTotalAmount", "0.00"),
				val(sum+"/ram:AllowanceTotalAmount", "0.00"),
				val(sum+"/ram:TaxBasisTotalAmount", "10.00"),
				val(sum+"/ram:TaxTotalAmount", "0.00"),
				val(sum+"/ram:GrandTotalAmount", "10.00"),
				val(sum+"/ram:DuePayableAmount", "0.00"),
			},
		},
		{
			name: "invoice reference", source: src + "285", doc: paymentCII(manual),
			checks: []check{
				has("//ram:InvoiceReferencedDocument"),
				val("//ram:IssuerAssignedID", "INV-24680-OIC-E"),
			},
		},
	})
}

func TestPaymentUBL(t *testing.T) {
	const src = "spec/serializers/e_invoices/payments/ubl/builder_spec.rb:"
	manual := newPaymentInput(payment.KindManual, payment.MethodDetails{Last4: "4321"})
	two := newPaymentInput(payment.KindManual, payment.MethodDetails{})
	second := *two.Invoices[0]
	second.Number, second.Type = "INV-2", "credit"
	two.Invoices = append(two.Invoices, &second)

	runXMLCases(t, []xmlCase{
		{
			name: "header", source: src + "38", doc: paymentUBL(manual),
			checks: []check{
				has("/ApplicationResponse"),
				val("//cbc:UBLVersionID", "2.1"),
				val("//cbc:CustomizationID", "urn:oasis:names:specification:ubl:xpath:ApplicationResponse-2.4"),
				val("//cbc:ProfileID", "urn:oasis:names:specification:ubl:schema:xsd:ApplicationResponse-2"),
				val("//cbc:ID", "LAG-1234-001-001"),
				val("//cbc:IssueDate", "2025-10-22"),
				val("//cbc:Note", "Receipt for payment of BRL 10.00 received via Manual for invoice INV-24680-OIC-E"),
				has("//cac:SenderParty"),
				has("//cac:ReceiverParty"),
			},
		},
		{
			name: "document response", source: src + "80", doc: paymentUBL(manual),
			checks: []check{
				has("//cac:DocumentResponse"),
				val("//cac:Response/cbc:ResponseCode", "PD"),
				has("//cac:Response/cbc:Description"),
				val("//cac:Response/cbc:EffectiveDate", "2025-10-22"),
				count("//cac:DocumentReference", 1),
				val("//cac:DocumentReference/cbc:ID", "INV-24680-OIC-E"),
				val("//cac:DocumentReference/cbc:IssueDate", "2025-03-15"),
				val("//cac:DocumentReference/cbc:DocumentTypeCode", "380"),
				val("//cac:DocumentReference/cbc:DocumentType", "Invoice"),
				val("//cac:DocumentReference/cbc:DocumentDescription", "Invoice ID from payment system: "+invoiceUUID),
			},
		},
		{
			name: "one response per invoice", source: src + "102", doc: paymentUBL(two),
			checks: []check{
				count("//cac:DocumentReference", 2),
				values("//cac:DocumentReference/cbc:ID", "INV-24680-OIC-E", "INV-2"),
				values("//cac:DocumentReference/cbc:DocumentTypeCode", "380", "386"),
				val("//cbc:Note", "Receipt for payment of BRL 10.00 received via Manual for invoice INV-24680-OIC-E and INV-2"),
			},
		},
	})
}
