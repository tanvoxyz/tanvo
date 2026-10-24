package x402

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The CDP adapter, against a fake facilitator.
//
// source: spec/services/x402/facilitator/coinbase_cdp_adapter_spec.rb,
// spec/support/x402/payment_context.rb (stub_cdp_facilitator)

const cdpKeyID = "organizations/lago-test/apiKeys/x402"

type cdpAnswer struct {
	status int
	body   string
	drop   bool
}

type cdpRequest struct {
	method, path, auth string
	body               []byte
}

type fakeCDP struct {
	t       *testing.T
	mu      sync.Mutex
	answers map[string]cdpAnswer
	reqs    []cdpRequest
	srv     *httptest.Server
	log     *bytes.Buffer
	fac     x402.Facilitator
	signer  ed25519.PrivateKey
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFakeCDP(t *testing.T) *fakeCDP {
	t.Helper()
	f := &fakeCDP{t: t, log: &bytes.Buffer{}, answers: map[string]cdpAnswer{
		CDPBasePath + "/verify":    {status: 200, body: fixture(t, "verify_valid.json")},
		CDPBasePath + "/settle":    {status: 200, body: fixture(t, "settle_success.json")},
		CDPBasePath + "/supported": {status: 200, body: fixture(t, "supported.json")},
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, cdpRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		a := f.answers[r.URL.Path]
		f.mu.Unlock()
		if a.drop {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(f.srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.signer = priv
	f.fac = f.open(base64.StdEncoding.EncodeToString(priv))
	return f
}

func (f *fakeCDP) open(secret string) x402.Facilitator {
	f.t.Helper()
	fs := NewFacilitators(WithBaseURL(f.srv.URL+CDPBasePath), WithLogger(slog.New(slog.NewTextHandler(f.log, nil))))
	fac, err := fs.For(x402.FacilitatorCoinbaseCDP, x402.Credentials{APIKeyID: cdpKeyID, APIKeySecret: secret})
	if err != nil {
		f.t.Fatal(err)
	}
	return fac
}

func (f *fakeCDP) answer(path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[CDPBasePath+path] = cdpAnswer{status: status, body: body}
}

func (f *fakeCDP) drop(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[CDPBasePath+path] = cdpAnswer{drop: true}
}

func (f *fakeCDP) requests(path string) []cdpRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cdpRequest
	for _, r := range f.reqs {
		if r.path == CDPBasePath+path {
			out = append(out, r)
		}
	}
	return out
}

func jsonObject(t *testing.T, s string) x402.Object {
	t.Helper()
	v, err := decodeJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func evmRequirements(t *testing.T) x402.Object {
	return jsonObject(t, `{"scheme":"exact","network":"eip155:84532","amount":"1000",
		"asset":"0x036CbD53842c5426634e7929541eC2318f3dCF7e","payTo":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
		"maxTimeoutSeconds":60,"extra":{"name":"USDC","version":"2"}}`)
}

func evmPayment(t *testing.T) x402.Object {
	p := jsonObject(t, `{"x402Version":2,
		"resource":{"url":"https://api.example.com/report","description":"Report","mimeType":"application/json"},
		"payload":{"signature":"0x`+strings.Repeat("ab", 65)+`","authorization":{
			"from":"0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2","to":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
			"value":"1000","validAfter":"1789648671","validBefore":"1789649331",
			"nonce":"0x8568d530303a96028f64623cfc5c7bbb166c4aa2889b25bbe09e1d699193f174"}},
		"extensions":{}}`)
	p["accepted"] = evmRequirements(t)
	return p
}

func claims(t *testing.T, auth string) map[string]any {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func facilitatorError(t *testing.T, err error, kind error) *x402.FacilitatorError {
	t.Helper()
	var fe *x402.FacilitatorError
	if !errors.As(err, &fe) || !errors.Is(err, kind) {
		t.Fatalf("got error = %v, want %v", err, kind)
	}
	return fe
}

func (f *fakeCDP) wantLog(pattern string) {
	f.t.Helper()
	if !regexp.MustCompile(pattern).MatchString(f.log.String()) {
		f.t.Errorf("got log = %q, want match %q", f.log.String(), pattern)
	}
}

// source: coinbase_cdp_adapter_spec.rb:28-220
func TestCDPVerify(t *testing.T) {
	ctx := context.Background()

	t.Run("valid", func(t *testing.T) { // :31
		f := newFakeCDP(t)
		got, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || !got.Valid || got.Payer != "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2" {
			t.Fatalf("got = %+v, %v, want valid with the payer", got, err)
		}
	})

	t.Run("forwards the payment and its requirements unchanged", func(t *testing.T) { // :35-44
		f := newFakeCDP(t)
		p := evmPayment(t)
		p["extensions"] = map[string]any{"bazaar": map[string]any{"info": map[string]any{"input": map[string]any{"method": "GET"}}}}
		if _, err := f.fac.Verify(ctx, p, evmRequirements(t)); err != nil {
			t.Fatal(err)
		}
		reqs := f.requests("/verify")
		if len(reqs) != 1 {
			t.Fatalf("got %d requests, want 1", len(reqs))
		}
		want, _ := json.Marshal(map[string]any{"x402Version": json.Number("2"), "paymentPayload": p, "paymentRequirements": evmRequirements(t)})
		if got := jsonObject(t, string(reqs[0].body)); inspect(got) != string(want) {
			t.Errorf("got body = %s, want = %s", inspect(got), want)
		}
	})

	t.Run("speaks the payment's version", func(t *testing.T) { // :46-54
		f := newFakeCDP(t)
		p := jsonObject(t, `{"x402Version":1,"scheme":"exact","network":"base-sepolia"}`)
		p["payload"] = evmPayment(t)["payload"]
		if _, err := f.fac.Verify(ctx, p, evmRequirements(t)); err != nil {
			t.Fatal(err)
		}
		if got := jsonObject(t, string(f.requests("/verify")[0].body))["x402Version"]; got != json.Number("1") {
			t.Errorf("got x402Version = %v, want = 1", got)
		}
	})

	t.Run("signs a token bound to the call", func(t *testing.T) { // :67-71
		f := newFakeCDP(t)
		if _, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t)); err != nil {
			t.Fatal(err)
		}
		if got := claims(t, f.requests("/verify")[0].auth)["uri"]; got != "POST api.cdp.coinbase.com/platform/v2/x402/verify" {
			t.Errorf("got uri = %v", got)
		}
	})

	t.Run("rejection", func(t *testing.T) { // :73-86
		f := newFakeCDP(t)
		f.answer("/verify", 200, `{"isValid":false,"invalidReason":"invalid_exact_evm_payload_signature","payer":"0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2"}`)
		got, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || got.Valid || got.InvalidReason != "invalid_exact_evm_payload_signature" {
			t.Fatalf("got = %+v, %v", got, err)
		}
		f.wantLog(`operation=verify reason=invalid_exact_evm_payload_signature status=200\n`)
	})

	t.Run("a Solana verify names no payer", func(t *testing.T) { // :88-94
		f := newFakeCDP(t)
		f.answer("/verify", 200, `{"isValid":true}`)
		got, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || !got.Valid || got.Payer != "" {
			t.Fatalf("got = %+v, %v", got, err)
		}
	})

	t.Run("a refused request is invalid with the error type", func(t *testing.T) { // :96-102
		f := newFakeCDP(t)
		f.answer("/verify", 400, `{"errorType":"invalid_request","errorMessage":"bad payload","correlationId":"corr-1"}`)
		got, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || got.Valid || got.InvalidReason != "invalid_request" {
			t.Fatalf("got = %+v, %v", got, err)
		}
	})

	t.Run("401", func(t *testing.T) { // :104-115
		f := newFakeCDP(t)
		f.answer("/verify", 401, "Unauthorized")
		_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if fe := facilitatorError(t, err, x402.ErrFacilitatorCredentials); fe.HTTPStatus != 401 {
			t.Errorf("got status = %d, want = 401", fe.HTTPStatus)
		}
		f.wantLog(`operation=verify reason=unauthorized status=401\n`)
	})

	t.Run("402", func(t *testing.T) { // :117-128
		f := newFakeCDP(t)
		f.answer("/verify", 402, `{"errorType":"payment_method_required","correlationId":"corr-3"}`)
		_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if fe := facilitatorError(t, err, x402.ErrFacilitatorCredentials); fe.HTTPStatus != 402 {
			t.Errorf("got status = %d, want = 402", fe.HTTPStatus)
		}
		f.wantLog(`operation=verify reason=payment_required status=402 error_type=payment_method_required correlation_id=corr-3\n`)
	})

	t.Run("403", func(t *testing.T) { // :130-136
		f := newFakeCDP(t)
		f.answer("/verify", 403, `{"errorType":"forbidden","correlationId":"corr-2"}`)
		_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if fe := facilitatorError(t, err, x402.ErrFacilitatorCredentials); fe.CorrelationID != "corr-2" {
			t.Errorf("got correlation = %q, want = corr-2", fe.CorrelationID)
		}
	})

	t.Run("screening decline", func(t *testing.T) { // :138-150
		f := newFakeCDP(t)
		f.answer("/verify", 403, `{"errorType":"kyt_risk_detected","correlationId":"corr-4"}`)
		got, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || got.Valid || got.InvalidReason != "kyt_risk_detected" {
			t.Fatalf("got = %+v, %v", got, err)
		}
		f.wantLog(`operation=verify reason=kyt_risk_detected status=403 correlation_id=corr-4\n`)
	})

	t.Run("429", func(t *testing.T) { // :152-158
		f := newFakeCDP(t)
		f.answer("/verify", 429, `{"errorType":"rate_limit_exceeded"}`)
		_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		_ = facilitatorError(t, err, x402.ErrFacilitatorRateLimited)
	})

	for name, a := range map[string]cdpAnswer{
		"500":            {status: 500, body: `{"errorType":"internal_server_error"}`}, // :160-166
		"a JSON array":   {status: 200, body: "[]"},                                    // :168-174
		"not JSON":       {status: 200, body: "<html></html>"},                         // :176-182
		"no verdict":     {status: 200, body: "{}"},                                    // :184-195
		"a string value": {status: 200, body: `{"isValid":"true"}`},                    // :197-203
	} {
		t.Run("unavailable with "+name, func(t *testing.T) {
			f := newFakeCDP(t)
			f.answer("/verify", a.status, a.body)
			_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
			_ = facilitatorError(t, err, x402.ErrFacilitatorUnavailable)
			if name == "no verdict" {
				f.wantLog(`operation=verify reason=malformed_response status=200\n`)
			}
		})
	}

	t.Run("a secret that is not a key", func(t *testing.T) { // :205-211
		f := newFakeCDP(t)
		_, err := f.open("not a key").Verify(ctx, evmPayment(t), evmRequirements(t))
		if !errors.Is(err, errNotAKey) {
			t.Fatalf("got error = %v, want errNotAKey", err)
		}
		if len(f.requests("/verify")) != 0 {
			t.Error("got a request, want none")
		}
	})

	t.Run("no answer", func(t *testing.T) { // :213-219
		f := newFakeCDP(t)
		f.drop("/verify")
		_, err := f.fac.Verify(ctx, evmPayment(t), evmRequirements(t))
		_ = facilitatorError(t, err, x402.ErrFacilitatorUnavailable)
		f.wantLog(`operation=verify reason=no_response`)
	})
}

// source: coinbase_cdp_adapter_spec.rb:222-489
func TestCDPSettle(t *testing.T) {
	ctx := context.Background()
	hash := "0x" + strings.Repeat("cd", 32)

	t.Run("settled", func(t *testing.T) { // :225-238
		f := newFakeCDP(t)
		got, err := f.fac.Settle(ctx, evmPayment(t), evmRequirements(t))
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != x402.SettleOutcomeSettled || got.Transaction != "0xefad33f01282b3a8b18893a9eb0aecbc2fc210ffcfa7fda7ca79f14c8822963a" ||
			got.Network != "eip155:84532" || got.Payer != "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2" {
			t.Errorf("got = %+v", got)
		}
		if f.log.Len() != 0 {
			t.Errorf("got log = %q, want none", f.log.String())
		}
	})

	cases := []struct {
		name, line string
		status     int
		body       string
		outcome    x402.SettleOutcome
		reason, tx string
		logged     string
	}{
		{
			"rejected", ":250-263", 400, `{"success":false,"errorReason":"invalid_exact_evm_signature","network":"eip155:84532"}`,
			x402.SettleOutcomeUnconfirmedFailure, "invalid_exact_evm_signature", "",
			`operation=settle reason=invalid_exact_evm_signature outcome=unconfirmed_failure network=eip155:84532 duration=`,
		},
		{
			"a rejection carrying a hash", ":265-271", 400, `{"success":false,"errorReason":"invalid_exact_evm_signature","transaction":"` + hash + `"}`,
			x402.SettleOutcomeUnconfirmedFailure, "invalid_exact_evm_signature", "", "",
		},
		{
			"nonce already submitted", ":273-287", 400, `{"errorMessage":"authorization nonce already submitted; transaction already on-chain","errorReason":"invalid_payload","network":"eip155:84532","success":false,"transaction":"0xefad33f01282b3a8b18893a9eb0aecbc2fc210ffcfa7fda7ca79f14c8822963a"}`,
			x402.SettleOutcomeUnconfirmedFailure, "invalid_payload", "", "",
		},
		{
			"failed on chain", ":289-295", 400, `{"success":false,"errorReason":"settle_exact_failed_onchain","network":"eip155:84532","transaction":"` + hash + `"}`,
			x402.SettleOutcomeUnconfirmedFailure, "settle_exact_failed_onchain", "", "",
		},
		{
			"Solana simulation failed", ":297-309", 400, `{"errorMessage":"simulation failed","errorReason":"invalid_exact_svm_payload_transaction_simulation_failed","success":false}`,
			x402.SettleOutcomeUnconfirmedFailure, "invalid_exact_svm_payload_transaction_simulation_failed", "", "",
		},
		{
			"an unknown reason", ":328-340", 200, `{"success":false,"errorReason":"settle_exact_new_failure"}`,
			x402.SettleOutcomeUnconfirmedFailure, "settle_exact_new_failure", "",
			`operation=settle reason=settle_exact_new_failure outcome=unconfirmed_failure duration=`,
		},
		{"no reason", "", 200, `{"success":false}`, x402.SettleOutcomeUnconfirmedFailure, "unexpected_settle_error", "", ""},
		{
			"pending", ":342-348", 500, `{"success":false,"errorReason":"settlement_pending","network":"eip155:84532","transaction":"` + hash + `"}`,
			x402.SettleOutcomeSettlementPending, "settlement_pending", hash, "",
		},
		{
			"pending as a 200", ":350-356", 200, `{"success":false,"errorReason":"settlement_pending","transaction":"` + hash + `"}`,
			x402.SettleOutcomeSettlementPending, "settlement_pending", hash, "",
		},
		{
			"a generic 500", ":358-370", 500, `{"errorType":"internal_server_error","errorMessage":"internal error","correlationId":"corr-1"}`,
			x402.SettleOutcomeServerError, "server_error", "", "",
		},
		{"success without a hash", ":372-378", 200, `{"success":true}`, x402.SettleOutcomeServerError, "malformed_response", "", ""},
		{"success with a hash that is not a string", "", 200, `{"success":true,"transaction":42}`, x402.SettleOutcomeServerError, "malformed_response", "", ""},
		{"not JSON", ":380-386", 200, "<html></html>", x402.SettleOutcomeServerError, "malformed_response", "", ""},
		{
			"screening decline", ":468-480", 403, `{"errorType":"kyt_risk_detected"}`, x402.SettleOutcomeUnconfirmedFailure, "kyt_risk_detected", "",
			`operation=settle reason=kyt_risk_detected outcome=unconfirmed_failure duration=`,
		},
	}
	// source: coinbase_cdp_adapter_spec.rb:311-326
	for _, r := range []string{
		"insufficient_funds", "invalid_payload", "invalid_exact_evm_payload_authorization_valid_before",
		"invalid_exact_evm_verification_failed", "invalid_exact_solana_transaction_confirmation_failed",
		"settle_exact_svm_transaction_confirmation_timed_out",
	} {
		cases = append(cases, struct {
			name, line string
			status     int
			body       string
			outcome    x402.SettleOutcome
			reason, tx string
			logged     string
		}{r, ":319-325", 400, `{"success":false,"errorReason":"` + r + `"}`, x402.SettleOutcomeUnconfirmedFailure, r, "", ""})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeCDP(t)
			f.answer("/settle", c.status, c.body)
			got, err := f.fac.Settle(ctx, evmPayment(t), evmRequirements(t))
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != c.outcome || got.ErrorReason != c.reason || got.Transaction != c.tx {
				t.Errorf("got = %s %q %q, want = %s %q %q", got.Outcome, got.ErrorReason, got.Transaction, c.outcome, c.reason, c.tx)
			}
			if n := len(f.requests("/settle")); n != 1 {
				t.Errorf("got %d requests, want 1", n)
			}
			if c.logged != "" {
				f.wantLog(c.logged)
			}
		})
	}

	t.Run("no answer", func(t *testing.T) { // :388-416
		f := newFakeCDP(t)
		f.drop("/settle")
		got, err := f.fac.Settle(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || got.Outcome != x402.SettleOutcomeNoResponse || got.ErrorReason != "no_response" {
			t.Fatalf("got = %+v, %v", got, err)
		}
		if n := len(f.requests("/settle")); n != 1 {
			t.Errorf("got %d requests, want 1", n)
		}
		f.wantLog(`operation=settle reason=no_response outcome=no_response duration=`)
	})

	t.Run("a refused connection", func(t *testing.T) { // :408-416
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		fac, _ := NewFacilitators(WithBaseURL("http://"+addr)).For(x402.FacilitatorCoinbaseCDP, x402.Credentials{APIKeyID: cdpKeyID, APIKeySecret: newFakeCDP(t).secret()})
		got, err := fac.Settle(ctx, evmPayment(t), evmRequirements(t))
		if err != nil || got.Outcome != x402.SettleOutcomeNoResponse {
			t.Fatalf("got = %+v, %v", got, err)
		}
	})

	t.Run("a secret that is not a key", func(t *testing.T) { // :418-429
		f := newFakeCDP(t)
		_, err := f.open("not a key").Settle(ctx, evmPayment(t), evmRequirements(t))
		if !errors.Is(err, errNotAKey) {
			t.Fatalf("got error = %v, want errNotAKey", err)
		}
		if len(f.requests("/settle")) != 0 {
			t.Error("got a request, want none")
		}
	})

	t.Run("a body that cannot be serialised", func(t *testing.T) { // :431-442
		f := newFakeCDP(t)
		p := evmPayment(t)
		p["memo"] = "\xff"
		if _, err := f.fac.Settle(ctx, p, evmRequirements(t)); err == nil || errors.As(err, new(*x402.FacilitatorError)) {
			t.Fatalf("got error = %v, want a serialisation error", err)
		}
		if len(f.requests("/settle")) != 0 {
			t.Error("got a request, want none")
		}
	})

	for _, c := range []struct {
		line, body string
		status     int
		kind       error
	}{
		{":444-450", "Unauthorized", 401, x402.ErrFacilitatorCredentials},
		{":452-458", `{"errorType":"payment_method_required"}`, 402, x402.ErrFacilitatorCredentials},
		{":460-466", `{"errorType":"forbidden"}`, 403, x402.ErrFacilitatorCredentials},
		{":482-488", `{"errorType":"rate_limit_exceeded"}`, 429, x402.ErrFacilitatorRateLimited},
	} {
		t.Run(c.line, func(t *testing.T) {
			f := newFakeCDP(t)
			f.answer("/settle", c.status, c.body)
			_, err := f.fac.Settle(ctx, evmPayment(t), evmRequirements(t))
			fe := facilitatorError(t, err, c.kind)
			if fe.HTTPStatus != c.status {
				t.Errorf("got status = %d, want = %d", fe.HTTPStatus, c.status)
			}
			if c.status == 403 && fe.ErrorType != "forbidden" {
				t.Errorf("got error type = %q, want = forbidden", fe.ErrorType)
			}
		})
	}
}

func (f *fakeCDP) secret() string { return base64.StdEncoding.EncodeToString(f.signer) }

// source: coinbase_cdp_adapter_spec.rb:491-529
func TestCDPSupported(t *testing.T) {
	ctx := context.Background()

	t.Run("lists the kinds", func(t *testing.T) { // :494-502
		f := newFakeCDP(t)
		got, err := f.fac.Supported(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if fp := got.FeePayer("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"); fp != "GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb" {
			t.Errorf("got fee payer = %q", fp)
		}
		r := f.requests("/supported")[0]
		if r.method != http.MethodGet || claims(t, r.auth)["uri"] != "GET api.cdp.coinbase.com/platform/v2/x402/supported" {
			t.Errorf("got %s %v", r.method, claims(t, r.auth)["uri"])
		}
	})

	t.Run("401", func(t *testing.T) { // :504-510
		f := newFakeCDP(t)
		f.answer("/supported", 401, "Unauthorized")
		_, err := f.fac.Supported(ctx)
		_ = facilitatorError(t, err, x402.ErrFacilitatorCredentials)
	})

	for _, c := range []struct {
		status int
		body   string
	}{ // :512-528
		{200, "<html></html>"},
		{200, "[]"},
		{200, "{}"},
		{200, `{"kinds":{"network":"eip155:84532"}}`},
		{200, `{"kinds":["exact"]}`},
		{503, ""},
	} {
		t.Run(c.body, func(t *testing.T) {
			f := newFakeCDP(t)
			f.answer("/supported", c.status, c.body)
			_, err := f.fac.Supported(ctx)
			_ = facilitatorError(t, err, x402.ErrFacilitatorUnavailable)
		})
	}
}

// source: spec/services/x402/facilitator/client_spec.rb
func TestFacilitatorsFor(t *testing.T) {
	fs := NewFacilitators()
	got, err := fs.For(x402.FacilitatorCoinbaseCDP, x402.Credentials{APIKeyID: "test-key-id", APIKeySecret: "test-key-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.(*cdp); !ok || c.keyID != "test-key-id" || c.secret != "test-key-secret" {
		t.Errorf("got = %#v, want the CDP facilitator of the credentials", got)
	}
	if _, err := fs.For("other", x402.Credentials{}); err == nil || err.Error() != `x402 facilitator "other" is not supported` {
		t.Errorf("got error = %v", err)
	}
}
