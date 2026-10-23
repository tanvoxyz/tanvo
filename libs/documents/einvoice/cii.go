package einvoice

import (
	"fmt"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ciiDocument returns the CrossIndustryInvoice root with its exchange
// document context (EN 16931).
func ciiDocument() *node {
	root := newRoot("rsm:CrossIndustryInvoice", ciiNamespaces()...)
	root.note("Exchange Document Context")
	root.with("rsm:ExchangedDocumentContext", func(n *node) {
		n.with("ram:GuidelineSpecifiedDocumentContextParameter", func(n *node) {
			n.leaf("ram:ID", profileEN16931)
		})
	})
	return root
}

// ciiDateTime appends a CCYYMMDD date element.
func ciiDateTime(n *node, name string, d core.Date) {
	n.with(name, func(n *node) {
		n.leaf("udt:DateTimeString", ciiDate(d), attr{"format", ciiDateFormat})
	})
}

// ciiHeader appends the exchanged document header.
func ciiHeader(n *node, number string, typeCode int, issued core.Date, notes []string) {
	n.note("Exchange Document Header")
	n.with("rsm:ExchangedDocument", func(n *node) {
		n.leaf("ram:ID", number)
		n.leaf("ram:TypeCode", strconv.Itoa(typeCode))
		ciiDateTime(n, "ram:IssueDateTime", issued)
		for _, note := range notes {
			n.with("ram:IncludedNote", func(n *node) { n.leaf("ram:Content", note) })
		}
	})
}

// ciiLine is a line item; the texts are printed as given.
type ciiLine struct {
	lineID         int
	name           string
	description    string
	chargeAmount   string
	billedQuantity string
	category       string
	ratePercent    string
	lineTotal      money.Decimal
}

// ciiLineItem appends a supply chain trade line item.
func ciiLineItem(n *node, l ciiLine) {
	n.note(fmt.Sprintf("Line Item %d: %s", l.lineID, l.description))
	n.with("ram:IncludedSupplyChainTradeLineItem", func(n *node) {
		n.with("ram:AssociatedDocumentLineDocument", func(n *node) {
			n.leaf("ram:LineID", strconv.Itoa(l.lineID))
		})
		n.with("ram:SpecifiedTradeProduct", func(n *node) {
			n.leaf("ram:Name", l.name)
			n.leaf("ram:Description", l.description)
		})
		n.with("ram:SpecifiedLineTradeAgreement", func(n *node) {
			n.with("ram:NetPriceProductTradePrice", func(n *node) {
				n.leaf("ram:ChargeAmount", l.chargeAmount)
			})
		})
		n.with("ram:SpecifiedLineTradeDelivery", func(n *node) {
			n.leaf("ram:BilledQuantity", l.billedQuantity, attr{"unitCode", unitCode})
		})
		n.with("ram:SpecifiedLineTradeSettlement", func(n *node) {
			n.with("ram:ApplicableTradeTax", func(n *node) {
				n.leaf("ram:TypeCode", taxScheme)
				n.leaf("ram:CategoryCode", l.category)
				if present(l.ratePercent) {
					n.leaf("ram:RateApplicablePercent", l.ratePercent)
				}
			})
			n.with("ram:SpecifiedTradeSettlementLineMonetarySummation", func(n *node) {
				n.leaf("ram:LineTotalAmount", number(l.lineTotal))
			})
		})
	})
}

// ciiTradeAgreement appends the seller, the buyer and the buyer's order
// reference.
func ciiTradeAgreement(n *node, p parties, taxRegistration bool, purchaseOrder string) {
	be, c := p.seller, p.buyer
	n.note("Applicable Header Trade Agreement")
	n.with("ram:ApplicableHeaderTradeAgreement", func(n *node) {
		n.with("ram:SellerTradeParty", func(n *node) {
			n.leaf("ram:ID", be.Code)
			n.leaf("ram:Name", deref(be.LegalName))
			n.with("ram:PostalTradeAddress", func(n *node) {
				n.leaf("ram:PostcodeCode", be.Address.Zipcode)
				n.leaf("ram:LineOne", be.Address.Line1)
				n.leaf("ram:LineTwo", be.Address.Line2)
				n.leaf("ram:CityName", be.Address.City)
				n.leaf("ram:CountryID", be.Address.Country)
			})
			if taxRegistration {
				n.with("ram:SpecifiedTaxRegistration", func(n *node) {
					n.leaf("ram:ID", deref(be.TaxID), attr{"schemeID", ciiTaxSchemeID})
				})
			}
		})
		n.with("ram:BuyerTradeParty", func(n *node) {
			name := c.Name
			if c.LegalName != nil {
				name = c.LegalName
			}
			n.leaf("ram:Name", deref(name))
			n.with("ram:PostalTradeAddress", func(n *node) {
				n.leaf("ram:PostcodeCode", c.BillingAddress.Zipcode)
				n.leaf("ram:LineOne", c.BillingAddress.Line1)
				n.leaf("ram:LineTwo", c.BillingAddress.Line2)
				n.leaf("ram:CityName", c.BillingAddress.City)
				n.leaf("ram:CountryID", c.BillingAddress.Country)
			})
		})
		if present(purchaseOrder) {
			n.with("ram:BuyerOrderReferencedDocument", func(n *node) {
				n.leaf("ram:IssuerAssignedID", purchaseOrder)
			})
		}
	})
}

// ciiTradeDelivery appends the date of supply.
func ciiTradeDelivery(n *node, delivered core.Date) {
	n.note("Applicable Header Trade Delivery")
	n.with("ram:ApplicableHeaderTradeDelivery", func(n *node) {
		n.with("ram:ActualDeliverySupplyChainEvent", func(n *node) {
			ciiDateTime(n, "ram:OccurrenceDateTime", delivered)
		})
	})
}

// ciiTradeSettlement appends the header trade settlement, filled by fn.
func ciiTradeSettlement(n *node, cur money.Currency, fn func(*node)) {
	n.note("Applicable Header Trade Settlement")
	n.with("ram:ApplicableHeaderTradeSettlement", func(n *node) {
		n.leaf("ram:InvoiceCurrencyCode", cur.Code())
		fn(n)
	})
}

// ciiPaymentMeans appends a payment means of amount units.
func ciiPaymentMeans(n *node, s settlement, means int, amount money.Decimal) {
	n.note("Payment Means: " + s.paymentLabel(means))
	n.with("ram:SpecifiedTradeSettlementPaymentMeans", func(n *node) {
		n.leaf("ram:TypeCode", strconv.Itoa(means))
		n.leaf("ram:Information", s.paymentInformation(means, amount))
		if means == paymentCreditCard {
			n.with("ram:ApplicableTradeSettlementFinancialCard", func(n *node) {
				n.leaf("ram:ID", s.cardDigits)
			})
		}
	})
}

// ciiTradeTax appends the VAT breakdown at one rate.
func ciiTradeTax(n *node, t taxSubtotal) {
	n.note(fmt.Sprintf("Tax Information %s %s", percent(t.rate), taxScheme))
	n.with("ram:ApplicableTradeTax", func(n *node) {
		n.leaf("ram:CalculatedAmount", number(t.tax))
		n.leaf("ram:TypeCode", taxScheme)
		n.leaf("ram:BasisAmount", number(t.basis))
		n.leaf("ram:CategoryCode", t.category)
		if t.category == categoryOutOfScope {
			n.leaf("ram:ExemptionReasonCode", vatExemptionOutOfScope)
		} else {
			n.leaf("ram:RateApplicablePercent", number(t.rate))
		}
	})
}

// ciiAllowanceCharge appends a document-level allowance (discount) or
// charge at one rate.
func ciiAllowanceCharge(n *node, s settlement, charge bool, a allowance) {
	n.note(fmt.Sprintf("Allowance/Charge - Discount %s portion", percent(a.rate)))
	n.with("ram:SpecifiedTradeAllowanceCharge", func(n *node) {
		n.with("ram:ChargeIndicator", func(n *node) {
			n.leaf("udt:Indicator", strconv.FormatBool(charge))
		})
		n.leaf("ram:ActualAmount", number(a.amount))
		n.leaf("ram:Reason", s.discountReason(a.rate))
		n.with("ram:CategoryTradeTax", func(n *node) {
			n.leaf("ram:TypeCode", taxScheme)
			n.leaf("ram:CategoryCode", taxCategory(false, a.rate))
			n.leaf("ram:RateApplicablePercent", number(a.rate))
		})
	})
}

// ciiPaymentTerms appends the payment terms.
func ciiPaymentTerms(n *node, due core.Date, description string) {
	n.note("Payment Terms")
	n.with("ram:SpecifiedTradePaymentTerms", func(n *node) {
		n.leaf("ram:Description", description)
		ciiDateTime(n, "ram:DueDateDateTime", due)
	})
}

// ciiAmounts are the document totals, in units. Prepaid is printed when
// set.
type ciiAmounts struct {
	lineTotal  money.Decimal
	charges    money.Decimal
	allowances money.Decimal
	taxBasis   money.Decimal
	tax        money.Decimal
	grandTotal money.Decimal
	prepaid    *money.Decimal
	duePayable money.Decimal
}

// ciiMonetarySummation appends the document totals.
func ciiMonetarySummation(n *node, cur money.Currency, a ciiAmounts) {
	n.note("Monetary Summation")
	n.with("ram:SpecifiedTradeSettlementHeaderMonetarySummation", func(n *node) {
		n.leaf("ram:LineTotalAmount", number(a.lineTotal))
		n.leaf("ram:ChargeTotalAmount", number(a.charges))
		n.leaf("ram:AllowanceTotalAmount", number(a.allowances))
		n.leaf("ram:TaxBasisTotalAmount", number(a.taxBasis))
		n.leaf("ram:TaxTotalAmount", number(a.tax), attr{"currencyID", cur.Code()})
		n.leaf("ram:GrandTotalAmount", number(a.grandTotal))
		if a.prepaid != nil {
			n.leaf("ram:TotalPrepaidAmount", number(*a.prepaid))
		}
		n.leaf("ram:DuePayableAmount", number(a.duePayable))
	})
}

// ciiInvoiceReference appends the reference to the settled invoices.
func ciiInvoiceReference(n *node, reference string) {
	n.note("Invoice reference")
	n.with("ram:InvoiceReferencedDocument", func(n *node) {
		n.leaf("ram:IssuerAssignedID", reference)
	})
}
