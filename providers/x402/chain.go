package x402

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// ChainReaders opens the JSON-RPC readers of stored payments; it
// implements [x402.ChainReaders].
type ChainReaders struct {
	urls    map[x402.Network]string
	http    *http.Client
	noSleep bool
}

var _ x402.ChainReaders = (*ChainReaders)(nil)

// NewChainReaders returns the chain readers.
func NewChainReaders(opts ...Option) *ChainReaders {
	c := newConfig(opts)
	return &ChainReaders{urls: c.rpcURLs, http: c.client(), noSleep: c.noRetrySleep}
}

// errNoSince is a reader opened without the time the payment was first
// seen.
var errNoSince = errors.New("x402 chain: since is required")

// For returns the reader of payment on network: the EVM reader or the
// Solana reader. A reader reads each fact once and is not safe for
// concurrent use; a failed read is retried by the next call.
func (r *ChainReaders) For(network x402.Network, payment, requirements x402.Object, since time.Time) (x402.ChainReader, error) {
	f, _ := network.Family()
	switch f {
	case x402.FamilyEVM:
		return &evmReader{chain: r, network: network, payment: payment, requirements: requirements, since: since}, nil
	case x402.FamilySVM:
		return &svmReader{chain: r, network: network, payment: payment, since: since}, nil
	}
	return nil, unreachable("no RPC endpoint for %q", string(network))
}

// rpc returns the client of network's endpoint.
//
// source: app/services/x402/chain/rpc_client.rb:18-20
func (r *ChainReaders) rpc(network x402.Network) (*rpcClient, error) {
	u, ok := r.urls[network]
	if !ok {
		return nil, unreachable("no RPC endpoint for %q", string(network))
	}
	return &rpcClient{url: u, http: r.http, noSleep: r.noSleep}, nil
}

// maxSeconds saturates the authorization's times, so that block
// arithmetic cannot overflow.
const maxSeconds = int64(1) << 62

// seconds reads an unsigned JSON integer, saturated at maxSeconds.
func seconds(v any) (int64, bool) {
	n, ok := x402.ParseUnsigned(v)
	if !ok {
		return 0, false
	}
	if !n.IsInt64() || n.Int64() > maxSeconds {
		return maxSeconds, true
	}
	return n.Int64(), true
}

// jsonInteger reads a JSON integer (not a string): Ruby's Integer.
func jsonInteger(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil
}

// rubyHex reads s as Ruby's String#to_i(16): an optional 0x, then the
// leading hexadecimal digits; nothing readable is 0.
func rubyHex(s string) *big.Int {
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	end := 0
	for end < len(s) && isHex(s[end]) {
		end++
	}
	n, ok := new(big.Int).SetString(s[:end], 16)
	if !ok {
		return new(big.Int)
	}
	return n
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// ceilHalf is ceil(n / 2) for any sign.
func ceilHalf(n int64) int64 {
	if n > 0 {
		return (n + 1) / 2
	}
	return n / 2
}

func list(v any) ([]any, bool) {
	l, ok := v.([]any)
	return l, ok
}

func inspect(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(b)
}
