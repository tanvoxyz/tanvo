package money

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLookupCurrency(t *testing.T) {
	tests := []struct {
		code     string
		exponent int32
		numeric  string
	}{
		{"EUR", 2, "978"},
		{"usd", 2, "840"},
		{"JPY", 0, "392"},
		{"KWD", 3, "414"},
		{"CLF", 4, "990"},
		{"HUF", 2, "348"},
		{"MRU", 2, "929"},
	}
	for _, tt := range tests {
		c, err := LookupCurrency(tt.code)
		if err != nil {
			t.Errorf("LookupCurrency(%q): %v", tt.code, err)
			continue
		}
		if c.Exponent() != tt.exponent || c.Numeric() != tt.numeric {
			t.Errorf("%s: got = exponent %d numeric %s, want = %d %s", tt.code, c.Exponent(), c.Numeric(), tt.exponent, tt.numeric)
		}
	}
	for _, code := range []string{"HRK", "XXX", "", "EURO"} {
		if _, err := LookupCurrency(code); !errors.Is(err, ErrUnknownCurrency) {
			t.Errorf("LookupCurrency(%q): got = %v, want = ErrUnknownCurrency", code, err)
		}
	}
}

func TestCurrenciesSorted(t *testing.T) {
	codes := slices.Collect(func(yield func(string) bool) {
		for c := range Currencies() {
			if !yield(c.Code()) {
				return
			}
		}
	})
	if !slices.IsSortedFunc(codes, strings.Compare) {
		t.Errorf("currency table is not sorted by code")
	}
	if len(codes) < 140 {
		t.Errorf("got = %d currencies, want ≥ 140", len(codes))
	}
}

func TestMinorUnits(t *testing.T) {
	eur, jpy, kwd := MustCurrency("EUR"), MustCurrency("JPY"), MustCurrency("KWD")
	tests := []struct {
		c    Currency
		in   string
		mode RoundingMode
		want int64
	}{
		{eur, "12.345", HalfUp, 1235},
		{eur, "12.345", HalfEven, 1234},
		{eur, "-12.345", HalfUp, -1235},
		{jpy, "1234.5", HalfUp, 1235},
		{kwd, "1.0005", HalfUp, 1001},
		{eur, "0", HalfUp, 0},
	}
	for _, tt := range tests {
		got, err := tt.c.ToMinor(MustParse(tt.in), tt.mode)
		if err != nil || got != tt.want {
			t.Errorf("%s.ToMinor(%s): got = %d %v, want = %d", tt.c, tt.in, got, err, tt.want)
		}
	}
	if got := eur.FromMinor(1235).String(); got != "12.35" {
		t.Errorf("FromMinor(1235): got = %s, want = 12.35", got)
	}
	if got := jpy.FromMinor(1235).String(); got != "1235" {
		t.Errorf("JPY FromMinor(1235): got = %s, want = 1235", got)
	}
	if got := eur.MinorToUnits(MustParse("1234.56")).String(); got != "12.3456" {
		t.Errorf("MinorToUnits(1234.56): got = %s, want = 12.3456", got)
	}
	if _, err := eur.ToMinor(MustParse("1e30"), HalfUp); !errors.Is(err, ErrOverflow) {
		t.Errorf("ToMinor(1e30): got = %v, want = ErrOverflow", err)
	}
}

func TestCustomCurrency(t *testing.T) {
	c, err := NewCustomCurrency("credits", "Credits", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsCustom() || c.Exponent() != 4 || c.Round(MustParse("1.23456"), HalfUp).String() != "1.2346" {
		t.Errorf("custom currency: got = %v", c)
	}
	if _, err := NewCustomCurrency("bad code", "x", 2); err == nil {
		t.Errorf("invalid code: got nil error")
	}
	if _, err := NewCustomCurrency("x", "x", 16); err == nil {
		t.Errorf("exponent 16: got nil error")
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		total   int64
		weights []string
		want    []int64
	}{
		{100, []string{"1", "1", "1"}, []int64{34, 33, 33}},
		{-100, []string{"1", "1", "1"}, []int64{-34, -33, -33}},
		{1000, []string{"50", "30", "20"}, []int64{500, 300, 200}},
		{5, []string{"0.3", "0.3", "0.4"}, []int64{2, 1, 2}},
		{7, []string{"0", "1"}, []int64{0, 7}},
	}
	for _, tt := range tests {
		ws := make([]Decimal, len(tt.weights))
		for i, w := range tt.weights {
			ws[i] = MustParse(w)
		}
		got, err := Allocate(tt.total, ws)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("Allocate(%d, %v): got = %v %v, want = %v", tt.total, tt.weights, got, err, tt.want)
		}
	}
	if _, err := Allocate(1, []Decimal{Zero}); err == nil {
		t.Errorf("zero weights: got nil error")
	}
}

func TestProrate(t *testing.T) {
	if got := Prorate(MustParse("31"), NewFromInt(10), NewFromInt(31)).String(); got != "10" {
		t.Errorf("Prorate: got = %s, want = 10", got)
	}
	if got := Prorate(MustParse("31"), NewFromInt(10), Zero); !got.IsZero() {
		t.Errorf("Prorate by zero: got = %s, want = 0", got)
	}
}
