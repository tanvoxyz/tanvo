package x402

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// rpcCall is one JSON-RPC request a fake endpoint received.
type rpcCall struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
}

// fakeRPC is a JSON-RPC endpoint answering through handle: a result, or
// a raw HTTP answer when raw is set.
type fakeRPC struct {
	mu     sync.Mutex
	calls  []rpcCall
	handle func(c rpcCall) (result any, raw *cdpAnswer)
	srv    *httptest.Server
}

func newFakeRPC(t *testing.T, handle func(c rpcCall) (any, *cdpAnswer)) *fakeRPC {
	t.Helper()
	f := &fakeRPC{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		v, err := decodeJSON(b)
		if err != nil {
			t.Errorf("got request = %s", b)
			return
		}
		o := v.(map[string]any)
		c := rpcCall{Method: o["method"].(string)}
		c.Params, _ = o["params"].([]any)
		f.mu.Lock()
		f.calls = append(f.calls, c)
		h := f.handle
		f.mu.Unlock()
		result, raw := h(c)
		if raw != nil {
			if raw.drop {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			w.WriteHeader(raw.status)
			_, _ = io.WriteString(w, raw.body)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRPC) callsOf(method string) []rpcCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rpcCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeRPC) readers(network x402.Network) *ChainReaders {
	return NewChainReaders(WithRPCURLs(map[x402.Network]string{network: f.srv.URL}), withoutRetrySleep())
}

func (f *fakeRPC) client() *rpcClient {
	return &rpcClient{url: f.srv.URL, http: newConfig(nil).client(), noSleep: true}
}

func wantChainError(t *testing.T, err, kind error, contains string) {
	t.Helper()
	if !errors.Is(err, kind) || !strings.Contains(err.Error(), contains) {
		t.Fatalf("got error = %v, want %v containing %q", err, kind, contains)
	}
}

// source: spec/services/x402/chain/rpc_client_spec.rb:16-107
func TestRPCURLs(t *testing.T) {
	t.Run("defaults to the public endpoints", func(t *testing.T) { // :17-30
		r := NewChainReaders()
		for n, want := range map[x402.Network]string{
			"eip155:8453":  "https://mainnet.base.org",
			"eip155:84532": "https://sepolia.base.org",
			"solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp": "https://api.mainnet-beta.solana.com",
			"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1": "https://api.devnet.solana.com",
		} {
			c, err := r.rpc(n)
			if err != nil || c.url != want {
				t.Errorf("got %s = %v, %v, want = %s", n, c, err, want)
			}
		}
	})

	t.Run("overrides one network and keeps the others", func(t *testing.T) { // :32-42
		urls, err := ParseRPCURLs(`{"eip155:84532":"https://base-sepolia.example.com/v2/key"}`)
		if err != nil {
			t.Fatal(err)
		}
		r := NewChainReaders(WithRPCURLs(urls))
		if c, _ := r.rpc("eip155:84532"); c.url != "https://base-sepolia.example.com/v2/key" {
			t.Errorf("got url = %s", c.url)
		}
		if c, _ := r.rpc("eip155:8453"); c.url != "https://mainnet.base.org" {
			t.Errorf("got url = %s", c.url)
		}
	})

	if urls, err := ParseRPCURLs(""); err != nil || len(urls) != 0 {
		t.Errorf("got %v, %v for no configuration", urls, err)
	}

	for _, c := range []struct {
		line, raw, contains string
	}{
		{":44-50", "eip155:84532=https://example.com", "not valid JSON"},
		{":52-58", `["https://example.com"]`, "JSON object"},
		{":60-66", `{"eip155:84532":"https://rpc.example.com/v2/SECRETKEY\n"}`, "for eip155:84532"},
		{":68-74", `{"eip155:84532":"ftp://rpc.example.com/SECRETKEY"}`, "for eip155:84532"},
		{":76-82", `{"https://rpc.example.com/v2/SECRETKEY":"eip155:84532"}`, "not a supported network"},
		{":84-90", `{"eip155:84532":"ftp://rpc.example.com"}`, "for eip155:84532"},
		{":92-98", `{'eip155:84532':'https://rpc.example.com/v2/SECRETKEY'}`, "not valid JSON"},
		{"", `{"eip155:84532":42}`, "for eip155:84532"},
	} {
		t.Run(c.line+" "+c.raw, func(t *testing.T) {
			_, err := ParseRPCURLs(c.raw)
			if err == nil || !strings.Contains(err.Error(), c.contains) || strings.Contains(err.Error(), "SECRETKEY") {
				t.Errorf("got error = %v, want one containing %q without the key", err, c.contains)
			}
		})
	}

	t.Run("a network without an endpoint", func(t *testing.T) { // :100-106
		_, err := NewChainReaders().rpc("eip155:1")
		wantChainError(t, err, x402.ErrChainUnreachable, "no RPC endpoint")
	})
}

// source: spec/services/x402/chain/rpc_client_spec.rb:109-233
func TestRPCCall(t *testing.T) {
	ctx := context.Background()

	t.Run("answers the result", func(t *testing.T) { // :114-118
		f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) { return "0x2d40e01", nil })
		got, err := f.client().call(ctx, "eth_blockNumber")
		if err != nil || got != "0x2d40e01" {
			t.Fatalf("got = %v, %v", got, err)
		}
		if c := f.callsOf("eth_blockNumber"); len(c) != 1 || c[0].Params == nil || len(c[0].Params) != 0 {
			t.Errorf("got calls = %+v, want one with empty params", c)
		}
	})

	t.Run("a null result", func(t *testing.T) { // :120-124
		f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) { return nil, nil })
		if got, err := f.client().call(ctx, "eth_blockNumber"); err != nil || got != nil {
			t.Fatalf("got = %v, %v, want nil", got, err)
		}
	})

	t.Run("fails once, then retries", func(t *testing.T) { // :168-176
		n := 0
		f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) {
			if n++; n == 1 {
				return nil, &cdpAnswer{status: 502}
			}
			return "0x2d40e01", nil
		})
		if got, err := f.client().call(ctx, "eth_blockNumber"); err != nil || got != "0x2d40e01" {
			t.Fatalf("got = %v, %v", got, err)
		}
	})

	t.Run("gives up after three attempts", func(t *testing.T) {
		f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) { return nil, &cdpAnswer{status: 503, body: "Service Unavailable"} })
		_, err := f.client().call(ctx, "eth_blockNumber")
		wantChainError(t, err, x402.ErrChainUnreachable, "eth_blockNumber: HTTP 503")
		if n := len(f.callsOf("eth_blockNumber")); n != 3 {
			t.Errorf("got %d attempts, want 3", n)
		}
	})

	for _, c := range []struct {
		line, want string
		raw        cdpAnswer
	}{
		{":126-132", "eth_blockNumber: no result", cdpAnswer{status: 200, body: `{"jsonrpc":"2.0","id":1}`}},
		{":134-140", "eth_blockNumber: eth_getLogs is limited to a 1,000 range", cdpAnswer{status: 200, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32614,"message":"eth_getLogs is limited to a 1,000 range"}}`}},
		{":142-148", "eth_blockNumber: invalid JSON", cdpAnswer{status: 200, body: "<html>rate limited</html>"}},
		{":150-156", "eth_blockNumber: malformed error", cdpAnswer{status: 200, body: `{"jsonrpc":"2.0","id":1,"error":"rate limited"}`}},
		{":158-166", "eth_blockNumber: not a JSON-RPC response", cdpAnswer{status: 200, body: "[]"}},
		{":158-166", "eth_blockNumber: not a JSON-RPC response", cdpAnswer{status: 200, body: "42"}},
		{":158-166", "eth_blockNumber: not a JSON-RPC response", cdpAnswer{status: 200, body: `"rate limited"`}},
		{":158-166", "eth_blockNumber: not a JSON-RPC response", cdpAnswer{status: 200, body: "null"}},
		{":178-184", "eth_blockNumber: invalid JSON", cdpAnswer{status: 204}},
		{":226-232", "eth_blockNumber: the answer is not valid UTF-8", cdpAnswer{status: 200, body: "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":\"\xff\"}"}},
		{"", "eth_blockNumber: HTTP 404", cdpAnswer{status: 404}},
		{"", "eth_blockNumber: connection closed early", cdpAnswer{drop: true}},
	} {
		t.Run(c.line+" "+c.want, func(t *testing.T) {
			f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) { return nil, &c.raw })
			_, err := f.client().call(ctx, "eth_blockNumber")
			wantChainError(t, err, x402.ErrChainUnreachable, c.want)
		})
	}

	t.Run("a keyed endpoint fails without the URL", func(t *testing.T) { // :186-198
		f := newFakeRPC(t, func(rpcCall) (any, *cdpAnswer) {
			return nil, &cdpAnswer{status: 503, body: "Service Unavailable"}
		})
		c := f.client()
		c.url += "/v2/secret-key"
		_, err := c.call(ctx, "eth_blockNumber")
		wantChainError(t, err, x402.ErrChainUnreachable, "eth_blockNumber: HTTP 503")
		if strings.Contains(err.Error(), "secret-key") {
			t.Errorf("got error = %v, want no URL", err)
		}
	})

	t.Run("a refused connection fails without the URL", func(t *testing.T) { // :200-208
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		u := "http://" + l.Addr().String() + "/v2/secret-key"
		_ = l.Close()
		c := &rpcClient{url: u, http: newConfig(nil).client(), noSleep: true}
		_, err = c.call(ctx, "eth_blockNumber")
		wantChainError(t, err, x402.ErrChainUnreachable, "eth_blockNumber: connection refused")
		if strings.Contains(err.Error(), "secret-key") {
			t.Errorf("got error = %v, want no URL", err)
		}
	})
}
