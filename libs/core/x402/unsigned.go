package x402

import (
	"encoding/json"
	"math/big"
	"regexp"
)

var digits = regexp.MustCompile(`\A[0-9]+\z`)

// ParseUnsigned reads v as an unsigned integer: a non-negative integer
// value, a json.Number or a string of decimal digits only (a leading zero
// stays decimal). Anything else, including fractions, signs, exponents
// and hexadecimal, is false. JSON payloads are decoded with
// json.Decoder.UseNumber so that their integers stay exact.
//
// source: app/services/x402/unsigned_integer.rb
func ParseUnsigned(v any) (*big.Int, bool) {
	switch x := v.(type) {
	case int:
		return nonNegative(big.NewInt(int64(x)))
	case int64:
		return nonNegative(big.NewInt(x))
	case uint64:
		return new(big.Int).SetUint64(x), true
	case *big.Int:
		if x == nil {
			return nil, false
		}
		return nonNegative(new(big.Int).Set(x))
	case json.Number:
		return parseDigits(string(x))
	case string:
		return parseDigits(x)
	default:
		return nil, false
	}
}

func nonNegative(n *big.Int) (*big.Int, bool) {
	if n.Sign() < 0 {
		return nil, false
	}
	return n, true
}

func parseDigits(s string) (*big.Int, bool) {
	if !digits.MatchString(s) {
		return nil, false
	}
	return new(big.Int).SetString(s, 10)
}
