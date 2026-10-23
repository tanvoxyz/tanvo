package einvoice

import (
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreditNoteCII renders in as a UN/CEFACT Cross Industry Invoice of type
// credit note (EN 16931). Amounts are negative.
func CreditNoteCII(in *CreditNote) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newCreditNoteDoc(in)
	cn, cur := in.CreditNote, in.CreditNote.Currency
	created := utcDate(cn.CreatedAt)

	root := ciiDocument()
	ciiHeader(root, cn.Number, typeCreditNote, cn.IssuingDate, d.notes())
	root.note("Supply Chain Trade Transaction")
	root.with("rsm:SupplyChainTradeTransaction", func(n *node) {
		for k, i := range d.order() {
			l := &in.Lines[i]
			category := taxCategory(l.Fee.Type == core.FeeTypeCredit, l.Fee.TaxesRate)
			ciiLineItem(n, ciiLine{
				lineID:         k + 1,
				name:           l.Name,
				description:    l.Description,
				chargeAmount:   plain(l.Fee.PreciseUnitAmount),
				billedQuantity: plain(l.Fee.Units.Neg()),
				category:       category,
				ratePercent:    linePercent(category, l.Fee.TaxesRate),
				lineTotal:      roundedUnits(l.Item.PreciseAmount.Neg(), cur),
			})
		}
		ciiTradeAgreement(n, d.parties, true, in.PurchaseOrderNumber)
		ciiTradeDelivery(n, created)
		ciiTradeSettlement(n, cur, func(n *node) {
			ciiPaymentMeans(n, d.settlement, paymentStandard, units(cn.CreditAmount, cur))
			for _, t := range d.taxes() {
				ciiTradeTax(n, t.negated())
			}
			for _, a := range d.allowanceCharges() {
				ciiAllowanceCharge(n, d.settlement, indicatorCharge, a)
			}
			ciiPaymentTerms(n, created, creditNoteSettlement)
			ciiMonetarySummation(n, cur, ciiAmounts{
				lineTotal:  roundedUnits(d.itemsPrecise(), cur).Neg(),
				charges:    roundedUnits(cn.PreciseCouponsAdjustmentAmount, cur),
				allowances: money.Zero,
				taxBasis:   units(d.subTotalExcludingTaxes(), cur).Neg(),
				tax:        units(cn.TaxesAmount, cur).Neg(),
				grandTotal: units(cn.TotalAmount, cur).Neg(),
				duePayable: units(cn.CreditAmount, cur).Neg(),
			})
		})
	})
	return root.document(), nil
}

// CreditNoteUBL renders in as a UBL 2.1 CreditNote: EN 16931, or XRechnung
// 3.0 with the Peppol BIS Billing profile for German sellers. Amounts are
// negative.
func CreditNoteUBL(in *CreditNote) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newCreditNoteDoc(in)
	cn, inv, cur := in.CreditNote, in.Invoice, in.CreditNote.Currency

	root := ublDocument("CreditNote", ublNSCreditNote, in.BillingEntity)
	ublHeader(root, d.parties, cn.Number, cn.IssuingDate, typeCreditNote, d.notes(), cur)
	if present(in.PurchaseOrderNumber) {
		ublOrderReference(root, in.PurchaseOrderNumber)
	}
	ublBillingReference(root, inv.Number, inv.IssuingDate)
	ublSupplierParty(root, in.BillingEntity, inv.Type != invoice.TypeCredit)
	ublCustomerParty(root, in.Customer)
	ublPaymentMeans(root, d.settlement, paymentStandard)
	ublPaymentTerms(root, creditNoteSettlement)
	for _, a := range d.allowanceCharges() {
		ublAllowanceCharge(root, d.settlement, indicatorCharge, a)
	}
	taxes := d.taxes()
	for k := range taxes {
		taxes[k] = taxes[k].negated()
	}
	ublTaxTotal(root, cur, roundedUnits(cn.PreciseTaxesAmount, cur).Neg(), taxes)
	subTotal := money.NewFromInt(d.subTotalExcludingTaxes())
	ublMonetaryTotal(root, cur, ublAmounts{
		lineExtension: roundedUnits(d.itemsPrecise(), cur).Neg(),
		taxExclusive:  cur.MinorToUnits(subTotal).Neg(),
		taxInclusive:  roundedUnits(subTotal.Add(cn.PreciseTaxesAmount), cur).Neg(),
		allowances:    money.Zero,
		charges:       roundedUnits(cn.PreciseCouponsAdjustmentAmount, cur),
		prepaid:       money.Zero,
		payable:       roundedUnits(d.preciseTotal(), cur).Neg(),
	})
	for k, i := range d.order() {
		l := &in.Lines[i]
		category := taxCategory(l.Fee.Type == core.FeeTypeCredit, l.Fee.TaxesRate)
		ublLineItem(root, ublLine{
			creditNote:    true,
			lineID:        k + 1,
			quantity:      feeRate(l).Mul(l.Fee.Units).Neg(),
			lineExtension: units(l.Item.Amount, cur).Neg(),
			currency:      cur,
			name:          l.Name,
			category:      category,
			ratePercent:   linePercent(category, l.Fee.TaxesRate),
			description:   l.Description,
			price:         plain(l.Fee.PreciseUnitAmount),
		})
	}
	return root.document(), nil
}

// feeRate is the credited share of a line's fee.
func feeRate(l *CreditNoteLine) money.Decimal {
	if l.Fee.PreciseAmount.IsZero() {
		return l.Item.PreciseAmount
	}
	return l.Item.PreciseAmount.Div(l.Fee.PreciseAmount)
}

// negated is t with its basis and tax negated.
func (t taxSubtotal) negated() taxSubtotal {
	t.basis, t.tax = t.basis.Neg(), t.tax.Neg()
	return t
}

// creditNoteDoc computes what both syntaxes print about a credit note.
type creditNoteDoc struct {
	in         *CreditNote
	parties    parties
	settlement settlement
	groups     []rateGroup
}

func newCreditNoteDoc(in *CreditNote) creditNoteDoc {
	return creditNoteDoc{
		in:      in,
		parties: parties{in.BillingEntity, in.Customer},
		settlement: settlement{
			labels:    in.Labels.orEnglish(),
			currency:  in.CreditNote.Currency,
			createdAt: in.CreditNote.CreatedAt,
		},
		groups: groupByRate(len(in.Lines), func(i int) money.Decimal { return in.Lines[i].Fee.TaxesRate }),
	}
}

// notes are the header notes.
func (d creditNoteDoc) notes() []string {
	cn := d.in.CreditNote
	return []string{
		"Credit Note ID: " + cn.ID.String(),
		"Original Invoice: " + d.in.Invoice.Number,
		"Reason: " + string(cn.Reason),
	}
}

// order is the line order: by ascending item amount.
func (d creditNoteDoc) order() []int {
	return byAmount(len(d.in.Lines), func(i int) int64 { return d.in.Lines[i].Item.Amount })
}

// itemsPrecise is Σ precise item amounts, in minor units.
func (d creditNoteDoc) itemsPrecise() money.Decimal {
	t := money.Zero
	for _, l := range d.in.Lines {
		t = t.Add(l.Item.PreciseAmount)
	}
	return t
}

// subTotalExcludingTaxes is the items net of coupons, rounded half up to
// whole minor units.
func (d creditNoteDoc) subTotalExcludingTaxes() int64 {
	n, _ := d.itemsPrecise().Sub(d.in.CreditNote.PreciseCouponsAdjustmentAmount).Round(0, money.HalfUp).Int64()
	return n
}

// preciseTotal is the items net of coupons plus taxes, in minor units.
func (d creditNoteDoc) preciseTotal() money.Decimal {
	cn := d.in.CreditNote
	return d.itemsPrecise().Sub(cn.PreciseCouponsAdjustmentAmount).Add(cn.PreciseTaxesAmount)
}

// allowancesPerRate is the coupons credited back per rate, in minor units:
// each fee's coupons prorated by the credited share of the fee.
func (d creditNoteDoc) allowancesPerRate() []money.Decimal {
	out := make([]money.Decimal, len(d.groups))
	for k, g := range d.groups {
		out[k] = money.Zero
		for _, i := range g.members {
			l := &d.in.Lines[i]
			share := money.Zero
			if l.Fee.Amount != 0 {
				share = l.Item.PreciseAmount.DivOrZero(l.Fee.PreciseAmount)
			}
			out[k] = out[k].Add(l.Fee.PreciseCouponsAmount.Mul(share))
		}
	}
	return out
}

// allowanceCharges are the non-zero coupons credited back per rate, in
// units.
func (d creditNoteDoc) allowanceCharges() []allowance {
	var out []allowance
	for k, a := range d.allowancesPerRate() {
		if a.IsZero() {
			continue
		}
		out = append(out, allowance{rate: d.groups[k].rate, amount: roundedUnits(a, d.in.CreditNote.Currency)})
	}
	return out
}

// prorated is a fee amount prorated by the credited share of the fee.
func prorated(l *CreditNoteLine, amount money.Decimal) money.Decimal {
	if l.Fee.Amount == 0 {
		return money.Zero
	}
	return amount.Mul(l.Item.PreciseAmount).Div(money.NewFromInt(l.Fee.Amount))
}

// taxes is the VAT breakdown per rate, ascending, positive. The tax amounts
// split the credit note's booked tax across the rates: by the prorated
// provider taxes of the credited fees, or by each base at its rate.
func (d creditNoteDoc) taxes() []taxSubtotal {
	cn := d.in.CreditNote
	allowances := d.allowancesPerRate()
	bases := make([]money.Decimal, len(d.groups))
	for k, g := range d.groups {
		bases[k] = money.Zero
		for _, i := range g.members {
			bases[k] = bases[k].Add(d.in.Lines[i].Item.PreciseAmount)
		}
		bases[k] = bases[k].Sub(allowances[k])
	}
	var amounts []int64
	if d.in.ProviderTaxes {
		booked := make([]money.Decimal, len(d.groups))
		precise := make([]money.Decimal, len(d.groups))
		for k, g := range d.groups {
			booked[k], precise[k] = money.Zero, money.Zero
			for _, i := range g.members {
				l := &d.in.Lines[i]
				booked[k] = booked[k].Add(prorated(l, money.NewFromInt(l.Fee.TaxesAmount)))
				precise[k] = precise[k].Add(prorated(l, l.Fee.TaxesPreciseAmount))
			}
		}
		amounts = allocateByGroup(cn.TaxesAmount, booked, precise)
	} else {
		weights := make([]money.Decimal, len(d.groups))
		for k, g := range d.groups {
			weights[k] = bases[k].Mul(g.rate).Div(money.NewFromInt(100))
		}
		amounts = allocate(cn.TaxesAmount, weights)
	}
	out := make([]taxSubtotal, len(d.groups))
	for k, g := range d.groups {
		first := &d.in.Lines[g.members[0]].Fee
		out[k] = taxSubtotal{
			category: taxCategory(first.Type == core.FeeTypeCredit, g.rate),
			rate:     g.rate,
			basis:    roundedUnits(bases[k], cn.Currency),
			tax:      units(amounts[k], cn.Currency),
		}
	}
	return out
}
