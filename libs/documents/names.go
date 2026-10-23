package documents

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// writer formats the texts of one document.
type writer struct {
	l    *i18n.Locale
	cat  *i18n.Catalog
	cur  money.Currency
	tz   *time.Location
	subs map[core.SubscriptionID]*Subscription
}

func present(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func (w *writer) money(minor int64) string { return w.l.Money(minor, w.cur) }

func (w *writer) negMoney(minor int64) string { return "-" + w.money(minor) }

// localDate returns the date of t in the customer's time zone.
func (w *writer) localDate(t time.Time) core.Date {
	if t.IsZero() {
		return core.Date{}
	}
	lt := t.In(w.tz)
	return core.Date{Year: lt.Year(), Month: lt.Month(), Day: lt.Day()}
}

// endDate returns the last day of a half-open period ending at end, in the
// customer's time zone (lago prints inclusive end dates).
func (w *writer) endDate(end time.Time) core.Date {
	if end.IsZero() {
		return core.Date{}
	}
	return w.localDate(end.Add(-time.Nanosecond))
}

func (w *writer) date(d core.Date) string { return w.l.Date(d) }

func (w *writer) subscription(f *Fee) *Subscription { return w.subs[f.Fee.SubscriptionID] }

// invoiceName is Fee#invoice_name.
func (w *writer) invoiceName(f *Fee) string {
	if s := present(f.Fee.InvoiceDisplayName); s != "" {
		return s
	}
	switch f.Fee.Type {
	case core.FeeTypeCharge:
		if f.Charge != nil {
			return f.Charge.InvoiceName(metricName(f.Metric))
		}
		return metricName(f.Metric)
	case core.FeeTypeAddOn:
		if f.AddOn != nil {
			return f.AddOn.InvoiceName()
		}
	case core.FeeTypeCredit:
		if f.WalletTransaction != nil {
			if s := present(f.WalletTransaction.Name); s != "" {
				return s
			}
		}
		return string(core.FeeTypeCredit)
	case core.FeeTypeFixedCharge:
		if f.FixedCharge != nil {
			if s := present(f.FixedCharge.InvoiceDisplayName); s != "" {
				return s
			}
		}
		if f.AddOn != nil {
			return f.AddOn.InvoiceName()
		}
	case core.FeeTypeCommitment:
		if f.Commitment != nil {
			if s := f.Commitment.InvoiceName(); s != "" {
				return s
			}
		}
	}
	if s := w.subscription(f); s != nil {
		return s.InvoiceName()
	}
	return ""
}

// itemName is Fee#item_name.
func (w *writer) itemName(f *Fee) string {
	switch f.Fee.Type {
	case core.FeeTypeCharge:
		return metricName(f.Metric)
	case core.FeeTypeAddOn, core.FeeTypeFixedCharge:
		if f.AddOn != nil {
			return f.AddOn.Name
		}
	case core.FeeTypeCredit:
		if f.WalletTransaction != nil {
			if s := present(f.WalletTransaction.Name); s != "" {
				return s
			}
		}
		return string(core.FeeTypeCredit)
	}
	if s := w.subscription(f); s != nil {
		return s.Plan.Name
	}
	return ""
}

func metricName(m *catalog.BillableMetric) string {
	if m == nil {
		return ""
	}
	return m.Name
}

// filterDisplay is Fee#filter_display_name.
func filterDisplay(f *Fee, sep string) string {
	if f.ChargeFilter == nil {
		return ""
	}
	return f.ChargeFilter.DisplayName(sep)
}

// groupedValues are the fee's grouping values in lago-api's order: Ruby
// iterates fees.grouped_by (a jsonb column, db/structure.sql:2215) in the
// order PostgreSQL stores jsonb object keys, shorter keys first, then
// bytewise.
func groupedValues(f *Fee) []string {
	var out []string
	keys := slices.SortedFunc(maps.Keys(f.Fee.GroupedBy), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	for _, k := range keys {
		if v := f.Fee.GroupedBy[k]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// groupedByDisplay is Fee#grouped_by_display.
func groupedByDisplay(f *Fee) string {
	if f.Fee.Type != core.FeeTypeCharge {
		return ""
	}
	vs := groupedValues(f)
	if len(vs) == 0 {
		return ""
	}
	return " • " + strings.Join(vs, " • ")
}

// feeTitle is FeeDisplayHelper.fee_title.
func (w *writer) feeTitle(f *Fee) string {
	s := w.invoiceName(f) + groupedByDisplay(f)
	if !f.Fee.ChargeFilterID.IsZero() {
		s += " • " + filterDisplay(f, " • ")
	}
	return s
}

// sortingClause is Fee#invoice_sorting_clause.
func (w *writer) sortingClause(f *Fee) string {
	base := strings.ToLower(w.invoiceName(f) + " " + filterDisplay(f, ", "))
	if f.Fee.Type != core.FeeTypeCharge || len(f.Fee.GroupedBy) == 0 {
		return base
	}
	var vals []string
	for _, k := range slices.Sorted(maps.Keys(f.Fee.GroupedBy)) {
		vals = append(vals, f.Fee.GroupedBy[k])
	}
	return strings.ToLower(w.invoiceName(f) + " " + strings.Join(vals, "") + " " + filterDisplay(f, ", "))
}

func (w *writer) sortAlphabetically(fees []*Fee) {
	slices.SortStableFunc(fees, func(a, b *Fee) int { return cmp.Compare(w.sortingClause(a), w.sortingClause(b)) })
}

// feeAmount is FeeDisplayHelper.format_amount.
func (w *writer) feeAmount(f *invoice.Fee) string {
	if u := f.PricingUnitUsage; u != nil {
		return w.l.PricingUnit(u.Amount, u.ShortName)
	}
	return w.l.Money(f.Amount, currencyOr(f.Currency, w.cur))
}

// preciseUnitAmount is FeeDisplayHelper.format_precise_unit_amount.
func (w *writer) preciseUnitAmount(f *invoice.Fee) string {
	if u := f.PricingUnitUsage; u != nil {
		return w.l.PricingUnitPrecise(u.PreciseUnitAmount, u.ShortName)
	}
	return w.l.MoneyPrecise(f.PreciseUnitAmount, currencyOr(f.Currency, w.cur))
}

// withPrecision is FeeDisplayHelper.format_with_precision.
func (w *writer) withPrecision(f *invoice.Fee, units money.Decimal) string {
	if u := f.PricingUnitUsage; u != nil {
		return w.l.PricingUnitPrecise(units, u.ShortName)
	}
	return w.l.MoneyPrecise(units, currencyOr(f.Currency, w.cur))
}

// asCurrency is FeeDisplayHelper.format_as_currency.
func (w *writer) asCurrency(f *invoice.Fee, units money.Decimal) string {
	if u := f.PricingUnitUsage; u != nil {
		return w.l.PricingUnit(units, u.ShortName)
	}
	return w.l.MoneyUnits(units, currencyOr(f.Currency, w.cur))
}

func currencyOr(c, def money.Currency) money.Currency {
	if c.IsZero() {
		return def
	}
	return c
}

// taxRates is TaxHelper.applied_taxes: the fee's tax rates, highest first,
// or "0.0%".
func taxRates(taxes []invoice.FeeAppliedTax) []string {
	if len(taxes) == 0 {
		return []string{"0.0%"}
	}
	rates := make([]money.Decimal, len(taxes))
	for i, t := range taxes {
		rates[i] = t.TaxRate
	}
	slices.SortStableFunc(rates, func(a, b money.Decimal) int { return b.Cmp(a) })
	out := make([]string, len(rates))
	for i, r := range rates {
		out[i] = i18n.RubyFloat(r) + "%"
	}
	return out
}

func feeTaxes(f *invoice.Fee) cell { return cell{Text: taxRates(f.AppliedTaxes)} }

// intervalName is IntervalHelper.interval_name.
func (w *writer) intervalName(s *Subscription) string {
	if s == nil {
		return ""
	}
	switch s.Plan.Interval.String() {
	case "weekly":
		return w.l.T("invoice.week")
	case "monthly":
		return w.l.T("invoice.month")
	case "yearly":
		return w.l.T("invoice.year")
	case "quarterly":
		return w.l.T("invoice.quarter")
	case "semiannual":
		return w.l.T("invoice.half_year")
	default:
		return ""
	}
}
