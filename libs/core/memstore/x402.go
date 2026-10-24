package memstore

import (
	"context"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// x402Data holds x402 connections and settlements.
type x402Data struct {
	connections *table[core.X402ConnectionID, x402.Connection]
	settlements *table[core.X402SettlementID, x402.Settlement]
}

func newX402Data() *x402Data {
	return &x402Data{
		connections: newTable[core.X402ConnectionID](func(c x402.Connection) x402.Connection {
			c.Networks = slices.Clone(c.Networks)
			c.PayoutAddresses = maps.Clone(c.PayoutAddresses)
			c.Secrets = slices.Clone(c.Secrets)
			c.DeletedAt = clonePtr(c.DeletedAt)
			return c
		}),
		settlements: newTable[core.X402SettlementID](func(s x402.Settlement) x402.Settlement {
			if s.SettledAmountAtomic != nil {
				s.SettledAmountAtomic = new(big.Int).Set(s.SettledAmountAtomic)
			}
			s.TransactionHash = clonePtr(s.TransactionHash)
			s.ReconcileAfter = clonePtr(s.ReconcileAfter)
			s.ErrorReason = clonePtr(s.ErrorReason)
			s.PurchaseSettings = cloneObject(s.PurchaseSettings)
			s.Payload = cloneObject(s.Payload)
			return s
		}),
	}
}

func (d *x402Data) snapshot() *x402Data {
	return &x402Data{connections: d.connections.snapshot(), settlements: d.settlements.snapshot()}
}

// cloneObject deep-copies a JSON object.
func cloneObject(o x402.Object) x402.Object {
	if o == nil {
		return nil
	}
	c, _ := cloneJSONValue(o).(x402.Object)
	return c
}

func cloneJSONValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(v))
		for k, x := range v {
			c[k] = cloneJSONValue(x)
		}
		return c
	case []any:
		c := make([]any, len(v))
		for i, x := range v {
			c[i] = cloneJSONValue(x)
		}
		return c
	default:
		return v
	}
}

// x402ConnectionRepo implements x402.ConnectionRepository.
type x402ConnectionRepo struct{ d *x402Data }

var _ x402.ConnectionRepository = x402ConnectionRepo{}

func (r x402ConnectionRepo) check(c *x402.Connection) error {
	if c.DeletedAt == nil && r.d.connections.any(func(x *x402.Connection) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.DeletedAt == nil && x.Code == c.Code
	}) {
		return conflict("x402_connection", "code")
	}
	return nil
}

func (r x402ConnectionRepo) Create(_ context.Context, c *x402.Connection) error {
	if r.d.connections.has(c.ID) {
		return conflict("x402_connection", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.connections.put(c.ID, *c)
	return nil
}

func (r x402ConnectionRepo) Update(_ context.Context, c *x402.Connection) error {
	cur, ok := r.d.connections.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("x402_connection", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.connections.put(c.ID, *c)
	return nil
}

func (r x402ConnectionRepo) Get(_ context.Context, org core.OrganizationID, id core.X402ConnectionID) (*x402.Connection, error) {
	c, ok := r.d.connections.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("x402_connection", id)
	}
	return c, nil
}

func (r x402ConnectionRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*x402.Connection, error) {
	c, ok := r.d.connections.first(func(x *x402.Connection) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.Code == code
	})
	if !ok {
		return nil, notFound("x402_connection", stringer(code))
	}
	return c, nil
}

// x402SettlementRepo implements x402.SettlementRepository.
type x402SettlementRepo struct{ d *x402Data }

var _ x402.SettlementRepository = x402SettlementRepo{}

// check enforces the unique rules of the x402_settlements indexes.
func (r x402SettlementRepo) check(s *x402.Settlement) error {
	other := func(pred func(x *x402.Settlement) bool) bool {
		return r.d.settlements.any(func(x *x402.Settlement) bool { return x.ID != s.ID && pred(x) })
	}
	live := func(x *x402.Settlement) bool {
		return x.Status == x402.SettlementStatusPending || x.Status == x402.SettlementStatusSettled
	}
	switch {
	case live(s) && other(func(x *x402.Settlement) bool { return live(x) && x.PaymentDigest == s.PaymentDigest }):
		return &x402.ConflictError{Rule: x402.RulePaymentDigest}
	case s.TransactionHash != nil && other(func(x *x402.Settlement) bool {
		return x.TransactionHash != nil && *x.TransactionHash == *s.TransactionHash && x.Network == s.Network
	}):
		return &x402.ConflictError{Rule: x402.RuleTransactionHash}
	case s.Kind == x402.SettlementKindCreditPurchase && s.Status == x402.SettlementStatusPending && other(func(x *x402.Settlement) bool {
		return x.OrganizationID == s.OrganizationID && x.Kind == x402.SettlementKindCreditPurchase &&
			x.Status == x402.SettlementStatusPending && x.PayerAddress == s.PayerAddress
	}):
		return &x402.ConflictError{Rule: x402.RulePendingCreditPurchasePayer}
	case s.Status == x402.SettlementStatusPending && !s.InvoiceID.IsZero() && other(func(x *x402.Settlement) bool {
		return x.OrganizationID == s.OrganizationID && x.Status == x402.SettlementStatusPending && x.InvoiceID == s.InvoiceID
	}):
		return &x402.ConflictError{Rule: x402.RulePendingInvoice}
	case !s.PaymentID.IsZero() && other(func(x *x402.Settlement) bool {
		return x.OrganizationID == s.OrganizationID && x.PaymentID == s.PaymentID
	}):
		return &x402.ConflictError{Rule: x402.RulePayment}
	}
	return nil
}

func (r x402SettlementRepo) Create(_ context.Context, s *x402.Settlement) error {
	if r.d.settlements.has(s.ID) {
		return conflict("x402_settlement", "id")
	}
	if err := r.check(s); err != nil {
		return err
	}
	r.d.settlements.put(s.ID, *s)
	return nil
}

func (r x402SettlementRepo) Update(_ context.Context, s *x402.Settlement) error {
	cur, ok := r.d.settlements.get(s.ID)
	if !ok || cur.OrganizationID != s.OrganizationID {
		return notFound("x402_settlement", s.ID)
	}
	next := *cur
	next.CustomerID, next.SubscriptionID = s.CustomerID, s.SubscriptionID
	next.WalletTransactionID, next.PaymentID = s.WalletTransactionID, s.PaymentID
	next.Status, next.SettledBy = s.Status, s.SettledBy
	next.TransactionHash, next.ReconcileAfter = s.TransactionHash, s.ReconcileAfter
	next.Payload, next.ErrorReason, next.UpdatedAt = s.Payload, s.ErrorReason, s.UpdatedAt
	if err := r.check(&next); err != nil {
		return err
	}
	r.d.settlements.put(s.ID, next)
	return nil
}

func (r x402SettlementRepo) Get(_ context.Context, org core.OrganizationID, id core.X402SettlementID) (*x402.Settlement, error) {
	s, ok := r.d.settlements.get(id)
	if !ok || s.OrganizationID != org {
		return nil, notFound("x402_settlement", id)
	}
	return s, nil
}

func (r x402SettlementRepo) PendingCreditPurchaseExists(_ context.Context, org core.OrganizationID, payer string) (bool, error) {
	return r.d.settlements.any(func(s *x402.Settlement) bool {
		return s.OrganizationID == org && s.Kind == x402.SettlementKindCreditPurchase &&
			s.Status == x402.SettlementStatusPending && s.PayerAddress == payer
	}), nil
}

func (r x402SettlementRepo) ListPendingReconciliation(_ context.Context, org core.OrganizationID, now time.Time) ([]*x402.Settlement, error) {
	rows := r.d.settlements.find(func(s *x402.Settlement) bool {
		return s.OrganizationID == org && s.Status == x402.SettlementStatusPending && s.ReconcileAfter != nil && !s.ReconcileAfter.After(now)
	})
	slices.SortStableFunc(rows, func(a, b *x402.Settlement) int {
		if c := a.ReconcileAfter.Compare(*b.ReconcileAfter); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID)
	})
	return rows, nil
}
