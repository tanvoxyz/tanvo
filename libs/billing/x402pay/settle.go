package x402pay

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// invoiceCurrency is the only currency an invoice payment settles.
const invoiceCurrency = "USD"

// raceCodes are the refusals of a settlement that a concurrent one
// already holds, by broken unique rule.
//
// source: app/services/x402/settlements/settle_service.rb:10-14
var raceCodes = map[string]string{
	x402.RulePaymentDigest:              "payment_already_recorded",
	x402.RulePendingCreditPurchasePayer: CodeCreditPurchasePending,
	x402.RulePendingInvoice:             "invoice_payment_pending",
}

// Outcome is where a settle call left the transfer.
type Outcome string

// Outcome values.
const (
	// OutcomeSettled is a transfer the facilitator settled.
	OutcomeSettled Outcome = "settled"
	// OutcomePending is a transfer that may still land; the settlement
	// is reconciled on chain.
	OutcomePending Outcome = "pending"
)

// SettleInput is a verified payment to settle.
type SettleInput struct {
	// Payment is the verified payment.
	Payment *VerifiedPayment
	// Kind is what it pays for.
	Kind x402.SettlementKind
	// Invoice is the invoice an invoice payment pays.
	Invoice *invoice.Invoice
	// PurchaseSettings are the wallet top-up settings of a credit
	// purchase.
	PurchaseSettings x402.Object
}

// SettleResult is a settle call's end.
type SettleResult struct {
	// Settlement is the recorded settlement; it stays pending whatever
	// the outcome, until reconciled.
	Settlement *x402.Settlement
	// Outcome is where the facilitator left the transfer.
	Outcome Outcome
	// TransactionHash is the settled transfer's hash; "" when pending.
	TransactionHash string
}

// Settle records a pending settlement of a verified payment, then asks
// the facilitator to settle it and records the answer. An invoice in
// another currency, or a payment of another amount than the invoice's
// due amount, a settlement that breaks its own rules, and a payment, a
// payer's credit purchase or an invoice already pending are validation
// failures; once recorded, a facilitator refusing the credentials or
// throttling leaves the settlement pending.
//
// source: app/services/x402/settlements/settle_service.rb
func (s *Service) Settle(ctx context.Context, in SettleInput) (*SettleResult, error) {
	vp := in.Payment
	if field, code := invoiceFailure(in); code != "" {
		return nil, fieldError(field, code)
	}
	st, err := s.insertPending(ctx, in)
	if err != nil {
		return nil, err
	}
	res := &SettleResult{Settlement: st}
	fac, err := s.facilitator(ctx, vp.Connection)
	if err != nil {
		return nil, err
	}
	answer, err := fac.Settle(ctx, vp.Payload.Payment, vp.Payload.Requirements)
	switch {
	case errors.Is(err, x402.ErrFacilitatorCredentials):
		s.recordUnsettled(ctx, vp, st, "credential_error", err)
		res.Outcome = OutcomePending
		return res, nil
	case errors.Is(err, x402.ErrFacilitatorRateLimited):
		s.recordUnsettled(ctx, vp, st, "rate_limited", err)
		res.Outcome = OutcomePending
		return res, nil
	case err != nil:
		return nil, fmt.Errorf("x402 settle: %w", err)
	}
	problem := answerProblem(vp, answer)
	if problem != "" {
		s.logFailure(ctx, vp, problem)
	}
	payload := maps.Clone(st.Payload)
	payload["settle_response"] = answer.Response
	reason := problem
	if reason == "" {
		reason = answer.ErrorReason
	}
	var hash *string
	if problem == "" && answer.Outcome == x402.SettleOutcomeSettlementPending && wellFormedHash(vp.Family(), answer.Transaction) {
		hash = &answer.Transaction
	}
	s.record(ctx, vp, st, payload, reason, hash)
	if answer.Settled() && problem == "" {
		res.Outcome, res.TransactionHash = OutcomeSettled, answer.Transaction
	} else {
		res.Outcome = OutcomePending
	}
	return res, nil
}

// invoiceFailure checks an invoice payment pays the invoice's due amount
// in USD.
func invoiceFailure(in SettleInput) (field, code string) {
	inv := in.Invoice
	if inv == nil {
		return "", ""
	}
	if inv.Currency.String() != invoiceCurrency {
		return "invoice", "currencies_does_not_match"
	}
	due := new(big.Int)
	if cents := inv.TotalDueAmount(); cents > 0 {
		a, ok := x402.LookupAsset(in.Payment.Connection.Asset, in.Payment.Network())
		if !ok {
			return "payment", "amount_mismatch"
		}
		due = a.AtomicFromCents(cents)
	}
	if atomic := in.Payment.SettledAmountAtomic(); atomic == nil || atomic.Cmp(due) != 0 {
		return "payment", "amount_mismatch"
	}
	return "", ""
}

// insertPending records the pending settlement of the payment.
func (s *Service) insertPending(ctx context.Context, in SettleInput) (*x402.Settlement, error) {
	vp := in.Payment
	reconcile, err := s.reconcileAfter(vp)
	if err != nil {
		return nil, err
	}
	now := s.d.Clock.Now().UTC()
	st := &x402.Settlement{
		ID: core.NewID[core.X402SettlementID](s.d.IDs), OrganizationID: vp.Connection.OrganizationID,
		ConnectionID: vp.Connection.ID, Kind: in.Kind, Status: x402.SettlementStatusPending, SettledBy: x402.SettledByTanvo,
		PurchaseSettings: in.PurchaseSettings, Network: vp.Network(), Asset: vp.Asset(),
		PayerAddress: vp.PayerAddress, PayeeAddress: vp.PayeeAddress(), SettledAmountAtomic: vp.SettledAmountAtomic(),
		PaymentDigest: vp.PaymentDigest, ReconcileAfter: &reconcile,
		Payload: x402.Object{
			"payment":              vp.Payload.Payment,
			"payment_requirements": vp.Payload.Requirements,
			"verify_response":      vp.VerifyResponse,
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if cents := vp.SettledAmountCents(); cents != nil && cents.IsInt64() {
		st.SettledAmountCents = cents.Int64()
	}
	if in.Invoice != nil {
		st.InvoiceID = in.Invoice.ID
	}
	st.Normalize()
	var v core.Validator
	v.Merge("", st.Validate())
	v.Merge("", st.ValidateConnection(vp.Connection))
	if in.Invoice != nil {
		v.Check(in.Invoice.OrganizationID == st.OrganizationID, "invoice", x402.CodeSameOrganization)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.X402Settlements.Create(ctx, st)
	})
	var ce *x402.ConflictError
	if errors.As(err, &ce) {
		if code, ok := raceCodes[ce.Rule]; ok {
			return nil, refusal(code)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create x402 settlement: %w", err)
	}
	return st, nil
}

// reconcileAfter is when the pending settlement may be reconciled: once
// an EVM authorization expired, or once a Solana blockhash can be proven
// expired.
func (s *Service) reconcileAfter(vp *VerifiedPayment) (time.Time, error) {
	if vp.Family() != x402.FamilyEVM {
		return s.d.Clock.Now().UTC().Add(x402.SVMExpiryProof), nil
	}
	vb, ok := vp.Payload.ValidBefore()
	if !ok || !vb.IsInt64() {
		return time.Time{}, fmt.Errorf("x402 settle: authorization validBefore %v is not a time", vp.Payload.Authorization()["validBefore"])
	}
	return time.Unix(vb.Int64(), 0).UTC().Add(clockSkewTolerance), nil
}

// answerProblem is what makes a settled or pending answer unusable: it
// names another network or another payer, or a settled answer has no
// well-formed hash.
func answerProblem(vp *VerifiedPayment, answer x402.SettleResult) string {
	if answer.Outcome != x402.SettleOutcomeSettled && answer.Outcome != x402.SettleOutcomeSettlementPending {
		return ""
	}
	switch {
	case answer.Network != vp.Network().String():
		return "network_mismatch"
	case answer.Payer != "" && x402.NormalizeAddress(answer.Payer, vp.Family()) != vp.PayerAddress:
		return "payer_mismatch"
	case answer.Settled() && !wellFormedHash(vp.Family(), answer.Transaction):
		return "malformed_response"
	}
	return ""
}

// wellFormedHash reports whether hash is an EVM transaction hash or a
// Solana signature.
func wellFormedHash(f x402.Family, hash string) bool {
	if f == x402.FamilyEVM {
		return x402.HexWord.MatchString(hash)
	}
	b, ok := x402.Base58Decode(hash)
	return ok && len(b) == x402.SVMSignatureBytes
}

// recordUnsettled records a facilitator failure that leaves the transfer
// pending.
func (s *Service) recordUnsettled(ctx context.Context, vp *VerifiedPayment, st *x402.Settlement, reason string, err error) {
	response := x402.Object{}
	var fe *x402.FacilitatorError
	if errors.As(err, &fe) {
		if fe.HTTPStatus != 0 {
			response["httpStatus"] = fe.HTTPStatus
		}
		if fe.ErrorType != "" {
			response["errorType"] = fe.ErrorType
		}
		if fe.CorrelationID != "" {
			response["correlationId"] = fe.CorrelationID
		}
	}
	payload := maps.Clone(st.Payload)
	payload["settle_response"] = response
	s.record(ctx, vp, st, payload, reason, nil)
}

// record stores the facilitator's answer on st; a failed write restores
// st and is logged, not returned.
func (s *Service) record(ctx context.Context, vp *VerifiedPayment, st *x402.Settlement, payload x402.Object, reason string, hash *string) {
	before := *st
	st.Payload = payload
	st.ErrorReason = nil
	if reason != "" {
		st.ErrorReason = &reason
	}
	st.TransactionHash = hash
	st.UpdatedAt = s.d.Clock.Now().UTC()
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.X402Settlements.Update(ctx, st)
	})
	if err != nil {
		*st = before
		s.logFailure(ctx, vp, "record_failed", "error", err.Error())
	}
}

// logFailure logs why a settle call did not settle.
func (s *Service) logFailure(ctx context.Context, vp *VerifiedPayment, reason string, details ...any) {
	args := append([]any{"reason", reason}, details...)
	args = append(args, "network", vp.Network().String())
	s.log.WarnContext(ctx, "x402 settle failed", args...)
}
