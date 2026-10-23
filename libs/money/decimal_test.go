package money

import (
	"encoding/json"
	"testing"
)

func TestArithmetic(t *testing.T) {
	tests := []struct {
		name string
		got  Decimal
		want string
	}{
		{"add", MustParse("0.1").Add(MustParse("0.2")), "0.3"},
		{"sub", MustParse("1").Sub(MustParse("1.25")), "-0.25"},
		{"mul exact", MustParse("123456789.123456789").Mul(MustParse("1000.001")), "123456912580.245912456789"},
		{"div rounds to 40 digits", One.Div(NewFromInt(3)), "0.3333333333333333333333333333333333333333"},
		{"div exact reduces", NewFromInt(10).Div(NewFromInt(4)), "2.5"},
		{"div or zero", NewFromInt(10).DivOrZero(Zero), "0"},
		{"neg", MustParse("2.5").Neg(), "-2.5"},
		{"neg zero", Zero.Neg(), "0"},
		{"abs", MustParse("-2.5").Abs(), "2.5"},
		{"pow", MustParse("1.1").Pow(3), "1.331"},
		{"sum", Sum(MustParse("1"), MustParse("2.5"), MustParse("-0.5")), "3.0"},
		{"min", Min(MustParse("3"), MustParse("-1"), MustParse("2")), "-1"},
		{"max", Max(MustParse("3"), MustParse("-1"), MustParse("2")), "3"},
		{"reduce", MustParse("1.500").Reduce(), "1.5"},
		{"reduce integer", MustParse("100").Reduce(), "100"},
		{"scientific", MustParse("1e-3"), "0.001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.String(); got != tt.want {
				t.Errorf("got = %s, want = %s", got, tt.want)
			}
		})
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		in     string
		places int32
		mode   RoundingMode
		want   string
	}{
		{"1.005", 2, HalfUp, "1.01"},
		{"1.005", 2, HalfEven, "1.00"},
		{"1.015", 2, HalfEven, "1.02"},
		{"1.005", 2, HalfDown, "1.00"},
		{"-1.005", 2, HalfUp, "-1.01"},
		{"1.001", 2, Up, "1.01"},
		{"-1.001", 2, Up, "-1.01"},
		{"1.009", 2, Down, "1.00"},
		{"-1.001", 2, Ceiling, "-1.00"},
		{"-1.001", 2, Floor, "-1.01"},
		{"999.995", 2, HalfUp, "1000.00"},
		{"0.001", 2, HalfUp, "0.00"},
		{"-0.001", 2, HalfUp, "0.00"},
		{"0", 3, HalfUp, "0.000"},
		{"1.5", 0, HalfUp, "2"},
		{"2.5", 0, HalfEven, "2"},
		{"1234.5", -2, HalfUp, "1200"},
		{"1.2", 4, HalfUp, "1.2000"},
		{"0.0322", 0, Ceiling, "1"},
		{"0.000001", 1, Ceiling, "0.1"},
		{"0.000001", 1, Up, "0.1"},
		{"-0.000001", 1, Up, "-0.1"},
		{"-0.000001", 1, Floor, "-0.1"},
		{"-0.000001", 1, Ceiling, "0.0"},
		{"0.000001", 1, Floor, "0.0"},
		{"0.000001", 1, HalfUp, "0.0"},
		{"0.0004", -2, Up, "100"},
		{"123456789012345678901234567890.123456789", 3, HalfUp, "123456789012345678901234567890.123"},
	}
	for _, tt := range tests {
		got := MustParse(tt.in).Round(tt.places, tt.mode).String()
		if got != tt.want {
			t.Errorf("Round(%s, %d, %s): got = %s, want = %s", tt.in, tt.places, tt.mode, got, tt.want)
		}
	}
}

func TestComparisons(t *testing.T) {
	a, b := MustParse("1.0"), MustParse("1.00")
	if !a.Equal(b) || a.Cmp(b) != 0 {
		t.Errorf("1.0 and 1.00: got unequal, want equal")
	}
	if !MustParse("1").LessThan(MustParse("1.0001")) {
		t.Errorf("1 < 1.0001: got false")
	}
	if MustParse("-0").Sign() != 0 || !MustParse("-0").IsZero() {
		t.Errorf("-0: got non-zero")
	}
	if !MustParse("2.000").IsInteger() || MustParse("2.001").IsInteger() {
		t.Errorf("IsInteger: got wrong answer")
	}
}

func TestInt64(t *testing.T) {
	n, ok := MustParse("-12.9").Int64()
	if !ok || n != -12 {
		t.Errorf("Int64(-12.9): got = %d %v, want = -12 true", n, ok)
	}
	if _, ok := MustParse("1e30").Int64(); ok {
		t.Errorf("Int64(1e30): got ok, want overflow")
	}
}

func TestParse(t *testing.T) {
	for _, s := range []string{"", "abc", "NaN", "Infinity", "1.2.3"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q): got nil error", s)
		}
	}
	if d, err := Parse(" 42.10 "); err != nil || d.String() != "42.10" {
		t.Errorf("Parse(\" 42.10 \"): got = %v %v", d, err)
	}
}

func TestNewFromFloat(t *testing.T) {
	d, err := NewFromFloat(0.1)
	if err != nil || d.String() != "0.1" {
		t.Errorf("NewFromFloat(0.1): got = %v %v, want = 0.1", d, err)
	}
}

func TestJSON(t *testing.T) {
	var v struct {
		A Decimal `json:"a"`
		B Decimal `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":"12.50","b":3.25}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A.String() != "12.50" || v.B.String() != "3.25" {
		t.Errorf("unmarshal: got = %s %s", v.A, v.B)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":"12.50","b":"3.25"}` {
		t.Errorf("marshal: got = %s", out)
	}
	if err := json.Unmarshal([]byte(`{"a":null}`), &v); err == nil {
		t.Errorf("unmarshal null: got nil error")
	}
}

func TestScan(t *testing.T) {
	var d Decimal
	for _, src := range []any{"1.5", []byte("1.5"), int64(3), 2.25} {
		if err := d.Scan(src); err != nil {
			t.Errorf("Scan(%#v): %v", src, err)
		}
	}
	if err := d.Scan(nil); err == nil {
		t.Errorf("Scan(nil): got nil error")
	}
}

func TestDivByZeroPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("Div by zero: got no panic")
		}
	}()
	One.Div(Zero)
}
