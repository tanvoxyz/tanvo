package einvoice

import (
	"fmt"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ublDocument returns a UBL root element with the version and the
// customization of the seller's country: XRechnung 3.0 with the Peppol BIS
// Billing profile for Germany, EN 16931 otherwise.
func ublDocument(name, namespace string, be *organization.BillingEntity) *node {
	root := newRoot(name, ublNamespaces(namespace)...)
	root.note("UBL Version and Customization")
	root.leaf("cbc:UBLVersionID", ublVersion)
	if germanSeller(be) {
		root.leaf("cbc:CustomizationID", profileXRechnung)
		root.leaf("cbc:ProfileID", profilePeppolBIS)
	} else {
		root.leaf("cbc:CustomizationID", profileEN16931)
	}
	return root
}

// currencyID is the currencyID attribute of an amount.
func currencyID(cur money.Currency) attr { return attr{"currencyID", cur.Code()} }

// ublHeader appends the document header. The buyer reference (the
// customer's external ID) is printed for German sellers.
func ublHeader(n *node, p parties, number string, issued core.Date, typeCode int, notes []string, cur money.Currency) {
	n.note("Invoice Header Information")
	n.leaf("cbc:ID", number)
	n.leaf("cbc:IssueDate", ublDate(issued))
	tag := "cbc:InvoiceTypeCode"
	if typeCode == typeCreditNote {
		tag = "cbc:CreditNoteTypeCode"
	}
	n.leaf(tag, strconv.Itoa(typeCode))
	for _, note := range notes {
		n.leaf("cbc:Note", note)
	}
	n.leaf("cbc:DocumentCurrencyCode", cur.Code())
	if germanSeller(p.seller) {
		n.leaf("cbc:BuyerReference", p.buyer.ExternalID)
	}
}

// ublOrderReference appends the buyer's purchase order.
func ublOrderReference(n *node, id string) {
	n.note("Order Reference")
	n.with("cac:OrderReference", func(n *node) { n.leaf("cbc:ID", id) })
}

// ublBillingReference appends the reference to the credited invoice.
func ublBillingReference(n *node, number string, issued core.Date) {
	n.note("Reference to Original Invoice")
	n.with("cac:BillingReference", func(n *node) {
		n.with("cac:InvoiceDocumentReference", func(n *node) {
			n.leaf("cbc:ID", number)
			n.leaf("cbc:IssueDate", ublDate(issued))
		})
	})
}

// ublAddress appends a postal address.
func ublAddress(n *node, a core.Address) {
	n.with("cac:PostalAddress", func(n *node) {
		n.leaf("cbc:StreetName", a.Line1)
		if present(a.Line2) {
			n.leaf("cbc:AdditionalStreetName", a.Line2)
		}
		n.leaf("cbc:CityName", a.City)
		n.leaf("cbc:PostalZone", a.Zipcode)
		n.with("cac:Country", func(n *node) { n.leaf("cbc:IdentificationCode", a.Country) })
	})
}

// ublPartyTaxScheme appends a VAT registration.
func ublPartyTaxScheme(n *node, taxID string) {
	n.with("cac:PartyTaxScheme", func(n *node) {
		n.leaf("cbc:CompanyID", taxID)
		n.with("cac:TaxScheme", func(n *node) { n.leaf("cbc:ID", taxScheme) })
	})
}

// ublSupplierParty appends the seller.
func ublSupplierParty(n *node, be *organization.BillingEntity, taxRegistration bool) {
	email := deref(be.Email)
	n.note("Supplier Party")
	n.with("cac:AccountingSupplierParty", func(n *node) {
		n.with("cac:Party", func(n *node) {
			if present(email) {
				n.leaf("cbc:EndpointID", email, attr{"schemeID", "EM"})
			}
			ublAddress(n, be.Address)
			if taxRegistration {
				ublPartyTaxScheme(n, deref(be.TaxID))
			}
			n.with("cac:PartyLegalEntity", func(n *node) {
				n.leaf("cbc:RegistrationName", deref(be.LegalName))
				n.leaf("cbc:CompanyID", deref(be.TaxID))
			})
			n.with("cac:Contact", func(n *node) {
				n.leaf("cbc:Name", be.Name)
				if germanSeller(be) {
					n.leaf("cbc:Telephone", deref(be.Phone))
				}
				if present(email) {
					n.leaf("cbc:ElectronicMail", email)
				}
			})
		})
	})
}

// ublCustomerParty appends the buyer.
func ublCustomerParty(n *node, c *customer.Customer) {
	email, name := deref(c.Email), deref(c.Name)
	n.note("Customer Party")
	n.with("cac:AccountingCustomerParty", func(n *node) {
		n.with("cac:Party", func(n *node) {
			if present(email) {
				n.leaf("cbc:EndpointID", email, attr{"schemeID", "EM"})
			}
			ublAddress(n, c.BillingAddress)
			n.with("cac:PartyLegalEntity", func(n *node) { n.leaf("cbc:RegistrationName", name) })
			n.with("cac:Contact", func(n *node) {
				n.leaf("cbc:Name", name)
				if present(email) {
					n.leaf("cbc:ElectronicMail", email)
				}
			})
		})
	})
}

// ublDelivery appends the date of supply.
func ublDelivery(n *node, delivered core.Date) {
	n.note("Delivery Information")
	n.with("cac:Delivery", func(n *node) { n.leaf("cbc:ActualDeliveryDate", ublDate(delivered)) })
}

// ublPaymentMeans appends a payment means.
func ublPaymentMeans(n *node, s settlement, means int) {
	n.note("Payment Means: " + s.paymentLabel(means))
	n.with("cac:PaymentMeans", func(n *node) { n.leaf("cbc:PaymentMeansCode", strconv.Itoa(means)) })
}

// ublPaymentTerms appends the payment terms.
func ublPaymentTerms(n *node, note string) {
	n.note("Payment Terms")
	n.with("cac:PaymentTerms", func(n *node) { n.leaf("cbc:Note", note) })
}

// ublAllowanceCharge appends a document-level allowance (discount) or
// charge at one rate.
func ublAllowanceCharge(n *node, s settlement, charge bool, a allowance) {
	n.note(fmt.Sprintf("Allowances and Charges - Discount %s portion", percent(a.rate)))
	n.with("cac:AllowanceCharge", func(n *node) {
		n.leaf("cbc:ChargeIndicator", strconv.FormatBool(charge))
		n.leaf("cbc:AllowanceChargeReason", s.discountReason(a.rate))
		n.leaf("cbc:Amount", amountText(a.amount, s.currency), currencyID(s.currency))
		n.with("cac:TaxCategory", func(n *node) {
			n.leaf("cbc:ID", taxCategory(false, a.rate))
			n.leaf("cbc:Percent", number(a.rate))
			n.with("cac:TaxScheme", func(n *node) { n.leaf("cbc:ID", taxScheme) })
		})
	})
}

// ublTaxTotal appends the tax total and its breakdown.
func ublTaxTotal(n *node, cur money.Currency, total money.Decimal, subtotals []taxSubtotal) {
	n.note("Tax Total Information")
	n.with("cac:TaxTotal", func(n *node) {
		n.leaf("cbc:TaxAmount", number(total), currencyID(cur))
		for _, t := range subtotals {
			ublTaxSubtotal(n, cur, t)
		}
	})
}

// ublTaxSubtotal appends the VAT breakdown at one rate.
func ublTaxSubtotal(n *node, cur money.Currency, t taxSubtotal) {
	n.note(fmt.Sprintf("Tax Information %s %s", percent(t.rate), taxScheme))
	n.with("cac:TaxSubtotal", func(n *node) {
		n.leaf("cbc:TaxableAmount", number(t.basis), currencyID(cur))
		n.leaf("cbc:TaxAmount", number(t.tax), currencyID(cur))
		n.with("cac:TaxCategory", func(n *node) {
			n.leaf("cbc:ID", t.category)
			if t.category == categoryOutOfScope {
				n.leaf("cbc:TaxExemptionReasonCode", vatExemptionOutOfScope)
				n.leaf("cbc:TaxExemptionReason", "Not subject to VAT")
			} else {
				n.leaf("cbc:Percent", number(t.rate))
			}
			n.with("cac:TaxScheme", func(n *node) { n.leaf("cbc:ID", taxScheme) })
		})
	})
}

// ublAmounts are the legal monetary totals, in units.
type ublAmounts struct {
	lineExtension money.Decimal
	taxExclusive  money.Decimal
	taxInclusive  money.Decimal
	allowances    money.Decimal
	charges       money.Decimal
	prepaid       money.Decimal
	payable       money.Decimal
}

// ublMonetaryTotal appends the legal monetary totals.
func ublMonetaryTotal(n *node, cur money.Currency, a ublAmounts) {
	id := currencyID(cur)
	n.note("Legal Monetary Total")
	n.with("cac:LegalMonetaryTotal", func(n *node) {
		n.leaf("cbc:LineExtensionAmount", number(a.lineExtension), id)
		n.leaf("cbc:TaxExclusiveAmount", number(a.taxExclusive), id)
		n.leaf("cbc:TaxInclusiveAmount", number(a.taxInclusive), id)
		n.leaf("cbc:AllowanceTotalAmount", number(a.allowances), id)
		n.leaf("cbc:ChargeTotalAmount", number(a.charges), id)
		n.leaf("cbc:PrepaidAmount", number(a.prepaid), id)
		n.leaf("cbc:PayableAmount", number(a.payable), id)
	})
}

// ublLine is an invoice or credit note line; the price is printed as
// given.
type ublLine struct {
	creditNote    bool
	lineID        int
	quantity      money.Decimal
	lineExtension money.Decimal
	currency      money.Currency
	name          string
	category      string
	ratePercent   string
	description   string
	price         string
}

// ublLineItem appends an invoice or credit note line.
func ublLineItem(n *node, l ublLine) {
	lineTag, quantityTag := "cac:InvoiceLine", "cbc:InvoicedQuantity"
	if l.creditNote {
		lineTag, quantityTag = "cac:CreditNoteLine", "cbc:CreditedQuantity"
	}
	n.note(fmt.Sprintf("Line Item %d: %s", l.lineID, l.description))
	n.with(lineTag, func(n *node) {
		n.leaf("cbc:ID", strconv.Itoa(l.lineID))
		n.leaf(quantityTag, number(l.quantity), attr{"unitCode", unitCode})
		n.leaf("cbc:LineExtensionAmount", number(l.lineExtension), currencyID(l.currency))
		n.with("cac:Item", func(n *node) {
			n.leaf("cbc:Name", l.name)
			n.with("cac:ClassifiedTaxCategory", func(n *node) {
				n.leaf("cbc:ID", l.category)
				if present(l.ratePercent) {
					n.leaf("cbc:Percent", l.ratePercent)
				}
				n.with("cac:TaxScheme", func(n *node) { n.leaf("cbc:ID", taxScheme) })
			})
			n.with("cac:AdditionalItemProperty", func(n *node) {
				n.leaf("cbc:Name", "Description")
				n.leaf("cbc:Value", l.description)
			})
		})
		n.with("cac:Price", func(n *node) { n.leaf("cbc:PriceAmount", l.price, currencyID(l.currency)) })
	})
}

// ublSenderParty appends the issuer of an application response.
func ublSenderParty(n *node, be *organization.BillingEntity) {
	n.note("Sender Party")
	n.with("cac:SenderParty", func(n *node) {
		n.with("cac:PartyIdentification", func(n *node) { n.leaf("cbc:ID", be.Code) })
		n.with("cac:PartyName", func(n *node) { n.leaf("cbc:Name", be.Name) })
		ublAddress(n, be.Address)
		ublPartyTaxScheme(n, deref(be.TaxID))
		n.with("cac:Contact", func(n *node) {
			n.leaf("cbc:Name", be.Name)
			n.leaf("cbc:ElectronicMail", deref(be.Email))
		})
	})
}

// ublReceiverParty appends the receiver of an application response.
func ublReceiverParty(n *node, c *customer.Customer) {
	n.note("Receiver Party")
	n.with("cac:ReceiverParty", func(n *node) {
		n.with("cac:PartyName", func(n *node) { n.leaf("cbc:Name", deref(c.Name)) })
		ublAddress(n, c.BillingAddress)
	})
}

// ublResponse is the response of a document response.
type ublResponse struct {
	code        string
	description string
	date        core.Date
}

// ublDocumentRef is the document a document response is about.
type ublDocumentRef struct {
	id          string
	issued      core.Date
	typeCode    int
	kind        string
	description string
}

// ublDocumentResponse appends the response about one document.
func ublDocumentResponse(n *node, r ublResponse, d ublDocumentRef) {
	n.note("Document Response")
	n.with("cac:DocumentResponse", func(n *node) {
		n.with("cac:Response", func(n *node) {
			n.leaf("cbc:ResponseCode", r.code)
			n.leaf("cbc:Description", r.description)
			n.leaf("cbc:EffectiveDate", ublDate(r.date))
		})
		n.with("cac:DocumentReference", func(n *node) {
			n.leaf("cbc:ID", d.id)
			n.leaf("cbc:IssueDate", ublDate(d.issued))
			n.leaf("cbc:DocumentTypeCode", strconv.Itoa(d.typeCode))
			n.leaf("cbc:DocumentType", d.kind)
			n.leaf("cbc:DocumentDescription", d.description)
		})
	})
}
