package metrics

import "testing"

func TestRubyText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"null", nil, ""},
		{"string", "2.4", "2.4"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"integer", float64(2), "2"},
		{"negative integer", float64(-12), "-12"},
		{"large integer", 1e15, "1000000000000000"},
		{"float", 10.4, "10.4"},
		{"negative float", -0.5, "-0.5"},
		{"small float", 0.0001, "0.0001"},
		{"tiny float", 0.00001, "1.0e-05"},
		{"tiny float with digits", 1.5e-7, "1.5e-07"},
		{"huge float", 1e16, "1.0e+16"},
		{"huge float with digits", 1.25e20, "1.25e+20"},
		{"object", map[string]any{"b": float64(1), "a": "x"}, `{"a" => "x", "b" => 1}`},
		{"empty object", map[string]any{}, "{}"},
		{"array", []any{float64(1), "b", nil, true, 2.5}, `[1, "b", nil, true, 2.5]`},
		{"nested", map[string]any{"a": []any{map[string]any{"b": nil}}}, `{"a" => [{"b" => nil}]}`},
		{"escapes", []any{"a\"b\\c\n\t#{x}#y\x01é"}, `["a\"b\\c\n\t\#{x}#y\u0001é"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rubyText(tc.in); got != tc.want {
				t.Errorf("rubyText(%#v) got = %q, want = %q", tc.in, got, tc.want)
			}
		})
	}
}
