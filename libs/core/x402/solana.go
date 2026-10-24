package x402

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrUnreadablePayment reports a stored payment the chain readers cannot
// read: no usable EIP-3009 authorization, or a Solana transaction that
// does not decode, has no TransferChecked, uses a durable nonce or is not
// signed by its transfer's authority.
//
// source: app/services/x402/chain.rb:9
var ErrUnreadablePayment = errors.New("x402: unreadable payment")

// SolanaInstruction is one compiled instruction of a Solana message.
type SolanaInstruction struct {
	// ProgramIDIndex indexes the program in the account keys.
	ProgramIDIndex byte
	// Accounts index the instruction's accounts in the account keys.
	Accounts []byte
	// Data is the instruction data.
	Data []byte
}

// SolanaLookup is an address table lookup of a v0 message.
type SolanaLookup struct {
	// AccountKey is the lookup table.
	AccountKey []byte
	// WritableIndexes and ReadonlyIndexes select its addresses.
	WritableIndexes, ReadonlyIndexes []byte
}

// SolanaTransaction is a decoded legacy or v0 Solana transaction.
//
// source: app/services/x402/chain/solana_transaction.rb
type SolanaTransaction struct {
	// Signatures are the 64-byte signatures, in signer order.
	Signatures [][]byte
	// Version is -1 for a legacy transaction, 0 for v0.
	Version int
	// NumRequiredSignatures is the number of signers.
	NumRequiredSignatures int
	// AccountKeys are the 32-byte static account keys.
	AccountKeys [][]byte
	// RecentBlockhash is the 32-byte blockhash the transaction expires
	// with.
	RecentBlockhash []byte
	// Instructions are the compiled instructions.
	Instructions []SolanaInstruction
	// AddressTableLookups are the lookups of a v0 transaction.
	AddressTableLookups []SolanaLookup
}

var (
	tokenPrograms = [][]byte{
		mustBase58("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA"),
		mustBase58("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb"),
	}
	systemProgram       = make([]byte, 32)
	advanceNonceAccount = binary.LittleEndian.AppendUint32(nil, 4)
)

const (
	transferChecked         = 12
	transferCheckedAccounts = 4
	unsupportedVersion      = "only legacy and v0 Solana transactions are readable"
)

func mustBase58(s string) []byte {
	b, ok := Base58Decode(s)
	if !ok {
		panic("x402: bad base58 constant " + s)
	}
	return b
}

// DecodeSolanaTransaction decodes the wire bytes of a transaction; the
// failure wraps [ErrUnreadablePayment].
//
// source: app/services/x402/chain/solana_transaction.rb:41-66
func DecodeSolanaTransaction(b []byte) (*SolanaTransaction, error) {
	r := &txReader{b: b}
	tx, err := r.transaction()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnreadablePayment, err.Error())
	}
	return tx, nil
}

// TransferChecked returns the first SPL token TransferChecked
// instruction with at least its four accounts, or nil.
//
// source: app/services/x402/chain/solana_transaction.rb:20-25
func (t *SolanaTransaction) TransferChecked() *SolanaInstruction {
	for i := range t.Instructions {
		in := &t.Instructions[i]
		if int(in.ProgramIDIndex) >= len(t.AccountKeys) || !isTokenProgram(t.AccountKeys[in.ProgramIDIndex]) {
			continue
		}
		if len(in.Data) > 0 && in.Data[0] == transferChecked && len(in.Accounts) >= transferCheckedAccounts {
			return in
		}
	}
	return nil
}

func isTokenProgram(k []byte) bool {
	for _, p := range tokenPrograms {
		if bytes.Equal(k, p) {
			return true
		}
	}
	return false
}

// SignatureFor returns the signature of the signer key, or nil when key
// does not sign.
//
// source: app/services/x402/chain/solana_transaction.rb:27-30
func (t *SolanaTransaction) SignatureFor(key []byte) []byte {
	for i, k := range t.AccountKeys {
		if bytes.Equal(k, key) {
			if i < t.NumRequiredSignatures && i < len(t.Signatures) {
				return t.Signatures[i]
			}
			return nil
		}
	}
	return nil
}

// DurableNonce reports whether the first instruction advances a durable
// nonce: such a transaction never expires.
//
// source: app/services/x402/chain/solana_transaction.rb:32-35
func (t *SolanaTransaction) DurableNonce() bool {
	if len(t.Instructions) == 0 {
		return false
	}
	first := t.Instructions[0]
	return int(first.ProgramIDIndex) < len(t.AccountKeys) && bytes.Equal(t.AccountKeys[first.ProgramIDIndex], systemProgram) &&
		bytes.HasPrefix(first.Data, advanceNonceAccount)
}

// SolanaPayment is the transaction of a stored Solana payment, checked
// for what the readers need.
type SolanaPayment struct {
	// Transaction is the decoded transaction.
	Transaction *SolanaTransaction
	// Authority is the TransferChecked authority: the payer.
	Authority []byte
	// BuyerSignature is the authority's signature.
	BuyerSignature []byte
}

// ReadSolanaPayment decodes the base64 transaction of payment and checks
// it expires, carries a TransferChecked and is signed by that transfer's
// authority; the failure wraps [ErrUnreadablePayment].
//
// source: app/services/x402/chain/svm_reader.rb:39-42,170-213
func ReadSolanaPayment(payment Object) (*SolanaPayment, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(str(object(payment["payload"])["transaction"]))
	if err != nil {
		return nil, fmt.Errorf("%w: the stored Solana transaction is not base64", ErrUnreadablePayment)
	}
	tx, err := DecodeSolanaTransaction(raw)
	if err != nil {
		return nil, err
	}
	if tx.DurableNonce() {
		return nil, fmt.Errorf("%w: the stored Solana transaction uses a durable nonce and never expires", ErrUnreadablePayment)
	}
	in := tx.TransferChecked()
	if in == nil {
		return nil, fmt.Errorf("%w: the stored Solana transaction has no TransferChecked", ErrUnreadablePayment)
	}
	var authority []byte
	if idx := int(in.Accounts[3]); idx < len(tx.AccountKeys) {
		authority = tx.AccountKeys[idx]
	}
	var sig []byte
	if authority != nil {
		sig = tx.SignatureFor(authority)
	}
	if sig == nil || bytes.Count(sig, []byte{0}) == len(sig) {
		return nil, fmt.Errorf("%w: the TransferChecked authority is not a signer", ErrUnreadablePayment)
	}
	return &SolanaPayment{Transaction: tx, Authority: authority, BuyerSignature: sig}, nil
}

// Payer is the base58 address of the payment's authority.
func (p *SolanaPayment) Payer() string { return Base58Encode(p.Authority) }

type txReader struct {
	b   []byte
	off int
}

var errTruncated = errors.New("truncated Solana transaction")

func (r *txReader) transaction() (*SolanaTransaction, error) {
	p, err := r.peek()
	if err != nil {
		return nil, err
	}
	if p&0x80 != 0 {
		return nil, errors.New(unsupportedVersion)
	}
	n, err := r.compactU16()
	if err != nil {
		return nil, err
	}
	t := &SolanaTransaction{Version: -1}
	for range n {
		s, err := r.read(64)
		if err != nil {
			return nil, err
		}
		t.Signatures = append(t.Signatures, s)
	}
	if p, err = r.peek(); err != nil {
		return nil, err
	}
	if p&0x80 != 0 {
		v, _ := r.byte()
		t.Version = int(v & 0x7f)
		if t.Version != 0 {
			return nil, errors.New(unsupportedVersion)
		}
	}
	header, err := r.read(3)
	if err != nil {
		return nil, err
	}
	t.NumRequiredSignatures = int(header[0])
	if n, err = r.compactU16(); err != nil {
		return nil, err
	}
	for range n {
		k, err := r.read(32)
		if err != nil {
			return nil, err
		}
		t.AccountKeys = append(t.AccountKeys, k)
	}
	if t.RecentBlockhash, err = r.read(32); err != nil {
		return nil, err
	}
	if n, err = r.compactU16(); err != nil {
		return nil, err
	}
	for range n {
		var in SolanaInstruction
		if in.ProgramIDIndex, err = r.byte(); err != nil {
			return nil, err
		}
		if in.Accounts, err = r.vec(); err != nil {
			return nil, err
		}
		if in.Data, err = r.vec(); err != nil {
			return nil, err
		}
		t.Instructions = append(t.Instructions, in)
	}
	if t.Version == 0 {
		if n, err = r.compactU16(); err != nil {
			return nil, err
		}
		for range n {
			var l SolanaLookup
			if l.AccountKey, err = r.read(32); err != nil {
				return nil, err
			}
			if l.WritableIndexes, err = r.vec(); err != nil {
				return nil, err
			}
			if l.ReadonlyIndexes, err = r.vec(); err != nil {
				return nil, err
			}
			t.AddressTableLookups = append(t.AddressTableLookups, l)
		}
	}
	if r.off != len(r.b) {
		return nil, errors.New("trailing bytes after the Solana transaction")
	}
	return t, nil
}

func (r *txReader) vec() ([]byte, error) {
	n, err := r.compactU16()
	if err != nil {
		return nil, err
	}
	return r.read(n)
}

func (r *txReader) compactU16() (int, error) {
	v := 0
	for shift := range 3 {
		c, err := r.byte()
		if err != nil {
			return 0, err
		}
		v |= int(c&0x7f) << (7 * shift)
		if c&0x80 == 0 {
			return v, nil
		}
	}
	return 0, errors.New("invalid compact-u16 in the Solana transaction")
}

func (r *txReader) read(n int) ([]byte, error) {
	if r.off+n > len(r.b) {
		return nil, errTruncated
	}
	c := r.b[r.off : r.off+n : r.off+n]
	r.off += n
	return c, nil
}

func (r *txReader) byte() (byte, error) {
	c, err := r.read(1)
	if err != nil {
		return 0, err
	}
	return c[0], nil
}

func (r *txReader) peek() (byte, error) {
	if r.off >= len(r.b) {
		return 0, errTruncated
	}
	return r.b[r.off], nil
}
