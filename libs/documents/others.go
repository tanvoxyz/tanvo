package documents

import (
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// creditNoteLayout is templates/credit_notes/credit_note and self_billed.
func (w *writer) creditNoteLayout(d *CreditNote) *layout {
	cn := &d.CreditNote
	self := d.Invoice.SelfBilled
	l := w.newLayout(w.l.T("credit_note.document_name"), &d.Party, !self)
	l.Info = append(l.Info,
		[2]string{w.l.T("credit_note.credit_note_number"), cn.Number},
		[2]string{w.l.T("credit_note.invoice_number"), d.Invoice.Number})
	if po := d.Invoice.PurchaseOrderNumber; po != nil && *po != "" {
		l.Info = append(l.Info, [2]string{w.l.T("credit_note.purchase_order_number"), *po})
	}
	l.Info = append(l.Info, [2]string{w.l.T("credit_note.issue_date"), w.date(cn.IssuingDate)})
	l.Meta = append(l.Meta, metadataRows(&d.Customer)...)
	from := w.billingEntityLines(&d.BillingEntity)
	to := w.customerLines(&d.Customer, false, true)
	if self {
		from, to = to, from
	}
	l.Parties = []party{{Title: w.l.T("credit_note.credit_from"), Lines: from}, {Title: w.l.T("credit_note.credit_to"), Lines: to}}
	l.Amount = w.money(cn.TotalAmount)
	issued := i18n.A("issuing_date", w.date(cn.IssuingDate))
	n := 0
	for _, b := range []bool{cn.Credited(), cn.Refunded(), cn.HasOffset()} {
		if b {
			n++
		}
	}
	switch {
	case n > 1:
		l.AmountNote = w.l.T("credit_note.issued_notice", issued)
	case cn.Credited():
		l.AmountNote = w.l.T("credit_note.credited_notice", issued)
	case cn.Refunded():
		l.AmountNote = w.l.T("credit_note.refunded_notice", issued)
	case cn.HasOffset():
		l.AmountNote = w.l.T("credit_note.offset_invoice_notice", i18n.A("invoice_number", d.Invoice.Number), issued)
	}
	l.Blocks = append(l.Blocks, w.creditNoteDetails(d)...)
	var codes []string
	for _, t := range cn.AppliedTaxes {
		codes = append(codes, t.TaxCode)
	}
	if lines := w.euNotices(d.BillingEntity.EUTaxManagement, d.BillingEntity.Address.Country, codes); len(lines) > 0 {
		l.Blocks = append(l.Blocks, block{Kind: blockText, Style: "body3", Lines: []string{strings.Join(lines, "")}})
	}
	text := breakLines(present(d.BillingEntity.InvoiceFooter))
	if self {
		text = breakLines(w.l.T("credit_note.self_billed.footer"))
	}
	w.footer(l, cn.Number, text)
	return l
}

type creditItem struct {
	item creditnote.Item
	fee  *Fee
}

// creditNoteDetails is templates/credit_notes/_details.
func (w *writer) creditNoteDetails(d *CreditNote) []block {
	cn := &d.CreditNote
	forCredit := d.Invoice.Type == invoice.TypeCredit
	fees := map[core.FeeID]*Fee{}
	for i := range d.Fees {
		fees[d.Fees[i].Fee.ID] = &d.Fees[i]
	}
	for i := range d.Subscriptions {
		w.subs[d.Subscriptions[i].Subscription.ID] = &d.Subscriptions[i]
	}
	var items []creditItem
	for _, it := range cn.Items {
		if f := fees[it.FeeID]; f != nil {
			items = append(items, creditItem{it, f})
		}
	}
	itemFor := func(id core.FeeID) *creditItem {
		for i := range items {
			if items[i].fee.Fee.ID == id {
				return &items[i]
			}
		}
		return nil
	}
	var rows []row
	var cols []column
	add := func(name string, it *creditItem) {
		rows = append(rows, row{Style: rowFee, Cells: []cell{bold(name), feeTaxes(&it.fee.Fee), txt(w.money(it.item.Amount))}})
	}
	trueUp := func(f *Fee) {
		if f.TrueUpFee == nil {
			return
		}
		if t := itemFor(f.TrueUpFee.ID); t != nil {
			add(w.l.T("invoice.true_up_metric", i18n.A("metric", w.invoiceName(t.fee))), t)
		}
	}
	if forCredit {
		cols = twoColumns()
		rows = append(rows,
			row{Style: rowHead, Cells: []cell{txt(w.l.T("credit_note.item")), txt(w.l.T("credit_note.amount"))}},
			row{Style: rowFee, Cells: []cell{bold(w.l.T("credit_note.prepaid_credits_for_wallet", i18n.A("wallet_name", d.WalletName))), txt(w.money(cn.RefundAmount))}})
	} else {
		cols = threeColumns()
		rows = append(rows, row{Style: rowHead, Cells: []cell{txt(w.l.T("credit_note.item")), txt(w.l.T("credit_note.tax_rate")), txt(w.l.T("credit_note.amount"))}})
		var subIDs []core.SubscriptionID
		for _, it := range items {
			if !slices.Contains(subIDs, it.fee.Fee.SubscriptionID) {
				subIDs = append(subIDs, it.fee.Fee.SubscriptionID)
			}
		}
		parents := map[core.FeeID]*Fee{}
		for _, f := range fees {
			if f.TrueUpFee != nil {
				parents[f.TrueUpFee.ID] = f
			}
		}
		for _, sid := range subIDs {
			var subItems []creditItem
			for _, it := range items {
				if it.fee.Fee.SubscriptionID == sid {
					subItems = append(subItems, it)
				}
			}
			if sid.IsZero() {
				for i := range subItems {
					it := &subItems[i]
					name := w.invoiceName(it.fee)
					if p := parents[it.fee.Fee.ID]; p != nil || !it.fee.Fee.TrueUpParentFeeID.IsZero() {
						parentName := ""
						if p != nil {
							parentName = w.invoiceName(p)
						}
						name = w.l.T("invoice.true_up_metric", i18n.A("metric", parentName))
					}
					add(name, it)
				}
				continue
			}
			for i := range subItems {
				it := &subItems[i]
				if it.fee.Fee.Type == core.FeeTypeSubscription && it.item.Amount > 0 {
					name := w.l.T("credit_note.subscription")
					if s := w.subs[sid]; s != nil {
						name += " - " + s.InvoiceName()
					}
					add(name, it)
				}
			}
			for i := range subItems {
				if it := &subItems[i]; it.fee.Fee.Type == core.FeeTypeFixedCharge {
					add(w.invoiceName(it.fee)+groupedByDisplay(it.fee), it)
				}
			}
			var charges []*Fee
			chargeItems := map[core.FeeID]*creditItem{}
			for i := range subItems {
				it := &subItems[i]
				if it.fee.Fee.Type == core.FeeTypeCharge && it.fee.Fee.TrueUpParentFeeID.IsZero() {
					charges = append(charges, it.fee)
					chargeItems[it.fee.Fee.ID] = it
				}
			}
			for _, group := range groupByCharge(charges) {
				allFiltered := true
				for _, f := range group {
					allFiltered = allFiltered && !f.Fee.ChargeFilterID.IsZero()
				}
				if allFiltered {
					for _, f := range group {
						add(w.invoiceName(f)+groupedByDisplay(f)+" • "+filterDisplay(f, " • "), chargeItems[f.Fee.ID])
					}
					for _, f := range group {
						trueUp(f)
					}
					continue
				}
				f := group[0]
				add(w.invoiceName(f)+groupedByDisplay(f), chargeItems[f.Fee.ID])
				trueUp(f)
			}
		}
	}
	t := &totals{}
	tr := func(label, value string) { t.rows = append(t.rows, totalRow{Label: label, Value: value}) }
	if cn.CouponsAdjustmentAmount > 0 {
		tr(w.l.T("credit_note.coupon_adjustment"), w.negMoney(cn.CouponsAdjustmentAmount))
	}
	if !forCredit {
		tr(w.l.T("credit_note.sub_total_without_tax"), w.money(cn.SubTotalExcludingTaxesAmount))
	}
	if len(cn.AppliedTaxes) > 0 {
		taxes := slices.Clone(cn.AppliedTaxes)
		slices.SortStableFunc(taxes, func(a, b creditnote.AppliedTax) int { return b.TaxRate.Cmp(a.TaxRate) })
		for _, at := range taxes {
			tr(w.l.T("credit_note.tax", i18n.A("name", at.TaxName), i18n.A("rate", i18n.RubyFloat(at.TaxRate)),
				i18n.A("amount", w.money(at.BaseAmount))), w.money(at.Amount))
		}
	} else if !forCredit {
		tr(w.l.T("invoice.tax_name_with_details", i18n.A("name", "Tax"), i18n.A("rate", "0")), w.money(0))
	}
	if cn.HasOffset() {
		tr(w.l.T("credit_note.offset_invoice", i18n.A("invoice_number", d.Invoice.Number)), w.money(cn.OffsetAmount))
	}
	if cn.Credited() {
		tr(w.l.T("credit_note.credited_on_customer_balance"), w.money(cn.CreditAmount))
	}
	if cn.Refunded() {
		tr(w.l.T("credit_note.refunded"), w.money(cn.RefundAmount))
	}
	t.rows = append(t.rows, totalRow{Label: w.l.T("credit_note.total"), Value: w.money(cn.TotalAmount), Strong: true})
	return []block{{Kind: blockTable, Style: "resume", Columns: cols, Rows: rows}, t.block()}
}

// receiptLayout is templates/payment_receipts/v1.
func (w *writer) receiptLayout(d *PaymentReceipt) *layout {
	p := &d.Payment
	l := w.newLayout(w.l.T("payment_receipt.document_name"), &d.Party, true)
	l.Info = append(l.Info, [2]string{w.l.T("payment_receipt.number"), d.Receipt.Number})
	if d.Invoice != nil {
		l.Info = append(l.Info, [2]string{w.l.T("invoice.invoice_number"), d.Invoice.Invoice.Number})
	}
	if m := p.ProviderPaymentMethod.DisplayName(); m != "" {
		l.Info = append(l.Info, [2]string{w.l.T("payment_receipt.payment_method"), m})
	}
	paid := w.date(w.utcDate(p.CreatedAt))
	l.Info = append(l.Info, [2]string{w.l.T("payment_receipt.payment_date"), paid})
	l.Meta = append(l.Meta, metadataRows(&d.Customer)...)
	l.Parties = []party{
		{Title: w.l.T("invoice.bill_from"), Lines: w.billingEntityLines(&d.BillingEntity)},
		{Title: w.l.T("invoice.bill_to"), Lines: w.customerLines(&d.Customer, false, false)},
	}
	l.Amount = w.l.Money(p.Amount, currencyOr(p.Currency, w.cur))
	var due int64
	switch {
	case d.Invoice != nil:
		due = d.Invoice.Invoice.TotalDueAmount()
	case d.Request != nil:
		due = d.Request.Request.Amount - p.Amount
	}
	l.AmountNote = w.l.T("payment_receipt.paid_on", i18n.A("date", paid), i18n.A("total_due_amount", w.money(due)))
	footerText := breakLines(present(d.BillingEntity.InvoiceFooter))
	switch {
	case d.Invoice != nil:
		inv := d.Invoice
		v := &invoiceView{writer: w, d: inv, inv: &inv.Invoice}
		for i := range inv.Subscriptions {
			s := &inv.Subscriptions[i]
			v.subs = append(v.subs, s)
			w.subs[s.Subscription.ID] = s
		}
		for i := range inv.Fees {
			v.fees = append(v.fees, &inv.Fees[i])
		}
		switch inv.Invoice.Type {
		case invoice.TypeCredit:
			l.Blocks = append(l.Blocks, v.credit()...)
		case invoice.TypeOneOff:
			l.Blocks = append(l.Blocks, v.oneOff()...)
		case invoice.TypeAdvanceCharges:
			l.Blocks = append(l.Blocks, v.charges()...)
		case invoice.TypeProgressiveBilling:
			l.Blocks = append(l.Blocks, v.progressive()...)
		default:
			if len(v.subs) == 1 {
				l.Blocks = append(l.Blocks, v.subscriptionDetails(false, false)...)
			} else {
				l.Blocks = append(l.Blocks, v.subscriptionsSummary()...)
			}
		}
		v.euNotice(l)
		if inv.UsageThreshold != nil && inv.Invoice.Type == invoice.TypeProgressiveBilling {
			t := inv.UsageThreshold
			l.Blocks = append(l.Blocks, block{Kind: blockText, Style: "body3", Lines: []string{w.l.T("invoice.reached_usage_threshold",
				i18n.A("usage_amount", w.money(t.LifetimeUsageAmount)), i18n.A("threshold_amount", w.money(t.PassedThresholdAmount)))}})
		}
		v.customSections(l)
		w.footer(l, d.Receipt.Number, footerText)
		if len(v.subs) > 1 {
			l.Blocks = append(l.Blocks, v.subscriptionDetails(true, false)...)
		}
	case d.Request != nil:
		l.Blocks = append(l.Blocks, w.requestedInvoices(d.Request.Invoices, "invoice.amount", func(inv *invoice.Invoice) int64 { return inv.TotalAmount }))
		w.footer(l, d.Receipt.Number, footerText)
	default:
		w.footer(l, d.Receipt.Number, footerText)
	}
	return l
}

func (w *writer) requestedInvoices(invs []invoice.Invoice, amountKey string, amount func(*invoice.Invoice) int64) block {
	rows := []row{{Style: rowHead, Cells: []cell{txt(w.l.T("invoice.invoice_number")), txt(w.l.T(amountKey))}}}
	for i := range invs {
		inv := &invs[i]
		rows = append(rows, row{Style: rowFee, Cells: []cell{bold(inv.Number), txt(w.l.Money(amount(inv), currencyOr(inv.Currency, w.cur)))}})
	}
	return block{Kind: blockTable, Style: "resume", Columns: twoColumns(), Rows: rows}
}

// paymentRequestLayout is Tanvo's payment request PDF: the overdue
// invoices a dunning campaign or a manual request asks the customer to pay,
// worded as lago-api's payment request email.
func (w *writer) paymentRequestLayout(d *PaymentRequest) *layout {
	r := &d.Request
	l := w.newLayout(w.l.T("payment_request.document_name"), &d.Party, true)
	l.Info = append(l.Info, [2]string{w.l.T("invoice.issue_date"), w.date(w.localDate(r.CreatedAt))})
	l.Meta = append(l.Meta, metadataRows(&d.Customer)...)
	l.Parties = []party{
		{Title: w.l.T("invoice.bill_from"), Lines: w.billingEntityLines(&d.BillingEntity)},
		{Title: w.l.T("invoice.bill_to"), Lines: w.customerLines(&d.Customer, true, false)},
	}
	l.Amount = w.money(r.Amount)
	l.AmountNote = w.l.T("email.payment_request.requested.total_amount_due", i18n.A("amount", w.money(r.Amount)))
	name := present(d.BillingEntity.LegalName)
	if name == "" {
		name = d.BillingEntity.Name
	}
	l.Blocks = append(l.Blocks,
		block{Kind: blockText, Style: "body1", Lines: []string{w.l.T("email.payment_request.requested.reminder_overdue_balance", i18n.A("billing_entity_name", name))}},
		w.requestedInvoices(d.Invoices, "invoice.total_due_amount", func(inv *invoice.Invoice) int64 { return inv.TotalDueAmount() }),
		block{Kind: blockText, Style: "body3", Lines: []string{w.l.T("email.payment_request.requested.already_paid")}},
	)
	w.footer(l, "", breakLines(present(d.BillingEntity.InvoiceFooter)))
	return l
}
