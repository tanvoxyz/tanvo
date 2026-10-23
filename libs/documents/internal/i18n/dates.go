package i18n

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

type dateFormat struct {
	def        string
	months     []string
	abbrMonths []string
	days       []string
	abbrDays   []string
}

func (l *Locale) loadFormats() error {
	l.date = dateFormat{
		def:        l.str("date.formats.default"),
		months:     l.list("date.month_names"),
		abbrMonths: l.list("date.abbr_month_names"),
		days:       l.list("date.day_names"),
		abbrDays:   l.list("date.abbr_day_names"),
	}
	if l.date.def == "" || len(l.date.abbrMonths) != 13 || len(l.date.months) != 13 {
		return fmt.Errorf("i18n: locale %s: incomplete date names", l.tag)
	}
	l.money = moneyFormat{
		format:       l.str("money.format"),
		customFormat: l.str("money.custom_format"),
		decimalMark:  l.str("money.decimal_mark"),
		thousands:    l.str("money.thousands_separator"),
	}
	if m, ok := walk(l.tree, "money.symbols").(map[string]any); ok {
		l.money.symbols = map[string]string{}
		for code, v := range m {
			if sym, ok := v.(string); ok {
				l.money.symbols[code] = sym
			}
		}
	}
	if l.money.format == "" || l.money.customFormat == "" || l.money.decimalMark == "" {
		return fmt.Errorf("i18n: locale %s: incomplete money format", l.tag)
	}
	return nil
}

// Date formats d with the locale's default date format, as
// I18n.l(date, format: :default).
func (l *Locale) Date(d core.Date) string {
	if d.IsZero() {
		return ""
	}
	return l.Strftime(d, l.date.def)
}

// Strftime formats d with a Ruby strftime layout, using the locale's month
// and day names for %b, %B, %a and %A as I18n.l does. It supports %Y, %y,
// %m, %d, %e, %j, %b, %B, %a, %A and %%, with the "-" (no padding) flag.
func (l *Locale) Strftime(d core.Date, layout string) string {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 0; i < len(layout); i++ {
		c := layout[i]
		if c != '%' || i+1 == len(layout) {
			b.WriteByte(c)
			continue
		}
		i++
		pad := true
		if layout[i] == '-' && i+1 < len(layout) {
			pad = false
			i++
		}
		num := func(n, width int) {
			s := strconv.Itoa(n)
			if pad {
				for len(s) < width {
					s = "0" + s
				}
			}
			b.WriteString(s)
		}
		switch layout[i] {
		case 'Y':
			b.WriteString(strconv.Itoa(t.Year()))
		case 'y':
			num(t.Year()%100, 2)
		case 'm':
			num(int(t.Month()), 2)
		case 'd':
			num(t.Day(), 2)
		case 'e':
			s := strconv.Itoa(t.Day())
			if pad && len(s) < 2 {
				s = " " + s
			}
			b.WriteString(s)
		case 'j':
			num(t.YearDay(), 3)
		case 'b', 'h':
			b.WriteString(l.date.abbrMonths[t.Month()])
		case 'B':
			b.WriteString(l.date.months[t.Month()])
		case 'a':
			b.WriteString(index(l.date.abbrDays, int(t.Weekday())))
		case 'A':
			b.WriteString(index(l.date.days, int(t.Weekday())))
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			if !pad {
				b.WriteByte('-')
			}
			b.WriteByte(layout[i])
		}
	}
	return b.String()
}

func index(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}
