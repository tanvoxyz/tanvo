package x402

import (
	"math/big"
	"strings"
)

// base58Alphabet is the Bitcoin base58 alphabet Solana uses.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var big58 = big.NewInt(58)

// Base58Encode returns the base58 text of b; each leading zero byte is a
// leading "1".
//
// source: app/services/x402/base58.rb:8-19
func Base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	var out []byte
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, big58, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// Base58Decode returns the bytes of the base58 text s; false when s is
// empty or has a character outside the alphabet.
//
// source: app/services/x402/base58.rb:21-29
func Base58Decode(s string) ([]byte, bool) {
	if s == "" {
		return nil, false
	}
	n := new(big.Int)
	for i := range len(s) {
		d := strings.IndexByte(base58Alphabet, s[i])
		if d < 0 {
			return nil, false
		}
		n.Mul(n, big58)
		n.Add(n, big.NewInt(int64(d)))
	}
	zeros := len(s) - len(strings.TrimLeft(s, "1"))
	return append(make([]byte, zeros), n.Bytes()...), true
}
