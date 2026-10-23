package metering

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestRounding(t *testing.T) {
	tests := []struct {
		in     string
		places int32
		mode   money.RoundingMode
		want   string
	}{
		{"0.0322", 0, money.Ceiling, "1"},
		{"0.000001", 5, money.Ceiling, "0.00001"},
		{"-0.03", 0, money.Ceiling, "0"},
		{"-0.03", 0, money.Floor, "-1"},
		{"0.5", -1, money.Ceiling, "10"},
		{"123.456", 2, money.Ceiling, "123.46"},
		{"123.456", 2, money.Floor, "123.45"},
		{"123.456", 1, money.HalfUp, "123.5"},
		{"123.45", 1, money.HalfUp, "123.5"},
		{"-123.45", 1, money.HalfUp, "-123.5"},
		{"123.456", -2, money.HalfUp, "100"},
		{"0.000006", 5, money.HalfUp, "0.00001"},
		{"0.000004", 5, money.HalfUp, "0"},
		{"5", 0, money.Ceiling, "5"},
	}
	for _, tt := range tests {
		got := money.MustParse(tt.in).Round(tt.places, tt.mode)
		if !got.Equal(money.MustParse(tt.want)) {
			t.Errorf("Round(%s, %d, %v) got = %s, want = %s", tt.in, tt.places, tt.mode, got, tt.want)
		}
	}
}
