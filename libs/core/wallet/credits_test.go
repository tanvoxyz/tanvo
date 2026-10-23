package wallet

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// source: app/models/wallet_credit.rb:7 (from_amount_cents: amount.fdiv(rate), stored in numeric(30,5))
func TestCreditsFromMinor(t *testing.T) {
	eur := money.MustCurrency("EUR")
	tests := []struct {
		name   string
		amount int64
		rate   string
		want   string
	}{
		{"exact", 1000, "1", "10"},
		{"rounded half up", 2000, "3", "6.66667"},
		{"rounded down", 1000, "3", "3.33333"},
		{"small rate", 103, "0.001", "1030"},
	}
	for _, tt := range tests {
		got := CreditsFromMinor(tt.amount, eur, money.MustParse(tt.rate))
		if want := money.MustParse(tt.want); !got.Equal(want) {
			t.Errorf("%s: got = %s, want = %s", tt.name, got, want)
		}
	}
}
