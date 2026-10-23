package einvoice

import (
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// PaymentCII renders the receipt of in as a UN/CEFACT Cross Industry
// Invoice of type payment receipt, with one line for the payment.
func PaymentCII(in *Payment) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newPaymentDoc(in)
	p, cur := in.Payment, in.Payment.Currency
	amount := units(p.Amount, cur)
	created := utcDate(p.CreatedAt)

	root := ciiDocument()
	ciiHeader(root, in.Receipt.Number, typePaymentReceipt, utcDate(in.Receipt.CreatedAt), []string{d.note()})
	root.note("Supply Chain Trade Transaction")
	root.with("rsm:SupplyChainTradeTransaction", func(n *node) {
		ciiLineItem(n, ciiLine{
			lineID:         1,
			name:           "Payment Received",
			description:    "Payment received via " + d.mode() + " for invoice " + d.invoiceNumbers(),
			chargeAmount:   amountText(amount, cur),
			billedQuantity: "1",
			category:       categoryZero,
			ratePercent:    "0.0",
			lineTotal:      amount,
		})
		ciiTradeAgreement(n, d.parties, true, "")
		ciiTradeDelivery(n, created)
		ciiTradeSettlement(n, cur, func(n *node) {
			switch p.Kind {
			case payment.KindManual:
				ciiPaymentMeans(n, d.settlement, paymentStandard, amount)
			case payment.KindProvider:
				ciiPaymentMeans(n, d.settlement, paymentCreditCard, amount)
			}
			ciiTradeTax(n, taxSubtotal{category: categoryZero, rate: money.Zero, basis: amount, tax: money.Zero})
			ciiPaymentTerms(n, created, titleize(d.method())+" payment received on  "+timeText(p.CreatedAt))
			ciiMonetarySummation(n, cur, ciiAmounts{
				lineTotal:  amount,
				charges:    money.Zero,
				allowances: money.Zero,
				taxBasis:   amount,
				tax:        money.Zero,
				grandTotal: amount,
				prepaid:    &amount,
				duePayable: money.Zero,
			})
			ciiInvoiceReference(n, d.invoiceNumbers())
		})
	})
	return root.document(), nil
}

// PaymentUBL renders the receipt of in as a UBL 2.1 ApplicationResponse
// with a paid response for each settled invoice.
func PaymentUBL(in *Payment) ([]byte, error) {
	if err := in.check(); err != nil {
		return nil, err
	}
	d := newPaymentDoc(in)
	p := in.Payment
	created := utcDate(p.CreatedAt)
	note := d.note()

	root := newRoot("ApplicationResponse", ublNamespaces(ublNSApplication)...)
	root.note("UBL Version and Customization")
	root.leaf("cbc:UBLVersionID", ublVersion)
	root.leaf("cbc:CustomizationID", receiptCustomizer)
	root.leaf("cbc:ProfileID", receiptProfile)
	root.leaf("cbc:ID", in.Receipt.Number)
	root.leaf("cbc:IssueDate", ublDate(created))
	root.leaf("cbc:Note", note)
	ublSenderParty(root, in.BillingEntity)
	ublReceiverParty(root, in.Customer)
	for _, inv := range in.Invoices {
		ublDocumentResponse(root,
			ublResponse{code: responsePaid, description: note, date: created},
			ublDocumentRef{
				id:          inv.Number,
				issued:      inv.IssuingDate,
				typeCode:    invoiceTypeCode(inv),
				kind:        "Invoice",
				description: "Invoice ID from payment system: " + inv.ID.String(),
			})
	}
	return root.document(), nil
}

// paymentDoc computes what both syntaxes print about a payment.
type paymentDoc struct {
	in         *Payment
	parties    parties
	settlement settlement
}

func newPaymentDoc(in *Payment) paymentDoc {
	return paymentDoc{
		in:      in,
		parties: parties{in.BillingEntity, in.Customer},
		settlement: settlement{
			labels:     in.Labels.orEnglish(),
			currency:   in.Payment.Currency,
			createdAt:  in.Payment.CreatedAt,
			cardDigits: in.Payment.ProviderPaymentMethod.CardLastDigits(),
		},
	}
}

// note is "Receipt for payment of EUR 100.00 received via Manual for
// invoice INV-001".
func (d paymentDoc) note() string {
	p := d.in.Payment
	return "Receipt for payment of " + p.Currency.Code() + " " + amountText(units(p.Amount, p.Currency), p.Currency) +
		" received via " + d.mode() + " for invoice " + d.invoiceNumbers()
}

// mode is "Manual" or the payment provider.
func (d paymentDoc) mode() string {
	if d.in.Payment.Kind == payment.KindManual {
		return "Manual"
	}
	return d.in.ProviderType
}

// method is the payment method: "manual", "provider" when the provider gave
// no detail, or the provider's method type.
func (d paymentDoc) method() string {
	p := d.in.Payment
	switch {
	case p.Kind == payment.KindManual:
		return "manual"
	case p.ProviderPaymentMethod.IsZero():
		return "provider"
	default:
		return p.ProviderPaymentMethod.Type
	}
}

// invoiceNumbers are the settled invoices' numbers as a sentence.
func (d paymentDoc) invoiceNumbers() string {
	numbers := make([]string, len(d.in.Invoices))
	for i, inv := range d.in.Invoices {
		numbers[i] = inv.Number
	}
	return sentence(numbers, d.settlement.labels)
}
