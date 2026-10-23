package einvoice

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

func TestUBLComponents(t *testing.T) {
	supplier := func(mod func(be *organization.BillingEntity), taxReg bool) func(t *testing.T) []byte {
		be := newBillingEntity("BR")
		if mod != nil {
			mod(be)
		}
		return fragment(func(n *node) { ublSupplierParty(n, be, taxReg) })
	}
	customerParty := func(noEmail, noLine2 bool) func(t *testing.T) []byte {
		c := newCustomer()
		c.Name = ptr("Andre")
		if noEmail {
			c.Email = nil
		}
		if noLine2 {
			c.BillingAddress.Line2 = ""
		}
		return fragment(func(n *node) { ublCustomerParty(n, c) })
	}
	header := func(country string, typeCode int) func(t *testing.T) []byte {
		be := newBillingEntity(country)
		c := newCustomer()
		return fragment(func(n *node) {
			ublHeader(n, parties{be, c}, "INV-1", date("2025-03-16"), typeCode, nil, cur("USD"))
		})
	}
	line := func(creditNote bool, rate string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			ublLineItem(n, ublLine{
				creditNote: creditNote, lineID: 1, quantity: dec("2"), lineExtension: dec("0.118"),
				currency: cur("USD"), name: "item name", category: categoryStandard, ratePercent: rate,
				description: "fee description", price: plain(dec("0.059")),
			})
		})
	}
	sender := func(noLine2 bool) func(t *testing.T) []byte {
		be := newBillingEntity("BR")
		be.Code, be.Name, be.TaxID, be.Email = "test_be", "Test BE", ptr("1234BR5678"), ptr("lago-be@test.com")
		be.Address.Line1, be.Address.Line2, be.Address.City, be.Address.Zipcode = "Somewhere", "Far Beyond", "A City", "1234-FE"
		if noLine2 {
			be.Address.Line2 = ""
		}
		return fragment(func(n *node) { ublSenderParty(n, be) })
	}
	receiver := func(noLine2 bool) func(t *testing.T) []byte {
		c := newCustomer()
		c.Name = ptr("Customer Jr")
		c.BillingAddress.Line1, c.BillingAddress.Line2 = "Somewhere", "Turn around, wrong way"
		c.BillingAddress.City, c.BillingAddress.Zipcode = "Some city", "09876 CE"
		if noLine2 {
			c.BillingAddress.Line2 = ""
		}
		return fragment(func(n *node) { ublReceiverParty(n, c) })
	}
	subtotal := func(category string) func(t *testing.T) []byte {
		return fragment(func(n *node) {
			ublTaxSubtotal(n, cur("EUR"), taxSubtotal{category: category, rate: dec("20.00"), basis: dec("10"), tax: dec("2.0")})
		})
	}
	const supplierRoot = "//cac:AccountingSupplierParty/cac:Party"
	const customerRoot = "//cac:AccountingCustomerParty/cac:Party"

	runXMLCases(t, []xmlCase{
		{
			name:   "allowance charge",
			source: "spec/serializers/e_invoices/ubl/allowance_charge_spec.rb:25",
			doc: fragment(func(n *node) {
				ublAllowanceCharge(n, englishSettlement("USD"), indicatorDiscount, allowance{rate: dec("19.00"), amount: dec("10.00")})
			}),
			checks: []check{
				comment("Allowances and Charges - Discount 19.00% portion"),
				val("//cac:AllowanceCharge/cbc:ChargeIndicator", "false"),
				val("//cac:AllowanceCharge/cbc:AllowanceChargeReason", "Discount 19.00% portion"),
				val("//cac:AllowanceCharge/cbc:Amount", "10.00"),
				attrVal("//cac:AllowanceCharge/cbc:Amount", "currencyID", "USD"),
				val("//cac:AllowanceCharge/cac:TaxCategory/cbc:ID", "S"),
				val("//cac:AllowanceCharge/cac:TaxCategory/cbc:Percent", "19.00"),
				val("//cac:AllowanceCharge/cac:TaxCategory/cac:TaxScheme/cbc:ID", "VAT"),
			},
		},
		{
			name:   "billing reference",
			source: "spec/serializers/e_invoices/ubl/billing_reference_spec.rb:20",
			doc:    fragment(func(n *node) { ublBillingReference(n, "INV-1", date("2025-03-16")) }),
			checks: []check{
				comment("Reference to Original Invoice"),
				val("//cac:BillingReference/cac:InvoiceDocumentReference/cbc:ID", "INV-1"),
				val("//cac:BillingReference/cac:InvoiceDocumentReference/cbc:IssueDate", "2025-03-16"),
			},
		},
		{
			name:   "customer party",
			source: "spec/serializers/e_invoices/ubl/customer_party_spec.rb:34",
			doc:    customerParty(false, false),
			checks: []check{
				comment("Customer Party"),
				val(customerRoot+"/cbc:EndpointID", "customer@example.com"),
				attrVal(customerRoot+"/cbc:EndpointID", "schemeID", "EM"),
				val(customerRoot+"/cac:PostalAddress/cbc:StreetName", "Streets of Tomorrow"),
				val(customerRoot+"/cac:PostalAddress/cbc:AdditionalStreetName", "on AC"),
				val(customerRoot+"/cac:PostalAddress/cbc:CityName", "SP"),
				val(customerRoot+"/cac:PostalAddress/cbc:PostalZone", "123654789"),
				val(customerRoot+"/cac:PostalAddress/cac:Country/cbc:IdentificationCode", "BR"),
				val(customerRoot+"/cac:Contact/cbc:Name", "Andre"),
				val(customerRoot+"/cac:Contact/cbc:ElectronicMail", "customer@example.com"),
				val(customerRoot+"/cac:PartyLegalEntity/cbc:RegistrationName", "Andre"),
			},
		},
		{
			name:   "customer party without email",
			source: "spec/serializers/e_invoices/ubl/customer_party_spec.rb:48",
			doc:    customerParty(true, false),
			checks: []check{
				none(customerRoot + "/cbc:EndpointID"),
				val(customerRoot+"/cac:Contact/cbc:Name", "Andre"),
				none(customerRoot + "/cac:Contact/cbc:ElectronicMail"),
			},
		},
		{
			name:   "customer party without address line 2",
			source: "spec/serializers/e_invoices/ubl/customer_party_spec.rb:65",
			doc:    customerParty(false, true),
			checks: []check{none(customerRoot + "/cac:PostalAddress/cbc:AdditionalStreetName")},
		},
		{
			name:   "delivery",
			source: "spec/serializers/e_invoices/ubl/delivery_spec.rb:20",
			doc:    fragment(func(n *node) { ublDelivery(n, date("2025-03-16")) }),
			checks: []check{
				comment("Delivery Information"),
				val("//cac:Delivery/cbc:ActualDeliveryDate", "2025-03-16"),
			},
		},
		{
			name:   "document response",
			source: "spec/serializers/e_invoices/ubl/document_response_spec.rb:35",
			doc: fragment(func(n *node) {
				ublDocumentResponse(n,
					ublResponse{code: responsePaid, description: "This is a test response PD", date: date("2025-03-16")},
					ublDocumentRef{id: invoiceUUID, issued: date("2025-03-15"), typeCode: typeCommercialInvoice, kind: "Invoice", description: "Original invoice reference: " + invoiceUUID})
			}),
			checks: []check{
				comment("Document Response"),
				val("//cac:DocumentResponse/cac:Response/cbc:ResponseCode", "PD"),
				val("//cac:DocumentResponse/cac:Response/cbc:Description", "This is a test response PD"),
				val("//cac:DocumentResponse/cac:Response/cbc:EffectiveDate", "2025-03-16"),
				val("//cac:DocumentResponse/cac:DocumentReference/cbc:ID", invoiceUUID),
				val("//cac:DocumentResponse/cac:DocumentReference/cbc:IssueDate", "2025-03-15"),
				val("//cac:DocumentResponse/cac:DocumentReference/cbc:DocumentTypeCode", "380"),
				val("//cac:DocumentResponse/cac:DocumentReference/cbc:DocumentType", "Invoice"),
				val("//cac:DocumentResponse/cac:DocumentReference/cbc:DocumentDescription", "Original invoice reference: "+invoiceUUID),
			},
		},
		{
			name:   "header",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:22",
			doc:    header("FR", typeCommercialInvoice),
			checks: []check{
				comment("Invoice Header Information"),
				val("//cbc:ID", "INV-1"),
				val("//cbc:IssueDate", "2025-03-16"),
				val("//cbc:InvoiceTypeCode", "380"),
				val("//cbc:DocumentCurrencyCode", "USD"),
				none("//cbc:BuyerReference"),
			},
		},
		{
			name:   "header of a prepaid invoice",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:40",
			doc:    header("FR", typePrepaidInvoice),
			checks: []check{val("//cbc:InvoiceTypeCode", "386")},
		},
		{
			name:   "header of a self-billed invoice",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:40",
			doc:    header("FR", typeSelfBilledInvoice),
			checks: []check{val("//cbc:InvoiceTypeCode", "389")},
		},
		{
			name:   "header of a credit note",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:52",
			doc:    header("FR", typeCreditNote),
			checks: []check{val("//cbc:CreditNoteTypeCode", "381"), none("//cbc:InvoiceTypeCode")},
		},
		{
			name:   "header of a German seller",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:76",
			doc:    header("DE", typeCommercialInvoice),
			checks: []check{val("//cbc:BuyerReference", "cust-ext-1")},
		},
		{
			name:   "header of a lowercase German seller",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:85",
			doc:    header("de", typeCommercialInvoice),
			checks: []check{val("//cbc:BuyerReference", "cust-ext-1")},
		},
		{
			name:   "header of a seller without country",
			source: "spec/serializers/e_invoices/ubl/header_spec.rb:94",
			doc:    header("", typeCommercialInvoice),
			checks: []check{none("//cbc:BuyerReference")},
		},
		{
			name:   "invoice line",
			source: "spec/serializers/e_invoices/ubl/line_item_spec.rb:36",
			doc:    line(false, "20.0"),
			checks: []check{
				comment("Line Item 1: fee description"),
				val("//cac:InvoiceLine/cbc:ID", "1"),
				val("//cac:InvoiceLine/cbc:InvoicedQuantity", "2.00"),
				attrVal("//cac:InvoiceLine/cbc:InvoicedQuantity", "unitCode", "C62"),
				val("//cac:InvoiceLine/cbc:LineExtensionAmount", "0.12"),
				attrVal("//cac:InvoiceLine/cbc:LineExtensionAmount", "currencyID", "USD"),
				val("//cac:InvoiceLine/cac:Item/cbc:Name", "item name"),
				val("//cac:InvoiceLine/cac:Item/cac:ClassifiedTaxCategory/cbc:ID", "S"),
				val("//cac:InvoiceLine/cac:Item/cac:ClassifiedTaxCategory/cbc:Percent", "20.0"),
				val("//cac:InvoiceLine/cac:Item/cac:ClassifiedTaxCategory/cac:TaxScheme/cbc:ID", "VAT"),
				val("//cac:InvoiceLine/cac:Item/cac:AdditionalItemProperty/cbc:Name", "Description"),
				val("//cac:InvoiceLine/cac:Item/cac:AdditionalItemProperty/cbc:Value", "fee description"),
				val("//cac:InvoiceLine/cac:Price/cbc:PriceAmount", "0.059"),
				attrVal("//cac:InvoiceLine/cac:Price/cbc:PriceAmount", "currencyID", "USD"),
			},
		},
		{
			name:   "credit note line",
			source: "spec/serializers/e_invoices/ubl/line_item_spec.rb:51",
			doc:    line(true, "20.0"),
			checks: []check{
				val("//cac:CreditNoteLine/cbc:CreditedQuantity", "2.00"),
				attrVal("//cac:CreditNoteLine/cbc:CreditedQuantity", "unitCode", "C62"),
				none("//cac:InvoiceLine"),
			},
		},
		{
			name:   "line outside the scope of VAT",
			source: "spec/serializers/e_invoices/ubl/line_item_spec.rb:86",
			doc:    line(false, ""),
			checks: []check{none("//cac:InvoiceLine/cac:Item/cac:ClassifiedTaxCategory/cbc:Percent")},
		},
		{
			name:   "monetary total",
			source: "spec/serializers/e_invoices/ubl/monetary_total_spec.rb:34",
			doc: fragment(func(n *node) {
				ublMonetaryTotal(n, cur("USD"), ublAmounts{
					lineExtension: dec("1000"), taxExclusive: dec("990"), taxInclusive: dec("1188.84"),
					allowances: dec("10"), charges: dec("0"), prepaid: dec("21.86"), payable: dec("1188.84"),
				})
			}),
			checks: []check{
				comment("Legal Monetary Total"),
				val("//cac:LegalMonetaryTotal/cbc:LineExtensionAmount", "1000.00"),
				val("//cac:LegalMonetaryTotal/cbc:ChargeTotalAmount", "0.00"),
				val("//cac:LegalMonetaryTotal/cbc:AllowanceTotalAmount", "10.00"),
				val("//cac:LegalMonetaryTotal/cbc:TaxExclusiveAmount", "990.00"),
				val("//cac:LegalMonetaryTotal/cbc:TaxInclusiveAmount", "1188.84"),
				val("//cac:LegalMonetaryTotal/cbc:PrepaidAmount", "21.86"),
				val("//cac:LegalMonetaryTotal/cbc:PayableAmount", "1188.84"),
				attrVal("//cac:LegalMonetaryTotal/cbc:PayableAmount", "currencyID", "USD"),
				attrVal("//cac:LegalMonetaryTotal/cbc:LineExtensionAmount", "currencyID", "USD"),
			},
		},
		{
			name:   "order reference",
			source: "spec/serializers/e_invoices/ubl/order_reference_spec.rb:18",
			doc:    fragment(func(n *node) { ublOrderReference(n, "PO-12345") }),
			checks: []check{comment("Order Reference"), val("//cac:OrderReference/cbc:ID", "PO-12345")},
		},
		{
			name:   "standard payment means",
			source: "spec/serializers/e_invoices/ubl/payment_means_spec.rb:22",
			doc:    fragment(func(n *node) { ublPaymentMeans(n, englishSettlement("USD"), paymentStandard) }),
			checks: []check{comment("Payment Means: Standard payment"), val("//cac:PaymentMeans/cbc:PaymentMeansCode", "1")},
		},
		{
			name:   "prepaid payment means",
			source: "spec/serializers/e_invoices/ubl/payment_means_spec.rb:34",
			doc:    fragment(func(n *node) { ublPaymentMeans(n, englishSettlement("USD"), paymentPrepaid) }),
			checks: []check{comment("Payment Means: Prepaid credits"), val("//cac:PaymentMeans/cbc:PaymentMeansCode", "57")},
		},
		{
			name:   "credit note payment means",
			source: "spec/serializers/e_invoices/ubl/payment_means_spec.rb:46",
			doc:    fragment(func(n *node) { ublPaymentMeans(n, englishSettlement("USD"), paymentCreditNote) }),
			checks: []check{comment("Payment Means: Credit notes"), val("//cac:PaymentMeans/cbc:PaymentMeansCode", "97")},
		},
		{
			name:   "payment terms",
			source: "spec/serializers/e_invoices/ubl/payment_terms_spec.rb:19",
			doc:    fragment(func(n *node) { ublPaymentTerms(n, "Payment term 1 days") }),
			checks: []check{comment("Payment Terms"), val("//cac:PaymentTerms/cbc:Note", "Payment term 1 days")},
		},
		{
			name:   "receiver party",
			source: "spec/serializers/e_invoices/ubl/receiver_party_spec.rb:32",
			doc:    receiver(false),
			checks: []check{
				comment("Receiver Party"),
				val("//cac:ReceiverParty/cac:PartyName/cbc:Name", "Customer Jr"),
				val("//cac:ReceiverParty/cac:PostalAddress/cbc:StreetName", "Somewhere"),
				val("//cac:ReceiverParty/cac:PostalAddress/cbc:AdditionalStreetName", "Turn around, wrong way"),
				val("//cac:ReceiverParty/cac:PostalAddress/cbc:CityName", "Some city"),
				val("//cac:ReceiverParty/cac:PostalAddress/cbc:PostalZone", "09876 CE"),
				val("//cac:ReceiverParty/cac:PostalAddress/cac:Country/cbc:IdentificationCode", "BR"),
			},
		},
		{
			name:   "receiver party without address line 2",
			source: "spec/serializers/e_invoices/ubl/receiver_party_spec.rb:48",
			doc:    receiver(true),
			checks: []check{none("//cac:ReceiverParty/cac:PostalAddress/cbc:AdditionalStreetName")},
		},
		{
			name:   "sender party",
			source: "spec/serializers/e_invoices/ubl/sender_party_spec.rb:36",
			doc:    sender(false),
			checks: []check{
				comment("Sender Party"),
				val("//cac:SenderParty/cac:PartyIdentification/cbc:ID", "test_be"),
				val("//cac:SenderParty/cac:PartyName/cbc:Name", "Test BE"),
				val("//cac:SenderParty/cac:PostalAddress/cbc:StreetName", "Somewhere"),
				val("//cac:SenderParty/cac:PostalAddress/cbc:AdditionalStreetName", "Far Beyond"),
				val("//cac:SenderParty/cac:PostalAddress/cbc:CityName", "A City"),
				val("//cac:SenderParty/cac:PostalAddress/cbc:PostalZone", "1234-FE"),
				val("//cac:SenderParty/cac:PostalAddress/cac:Country/cbc:IdentificationCode", "BR"),
				val("//cac:SenderParty/cac:PartyTaxScheme/cbc:CompanyID", "1234BR5678"),
				val("//cac:SenderParty/cac:PartyTaxScheme/cac:TaxScheme/cbc:ID", "VAT"),
				val("//cac:SenderParty/cac:Contact/cbc:Name", "Test BE"),
				val("//cac:SenderParty/cac:Contact/cbc:ElectronicMail", "lago-be@test.com"),
			},
		},
		{
			name:   "sender party without address line 2",
			source: "spec/serializers/e_invoices/ubl/sender_party_spec.rb:56",
			doc:    sender(true),
			checks: []check{none("//cac:SenderParty/cac:PostalAddress/cbc:AdditionalStreetName")},
		},
		{
			name:   "supplier party",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:38",
			doc:    supplier(nil, true),
			checks: []check{
				comment("Supplier Party"),
				val(supplierRoot+"/cbc:EndpointID", "billing@example.com"),
				attrVal(supplierRoot+"/cbc:EndpointID", "schemeID", "EM"),
				val(supplierRoot+"/cac:PostalAddress/cbc:StreetName", "Rue quelque part"),
				val(supplierRoot+"/cac:PostalAddress/cbc:AdditionalStreetName", "Tourne au deuxième angle"),
				val(supplierRoot+"/cac:PostalAddress/cbc:CityName", "Eine Stadt"),
				val(supplierRoot+"/cac:PostalAddress/cbc:PostalZone", "60192460"),
				val(supplierRoot+"/cac:PostalAddress/cac:Country/cbc:IdentificationCode", "BR"),
				val(supplierRoot+"/cac:PartyTaxScheme/cbc:CompanyID", "BR987654321"),
				val(supplierRoot+"/cac:PartyTaxScheme/cac:TaxScheme/cbc:ID", "VAT"),
				val(supplierRoot+"/cac:Contact/cbc:Name", "Billing Entity"),
				val(supplierRoot+"/cac:Contact/cbc:ElectronicMail", "billing@example.com"),
				none(supplierRoot + "/cac:Contact/cbc:Telephone"),
				val(supplierRoot+"/cac:PartyLegalEntity/cbc:RegistrationName", "BE Legal Name"),
				val(supplierRoot+"/cac:PartyLegalEntity/cbc:CompanyID", "BR987654321"),
			},
		},
		{
			name:   "supplier party without email",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:52",
			doc:    supplier(func(be *organization.BillingEntity) { be.Email = nil }, true),
			checks: []check{
				none(supplierRoot + "/cbc:EndpointID"),
				val(supplierRoot+"/cac:Contact/cbc:Name", "Billing Entity"),
				none(supplierRoot + "/cac:Contact/cbc:ElectronicMail"),
			},
		},
		{
			name:   "supplier party without address line 2",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:69",
			doc:    supplier(func(be *organization.BillingEntity) { be.Address.Line2 = "" }, true),
			checks: []check{none(supplierRoot + "/cac:PostalAddress/cbc:AdditionalStreetName")},
		},
		{
			name:   "supplier party without tax registration",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:102",
			doc:    supplier(nil, false),
			checks: []check{none(supplierRoot + "/cac:PartyTaxScheme")},
		},
		{
			name:   "supplier party of a German seller",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:121",
			doc: supplier(func(be *organization.BillingEntity) {
				be.Address.Country, be.Phone = "DE", ptr("+49 30 1234-5678")
			}, true),
			checks: []check{val(supplierRoot+"/cac:Contact/cbc:Telephone", "+49 30 1234-5678")},
		},
		{
			name:   "supplier party of a French seller",
			source: "spec/serializers/e_invoices/ubl/supplier_party_spec.rb:130",
			doc:    supplier(func(be *organization.BillingEntity) { be.Address.Country = "FR" }, true),
			checks: []check{none(supplierRoot + "/cac:Contact/cbc:Telephone")},
		},
		{
			name:   "tax subtotal",
			source: "spec/serializers/e_invoices/ubl/tax_subtotal_spec.rb:24",
			doc:    subtotal(categoryStandard),
			checks: []check{
				comment("Tax Information 20.00% VAT"),
				val("//cac:TaxSubtotal/cbc:TaxAmount", "2.00"),
				attrVal("//cac:TaxSubtotal/cbc:TaxAmount", "currencyID", "EUR"),
				val("//cac:TaxSubtotal/cbc:TaxableAmount", "10.00"),
				attrVal("//cac:TaxSubtotal/cbc:TaxableAmount", "currencyID", "EUR"),
				val("//cac:TaxSubtotal/cac:TaxCategory/cac:TaxScheme/cbc:ID", "VAT"),
				val("//cac:TaxSubtotal/cac:TaxCategory/cbc:ID", "S"),
				val("//cac:TaxSubtotal/cac:TaxCategory/cbc:Percent", "20.00"),
			},
		},
		{
			name:   "tax subtotal out of scope",
			source: "spec/serializers/e_invoices/ubl/tax_subtotal_spec.rb:53",
			doc:    subtotal(categoryOutOfScope),
			checks: []check{
				val("//cac:TaxSubtotal/cac:TaxCategory/cbc:ID", "O"),
				val("//cac:TaxSubtotal/cac:TaxCategory/cbc:TaxExemptionReasonCode", "VATEX-EU-O"),
				val("//cac:TaxSubtotal/cac:TaxCategory/cbc:TaxExemptionReason", "Not subject to VAT"),
				none("//cac:TaxSubtotal/cac:TaxCategory/cbc:Percent"),
			},
		},
	})
}
