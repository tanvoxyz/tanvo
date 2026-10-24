package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ConnectionRepository stores connections.
type ConnectionRepository interface {
	// Create stores a new connection; a code already held by a live
	// connection of the organization is core.ErrConflict.
	Create(ctx context.Context, c *Connection) error
	// Update stores c over its row.
	Update(ctx context.Context, c *Connection) error
	// Get returns the connection id of org, deleted or not, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.X402ConnectionID) (*Connection, error)
	// GetByCode returns the live connection code of org, or
	// core.ErrNotFound.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Connection, error)
}

// The unique rules of settlements, the rule of a [*ConflictError].
const (
	// RulePaymentDigest is one pending or settled settlement per payment
	// digest, across all organizations.
	RulePaymentDigest = "payment_digest"
	// RulePendingCreditPurchasePayer is one pending credit purchase per
	// payer and organization.
	RulePendingCreditPurchasePayer = "pending_credit_purchase_payer"
	// RulePendingInvoice is one pending settlement per invoice.
	RulePendingInvoice = "pending_invoice"
	// RulePayment is one settlement per payment.
	RulePayment = "payment"
	// RuleTransactionHash is one settlement per transaction hash and
	// network, across all organizations.
	RuleTransactionHash = "transaction_hash"
)

// ConflictError is a settlement write that breaks a unique rule; it is
// core.ErrConflict.
type ConflictError struct {
	// Rule is the broken rule, one of the Rule constants.
	Rule string
}

func (e *ConflictError) Error() string { return "x402 settlement: " + e.Rule + ": conflict" }

// Is makes the error core.ErrConflict.
func (e *ConflictError) Is(target error) bool { return target == core.ErrConflict }

// SettlementRepository stores settlements.
type SettlementRepository interface {
	// Create stores a new settlement; a broken unique rule is a
	// *ConflictError.
	Create(ctx context.Context, s *Settlement) error
	// Update stores the status, the references, the hash, the
	// reconciliation time, the payload and the error reason of s; a
	// broken unique rule is a *ConflictError.
	Update(ctx context.Context, s *Settlement) error
	// Get returns the settlement id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.X402SettlementID) (*Settlement, error)
	// PendingCreditPurchaseExists reports whether payer has a pending
	// credit purchase in org.
	PendingCreditPurchaseExists(ctx context.Context, org core.OrganizationID, payer string) (bool, error)
	// ListPendingReconciliation returns the pending settlements of org
	// whose reconciliation time is at or before now, oldest first.
	ListPendingReconciliation(ctx context.Context, org core.OrganizationID, now time.Time) ([]*Settlement, error)
}

func jsonEqual(a, b Object) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}
