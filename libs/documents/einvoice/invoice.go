package einvoice

import (
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// InvoiceCII renders in as a UN/CEFACT Cross Industry Invoice (EN 16931).
func InvoiceCII(in *Invoice) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newInvoiceDoc(in)
	inv, cur := in.Invoice, in.Invoice.Currency

	root := ciiDocument()
	ciiHeader(root, inv.Number, d.typeCode(), inv.IssuingDate, d.notes())
	root.note("Supply Chain Trade Transaction")
	root.with("rsm:SupplyChainTradeTransaction", func(n *node) {
		for k, i := range d.order() {
			f := &in.Lines[i].Fee
			category := taxCategory(f.Type == core.FeeTypeCredit, f.TaxesRate)
			ciiLineItem(n, ciiLine{
				lineID:         k + 1,
				name:           in.Lines[i].Name,
				description:    in.Lines[i].Description,
				chargeAmount:   plain(f.PreciseUnitAmount),
				billedQuantity: plain(f.Units),
				category:       category,
				ratePercent:    linePercent(category, f.TaxesRate),
				lineTotal:      units(f.Amount, cur),
			})
		}
		ciiTradeAgreement(n, d.parties, inv.Type != invoice.TypeCredit, in.PurchaseOrderNumber)
		ciiTradeDelivery(n, d.deliveryDate())
		ciiTradeSettlement(n, cur, func(n *node) {
			for _, p := range d.payments() {
				ciiPaymentMeans(n, d.settlement, p.means, p.amount)
			}
			for _, t := range d.taxes() {
				ciiTradeTax(n, t)
			}
			for _, a := range d.allowanceCharges() {
				ciiAllowanceCharge(n, d.settlement, indicatorDiscount, a)
			}
			ciiPaymentTerms(n, inv.PaymentDueDate, d.paymentTermsDescription())
			prepaid := units(inv.PrepaidCreditAmount+inv.CreditNotesAmount, cur)
			ciiMonetarySummation(n, cur, ciiAmounts{
				lineTotal:  units(inv.FeesAmount, cur),
				charges:    money.Zero,
				allowances: units(d.allowances(), cur),
				taxBasis:   units(inv.SubTotalExcludingTaxesAmount, cur),
				tax:        units(inv.TaxesAmount, cur),
				grandTotal: units(inv.SubTotalIncludingTaxesAmount, cur),
				prepaid:    &prepaid,
				duePayable: units(inv.TotalAmount, cur),
			})
		})
	})
	return root.document(), nil
}

// InvoiceUBL renders in as a UBL 2.1 Invoice: EN 16931, or XRechnung 3.0
// with the Peppol BIS Billing profile for German sellers.
func InvoiceUBL(in *Invoice) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newInvoiceDoc(in)
	inv, cur := in.Invoice, in.Invoice.Currency

	root := ublDocument("Invoice", ublNSInvoice, in.BillingEntity)
	ublHeader(root, d.parties, inv.Number, inv.IssuingDate, d.typeCode(), nil, cur)
	if present(in.PurchaseOrderNumber) {
		ublOrderReference(root, in.PurchaseOrderNumber)
	}
	ublSupplierParty(root, in.BillingEntity, inv.Type != invoice.TypeCredit)
	ublCustomerParty(root, in.Customer)
	ublDelivery(root, d.deliveryDate())
	ublPaymentMeans(root, d.settlement, paymentStandard)
	ublPaymentTerms(root, d.paymentTermsNote())
	for _, a := range d.allowanceCharges() {
		ublAllowanceCharge(root, d.settlement, indicatorDiscount, a)
	}
	ublTaxTotal(root, cur, units(inv.TaxesAmount, cur), d.taxes())
	ublMonetaryTotal(root, cur, ublAmounts{
		lineExtension: units(inv.FeesAmount, cur),
		taxExclusive:  units(inv.SubTotalExcludingTaxesAmount, cur),
		taxInclusive:  units(inv.SubTotalIncludingTaxesAmount, cur),
		allowances:    units(d.allowances(), cur),
		charges:       money.Zero,
		prepaid:       units(inv.PrepaidCreditAmount+inv.CreditNotesAmount, cur),
		payable:       units(inv.TotalAmount, cur),
	})
	for k, i := range d.order() {
		f := &in.Lines[i].Fee
		category := taxCategory(f.Type == core.FeeTypeCredit, f.TaxesRate)
		ublLineItem(root, ublLine{
			lineID:        k + 1,
			quantity:      f.Units,
			lineExtension: units(f.Amount, f.Currency),
			currency:      f.Currency,
			name:          in.Lines[i].Name,
			category:      category,
			ratePercent:   linePercent(category, f.TaxesRate),
			description:   in.Lines[i].Description,
			price:         plain(f.PreciseUnitAmount),
		})
	}
	return root.document(), nil
}

// linePercent is the printed rate of a line, absent outside the scope of
// VAT.
func linePercent(category string, rate money.Decimal) string {
	if category == categoryOutOfScope {
		return ""
	}
	return plain(rate)
}

// invoiceDoc computes what both syntaxes print about an invoice.
type invoiceDoc struct {
	in         *Invoice
	parties    parties
	settlement settlement
	groups     []rateGroup
}

func newInvoiceDoc(in *Invoice) invoiceDoc {
	return invoiceDoc{
		in:      in,
		parties: parties{in.BillingEntity, in.Customer},
		settlement: settlement{
			labels:    in.Labels.orEnglish(),
			currency:  in.Invoice.Currency,
			createdAt: in.Invoice.CreatedAt,
		},
		groups: groupByRate(len(in.Lines), func(i int) money.Decimal { return in.Lines[i].Fee.TaxesRate }),
	}
}

// notes are the header notes.
func (d invoiceDoc) notes() []string { return []string{"Invoice ID: " + d.in.Invoice.ID.String()} }

// typeCode is the document type: prepaid for credit purchases, self-billed,
// or commercial.
func (d invoiceDoc) typeCode() int { return invoiceTypeCode(d.in.Invoice) }

// invoiceTypeCode is the document type of inv.
func invoiceTypeCode(inv *invoice.Invoice) int {
	switch {
	case inv.Type == invoice.TypeCredit:
		return typePrepaidInvoice
	case inv.SelfBilled:
		return typeSelfBilledInvoice
	default:
		return typeCommercialInvoice
	}
}

// order is the line order: by ascending amount.
func (d invoiceDoc) order() []int {
	return byAmount(len(d.in.Lines), func(i int) int64 { return d.in.Lines[i].Fee.Amount })
}

// deliveryDate is the date of supply.
func (d invoiceDoc) deliveryDate() core.Date {
	if !d.in.DeliveryDate.IsZero() {
		return d.in.DeliveryDate
	}
	return utcDate(d.in.Invoice.CreatedAt)
}

// settled is a payment means with its amount, in units.
type settled struct {
	means  int
	amount money.Decimal
}

// payments are the positive amounts due, paid with prepaid credits and
// paid with credit notes.
func (d invoiceDoc) payments() []settled {
	inv := d.in.Invoice
	var out []settled
	for _, p := range []struct {
		means  int
		amount int64
	}{
		{paymentStandard, inv.TotalDueAmount()},
		{paymentPrepaid, inv.PrepaidCreditAmount},
		{paymentCreditNote, inv.CreditNotesAmount},
	} {
		if p.amount > 0 {
			out = append(out, settled{p.means, units(p.amount, inv.Currency)})
		}
	}
	return out
}

// paymentTermsDescription is "Payment term 30 days".
func (d invoiceDoc) paymentTermsDescription() string {
	l := d.settlement.labels
	return l.PaymentTerm + " " + interpolate(l.PaymentTermDays, "net_payment_term", strconv.Itoa(d.in.Invoice.NetPaymentTermDays))
}

// paymentTermsNote is the payment terms followed by the prepaid credits and
// credit notes applied.
func (d invoiceDoc) paymentTermsNote() string {
	items := []string{d.paymentTermsDescription()}
	for _, p := range d.payments() {
		if p.means != paymentStandard {
			items = append(items, d.settlement.paymentInformation(p.means, p.amount))
		}
	}
	return sentence(items, d.settlement.labels)
}

// allowances are the document-level discounts, in minor units: coupons and
// progressive billing credits.
func (d invoiceDoc) allowances() int64 {
	return d.in.Invoice.CouponsAmount + d.in.Invoice.ProgressiveBillingCreditAmount
}

// groupPrecise is Σ precise amounts of a group, in minor units.
func (d invoiceDoc) groupPrecise(g rateGroup) money.Decimal {
	t := money.Zero
	for _, i := range g.members {
		t = t.Add(d.in.Lines[i].Fee.PreciseAmount)
	}
	return t
}

// taxable is the taxable base of a group at a positive rate, in minor
// units: each fee's base inferred from its tax, capped by the fee net of
// coupons so a rounded-up provider tax never inflates it.
func (d invoiceDoc) taxable(g rateGroup) money.Decimal {
	t := money.Zero
	for _, i := range g.members {
		f := &d.in.Lines[i].Fee
		inferred := f.TaxesPreciseAmount.Mul(money.NewFromInt(100)).Div(g.rate)
		t = t.Add(money.Min(inferred, f.PreciseAmount.Sub(f.PreciseCouponsAmount)))
	}
	return t
}

// allowancesPerRate is the discount of each group, in minor units: the
// untaxed part at positive rates, and a share of the document allowances
// in proportion to the group's amount at 0 %.
func (d invoiceDoc) allowancesPerRate() []money.Decimal {
	total := money.Zero
	for _, l := range d.in.Lines {
		total = total.Add(l.Fee.PreciseAmount)
	}
	out := make([]money.Decimal, len(d.groups))
	for k, g := range d.groups {
		amount := d.groupPrecise(g)
		switch {
		case g.rate.IsPositive():
			out[k] = amount.Sub(d.taxable(g))
		case total.IsZero():
			out[k] = money.Zero
		default:
			out[k] = amount.Div(total).Mul(money.NewFromInt(d.allowances()))
		}
	}
	return out
}

// allowanceCharges are the non-zero discounts per rate, in units.
func (d invoiceDoc) allowanceCharges() []allowance {
	cur := d.in.Invoice.Currency
	var out []allowance
	for k, a := range d.allowancesPerRate() {
		if a.IsZero() {
			continue
		}
		out = append(out, allowance{rate: d.groups[k].rate, amount: roundedUnits(a, cur)})
	}
	return out
}

// taxes is the VAT breakdown per rate, ascending. The bases keep their
// precision; the tax amounts split the booked invoice tax across the rates
// so they add up to it.
func (d invoiceDoc) taxes() []taxSubtotal {
	inv := d.in.Invoice
	booked := make([]money.Decimal, len(d.groups))
	precise := make([]money.Decimal, len(d.groups))
	for k, g := range d.groups {
		booked[k], precise[k] = money.Zero, money.Zero
		for _, i := range g.members {
			f := &d.in.Lines[i].Fee
			if d.in.ProviderTaxes {
				booked[k] = booked[k].Add(money.NewFromInt(f.TaxesAmount))
			} else {
				booked[k] = booked[k].Add(f.TaxesPreciseAmount)
			}
			precise[k] = precise[k].Add(f.TaxesPreciseAmount)
		}
	}
	amounts := allocateByGroup(inv.TaxesAmount, booked, precise)
	var allowances []money.Decimal
	out := make([]taxSubtotal, len(d.groups))
	for k, g := range d.groups {
		var basis money.Decimal
		if g.rate.IsPositive() {
			basis = d.taxable(g)
		} else {
			if allowances == nil {
				allowances = d.allowancesPerRate()
			}
			basis = d.groupPrecise(g).Sub(allowances[k])
		}
		out[k] = taxSubtotal{
			category: taxCategory(inv.Type == invoice.TypeCredit, g.rate),
			rate:     g.rate,
			basis:    roundedUnits(basis, inv.Currency),
			tax:      units(amounts[k], inv.Currency),
		}
	}
	return out
}
