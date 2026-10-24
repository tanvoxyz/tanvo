package x402

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/chain/evm_reader_spec.rb

const (
	evmLogTime     = int64(1_789_649_274)
	evmValidBefore = int64(1_789_649_331)
	evmAsset       = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
	evmNonce       = "0x8568d530303a96028f64623cfc5c7bbb166c4aa2889b25bbe09e1d699193f174"
	evmTx          = "0xefad33f01282b3a8b18893a9eb0aecbc2fc210ffcfa7fda7ca79f14c8822963a"
	evmPaddedPayer = "0x000000000000000000000000f4a43b9cc729c9e4e139cb86808f48e3ed09dcb2"
)

type evmWorld struct {
	t            *testing.T
	log          x402.Object
	receipt      x402.Object
	logBlock     int64
	safeBlock    int64
	used         bool
	logs         []x402.Object
	chainID      string
	blockTime    func(n int64) int64
	auth         x402.Object
	payment      x402.Object
	requirements x402.Object
	since        time.Time
	override     func(c rpcCall) (any, *cdpAnswer, bool)
	rpc          *fakeRPC
}

func clone(t *testing.T, v any) x402.Object {
	t.Helper()
	b, _ := json.Marshal(v)
	return jsonObject(t, string(b))
}

func newEVMWorld(t *testing.T) *evmWorld {
	t.Helper()
	data := jsonObject(t, fixture(t, "base_sepolia_authorization_used.json"))
	w := &evmWorld{t: t, log: object(data["log"]), receipt: object(data["receipt"]), used: true, chainID: "0x14a34"}
	w.logBlock = rubyHex(str(w.log["blockNumber"])).Int64()
	w.safeBlock = w.logBlock + 90
	w.logs = []x402.Object{w.log}
	w.blockTime = func(n int64) int64 { return evmLogTime + 2*(n-w.logBlock) }
	w.auth = x402.Object{
		"from": "0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2", "to": "0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
		"value": "1000", "validAfter": strconv.FormatInt(evmValidBefore-660, 10), "validBefore": strconv.FormatInt(evmValidBefore, 10),
		"nonce": evmNonce,
	}
	w.requirements = x402.Object{"scheme": "exact", "network": "eip155:84532", "asset": evmAsset}
	w.since = time.Unix(evmLogTime-4, 0)
	w.rpc = newFakeRPC(t, w.answer)
	return w
}

func (w *evmWorld) answer(c rpcCall) (any, *cdpAnswer) {
	if w.override != nil {
		if r, raw, ok := w.override(c); ok {
			return r, raw
		}
	}
	switch c.Method {
	case "eth_chainId":
		return w.chainID, nil
	case "eth_getBlockByNumber":
		tag := c.Params[0].(string)
		n := w.safeBlock
		if tag != "safe" {
			n = rubyHex(tag).Int64()
		}
		return map[string]any{"number": hexNumber(n), "timestamp": hexNumber(w.blockTime(n))}, nil
	case "eth_call":
		bit := "0"
		if w.used && rubyHex(c.Params[1].(string)).Int64() >= w.logBlock {
			bit = "1"
		}
		return "0x" + strings.Repeat("0", 63) + bit, nil
	case "eth_getLogs":
		q := c.Params[0].(map[string]any)
		from, to := rubyHex(q["fromBlock"].(string)).Int64(), rubyHex(q["toBlock"].(string)).Int64()
		out := []any{}
		for _, l := range w.logs {
			if b := rubyHex(str(l["blockNumber"])).Int64(); from <= b && b <= to {
				out = append(out, l)
			}
		}
		return out, nil
	case "eth_getTransactionReceipt":
		return w.receipt, nil
	}
	return nil, nil
}

func (w *evmWorld) reader() x402.ChainReader {
	w.t.Helper()
	p := w.payment
	if p == nil {
		p = x402.Object{"x402Version": 2, "payload": x402.Object{"signature": "0x00", "authorization": w.auth}}
	}
	r, err := w.rpc.readers("eip155:84532").For("eip155:84532", p, w.requirements, w.since)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *evmWorld) receiptLogs() []x402.Object {
	l, _ := objects(clone(w.t, w.receipt)["logs"])
	return l
}

func (w *evmWorld) withReceiptLogs(logs ...x402.Object) {
	r := clone(w.t, w.receipt)
	anys := make([]any, len(logs))
	for i, l := range logs {
		anys[i] = l
	}
	r["logs"] = anys
	w.receipt = r
}

func withTopics(t *testing.T, l x402.Object, topics ...any) x402.Object {
	c := clone(t, l)
	c["topics"] = topics
	return c
}

func canceled(t *testing.T, l x402.Object) x402.Object {
	topics, _ := list(l["topics"])
	return withTopics(t, l, append([]any{authorizationCanceled}, topics[1:]...)...)
}

type evmWant struct {
	settled, final bool
	identifier     string
}

func (w *evmWorld) want(want evmWant) {
	w.t.Helper()
	ctx := context.Background()
	r := w.reader()
	settled, err := r.Settled(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	id, err := r.Identifier(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	final, err := r.Final(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if settled != want.settled || final != want.final || id != want.identifier {
		w.t.Errorf("got settled = %v, final = %v, identifier = %q, want = %+v", settled, final, id, want)
	}
}

func (w *evmWorld) wantError(kind error, contains string) {
	w.t.Helper()
	_, err := w.reader().Settled(context.Background())
	wantChainError(w.t, err, kind, contains)
}

func TestEVMReaderSettled(t *testing.T) {
	ctx := context.Background()

	t.Run("the authorization paid the payee", func(t *testing.T) { // :66-106
		w := newEVMWorld(t)
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
		call := w.rpc.callsOf("eth_call")
		if len(call) != 1 {
			t.Fatalf("got %d eth_call, want 1", len(call))
		}
		wantParams := []any{
			map[string]any{"to": evmAsset, "data": "0xe94a0102" + strings.TrimPrefix(evmPaddedPayer, "0x") + strings.TrimPrefix(evmNonce, "0x")},
			hexNumber(w.safeBlock),
		}
		if inspect(call[0].Params) != inspect(wantParams) {
			t.Errorf("got eth_call params = %s, want = %s", inspect(call[0].Params), inspect(wantParams))
		}
		q := w.rpc.callsOf("eth_getLogs")[0].Params[0].(map[string]any)
		wantTopics := []any{[]any{authorizationUsed, authorizationCanceled}, evmPaddedPayer, evmNonce}
		if q["address"] != evmAsset || inspect(q["topics"]) != inspect(wantTopics) {
			t.Errorf("got getLogs filter = %s", inspect(q))
		}
	})

	t.Run("reads each fact once", func(t *testing.T) { // :101-105
		w := newEVMWorld(t)
		r := w.reader()
		for range 2 {
			_, _ = r.Settled(ctx)
			_, _ = r.Identifier(ctx)
			_, _ = r.Final(ctx)
		}
		if n := len(w.rpc.callsOf("eth_call")); n != 1 {
			t.Errorf("got %d eth_call, want 1", n)
		}
	})

	t.Run("the settle answer named another hash", func(t *testing.T) { // :108-114
		w := newEVMWorld(t)
		w.payment = x402.Object{
			"x402Version": 2, "payload": x402.Object{"authorization": w.auth},
			"settleResponse": x402.Object{"transaction": "0x" + strings.Repeat("99", 32)},
		}
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})

	t.Run("unused, past validBefore", func(t *testing.T) { // :116-135
		w := newEVMWorld(t)
		w.used = false
		w.want(evmWant{final: true})
		var tags []any
		for _, c := range w.rpc.callsOf("eth_getBlockByNumber") {
			tags = append(tags, c.Params[0])
		}
		if inspect(tags) != `["safe"]` {
			t.Errorf("got blocks read = %v, want only the safe block", tags)
		}
	})

	t.Run("unused, before validBefore", func(t *testing.T) { // :137-143
		w := newEVMWorld(t)
		w.used = false
		w.safeBlock = w.logBlock + 10
		w.want(evmWant{})
	})

	t.Run("the payment's block is not yet safe", func(t *testing.T) { // :146-165
		w := newEVMWorld(t)
		w.safeBlock = w.logBlock - 1
		w.want(evmWant{})
		if n := len(w.rpc.callsOf("eth_getTransactionReceipt")); n != 0 {
			t.Errorf("got %d receipts read, want none", n)
		}
	})

	t.Run("a cancellation in a block not yet safe", func(t *testing.T) { // :167-173
		w := newEVMWorld(t)
		w.safeBlock = w.logBlock - 1
		w.logs = []x402.Object{canceled(t, w.log)}
		w.want(evmWant{})
	})

	t.Run("the log's block is the safe block", func(t *testing.T) { // :176-182
		w := newEVMWorld(t)
		w.safeBlock = w.logBlock
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})

	t.Run("canceled", func(t *testing.T) { // :233-243
		w := newEVMWorld(t)
		w.logs = []x402.Object{canceled(t, w.log)}
		w.want(evmWant{final: true})
	})

	for _, offset := range []int64{300, 1_000} { // :245-264
		t.Run("canceled "+strconv.FormatInt(offset, 10)+" blocks after", func(t *testing.T) {
			w := newEVMWorld(t)
			w.safeBlock = w.logBlock + 5_000
			l := canceled(t, w.log)
			l["blockNumber"] = hexNumber(w.logBlock + offset)
			w.logs = []x402.Object{l}
			w.want(evmWant{final: true})
			calls := w.rpc.callsOf("eth_getLogs")
			last := calls[len(calls)-1].Params[0].(map[string]any)["topics"].([]any)[0]
			if inspect(last) != `["`+authorizationCanceled+`"]` {
				t.Errorf("got last topics = %v, want cancellations only", last)
			}
		})
	}

	t.Run("the nonce paid someone else", func(t *testing.T) { // :266-279
		w := newEVMWorld(t)
		logs := w.receiptLogs()
		tt, _ := list(logs[1]["topics"])
		w.withReceiptLogs(logs[0], withTopics(t, logs[1], tt[0], tt[1], "0x"+strings.Repeat("11", 32)))
		w.want(evmWant{final: true})
	})

	t.Run("the transfer's amount differs", func(t *testing.T) { // :281-290
		w := newEVMWorld(t)
		logs := w.receiptLogs()
		logs[1]["data"] = "0x" + strings.Repeat("0", 63) + "1"
		w.withReceiptLogs(logs...)
		w.want(evmWant{final: true})
	})

	t.Run("another authorization in the same transaction pays the payee", func(t *testing.T) { // :338-353
		w := newEVMWorld(t)
		logs := w.receiptLogs()
		used, transfer := logs[0], logs[1]
		tt, _ := list(transfer["topics"])
		ut, _ := list(used["topics"])
		elsewhere := withTopics(t, transfer, tt[0], tt[1], "0x"+strings.Repeat("11", 32))
		otherUsed := withTopics(t, used, ut[0], ut[1], "0x"+strings.Repeat("55", 32))
		w.withReceiptLogs(used, elsewhere, otherUsed, transfer)
		w.want(evmWant{final: true})
	})

	t.Run("read a day later", func(t *testing.T) { // :382-401
		w := newEVMWorld(t)
		w.safeBlock = w.logBlock + 43_200
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
		calls := w.rpc.callsOf("eth_getLogs")
		if len(calls) != 1 {
			t.Errorf("got %d eth_getLogs, want 1", len(calls))
		}
		for _, c := range calls {
			q := c.Params[0].(map[string]any)
			if rubyHex(q["toBlock"].(string)).Int64()-rubyHex(q["fromBlock"].(string)).Int64() >= 1_000 {
				t.Errorf("got a chunk of %v..%v", q["fromBlock"], q["toBlock"])
			}
		}
	})

	t.Run("blocks slower than estimated", func(t *testing.T) { // :403-413
		w := newEVMWorld(t)
		w.blockTime = func(n int64) int64 { return evmLogTime + 3*(n-w.logBlock) }
		w.safeBlock = w.logBlock + 2_000
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})

	t.Run("blocks faster than estimated", func(t *testing.T) { // :415-425
		w := newEVMWorld(t)
		w.blockTime = func(n int64) int64 { return evmLogTime + floorDiv((n-w.logBlock)*9, 5) }
		w.safeBlock = w.logBlock + 2_000
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})

	t.Run("block times stall before validBefore", func(t *testing.T) { // :437-449
		w := newEVMWorld(t)
		w.safeBlock = w.logBlock + 43_200
		w.blockTime = func(n int64) int64 {
			if n < w.logBlock || n == w.safeBlock {
				return evmLogTime + 2*(n-w.logBlock)
			}
			return evmValidBefore - 1
		}
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})

	t.Run("the authorization carries numbers", func(t *testing.T) { // :476-482
		w := newEVMWorld(t)
		w.auth["value"] = json.Number("1000")
		w.auth["validAfter"] = json.Number(strconv.FormatInt(evmValidBefore-660, 10))
		w.auth["validBefore"] = json.Number(strconv.FormatInt(evmValidBefore, 10))
		w.want(evmWant{settled: true, final: true, identifier: evmTx})
	})
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func TestEVMReaderErrors(t *testing.T) {
	onMethod := func(method string, result any) func(c rpcCall) (any, *cdpAnswer, bool) {
		return func(c rpcCall) (any, *cdpAnswer, bool) { return result, nil, c.Method == method }
	}
	cases := []struct {
		name, line string
		setup      func(w *evmWorld)
		kind       error
		contains   string
	}{
		{"no safe block", ":184-193", func(w *evmWorld) {
			w.override = func(c rpcCall) (any, *cdpAnswer, bool) {
				return nil, nil, c.Method == "eth_getBlockByNumber" && c.Params[0] == "safe"
			}
		}, x402.ErrChainInconclusive, "no safe block"},
		{"a safe block without a number", ":195-204", func(w *evmWorld) {
			w.override = func(c rpcCall) (any, *cdpAnswer, bool) {
				return map[string]any{"timestamp": "0x1"}, nil, c.Method == "eth_getBlockByNumber" && c.Params[0] == "safe"
			}
		}, x402.ErrChainInconclusive, "no safe block"},
		{"eth_getLogs answers an object", ":206-215", func(w *evmWorld) {
			w.override = onMethod("eth_getLogs", map[string]any{"logs": []any{}})
		}, x402.ErrChainInconclusive, "eth_getLogs"},
		{"a log without topics", ":217-223", func(w *evmWorld) {
			l := clone(w.t, w.log)
			delete(l, "topics")
			w.logs = []x402.Object{l}
		}, x402.ErrChainInconclusive, "eth_getLogs"},
		{"a receipt without logs", ":225-231", func(w *evmWorld) {
			r := clone(w.t, w.receipt)
			delete(r, "logs")
			w.receipt = r
		}, x402.ErrChainInconclusive, "receipt"},
		{"empty receipt logs", ":292-298", func(w *evmWorld) { w.withReceiptLogs() }, x402.ErrChainInconclusive, "receipt"},
		{"a receipt of another transaction", ":300-306", func(w *evmWorld) {
			r := clone(w.t, w.receipt)
			r["transactionHash"] = "0x" + strings.Repeat("22", 32)
			w.receipt = r
		}, x402.ErrChainInconclusive, "receipt"},
		{"only the authorization's event", ":308-314", func(w *evmWorld) { w.withReceiptLogs(w.receiptLogs()[0]) }, x402.ErrChainInconclusive, "transfer"},
		{"an event for another nonce", ":316-325", func(w *evmWorld) {
			logs := w.receiptLogs()
			ut, _ := list(logs[0]["topics"])
			w.withReceiptLogs(withTopics(w.t, logs[0], ut[0], ut[1], "0x"+strings.Repeat("33", 32)), logs[1])
		}, x402.ErrChainInconclusive, "transfer"},
		{"a next log that is not the token's transfer", ":327-336", func(w *evmWorld) {
			logs := w.receiptLogs()
			logs[1]["address"] = "0x" + strings.Repeat("44", 20)
			w.withReceiptLogs(logs...)
		}, x402.ErrChainInconclusive, "transfer"},
		{"a failed transaction", ":355-361", func(w *evmWorld) {
			r := clone(w.t, w.receipt)
			r["status"] = "0x0"
			w.receipt = r
		}, x402.ErrChainInconclusive, "receipt"},
		{"a used nonce without an event", ":363-369", func(w *evmWorld) { w.logs = nil }, x402.ErrChainInconclusive, "no AuthorizationUsed"},
		{"an empty eth_call", ":371-380", func(w *evmWorld) { w.override = onMethod("eth_call", "0x") }, x402.ErrChainInconclusive, "authorizationState"},
		{"no block precedes the window", ":427-435", func(w *evmWorld) {
			w.blockTime = func(int64) int64 { return evmLogTime + 86_400 }
		}, x402.ErrChainInconclusive, "could not find a block before"},
		{"a window starting before the first block", ":451-458", func(w *evmWorld) {
			w.auth["validAfter"] = "0"
			w.since = time.Unix(0, 1)
			w.logs = nil
		}, x402.ErrChainInconclusive, "blocks 0.."},
		{"the chunk cap", ":460-462", func(w *evmWorld) {
			w.auth["validAfter"] = "0"
			w.since = time.Unix(0, 1)
			w.logs = nil
		}, x402.ErrChainInconclusive, "exceeds 50 chunks"},
		{"a bound block unknown to the endpoint", ":465-474", func(w *evmWorld) {
			w.override = func(c rpcCall) (any, *cdpAnswer, bool) {
				return nil, nil, c.Method == "eth_getBlockByNumber" && c.Params[0] != "safe"
			}
		}, x402.ErrChainInconclusive, "unknown to the endpoint"},
		{"another chain", ":502-508", func(w *evmWorld) { w.chainID = "0x2105" }, x402.ErrChainInconclusive, "another chain"},
		{"unreachable", ":510-519", func(w *evmWorld) {
			w.override = func(rpcCall) (any, *cdpAnswer, bool) { return nil, &cdpAnswer{drop: true}, true }
		}, x402.ErrChainUnreachable, ""},
		{"a malformed nonce", ":521-527", func(w *evmWorld) { w.auth["nonce"] = "0x1234" }, x402.ErrUnreadablePayment, ""},
		{"a payload that is not an object", ":529-535", func(w *evmWorld) {
			w.payment = x402.Object{"x402Version": 2, "payload": "abc"}
		}, x402.ErrUnreadablePayment, ""},
		{"an authorization that is a list", ":537-543", func(w *evmWorld) {
			w.payment = x402.Object{"x402Version": 2, "payload": x402.Object{"authorization": []any{"abc"}}}
		}, x402.ErrUnreadablePayment, ""},
		{"requirements naming no contract", ":545-551", func(w *evmWorld) {
			w.requirements = x402.Object{"asset": "USDC"}
		}, x402.ErrUnreadablePayment, ""},
	}
	// source: evm_reader_spec.rb:484-492
	for _, bad := range []any{json.Number("-1"), "1.5", nil} {
		cases = append(cases, struct {
			name, line string
			setup      func(w *evmWorld)
			kind       error
			contains   string
		}{"value " + inspect(bad), ":484-492", func(w *evmWorld) { w.auth["value"] = bad }, x402.ErrUnreadablePayment, ""})
	}
	for _, c := range cases {
		t.Run(c.line+" "+c.name, func(t *testing.T) {
			w := newEVMWorld(t)
			c.setup(w)
			w.wantError(c.kind, c.contains)
		})
	}

	t.Run("without a since", func(t *testing.T) { // :494-500
		w := newEVMWorld(t)
		w.since = time.Time{}
		if _, err := w.reader().Settled(context.Background()); !errors.Is(err, errNoSince) {
			t.Errorf("got error = %v, want errNoSince", err)
		}
	})

	t.Run("a network without a family", func(t *testing.T) {
		if _, err := NewChainReaders().For("cosmos:hub", nil, nil, time.Now()); !errors.Is(err, x402.ErrChainUnreachable) {
			t.Errorf("got error = %v", err)
		}
	})
}
