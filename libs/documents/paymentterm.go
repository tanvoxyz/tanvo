package documents

import (
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// label returns the term's sentence in l, as PaymentTerm#label does in
// lago-api: given an issuing date, a day_of_month term names the day and
// month its due date really falls on.
func (p PaymentTerm) label(l *i18n.Locale, issuing core.Date) string {
	days := int64(p.Days)
	switch p.Type {
	case PaymentTermDueOnReceipt, PaymentTermEndOfMonth:
		return l.T("invoice.payment_terms." + string(p.Type))
	case PaymentTermNet, PaymentTermNetEndOfMonth, PaymentTermDaysEndOfMonth:
		return l.Plural("invoice.payment_terms."+string(p.Type), days, i18n.A("days", strconv.Itoa(p.Days)))
	case PaymentTermDayOfMonth:
		day, months := p.DayOfMonth, p.MonthOffset
		if !issuing.IsZero() {
			due := p.dayOfMonthDueDate(issuing)
			day = due.Day
			months = (due.Year*12 + int(due.Month)) - (issuing.Year*12 + int(issuing.Month))
		}
		variant := "months_after"
		switch months {
		case 0:
			variant = "same_month"
		case 1:
			variant = "following_month"
		}
		return l.T("invoice.payment_terms.day_of_month."+variant,
			i18n.A("day", strconv.Itoa(day)), i18n.A("months", strconv.Itoa(months)))
	}
	return ""
}

// dayOfMonthDueDate is the due date of a day_of_month term: the day in the
// offset month, clamped to its length, rolled into the next month when it
// precedes the issuing date.
func (p PaymentTerm) dayOfMonthDueDate(issuing core.Date) core.Date {
	due := p.clamped(issuing.Year, issuing.Month+time.Month(p.MonthOffset))
	if dateBefore(due, issuing) {
		due = p.clamped(due.Year, due.Month+1)
	}
	return due
}

func (p PaymentTerm) clamped(year int, month time.Month) core.Date {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return core.Date{Year: first.Year(), Month: first.Month(), Day: min(p.DayOfMonth, last)}
}

func dateBefore(a, b core.Date) bool {
	if a.Year != b.Year {
		return a.Year < b.Year
	}
	if a.Month != b.Month {
		return a.Month < b.Month
	}
	return a.Day < b.Day
}

// paymentTermText is the payment term row of an invoice: the structured
// term's sentence, or the legacy "N days" of the net payment term.
func (w *writer) paymentTermText(d *Invoice) string {
	if d.PaymentTerm.Type != "" {
		return d.PaymentTerm.label(w.l, d.Invoice.IssuingDate)
	}
	return w.l.T("invoice.payment_term_days", i18n.A("net_payment_term", strconv.Itoa(d.Invoice.NetPaymentTermDays)))
}
