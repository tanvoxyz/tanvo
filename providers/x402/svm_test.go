package x402

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/chain/svm_reader_spec.rb

const (
	svmNetwork  = x402.Network("solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1")
	svmTxID     = "7scJSNzkdyryaFQcfRX52upVRridoUijgMNaUmLBtJTxB9iCjmEDk2Mk54eEuTYoPqAgdtajPvxDMk9RFimsZ93"
	svmBuyer    = "BprZ3eTVMHAcqC2wcE4XY71tvjdxJ6C6pSYjVmD75ujf"
	svmGenesis  = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcWFFmQcPYDT"
	svmFeePayer = "D6ZhtNQ5nT9ZnTHUbqXZsTx5MH2rPFiBBggX4hY1WePM"
	svmToken    = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	svmSystem   = "11111111111111111111111111111111"
)

type svmWorld struct {
	t            *testing.T
	slot, bt     int64
	landedJSON   x402.Object
	signed       []byte
	payment      x402.Object
	since        time.Time
	valid        bool
	contextSlot  int64
	expiry       any
	genesis      string
	history      []x402.Object
	transactions func(sig string) any
	slotTime     func(slot int64) any
	override     func(c rpcCall) (any, *cdpAnswer, bool)
	rpc          *fakeRPC
}

func newSVMWorld(t *testing.T) *svmWorld {
	t.Helper()
	landed := jsonObject(t, fixture(t, "solana_devnet_transfer.json"))
	w := &svmWorld{t: t, landedJSON: jsonObject(t, fixture(t, "solana_devnet_transfer_as_json.json")), genesis: svmGenesis}
	w.slot, _ = jsonInteger(landed["slot"])
	w.bt, _ = jsonInteger(landed["blockTime"])
	txs, _ := list(landed["transaction"])
	raw, err := base64.StdEncoding.DecodeString(txs[0].(string))
	if err != nil {
		t.Fatal(err)
	}
	copy(raw[1:65], make([]byte, 64))
	w.signed = raw
	w.since = time.Unix(w.bt-5, 0)
	w.contextSlot = w.slot + 400
	w.history = []x402.Object{{"signature": svmTxID, "slot": w.slot, "err": nil, "blockTime": w.bt}}
	known := map[string]any{svmTxID: w.landedJSON}
	w.transactions = func(sig string) any { return known[sig] }
	w.slotTime = func(slot int64) any { return w.bt + (slot-w.slot)*2/5 }
	w.rpc = newFakeRPC(t, w.answer)
	return w
}

func (w *svmWorld) answer(c rpcCall) (any, *cdpAnswer) {
	if w.override != nil {
		if r, raw, ok := w.override(c); ok {
			return r, raw
		}
	}
	switch c.Method {
	case "isBlockhashValid":
		if w.expiry != nil {
			return w.expiry, nil
		}
		return map[string]any{"context": map[string]any{"slot": w.contextSlot}, "value": w.valid}, nil
	case "getSignaturesForAddress":
		q := c.Params[1].(map[string]any)
		start := 0
		if before, ok := q["before"].(string); ok {
			for i, e := range w.history {
				if e["signature"] == before {
					start = i + 1
				}
			}
		}
		limit, _ := strconv.Atoi(string(q["limit"].(json.Number)))
		page := []any{}
		for _, e := range w.history[start:min(start+limit, len(w.history))] {
			page = append(page, e)
		}
		return page, nil
	case "getTransaction":
		tx := w.transactions(c.Params[0].(string))
		if o, ok := tx.(x402.Object); ok {
			if v, ok := o["version"].(int); ok {
				max, _ := strconv.Atoi(string(c.Params[1].(map[string]any)["maxSupportedTransactionVersion"].(json.Number)))
				if v > max {
					return nil, &cdpAnswer{status: 200, body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32015,"message":"Transaction version is not supported"}}`}
				}
			}
		}
		return tx, nil
	case "getBlockTime":
		slot, _ := jsonInteger(c.Params[0])
		return w.slotTime(slot), nil
	case "getGenesisHash":
		return w.genesis, nil
	}
	return nil, nil
}

func (w *svmWorld) reader() x402.ChainReader {
	w.t.Helper()
	p := w.payment
	if p == nil {
		p = x402.Object{"x402Version": 2, "payload": x402.Object{"transaction": base64.StdEncoding.EncodeToString(w.signed)}}
	}
	r, err := w.rpc.readers(svmNetwork).For(svmNetwork, p, x402.Object{}, w.since)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func otherEntry(index int, blockTime any, slot int64) x402.Object {
	sig := bytes.Repeat(binary.BigEndian.AppendUint32(nil, uint32(index)), 16)
	return x402.Object{"signature": x402.Base58Encode(sig), "slot": slot - int64(index), "err": nil, "blockTime": blockTime}
}

func (w *svmWorld) otherTransaction() x402.Object {
	sig := make([]byte, 64)
	_, _ = rand.Read(sig)
	o := clone(w.t, w.landedJSON)
	o["transaction"] = map[string]any{"signatures": []any{x402.Base58Encode(sig)}}
	return o
}

func (w *svmWorld) others(n, from int, blockTime any) []x402.Object {
	out := make([]x402.Object, n)
	for i := range n {
		out[i] = otherEntry(i+from, blockTime, w.slot)
	}
	return out
}

func (w *svmWorld) want(settled, final bool, id string) {
	w.t.Helper()
	ctx := context.Background()
	r := w.reader()
	s, err := r.Settled(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	gotID, err := r.Identifier(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	f, err := r.Final(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if s != settled || f != final || gotID != id {
		w.t.Errorf("got settled = %v, final = %v, identifier = %q, want = %v, %v, %q", s, f, gotID, settled, final, id)
	}
}

func (w *svmWorld) wantError(kind error, contains string) {
	w.t.Helper()
	_, err := w.reader().Settled(context.Background())
	wantChainError(w.t, err, kind, contains)
}

func TestSVMReaderSettled(t *testing.T) {
	ctx := context.Background()

	t.Run("the buyer's transaction landed", func(t *testing.T) { // :87-123
		w := newSVMWorld(t)
		w.want(true, true, svmTxID)
		for method, want := range map[string]string{
			"getSignaturesForAddress": `["` + svmBuyer + `",{"commitment":"finalized","limit":1000,"minContextSlot":` + strconv.FormatInt(w.contextSlot, 10) + `}]`,
			"isBlockhashValid":        `["7LPzenu2Lg6XG5aSrZ6ihu7GFy9gHYEh5KVpVVxrQYue",{"commitment":"finalized"}]`,
			"getTransaction":          `["` + svmTxID + `",{"commitment":"finalized","encoding":"json","maxSupportedTransactionVersion":1}]`,
		} {
			calls := w.rpc.callsOf(method)
			if len(calls) != 1 || inspect(calls[0].Params) != want {
				t.Errorf("got %s calls = %+v, want one with %s", method, calls, want)
			}
		}
	})

	t.Run("reads each fact once", func(t *testing.T) { // :118-122
		w := newSVMWorld(t)
		r := w.reader()
		for range 2 {
			_, _ = r.Settled(ctx)
			_, _ = r.Identifier(ctx)
			_, _ = r.Final(ctx)
		}
		if n := len(w.rpc.callsOf("getTransaction")); n != 1 {
			t.Errorf("got %d getTransaction, want 1", n)
		}
	})

	t.Run("other transactions come first", func(t *testing.T) { // :125-132
		w := newSVMWorld(t)
		others := w.others(2, 1, w.bt)
		w.history = append(others, w.history...)
		base := w.transactions
		o1, o2 := w.otherTransaction(), w.otherTransaction()
		w.transactions = func(sig string) any {
			switch sig {
			case others[0]["signature"]:
				return o1
			case others[1]["signature"]:
				return o2
			}
			return base(sig)
		}
		w.want(true, true, svmTxID)
	})

	t.Run("a v1 transaction comes first", func(t *testing.T) { // :134-141
		w := newSVMWorld(t)
		other := otherEntry(1, w.bt, w.slot)
		w.history = append([]x402.Object{other}, w.history...)
		base := w.transactions
		v1 := w.otherTransaction()
		v1["version"] = 1
		w.transactions = func(sig string) any {
			if sig == other["signature"] {
				return v1
			}
			return base(sig)
		}
		w.want(true, true, svmTxID)
	})

	t.Run("did not land, blockhash expired", func(t *testing.T) { // :143-162
		w := newSVMWorld(t)
		w.history = nil
		w.want(false, true, "")
		r := w.reader()
		_, _ = r.Final(ctx)
		_, _ = r.Final(ctx)
		if n := len(w.rpc.callsOf("getBlockTime")); n != 2 { // one from want, one from r
			t.Errorf("got %d getBlockTime, want one per reader", n)
		}
	})

	t.Run("did not land, blockhash still valid", func(t *testing.T) { // :164-170
		w := newSVMWorld(t)
		w.history = nil
		w.valid = true
		w.want(false, false, "")
	})

	t.Run("the slot is not proven past the blockhash's life", func(t *testing.T) { // :173-180
		w := newSVMWorld(t)
		w.history = nil
		w.contextSlot = w.slot - 50
		w.want(false, false, "")
	})

	t.Run("the node cannot date its slot", func(t *testing.T) { // :182-192
		w := newSVMWorld(t)
		w.history = nil
		w.slotTime = func(int64) any { return nil }
		w.want(false, false, "")
	})

	t.Run("the cluster's block time runs minutes behind", func(t *testing.T) { // :251-257
		w := newSVMWorld(t)
		w.history[0]["blockTime"] = w.since.Unix() - 300
		w.want(true, true, svmTxID)
	})

	t.Run("thousands of later transactions", func(t *testing.T) { // :259-272
		w := newSVMWorld(t)
		w.history = append(w.others(6_000, 10, w.since.Unix()+3_600), w.history...)
		w.want(true, true, svmTxID)
		if n := len(w.rpc.callsOf("getTransaction")); n != 1 {
			t.Errorf("got %d getTransaction, want 1", n)
		}
	})

	t.Run("landed with an error", func(t *testing.T) { // :274-285
		w := newSVMWorld(t)
		failed := clone(t, w.landedJSON)
		failed["meta"] = map[string]any{"err": map[string]any{"InstructionError": []any{2, map[string]any{"Custom": 1}}}}
		w.transactions = func(string) any { return failed }
		w.want(false, true, "")
	})

	t.Run("the history reaches back before the row", func(t *testing.T) { // :312-318
		w := newSVMWorld(t)
		w.history = append([]x402.Object{otherEntry(1, w.since.Unix()-3_600, w.slot)}, w.history...)
		w.want(false, true, "")
	})

	t.Run("a signature without a block time", func(t *testing.T) { // :320-327
		w := newSVMWorld(t)
		other := otherEntry(1, nil, w.slot)
		w.history = append([]x402.Object{other}, w.history...)
		base, o := w.transactions, w.otherTransaction()
		w.transactions = func(sig string) any {
			if sig == other["signature"] {
				return o
			}
			return base(sig)
		}
		w.want(true, true, svmTxID)
	})

	t.Run("the history spans several pages", func(t *testing.T) { // :329-344
		w := newSVMWorld(t)
		others := w.others(1_000, 10, w.bt)
		w.history = append(others, w.history...)
		base, o := w.transactions, w.otherTransaction()
		w.transactions = func(sig string) any {
			if sig == svmTxID {
				return base(sig)
			}
			return o
		}
		w.want(true, true, svmTxID)
		calls := w.rpc.callsOf("getSignaturesForAddress")
		if before := calls[len(calls)-1].Params[1].(map[string]any)["before"]; before != others[999]["signature"] {
			t.Errorf("got before = %v, want the last signature of the first page", before)
		}
	})

	t.Run("the window fills exactly five pages", func(t *testing.T) { // :355-362
		w := newSVMWorld(t)
		w.history = append(w.others(5_000, 10, w.bt), otherEntry(9, w.since.Unix()-3_600, w.slot))
		o := w.otherTransaction()
		w.transactions = func(string) any { return o }
		w.want(false, true, "")
	})
}

// buildSolana builds a v0 transaction with a 0x07 blockhash.
//
// source: spec/support/x402/solana_transaction_builder.rb
func buildSolana(t *testing.T, keys []string, instructions [][3][]byte, signatures [][]byte) []byte {
	t.Helper()
	var m bytes.Buffer
	m.WriteByte(0x80)
	m.Write([]byte{byte(len(signatures)), 0, 0})
	m.Write(compactU16(len(keys)))
	for _, k := range keys {
		b, ok := x402.Base58Decode(k)
		if !ok {
			t.Fatalf("base58 %q", k)
		}
		m.Write(b)
	}
	m.Write(bytes.Repeat([]byte{7}, 32))
	m.Write(compactU16(len(instructions)))
	for _, in := range instructions {
		m.Write(in[0])
		m.Write(compactU16(len(in[1])))
		m.Write(in[1])
		m.Write(compactU16(len(in[2])))
		m.Write(in[2])
	}
	m.Write(compactU16(0))
	out := compactU16(len(signatures))
	for _, s := range signatures {
		out = append(out, s...)
	}
	return append(out, m.Bytes()...)
}

func compactU16(v int) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, c)
		}
		out = append(out, c|0x80)
	}
}

func transferChecked(amount uint64) []byte {
	return append(binary.LittleEndian.AppendUint64([]byte{12}, amount), 6)
}

func TestSVMReaderErrors(t *testing.T) {
	sig := func(b byte) []byte { return bytes.Repeat([]byte{b}, 64) }
	ins := func(program byte, accounts []byte, data []byte) [3][]byte {
		return [3][]byte{{program}, accounts, data}
	}
	cases := []struct {
		name, line string
		setup      func(w *svmWorld)
		kind       error
		contains   string
	}{
		{"isBlockhashValid without its context", ":202-208", func(w *svmWorld) {
			w.expiry = map[string]any{"value": false}
		}, x402.ErrChainInconclusive, "isBlockhashValid"},
		{"a null history page", ":210-221", func(w *svmWorld) {
			w.override = func(c rpcCall) (any, *cdpAnswer, bool) { return nil, nil, c.Method == "getSignaturesForAddress" }
		}, x402.ErrChainInconclusive, "getSignaturesForAddress"},
		{"a false history page", ":210-221", func(w *svmWorld) {
			w.override = func(c rpcCall) (any, *cdpAnswer, bool) { return false, nil, c.Method == "getSignaturesForAddress" }
		}, x402.ErrChainInconclusive, "getSignaturesForAddress"},
		{"an entry without a signature", ":223-229", func(w *svmWorld) {
			w.history = []x402.Object{{"slot": w.slot, "blockTime": w.bt}}
		}, x402.ErrChainInconclusive, "getSignaturesForAddress"},
		{"another cluster", ":243-249", func(w *svmWorld) {
			w.genesis = "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d"
		}, x402.ErrChainInconclusive, "another cluster"},
		{"null metadata", ":287-294", func(w *svmWorld) {
			o := clone(w.t, w.landedJSON)
			o["meta"] = nil
			w.transactions = func(string) any { return o }
		}, x402.ErrChainInconclusive, "no status"},
		{"no metadata", ":296-302", func(w *svmWorld) {
			o := clone(w.t, w.landedJSON)
			delete(o, "meta")
			w.transactions = func(string) any { return o }
		}, x402.ErrChainInconclusive, "no status"},
		{"metadata without an error field", ":304-310", func(w *svmWorld) {
			o := clone(w.t, w.landedJSON)
			o["meta"] = map[string]any{}
			w.transactions = func(string) any { return o }
		}, x402.ErrChainInconclusive, "no status"},
		{"a history longer than the window cap", ":346-353", func(w *svmWorld) {
			w.history = w.others(5_001, 10, w.bt)
			o := w.otherTransaction()
			w.transactions = func(string) any { return o }
		}, x402.ErrChainInconclusive, "pages"},
		{"later transactions past the page cap", ":364-370", func(w *svmWorld) {
			w.history = w.others(50_000, 10, w.since.Unix()+3_600)
		}, x402.ErrChainInconclusive, "50 pages"},
		{"a listed transaction not returned", ":372-378", func(w *svmWorld) {
			w.transactions = func(string) any { return nil }
		}, x402.ErrChainInconclusive, "getTransaction"},
		{"a transaction that is not an object", ":380-386", func(w *svmWorld) {
			w.transactions = func(string) any { return "oops" }
		}, x402.ErrChainInconclusive, "getTransaction"},
		{"a transaction without its signatures", ":388-394", func(w *svmWorld) {
			o := clone(w.t, w.landedJSON)
			o["transaction"] = map[string]any{}
			w.transactions = func(string) any { return o }
		}, x402.ErrChainInconclusive, "signatures"},
		{"unreachable", ":396-405", func(w *svmWorld) {
			w.override = func(rpcCall) (any, *cdpAnswer, bool) { return nil, &cdpAnswer{drop: true}, true }
		}, x402.ErrChainUnreachable, ""},
		{"a durable nonce", ":407-423", func(w *svmWorld) {
			w.signed = buildSolana(w.t, []string{svmFeePayer, svmBuyer, svmSystem, svmToken},
				[][3][]byte{ins(2, []byte{1, 0, 1}, binary.LittleEndian.AppendUint32(nil, 4)), ins(3, []byte{1, 1, 1, 1}, transferChecked(1000))},
				[][]byte{sig(0), sig(2)})
		}, x402.ErrUnreadablePayment, "durable nonce"},
		{"no TransferChecked", ":425-435", func(w *svmWorld) {
			w.signed = buildSolana(w.t, []string{svmBuyer}, nil, [][]byte{sig(2)})
		}, x402.ErrUnreadablePayment, "TransferChecked"},
		{"a TransferChecked without an authority", ":437-453", func(w *svmWorld) {
			w.signed = buildSolana(w.t, []string{svmBuyer, svmToken}, [][3][]byte{ins(1, []byte{0, 0, 0}, transferChecked(1000))}, [][]byte{sig(2)})
		}, x402.ErrUnreadablePayment, "TransferChecked"},
		{"an authority that does not sign", ":455-471", func(w *svmWorld) {
			w.signed = buildSolana(w.t, []string{svmFeePayer, svmBuyer, svmToken}, [][3][]byte{ins(2, []byte{1, 1, 1, 1}, transferChecked(1000))}, [][]byte{sig(0)})
		}, x402.ErrUnreadablePayment, "signer"},
		{"a transaction that is not base64", ":473-483", func(w *svmWorld) {
			w.payment = x402.Object{"payload": x402.Object{"transaction": "not base64!"}}
		}, x402.ErrUnreadablePayment, ""},
		{"a payload that is not an object", ":485-495", func(w *svmWorld) {
			w.payment = x402.Object{"payload": "abc"}
		}, x402.ErrUnreadablePayment, ""},
	}
	for _, c := range cases {
		t.Run(c.line+" "+c.name, func(t *testing.T) {
			w := newSVMWorld(t)
			c.setup(w)
			w.wantError(c.kind, c.contains)
			if errors.Is(c.kind, x402.ErrUnreadablePayment) && len(w.rpc.callsOf("getGenesisHash")) != 0 {
				t.Error("got an RPC call, want none for an unreadable payment")
			}
		})
	}

	t.Run("getBlockTime answers something other than a time", func(t *testing.T) { // :231-241
		w := newSVMWorld(t)
		w.history = nil
		w.slotTime = func(int64) any { return "soon" }
		_, err := w.reader().Final(context.Background())
		wantChainError(t, err, x402.ErrChainInconclusive, "getBlockTime")
	})

	t.Run("without a since", func(t *testing.T) { // :194-200
		w := newSVMWorld(t)
		w.since = time.Time{}
		if _, err := w.reader().Settled(context.Background()); !errors.Is(err, errNoSince) {
			t.Errorf("got error = %v, want errNoSince", err)
		}
	})
}
