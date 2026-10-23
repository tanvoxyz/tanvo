package mail

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Date formats d with the locale's date.formats.default (I18n.l(date,
// format: :default)). It supports the strftime directives lago's locales
// use: %Y %y %m %-m %d %-d %e %b %B and %%.
func (l *Localizer) Date(d core.Date) (string, error) {
	f, err := l.str("date.formats.default")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' || i+1 == len(f) {
			b.WriteByte(f[i])
			continue
		}
		i++
		pad := true
		if f[i] == '-' && i+1 < len(f) {
			pad = false
			i++
		}
		num := func(n, width int) {
			s := strconv.Itoa(n)
			if pad && len(s) < width {
				s = strings.Repeat("0", width-len(s)) + s
			}
			b.WriteString(s)
		}
		switch f[i] {
		case 'Y':
			b.WriteString(strconv.Itoa(d.Year))
		case 'y':
			num(d.Year%100, 2)
		case 'm':
			num(int(d.Month), 2)
		case 'd':
			num(d.Day, 2)
		case 'e':
			fmt.Fprintf(&b, "%2d", d.Day)
		case 'b':
			s, err := l.monthName("date.abbr_month_names", int(d.Month))
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		case 'B':
			s, err := l.monthName("date.month_names", int(d.Month))
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		case '%':
			b.WriteByte('%')
		default:
			return "", fmt.Errorf("date format %q: unsupported directive %%%c", f, f[i])
		}
	}
	return b.String(), nil
}

// currencySymbol returns the money-gem symbol of the currencies whose
// symbol is one of $, €, £ or ¥. lago's MoneyHelper.currency_format prints
// these with the locale's money.format ("%u%n"); every other currency is
// printed with money.custom_format ("%{iso_code} %n").
func currencySymbol(code string) (string, bool) {
	switch code {
	case "USD", "ARS", "AUD", "BBD", "BMD", "BND", "BSD", "BZD", "CAD", "CLP", "COP", "CUC", "CUP",
		"DOP", "FJD", "GYD", "HKD", "JMD", "KYD", "LRD", "MXN", "NAD", "NZD", "SBD", "SGD", "SRD",
		"TTD", "TWD", "UYU", "XCD":
		return "$", true
	case "EUR":
		return "€", true
	case "GBP", "FKP", "GIP", "SHP", "SSP", "GGP", "IMP", "JEP":
		return "£", true
	case "JPY", "CNY":
		return "¥", true
	default:
		return "", false
	}
}

// Money formats amount (in currency units, not cents) of the ISO 4217
// currency code as lago's MoneyHelper.format does: rounded half up to the
// currency's minor unit, grouped with the locale's thousands separator,
// with its decimal mark, and placed in money.format with the symbol or in
// money.custom_format with the code. A negative sign precedes the number.
func (l *Localizer) Money(amount money.Decimal, code string) (string, error) {
	cur, err := money.LookupCurrency(code)
	if err != nil {
		return "", fmt.Errorf("format money: %w", err)
	}
	dec, err := l.str("money.decimal_mark")
	if err != nil {
		return "", err
	}
	sep, err := l.str("money.thousands_separator")
	if err != nil {
		return "", err
	}
	exp := cur.Exponent()
	s := amount.Round(exp, money.HalfUp).Abs().StringFixed(exp)
	intPart, frac, _ := strings.Cut(s, ".")
	var groups []string
	for len(intPart) > 3 {
		groups = append(groups, intPart[len(intPart)-3:])
		intPart = intPart[:len(intPart)-3]
	}
	groups = append(groups, intPart)
	slices.Reverse(groups)
	n := strings.Join(groups, sep)
	if frac != "" {
		n += dec + frac
	}
	if amount.Round(exp, money.HalfUp).IsNegative() {
		n = "-" + n
	}
	if sym, ok := currencySymbol(cur.Code()); ok {
		f, err := l.str("money.format")
		if err != nil {
			return "", err
		}
		return strings.NewReplacer("%u", sym, "%n", n).Replace(f), nil
	}
	f, err := l.str("money.custom_format")
	if err != nil {
		return "", err
	}
	return strings.NewReplacer("%{iso_code}", cur.Code(), "%n", n).Replace(f), nil
}
