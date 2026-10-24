package x402pay_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// The Solana transaction builder: v0 messages, a 0x07 blockhash.
//
// source: spec/support/x402/solana_transaction_builder.rb
const (
	tokenProgram  = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"
	systemProgram = "11111111111111111111111111111111"
	feePayerKey   = "D6ZhtNQ5nT9ZnTHUbqXZsTx5MH2rPFiBBggX4hY1WePM"
)

type instruction struct {
	program  byte
	accounts []byte
	data     []byte
}

func buildSolana(t *testing.T, keys []string, instructions []instruction, signatures [][]byte) []byte {
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
		m.WriteByte(in.program)
		m.Write(compactU16(len(in.accounts)))
		m.Write(in.accounts)
		m.Write(compactU16(len(in.data)))
		m.Write(in.data)
	}
	m.Write(compactU16(0))
	out := compactU16(len(signatures))
	for _, s := range signatures {
		out = append(out, s...)
	}
	return append(out, m.Bytes()...)
}

func transferCheckedData(amount uint64) []byte {
	return append(binary.LittleEndian.AppendUint64([]byte{12}, amount), 6)
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
