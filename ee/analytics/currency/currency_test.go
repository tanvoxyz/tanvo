// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package currency

import (
	"maps"
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestExponents checks the exponents against the core's currency table.
func TestExponents(t *testing.T) {
	src, err := os.ReadFile("../../../libs/money/currencies_table.go")
	if err != nil {
		t.Skipf("core currency table not found: %v", err)
	}
	re := regexp.MustCompile(`code: "([A-Z]{3})", numeric: "[0-9]+", exponent: ([0-9]+)`)
	want := map[string]int32{}
	n := 0
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		n++
		e, _ := strconv.Atoi(m[2])
		if e != 2 {
			want[m[1]] = int32(e)
		}
	}
	if n < 100 {
		t.Fatalf("parsed %d currencies, want = at least 100", n)
	}
	if !maps.Equal(exponents, want) {
		t.Errorf("exponents got = %v, want = %v", exponents, want)
	}
}

func TestMajorString(t *testing.T) {
	for _, tt := range []struct{ minor, code, want string }{
		{"1250", "USD", "12.50"},
		{"0", "USD", "0.00"},
		{"", "EUR", "0.00"},
		{"-5", "USD", "-0.05"},
		{"1250", "JPY", "1250"},
		{"1250", "KWD", "1.250"},
		{"12345", "CLF", "1.2345"},
		{"333.333333333333", "USD", "3.33"},
		{"0.5", "USD", "0.00"},
		{"1.5", "USD", "0.02"},
		{"-0.4", "USD", "0.00"},
	} {
		got, err := MajorString(tt.minor, tt.code)
		if err != nil || got != tt.want {
			t.Errorf("MajorString(%q, %s) got = %q, %v, want = %q", tt.minor, tt.code, got, err, tt.want)
		}
	}
}
