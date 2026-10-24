package x402

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The JSON-RPC client settings.
//
// source: app/services/x402/chain/rpc_client.rb:13-14,
// lib/lago_http_client/lago_http_client/client.rb:10-22
const (
	rpcOpenTimeout = 5 * time.Second
	rpcReadTimeout = 10 * time.Second
	rpcAttempts    = 3
	rpcBackoffMin  = 250 * time.Millisecond
	rpcBackoffMax  = 500 * time.Millisecond
)

// DefaultRPCURLs returns the public JSON-RPC endpoint of each network.
//
// source: app/services/x402/chain/rpc_client.rb:6-11
func DefaultRPCURLs() map[x402.Network]string {
	return map[x402.Network]string{
		x402.Network("eip155:8453"):                             "https://mainnet.base.org",
		x402.Network("eip155:84532"):                            "https://sepolia.base.org",
		x402.Network("solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"): "https://api.mainnet-beta.solana.com",
		x402.Network("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"): "https://api.devnet.solana.com",
	}
}

// ParseRPCURLs reads a JSON object mapping a CAIP-2 network to its
// JSON-RPC endpoint (TANVO_X402_RPC_URLS); "" is no override. Errors never
// quote a URL or a key, which may carry an API key.
//
// source: app/services/x402/chain/rpc_client.rb:63-86
func ParseRPCURLs(raw string) (map[x402.Network]string, error) {
	if len(bytes.TrimSpace([]byte(raw))) == 0 {
		return map[x402.Network]string{}, nil
	}
	v, err := decodeJSON([]byte(raw))
	if err != nil {
		return nil, errors.New("x402 RPC URLs are not valid JSON")
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("x402 RPC URLs must be a JSON object mapping a CAIP-2 network to a URL")
	}
	urls := make(map[x402.Network]string, len(o))
	for k, e := range o {
		s, ok := e.(string)
		if !ok || !httpURL(s) {
			name := "a key that is not a supported network"
			if x402.Network(k).Valid() {
				name = k
			}
			return nil, fmt.Errorf("x402 RPC URLs have an invalid URL for %s", name)
		}
		urls[x402.Network(k)] = s
	}
	return urls, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// rpcClient is the JSON-RPC client of one network's endpoint.
//
// source: app/services/x402/chain/rpc_client.rb
type rpcClient struct {
	url     string
	http    *http.Client
	noSleep bool
}

// call calls method; any failure to get a result wraps
// [x402.ErrChainUnreachable]. A null result is nil.
//
// source: app/services/x402/chain/rpc_client.rb:22-35
func (c *rpcClient) call(ctx context.Context, method string, params ...any) (any, error) {
	if params == nil {
		params = []any{}
	}
	raw, err := c.post(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, unreachable("%s: the answer is not valid UTF-8", method)
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, unreachable("%s: invalid JSON", method)
	}
	body, ok := v.(map[string]any)
	if !ok {
		return nil, unreachable("%s: not a JSON-RPC response", method)
	}
	if e, has := body["error"]; has && e != nil && e != false {
		if eo, ok := e.(map[string]any); ok {
			return nil, unreachable("%s: %s", method, rubyString(eo["message"]))
		}
		return nil, unreachable("%s: malformed error", method)
	}
	result, has := body["result"]
	if !has {
		return nil, unreachable("%s: no result", method)
	}
	return result, nil
}

// post sends the request, retrying a transient failure: a 500, 502, 503
// or 504, a timeout, a reset, refused or unreachable connection, an
// early EOF or a TLS failure.
//
// source: app/services/x402/chain/rpc_client.rb:39-48,
// lib/lago_http_client/lago_http_client/client.rb:181-221
func (c *rpcClient) post(ctx context.Context, method string, params []any) ([]byte, error) {
	body, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}{"2.0", 1, method, params})
	if err != nil {
		return nil, fmt.Errorf("x402 chain: encode %s: %w", method, err)
	}
	for attempt := 1; ; attempt++ {
		raw, status, err := c.attempt(ctx, body)
		retry := (err != nil && transient(err)) || status == 500 || status == 502 || status == 503 || status == 504
		if retry && attempt < rpcAttempts && ctx.Err() == nil {
			if err := c.backoff(ctx); err != nil {
				return nil, unreachable("%s: %s", method, describe(err))
			}
			continue
		}
		switch {
		case err != nil:
			return nil, unreachable("%s: %s", method, describe(err))
		case status != 200 && status != 201 && status != 202 && status != 204:
			return nil, unreachable("%s: HTTP %d", method, status)
		}
		return raw, nil
	}
}

func (c *rpcClient) attempt(ctx context.Context, body []byte) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcOpenTimeout+rpcReadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return nil, 0, err
	}
	return raw, resp.StatusCode, nil
}

func (c *rpcClient) backoff(ctx context.Context) error {
	if c.noSleep {
		return nil
	}
	d := rpcBackoffMin + rand.N(rpcBackoffMax-rpcBackoffMin)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// transient reports a failure worth retrying.
func transient(err error) bool {
	var ne net.Error
	var rh tls.RecordHeaderError
	var alert tls.AlertError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.Is(err, context.Canceled):
		return false
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ETIMEDOUT), errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return true
	case errors.As(err, &rh), errors.As(err, &alert), errors.As(err, &cert), errors.As(err, &unknown):
		return true
	}
	return false
}

// describe names a transport failure without quoting the URL.
func describe(err error) string {
	var ne net.Error
	var dns *net.DNSError
	var errno syscall.Errno
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &dns):
		return "name resolution failed"
	case errors.As(err, &errno):
		return errno.Error()
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection closed early"
	case transient(err):
		return "TLS failure"
	}
	return "transport error"
}

func unreachable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", x402.ErrChainUnreachable, fmt.Sprintf(format, args...))
}

func inconclusive(format string, args ...any) error {
	return fmt.Errorf("%w: %s", x402.ErrChainInconclusive, fmt.Sprintf(format, args...))
}

// rubyString renders v as Ruby string interpolation would for JSON
// values: nil is empty, a string is itself, anything else its JSON.
func rubyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
