package x402

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Payment digest failures: the code of the payment field error.
const (
	CodeInvalidPayment       = "invalid_payment"
	CodeUnsupportedNetwork   = "unsupported_network"
	CodeInvalidAsset         = "invalid_asset"
	CodeInvalidAuthorization = "invalid_authorization"
	CodeInvalidTransaction   = "invalid_transaction"
)

var nonceWord = regexp.MustCompile(`\A0x[0-9a-fA-F]{64}\z`)

var authorizationFields = []string{"from", "to", "value", "validAfter", "validBefore", "nonce"}

// DigestError is a payment that has no digest; Code is one of the
// payment digest failure codes.
type DigestError struct{ Code string }

func (e *DigestError) Error() string { return "x402 payment digest: " + e.Code }

// ComputeDigest returns the hexadecimal SHA-256 of the canonical form of
// payment, which identifies one transfer whatever the encoding: for EVM,
// the network, the checksummed asset, payer and payee, the amounts and
// the lowercased nonce of the authorization; for Solana, the network and
// the signed message of the transaction, without its signatures. asset
// is the required token address (EVM only). The failure is a
// *DigestError.
//
// source: app/services/x402/payments/compute_digest_service.rb
func ComputeDigest(payment Object, network Network, asset string) (string, error) {
	if payment == nil {
		return "", &DigestError{CodeInvalidPayment}
	}
	f, ok := network.Family()
	if !ok {
		return "", &DigestError{CodeUnsupportedNetwork}
	}
	var canonical []any
	var err error
	if f == FamilyEVM {
		canonical, err = evmCanonical(payment, network, asset)
	} else {
		canonical, err = svmCanonical(payment, network)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("x402 payment digest: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func evmCanonical(payment Object, network Network, asset string) ([]any, error) {
	if !IsEVMAddress(asset) {
		return nil, &DigestError{CodeInvalidAsset}
	}
	a, ok := object(payment["payload"])["authorization"].(map[string]any)
	if !ok || !validAuthorization(a) {
		return nil, &DigestError{CodeInvalidAuthorization}
	}
	value, _ := ParseUnsigned(a["value"])
	after, _ := ParseUnsigned(a["validAfter"])
	before, _ := ParseUnsigned(a["validBefore"])
	assetSum, _ := Checksum(asset)
	from, _ := Checksum(a["from"].(string))
	to, _ := Checksum(a["to"].(string))
	return []any{"evm", network, assetSum, from, to, value, after, before, strings.ToLower(a["nonce"].(string))}, nil
}

func validAuthorization(a Object) bool {
	for _, f := range authorizationFields {
		if _, ok := a[f]; !ok {
			return false
		}
	}
	if !IsEVMAddress(str(a["from"])) || !IsEVMAddress(str(a["to"])) || !nonceWord.MatchString(str(a["nonce"])) {
		return false
	}
	for _, f := range []string{"value", "validAfter", "validBefore"} {
		if _, ok := ParseUnsigned(a[f]); !ok {
			return false
		}
	}
	return true
}

func svmCanonical(payment Object, network Network) ([]any, error) {
	tx := str(object(payment["payload"])["transaction"])
	if tx == "" {
		return nil, &DigestError{CodeInvalidTransaction}
	}
	b, err := base64.StdEncoding.Strict().DecodeString(tx)
	if err != nil {
		return nil, &DigestError{CodeInvalidTransaction}
	}
	msg := signedMessage(b)
	if msg == nil {
		return nil, &DigestError{CodeInvalidTransaction}
	}
	return []any{"svm", network, base64.StdEncoding.EncodeToString(msg)}, nil
}

// signedMessage returns the message after the 1 to 127 signatures of a
// Solana transaction, or nil.
func signedMessage(b []byte) []byte {
	if len(b) == 0 || b[0] < 1 || b[0] > 0x7f {
		return nil
	}
	start := 1 + int(b[0])*64
	if start >= len(b) {
		return nil
	}
	return b[start:]
}
