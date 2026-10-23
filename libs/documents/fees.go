package documents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// amountDetails is the union of the charge models' amount details stored on
// fees (libs/pricing's Details, decoded by key).
type amountDetails struct {
	GraduatedRanges           []rangeDetails `json:"graduated_ranges"`
	GraduatedPercentageRanges []rangeDetails `json:"graduated_percentage_ranges"`

	FreeUnits            *money.Decimal `json:"free_units"`
	PaidUnits            *money.Decimal `json:"paid_units"`
	PerPackageSize       *money.Decimal `json:"per_package_size"`
	PerPackageUnitAmount *money.Decimal `json:"per_package_unit_amount"`

	FlatUnitAmount     *money.Decimal `json:"flat_unit_amount"`
	PerUnitAmount      *money.Decimal `json:"per_unit_amount"`
	PerUnitTotalAmount *money.Decimal `json:"per_unit_total_amount"`

	Units                       *money.Decimal `json:"units"`
	FreeEvents                  *money.Decimal `json:"free_events"`
	Rate                        rate           `json:"rate"`
	PaidEvents                  *money.Decimal `json:"paid_events"`
	FixedFeeUnitAmount          *money.Decimal `json:"fixed_fee_unit_amount"`
	FixedFeeTotalAmount         *money.Decimal `json:"fixed_fee_total_amount"`
	MinMaxAdjustmentTotalAmount *money.Decimal `json:"min_max_adjustment_total_amount"`
}

type rangeDetails struct {
	From               money.Decimal  `json:"from_value"`
	To                 *money.Decimal `json:"to_value"`
	FlatUnitAmount     money.Decimal  `json:"flat_unit_amount"`
	PerUnitAmount      money.Decimal  `json:"per_unit_amount"`
	Rate               rate           `json:"rate"`
	Units              money.Decimal  `json:"units"`
	PerUnitTotalAmount money.Decimal  `json:"per_unit_total_amount"`
}

// details decodes the fee's amount details; nil when they are blank.
func details(f *invoice.Fee) *amountDetails {
	if len(f.AmountDetails) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(f.AmountDetails, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	var d amountDetails
	if err := json.Unmarshal(f.AmountDetails, &d); err != nil {
		return nil
	}
	return &d
}

func dec(p *money.Decimal) money.Decimal {
	if p == nil {
		return money.Zero
	}
	return *p
}

// plain prints a decimal as Ruby prints a JSON number: without trailing
// zeros.
func plain(d money.Decimal) string { return d.Reduce().String() }

func percentRate(d money.Decimal) string { return i18n.RoundDecimalPart(d) + "%" }

// rate is a stored percentage rate, absent when null or blank.
type rate struct{ d *money.Decimal }

// UnmarshalJSON implements [json.Unmarshaler]: null and "" are blank.
func (r *rate) UnmarshalJSON(b []byte) error {
	if s := strings.TrimSpace(string(b)); s == "null" || s == `""` {
		r.d = nil
		return nil
	}
	var d money.Decimal
	if err := json.Unmarshal(b, &d); err != nil {
		return fmt.Errorf("rate: %w", err)
	}
	r.d = &d
	return nil
}

// String is FeeDisplayHelper.format_percentage_rate
// (app/views/helpers/fee_display_helper.rb:61): nothing for a blank rate.
func (r rate) String() string {
	if r.d == nil {
		return ""
	}
	return percentRate(*r.d)
}

func units(d money.Decimal) string { return i18n.RoundDecimalPart(d) }

// feeRows renders the item rows of a fee table.
type feeRows struct {
	w    *writer
	rows []row
}

func (r *feeRows) add(style string, cells ...cell) {
	r.rows = append(r.rows, row{Style: style, Cells: cells})
}

func (r *feeRows) head(first string, rest ...string) {
	cells := []cell{txt(first)}
	for _, s := range rest {
		cells = append(cells, txt(s))
	}
	r.add(rowHead, cells...)
}

func (r *feeRows) fiveHead(first string) {
	l := r.w.l
	r.head(first, l.T("invoice.units"), l.T("invoice.unit_price"), l.T("invoice.tax_rate"), l.T("invoice.amount"))
}

func empty4() []cell { return []cell{{}, {}, {}, {}} }

func (r *feeRows) detail(label, units, price string, taxes cell, amount string) {
	r.add(rowDetail, txt(label), txt(units), txt(price), taxes, txt(amount))
}

func (r *feeRows) subtotal(f *invoice.Fee) {
	r.add(rowSubtotal, txt(r.w.l.T("invoice.sub_total")), cell{}, cell{}, cell{}, txt(r.w.feeAmount(f)))
}

// sublines are the muted lines under a fee's name (_default_fee and the
// name rows of fees with details).
func (r *feeRows) sublines(f *Fee, withFixedCharge bool) []string {
	w := r.w
	var out []string
	if f.Metric != nil && f.Metric.AggregationType == catalog.AggregationTypeWeightedSum {
		out = append(out, w.l.T("invoice.units_prorated_per_period", i18n.A("period", w.intervalName(w.subscription(f)))))
	}
	if f.Charge != nil && f.Charge.ChargeModel == catalog.ChargeModelPercentage {
		out = append(out, w.l.T("invoice.total_events", i18n.A("count", strconv.FormatInt(f.Fee.EventsCount, 10))))
	}
	if (f.Charge != nil && f.Charge.Prorated) || (withFixedCharge && f.FixedCharge != nil && f.FixedCharge.Prorated) {
		out = append(out, w.l.T("invoice.fee_prorated"))
	}
	return out
}

// defaultFee is _default_fee (and _default_fee_with_filters).
func (r *feeRows) defaultFee(f *Fee) {
	w := r.w
	r.add(rowFee, bold(w.feeTitle(f), r.sublines(f, true)...),
		txt(units(f.Fee.Units)), txt(w.preciseUnitAmount(&f.Fee)), feeTaxes(&f.Fee), txt(w.feeAmount(&f.Fee)))
	r.conversion(f, &f.Fee)
}

// conversion is _conversion_row.
func (r *feeRows) conversion(f *Fee, fee *invoice.Fee) {
	u := fee.PricingUnitUsage
	if u == nil {
		return
	}
	w := r.w
	fiat := w.l.MoneyPrecise(i18n.NormalizePrecision(u.ConversionRate), w.planCurrency(f))
	r.add(rowSubtotal, txt(w.l.T("invoice.conversion_rate")+": 1 "+u.ShortName+" = "+fiat),
		cell{}, cell{}, cell{}, txt(w.l.Money(fee.Amount, currencyOr(fee.Currency, w.cur))))
}

func (w *writer) planCurrency(f *Fee) money.Currency {
	if s := w.subscription(f); s != nil && !s.Plan.Currency.IsZero() {
		return s.Plan.Currency
	}
	return currencyOr(f.Fee.Currency, w.cur)
}

// nameRow is the name row of a fee whose details follow.
func (r *feeRows) nameRow(f *Fee, title string, sublines []string) {
	r.add(rowName, append([]cell{bold(title, sublines...)}, empty4()...)...)
}

// withDetails renders a fee's model details (_graduated, _volume, …).
func (r *feeRows) modelDetails(f *Fee, model string, d *amountDetails) {
	switch model {
	case string(catalog.ChargeModelGraduated):
		r.graduated(&f.Fee, d.GraduatedRanges, false)
	case string(catalog.ChargeModelGraduatedPercentage):
		r.graduated(&f.Fee, d.GraduatedPercentageRanges, true)
	case string(catalog.ChargeModelVolume):
		r.volume(&f.Fee, d)
	case string(catalog.ChargeModelPackage):
		r.pkg(&f.Fee, d)
	case string(catalog.ChargeModelPercentage):
		r.percentage(&f.Fee, d)
	}
}

// graduated is _graduated and _graduated_percentage.
func (r *feeRows) graduated(f *invoice.Fee, ranges []rangeDetails, percentage bool) {
	if len(ranges) == 0 {
		r.subtotal(f)
		return
	}
	w, l := r.w, r.w.l
	var first, last *rangeDetails
	if ranges[0].To != nil {
		first = &ranges[0]
	}
	var next []*rangeDetails
	for i := range ranges {
		rg := &ranges[i]
		if rg.To == nil && last == nil {
			last = rg
		}
		if !rg.From.IsZero() && rg.To != nil {
			next = append(next, rg)
		}
	}
	taxes := feeTaxes(f)
	price := func(rg *rangeDetails) string {
		if percentage {
			return rg.Rate.String()
		}
		return w.withPrecision(f, rg.PerUnitAmount)
	}
	unitRow := func(label string, rg *rangeDetails) {
		r.detail(label, units(rg.Units), price(rg), taxes, w.asCurrency(f, rg.PerUnitTotalAmount))
	}
	flatRow := func(label string, rg *rangeDetails) {
		v := w.asCurrency(f, rg.FlatUnitAmount)
		r.detail(label, "1", v, taxes, v)
	}
	if first != nil {
		unitRow(l.T("invoice.graduated.fee_per_unit_for_the_first", i18n.A("to", plain(*first.To))), first)
	}
	for _, rg := range next {
		unitRow(l.T("invoice.graduated.fee_per_unit_for_the_next", i18n.A("from", plain(rg.From)), i18n.A("to", plain(*rg.To))), rg)
	}
	if last != nil {
		unitRow(l.T("invoice.graduated.fee_per_unit_for_the_last", i18n.A("from", plain(last.From))), last)
	}
	if first != nil && first.FlatUnitAmount.IsPositive() {
		flatRow(l.T("invoice.graduated.flat_fee_for_the_first", i18n.A("to", plain(*first.To))), first)
	}
	for _, rg := range next {
		if rg.FlatUnitAmount.IsPositive() {
			flatRow(l.T("invoice.graduated.flat_fee_for_the_next", i18n.A("from", plain(rg.From)), i18n.A("to", plain(*rg.To))), rg)
		}
	}
	if last != nil && last.FlatUnitAmount.IsPositive() {
		flatRow(l.T("invoice.graduated.flat_fee_for_the_last", i18n.A("from", plain(last.From))), last)
	}
	r.subtotal(f)
}

func (r *feeRows) zero(f *invoice.Fee) string { return r.w.l.Money(0, currencyOr(f.Currency, r.w.cur)) }

// volume is _volume.
func (r *feeRows) volume(f *invoice.Fee, d *amountDetails) {
	w, l := r.w, r.w.l
	taxes := feeTaxes(f)
	r.detail(l.T("invoice.volume.fee_per_unit"), units(f.Units), w.withPrecision(f, dec(d.PerUnitAmount)), taxes, w.asCurrency(f, dec(d.PerUnitTotalAmount)))
	if flat := dec(d.FlatUnitAmount); flat.IsPositive() {
		r.detail(l.T("invoice.volume.flat_fee_for_all_units"), "1", w.asCurrency(f, flat), taxes, l.MoneyUnits(flat, currencyOr(f.Currency, w.cur)))
	}
	r.subtotal(f)
}

// pkg is _package.
func (r *feeRows) pkg(f *invoice.Fee, d *amountDetails) {
	w, l := r.w, r.w.l
	taxes := feeTaxes(f)
	if free := dec(d.FreeUnits); free.IsPositive() {
		r.detail(l.T("invoice.package.free_units_for_the_first", i18n.A("count", plain(free))), plain(free), r.zero(f), taxes, r.zero(f))
	}
	price := l.T("invoice.package.fee_per_package_unit_price",
		i18n.A("amount", w.withPrecision(f, dec(d.PerPackageUnitAmount))), i18n.A("package_size", plain(dec(d.PerPackageSize))))
	r.detail(l.T("invoice.package.fee_per_package"), plain(dec(d.PaidUnits)), price, taxes, w.feeAmount(f))
	r.subtotal(f)
}

// percentage is _percentage.
func (r *feeRows) percentage(f *invoice.Fee, d *amountDetails) {
	w, l := r.w, r.w.l
	taxes := feeTaxes(f)
	if free := dec(d.FreeUnits); free.IsPositive() {
		n, _ := dec(d.FreeEvents).Int64()
		r.detail(l.Plural("invoice.percentage.free_units_per_transaction", n), plain(free), r.zero(f), taxes, r.zero(f))
	}
	r.detail(l.T("invoice.percentage.percentage_rate_on_amount"), plain(dec(d.PaidUnits)), d.Rate.String(), taxes, w.asCurrency(f, dec(d.PerUnitTotalAmount)))
	if fixed := dec(d.FixedFeeUnitAmount); fixed.IsPositive() {
		r.detail(l.T("invoice.percentage.fee_per_transaction"), plain(dec(d.PaidEvents)), w.asCurrency(f, fixed), taxes, w.asCurrency(f, dec(d.FixedFeeTotalAmount)))
	}
	if adj := dec(d.MinMaxAdjustmentTotalAmount); !adj.IsZero() {
		v := w.asCurrency(f, adj)
		r.detail(l.T("invoice.percentage.adjustment_per_transaction"), "1", v, taxes, v)
	}
	r.subtotal(f)
}

// chargePercentage is _charge_percentage (pay-in-advance percentage fees).
func (r *feeRows) chargePercentage(f *Fee, d *amountDetails) {
	w, l := r.w, r.w.l
	fee := &f.Fee
	taxes := feeTaxes(fee)
	if dec(d.FreeUnits).IsPositive() || dec(d.FreeEvents).IsPositive() {
		r.detail(l.Plural("invoice.percentage.free_units_per_transaction", 1), plain(dec(d.FreeUnits)), r.zero(fee), taxes, r.zero(fee))
	}
	if dec(d.PaidUnits).IsPositive() {
		r.detail(l.T("invoice.percentage.percentage_rate_on_amount"), plain(dec(d.PaidUnits)), d.Rate.String(), taxes, w.asCurrency(fee, dec(d.PerUnitTotalAmount)))
	}
	if total := dec(d.FixedFeeTotalAmount); total.IsPositive() {
		v := w.asCurrency(fee, total)
		r.detail(l.T("invoice.percentage.fee_per_transaction"), plain(dec(d.PaidEvents)), v, taxes, v)
	}
	if adj := dec(d.MinMaxAdjustmentTotalAmount); !adj.IsZero() {
		v := w.asCurrency(fee, adj)
		r.detail(l.T("invoice.percentage.adjustment_per_transaction"), "1", v, taxes, v)
	}
	r.subtotal(fee)
	r.conversion(f, fee)
}

// trueUp is _true_up_fee.
func (r *feeRows) trueUp(f *Fee) {
	t := f.TrueUpFee
	if t == nil {
		return
	}
	w, l := r.w, r.w.l
	amount := w.feeAmount(t)
	r.add(rowFee, bold(l.T("invoice.true_up_metric", i18n.A("metric", w.invoiceName(f))),
		l.T("invoice.true_up_details", i18n.A("min_amount", w.minAmount(f)))),
		txt("1"), txt(amount), feeTaxes(t), txt(amount))
	r.conversion(f, t)
}

// minAmount is ChargeDisplayHelper.format_min_amount: the charge's minimum
// in its pricing unit (min_amount_cents / 100), or in the plan's currency.
func (w *writer) minAmount(f *Fee) string {
	if f.Charge == nil {
		return ""
	}
	if f.Charge.AppliedPricingUnit != nil {
		short := ""
		if f.PricingUnit != nil {
			short = f.PricingUnit.ShortName
		} else if u := f.Fee.PricingUnitUsage; u != nil {
			short = u.ShortName
		}
		return w.l.PricingUnit(money.New(f.Charge.MinAmount, -2), short)
	}
	return w.l.Money(f.Charge.MinAmount, w.planCurrency(f))
}

// feeWithFilters is _fee_with_filters.
func (r *feeRows) feeWithFilters(f *Fee, d *amountDetails) {
	r.nameRow(f, r.w.feeTitle(f), r.sublines(f, false))
	if f.Charge != nil {
		r.modelDetails(f, string(f.Charge.ChargeModel), d)
	}
	r.conversion(f, &f.Fee)
}

// feeWithoutFilters is _fees_without_filters.
func (r *feeRows) feeWithoutFilters(f *Fee) {
	d := details(&f.Fee)
	if f.Fee.Amount == 0 || d == nil || f.Charge == nil {
		r.defaultFee(f)
	} else {
		r.nameRow(f, r.w.feeTitle(f), r.sublines(f, false))
		r.modelDetails(f, string(f.Charge.ChargeModel), d)
		r.conversion(f, &f.Fee)
	}
	r.trueUp(f)
}

// fixedChargeFee is _fixed_charge_fee.
func (r *feeRows) fixedChargeFee(f *Fee) {
	d := details(&f.Fee)
	if f.Fee.Amount == 0 || d == nil || f.FixedCharge == nil {
		r.defaultFee(f)
		return
	}
	var sub []string
	if f.FixedCharge.Prorated {
		sub = append(sub, r.w.l.T("invoice.fee_prorated"))
	}
	r.nameRow(f, r.w.invoiceName(f)+groupedByDisplay(f), sub)
	r.modelDetails(f, string(f.FixedCharge.ChargeModel), d)
}

// chargeFees renders the fees of one charge in a subscription table (the
// charge loop of _subscription_details and _progressive_billing_details).
func (r *feeRows) chargeFees(fees []*Fee, onlyPositiveFiltered bool) {
	allFiltered := true
	total := money.Zero
	for _, f := range fees {
		allFiltered = allFiltered && !f.Fee.ChargeFilterID.IsZero()
		total = total.Add(f.Fee.Units)
	}
	if allFiltered && total.IsPositive() {
		for _, f := range fees {
			if onlyPositiveFiltered && !f.Fee.Units.IsPositive() {
				continue
			}
			if d := details(&f.Fee); d == nil {
				r.defaultFee(f)
			} else {
				r.feeWithFilters(f, d)
			}
		}
		for _, f := range fees {
			r.trueUp(f)
		}
		return
	}
	for _, f := range fees {
		r.feeWithoutFilters(f)
	}
}

// groupByCharge groups fees by charge, in order of first appearance.
func groupByCharge(fees []*Fee) [][]*Fee {
	var out [][]*Fee
	index := map[core.ChargeID]int{}
	for _, f := range fees {
		i, ok := index[f.Fee.ChargeID]
		if !ok {
			i = len(out)
			index[f.Fee.ChargeID] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], f)
	}
	return out
}

func unmarshalJSON(b []byte, v any) error {
	if len(b) == 0 {
		return errEmptyJSON
	}
	return json.Unmarshal(b, v)
}

var errEmptyJSON = errors.New("documents: empty JSON")
