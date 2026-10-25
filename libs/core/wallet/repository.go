package wallet

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List]; zero fields match everything.
type Filter struct {
	// CustomerID keeps the wallets of one customer.
	CustomerID core.CustomerID
	// Status keeps the wallets in one status.
	Status Status
	// Recent lists newest first, then by ID, the order of the API;
	// otherwise the list is by ID, oldest first.
	Recent bool
}

// Repository stores wallets.
type Repository interface {
	// Create inserts w; it fails with [core.ErrConflict] when an active
	// wallet of the customer already has w's code.
	Create(ctx context.Context, w *Wallet) error
	// Update replaces w; it fails with [core.ErrNotFound] when w does not
	// exist in its organization and with [core.ErrConflict] on a code
	// collision among the customer's active wallets.
	Update(ctx context.Context, w *Wallet) error
	// Get returns the wallet id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.WalletID) (*Wallet, error)
	// GetByCode returns the active wallet of the customer with code, or
	// [core.ErrNotFound].
	GetByCode(ctx context.Context, org core.OrganizationID, customer core.CustomerID, code string) (*Wallet, error)
	// List returns a page of the wallets of org matching f, oldest first.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Wallet], error)
	// ListActiveForApplication returns the customer's active wallets in the
	// order invoices consume them ([CompareForApplication]).
	ListActiveForApplication(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*Wallet, error)
	// CountActive returns the number of active wallets of the customer.
	CountActive(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (int, error)
	// ListExpired returns the active wallets of org whose expiration is at or
	// before now, to be terminated.
	ListExpired(ctx context.Context, org core.OrganizationID, now time.Time) ([]*Wallet, error)
}

// TransactionFilter narrows [TransactionRepository.List]; zero fields match
// everything.
type TransactionFilter struct {
	// WalletID keeps the transactions of one wallet.
	WalletID core.WalletID
	// InvoiceID keeps the transactions of one invoice.
	InvoiceID core.InvoiceID
	// TransactionType keeps one direction.
	TransactionType TransactionType
	// TransactionStatus keeps one kind of movement.
	TransactionStatus TransactionStatus
	// Status keeps one settlement status.
	Status SettlementStatus
	// Recent lists newest first, then by ID, the order of the API;
	// otherwise the list is by ID, oldest first.
	Recent bool
}

// TransactionRepository stores wallet transactions and their consumption
// records.
type TransactionRepository interface {
	// Create inserts t.
	Create(ctx context.Context, t *Transaction) error
	// Update replaces t; it fails with [core.ErrNotFound] when t does not
	// exist in its organization.
	Update(ctx context.Context, t *Transaction) error
	// Get returns the transaction id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*Transaction, error)
	// List returns a page of the transactions of org matching f, oldest
	// first.
	List(ctx context.Context, org core.OrganizationID, f TransactionFilter, page core.PageRequest) (core.Page[*Transaction], error)
	// ListByInvoice returns the transactions of an invoice (its purchased
	// credits or the credits it consumed), oldest first.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Transaction, error)
	// GetPendingPurchase returns the pending purchased transaction created
	// by rule, or [core.ErrNotFound] when there is none.
	GetPendingPurchase(ctx context.Context, org core.OrganizationID, rule core.RecurringTransactionRuleID) (*Transaction, error)
	// ListAvailableInbound returns the settled inbound transactions of the
	// wallet with value left, in consumption order
	// ([CompareForConsumption]).
	ListAvailableInbound(ctx context.Context, org core.OrganizationID, wallet core.WalletID) ([]*Transaction, error)
	// CreateConsumption inserts c; it fails with [core.ErrConflict] when the
	// pair of transactions is already linked.
	CreateConsumption(ctx context.Context, c *Consumption) error
	// ListConsumptionsByOutbound returns the consumptions of an outbound
	// transaction, oldest first.
	ListConsumptionsByOutbound(ctx context.Context, org core.OrganizationID, outbound core.WalletTransactionID) ([]*Consumption, error)
	// ListConsumptionsByInbound returns the consumptions of an inbound
	// transaction, oldest first.
	ListConsumptionsByInbound(ctx context.Context, org core.OrganizationID, inbound core.WalletTransactionID) ([]*Consumption, error)
}

// RecurringTransactionRuleRepository stores recurring transaction rules.
type RecurringTransactionRuleRepository interface {
	// Create inserts r.
	Create(ctx context.Context, r *RecurringTransactionRule) error
	// Update replaces r; it fails with [core.ErrNotFound] when r does not
	// exist in its organization.
	Update(ctx context.Context, r *RecurringTransactionRule) error
	// Get returns the rule id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) (*RecurringTransactionRule, error)
	// Delete removes the rule id of org, or fails with [core.ErrNotFound].
	Delete(ctx context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) error
	// ListByWallet returns the rules of a wallet, oldest first.
	ListByWallet(ctx context.Context, org core.OrganizationID, wallet core.WalletID) ([]*RecurringTransactionRule, error)
	// ListActiveInterval returns the interval rules of org currently active
	// at now, oldest first; the caller decides which are due.
	ListActiveInterval(ctx context.Context, org core.OrganizationID, now time.Time) ([]*RecurringTransactionRule, error)
	// ListActiveThreshold returns the threshold rules of the wallet
	// currently active at now, oldest first.
	ListActiveThreshold(ctx context.Context, org core.OrganizationID, wallet core.WalletID, now time.Time) ([]*RecurringTransactionRule, error)
	// ListEligibleForTermination returns the active rules of org whose
	// expiration is at or before now.
	ListEligibleForTermination(ctx context.Context, org core.OrganizationID, now time.Time) ([]*RecurringTransactionRule, error)
}
