package x402_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// source: spec/services/x402/chain/solana_transaction_spec.rb

func mustDecodeTx(t *testing.T, b []byte) *x402.SolanaTransaction {
	t.Helper()
	tx, err := x402.DecodeSolanaTransaction(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tx
}

func TestDecodeSolanaTransaction(t *testing.T) {
	t.Parallel()
	tx := mustDecodeTx(t, landedTransaction(t))
	// source: solana_transaction_spec.rb:18
	if got := x402.Base58Encode(tx.Signatures[0]); got != "7scJSNzkdyryaFQcfRX52upVRridoUijgMNaUmLBtJTxB9iCjmEDk2Mk54eEuTYoPqAgdtajPvxDMk9RFimsZ93" {
		t.Errorf("signature: got = %s", got)
	}
	// source: solana_transaction_spec.rb:22
	if tx.Version != 0 || tx.NumRequiredSignatures != 2 || len(tx.AddressTableLookups) != 0 {
		t.Errorf("v0: got = %d, %d, %v", tx.Version, tx.NumRequiredSignatures, tx.AddressTableLookups)
	}
	// source: solana_transaction_spec.rb:26
	if a, b := x402.Base58Encode(tx.AccountKeys[0]), x402.Base58Encode(tx.AccountKeys[1]); a != feePayer || b != svmBuyer {
		t.Errorf("keys: got = %s, %s", a, b)
	}
	// source: solana_transaction_spec.rb:30
	if got := x402.Base58Encode(tx.RecentBlockhash); got != "7LPzenu2Lg6XG5aSrZ6ihu7GFy9gHYEh5KVpVVxrQYue" {
		t.Errorf("blockhash: got = %s", got)
	}

	// source: solana_transaction_spec.rb:34-47
	legacy := mustDecodeTx(t, solanaTx{
		keys:         []string{feePayer, svmBuyer, tokenProgram},
		instructions: []instruction{{2, []byte{1, 1, 1, 1}, transferCheckedData(5)}},
		signatures:   [][]byte{sig(1), sig(2)},
		legacy:       true,
	}.build(t))
	if legacy.Version != -1 || legacy.NumRequiredSignatures != 2 || len(legacy.AddressTableLookups) != 0 {
		t.Errorf("legacy: got = %d, %d", legacy.Version, legacy.NumRequiredSignatures)
	}

	// source: solana_transaction_spec.rb:49-61
	long := mustDecodeTx(t, solanaTx{
		keys:         []string{feePayer, tokenProgram},
		instructions: []instruction{{1, []byte{0}, bytes.Repeat([]byte{9}, 200)}},
		signatures:   [][]byte{sig(1)},
	}.build(t))
	if got := len(long.Instructions[0].Data); got != 200 {
		t.Errorf("two-byte length: got = %d, want = 200", got)
	}

	// source: solana_transaction_spec.rb:63-76
	lk := mustDecodeTx(t, solanaTx{
		keys:       []string{feePayer},
		signatures: [][]byte{sig(1)},
		lookups:    []lookup{{merchantTokenAccount, []byte{3}, []byte{1, 2}}},
	}.build(t))
	if len(lk.AddressTableLookups) != 1 || !bytes.Equal(lk.AddressTableLookups[0].AccountKey, base58(t, merchantTokenAccount)) ||
		!bytes.Equal(lk.AddressTableLookups[0].WritableIndexes, []byte{3}) || !bytes.Equal(lk.AddressTableLookups[0].ReadonlyIndexes, []byte{1, 2}) {
		t.Errorf("lookup: got = %+v", lk.AddressTableLookups)
	}
}

func TestDecodeSolanaTransactionUnreadable(t *testing.T) {
	t.Parallel()
	landed := landedTransaction(t)
	for _, c := range []struct {
		name  string
		bytes []byte
		want  string
	}{
		// source: solana_transaction_spec.rb:78-84
		{"truncated", landed[:300], "truncated"},
		// source: solana_transaction_spec.rb:86-92
		{"long prefix", slices.Concat(compactU16(1), sig(1), []byte{0x80, 0x01, 0x00, 0x00, 0xff, 0xff, 0xff, 0x01}), "compact-u16"},
		// source: solana_transaction_spec.rb:94-100
		{"no message", slices.Concat(compactU16(1), sig(1)), "truncated"},
		// source: solana_transaction_spec.rb:102-108
		{"trailing", append(slices.Clone(landed), 0), "trailing"},
		// source: solana_transaction_spec.rb:110-116
		{"v1 transaction", append([]byte{0x81, 0x01, 0x00, 0x00}, sig(0)...), "legacy and v0"},
		// source: solana_transaction_spec.rb:118-124
		{"v1 message", solanaTx{keys: []string{feePayer}, signatures: [][]byte{sig(1)}, version: 1}.build(t), "legacy and v0"},
	} {
		_, err := x402.DecodeSolanaTransaction(c.bytes)
		if !errors.Is(err, x402.ErrUnreadablePayment) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got = %v, want = unreadable %q", c.name, err, c.want)
		}
	}
}

func TestSolanaTransferChecked(t *testing.T) {
	t.Parallel()
	// source: solana_transaction_spec.rb:128
	if in := mustDecodeTx(t, landedTransaction(t)).TransferChecked(); in == nil || !bytes.Equal(in.Accounts, []byte{2, 4, 3, 1}) {
		t.Errorf("landed: got = %+v, want = accounts [2 4 3 1]", in)
	}
	build := func(program string, accounts []byte) *x402.SolanaTransaction {
		return mustDecodeTx(t, solanaTx{
			keys:         []string{feePayer, svmBuyer, program},
			instructions: []instruction{{2, accounts, transferCheckedData(5)}},
			signatures:   [][]byte{sig(1), sig(2)},
		}.build(t))
	}
	// source: solana_transaction_spec.rb:132-144
	if in := build(token2022Program, []byte{1, 1, 1, 1}).TransferChecked(); in == nil || in.ProgramIDIndex != 2 {
		t.Errorf("token-2022: got = %+v", in)
	}
	// source: solana_transaction_spec.rb:146-156
	if in := build(tokenProgram, []byte{1, 1, 1}).TransferChecked(); in != nil {
		t.Errorf("short of authority: got = %+v, want = nil", in)
	}
	// source: solana_transaction_spec.rb:158-164
	none := mustDecodeTx(t, solanaTx{
		keys:         []string{feePayer, tokenProgram},
		instructions: []instruction{{1, []byte{0}, []byte{3}}},
		signatures:   [][]byte{sig(1)},
	}.build(t))
	if in := none.TransferChecked(); in != nil {
		t.Errorf("no transfer: got = %+v, want = nil", in)
	}
}

func TestSolanaSignatureFor(t *testing.T) {
	t.Parallel()
	tx := mustDecodeTx(t, landedTransaction(t))
	// source: solana_transaction_spec.rb:169
	if got := tx.SignatureFor(base58(t, svmBuyer)); !bytes.Equal(got, tx.Signatures[1]) {
		t.Errorf("signer: got = %x, want = %x", got, tx.Signatures[1])
	}
	// source: solana_transaction_spec.rb:173
	if got := tx.SignatureFor(base58(t, merchantTokenAccount)); got != nil {
		t.Errorf("non-signer: got = %x, want = nil", got)
	}
}

func TestSolanaDurableNonce(t *testing.T) {
	t.Parallel()
	// source: solana_transaction_spec.rb:178
	if mustDecodeTx(t, landedTransaction(t)).DurableNonce() {
		t.Error("landed: got = durable nonce, want = none")
	}
	// source: solana_transaction_spec.rb:180-190
	tx := mustDecodeTx(t, solanaTx{
		keys:         []string{feePayer, svmBuyer, systemProgram},
		instructions: []instruction{{2, []byte{1, 0, 1}, binary.LittleEndian.AppendUint32(nil, 4)}},
		signatures:   [][]byte{sig(1)},
	}.build(t))
	if !tx.DurableNonce() {
		t.Error("advance nonce: got = none, want = durable nonce")
	}
}
