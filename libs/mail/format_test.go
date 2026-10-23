package mail

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestLocalizerDate(t *testing.T) {
	c := newComposer(t)
	d := core.NewDate(2024, time.March, 5)
	tests := []struct{ locale, want string }{
		{"en", "Mar 05, 2024"},
		{"de", "05. Mär. 2024"},
		{"fr", "5 mars 2024"},
		{"zh-TW", "2024年3月5日"},
		{"zh-CN", "2024年3月5日"},
	}
	for _, tt := range tests {
		got, err := c.Localizer(tt.locale).Date(d)
		if err != nil {
			t.Fatalf("Date(%s) error = %v", tt.locale, err)
		}
		if got != tt.want {
			t.Errorf("Date(%s) = %q, want = %q", tt.locale, got, tt.want)
		}
	}
}

func TestLocalizerMoney(t *testing.T) {
	c := newComposer(t)
	tests := []struct {
		locale, amount, code, want string
	}{
		{"en", "1234.5", "USD", "$1,234.50"},
		{"en", "1234567.891", "EUR", "€1,234,567.89"},
		{"en", "0.005", "USD", "$0.01"},
		{"en", "-12.3", "GBP", "£-12.30"},
		{"en", "1500", "JPY", "¥1,500"},
		{"en", "2970", "CHF", "CHF 2,970.00"},
		{"en", "1.2345", "BHD", "BHD 1.235"},
		{"de", "1234.5", "EUR", "€1.234,50"},
		{"fr", "1234.5", "EUR", "1 234,50 €"},
		{"fr", "1234.5", "CHF", "1 234,50 CHF"},
		{"zh-TW", "1234.5", "CHF", "CHF 1,234.50"},
		{"zh-CN", "1234.5", "CHF", "CHF 1,234.50"},
		{"zh-CN", "1234.5", "CNY", "¥1,234.50"},
	}
	for _, tt := range tests {
		got, err := c.Localizer(tt.locale).Money(money.MustParse(tt.amount), tt.code)
		if err != nil {
			t.Fatalf("Money(%s, %s %s) error = %v", tt.locale, tt.amount, tt.code, err)
		}
		if got != tt.want {
			t.Errorf("Money(%s, %s %s) = %q, want = %q", tt.locale, tt.amount, tt.code, got, tt.want)
		}
	}
	if _, err := c.Localizer("en").Money(money.MustParse("1"), "XXX1"); err == nil {
		t.Errorf("Money(unknown currency) error = nil, want an error")
	}
}
