package validators

import (
	"regexp"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// bigDecimalRe is the finite syntax Ruby's BigDecimal(String) accepts once
// surrounding whitespace is stripped: a sign, digits with single
// underscores between them, an optional fraction and an exponent in e, E,
// d or D.
var (
	bigDecimalRe  = regexp.MustCompile(`\A[+-]?(?:\d(?:_?\d)*(?:\.(?:\d(?:_?\d)*)?)?|\.\d(?:_?\d)*)(?:[eEdD][+-]?\d+)?\z`)
	bigDecimalInf = regexp.MustCompile(`\A[+-]?(?:Infinity|NaN)\z`)
)

// rubyDecimal parses s the way Ruby's BigDecimal(String) does. ok is false
// when BigDecimal raises; finite is false for Infinity and NaN.
func rubyDecimal(s string) (d money.Decimal, ok, finite bool) {
	s = strings.TrimSpace(s)
	if bigDecimalInf.MatchString(s) {
		return money.Decimal{}, true, false
	}
	if !bigDecimalRe.MatchString(s) {
		return money.Decimal{}, false, false
	}
	n := strings.ReplaceAll(s, "_", "")
	n = strings.NewReplacer("d", "e", "D", "e").Replace(n)
	mantissa, exp, hasExp := strings.Cut(n, "e")
	if strings.HasSuffix(mantissa, ".") {
		mantissa += "0"
	}
	if i := strings.Index(mantissa, "."); i == 0 || (i == 1 && (mantissa[0] == '+' || mantissa[0] == '-')) {
		mantissa = mantissa[:i] + "0" + mantissa[i:]
	}
	if hasExp {
		mantissa += "e" + exp
	}
	v, err := money.Parse(mantissa)
	if err != nil {
		return money.Decimal{}, false, false
	}
	return v, true, true
}

// validDecimal is Validators::DecimalAmountService#valid_decimal?: a
// string BigDecimal parses to a finite number.
func validDecimal(v *value) (money.Decimal, bool) {
	s, isString := v.str()
	if !isString {
		return money.Decimal{}, false
	}
	d, ok, finite := rubyDecimal(s)
	return d, ok && finite
}

// validAmount is DecimalAmountService.valid_amount?: a valid decimal ≥ 0.
func validAmount(v *value) bool {
	d, ok := validDecimal(v)
	return ok && !d.IsNegative()
}

// validPositiveAmount is DecimalAmountService.valid_positive_amount?.
func validPositiveAmount(v *value) bool {
	d, ok := validDecimal(v)
	return ok && d.IsPositive()
}
