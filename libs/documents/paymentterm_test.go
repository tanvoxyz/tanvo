package documents

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Ports of PaymentTerm#label in spec/models/payment_term_spec.rb (lago-api
// payment terms branch, 8ceddebb7).

func TestPaymentTermLabel(t *testing.T) {
	const src = "spec/models/payment_term_spec.rb:"
	en := testCatalog(t).Locale("en")
	fr := testCatalog(t).Locale("fr")
	dom := func(day, offset int) PaymentTerm {
		return PaymentTerm{Type: PaymentTermDayOfMonth, DayOfMonth: day, MonthOffset: offset}
	}
	for _, c := range []struct {
		line    string
		term    PaymentTerm
		issuing string
		fr      bool
		want    string
	}{
		{"105", PaymentTerm{Type: PaymentTermDueOnReceipt}, "", false, "Due on receipt"},
		{"105", PaymentTerm{Type: PaymentTermNet, Days: 1}, "", false, "Payment due within 1 day"},
		{"105", PaymentTerm{Type: PaymentTermNet, Days: 30}, "", false, "Payment due within 30 days"},
		{"105", PaymentTerm{Type: PaymentTermEndOfMonth}, "", false, "Due at end of month"},
		{"105", PaymentTerm{Type: PaymentTermNetEndOfMonth, Days: 1}, "", false, "Due 1 day after end of month"},
		{"105", PaymentTerm{Type: PaymentTermNetEndOfMonth, Days: 30}, "", false, "Due 30 days after end of month"},
		{"105", PaymentTerm{Type: PaymentTermDaysEndOfMonth, Days: 1}, "", false, "Due 1 day, end of month"},
		{"105", PaymentTerm{Type: PaymentTermDaysEndOfMonth, Days: 30}, "", false, "Due 30 days, end of month"},
		{"116", PaymentTerm{Type: PaymentTermNet, Days: 0}, "", false, "Payment due within 0 days"},
		{"120", dom(15, 0), "", false, "Due on the 15 of this month"},
		// lago's default month_offset is 1; Tanvo's terms always carry it.
		{"120", dom(15, 1), "", false, "Due on the 15 of the following month"},
		{"120", dom(15, 2), "", false, "Due on the 15, 2 months after invoice"},
		{"130", dom(10, 0), "2026-07-15", false, "Due on the 10 of the following month"},
		{"130", dom(10, 0), "2026-07-05", false, "Due on the 10 of this month"},
		{"137", dom(31, 1), "2026-01-15", false, "Due on the 28 of the following month"},
		{"137", dom(31, 1), "2028-01-15", false, "Due on the 29 of the following month"},
		{"137", dom(31, 1), "2026-07-15", false, "Due on the 31 of the following month"},
		{"145", dom(30, 0), "2027-01-31", false, "Due on the 28 of the following month"},
		{"153", PaymentTerm{Type: PaymentTermEndOfMonth}, "", true, "Payable fin de mois"},
	} {
		t.Run(c.want, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			l := en
			if c.fr {
				l = fr
			}
			var issuing core.Date
			if c.issuing != "" {
				issuing = day(c.issuing)
			}
			if got := c.term.label(l, issuing); got != c.want {
				t.Errorf("label: got = %q, want = %q", got, c.want)
			}
		})
	}
}

// source: spec/models/payment_term_spec.rb:148
func TestDayOfMonthDueDateReclamps(t *testing.T) {
	term := PaymentTerm{Type: PaymentTermDayOfMonth, DayOfMonth: 30}
	if got, want := term.dayOfMonthDueDate(day("2027-01-31")), day("2027-02-28"); got != want {
		t.Errorf("due date: got = %v, want = %v", got, want)
	}
}
