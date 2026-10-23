package documents

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
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

// refs returns the objects f's names are read from.
func (w *writer) refs(f *Fee) invoice.FeeRefs {
	r := invoice.FeeRefs{
		Charge: f.Charge, Metric: f.Metric, ChargeFilter: f.ChargeFilter, FixedCharge: f.FixedCharge,
		AddOn: f.AddOn, WalletTransaction: f.WalletTransaction,
	}
	if s := w.subscription(f); s != nil {
		r.Subscription, r.Plan = &s.Subscription, &s.Plan
	}
	return r
}

// invoiceName is Fee#invoice_name.
func (w *writer) invoiceName(f *Fee) string { return w.refs(f).InvoiceName(&f.Fee) }

// itemName is Fee#item_name.
func (w *writer) itemName(f *Fee) string { return w.refs(f).ItemName(&f.Fee) }

// filterDisplay is Fee#filter_display_name.
func filterDisplay(f *Fee, sep string) string {
	return invoice.FeeRefs{ChargeFilter: f.ChargeFilter}.FilterDisplayName(sep)
}

// groupedByDisplay is Fee#grouped_by_display.
func groupedByDisplay(f *Fee) string { return invoice.GroupedByDisplay(&f.Fee) }

// feeTitle is FeeDisplayHelper.fee_title.
func (w *writer) feeTitle(f *Fee) string { return w.refs(f).Title(&f.Fee) }

// sortingClause is Fee#invoice_sorting_clause.
func (w *writer) sortingClause(f *Fee) string { return w.refs(f).SortingKey(&f.Fee) }

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
