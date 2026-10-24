package x402_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/payments/compute_digest_service_spec.rb

const (
	digestNetwork = x402.Network("eip155:84532")
	digestAsset   = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
)

var hexDigest = regexp.MustCompile(`\A[0-9a-f]{64}\z`)

func digestAuthorization(t *testing.T) x402.Object {
	return decode(t, `{"from":"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359","to":"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"value":"1000000","validAfter":"0","validBefore":"1790300000","nonce":"0x`+strings.Repeat("ab", 32)+`"}`)
}

func digestPayment(t *testing.T, auth x402.Object) x402.Object {
	p := decode(t, `{"x402Version":2,"resource":{"url":"https://api.example.com/report"},
		"accepted":{"scheme":"exact","network":"eip155:84532","amount":"1000000","asset":"`+digestAsset+`"},
		"payload":{"signature":"0x`+strings.Repeat("12", 65)+`"},"extensions":{}}`)
	p["payload"].(x402.Object)["authorization"] = auth
	return p
}

func mustDigest(t *testing.T, p x402.Object, n x402.Network, asset string) string {
	t.Helper()
	d, err := x402.ComputeDigest(p, n, asset)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return d
}

func digestCode(err error) string {
	var d *x402.DigestError
	if errors.As(err, &d) {
		return d.Code
	}
	return ""
}

func TestComputeDigestEVM(t *testing.T) {
	t.Parallel()
	base := mustDigest(t, digestPayment(t, digestAuthorization(t)), digestNetwork, digestAsset)
	// source: compute_digest_service_spec.rb:34
	if !hexDigest.MatchString(base) {
		t.Fatalf("digest: got = %q, want = 64 hex", base)
	}

	// source: compute_digest_service_spec.rb:38-62
	respelled := decode(t, `{"payload":{"authorization":{"nonce":"0x`+strings.Repeat("AB", 32)+`","validBefore":1790300000,
		"validAfter":0,"value":1000000,"to":"0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed",
		"from":"0xfb6916095ca1df60bb79ce92ce3ea74c37c5d359"},"signature":"0x`+strings.Repeat("34", 65)+`"},
		"accepted":{"scheme":"exact","network":"eip155:84532","amount":"1000000","asset":"`+strings.ToLower(digestAsset)+`","extra":{}},
		"x402Version":2}`)
	if got := mustDigest(t, respelled, digestNetwork, digestAsset); got != base {
		t.Errorf("respelled: got = %s, want = %s", got, base)
	}

	// source: compute_digest_service_spec.rb:64-70
	v1 := x402.Object{"x402Version": 1, "scheme": "exact", "network": "base-sepolia", "payload": digestPayment(t, digestAuthorization(t))["payload"]}
	if got := mustDigest(t, v1, digestNetwork, digestAsset); got != base {
		t.Errorf("v1 wrapper: got = %s, want = %s", got, base)
	}

	// source: compute_digest_service_spec.rb:72
	if got := mustDigest(t, digestPayment(t, digestAuthorization(t)), digestNetwork, strings.ToLower(digestAsset)); got != base {
		t.Errorf("asset case: got = %s, want = %s", got, base)
	}

	// source: compute_digest_service_spec.rb:76-82
	other := digestAuthorization(t)
	other["nonce"] = "0x" + strings.Repeat("cd", 32)
	if got := mustDigest(t, digestPayment(t, other), digestNetwork, digestAsset); got == base {
		t.Error("another nonce: got = same digest, want = another")
	}

	// source: compute_digest_service_spec.rb:84
	if got := mustDigest(t, digestPayment(t, digestAuthorization(t)), digestNetwork, "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"); got == base {
		t.Error("another asset: got = same digest, want = another")
	}

	// source: compute_digest_service_spec.rb:88
	if got := mustDigest(t, digestPayment(t, digestAuthorization(t)), "eip155:8453", digestAsset); got == base {
		t.Error("another network: got = same digest, want = another")
	}

	// source: compute_digest_service_spec.rb:240-247
	lead := digestAuthorization(t)
	lead["value"] = "01000000"
	if got := mustDigest(t, x402.Object{"payload": x402.Object{"authorization": lead}}, digestNetwork, digestAsset); got != base {
		t.Errorf("leading zero: got = %s, want = %s", got, base)
	}
}

func TestComputeDigestMalformed(t *testing.T) {
	t.Parallel()
	auth := func(field string, value any, drop bool) x402.Object {
		a := digestAuthorization(t)
		if drop {
			delete(a, field)
		} else {
			a[field] = value
		}
		return x402.Object{"payload": x402.Object{"authorization": a}}
	}
	type tc struct {
		name    string
		payment x402.Object
		network x402.Network
		asset   string
		want    string
	}
	cases := []tc{
		// source: compute_digest_service_spec.rb:186-190
		{"not a hash", nil, digestNetwork, digestAsset, x402.CodeInvalidPayment},
		// source: compute_digest_service_spec.rb:192-202
		{"unknown namespace", digestPayment(t, digestAuthorization(t)), "sui:mainnet", digestAsset, x402.CodeUnsupportedNetwork},
		{"network not accepted", digestPayment(t, digestAuthorization(t)), "eip155:1", digestAsset, x402.CodeUnsupportedNetwork},
		// source: compute_digest_service_spec.rb:204-208
		{"asset not an address", digestPayment(t, digestAuthorization(t)), digestNetwork, "USDC", x402.CodeInvalidAsset},
		// source: compute_digest_service_spec.rb:210-220
		{"payload not an object", x402.Object{"payload": "0xdeadbeef"}, digestNetwork, digestAsset, x402.CodeInvalidAuthorization},
		{"no authorization", x402.Object{"payload": x402.Object{"signature": "0x00"}}, digestNetwork, digestAsset, x402.CodeInvalidAuthorization},
		// source: compute_digest_service_spec.rb:228-232
		{"short nonce", auth("nonce", "0xabcd", false), digestNetwork, digestAsset, x402.CodeInvalidAuthorization},
		// source: compute_digest_service_spec.rb:234-238
		{"from not EVM", auth("from", "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4", false), digestNetwork, digestAsset, x402.CodeInvalidAuthorization},
	}
	// source: compute_digest_service_spec.rb:222-226
	for _, f := range []string{"from", "to", "value", "validAfter", "validBefore", "nonce"} {
		cases = append(cases, tc{"without " + f, auth(f, nil, true), digestNetwork, digestAsset, x402.CodeInvalidAuthorization})
	}
	// source: compute_digest_service_spec.rb:240-246
	for _, v := range []any{1.5, "1.5", "010x", "0x10", "1_000", "-1", -1} {
		cases = append(cases, tc{"value", auth("value", v, false), digestNetwork, digestAsset, x402.CodeInvalidAuthorization})
	}
	for _, c := range cases {
		_, err := x402.ComputeDigest(c.payment, c.network, c.asset)
		if got := digestCode(err); got != c.want {
			t.Errorf("%s: got = %q (%v), want = %q", c.name, got, err, c.want)
		}
	}
}

func TestComputeDigestSVM(t *testing.T) {
	t.Parallel()
	const network = x402.Network("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1")
	signatures := [][]byte{sig(0), sig(0x11)}
	message := bytes.Repeat([]byte{0x80}, 40)
	encode := func(sigs [][]byte, msg []byte) string {
		b := []byte{byte(len(sigs))}
		for _, s := range sigs {
			b = append(b, s...)
		}
		return base64.StdEncoding.EncodeToString(append(b, msg...))
	}
	payment := func(tx string) x402.Object {
		return x402.Object{"x402Version": 2, "accepted": x402.Object{"network": string(network)}, "payload": x402.Object{"transaction": tx}}
	}
	digestOf := func(tx string) (string, error) {
		return x402.ComputeDigest(x402.Object{"payload": x402.Object{"transaction": tx}}, network, "")
	}
	tx := encode(signatures, message)
	base, err := x402.ComputeDigest(payment(tx), network, "")
	// source: compute_digest_service_spec.rb:106
	if err != nil || !hexDigest.MatchString(base) {
		t.Fatalf("digest: got = %q, %v, want = 64 hex", base, err)
	}
	// source: compute_digest_service_spec.rb:110
	if got, _ := digestOf(tx); got != base {
		t.Errorf("envelope: got = %s, want = %s", got, base)
	}
	// source: compute_digest_service_spec.rb:114
	if got, _ := digestOf(encode([][]byte{sig(0xab), signatures[1]}, message)); got != base {
		t.Errorf("fee payer slot: got = %s, want = %s", got, base)
	}
	// source: compute_digest_service_spec.rb:118
	if got, _ := digestOf(encode(signatures, bytes.Repeat([]byte{0x81}, 40))); got == base {
		t.Error("another message: got = same digest, want = another")
	}
	// source: compute_digest_service_spec.rb:122-160
	for name, p := range map[string]x402.Object{
		"no message":     payment(encode(signatures, nil)),
		"no signature":   payment(encode(nil, message)),
		"wide count":     payment(base64.StdEncoding.EncodeToString(append(append([]byte{0x80, 0x01}, sig(0x11)...), message...))),
		"not base64":     payment("not base64!"),
		"no transaction": {"payload": x402.Object{}},
	} {
		if _, err := x402.ComputeDigest(p, network, ""); digestCode(err) != x402.CodeInvalidTransaction {
			t.Errorf("%s: got = %v, want = %s", name, err, x402.CodeInvalidTransaction)
		}
	}
}
