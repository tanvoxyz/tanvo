package validators

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

func TestValidISO8601(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"2026-01-01", true},
		{"2026-01-01T10:00:00Z", true},
		{"2026-01-01T10:00+05:30", true},
		{"20260101", true},
		{"2026-032", true},
		{"26-01-01", true},
		// Ruby's Time rolls 30 February over to March.
		{"2026-02-30", true},
		{"2026-01-01T24:00:00Z", true},
		{"2026-01-01T24:00:01Z", false},
		{"2026-13-01", false},
		{"2026-W01-1", false},
		{"not-a-date", false},
		{"whenever", false},
		{"", false},
	}
	for _, c := range cases {
		if got := validISO8601(c.in); got != c.want {
			t.Errorf("validISO8601(%q): got = %v, want = %v", c.in, got, c.want)
		}
	}
}

func TestParseISO8601Date(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" when rejected
	}{
		{"2026-03-15", "2026-03-15"},
		{"2026-03-15T23:00:00-05:00", "2026-03-15"},
		{"2026-01-01T24:00:00Z", "2026-01-02"},
		{"2026-032", "2026-02-01"},
		{"2026-02-30", ""},
		{"bogus", ""},
	}
	for _, c := range cases {
		d, ok := parseISO8601Date(c.in)
		got := ""
		if ok {
			got = d.Format(time.DateOnly)
		}
		if got != c.want {
			t.Errorf("parseISO8601Date(%q): got = %q, want = %q", c.in, got, c.want)
		}
	}
}

func TestValidDateTimeFormat(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"2026-01-01T00:00:00Z", true},
		{"2026-01-01t00:00:00z", true},
		{"2026-01-01T00:00:00.123+02:00", true},
		{"2026-12-31T23:59:60Z", true},
		{"2026-01-01T12:00:60Z", false},
		{"2026-01-01T24:00:00Z", false},
		{"2026-02-30T00:00:00Z", false},
		{"2026-01-01T00:00:00+24:00", false},
		{"2026-01-01", false},
		{"not-a-date", false},
	}
	for _, c := range cases {
		if got := validDateTimeFormat(c.in); got != c.want {
			t.Errorf("validDateTimeFormat(%q): got = %v, want = %v", c.in, got, c.want)
		}
	}
}

func TestValidUUID(t *testing.T) {
	cases := map[string]bool{
		"48e59220-6722-49c1-8cdf-eacd040e2a56": true,
		"48E59220-6722-49C1-8CDF-EACD040E2A56": true,
		"00000000-0000-0000-0000-000000000000": true,
		"48e59220672249c18cdfeacd040e2a56":     false,
		"not-a-uuid":                           false,
	}
	for in, want := range cases {
		if got := validUUID(in); got != want {
			t.Errorf("validUUID(%q): got = %v, want = %v", in, got, want)
		}
	}
}

func TestRubyDecimal(t *testing.T) {
	cases := []struct {
		in         string
		want       string // "" when BigDecimal raises
		infinitely bool
	}{
		{in: "1.5", want: "1.5"},
		{in: " 2 ", want: "2"},
		{in: "1_000", want: "1000"},
		{in: "1e3", want: "1000"},
		{in: "1d3", want: "1000"},
		{in: ".5", want: "0.5"},
		{in: "-0.25", want: "-0.25"},
		{in: "Infinity", infinitely: true},
		{in: "-NaN", infinitely: true},
		{in: "1__0"},
		{in: "abc"},
		{in: ""},
	}
	for _, c := range cases {
		d, ok, finite := rubyDecimal(c.in)
		switch {
		case c.infinitely:
			if !ok || finite {
				t.Errorf("rubyDecimal(%q): got = ok %v finite %v, want = ok true finite false", c.in, ok, finite)
			}
		case c.want == "":
			if ok {
				t.Errorf("rubyDecimal(%q): got = %s, want = an error", c.in, d)
			}
		default:
			if !ok || !finite || d.Cmp(money.MustParse(c.want)) != 0 {
				t.Errorf("rubyDecimal(%q): got = %s (ok %v), want = %s", c.in, d, ok, c.want)
			}
		}
	}
}

func TestUnderscore(t *testing.T) {
	cases := map[string]string{
		"graduatedRanges":         "graduated_ranges",
		"perUnitAmount":           "per_unit_amount",
		"graduated_ranges":        "graduated_ranges",
		"HTMLParser":              "html_parser",
		"pricingGroupKeys":        "pricing_group_keys",
		"free-units":              "free_units",
		"amount":                  "amount",
		"perTransactionMaxAmount": "per_transaction_max_amount",
		"rate2Value":              "rate2_value",
	}
	for in, want := range cases {
		if got := underscore(in); got != want {
			t.Errorf("underscore(%q): got = %q, want = %q", in, got, want)
		}
	}
}

func TestUnderscoreKeysKeepsCustomProperties(t *testing.T) {
	v, err := decodeValue([]byte(`{"customProperties":{"someKey":1},"graduatedRanges":[{"fromValue":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := underscoreKeys(v)
	custom, _ := got["custom_properties"].(map[string]any)
	if _, ok := custom["someKey"]; !ok {
		t.Errorf("custom_properties: got = %v, want = someKey kept", got["custom_properties"])
	}
	ranges, _ := got["graduated_ranges"].([]any)
	if len(ranges) != 1 {
		t.Fatalf("graduated_ranges: got = %v, want = one range", got["graduated_ranges"])
	}
	if _, ok := ranges[0].(map[string]any)["from_value"]; !ok {
		t.Errorf("range keys: got = %v, want = from_value", ranges[0])
	}
}

func TestPropertyErrorsRaise(t *testing.T) {
	cases := []struct {
		name  string
		model pricing.ChargeModel
		props string
		want  bool
	}{
		{"graduated ranges a string", pricing.ChargeModel("graduated"), `{"graduated_ranges":"one to ten"}`, true},
		{"graduated ranges false", pricing.ChargeModel("graduated"), `{"graduated_ranges":false}`, false},
		{"graduated ranges of strings", pricing.ChargeModel("graduated"), `{"graduated_ranges":["a"]}`, true},
		{"volume to_value a string", pricing.ChargeModel("volume"), `{"volume_ranges":[{"from_value":0,"to_value":"x","per_unit_amount":"1","flat_amount":"0"}]}`, true},
		{"standard amount a string", pricing.ChargeModel("standard"), `{"amount":"not a number"}`, false},
		{"presentation keys true", pricing.ChargeModel("standard"), `{"amount":"1","presentation_group_keys":true}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, err := decodeValue([]byte(c.props))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, got := propertyErrors(c.model, underscoreKeys(v), false); got != c.want {
				t.Errorf("raised: got = %v, want = %v", got, c.want)
			}
		})
	}
}
