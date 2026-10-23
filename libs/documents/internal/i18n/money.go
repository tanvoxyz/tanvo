package i18n

import (
	"strings"

	"github.com/tanvoxyz/tanvo/libs/money"
)

type moneyFormat struct {
	format       string
	customFormat string
	decimalMark  string
	thousands    string
	// symbols, when set, are the locale's own currency symbols (CLDR's, for
	// Tanvo's zh-CN), replacing the money gem's: other currencies print
	// with customFormat.
	symbols map[string]string
}

// currencySymbol returns the money gem's symbol of an ISO currency when it is
// one of the four lago-api prints as a symbol ($, €, £, ¥); other currencies
// print their ISO code (MoneyHelper::SYMBOLS_CURRENCIES).
func currencySymbol(code string) string {
	switch code {
	case "ARS", "AUD", "BBD", "BMD", "BND", "BSD", "BZD", "CAD", "CLP", "COP", "CUC", "CUP", "CVE",
		"DOP", "FJD", "GYD", "HKD", "JMD", "KYD", "LRD", "MXN", "NAD", "NZD", "SBD", "SGD", "SRD",
		"TTD", "TWD", "USD", "XCD":
		return "$"
	case "EUR":
		return "€"
	case "FKP", "GBP", "GIP", "SDG", "SHP", "SSP":
		return "£"
	case "CNY", "JPY":
		return "¥"
	default:
		return ""
	}
}

// Money formats an amount in minor units, as MoneyHelper.format of a Money.
func (l *Locale) Money(minor int64, cur money.Currency) string {
	return l.formatMoney(cur.FromMinor(minor), cur)
}

// MoneyUnits formats an amount in currency units rounded half up to the
// currency's minor unit, as MoneyHelper.format(amount.to_money(currency)).
func (l *Locale) MoneyUnits(units money.Decimal, cur money.Currency) string {
	return l.formatMoney(units.Round(cur.Exponent(), money.HalfUp), cur)
}

// MoneyPrecise formats an unrounded amount in currency units with up to six
// decimals (six significant digits below 1), as
// MoneyHelper.format_with_precision.
func (l *Locale) MoneyPrecise(units money.Decimal, cur money.Currency) string {
	return l.formatMoney(NormalizePrecision(units), cur)
}

// PricingUnit formats an amount of a pricing unit with at least two decimals,
// followed by its short name, as MoneyHelper.format_pricing_unit.
func (l *Locale) PricingUnit(units money.Decimal, shortName string) string {
	return l.number(units, 2) + " " + shortName
}

// PricingUnitPrecise formats an unrounded amount of a pricing unit, as
// MoneyHelper.format_pricing_unit_with_precision.
func (l *Locale) PricingUnitPrecise(units money.Decimal, shortName string) string {
	return l.number(NormalizePrecision(units), 2) + " " + shortName
}

// NormalizePrecision keeps six significant digits of values below 1 and six
// decimals of the others (MoneyHelper.normalize_precision).
func NormalizePrecision(d money.Decimal) money.Decimal {
	if d.LessThan(money.One) {
		return significant(d, 6)
	}
	return d.Round(6, money.HalfUp)
}

func (l *Locale) formatMoney(units money.Decimal, cur money.Currency) string {
	if cur.IsCustom() {
		return l.number(units, 2) + " " + cur.Code()
	}
	exp := int(cur.Exponent())
	if l.money.symbols != nil && units.IsNegative() {
		// CLDR puts the minus sign before the symbol ("-US$5.00"); the
		// money gem puts it in %n ("$-5.00").
		return "-" + l.formatMoney(units.Abs(), cur)
	}
	num := l.number(units, exp)
	sym := currencySymbol(cur.Code())
	if l.money.symbols != nil {
		sym = l.money.symbols[cur.Code()]
	}
	if sym != "" {
		return strings.ReplaceAll(strings.ReplaceAll(l.money.format, "%u", sym), "%n", num)
	}
	f := strings.ReplaceAll(l.money.customFormat, "%{iso_code}", cur.Code())
	return strings.ReplaceAll(f, "%n", num)
}

// number writes d with the locale's marks, padding the decimals to at least
// minDecimals (none when minDecimals is 0, as the money gem drops the decimals
// of zero-decimal currencies), with a leading minus for negative values.
func (l *Locale) number(d money.Decimal, minDecimals int) string {
	s := d.Abs().Reduce().String()
	whole, frac, _ := strings.Cut(s, ".")
	if minDecimals == 0 {
		frac = ""
	} else {
		for len(frac) < minDecimals {
			frac += "0"
		}
	}
	whole = group(whole, l.money.thousands)
	out := whole
	if frac != "" {
		out += l.money.decimalMark + frac
	}
	if d.IsNegative() {
		out = "-" + out
	}
	return out
}

func group(whole, sep string) string {
	if len(whole) <= 3 || sep == "" {
		return whole
	}
	var b strings.Builder
	first := len(whole) % 3
	if first > 0 {
		b.WriteString(whole[:first])
	}
	for i := first; i < len(whole); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(whole[i : i+3])
	}
	return b.String()
}
