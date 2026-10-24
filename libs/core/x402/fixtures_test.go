package x402_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The shared x402 payment fixtures.
//
// source: spec/support/shared_context/x402_payment.rb

const (
	feePayer             = "D6ZhtNQ5nT9ZnTHUbqXZsTx5MH2rPFiBBggX4hY1WePM"
	svmBuyer             = "BprZ3eTVMHAcqC2wcE4XY71tvjdxJ6C6pSYjVmD75ujf"
	merchantTokenAccount = "H6KPkFCgvBzk6uCcD3VmQKZdp2BTkcoCctcgziSLeSpL"
	tokenProgram         = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	token2022Program     = "TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"
	systemProgram        = "11111111111111111111111111111111"
)

// decode decodes JSON the way stored payments are decoded: numbers stay
// exact.
func decode(t *testing.T, s string) x402.Object {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var o x402.Object
	if err := d.Decode(&o); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return o
}

func evmRequirements(t *testing.T) x402.Object {
	return decode(t, `{"scheme":"exact","network":"eip155:84532","amount":"1000",
		"asset":"0x036CbD53842c5426634e7929541eC2318f3dCF7e","payTo":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
		"maxTimeoutSeconds":60,"extra":{"name":"USDC","version":"2"}}`)
}

func evmPayment(t *testing.T) x402.Object {
	p := decode(t, `{"x402Version":2,
		"resource":{"url":"https://api.example.com/report","description":"Report","mimeType":"application/json"},
		"payload":{"signature":"0x`+strings.Repeat("ab", 65)+`","authorization":{
			"from":"0xf4a43B9cc729c9E4E139CB86808f48e3eD09Dcb2","to":"0x94bA479439C2f1bA5f5DaCBD06Ea0c129604B4a5",
			"value":"1000","validAfter":"1789648671","validBefore":"1789649331",
			"nonce":"0x8568d530303a96028f64623cfc5c7bbb166c4aa2889b25bbe09e1d699193f174"}},
		"extensions":{}}`)
	p["accepted"] = evmRequirements(t)
	return p
}

func svmRequirements(t *testing.T) x402.Object {
	return decode(t, `{"scheme":"exact","network":"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1","amount":"1000",
		"asset":"4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU","payTo":"HHU1aLQQCbCzW9ebjFTntq2vkvsQsxkDyPjMsW2WtiLG",
		"maxTimeoutSeconds":60,"extra":{"feePayer":"GVJJ7rdGiXr5xaYbRwRbjfaJL7fmwRygFi1H6aGqDveb"}}`)
}

// landedTransaction is the devnet transfer of the fixture, as signed by
// both signers.
func landedTransaction(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/solana_devnet_transfer.json")
	if err != nil {
		t.Fatal(err)
	}
	var landed struct{ Transaction []string }
	if err := json.Unmarshal(raw, &landed); err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(landed.Transaction[0])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// svmPayment is the landed transfer before the fee payer signed it.
func svmPayment(t *testing.T) x402.Object {
	b := landedTransaction(t)
	copy(b[1:65], make([]byte, 64))
	return x402.Object{
		"x402Version": json.Number("2"),
		"accepted":    svmRequirements(t),
		"payload":     x402.Object{"transaction": base64.StdEncoding.EncodeToString(b)},
	}
}

// The Solana transaction builder.
//
// source: spec/support/x402/solana_transaction_builder.rb

type instruction struct {
	program  byte
	accounts []byte
	data     []byte
}

type lookup struct {
	table              string
	writable, readonly []byte
}

type solanaTx struct {
	keys         []string
	instructions []instruction
	signatures   [][]byte
	required     int // the required signatures; 0 is one per signature
	legacy       bool
	version      byte
	lookups      []lookup
}

func base58(t *testing.T, s string) []byte {
	t.Helper()
	b, ok := x402.Base58Decode(s)
	if !ok {
		t.Fatalf("base58 %q", s)
	}
	return b
}

func (s solanaTx) build(t *testing.T) []byte {
	t.Helper()
	var m bytes.Buffer
	if !s.legacy {
		m.WriteByte(0x80 | s.version)
	}
	required := s.required
	if required == 0 {
		required = len(s.signatures)
	}
	m.Write([]byte{byte(required), 0, 0})
	m.Write(compactU16(len(s.keys)))
	for _, k := range s.keys {
		m.Write(base58(t, k))
	}
	m.Write(bytes.Repeat([]byte{7}, 32))
	m.Write(compactU16(len(s.instructions)))
	for _, in := range s.instructions {
		m.WriteByte(in.program)
		m.Write(compactU16(len(in.accounts)))
		m.Write(in.accounts)
		m.Write(compactU16(len(in.data)))
		m.Write(in.data)
	}
	if !s.legacy {
		m.Write(compactU16(len(s.lookups)))
		for _, l := range s.lookups {
			m.Write(base58(t, l.table))
			m.Write(compactU16(len(l.writable)))
			m.Write(l.writable)
			m.Write(compactU16(len(l.readonly)))
			m.Write(l.readonly)
		}
	}
	out := compactU16(len(s.signatures))
	for _, sig := range s.signatures {
		out = append(out, sig...)
	}
	return append(out, m.Bytes()...)
}

func transferCheckedData(amount uint64) []byte {
	b := []byte{12}
	b = binary.LittleEndian.AppendUint64(b, amount)
	return append(b, 6)
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

func sig(b byte) []byte { return bytes.Repeat([]byte{b}, 64) }
