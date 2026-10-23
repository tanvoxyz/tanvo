package documents

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// InvoiceTemplate is the layout an invoice is printed with (lago-api's v4
// templates).
type InvoiceTemplate string

// Invoice templates.
const (
	// TemplateInvoice is the general template (v4): subscription, credit
	// and progressive billing invoices.
	TemplateInvoice InvoiceTemplate = "invoice"
	// TemplateSelfBilled is the self-billing invoice of a partner.
	TemplateSelfBilled InvoiceTemplate = "self_billed"
	// TemplateOneOff is the one-off (add-on) invoice.
	TemplateOneOff InvoiceTemplate = "one_off"
	// TemplateCharge is the invoice of pay-in-advance charges.
	TemplateCharge InvoiceTemplate = "charge"
	// TemplateFixedCharge is the invoice of pay-in-advance fixed charges.
	TemplateFixedCharge InvoiceTemplate = "fixed_charge"
)

// Template returns the template d is printed with, chosen as lago-api's
// Invoices::GeneratePdfService does.
func (d *Invoice) Template() InvoiceTemplate {
	switch {
	case d.Invoice.SelfBilled:
		return TemplateSelfBilled
	case d.Invoice.Type == invoice.TypeOneOff:
		return TemplateOneOff
	case d.allFees(func(f *invoice.Fee) bool { return f.PayInAdvance && f.Type == core.FeeTypeCharge }):
		return TemplateCharge
	case d.allFees(func(f *invoice.Fee) bool { return f.PayInAdvance && f.Type == core.FeeTypeFixedCharge }):
		return TemplateFixedCharge
	}
	return TemplateInvoice
}

func (d *Invoice) allFees(pred func(*invoice.Fee) bool) bool {
	if len(d.Fees) == 0 {
		return false
	}
	for i := range d.Fees {
		if !pred(&d.Fees[i].Fee) {
			return false
		}
	}
	return true
}

// taxInvoiceCountries print "Tax invoice" (Invoice::TAX_INVOICE_LABEL_COUNTRIES).
var taxInvoiceCountries = []string{"AU", "AE", "NZ", "ID", "SG"}

// wholeInvoiceTaxCodes are the provider tax codes printed without amount
// (Invoice::AppliedTax::TAX_CODES_APPLICABLE_ON_WHOLE_INVOICE).
var wholeInvoiceTaxCodes = []string{
	"not_collecting", "juris_not_taxed", "reverse_charge", "customer_exempt",
	"transaction_exempt", "juris_has_no_tax", "unknown_taxation",
}

// invoiceView holds an invoice being laid out.
type invoiceView struct {
	*writer
	d    *Invoice
	inv  *invoice.Invoice
	fees []*Fee
	subs []*Subscription
}

func (w *writer) invoiceLayout(d *Invoice) *layout {
	v := &invoiceView{writer: w, d: d, inv: &d.Invoice}
	for i := range d.Subscriptions {
		s := &d.Subscriptions[i]
		v.subs = append(v.subs, s)
		w.subs[s.Subscription.ID] = s
	}
	slices.SortStableFunc(v.subs, func(a, b *Subscription) int {
		return cmp.Compare(strings.ToLower(a.InvoiceName()), strings.ToLower(b.InvoiceName()))
	})
	for i := range d.Fees {
		v.fees = append(v.fees, &d.Fees[i])
	}
	tmpl := d.Template()
	l := w.newLayout(v.documentName(), &d.Party, tmpl != TemplateSelfBilled)
	l.Info = append(l.Info, [2]string{w.l.T("invoice.invoice_number"), v.inv.Number})
	if d.PurchaseOrderNumber != "" {
		l.Info = append(l.Info, [2]string{w.l.T("invoice.purchase_order_number"), d.PurchaseOrderNumber})
	}
	l.Info = append(l.Info,
		[2]string{w.l.T("invoice.issue_date"), w.date(v.inv.IssuingDate)},
		[2]string{w.l.T("invoice.payment_term"), w.paymentTermText(d)})
	l.Meta = append(l.Meta, metadataRows(&d.Customer)...)

	rawEmail := tmpl == TemplateOneOff || tmpl == TemplateCharge || tmpl == TemplateFixedCharge
	from := party{Title: w.l.T("invoice.bill_from"), Lines: w.billingEntityLines(&d.BillingEntity)}
	to := party{Title: w.l.T("invoice.bill_to"), Lines: w.customerLines(&d.Customer, true, rawEmail)}
	if tmpl == TemplateSelfBilled {
		from.Lines, to.Lines = to.Lines, from.Lines
	}
	l.Parties = []party{from, to}

	l.Amount = w.money(v.inv.TotalAmount)
	if v.inv.Type == invoice.TypeAdvanceCharges && (tmpl == TemplateCharge || tmpl == TemplateFixedCharge) {
		l.AmountNote = w.l.T("invoice.already_paid")
	} else {
		l.AmountNote = w.l.T("invoice.due_date", i18n.A("date", w.date(v.inv.PaymentDueDate)))
	}

	switch tmpl {
	case TemplateOneOff:
		l.Blocks = append(l.Blocks, v.oneOff()...)
		v.trailer(l, false, false)
	case TemplateCharge:
		if len(v.subs) > 0 {
			l.Blocks = append(l.Blocks, v.subscriptionName(v.subs[0], false))
		}
		l.Blocks = append(l.Blocks, v.charges()...)
		v.euNotice(l)
		v.customSections(l)
		l.Blocks = append(l.Blocks, v.presentationBreakdowns(v.fees)...)
		w.footer(l, v.inv.Number, breakLines(present(d.BillingEntity.InvoiceFooter)))
	case TemplateFixedCharge:
		l.Blocks = append(l.Blocks, v.fixedCharges()...)
		v.euNotice(l)
		v.customSections(l)
		w.footer(l, v.inv.Number, breakLines(present(d.BillingEntity.InvoiceFooter)))
	default:
		self := tmpl == TemplateSelfBilled
		switch {
		case self && v.inv.Type == invoice.TypeOneOff:
			l.Blocks = append(l.Blocks, v.oneOff()...)
		case v.inv.Type == invoice.TypeCredit:
			l.Blocks = append(l.Blocks, v.credit()...)
		case v.inv.Type == invoice.TypeProgressiveBilling:
			l.Blocks = append(l.Blocks, v.progressive()...)
		case len(v.subs) == 1:
			l.Blocks = append(l.Blocks, v.subscriptionDetails(false, true)...)
		default:
			l.Blocks = append(l.Blocks, v.subscriptionsSummary()...)
		}
		v.trailer(l, true, self)
		if len(v.subs) > 1 {
			l.Blocks = append(l.Blocks, v.subscriptionDetails(true, true)...)
		}
	}
	return l
}

// trailer adds the notices, sections and footer after an invoice's body.
func (v *invoiceView) trailer(l *layout, threshold, selfBilled bool) {
	v.euNotice(l)
	if threshold && v.d.UsageThreshold != nil && v.inv.Type == invoice.TypeProgressiveBilling {
		t := v.d.UsageThreshold
		l.Blocks = append(l.Blocks, block{Kind: blockText, Style: "body3", Lines: []string{v.l.T("invoice.reached_usage_threshold",
			i18n.A("usage_amount", v.money(t.LifetimeUsageAmount)), i18n.A("threshold_amount", v.money(t.PassedThresholdAmount)))}})
	}
	v.customSections(l)
	text := breakLines(present(v.d.BillingEntity.InvoiceFooter))
	if selfBilled {
		text = breakLines(v.l.T("invoice.self_billed.footer"))
	}
	v.footer(l, v.inv.Number, text)
}

func (v *invoiceView) documentName() string {
	inv := v.inv
	switch {
	case inv.SelfBilled:
		return v.l.T("invoice.self_billed.document_name")
	case inv.Type == invoice.TypeCredit:
		return v.l.T("invoice.prepaid_credit_invoice")
	case slices.Contains(taxInvoiceCountries, strings.ToUpper(v.d.BillingEntity.Address.Country)):
		if inv.Type == invoice.TypeAdvanceCharges {
			return v.l.T("invoice.paid_tax_invoice")
		}
		return v.l.T("invoice.document_tax_name")
	case inv.Type == invoice.TypeAdvanceCharges:
		return v.l.T("invoice.paid_invoice")
	}
	return v.l.T("invoice.document_name")
}

// euNotice is _eu_tax_management.
func (w *writer) euNotices(be bool, country string, taxes []string) []string {
	if !be || len(taxes) == 0 {
		return nil
	}
	var out []string
	if slices.Contains(taxes, catalog.EUTaxExemptCode) {
		if strings.EqualFold(country, "FR") {
			out = append(out, w.l.T("invoice.taxes.fr_tax_exempt"))
		} else {
			out = append(out, w.l.T("invoice.taxes.tax_exempt"))
		}
	}
	if slices.Contains(taxes, catalog.EUReverseChargeTaxCode) {
		out = append(out, w.l.T("invoice.taxes.reverse_charge"))
	}
	return out
}

func (v *invoiceView) euNotice(l *layout) {
	var codes []string
	for _, t := range v.d.AppliedTaxes {
		codes = append(codes, t.TaxCode)
	}
	if lines := v.euNotices(v.d.BillingEntity.EUTaxManagement, v.d.BillingEntity.Address.Country, codes); len(lines) > 0 {
		l.Blocks = append(l.Blocks, block{Kind: blockText, Style: "body3", Lines: []string{strings.Join(lines, "")}})
	}
}

// customSections is _custom_sections.
func (v *invoiceView) customSections(l *layout) {
	if len(v.d.CustomSections) == 0 {
		return
	}
	b := block{Kind: blockSections}
	for _, s := range v.d.CustomSections {
		title := present(s.DisplayName)
		if title == "" {
			title = s.Name
		}
		b.Sections = append(b.Sections, section{Title: title, Lines: breakLines(present(s.Details))})
	}
	l.Blocks = append(l.Blocks, b)
}

func (v *invoiceView) subscriptionName(s *Subscription, pageBreak bool) block {
	return block{Kind: blockHeading, Text: v.l.T("invoice.details", i18n.A("resource", s.InvoiceName())), Break: pageBreak}
}

func tableBlock(cols []column, rows []row, gap bool) block {
	return block{Kind: blockTable, Style: "resume", Columns: cols, Rows: rows, Gap: gap}
}

// --- totals ---------------------------------------------------------------

type totals struct {
	v    *invoiceView
	rows []totalRow
}

func (t *totals) add(label, value string) {
	t.rows = append(t.rows, totalRow{Label: label, Value: value})
}

func (t *totals) neg(label string, amount int64) { t.add(label, t.v.negMoney(amount)) }

func (t *totals) strong(label, value string) {
	t.rows = append(t.rows, totalRow{Label: label, Value: value, Strong: true})
}

func (t *totals) block() block { return block{Kind: blockTotals, Totals: t.rows} }

func (t *totals) progressiveCredit() {
	if t.v.inv.ProgressiveBillingCreditAmount > 0 {
		t.neg(t.v.l.T("invoice.progressive_billing_credit"), t.v.inv.ProgressiveBillingCreditAmount)
	}
}

func (t *totals) coupons() {
	if t.v.inv.CouponsAmount <= 0 {
		return
	}
	for _, c := range t.v.d.Coupons {
		t.neg(t.v.couponName(c), c.Amount)
	}
}

// couponName is Credit#invoice_coupon_display_name.
func (w *writer) couponName(c CouponCredit) string {
	var suffix string
	if c.PercentageRate != nil {
		suffix = c.PercentageRate.StringFixed(2) + "%"
	} else {
		suffix = w.l.Money(c.FixedAmount, currencyOr(c.Currency, w.cur))
	}
	return c.CouponName + " (" + suffix + ")"
}

// taxes are the sub totals and tax lines common to every invoice.
func (t *totals) taxes() {
	v := t.v
	t.add(v.l.T("invoice.sub_total_without_tax"), v.money(v.inv.SubTotalExcludingTaxesAmount))
	if len(v.d.AppliedTaxes) == 0 {
		t.add(v.l.T("invoice.tax_name_with_details", i18n.A("name", "Tax"), i18n.A("rate", "0")), v.money(0))
	} else {
		taxes := slices.Clone(v.d.AppliedTaxes)
		slices.SortStableFunc(taxes, func(a, b invoice.AppliedTax) int { return b.TaxRate.Cmp(a.TaxRate) })
		for _, at := range taxes {
			if slices.Contains(wholeInvoiceTaxCodes, at.TaxCode) {
				t.add(v.l.T("invoice.tax_name_only."+at.TaxCode), "")
				continue
			}
			t.add(v.l.T("invoice.tax_name", i18n.A("name", at.TaxName), i18n.A("rate", i18n.RubyFloat(at.TaxRate)),
				i18n.A("amount", v.money(at.FeesAmount))), v.money(at.Amount))
		}
	}
	t.add(v.l.T("invoice.sub_total_with_tax"), v.money(v.inv.SubTotalIncludingTaxesAmount))
}

func (t *totals) creditNotes() {
	if t.v.inv.CreditNotesAmount > 0 {
		t.neg(t.v.l.T("invoice.credit_notes"), t.v.inv.CreditNotesAmount)
	}
}

// prepaid is _prepaid_credits.
func (t *totals) prepaid() {
	v := t.v
	inv := v.inv
	if inv.Type != invoice.TypeSubscription && inv.Type != invoice.TypeProgressiveBilling || !v.d.HasWalletTransactions {
		return
	}
	if inv.PrepaidGrantedCreditAmount > 0 || inv.PrepaidPurchasedCreditAmount > 0 {
		if inv.PrepaidGrantedCreditAmount > 0 {
			t.neg(v.l.T("invoice.free_credits"), inv.PrepaidGrantedCreditAmount)
		}
		if inv.PrepaidPurchasedCreditAmount > 0 {
			t.neg(v.l.T("invoice.prepaid_credits"), inv.PrepaidPurchasedCreditAmount)
		}
		return
	}
	t.neg(v.l.T("invoice.prepaid_credits"), inv.PrepaidCreditAmount)
}

// full is the totals table of an invoice's body: credits, taxes, credit
// notes, prepaid credits and total (label totalKey).
func (v *invoiceView) full(progressive bool, totalKey string) block {
	t := &totals{v: v}
	if progressive {
		t.progressiveCredit()
	}
	t.coupons()
	t.taxes()
	t.creditNotes()
	t.prepaid()
	t.strong(v.l.T(totalKey), v.money(v.inv.TotalAmount))
	return t.block()
}

func (v *invoiceView) totalKey() string {
	if v.inv.Type == invoice.TypeAdvanceCharges {
		return "invoice.already_paid"
	}
	return "invoice.total"
}

// --- bodies -----------------------------------------------------------------

// oneOff is _one_off.
func (v *invoiceView) oneOff() []block {
	r := &feeRows{w: v.writer}
	r.head(v.l.T("invoice.item"), v.l.T("invoice.units"), v.l.T("invoice.unit_price"), v.l.T("invoice.tax_rate"), v.l.T("invoice.amount"))
	if v.inv.Type == invoice.TypeOneOff {
		fees := slices.Clone(v.fees)
		slices.SortStableFunc(fees, func(a, b *Fee) int { return a.Fee.Properties.From.Compare(b.Fee.Properties.From) })
		for _, f := range fees {
			sub := []string{present(f.Fee.Description)}
			if p := f.Fee.Properties; !p.From.IsZero() && !p.To.IsZero() {
				sub = append(sub, v.l.T("invoice.from_to_date", i18n.A("from_date", v.date(v.utcDate(p.From))), i18n.A("to_date", v.date(v.utcEndDate(p.To)))))
			}
			r.add(rowFee, bold(v.invoiceName(f), nonEmpty(sub)...), txt(units(f.Fee.Units)),
				txt(v.l.Money(f.Fee.UnitAmount, currencyOr(f.Fee.Currency, v.cur))), feeTaxes(&f.Fee), txt(v.feeAmount(&f.Fee)))
		}
	}
	t := &totals{v: v}
	t.taxes()
	t.strong(v.l.T("invoice.total"), v.money(v.inv.TotalAmount))
	return []block{tableBlock(fiveColumns(), r.rows, false), t.block()}
}

// utcDate is the UTC date of t, as lago-api takes it where it calls to_date
// on a time without converting it to the customer's zone (Time.zone is UTC:
// config/application.rb sets no time_zone): the one-off fee period
// (app/views/templates/invoices/v4/_one_off.slim:14 calls to_date on the
// stored ISO strings) and the billing-period grouping key
// (BillingPeriod#to_grouping_key, app/views/helpers/fee_boundaries_helper.rb:12).
// Billing period headers are in the customer's zone instead (localDate,
// FeeBoundariesHelper.format_billing_period, fee_boundaries_helper.rb:115).
func (w *writer) utcDate(t time.Time) core.Date {
	u := t.UTC()
	return core.Date{Year: u.Year(), Month: u.Month(), Day: u.Day()}
}

func (w *writer) utcEndDate(t time.Time) core.Date { return w.utcDate(t.Add(-time.Nanosecond)) }

// credit is _credit.
func (v *invoiceView) credit() []block {
	r := &feeRows{w: v.writer}
	r.head(v.l.T("invoice.item"), v.l.T("invoice.units"), v.l.T("invoice.unit_price"), v.l.T("invoice.amount"))
	if len(v.fees) > 0 {
		f := v.fees[0]
		name := v.l.T("invoice.prepaid_credits")
		var credits, rate string
		if wt := f.WalletTransaction; wt != nil {
			credits = i18n.RubyFloat(wt.CreditAmount) // BigDecimal#to_s
		}
		if f.Wallet != nil {
			rate = i18n.RubyFloat(f.Wallet.RateAmount)
		}
		switch {
		case f.WalletTransaction != nil && present(f.WalletTransaction.Name) != "":
			name = present(f.WalletTransaction.Name)
		case f.Wallet != nil && present(f.Wallet.Name) != "":
			name = v.l.T("invoice.prepaid_credits_with_value", i18n.A("wallet_name", present(f.Wallet.Name)))
		}
		r.add(rowFee, bold(name), txt(credits), txt(rate), txt(v.money(f.Fee.Amount)))
	}
	t := &totals{v: v}
	t.strong(v.l.T("invoice.total"), v.money(v.inv.TotalAmount))
	return []block{tableBlock(fourColumns(), r.rows, false), t.block()}
}

// subscriptionsSummary is _subscriptions_summary.
func (v *invoiceView) subscriptionsSummary() []block {
	r := &feeRows{w: v.writer}
	r.head(v.l.T("invoice.item"), v.l.T("invoice.amount"))
	for _, s := range v.subs {
		r.add(rowFee, bold(s.InvoiceName()), txt(v.subscriptionTotalMoney(s)))
		if c := v.commitmentFee(s); c != nil {
			r.add(rowFee, bold(v.invoiceName(c)), txt(v.money(c.Fee.Amount)))
		}
	}
	return []block{tableBlock(twoColumns(), r.rows, false), v.full(v.inv.Type != invoice.TypeCredit, "invoice.total")}
}

func (v *invoiceView) subscriptionFees(s *Subscription) []*Fee {
	var out []*Fee
	for _, f := range v.fees {
		if f.Fee.SubscriptionID == s.Subscription.ID {
			out = append(out, f)
		}
	}
	return out
}

func (v *invoiceView) sumAmount(s *Subscription, t core.FeeType) int64 {
	var n int64
	for _, f := range v.subscriptionFees(s) {
		if f.Fee.Type == t {
			n += f.Fee.Amount
		}
	}
	return n
}

// subscriptionTotal is InvoiceSubscription#total_amount.
func (v *invoiceView) subscriptionTotal(s *Subscription) int64 {
	return v.sumAmount(s, core.FeeTypeCharge) + v.sumAmount(s, core.FeeTypeSubscription) + v.sumAmount(s, core.FeeTypeFixedCharge)
}

// subscriptionTotalMoney prints the subscription total in the plan's
// currency, as lago-api monetizes it (total_amount_currency is
// plan.amount_currency, app/models/invoice_subscription.rb:16, :119).
func (v *invoiceView) subscriptionTotalMoney(s *Subscription) string {
	return v.l.Money(v.subscriptionTotal(s), currencyOr(s.Plan.Currency, v.cur))
}

func (v *invoiceView) commitmentFee(s *Subscription) *Fee {
	for _, f := range v.subscriptionFees(s) {
		if f.Fee.Type == core.FeeTypeCommitment {
			return f
		}
	}
	return nil
}

// shouldDisplaySubscriptionFee is FeeDisplayHelper.should_display_subscription_fee?.
func (v *invoiceView) shouldDisplaySubscriptionFee(s *Subscription) bool {
	if v.inv.Type == invoice.TypeProgressiveBilling {
		return false
	}
	if v.sumAmount(s, core.FeeTypeCharge) == 0 && v.sumAmount(s, core.FeeTypeFixedCharge) == 0 {
		return true
	}
	return v.sumAmount(s, core.FeeTypeSubscription) > 0
}

// billingPeriod is FeeBoundariesHelper::BillingPeriod.
type billingPeriod struct{ from, to time.Time }

func (p billingPeriod) compare(q billingPeriod) int {
	if c := p.from.Compare(q.from); c != 0 {
		return c
	}
	return p.to.Compare(q.to)
}

func pick(from, to time.Time, fallback billingPeriod) billingPeriod {
	if !from.IsZero() && !to.IsZero() {
		return billingPeriod{from, to}
	}
	return fallback
}

// billingPeriodFor is FeeBoundariesHelper.billing_period_for.
func billingPeriodFor(f *Fee, s *Subscription) billingPeriod {
	is := s.InvoiceSubscription
	p := f.Fee.Properties
	switch f.Fee.Type {
	case core.FeeTypeCharge:
		if c := f.Charge; c != nil && c.PayInAdvance && (!f.Fee.PayInAdvance || !c.Invoiceable) && !f.AdvanceInterval.From.IsZero() {
			return billingPeriod{f.AdvanceInterval.From, f.AdvanceInterval.To}
		}
		return pick(p.ChargesFrom, p.ChargesTo, billingPeriod{is.ChargesPeriod.From, is.ChargesPeriod.To})
	case core.FeeTypeFixedCharge:
		return pick(f.Period.From, f.Period.To, billingPeriod{is.FixedChargesPeriod.From, is.FixedChargesPeriod.To})
	case core.FeeTypeCommitment:
		return pick(p.From, p.To, pick(f.Period.From, f.Period.To, billingPeriod{is.Period.From, is.Period.To}))
	}
	return pick(p.From, p.To, billingPeriod{is.Period.From, is.Period.To})
}

type feeGroup struct {
	period       billingPeriod
	subscription *Fee
	fixedCharges []*Fee
	charges      []*Fee
	commitment   *Fee
}

func (g *feeGroup) any() bool {
	return g.subscription != nil || len(g.fixedCharges) > 0 || len(g.charges) > 0 || g.commitment != nil
}

// groupByPeriod is FeeBoundariesHelper.group_fees_by_billing_period.
func (v *invoiceView) groupByPeriod(fees []*Fee, s *Subscription) []*feeGroup {
	var groups []*feeGroup
	key := func(p billingPeriod) (core.Date, core.Date) { return v.utcDate(p.from), v.utcEndDate(p.to) }
	find := func(p billingPeriod) *feeGroup {
		kf, kt := key(p)
		for _, g := range groups {
			gf, gt := key(g.period)
			if gf == kf && gt == kt {
				return g
			}
		}
		g := &feeGroup{period: p}
		groups = append(groups, g)
		return g
	}
	for _, f := range fees {
		switch f.Fee.Type {
		case core.FeeTypeSubscription:
			find(billingPeriodFor(f, s)).subscription = f
		case core.FeeTypeFixedCharge:
			g := find(billingPeriodFor(f, s))
			g.fixedCharges = append(g.fixedCharges, f)
		case core.FeeTypeCharge:
			g := find(billingPeriodFor(f, s))
			g.charges = append(g.charges, f)
		case core.FeeTypeCommitment:
			find(billingPeriodFor(f, s)).commitment = f
		}
	}
	for _, g := range groups {
		v.sortAlphabetically(g.fixedCharges)
		v.sortAlphabetically(g.charges)
	}
	slices.SortStableFunc(groups, func(a, b *feeGroup) int { return a.period.compare(b.period) })
	return groups
}

// filteredFees is InvoiceSubscriptionsHelper's filtered_fees.
func (v *invoiceView) filteredFees(s *Subscription) []*Fee {
	all := v.subscriptionFees(s)
	trueUpParents := map[core.FeeID]bool{}
	for _, f := range all {
		if f.Fee.Type == core.FeeTypeCharge && !f.Fee.TrueUpParentFeeID.IsZero() {
			trueUpParents[f.Fee.TrueUpParentFeeID] = true
		}
	}
	var out []*Fee
	for _, f := range all {
		displayable := f.Fee.Units.IsPositive() || f.Fee.Amount > 0
		switch f.Fee.Type {
		case core.FeeTypeSubscription, core.FeeTypeCommitment:
			out = append(out, f)
		case core.FeeTypeFixedCharge:
			if displayable {
				out = append(out, f)
			}
		case core.FeeTypeCharge:
			if displayable && f.Fee.TrueUpParentFeeID.IsZero() || trueUpParents[f.Fee.ID] {
				out = append(out, f)
			}
		}
	}
	return out
}

func (v *invoiceView) periodHeader(r *feeRows, p billingPeriod) {
	r.fiveHead(v.l.T("invoice.fees_from_to_date",
		i18n.A("from_date", v.date(v.localDate(p.from))), i18n.A("to_date", v.date(v.endDate(p.to)))))
}

// subscriptionDetails is _subscription_details for every subscription (one
// on single-subscription invoices; the detail pages otherwise).
func (v *invoiceView) subscriptionDetails(pages, presentation bool) []block {
	var out []block
	for _, s := range v.subs {
		filtered := v.filteredFees(s)
		out = append(out, v.subscriptionName(s, len(v.subs) > 1))
		groups := v.groupByPeriod(filtered, s)
		gi := 0
		hasCharges := false
		for _, g := range groups {
			if !g.any() {
				continue
			}
			hasCharges = hasCharges || len(g.charges) > 0
			r := &feeRows{w: v.writer}
			v.periodHeader(r, g.period)
			if f := g.subscription; f != nil && v.shouldDisplaySubscriptionFee(s) {
				name := present(f.Fee.InvoiceDisplayName)
				if name == "" {
					name = v.l.T("invoice.subscription_interval",
						i18n.A("plan_interval", v.l.T("invoice."+s.Plan.Interval.String())), i18n.A("plan_name", s.Plan.InvoiceName()))
				}
				// subscription_fee.units: a Ruby BigDecimal ("1.0").
				r.add(rowFee, bold(name), txt(i18n.RubyFloat(f.Fee.Units)), txt(v.l.Money(f.Fee.UnitAmount, v.cur)), feeTaxes(&f.Fee), txt(v.money(f.Fee.Amount)))
			}
			for _, f := range g.fixedCharges {
				r.fixedChargeFee(f)
			}
			for _, fees := range groupByCharge(g.charges) {
				r.chargeFees(fees, false)
			}
			if f := g.commitment; f != nil {
				amount := v.money(f.Fee.Amount)
				r.add(rowFee, bold(v.invoiceName(f)), txt("1"), txt(amount), feeTaxes(&f.Fee), txt(amount))
			}
			out = append(out, tableBlock(fiveColumns(), r.rows, gi > 0))
			gi++
		}
		if !pages {
			out = append(out, v.full(v.inv.Type != invoice.TypeCredit, "invoice.total"))
		} else {
			t := &totals{v: v}
			if v.inv.ProgressiveBillingCreditAmount > 0 {
				if c, ok := v.d.ProgressiveBillingCredits[s.Subscription.ID]; ok {
					t.neg(v.l.T("invoice.progressive_billing_credit"), c)
				}
			}
			t.strong(v.l.T("invoice.total"), v.subscriptionTotalMoney(s))
			out = append(out, t.block())
		}
		if hasCharges {
			out = append(out, v.recurringBreakdowns(s)...)
		}
		if presentation {
			out = append(out, v.presentationBreakdowns(filtered)...)
		}
	}
	return out
}

// recurringBreakdowns is the recurring fees breakdown of _subscription_details.
func (v *invoiceView) recurringBreakdowns(s *Subscription) []block {
	var recurring []*Fee
	for _, f := range v.subscriptionFees(s) {
		if f.Fee.Type != core.FeeTypeCharge || f.Metric == nil || f.Charge == nil || !f.Metric.Recurring || f.Charge.PayInAdvance {
			continue
		}
		if a := f.Metric.AggregationType; a == catalog.AggregationTypeSum || a == catalog.AggregationTypeUniqueCount {
			recurring = append(recurring, f)
		}
	}
	var out []block
	for _, fees := range groupByCharge(recurring) {
		total := money.Zero
		allFiltered := true
		for _, f := range fees {
			total = total.Add(f.Fee.Units)
			allFiltered = allFiltered && !f.Fee.ChargeFilterID.IsZero()
		}
		if !total.IsPositive() {
			continue
		}
		out = append(out, block{Kind: blockHeading, Text: v.l.T("invoice.details", i18n.A("resource", s.InvoiceName())), Break: true})
		days := 30
		first := fees[0]
		if allFiltered {
			for _, f := range fees {
				if !f.Fee.Units.IsPositive() {
					continue
				}
				out = append(out,
					block{Kind: blockText, Style: "body3", Lines: []string{v.invoiceName(first)}},
					block{Kind: blockText, Style: "body1", Lines: []string{v.l.T("invoice.breakdown_of", i18n.A("fee_filter_display_name", filterDisplay(f, " • ")))}})
				out = append(out, v.breakdownTable([]*Fee{f}, &days, func(d core.Date) string { return v.date(d) }))
			}
		} else {
			out = append(out,
				block{Kind: blockText, Style: "body3", Lines: []string{v.invoiceName(first)}},
				block{Kind: blockText, Style: "body1", Lines: []string{v.l.T("invoice.breakdown")}})
			out = append(out, v.breakdownTable(fees, &days, func(d core.Date) string { return v.cat.Locale("en").Strftime(d, "%b %d, %Y") }))
		}
		notice := v.l.T("invoice.notice_full")
		if first.Charge.Prorated {
			notice = v.l.T("invoice.notice_prorated", i18n.A("days_in_month", fmt.Sprint(days)))
		}
		out = append(out, block{Kind: blockText, Style: "alert", Lines: []string{notice}})
	}
	return out
}

// breakdownAmount prints a breakdown amount as lago-api interpolates it
// ("+#{breakdown.amount}", app/views/templates/invoices/v4/_subscription_details.slim:161):
// unique count amounts are Integers (prorated_value.ceil,
// app/services/billable_metrics/breakdown/unique_count_service.rb:28), sum
// amounts BigDecimals, which ActiveSupport prints as "10.0"
// (app/services/billable_metrics/breakdown/sum_service.rb:38, :50).
func breakdownAmount(f *Fee, a money.Decimal) string {
	if f.Metric != nil && f.Metric.AggregationType == catalog.AggregationTypeUniqueCount {
		return a.Round(0, money.Ceiling).String()
	}
	return i18n.RubyFloat(a)
}

func (v *invoiceView) breakdownTable(fees []*Fee, days *int, date func(core.Date) string) block {
	var rows []row
	for _, f := range fees {
		item := v.itemName(f)
		for _, b := range f.RecurringBreakdown {
			*days = b.TotalDuration
			amount := breakdownAmount(f, b.Amount)
			var text string
			switch b.Action {
			case BreakdownAdd:
				text = "+" + amount + " " + item
			case BreakdownRemove:
				sign := "-"
				if b.Amount.IsNegative() {
					sign = ""
				}
				text = sign + amount + " " + item
			default:
				text = "+/-" + amount + " " + item
			}
			rows = append(rows, row{Style: rowPlain, Cells: []cell{
				{Text: []string{date(b.Date)}, Sub: []string{v.l.T("invoice.breakdown_for_days",
					i18n.A("breakdown_duration", fmt.Sprint(b.Duration)), i18n.A("breakdown_total_duration", fmt.Sprint(b.TotalDuration)))}, Bold: true},
				{Text: []string{text}, Bold: true},
			}})
		}
	}
	return block{Kind: blockTable, Style: "breakdown", Columns: []column{{20, "left"}, {80, "left"}}, Rows: rows}
}

// presentationKeys are the charge's presentation group keys displayed on
// invoices (Charge#presentation_group_keys_values_displayed_in_invoice).
func presentationKeys(c *catalog.Charge) []string {
	if c == nil || c.Properties.IsZero() {
		return nil
	}
	var p struct {
		Keys []struct {
			Value   *string `json:"value"`
			Options struct {
				DisplayInInvoice bool `json:"display_in_invoice"`
			} `json:"options"`
		} `json:"presentation_group_keys"`
	}
	if err := unmarshalJSON(c.Properties.Raw(), &p); err != nil {
		return nil
	}
	var out []string
	for _, k := range p.Keys {
		if k.Options.DisplayInInvoice && k.Value != nil {
			out = append(out, *k.Value)
		}
	}
	return out
}

type presentationRow struct {
	values []string // "" for a missing value
	units  money.Decimal
}

// presentationRows is FeeDisplayHelper.sorted_presentation_breakdowns_displayed_in_invoice.
func presentationRows(f *Fee, keys []string) []presentationRow {
	var rows []presentationRow
	index := map[string]int{}
	for _, b := range f.PresentationBreakdowns {
		var present []string
		vals := make([]string, len(keys))
		for i, k := range keys {
			if val, ok := b.PresentationBy[k]; ok {
				present = append(present, k+"\x00"+val)
				vals[i] = val
			}
		}
		if len(present) == 0 {
			continue
		}
		id := strings.Join(present, "\x01")
		if i, ok := index[id]; ok {
			rows[i].units = rows[i].units.Add(b.Units)
			continue
		}
		index[id] = len(rows)
		rows = append(rows, presentationRow{values: vals, units: b.Units})
	}
	var clean, blank []presentationRow
	for _, r := range rows {
		if !slices.Contains(r.values, "") {
			clean = append(clean, r)
		} else {
			blank = append(blank, r)
		}
	}
	byValues := func(a, b presentationRow) int { return slices.Compare(nonEmpty(a.values), nonEmpty(b.values)) }
	slices.SortStableFunc(clean, byValues)
	slices.SortStableFunc(blank, byValues)
	return append(clean, blank...)
}

// presentationBreakdowns is _presentation_breakdowns.
func (v *invoiceView) presentationBreakdowns(fees []*Fee) []block {
	var shown []*Fee
	for _, f := range fees {
		if f.Fee.Type == core.FeeTypeCharge && len(presentationRows(f, presentationKeys(f.Charge))) > 0 {
			shown = append(shown, f)
		}
	}
	var out []block
	for i, group := range groupByCharge(shown) {
		keys := presentationKeys(group[0].Charge)
		var rows []row
		rows = append(rows, row{Style: rowHead, Cells: []cell{
			txt(v.l.T("invoice.usage_breakdown_by", i18n.A("keys", v.l.Sentence(keys)))), txt(v.l.T("invoice.units")),
		}})
		for _, f := range group {
			rows = append(rows, row{Style: rowFee, Cells: []cell{bold(v.feeTitle(f)), txt(units(f.Fee.Units))}})
			prs := presentationRows(f, keys)
			for j, pr := range prs {
				style := rowPlain
				if j == len(prs)-1 {
					style = rowEnd
				}
				rows = append(rows, row{Style: style, Cells: []cell{txt(strings.Join(nonEmpty(pr.values), ", ")), txt(units(pr.units))}})
			}
		}
		out = append(out, block{Kind: blockTable, Style: "presentation", Columns: []column{{80, "left"}, {20, "right"}}, Rows: rows, Break: i == 0})
	}
	return out
}

// progressive is _progressive_billing_details.
func (v *invoiceView) progressive() []block {
	if len(v.subs) == 0 {
		return []block{v.full(true, "invoice.total")}
	}
	s := v.subs[0]
	r := &feeRows{w: v.writer}
	r.fiveHead(v.l.T("invoice.fees_from_to_date",
		i18n.A("from_date", v.date(v.localDate(s.InvoiceSubscription.ChargesPeriod.From))), i18n.A("to_date", v.date(v.inv.IssuingDate))))
	out := []block{tableBlock(fiveColumns(), r.rows, false)}
	var charges []*Fee
	for _, f := range v.subscriptionFees(s) {
		if f.Fee.Type == core.FeeTypeCharge && f.Fee.Units.IsPositive() && f.Fee.TrueUpParentFeeID.IsZero() {
			charges = append(charges, f)
		}
	}
	if len(charges) > 0 {
		v.sortAlphabetically(charges)
		cr := &feeRows{w: v.writer}
		for _, fees := range groupByCharge(charges) {
			if fees[0].Charge != nil && fees[0].Charge.PayInAdvance {
				continue
			}
			cr.chargeFees(fees, true)
		}
		out = append(out, tableBlock(fiveColumns(), cr.rows, false))
	}
	return append(out, v.full(true, "invoice.total"))
}

// charges is _charge (pay-in-advance charge invoices).
func (v *invoiceView) charges() []block {
	r := &feeRows{w: v.writer}
	r.fiveHead(v.l.T("invoice.item"))
	fees := slices.Clone(v.fees)
	slices.SortStableFunc(fees, func(a, b *Fee) int {
		if c := compareTimes(a.Fee.SucceededAt, b.Fee.SucceededAt); c != 0 {
			return c
		}
		return a.Fee.CreatedAt.Compare(b.Fee.CreatedAt)
	})
	for _, f := range fees {
		if f.Fee.Type == core.FeeTypeFixedCharge {
			continue
		}
		title := v.feeTitle(f)
		d := details(&f.Fee)
		switch {
		case f.Charge != nil && f.Charge.ChargeModel == catalog.ChargeModelPercentage && d != nil:
			if basicRatePercentage(f) {
				r.add(rowFee, bold(v.invoiceName(f)+groupedByDisplay(f)), txt(plain(dec(d.PaidUnits))), txt(d.Rate.String()),
					feeTaxes(&f.Fee), txt(v.asCurrency(&f.Fee, dec(d.PerUnitTotalAmount))))
			} else {
				var sub []string
				if f.Metric != nil && f.Metric.AggregationType == catalog.AggregationTypeWeightedSum {
					sub = append(sub, v.l.T("invoice.units_prorated_per_period", i18n.A("period", v.intervalName(v.subscription(f)))))
				}
				events := v.l.T("invoice.total_events", i18n.A("count", fmt.Sprint(f.Fee.EventsCount)))
				if f.Fee.SucceededAt != nil {
					events = v.date(v.utcDate(*f.Fee.SucceededAt)) + " • " + events
				}
				sub = append(sub, events)
				if f.Charge.Prorated {
					sub = append(sub, v.l.T("invoice.fee_prorated"))
				}
				r.nameRow(f, title, sub)
				r.chargePercentage(f, d)
			}
		default:
			var sub []string
			switch {
			case f.Charge != nil && !f.Charge.Invoiceable:
				if f.Fee.SucceededAt != nil {
					sub = append(sub, v.date(v.localTime(f.Fee.SucceededAt)))
				}
			case f.Charge != nil && f.Charge.Prorated && f.ProrationDays != nil:
				sub = append(sub, v.l.T("invoice.breakdown_for_days",
					i18n.A("breakdown_duration", fmt.Sprint(f.ProrationDays.Days)), i18n.A("breakdown_total_duration", fmt.Sprint(f.ProrationDays.Total))))
			}
			r.add(rowFee, bold(title, sub...), txt(units(f.Fee.Units)), txt(v.preciseUnitAmount(&f.Fee)), feeTaxes(&f.Fee), txt(v.feeAmount(&f.Fee)))
		}
		r.conversion(f, &f.Fee)
	}
	return []block{tableBlock(fiveColumns(), r.rows, false), v.full(false, v.totalKey())}
}

// fixedCharges is _fixed_charge (pay-in-advance fixed charge invoices).
func (v *invoiceView) fixedCharges() []block {
	var out []block
	if len(v.subs) > 0 {
		s := v.subs[0]
		out = append(out, v.subscriptionName(s, false))
		var fees []*Fee
		for _, f := range v.fees {
			if f.Fee.Type == core.FeeTypeFixedCharge {
				fees = append(fees, f)
			}
		}
		slices.SortStableFunc(fees, func(a, b *Fee) int {
			if c := compareTimes(a.Fee.SucceededAt, b.Fee.SucceededAt); c != 0 {
				return c
			}
			return a.Fee.CreatedAt.Compare(b.Fee.CreatedAt)
		})
		gi := 0
		for _, g := range v.groupByPeriod(fees, s) {
			if len(g.fixedCharges) == 0 {
				continue
			}
			r := &feeRows{w: v.writer}
			v.periodHeader(r, g.period)
			for _, f := range g.fixedCharges {
				r.fixedChargeFee(f)
			}
			out = append(out, tableBlock(fiveColumns(), r.rows, gi > 0))
			gi++
		}
	}
	return append(out, v.full(false, v.totalKey()))
}

// compareTimes orders nil times last, as PostgreSQL orders NULLs.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

// basicRatePercentage is Fee#basic_rate_percentage?: the charge (or the
// filter) has only a rate.
func basicRatePercentage(f *Fee) bool {
	if f.Charge == nil || f.Charge.ChargeModel != catalog.ChargeModelPercentage {
		return false
	}
	raw := f.Charge.Properties.Raw()
	if f.ChargeFilter != nil {
		raw = f.ChargeFilter.Properties.Raw()
	}
	var m map[string]any
	if err := unmarshalJSON(raw, &m); err != nil {
		return false
	}
	_, ok := m["rate"]
	return ok && len(m) == 1
}
