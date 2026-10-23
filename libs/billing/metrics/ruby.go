package metrics

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// rubyText converts a JSON property value to the text Ruby's to_s gives
// it, as lago's evaluate service converts every property
// (evaluate_expression_service.rb:26, transform_values(&:to_s)) of the
// parsed request parameters:
//
//   - null is "", strings are themselves, true and false are "true" and
//     "false";
//   - numbers are Integer#to_s or Float#to_s. A JSON number reaches Tanvo as
//     a float64 (google.protobuf.Struct), which loses whether it was
//     written with a fraction; an integral number below 10^16 is rendered
//     as the Integer Ruby parses "2" to ("2"), any other as Float#to_s
//     ("10.4", "1.0e+16", "1.0e-05");
//   - objects and arrays are Hash#inspect and Array#inspect in the Ruby
//     4.0 notation ({"a" => 1}, [1, "b"]).
func rubyText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		var b strings.Builder
		rubyInspect(&b, v)
		return b.String()
	}
}

// rubyInspect writes Ruby's inspect of the JSON value v to b.
func rubyInspect(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("nil")
	case string:
		rubyQuote(b, x)
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case float64:
		b.WriteString(rubyNumber(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		// Ruby keeps the insertion order, which a decoded JSON object no
		// longer has; keys are written in sorted order.
		b.WriteByte('{')
		for i, k := range slices.Sorted(maps.Keys(x)) {
			if i > 0 {
				b.WriteString(", ")
			}
			rubyQuote(b, k)
			b.WriteString(" => ")
			rubyInspect(b, x[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			rubyInspect(b, e)
		}
		b.WriteByte(']')
	default:
		fmt.Fprint(b, x)
	}
}

// rubyNumber renders a JSON number: integral values below 10^16 as an
// Integer, the others as Float#to_s.
func rubyNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return rubyFloat(f)
}

// rubyFloat renders f as Ruby's Float#to_s (numeric.c flo_to_s): the
// shortest digits that round-trip, in fixed notation with at least one
// fractional digit when the decimal exponent is within (-4, 16], in
// exponential notation with a signed two-digit exponent otherwise.
func rubyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	sign := ""
	if math.Signbit(f) {
		sign, f = "-", -f
	}
	if f == 0 {
		return sign + "0.0"
	}
	// d.ddde±x: digits and the position of the decimal point (value =
	// 0.digits × 10^decpt).
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	decpt := x + 1
	switch {
	case decpt > 0 && decpt <= 16:
		if decpt >= len(digits) {
			return sign + digits + strings.Repeat("0", decpt-len(digits)) + ".0"
		}
		return sign + digits[:decpt] + "." + digits[decpt:]
	case decpt <= 0 && decpt > -4:
		return sign + "0." + strings.Repeat("0", -decpt) + digits
	default:
		frac := digits[1:]
		if frac == "" {
			frac = "0"
		}
		es := "+"
		if decpt-1 < 0 {
			es = "-"
		}
		n := decpt - 1
		if n < 0 {
			n = -n
		}
		return fmt.Sprintf("%s%s.%se%s%02d", sign, digits[:1], frac, es, n)
	}
}

// rubyQuote writes Ruby's String#inspect of s to b.
func rubyQuote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\f':
			b.WriteString(`\f`)
		case '\v':
			b.WriteString(`\v`)
		case '\b':
			b.WriteString(`\b`)
		case '\a':
			b.WriteString(`\a`)
		case 0x1b:
			b.WriteString(`\e`)
		case '#':
			// "#{", "#$" and "#@" would interpolate.
			if n := s[i+1:]; n != "" && (n[0] == '{' || n[0] == '$' || n[0] == '@') {
				b.WriteByte('\\')
			}
			b.WriteByte('#')
		default:
			switch {
			case r == utf8.RuneError:
				fmt.Fprintf(b, `\x%02X`, s[i])
			case r == 0x7f:
				b.WriteString(`\x7F`)
			case unicode.IsPrint(r) || r == ' ':
				b.WriteRune(r)
			case r < 0x10000:
				fmt.Fprintf(b, `\u%04X`, r)
			default:
				fmt.Fprintf(b, `\u{%X}`, r)
			}
		}
	}
	b.WriteByte('"')
}
