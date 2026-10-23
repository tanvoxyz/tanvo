package kernel

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Lock order.
//
// Every billing transaction takes its advisory locks in one fixed order, so
// that two transactions never wait on each other (domain §6):
//
//  1. the customer's billing_schedule lock;
//  2. subscription locks (several are taken only under 1, which
//     serializes them, or else in ascending ID order);
//  3. event locks (in-advance billing of one event);
//  4. invoice locks;
//  5. credit note row locks (refunds);
//  6. the customer's coupons lock;
//  7. the customer's credit_notes lock;
//  8. the customer's prepaid_credits lock.
//
// Store row locks taken by sequences (document numbers) come last. A
// transaction that will need several of the customer balance locks (6–8)
// takes them all up front with [LockCustomerBalances] before any of them,
// since the deductions of invoice generation take them in that order:
// termination (lifecycle/terminate.go) and void (invoicing/void.go) do
// this; regeneration and finalization take only the locks their
// deductions take, in that order.
//
// A transaction may take a lock of a rank lower than one it holds only if
// it already holds that lock (locks are re-entrant). The test harness
// checks this order, subscription IDs included, on every transaction
// (billingtest.Tx).

// LockRank returns the rank of k in the lock order, or 0 for a lock
// outside it (sequences).
func LockRank(k port.LockKey) int {
	switch k.Scope {
	case customerScope(customer.LockScopeBillingSchedule):
		return 1
	case "subscription":
		return 2
	case "event":
		return 3
	case "invoice":
		return 4
	case "credit_note":
		return 5
	case customerScope(customer.LockScopeCoupons):
		return 6
	case customerScope(customer.LockScopeCreditNotes):
		return 7
	case customerScope(customer.LockScopePrepaidCredits):
		return 8
	}
	return 0
}

// customerScope is the LockKey scope of a customer lock.
func customerScope(s customer.LockScope) string { return port.CustomerLock(s, core.CustomerID{}).Scope }

// LockCustomerBalances takes the customer's coupons, credit_notes and
// prepaid_credits locks, in that order: a transaction that changes several
// of the customer's balances takes them before any.
func LockCustomerBalances(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) error {
	for _, s := range []customer.LockScope{customer.LockScopeCoupons, customer.LockScopeCreditNotes, customer.LockScopePrepaidCredits} {
		if err := LockCustomer(ctx, r, org, c, s); err != nil {
			return err
		}
	}
	return nil
}

// LockCreditNote takes the row lock of credit note id: a refund records
// its outcome under it.
func LockCreditNote(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CreditNoteID) error {
	if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: "credit_note", ID: id.UUID()}); err != nil {
		return fmt.Errorf("lock credit note %s: %w", id, err)
	}
	return nil
}
