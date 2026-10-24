package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindX402 binds the repositories of the x402 aggregates.
func (t *txState) bindX402(r *port.Repos) {
	r.X402Connections = x402ConnectionRepo{t}
	r.X402Settlements = x402SettlementRepo{t}
}

// x402ConnectionRepo implements x402.ConnectionRepository.
type x402ConnectionRepo struct{ t *txState }

var _ x402.ConnectionRepository = x402ConnectionRepo{}

func x402ConnectionParams(c *x402.Connection) (db.CreateX402ConnectionParams, error) {
	networks := make([]string, len(c.Networks))
	for i, n := range c.Networks {
		networks[i] = string(n)
	}
	payouts := c.PayoutAddresses
	if payouts == nil {
		payouts = map[x402.Family]string{}
	}
	pa, err := encodeJSON(payouts)
	if err != nil {
		return db.CreateX402ConnectionParams{}, fmt.Errorf("x402 connection %s: payout addresses: %w", c.ID, err)
	}
	return db.CreateX402ConnectionParams{
		OrganizationID:      c.OrganizationID.UUID(),
		ID:                  c.ID.UUID(),
		Code:                c.Code,
		Name:                c.Name,
		Facilitator:         string(c.Facilitator),
		Asset:               string(c.Asset),
		Networks:            networks,
		PayoutAddresses:     pa,
		AutoCreateCustomers: c.AutoCreateCustomers,
		Secrets:             c.Secrets,
		DeletedAt:           optUTC(c.DeletedAt),
		CreatedAt:           utc(c.CreatedAt),
		UpdatedAt:           utc(c.UpdatedAt),
	}, nil
}

func x402ConnectionOf(r db.X402Connection) (*x402.Connection, error) {
	var payouts map[x402.Family]string
	if err := decodeJSON(r.PayoutAddresses, &payouts); err != nil {
		return nil, fmt.Errorf("x402 connection %s: payout addresses: %w", r.ID, err)
	}
	networks := make([]x402.Network, len(r.Networks))
	for i, n := range r.Networks {
		networks[i] = x402.Network(n)
	}
	return &x402.Connection{
		ID:                  idOf[core.X402ConnectionID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		Code:                r.Code,
		Name:                r.Name,
		Facilitator:         x402.FacilitatorKind(r.Facilitator),
		Asset:               x402.AssetCode(r.Asset),
		Networks:            networks,
		PayoutAddresses:     payouts,
		AutoCreateCustomers: r.AutoCreateCustomers,
		Secrets:             r.Secrets,
		DeletedAt:           optUTC(r.DeletedAt),
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

func (r x402ConnectionRepo) Create(ctx context.Context, c *x402.Connection) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p, err := x402ConnectionParams(c)
	if err != nil {
		return err
	}
	n, err := q.CreateX402Connection(ctx, p)
	return insertErr("x402 connection", n, err)
}

func (r x402ConnectionRepo) Update(ctx context.Context, c *x402.Connection) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p, err := x402ConnectionParams(c)
	if err != nil {
		return err
	}
	// The code is unique among live connections: guard the update so that
	// a conflict keeps the transaction usable.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateX402Connection(ctx, db.UpdateX402ConnectionParams(p))
		return err
	})
	return updateErr("x402 connection", c.ID, n, err)
}

func (r x402ConnectionRepo) Get(ctx context.Context, org core.OrganizationID, id core.X402ConnectionID) (*x402.Connection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetX402Connection(ctx, db.GetX402ConnectionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("x402 connection", id, err)
	}
	return x402ConnectionOf(row)
}

func (r x402ConnectionRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*x402.Connection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetX402ConnectionByCode(ctx, db.GetX402ConnectionByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("x402 connection", code, err)
	}
	return x402ConnectionOf(row)
}

// x402SettlementRepo implements x402.SettlementRepository.
type x402SettlementRepo struct{ t *txState }

var _ x402.SettlementRepository = x402SettlementRepo{}

// x402SettlementRules maps the unique indexes of x402_settlements to the
// rules of [x402.ConflictError].
var x402SettlementRules = map[string]string{
	"x402_settlements_payment_digest_key":                x402.RulePaymentDigest,
	"x402_settlements_pending_credit_purchase_payer_key": x402.RulePendingCreditPurchasePayer,
	"x402_settlements_pending_invoice_key":               x402.RulePendingInvoice,
	"x402_settlements_payment_key":                       x402.RulePayment,
	"x402_settlements_transaction_hash_key":              x402.RuleTransactionHash,
}

// x402SettlementErr turns the violation of a unique rule into an
// [x402.ConflictError].
func x402SettlementErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		if rule, ok := x402SettlementRules[pgErr.ConstraintName]; ok {
			return fmt.Errorf("%w: %w", &x402.ConflictError{Rule: rule}, mapErr(err))
		}
	}
	return mapErr(err)
}

func x402SettlementParams(s *x402.Settlement) (db.CreateX402SettlementParams, error) {
	if s.SettledAmountAtomic == nil {
		return db.CreateX402SettlementParams{}, fmt.Errorf("x402 settlement %s: no amount: %w", s.ID, core.ErrInvalidValue)
	}
	amount, err := money.Parse(s.SettledAmountAtomic.String())
	if err != nil {
		return db.CreateX402SettlementParams{}, fmt.Errorf("x402 settlement %s: amount: %w", s.ID, err)
	}
	var settings []byte
	if s.PurchaseSettings != nil {
		if settings, err = encodeJSON(s.PurchaseSettings); err != nil {
			return db.CreateX402SettlementParams{}, fmt.Errorf("x402 settlement %s: purchase settings: %w", s.ID, err)
		}
	}
	payload := s.Payload
	if payload == nil {
		payload = x402.Object{}
	}
	pl, err := encodeJSON(payload)
	if err != nil {
		return db.CreateX402SettlementParams{}, fmt.Errorf("x402 settlement %s: payload: %w", s.ID, err)
	}
	return db.CreateX402SettlementParams{
		OrganizationID:      s.OrganizationID.UUID(),
		ID:                  s.ID.UUID(),
		X402ConnectionID:    s.ConnectionID.UUID(),
		CustomerID:          optUUID(s.CustomerID),
		SubscriptionID:      optUUID(s.SubscriptionID),
		WalletTransactionID: optUUID(s.WalletTransactionID),
		PaymentID:           optUUID(s.PaymentID),
		InvoiceID:           optUUID(s.InvoiceID),
		Kind:                string(s.Kind),
		Status:              string(s.Status),
		SettledBy:           string(s.SettledBy),
		Network:             string(s.Network),
		Asset:               s.Asset,
		PayerAddress:        s.PayerAddress,
		PayeeAddress:        s.PayeeAddress,
		SettledAmountAtomic: amount,
		SettledAmountCents:  s.SettledAmountCents,
		TransactionHash:     s.TransactionHash,
		ReconcileAfter:      optUTC(s.ReconcileAfter),
		PaymentDigest:       s.PaymentDigest,
		PurchaseSettings:    settings,
		Payload:             pl,
		ErrorReason:         s.ErrorReason,
		CreatedAt:           utc(s.CreatedAt),
		UpdatedAt:           utc(s.UpdatedAt),
	}, nil
}

// decodeObject decodes a JSON object keeping its numbers exact; an empty
// or null column is nil.
func decodeObject(b []byte) (x402.Object, error) {
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var o x402.Object
	if err := d.Decode(&o); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return o, nil
}

func x402SettlementOf(r db.X402Settlement) (*x402.Settlement, error) {
	amount, ok := new(big.Int).SetString(strings.TrimSpace(r.SettledAmountAtomic.StringFixed(0)), 10)
	if !ok {
		return nil, fmt.Errorf("x402 settlement %s: amount %s: %w", r.ID, r.SettledAmountAtomic, core.ErrInvalidValue)
	}
	settings, err := decodeObject(r.PurchaseSettings)
	if err != nil {
		return nil, fmt.Errorf("x402 settlement %s: purchase settings: %w", r.ID, err)
	}
	payload, err := decodeObject(r.Payload)
	if err != nil {
		return nil, fmt.Errorf("x402 settlement %s: payload: %w", r.ID, err)
	}
	return &x402.Settlement{
		ID:                  idOf[core.X402SettlementID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		ConnectionID:        idOf[core.X402ConnectionID](r.X402ConnectionID),
		CustomerID:          optIDOf[core.CustomerID](r.CustomerID),
		SubscriptionID:      optIDOf[core.SubscriptionID](r.SubscriptionID),
		WalletTransactionID: optIDOf[core.WalletTransactionID](r.WalletTransactionID),
		PaymentID:           optIDOf[core.PaymentID](r.PaymentID),
		InvoiceID:           optIDOf[core.InvoiceID](r.InvoiceID),
		Kind:                x402.SettlementKind(r.Kind),
		Status:              x402.SettlementStatus(r.Status),
		SettledBy:           x402.SettledBy(r.SettledBy),
		Network:             x402.Network(r.Network),
		Asset:               r.Asset,
		PayerAddress:        r.PayerAddress,
		PayeeAddress:        r.PayeeAddress,
		SettledAmountAtomic: amount,
		SettledAmountCents:  r.SettledAmountCents,
		TransactionHash:     r.TransactionHash,
		ReconcileAfter:      optUTC(r.ReconcileAfter),
		PaymentDigest:       r.PaymentDigest,
		PurchaseSettings:    settings,
		Payload:             payload,
		ErrorReason:         r.ErrorReason,
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

func (r x402SettlementRepo) Create(ctx context.Context, s *x402.Settlement) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	p, err := x402SettlementParams(s)
	if err != nil {
		return err
	}
	// The unique rules name the conflict: insert under a savepoint rather
	// than with ON CONFLICT DO NOTHING.
	err = r.t.guard(ctx, func() error { return q.CreateX402Settlement(ctx, p) })
	if err != nil {
		return fmt.Errorf("create x402 settlement: %w", x402SettlementErr(err))
	}
	return nil
}

func (r x402SettlementRepo) Update(ctx context.Context, s *x402.Settlement) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	p, err := x402SettlementParams(s)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateX402Settlement(ctx, db.UpdateX402SettlementParams{
			OrganizationID: p.OrganizationID, ID: p.ID, CustomerID: p.CustomerID, SubscriptionID: p.SubscriptionID,
			WalletTransactionID: p.WalletTransactionID, PaymentID: p.PaymentID, Status: p.Status, SettledBy: p.SettledBy,
			TransactionHash: p.TransactionHash, ReconcileAfter: p.ReconcileAfter, Payload: p.Payload,
			ErrorReason: p.ErrorReason, UpdatedAt: p.UpdatedAt,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("x402 settlement %s: %w", s.ID, x402SettlementErr(err))
	}
	return updateErr("x402 settlement", s.ID, n, nil)
}

func (r x402SettlementRepo) Get(ctx context.Context, org core.OrganizationID, id core.X402SettlementID) (*x402.Settlement, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetX402Settlement(ctx, db.GetX402SettlementParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("x402 settlement", id, err)
	}
	return x402SettlementOf(row)
}

func (r x402SettlementRepo) PendingCreditPurchaseExists(ctx context.Context, org core.OrganizationID, payer string) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	ok, err := q.X402PendingCreditPurchaseExists(ctx, db.X402PendingCreditPurchaseExistsParams{OrganizationID: org.UUID(), PayerAddress: payer})
	if err != nil {
		return false, fmt.Errorf("x402 pending credit purchase: %w", mapErr(err))
	}
	return ok, nil
}

func (r x402SettlementRepo) ListPendingReconciliation(ctx context.Context, org core.OrganizationID, now time.Time) ([]*x402.Settlement, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListX402PendingReconciliation(ctx, db.ListX402PendingReconciliationParams{OrganizationID: org.UUID(), Now: new(utc(now))})
	if err != nil {
		return nil, fmt.Errorf("list x402 pending reconciliation: %w", mapErr(err))
	}
	out := make([]*x402.Settlement, 0, len(rows))
	for _, row := range rows {
		s, err := x402SettlementOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
