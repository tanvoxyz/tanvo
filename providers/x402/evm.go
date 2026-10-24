package x402

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The EVM reader's constants: the EIP-3009 authorizationState selector
// and event topics, the ERC-20 Transfer topic, and the block window.
//
// source: app/services/x402/chain/evm_reader.rb:6-21
const (
	authorizationState    = "0xe94a0102"
	authorizationUsed     = "0x98de503528ee59b575ef0c0a2576a82497bfc029a5685b209e9ec333479b10a5"
	authorizationCanceled = "0x1cdd46ff242716cdaa72d159d339a485b3438398348d68f09d7c8c0a59353d81"
	erc20Transfer         = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	secondsPerBlock  = 2
	marginBlocks     = 150
	evmLookback      = 10 * time.Minute
	logChunkBlocks   = 1_000
	maxLogChunks     = 50
	maxBoundAttempts = 3
)

var (
	booleanWord = regexp.MustCompile(`\A0x0{63}[01]\z`)
	hexQuantity = regexp.MustCompile(`\A0x[0-9a-fA-F]+\z`)
)

type evmOutcome int

const (
	evmUnused evmOutcome = iota + 1
	evmSettled
	evmCanceled
	evmSuperseded
)

type evmBlock struct{ number, timestamp int64 }

// evmReader reads whether an EIP-3009 authorization paid the payee.
//
// source: app/services/x402/chain/evm_reader.rb
type evmReader struct {
	chain        *ChainReaders
	network      x402.Network
	payment      x402.Object
	requirements x402.Object
	since        time.Time

	client     *rpcClient
	safe       *evmBlock
	outcome    evmOutcome
	identifier string

	asset, payer, payee, nonce string
	value                      *big.Int
	validAfter, validBefore    int64
}

// Settled reports whether the authorization's transfer paid the payee.
func (r *evmReader) Settled(ctx context.Context) (bool, error) {
	o, err := r.read(ctx)
	return o == evmSettled, err
}

// Identifier is the transaction hash of the settled transfer.
func (r *evmReader) Identifier(ctx context.Context) (string, error) {
	o, err := r.read(ctx)
	if err != nil || o != evmSettled {
		return "", err
	}
	return r.identifier, nil
}

// Final reports whether the authorization was used, or can no longer be:
// the safe block is past validBefore.
//
// source: app/services/x402/chain/evm_reader.rb:35-37
func (r *evmReader) Final(ctx context.Context) (bool, error) {
	o, err := r.read(ctx)
	if err != nil {
		return false, err
	}
	if o != evmUnused {
		return true, nil
	}
	safe, err := r.safeHead(ctx)
	if err != nil {
		return false, err
	}
	return safe.timestamp >= r.validBefore, nil
}

// source: app/services/x402/chain/evm_reader.rb:43-56
func (r *evmReader) read(ctx context.Context) (evmOutcome, error) {
	if r.outcome != 0 {
		return r.outcome, nil
	}
	if r.since.IsZero() {
		return 0, errNoSince
	}
	if err := r.validate(); err != nil {
		return 0, err
	}
	if err := r.verifyChain(ctx); err != nil {
		return 0, err
	}
	used, err := r.authorizationUsed(ctx)
	if err != nil {
		return 0, err
	}
	o := evmUnused
	if used {
		log, err := r.findAuthorizationLog(ctx)
		if err != nil {
			return 0, err
		}
		if o, err = r.classify(ctx, log); err != nil {
			return 0, err
		}
	}
	r.outcome = o
	return o, nil
}

// validate reads the stored authorization.
//
// source: app/services/x402/chain/evm_reader.rb:217-224
func (r *evmReader) validate() error {
	auth := object(object(r.payment["payload"])["authorization"])
	r.asset = str(r.requirements["asset"])
	r.payer, r.payee, r.nonce = str(auth["from"]), str(auth["to"]), str(auth["nonce"])
	var okValue, okAfter, okBefore bool
	r.value, okValue = x402.ParseUnsigned(auth["value"])
	r.validAfter, okAfter = seconds(auth["validAfter"])
	r.validBefore, okBefore = seconds(auth["validBefore"])
	if !x402.IsEVMAddress(r.asset) || !x402.IsEVMAddress(r.payer) || !x402.IsEVMAddress(r.payee) ||
		!x402.HexWord.MatchString(r.nonce) || !okValue || !okAfter || !okBefore {
		return fmt.Errorf("%w: the stored EVM payment has no usable authorization", x402.ErrUnreadablePayment)
	}
	r.nonce = strings.ToLower(r.nonce)
	return nil
}

// source: app/services/x402/chain/evm_reader.rb:58-62
func (r *evmReader) verifyChain(ctx context.Context) error {
	v, err := r.call(ctx, "eth_chainId")
	if err != nil {
		return err
	}
	expected, _ := new(big.Int).SetString(leadingDigits(r.network.Reference()), 10)
	if expected == nil {
		expected = new(big.Int)
	}
	actual := rubyHex(rubyString(v))
	if actual.Cmp(expected) != 0 {
		return inconclusive("the endpoint for %s serves another chain (%s)", r.network, actual)
	}
	return nil
}

// source: app/services/x402/chain/evm_reader.rb:64-75
func (r *evmReader) classify(ctx context.Context, log x402.Object) (evmOutcome, error) {
	topics, _ := list(log["topics"])
	if len(topics) > 0 && topics[0] == authorizationCanceled {
		return evmCanceled, nil
	}
	hash := str(log["transactionHash"])
	transfer, err := r.authorizedTransfer(ctx, hash)
	if err != nil {
		return 0, err
	}
	tt, _ := list(transfer["topics"])
	if strings.ToLower(str(tt[2])) == "0x"+padded(r.payee) && rubyHex(str(transfer["data"])).Cmp(r.value) == 0 {
		r.identifier = hash
		return evmSettled, nil
	}
	return evmSuperseded, nil
}

// authorizedTransfer returns the token transfer that follows the
// authorization's event in the receipt.
//
// source: app/services/x402/chain/evm_reader.rb:77-87
func (r *evmReader) authorizedTransfer(ctx context.Context, hash string) (x402.Object, error) {
	logs, err := r.receiptLogs(ctx, hash)
	if err != nil {
		return nil, err
	}
	want := []string{authorizationUsed, "0x" + padded(r.payer), r.nonce}
	for i, entry := range logs {
		if !r.fromAsset(entry) || !topicsEqual(entry["topics"], want) {
			continue
		}
		if i+1 < len(logs) && r.fromAsset(logs[i+1]) && r.tokenTransferFromPayer(logs[i+1]) {
			return logs[i+1], nil
		}
		break
	}
	return nil, inconclusive("the receipt for %s doesn't show the authorization's transfer", hash)
}

// source: app/services/x402/chain/evm_reader.rb:89-97
func (r *evmReader) receiptLogs(ctx context.Context, hash string) ([]x402.Object, error) {
	v, err := r.call(ctx, "eth_getTransactionReceipt", hash)
	if err != nil {
		return nil, err
	}
	receipt, ok := v.(map[string]any)
	if ok && receipt["status"] == "0x1" && strings.EqualFold(rubyString(receipt["transactionHash"]), hash) {
		if logs, ok := objects(receipt["logs"]); ok {
			return logs, nil
		}
	}
	return nil, inconclusive("no successful receipt for %s", hash)
}

// source: app/services/x402/chain/evm_reader.rb:99-102
func (r *evmReader) tokenTransferFromPayer(entry x402.Object) bool {
	topics, ok := list(entry["topics"])
	return ok && len(topics) == 3 && topics[0] == erc20Transfer && topics[1] == "0x"+padded(r.payer) &&
		x402.HexWord.MatchString(str(topics[2])) && x402.HexWord.MatchString(str(entry["data"]))
}

func (r *evmReader) fromAsset(entry x402.Object) bool {
	return strings.EqualFold(rubyString(entry["address"]), r.asset)
}

// source: app/services/x402/chain/evm_reader.rb:108-116
func (r *evmReader) authorizationUsed(ctx context.Context) (bool, error) {
	data := authorizationState + padded(r.payer) + strings.TrimPrefix(r.nonce, "0x")
	safe, err := r.safeHead(ctx)
	if err != nil {
		return false, err
	}
	v, err := r.call(ctx, "eth_call", map[string]any{"to": r.asset, "data": data}, hexNumber(safe.number))
	if err != nil {
		return false, err
	}
	word := str(v)
	if !booleanWord.MatchString(word) {
		return false, inconclusive("authorizationState answered %s", inspect(v))
	}
	return strings.HasSuffix(word, "1"), nil
}

// source: app/services/x402/chain/evm_reader.rb:118-125
func (r *evmReader) findAuthorizationLog(ctx context.Context) (x402.Object, error) {
	from, err := r.lowerBound(ctx)
	if err != nil {
		return nil, err
	}
	to, err := r.upperBound(ctx)
	if err != nil {
		return nil, err
	}
	log, err := r.scanLogs(ctx, from, to, []any{authorizationUsed, authorizationCanceled})
	if err != nil || log != nil {
		return log, err
	}
	safe, err := r.safeHead(ctx)
	if err != nil {
		return nil, err
	}
	if log, err = r.scanLogs(ctx, to+1, safe.number, []any{authorizationCanceled}); err != nil || log != nil {
		return log, err
	}
	return nil, inconclusive("the nonce is used but no AuthorizationUsed or AuthorizationCanceled event was found in blocks %d..%d", from, safe.number)
}

// scanLogs scans [from, to] in inclusive chunks of at most 1,000 blocks,
// at most 50 of them, for the first log of events not removed.
//
// source: app/services/x402/chain/evm_reader.rb:127-145
func (r *evmReader) scanLogs(ctx context.Context, from, to int64, events []any) (x402.Object, error) {
	for i, start := 0, from; i < maxLogChunks && start <= to; i, start = i+1, start+logChunkBlocks {
		end := min(start+logChunkBlocks-1, to)
		v, err := r.call(ctx, "eth_getLogs", map[string]any{
			"address":   r.asset,
			"topics":    []any{events, "0x" + padded(r.payer), r.nonce},
			"fromBlock": hexNumber(start),
			"toBlock":   hexNumber(end),
		})
		if err != nil {
			return nil, err
		}
		if !truthy(v) {
			v = []any{}
		}
		logs, ok := objects(v)
		if ok {
			for _, entry := range logs {
				if !usableLog(entry) {
					ok = false
				}
			}
		}
		if !ok {
			return nil, inconclusive("eth_getLogs returned a malformed answer")
		}
		for _, entry := range logs {
			if !truthy(entry["removed"]) {
				return entry, nil
			}
		}
	}
	if to-from >= logChunkBlocks*maxLogChunks {
		return nil, inconclusive("the log window over blocks %d..%d exceeds %d chunks", from, to, maxLogChunks)
	}
	return nil, nil
}

func usableLog(entry x402.Object) bool {
	_, topics := list(entry["topics"])
	_, hash := entry["transactionHash"].(string)
	return topics && hash
}

// lowerBound finds a block dated at or before the window's start.
//
// source: app/services/x402/chain/evm_reader.rb:151-164
func (r *evmReader) lowerBound(ctx context.Context) (int64, error) {
	t := max(r.validAfter, r.since.Unix()-int64(evmLookback/time.Second))
	b, err := r.estimatedBlock(ctx, t)
	if err != nil {
		return 0, err
	}
	b -= marginBlocks
	for range maxBoundAttempts {
		if b <= 0 {
			return 0, nil
		}
		ts, err := r.blockTimestamp(ctx, b)
		if err != nil {
			return 0, err
		}
		late := ts - t
		if late <= 0 {
			return b, nil
		}
		b -= ceilHalf(late) + marginBlocks
	}
	return 0, inconclusive("could not find a block before %d", t)
}

// upperBound finds a block dated at or after the window's end, or the
// safe block.
//
// source: app/services/x402/chain/evm_reader.rb:166-179
func (r *evmReader) upperBound(ctx context.Context) (int64, error) {
	safe, err := r.safeHead(ctx)
	if err != nil {
		return 0, err
	}
	t := min(r.validBefore, safe.timestamp)
	b, err := r.estimatedBlock(ctx, t)
	if err != nil {
		return 0, err
	}
	b += marginBlocks
	for range maxBoundAttempts {
		if b >= safe.number {
			return safe.number, nil
		}
		ts, err := r.blockTimestamp(ctx, b)
		if err != nil {
			return 0, err
		}
		early := t - ts
		if early <= 0 {
			return b, nil
		}
		b += ceilHalf(early) + marginBlocks
	}
	return safe.number, nil
}

func (r *evmReader) estimatedBlock(ctx context.Context, t int64) (int64, error) {
	safe, err := r.safeHead(ctx)
	if err != nil {
		return 0, err
	}
	return safe.number - ceilHalf(safe.timestamp-t), nil
}

func (r *evmReader) safeHead(ctx context.Context) (evmBlock, error) {
	if r.safe != nil {
		return *r.safe, nil
	}
	b, err := r.readBlock(ctx, "safe", "no safe block")
	if err != nil {
		return evmBlock{}, err
	}
	r.safe = &b
	return b, nil
}

func (r *evmReader) blockTimestamp(ctx context.Context, number int64) (int64, error) {
	b, err := r.readBlock(ctx, hexNumber(number), fmt.Sprintf("block %d is unknown to the endpoint", number))
	return b.timestamp, err
}

// source: app/services/x402/chain/evm_reader.rb:205-213
func (r *evmReader) readBlock(ctx context.Context, tag, missing string) (evmBlock, error) {
	v, err := r.call(ctx, "eth_getBlockByNumber", tag, false)
	if err != nil {
		return evmBlock{}, err
	}
	block, ok := v.(map[string]any)
	if ok {
		number, okN := hexInt(block["number"])
		ts, okT := hexInt(block["timestamp"])
		if okN && okT {
			return evmBlock{number: number, timestamp: ts}, nil
		}
	}
	return evmBlock{}, inconclusive("%s", missing)
}

func (r *evmReader) call(ctx context.Context, method string, params ...any) (any, error) {
	if r.client == nil {
		c, err := r.chain.rpc(r.network)
		if err != nil {
			return nil, err
		}
		r.client = c
	}
	return r.client.call(ctx, method, params...)
}

// hexInt reads a hexadecimal quantity that fits an int64.
func hexInt(v any) (int64, bool) {
	s := str(v)
	if !hexQuantity.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s[2:], 16, 64)
	return n, err == nil
}

func hexNumber(n int64) string {
	if n < 0 {
		return "0x-" + strconv.FormatInt(-n, 16)
	}
	return "0x" + strconv.FormatInt(n, 16)
}

func padded(address string) string {
	a := strings.ToLower(strings.TrimPrefix(address, "0x"))
	if len(a) >= 64 {
		return a
	}
	return strings.Repeat("0", 64-len(a)) + a
}

func topicsEqual(v any, want []string) bool {
	topics, ok := list(v)
	if !ok || len(topics) != len(want) {
		return false
	}
	for i, t := range topics {
		if t != want[i] {
			return false
		}
	}
	return true
}

func objects(v any) ([]x402.Object, bool) {
	l, ok := list(v)
	if !ok {
		return nil, false
	}
	out := make([]x402.Object, 0, len(l))
	for _, e := range l {
		o, ok := e.(map[string]any)
		if !ok {
			return nil, false
		}
		out = append(out, o)
	}
	return out, true
}

func truthy(v any) bool { return v != nil && v != false }

func leadingDigits(s string) string {
	end := 0
	for end < len(s) && '0' <= s[end] && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return "0"
	}
	return s[:end]
}
