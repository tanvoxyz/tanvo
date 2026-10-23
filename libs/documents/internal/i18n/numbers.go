package i18n

import (
	"strings"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// significant rounds d half up to n significant digits and removes trailing
// zeros, as BigDecimal("%.<n>g" % d) does.
func significant(d money.Decimal, n int) money.Decimal {
	if d.IsZero() {
		return money.Zero
	}
	whole, frac, _ := strings.Cut(d.Abs().Reduce().String(), ".")
	var mag int
	if whole != "0" {
		mag = len(whole) - 1
	} else {
		mag = -(len(frac) - len(strings.TrimLeft(frac, "0")) + 1)
	}
	return d.Round(int32(n-1-mag), money.HalfUp).Reduce()
}

// RoundDecimalPart writes n with six significant digits when |n| < 1 and six
// decimals otherwise, without trailing zeros (RoundingHelper.round_decimal_part
// with its default precision).
func RoundDecimalPart(n money.Decimal) string {
	return RoundDecimalPartTo(n, 6)
}

// RoundDecimalPartTo is [RoundDecimalPart] with figs significant digits or
// decimals.
func RoundDecimalPartTo(n money.Decimal, figs int) string {
	if n.IsZero() {
		return "0"
	}
	if n.Abs().LessThan(money.One) {
		return significant(n, figs).String()
	}
	return n.Round(int32(figs), money.HalfUp).Reduce().String()
}

// Percentage writes a percentage rate as RoundDecimalPart followed by "%"
// (format_percentage_rate of the invoice templates).
func Percentage(rate money.Decimal) string {
	return RoundDecimalPart(rate) + "%"
}

// RubyFloat writes d as Ruby's Float#to_s does for the tax rates of lago-api's
// templates: "20.0", "5.5", "0.0".
func RubyFloat(d money.Decimal) string {
	s := d.Reduce().String()
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// Sentence joins words as Array#to_sentence does with l's connectors.
func (l *Locale) Sentence(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + l.T("support.array.two_words_connector") + words[1]
	}
	return strings.Join(words[:len(words)-1], l.T("support.array.words_connector")) +
		l.T("support.array.last_word_connector") + words[len(words)-1]
}
