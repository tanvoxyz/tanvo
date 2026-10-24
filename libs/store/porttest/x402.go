package porttest

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// x402Payout is the EVM payout address of the fixtures.
const x402Payout = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"

// x402FixtureConnection returns a Base Sepolia connection of org.
//
// source: spec/factories/x402/connections.rb
func x402FixtureConnection(e *env, org core.OrganizationID, code string) *x402.Connection {
	return &x402.Connection{
		ID: core.NewID[core.X402ConnectionID](e.ids), OrganizationID: org, Code: code, Name: "Coinbase CDP",
		Facilitator: x402.FacilitatorCoinbaseCDP, Asset: x402.AssetUSDC,
		Networks:            []x402.Network{x402.NetworkBaseSepolia, x402.NetworkSolanaDevnet},
		PayoutAddresses:     map[x402.Family]string{x402.FamilyEVM: x402Payout, x402.FamilySVM: "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4"},
		AutoCreateCustomers: true, Secrets: []byte("sealed"), CreatedAt: now, UpdatedAt: now,
	}
}

// x402FixtureSettlement returns a settled credit purchase through c; n
// makes its payer, hash and digest unique.
//
// source: spec/factories/x402/settlements.rb
func x402FixtureSettlement(e *env, c *x402.Connection, n int) *x402.Settlement {
	return &x402.Settlement{
		ID: core.NewID[core.X402SettlementID](e.ids), OrganizationID: c.OrganizationID, ConnectionID: c.ID,
		Kind: x402.SettlementKindCreditPurchase, Status: x402.SettlementStatusSettled, SettledBy: x402.SettledByTanvo,
		Network: x402.NetworkBaseSepolia, Asset: "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		PayerAddress: fmt.Sprintf("0x%040x", n), PayeeAddress: x402Payout,
		SettledAmountAtomic: big.NewInt(1_000_000), SettledAmountCents: 100,
		TransactionHash: ptr(fmt.Sprintf("0x%064x", n)), PaymentDigest: fmt.Sprintf("%064x", n),
		PurchaseSettings: x402.Object{"plan_code": "agent_api", "wallet_code": "agent_credits"},
		Payload:          x402.Object{"x402Version": "2", "payload": x402.Object{"amounts": []any{"1", "2"}}},
		CreatedAt:        now, UpdatedAt: now,
	}
}

// x402Cmp compares the atomic amounts by value.
var x402Cmp = cmp.Comparer(func(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Cmp(b) == 0
})

// x402Pending makes s a pending attempt.
func x402Pending(s *x402.Settlement, reconcile time.Time) *x402.Settlement {
	s.Status, s.TransactionHash, s.ReconcileAfter = x402.SettlementStatusPending, nil, &reconcile
	return s
}

// x402Failed makes s a failed attempt.
func x402Failed(s *x402.Settlement) *x402.Settlement {
	s.Status, s.TransactionHash, s.ErrorReason = x402.SettlementStatusFailed, nil, ptr("invalid_exact_evm_payload_signature")
	return s
}

// x402InvoicePayment makes s the payment of inv.
func x402InvoicePayment(s *x402.Settlement, inv core.InvoiceID) *x402.Settlement {
	s.Kind, s.InvoiceID, s.PurchaseSettings = x402.SettlementKindInvoicePayment, inv, nil
	return s
}

// wantX402Conflict fails the test unless err breaks rule.
func wantX402Conflict(t *testing.T, what string, err error, rule string) {
	t.Helper()
	wantErr(t, what, err, core.ErrConflict)
	var c *x402.ConflictError
	if !errors.As(err, &c) || c.Rule != rule {
		t.Errorf("%s rule got = %v, want = %s", what, err, rule)
	}
}

func testX402(t *testing.T, h Harness) {
	t.Run("connections", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		conn := x402FixtureConnection(e, org, "cdp")
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.X402Connections.Create(ctx, conn))
			got, err := r.X402Connections.Get(ctx, org, conn.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, conn, x402Cmp)
			got, err = r.X402Connections.GetByCode(ctx, org, "cdp")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, conn, x402Cmp)
			_, err = r.X402Connections.Get(ctx, b.OrganizationID, conn.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.X402Connections.GetByCode(ctx, org, "other")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			// source: spec/models/x402/connection_spec.rb:206-210
			dup := x402FixtureConnection(e, org, "cdp")
			wantErr(t, "Create() with a taken code", r.X402Connections.Create(ctx, dup), core.ErrConflict)
			// source: spec/models/x402/connection_spec.rb:63-67
			noErr(t, "Create() in another organization", r.X402Connections.Create(ctx, x402FixtureConnection(e, b.OrganizationID, "cdp")))

			conn.Name, conn.Networks = "Renamed", []x402.Network{x402.NetworkBaseSepolia}
			conn.UpdatedAt = at(time.Hour)
			noErr(t, "Update()", r.X402Connections.Update(ctx, conn))
			got, err = r.X402Connections.Get(ctx, org, conn.ID)
			noErr(t, "Get() updated", err)
			equal(t, "Get() updated", got, conn, x402Cmp)

			// source: spec/models/x402/connection_spec.rb:212-216
			conn.DeletedAt = ptr(at(2 * time.Hour))
			noErr(t, "Update() discarded", r.X402Connections.Update(ctx, conn))
			_, err = r.X402Connections.GetByCode(ctx, org, "cdp")
			wantErr(t, "GetByCode() discarded", err, core.ErrNotFound)
			got, err = r.X402Connections.Get(ctx, org, conn.ID)
			noErr(t, "Get() discarded", err)
			equal(t, "Get() discarded", got, conn, x402Cmp)
			noErr(t, "Create() with a freed code", r.X402Connections.Create(ctx, dup))
			return nil
		})
	})

	t.Run("settlements", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		conn := x402FixtureConnection(e, org, "cdp")
		s := x402FixtureSettlement(e, conn, 1)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() connection", r.X402Connections.Create(ctx, conn))
			noErr(t, "Create()", r.X402Settlements.Create(ctx, s))
			got, err := r.X402Settlements.Get(ctx, org, s.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, s, x402Cmp)
			_, err = r.X402Settlements.Get(ctx, b.OrganizationID, s.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)

			// source: spec/models/x402/settlement_spec.rb:240-250
			changed := *s
			changed.Status, changed.ErrorReason = x402.SettlementStatusFailed, ptr("reverted")
			changed.Payload = x402.Object{"reverted": true}
			changed.PayerAddress, changed.PaymentDigest = "0xother", "other"
			changed.UpdatedAt = at(time.Hour)
			noErr(t, "Update()", r.X402Settlements.Update(ctx, &changed))
			got, err = r.X402Settlements.Get(ctx, org, s.ID)
			noErr(t, "Get() updated", err)
			want := *s
			want.Status, want.ErrorReason, want.Payload, want.UpdatedAt = changed.Status, changed.ErrorReason, changed.Payload, changed.UpdatedAt
			equal(t, "Get() updated keeps the verified facts", got, &want, x402Cmp)
			return nil
		})
	})

	// source: spec/models/x402/settlement_spec.rb:53-65
	t.Run("pending reconciliation", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		conn := x402FixtureConnection(e, org, "cdp")
		other := x402FixtureConnection(e, b.OrganizationID, "cdp")
		later := x402Pending(x402FixtureSettlement(e, conn, 1), at(-time.Minute))
		due := x402Pending(x402FixtureSettlement(e, conn, 2), at(-2*time.Minute))
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() connection", r.X402Connections.Create(ctx, conn))
			noErr(t, "Create() other connection", r.X402Connections.Create(ctx, other))
			for _, s := range []*x402.Settlement{
				later, due,
				x402Pending(x402FixtureSettlement(e, conn, 3), at(5*time.Minute)),
				x402FixtureSettlement(e, conn, 4),
				x402Pending(x402FixtureSettlement(e, other, 5), at(-time.Minute)),
			} {
				if s.Status == x402.SettlementStatusSettled {
					s.ReconcileAfter = ptr(at(-time.Minute))
				}
				noErr(t, "Create()", r.X402Settlements.Create(ctx, s))
			}
			got, err := r.X402Settlements.ListPendingReconciliation(ctx, org, now)
			noErr(t, "ListPendingReconciliation()", err)
			equal(t, "ListPendingReconciliation()", got, []*x402.Settlement{due, later}, x402Cmp)

			ok, err := r.X402Settlements.PendingCreditPurchaseExists(ctx, org, due.PayerAddress)
			noErr(t, "PendingCreditPurchaseExists()", err)
			equal(t, "PendingCreditPurchaseExists() pending", ok, true)
			ok, err = r.X402Settlements.PendingCreditPurchaseExists(ctx, org, fmt.Sprintf("0x%040x", 4))
			noErr(t, "PendingCreditPurchaseExists() settled", err)
			equal(t, "PendingCreditPurchaseExists() settled", ok, false)
			ok, err = r.X402Settlements.PendingCreditPurchaseExists(ctx, b.OrganizationID, due.PayerAddress)
			noErr(t, "PendingCreditPurchaseExists() other organization", err)
			equal(t, "PendingCreditPurchaseExists() other organization", ok, false)
			return nil
		})
	})

	// source: spec/models/x402/settlement_spec.rb:312-410
	t.Run("unique rules", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		p := invoiceFixtureParents(e, a)
		conn := x402FixtureConnection(e, org, "cdp")
		other := x402FixtureConnection(e, b.OrganizationID, "cdp")
		invs := [2]core.InvoiceID{}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() connection", r.X402Connections.Create(ctx, conn))
			noErr(t, "Create() other connection", r.X402Connections.Create(ctx, other))
			for i := range invs {
				inv := invoiceFixtureMinimal(e, p, now)
				noErr(t, "Create() invoice", r.Invoices.Create(ctx, inv))
				invs[i] = inv.ID
			}
			return nil
		})
		create := func(s *x402.Settlement) error {
			return e.txErr(func(ctx context.Context, r port.Repos) error { return r.X402Settlements.Create(ctx, s) })
		}
		n := 100
		next := func(c *x402.Connection) *x402.Settlement { n++; return x402FixtureSettlement(e, c, n) }
		soon := at(5 * time.Minute)

		// payment digest
		held := next(conn)
		noErr(t, "Create() settled", create(held))
		dup := x402Pending(next(conn), soon)
		dup.PaymentDigest = held.PaymentDigest
		wantX402Conflict(t, "Create() with a settled digest", create(dup), x402.RulePaymentDigest)
		failed := x402Failed(next(conn))
		noErr(t, "Create() failed", create(failed))
		dup = x402Pending(next(conn), soon)
		dup.PaymentDigest = failed.PaymentDigest
		noErr(t, "Create() with a failed digest", create(dup))
		dup = x402Pending(next(other), soon)
		dup.PaymentDigest = held.PaymentDigest
		wantX402Conflict(t, "Create() with a digest of another organization", create(dup), x402.RulePaymentDigest)

		// pending credit purchase per payer
		const payer = "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359"
		first := x402Pending(next(conn), soon)
		first.PayerAddress = payer
		noErr(t, "Create() pending purchase", create(first))
		dup = x402Pending(next(conn), soon)
		dup.PayerAddress = payer
		wantX402Conflict(t, "Create() a second pending purchase", create(dup), x402.RulePendingCreditPurchasePayer)
		dup = x402InvoicePayment(x402Pending(next(conn), soon), invs[0])
		dup.PayerAddress = payer
		noErr(t, "Create() a pending invoice payment of the payer", create(dup))
		dup = next(conn)
		dup.PayerAddress = payer
		noErr(t, "Create() a settled purchase of the payer", create(dup))

		// pending attempt per invoice
		dup = x402InvoicePayment(x402Pending(next(conn), soon), invs[0])
		wantX402Conflict(t, "Create() a second pending attempt", create(dup), x402.RulePendingInvoice)
		noErr(t, "Create() a failed attempt", create(x402InvoicePayment(x402Failed(next(conn)), invs[0])))

		// payment
		pay := &payment.Payment{
			ID: core.NewID[core.PaymentID](e.ids), OrganizationID: org, Payable: payment.InvoicePayable(invs[1]),
			CustomerID: p.customers[0], Kind: payment.KindX402, Status: payment.StateSucceeded, Amount: 100,
			Currency: invoiceEUR, PaidAt: ptr(now), CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Payments.Create(ctx, pay) })
		paid := x402InvoicePayment(next(conn), invs[1])
		paid.PaymentID = pay.ID
		noErr(t, "Create() paid", create(paid))
		dup = x402InvoicePayment(next(conn), invs[1])
		dup.PaymentID = pay.ID
		wantX402Conflict(t, "Create() with a taken payment", create(dup), x402.RulePayment)

		// transaction hash
		dup = next(conn)
		dup.TransactionHash = held.TransactionHash
		wantX402Conflict(t, "Create() with a taken hash", create(dup), x402.RuleTransactionHash)
		dup.Network = x402.NetworkBase
		noErr(t, "Create() with the hash on another network", create(dup))
		dup = next(other)
		dup.TransactionHash = held.TransactionHash
		wantX402Conflict(t, "Create() with a hash of another organization", create(dup), x402.RuleTransactionHash)

		// An update is held to the same rules.
		moved := x402Pending(next(conn), soon)
		noErr(t, "Create() to update", create(moved))
		moved.Status, moved.TransactionHash, moved.ReconcileAfter = x402.SettlementStatusSettled, held.TransactionHash, nil
		wantX402Conflict(t, "Update() to a taken hash", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.X402Settlements.Update(ctx, moved)
		}), x402.RuleTransactionHash)
	})

	// source: spec/models/x402/settlement_spec.rb:252-310
	if h.Name != "memstore" { // memstore has no check constraints
		t.Run("check constraints", func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, h)
			_, a := e.org("Acme")
			p := invoiceFixtureParents(e, a)
			conn := x402FixtureConnection(e, a.OrganizationID, "cdp")
			inv := invoiceFixtureMinimal(e, p, now)
			e.tx(func(ctx context.Context, r port.Repos) error {
				if err := r.X402Connections.Create(ctx, conn); err != nil {
					return err
				}
				return r.Invoices.Create(ctx, inv)
			})
			n := 0
			for _, c := range []struct {
				name   string
				change func(*x402.Settlement)
				ok     bool
			}{
				{"zero amount", func(s *x402.Settlement) { s.SettledAmountAtomic = big.NewInt(0) }, false},
				{"settled without a hash", func(s *x402.Settlement) { s.TransactionHash = nil }, false},
				{"pending without reconcile_after", func(s *x402.Settlement) { x402Pending(s, now).ReconcileAfter = nil }, false},
				{"invoice payment without an invoice", func(s *x402.Settlement) { x402InvoicePayment(s, core.InvoiceID{}) }, false},
				{"credit purchase without purchase settings", func(s *x402.Settlement) { s.PurchaseSettings = nil }, false},
				{"merchant row still pending", func(s *x402.Settlement) {
					x402Pending(s, now).SettledBy = x402.SettledByMerchant
				}, false},
				{"merchant row on an invoice payment", func(s *x402.Settlement) {
					x402InvoicePayment(s, inv.ID).SettledBy = x402.SettledByMerchant
				}, false},
				{"merchant row that settled a credit purchase", func(s *x402.Settlement) { s.SettledBy = x402.SettledByMerchant }, true},
				{"granted row without a subscription", func(s *x402.Settlement) { s.WalletTransactionID = p.walletTx }, false},
			} {
				n++
				s := x402FixtureSettlement(e, conn, n)
				c.change(s)
				err := e.txErr(func(ctx context.Context, r port.Repos) error { return r.X402Settlements.Create(ctx, s) })
				if c.ok {
					noErr(t, c.name, err)
					continue
				}
				wantErr(t, c.name, err, core.ErrInvalidValue)
			}
		})
	}
}
