package x402

import (
	"math/big"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// SettlementKind is what a settlement pays for.
type SettlementKind string

// SettlementKind values.
const (
	// SettlementKindCreditPurchase is "credit_purchase": wallet credits.
	SettlementKindCreditPurchase SettlementKind = "credit_purchase"
	// SettlementKindInvoicePayment is "invoice_payment": an invoice.
	SettlementKindInvoicePayment SettlementKind = "invoice_payment"
)

// Valid reports whether k is a declared kind.
func (k SettlementKind) Valid() bool {
	return k == SettlementKindCreditPurchase || k == SettlementKindInvoicePayment
}

// String returns the stored form of k.
func (k SettlementKind) String() string { return string(k) }

// SettlementStatus is where a settlement stands.
type SettlementStatus string

// SettlementStatus values.
const (
	// SettlementStatusPending is "pending": submitted, not known settled.
	SettlementStatusPending SettlementStatus = "pending"
	// SettlementStatusSettled is "settled": the transfer landed.
	SettlementStatusSettled SettlementStatus = "settled"
	// SettlementStatusFailed is "failed": the transfer will not land.
	SettlementStatusFailed SettlementStatus = "failed"
)

// Valid reports whether s is a declared status.
func (s SettlementStatus) Valid() bool {
	return s == SettlementStatusPending || s == SettlementStatusSettled || s == SettlementStatusFailed
}

// String returns the stored form of s.
func (s SettlementStatus) String() string { return string(s) }

// SettledBy is who settled the transfer.
type SettledBy string

// SettledBy values.
const (
	// SettledByTanvo is "tanvo": through the connection's facilitator.
	SettledByTanvo SettledBy = "tanvo"
	// SettledByMerchant is "merchant": by the organization itself.
	SettledByMerchant SettledBy = "merchant"
)

// Valid reports whether s is a declared value.
func (s SettledBy) Valid() bool { return s == SettledByTanvo || s == SettledByMerchant }

// String returns the stored form of s.
func (s SettledBy) String() string { return string(s) }

// Settlement is one attempt to settle a verified payment: the verified
// facts (payer, payee, amount, digest) and where the transfer stands
// (parity §1.36).
//
// source: app/models/x402/settlement.rb
type Settlement struct {
	// ID identifies the settlement.
	ID core.X402SettlementID
	// OrganizationID owns the settlement.
	OrganizationID core.OrganizationID
	// ConnectionID is the connection paid through (possibly deleted
	// since).
	ConnectionID core.X402ConnectionID
	// CustomerID, SubscriptionID, WalletTransactionID and PaymentID are
	// what the settlement granted or recorded, when it did.
	CustomerID          core.CustomerID
	SubscriptionID      core.SubscriptionID
	WalletTransactionID core.WalletTransactionID
	PaymentID           core.PaymentID
	// InvoiceID is the invoice an invoice payment pays.
	InvoiceID core.InvoiceID
	// Kind is what the settlement pays for.
	Kind SettlementKind
	// Status is where it stands.
	Status SettlementStatus
	// SettledBy is who settles.
	SettledBy SettledBy
	// Network is the network of the transfer.
	Network Network
	// Asset is the token address.
	Asset string
	// PayerAddress and PayeeAddress are the transfer's ends.
	PayerAddress, PayeeAddress string
	// SettledAmountAtomic is the amount in atomic units, positive.
	SettledAmountAtomic *big.Int
	// SettledAmountCents is the amount in cents, floored.
	SettledAmountCents int64
	// TransactionHash identifies the landed transfer; a settled
	// settlement has one. Unique per network across all organizations.
	TransactionHash *string
	// ReconcileAfter is when a pending settlement may be reconciled
	// on chain; a pending settlement has one.
	ReconcileAfter *time.Time
	// PaymentDigest identifies the payment (see [ComputeDigest]); unique
	// across all organizations among pending and settled settlements.
	PaymentDigest string
	// PurchaseSettings are the wallet top-up settings of a credit
	// purchase.
	PurchaseSettings Object
	// Payload keeps the payment, its requirements and the facilitator's
	// answers.
	Payload Object
	// ErrorReason is why the last facilitator call did not settle.
	ErrorReason *string
	// CreatedAt and UpdatedAt are the row timestamps.
	CreatedAt, UpdatedAt time.Time
}

// Family returns the family of the settlement's network; "" when the
// network is unsupported.
func (s *Settlement) Family() Family {
	f, _ := s.Network.Family()
	return f
}

// Normalize stores the addresses of an EVM settlement checksummed; it
// runs on creation only.
//
// source: app/models/x402/settlement.rb:44-50
func (s *Settlement) Normalize() {
	if f := s.Family(); f != "" {
		s.PayerAddress = NormalizeAddress(s.PayerAddress, f)
		s.PayeeAddress = NormalizeAddress(s.PayeeAddress, f)
		s.Asset = NormalizeAddress(s.Asset, f)
	}
}

// Validate checks the settlement on its own: declared enumerations; a
// supported network; an asset and a digest; a positive amount; an invoice
// for an invoice payment and settings for a credit purchase; valid
// addresses of the network's family; a hash once settled and a
// reconciliation time while pending; a subscription for a granted wallet
// transaction; and a merchant only on a settled credit purchase.
//
// source: app/models/x402/settlement.rb:24-36,52-58 and the
// check_x402_settlements_* constraints of db/structure.sql:6392-6398
func (s *Settlement) Validate() error {
	var v core.Validator
	v.Enum("kind", s.Kind)
	v.Enum("status", s.Status)
	v.Enum("settled_by", s.SettledBy)
	v.Check(s.Network.Valid(), "network", core.CodeInvalid)
	v.Required("asset", s.Asset)
	v.Required("payment_digest", s.PaymentDigest)
	v.Check(s.SettledAmountAtomic != nil && s.SettledAmountAtomic.Sign() > 0, "settled_amount_atomic", core.CodeNotPositive)
	if s.Kind == SettlementKindInvoicePayment {
		v.Check(!s.InvoiceID.IsZero(), "invoice", core.CodeRequired)
	}
	if s.Kind == SettlementKindCreditPurchase {
		v.Check(len(s.PurchaseSettings) > 0, "purchase_settings", core.CodeRequired)
	}
	if f := s.Family(); f != "" {
		v.Check(ValidAddress(s.PayerAddress, f), "payer_address", core.CodeInvalidFormat)
		v.Check(ValidAddress(s.PayeeAddress, f), "payee_address", core.CodeInvalidFormat)
	}
	if s.Status == SettlementStatusSettled {
		v.Check(s.TransactionHash != nil, "transaction_hash", core.CodeRequired)
	}
	if s.Status == SettlementStatusPending {
		v.Check(s.ReconcileAfter != nil, "reconcile_after", core.CodeRequired)
	}
	if !s.WalletTransactionID.IsZero() {
		v.Check(!s.SubscriptionID.IsZero(), "subscription", core.CodeRequired)
	}
	if s.SettledBy == SettledByMerchant {
		v.Check(s.Kind == SettlementKindCreditPurchase && s.Status == SettlementStatusSettled, "settled_by", core.CodeForbidden)
	}
	return v.Err()
}

// ValidateConnection checks, at creation, that the settlement belongs to
// the connection's organization and matches its terms: a network it
// offers, its payout address of the family as payee, its token as asset.
//
// source: app/models/x402/settlement.rb:60-96
func (s *Settlement) ValidateConnection(c *Connection) error {
	var v core.Validator
	v.Check(c.OrganizationID == s.OrganizationID, "x402_connection", CodeSameOrganization)
	if f := s.Family(); f != "" {
		v.Check(c.Offers(s.Network), "network", CodeNotOfferedByConnection)
		if ValidAddress(s.PayeeAddress, f) {
			v.Check(s.PayeeAddress == c.PayoutAddresses[f], "payee_address", CodeNotConnectionPayoutAddress)
		}
		if s.Asset != "" {
			a, ok := LookupAsset(c.Asset, s.Network)
			v.Check(ok && s.Asset == a.Address, "asset", CodeNotConnectionAsset)
		}
	}
	return v.Err()
}

// SameFacts reports whether next keeps the verified facts of s, which
// never change once recorded: the payer, the payee, the digest, the
// purchase settings and the invoice.
//
// source: app/models/x402/settlement.rb:20
func (s *Settlement) SameFacts(next *Settlement) bool {
	return s.PayerAddress == next.PayerAddress && s.PayeeAddress == next.PayeeAddress &&
		s.PaymentDigest == next.PaymentDigest && s.InvoiceID == next.InvoiceID &&
		jsonEqual(s.PurchaseSettings, next.PurchaseSettings)
}
