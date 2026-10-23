package einvoice

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Date layouts.
const (
	// ciiDateLayout is CCYYMMDD, UNTDID 2379 format 102.
	ciiDateLayout = "%04d%02d%02d"
	// ublDateLayout is the ISO 8601 calendar date.
	ublDateLayout = "%04d-%02d-%02d"
)

// ciiDate formats d as CCYYMMDD.
func ciiDate(d core.Date) string { return fmt.Sprintf(ciiDateLayout, d.Year, int(d.Month), d.Day) }

// ublDate formats d as YYYY-MM-DD.
func ublDate(d core.Date) string { return fmt.Sprintf(ublDateLayout, d.Year, int(d.Month), d.Day) }

// utcDate is the calendar date of t in UTC.
func utcDate(t time.Time) core.Date { return core.DateOf(t, time.UTC) }

// timeText is the text form of an instant in UTC: "2025-10-22 10:00:00 UTC".
func timeText(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") + " UTC" }

// number formats d with two decimals, rounding half up ("%.2f").
func number(d money.Decimal) string {
	s := d.StringFixed(2)
	if s == "-0.00" {
		return "0.00"
	}
	return s
}

// percent formats a rate as "20.00%".
func percent(rate money.Decimal) string { return number(rate) + "%" }

// plain formats a quantity, unit price or rate the way it is stored: in
// plain notation without trailing zeros, keeping one decimal for integers
// ("5.0", "0.059", "-1.0").
func plain(d money.Decimal) string {
	if d.IsZero() {
		return "0.0"
	}
	s := d.Reduce().String()
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// units converts minor units of cur to units.
func units(minor int64, cur money.Currency) money.Decimal { return cur.FromMinor(minor) }

// roundedUnits rounds a precise amount of minor units half up to a whole
// minor unit and converts it to units of cur.
func roundedUnits(minor money.Decimal, cur money.Currency) money.Decimal {
	return cur.MinorToUnits(minor.Round(0, money.HalfUp))
}

// amountText formats an amount in units with the currency's decimals
// ("10.00", "1000" for JPY).
func amountText(d money.Decimal, cur money.Currency) string {
	s := d.StringFixed(cur.Exponent())
	if strings.Trim(s, "-0.") == "" {
		return strings.TrimPrefix(s, "-")
	}
	return s
}

// interpolate replaces %{key} placeholders of tmpl by their values, given as
// key, value pairs.
func interpolate(tmpl string, kv ...string) string {
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "%{"+kv[i]+"}", kv[i+1])
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// sentence joins items into a sentence: "a", "a and b", "a, b, and c".
func sentence(items []string, l Labels) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + l.TwoWordsConnector + items[1]
	default:
		return strings.Join(items[:len(items)-1], l.WordsConnector) + l.LastWordConnector + items[len(items)-1]
	}
}

// titleize turns an identifier into a title: "sepa_debit" is "Sepa Debit".
func titleize(s string) string {
	s = strings.ToLower(strings.NewReplacer("_", " ", "-", " ").Replace(s))
	s = strings.TrimSpace(s)
	var b strings.Builder
	start := true
	for _, r := range s {
		if start && unicode.IsLetter(r) {
			r = unicode.ToUpper(r)
		}
		start = !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
		b.WriteRune(r)
	}
	return b.String()
}

// present reports whether s holds anything but white space.
func present(s string) bool { return strings.TrimSpace(s) != "" }

// deref returns *s, or "" for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
