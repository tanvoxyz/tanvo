package x402pay_test

import (
	"context"
	"errors"
	"maps"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// spyTx counts the settlements created and fails their writes on demand.
type spyTx struct {
	inner      port.Transactor
	creates    int
	failCreate error
	failUpdate error
}

func (s *spyTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	return s.inner.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		r.X402Settlements = spySettlements{SettlementRepository: r.X402Settlements, tx: s}
		return fn(ctx, r)
	})
}

type spySettlements struct {
	x402.SettlementRepository
	tx *spyTx
}

func (s spySettlements) Create(ctx context.Context, st *x402.Settlement) error {
	if s.tx.failCreate != nil {
		return s.tx.failCreate
	}
	if err := s.SettlementRepository.Create(ctx, st); err != nil {
		return err
	}
	s.tx.creates++
	return nil
}

func (s spySettlements) Update(ctx context.Context, st *x402.Settlement) error {
	if s.tx.failUpdate != nil {
		return s.tx.failUpdate
	}
	return s.SettlementRepository.Update(ctx, st)
}

// settleCase is the payment of the settle specs: a verified credit
// purchase of 1.050001 USDC on Base Sepolia, settled at 1789649271.
//
// source: spec/services/x402/settlements/settle_service_spec.rb:11-36
type settleCase struct {
	*env
	spy          *spyTx
	connection   *x402.Connection
	payment      x402.Object
	requirements x402.Object
	payer        string
	in           x402pay.SettleInput
}

func newSettle(t *testing.T) *settleCase {
	t.Helper()
	st := memstore.New()
	spy := &spyTx{inner: st}
	e := newEnvOn(t, billingtest.NewOn(t, now, spy, st))
	c := &settleCase{env: e, spy: spy, connection: e.sharedConnection(), payer: evmPayer}
	c.requirements = evmRequirements(t)
	c.requirements["amount"] = "1050001"
	c.payment = evmPayment(t, c.requirements)
	authorization(c.payment)["value"] = "1050001"
	c.in = x402pay.SettleInput{
		Kind: x402.SettlementKindCreditPurchase, PurchaseSettings: x402.Object{"plan_code": "agent_api", "wallet_code": "agent_credits"},
	}
	return c
}

// solana switches the case to the Solana payment of 0.01 USDC.
//
// source: spec/services/x402/settlements/settle_service_spec.rb:530-534
func (c *settleCase) solana(t *testing.T) {
	c.requirements = svmRequirements(t)
	c.requirements["amount"] = "10000"
	c.payment = svmPayment(t, c.requirements)
	c.payer = svmPayer
}

// answer scripts the settle answer.
func (c *settleCase) answer(r x402.SettleResult, err error) {
	c.fac.settle = func() (x402.SettleResult, error) { return r, err }
}

func (c *settleCase) run() (*x402pay.SettleResult, error) {
	in := c.in
	in.Payment = &x402pay.VerifiedPayment{
		Connection: c.connection, Payload: x402.PaymentPayload{Payment: c.payment, Requirements: c.requirements},
		PayerAddress: c.payer, PaymentDigest: "digest", VerifyResponse: x402.Object{"isValid": true},
	}
	return c.svc.Settle(c.h.Ctx, in)
}

// settled runs the case and returns its result and its stored settlement.
func (c *settleCase) settled(t *testing.T) (*x402pay.SettleResult, *x402.Settlement) {
	t.Helper()
	res, err := c.run()
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	return res, c.reload(t, res.Settlement.ID)
}

func (c *settleCase) reload(t *testing.T, id core.X402SettlementID) *x402.Settlement {
	t.Helper()
	return billingtest.Read(c.h, func(ctx context.Context, r port.Repos) (*x402.Settlement, error) {
		return r.X402Settlements.Get(ctx, c.h.OrgID(), id)
	})
}

// pending checks a pending outcome: no hash reported, the row pending.
func pending(t *testing.T, res *x402pay.SettleResult, st *x402.Settlement) {
	t.Helper()
	if res.Outcome != x402pay.OutcomePending || res.TransactionHash != "" || st.Status != x402.SettlementStatusPending {
		t.Errorf("outcome, hash, status: got = %s %q %s, want = pending \"\" pending", res.Outcome, res.TransactionHash, st.Status)
	}
}

func reason(st *x402.Settlement) string {
	if st.ErrorReason == nil {
		return ""
	}
	return *st.ErrorReason
}

func hash(st *x402.Settlement) string {
	if st.TransactionHash == nil {
		return ""
	}
	return *st.TransactionHash
}

// refusedBeforeSettle checks a refusal that writes nothing and calls no
// settle.
func (c *settleCase) refusedBeforeSettle(t *testing.T, want string) {
	t.Helper()
	before := c.spy.creates
	_, err := c.run()
	wantError(t, err, want)
	if n := c.fac.count("settle"); n != 0 {
		t.Errorf("settle calls: got = %d, want = 0", n)
	}
	if n := c.spy.creates - before; n != 0 {
		t.Errorf("settlements created: got = %d, want = 0", n)
	}
}

var pendingHash = "0x" + strings.Repeat("cd", 32)

func TestSettleSettled(t *testing.T) {
	// source: spec/services/x402/settlements/settle_service_spec.rb:88-142
	c := newSettle(t)
	var seen []string
	c.fac.onSettle = func() {
		st := billingtest.Read(c.h, func(ctx context.Context, r port.Repos) ([]*x402.Settlement, error) {
			return r.X402Settlements.ListPendingReconciliation(ctx, c.h.OrgID(), c.h.Now().Add(time.Hour))
		})
		for _, s := range st {
			seen = append(seen, string(s.Status)+" "+s.PaymentDigest)
		}
	}
	res, st := c.settled(t)
	if diff := cmp.Diff([]string{"pending digest"}, seen); diff != "" {
		t.Errorf("rows at settle (-want +got):\n%s", diff)
	}
	if res.Outcome != x402pay.OutcomeSettled || res.TransactionHash != settledHash {
		t.Errorf("outcome, hash: got = %s %q, want = settled %q", res.Outcome, res.TransactionHash, settledHash)
	}
	if st.Status != x402.SettlementStatusPending || st.TransactionHash != nil || st.ErrorReason != nil {
		t.Errorf("row: got = %s %v %v, want = pending without hash or error", st.Status, st.TransactionHash, st.ErrorReason)
	}
	type terms struct {
		Org, Connection             string
		Kind                        x402.SettlementKind
		Network                     x402.Network
		Asset, Payer, Payee, Digest string
		Atomic                      string
		Cents                       int64
		Settings                    x402.Object
		ReconcileAfter              time.Time
	}
	got := terms{
		st.OrganizationID.String(), st.ConnectionID.String(), st.Kind, st.Network, st.Asset, st.PayerAddress, st.PayeeAddress,
		st.PaymentDigest, st.SettledAmountAtomic.String(), st.SettledAmountCents, st.PurchaseSettings, *st.ReconcileAfter,
	}
	want := terms{
		c.h.OrgID().String(), c.connection.ID.String(), x402.SettlementKindCreditPurchase, baseSepolia, sepoliaUSDC, evmPayer, evmPayout,
		"digest", "1050001", 105, c.in.PurchaseSettings, time.Unix(1_789_649_331+30, 0).UTC(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("terms (-want +got):\n%s", diff)
	}
	keys := slices.Sorted(maps.Keys(st.Payload))
	if diff := cmp.Diff([]string{"payment", "payment_requirements", "settle_response", "verify_response"}, keys); diff != "" {
		t.Errorf("payload keys (-want +got):\n%s", diff)
	}
	if c.fac.count("settle") != 1 {
		t.Errorf("settle calls: got = %d, want = 1", c.fac.count("settle"))
	}
	if diff := cmp.Diff(c.payment, c.fac.payments[0]); diff != "" {
		t.Errorf("forwarded payment (-want +got):\n%s", diff)
	}
}

func TestSettleUnsettledAnswers(t *testing.T) {
	answer := func(outcome x402.SettleOutcome, reason, transaction string) x402.SettleResult {
		resp := x402.Object{"success": false, "network": string(baseSepolia)}
		if reason != "" {
			resp["errorReason"] = reason
		}
		if transaction != "" {
			resp["transaction"] = transaction
		}
		return x402.SettleResult{Outcome: outcome, Transaction: transaction, Network: string(baseSepolia), ErrorReason: reason, Response: resp}
	}
	tests := []struct {
		name, source string
		answer       x402.SettleResult
		reason, hash string
	}{
		{"with a failure after verify", "154-162", answer(x402.SettleOutcomeUnconfirmedFailure, "invalid_exact_evm_signature", ""), "invalid_exact_evm_signature", ""},
		{"with a failure on chain", "164-176", answer(x402.SettleOutcomeUnconfirmedFailure, "settle_exact_failed_onchain", pendingHash), "settle_exact_failed_onchain", ""},
		{"with a KYT decline", "178-186", answer(x402.SettleOutcomeUnconfirmedFailure, "kyt_risk_detected", ""), "kyt_risk_detected", ""},
		{"with a pending settlement", "188-200", answer(x402.SettleOutcomeSettlementPending, "settlement_pending", pendingHash), "settlement_pending", pendingHash},
		{"with a pending settlement without a hash", "202-214", answer(x402.SettleOutcomeSettlementPending, "settlement_pending", ""), "settlement_pending", ""},
		{"with a dropped connection or a timeout", "216-238", x402.SettleResult{Outcome: x402.SettleOutcomeNoResponse, ErrorReason: "no_response", Response: x402.Object{}}, "no_response", ""},
		{"with a server error", "240-253", answer(x402.SettleOutcomeServerError, "server_error", ""), "server_error", ""},
		{"with a pending settlement whose hash is malformed", "306-314", answer(x402.SettleOutcomeSettlementPending, "settlement_pending", "not-a-hash"), "settlement_pending", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			c := newSettle(t)
			c.answer(tt.answer, nil)
			res, st := c.settled(t)
			pending(t, res, st)
			if reason(st) != tt.reason || hash(st) != tt.hash {
				t.Errorf("reason, hash: got = %q %q, want = %q %q", reason(st), hash(st), tt.reason, tt.hash)
			}
			if diff := cmp.Diff(tt.answer.Response, st.Payload["settle_response"]); diff != "" {
				t.Errorf("settle response (-want +got):\n%s", diff)
			}
			if c.fac.count("settle") != 1 {
				t.Errorf("settle calls: got = %d, want = 1", c.fac.count("settle"))
			}
		})
	}
}

func TestSettleFacilitatorErrors(t *testing.T) {
	tests := []struct {
		name, source string
		kind         error
		status       int
		errorType    string
		reason       string
	}{
		{"with a 401 answer", "255-269", x402.ErrFacilitatorCredentials, 401, "unauthorized", "credential_error"},
		{"with a 402 answer", "255-269", x402.ErrFacilitatorCredentials, 402, "unauthorized", "credential_error"},
		{"with a 403 answer", "255-269", x402.ErrFacilitatorCredentials, 403, "unauthorized", "credential_error"},
		{"with a 429 answer", "271-283", x402.ErrFacilitatorRateLimited, 429, "rate_limit_exceeded", "rate_limited"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			c := newSettle(t)
			c.answer(x402.SettleResult{}, &x402.FacilitatorError{
				Kind: tt.kind, Message: "settle: failed", HTTPStatus: tt.status, ErrorType: tt.errorType, CorrelationID: "corr-1",
			})
			res, st := c.settled(t)
			pending(t, res, st)
			if reason(st) != tt.reason {
				t.Errorf("reason: got = %q, want = %q", reason(st), tt.reason)
			}
			want := x402.Object{"httpStatus": tt.status, "errorType": tt.errorType, "correlationId": "corr-1"}
			if diff := cmp.Diff(want, st.Payload["settle_response"]); diff != "" {
				t.Errorf("settle response (-want +got):\n%s", diff)
			}
		})
	}
	t.Run("without the facilitator's details", func(t *testing.T) {
		c := newSettle(t)
		c.answer(x402.SettleResult{}, &x402.FacilitatorError{Kind: x402.ErrFacilitatorRateLimited, Message: "settle: slow"})
		_, st := c.settled(t)
		if diff := cmp.Diff(x402.Object{}, st.Payload["settle_response"]); diff != "" {
			t.Errorf("settle response (-want +got):\n%s", diff)
		}
	})
}

func TestSettleProblems(t *testing.T) {
	settledAnswer := func(transaction, network, payer string) x402.SettleResult {
		resp := x402.Object{"success": true, "transaction": transaction}
		if network != "" {
			resp["network"] = network
		}
		if payer != "" {
			resp["payer"] = payer
		}
		return x402.SettleResult{Outcome: x402.SettleOutcomeSettled, Transaction: transaction, Network: network, Payer: payer, Response: resp}
	}
	other := "0x" + strings.Repeat("ef", 32)
	tests := []struct {
		name, source string
		answer       x402.SettleResult
		problem      string
	}{
		{"with a settled answer whose transaction is not a hash", "285-304", settledAnswer("not-a-hash", string(baseSepolia), ""), "malformed_response"},
		{"with a settled answer whose transaction is a number", "285-304", settledAnswer("123", string(baseSepolia), ""), "malformed_response"},
		{"with another network", "358-362", settledAnswer(other, "eip155:8453", evmPayer), "network_mismatch"},
		{"without a network", "364-368", settledAnswer(other, "", evmPayer), "network_mismatch"},
		{"with another payer", "370-374", settledAnswer(other, string(baseSepolia), otherPayTo), "payer_mismatch"},
		{"with a pending answer on another network", "392-397", x402.SettleResult{
			Outcome: x402.SettleOutcomeSettlementPending, Transaction: pendingHash, Network: "eip155:8453", ErrorReason: "settlement_pending",
			Response: x402.Object{"success": false, "errorReason": "settlement_pending", "network": "eip155:8453", "transaction": pendingHash},
		}, "network_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source} and :56-75
			c := newSettle(t)
			c.answer(tt.answer, nil)
			res, st := c.settled(t)
			pending(t, res, st)
			if reason(st) != tt.problem || st.TransactionHash != nil {
				t.Errorf("reason, hash: got = %q %v, want = %q nil", reason(st), st.TransactionHash, tt.problem)
			}
			if diff := cmp.Diff(tt.answer.Response, st.Payload["settle_response"]); diff != "" {
				t.Errorf("settle response (-want +got):\n%s", diff)
			}
			if !strings.Contains(c.log.String(), "reason="+tt.problem) {
				t.Errorf("log: got = %q, want = reason=%s", c.log.String(), tt.problem)
			}
		})
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:376-390
	for _, payer := range []string{strings.ToLower(evmPayer), ""} {
		c := newSettle(t)
		c.answer(settledAnswer(other, string(baseSepolia), payer), nil)
		res, _ := c.settled(t)
		if res.Outcome != x402pay.OutcomeSettled || res.TransactionHash != other {
			t.Errorf("payer %q: got = %s %q, want = settled %q", payer, res.Outcome, res.TransactionHash, other)
		}
	}
}

func TestSettleRecordFailure(t *testing.T) {
	// source: spec/services/x402/settlements/settle_service_spec.rb:316-338
	t.Run("with a settled answer that cannot be stored", func(t *testing.T) {
		c := newSettle(t)
		other := "0x" + strings.Repeat("ef", 32)
		c.fac.onSettle = func() { c.spy.failUpdate = errors.New("invalid input syntax for type json") }
		c.answer(x402.SettleResult{
			Outcome: x402.SettleOutcomeSettled, Transaction: other, Network: string(baseSepolia),
			Response: x402.Object{"success": true, "transaction": other, "note": "a\x00b"},
		}, nil)
		res, err := c.run()
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if res.Outcome != x402pay.OutcomeSettled || res.TransactionHash != other {
			t.Errorf("outcome, hash: got = %s %q, want = settled %q", res.Outcome, res.TransactionHash, other)
		}
		st := c.reload(t, res.Settlement.ID)
		if _, ok := st.Payload["settle_response"]; ok || st.Status != x402.SettlementStatusPending {
			t.Errorf("row: got = %s %v, want = pending without the answer", st.Status, st.Payload)
		}
		if _, ok := res.Settlement.Payload["settle_response"]; ok {
			t.Errorf("returned settlement: got = %v, want = restored", res.Settlement.Payload)
		}
		if !strings.Contains(c.log.String(), "reason=record_failed") {
			t.Errorf("log: got = %q, want = reason=record_failed", c.log.String())
		}
		c.spy.failUpdate = nil
		res.Settlement.Status, res.Settlement.TransactionHash = x402.SettlementStatusSettled, &other
		c.h.Do(func(ctx context.Context, r port.Repos) error { return r.X402Settlements.Update(ctx, res.Settlement) })
	})
	// source: spec/services/x402/settlements/settle_service_spec.rb:340-350
	t.Run("with a pending settlement whose hash another row holds", func(t *testing.T) {
		c := newSettle(t)
		pendingPurchase(t, c.env, c.connection, otherPayTo, x402.SettlementKindCreditPurchase, func(s *x402.Settlement) {
			s.Status, s.ReconcileAfter, s.TransactionHash = x402.SettlementStatusFailed, nil, &pendingHash
		})
		c.answer(x402.SettleResult{
			Outcome: x402.SettleOutcomeSettlementPending, Transaction: pendingHash, Network: string(baseSepolia),
			ErrorReason: "settlement_pending", Response: x402.Object{"transaction": pendingHash},
		}, nil)
		res, st := c.settled(t)
		pending(t, res, st)
		if st.TransactionHash != nil {
			t.Errorf("hash: got = %s, want = nil", *st.TransactionHash)
		}
	})
}

func TestSettleRaces(t *testing.T) {
	digest := func(s *x402.Settlement) { s.PaymentDigest = "digest" }
	tests := []struct {
		name, source string
		setup        func(*settleCase)
		want         string
	}{
		{"when a pending attempt holds the digest", "400-409", func(c *settleCase) {
			pendingPurchase(t, c.env, c.connection, otherPayTo, x402.SettlementKindCreditPurchase, digest)
		}, "base: payment_already_recorded"},
		{"when a settled attempt holds the digest", "411-420", func(c *settleCase) {
			pendingPurchase(t, c.env, c.connection, otherPayTo, x402.SettlementKindCreditPurchase, digest, func(s *x402.Settlement) {
				s.Status, s.ReconcileAfter, s.TransactionHash = x402.SettlementStatusSettled, nil, new("0x"+strings.Repeat("12", 32))
			})
		}, "base: payment_already_recorded"},
		{"when another organization holds the digest", "458-462", func(c *settleCase) {
			pendingPurchase(t, c.env, c.otherConnection(), otherPayTo, x402.SettlementKindCreditPurchase, digest)
		}, "base: payment_already_recorded"},
		{"when the payer has a pending credit purchase", "464-468", func(c *settleCase) {
			pendingPurchase(t, c.env, c.connection, evmPayer, x402.SettlementKindCreditPurchase)
		}, "base: credit_purchase_pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			c := newSettle(t)
			tt.setup(c)
			c.refusedBeforeSettle(t, tt.want)
		})
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:422-433
	t.Run("when a pending attempt of the same payer holds the digest", func(t *testing.T) {
		c := newSettle(t)
		pendingPurchase(t, c.env, c.connection, evmPayer, x402.SettlementKindCreditPurchase, digest)
		_, err := c.run()
		if got := errorText(err); got != "base: payment_already_recorded" && got != "base: credit_purchase_pending" {
			t.Errorf("error: got = %q, want = a race code", got)
		}
	})
	// source: spec/services/x402/settlements/settle_service_spec.rb:435-448
	t.Run("when the insert hits an unknown unique rule", func(t *testing.T) {
		c := newSettle(t)
		c.spy.failCreate = &x402.ConflictError{Rule: x402.RulePayment}
		_, err := c.run()
		if !errors.Is(err, core.ErrConflict) || len(core.FieldErrors(err)) != 0 {
			t.Errorf("error: got = %v, want = the conflict", err)
		}
		if c.fac.count("settle") != 0 {
			t.Errorf("settle calls: got = %d, want = 0", c.fac.count("settle"))
		}
	})
	settles := []struct {
		name, source string
		setup        func(*settleCase)
	}{
		{"when only a failed attempt holds the digest", "450-456", func(c *settleCase) {
			pendingPurchase(t, c.env, c.connection, otherPayTo, x402.SettlementKindCreditPurchase, digest, func(s *x402.Settlement) {
				s.Status, s.ReconcileAfter = x402.SettlementStatusFailed, nil
			})
		}},
		{"when the payer has a pending invoice payment", "470-476", func(c *settleCase) {
			pendingPurchase(t, c.env, c.connection, evmPayer, x402.SettlementKindInvoicePayment)
		}},
	}
	for _, tt := range settles {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			c := newSettle(t)
			tt.setup(c)
			if res, _ := c.settled(t); res.Outcome != x402pay.OutcomeSettled {
				t.Errorf("outcome: got = %s, want = settled", res.Outcome)
			}
		})
	}
}

func TestSettleInvoicePayment(t *testing.T) {
	// source: spec/services/x402/settlements/settle_service_spec.rb:478-484
	setup := func(t *testing.T, opts ...func(*invoice.Invoice)) *settleCase {
		c := newSettle(t)
		c.requirements["amount"] = "1050000"
		authorization(c.payment)["value"] = "1050000"
		c.in.Kind, c.in.PurchaseSettings = x402.SettlementKindInvoicePayment, nil
		c.in.Invoice = c.h.StoredInvoice(c.h.Customer(), append([]func(*invoice.Invoice){func(inv *invoice.Invoice) {
			inv.TotalAmount = 105
		}}, opts...)...)
		return c
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:485-491
	t.Run("links the invoice and settles the amount due", func(t *testing.T) {
		c := setup(t)
		res, st := c.settled(t)
		if st.InvoiceID != c.in.Invoice.ID || res.Outcome != x402pay.OutcomeSettled {
			t.Errorf("invoice, outcome: got = %s %s, want = %s settled", st.InvoiceID, res.Outcome, c.in.Invoice.ID)
		}
	})
	tests := []struct {
		name, source string
		change       func(*invoice.Invoice)
		want         string
	}{
		{"with a payment below the amount due", "493-497", func(inv *invoice.Invoice) { inv.TotalAmount = 5_000 }, "payment: amount_mismatch"},
		{"with a payment above the amount due", "499-503", func(inv *invoice.Invoice) { inv.TotalAmount = 100 }, "payment: amount_mismatch"},
		{"with an invoice already paid", "505-509", func(inv *invoice.Invoice) { inv.TotalPaidAmount = 105 }, "payment: amount_mismatch"},
		{"with an invoice in another currency", "511-515", func(inv *invoice.Invoice) { inv.Currency = money.MustCurrency("EUR") }, "invoice: currencies_does_not_match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			setup(t, tt.change).refusedBeforeSettle(t, tt.want)
		})
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:517-521
	t.Run("with an invoice of another organization", func(t *testing.T) {
		c := setup(t)
		inv := *c.in.Invoice
		inv.OrganizationID = core.NewID[core.OrganizationID](c.h.IDs)
		c.in.Invoice = &inv
		c.refusedBeforeSettle(t, "invoice: must_belong_to_same_organization")
	})
	// source: spec/services/x402/settlements/settle_service_spec.rb:523-527
	t.Run("when an earlier attempt is pending for the invoice", func(t *testing.T) {
		c := setup(t)
		pendingPurchase(t, c.env, c.connection, otherPayTo, x402.SettlementKindInvoicePayment, func(s *x402.Settlement) {
			s.InvoiceID = c.in.Invoice.ID
		})
		c.refusedBeforeSettle(t, "base: invoice_payment_pending")
	})
}

func TestSettleSolana(t *testing.T) {
	// source: spec/services/x402/settlements/settle_service_spec.rb:536-546
	c := newSettle(t)
	c.solana(t)
	signature := x402.Base58Encode([]byte(strings.Repeat("\xab", 64)))
	c.answer(x402.SettleResult{
		Outcome: x402.SettleOutcomeSettled, Transaction: signature, Network: string(devnet),
		Response: x402.Object{"success": true, "transaction": signature},
	}, nil)
	res, st := c.settled(t)
	if !st.ReconcileAfter.Equal(now.Add(120*time.Second)) || st.SettledAmountAtomic.Cmp(big.NewInt(10_000)) != 0 || st.PayeeAddress != svmPayout {
		t.Errorf("reconcile, amount, payee: got = %v %v %s", st.ReconcileAfter, st.SettledAmountAtomic, st.PayeeAddress)
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:548-556
	if res.Outcome != x402pay.OutcomeSettled || res.TransactionHash != signature {
		t.Errorf("outcome, hash: got = %s %q, want = settled %q", res.Outcome, res.TransactionHash, signature)
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:558-566
	c = newSettle(t)
	c.solana(t)
	evmHash := "0x" + strings.Repeat("ef", 32)
	c.answer(x402.SettleResult{Outcome: x402.SettleOutcomeSettled, Transaction: evmHash, Network: string(devnet), Response: x402.Object{}}, nil)
	res, st = c.settled(t)
	pending(t, res, st)
	if reason(st) != "malformed_response" {
		t.Errorf("reason: got = %q, want = malformed_response", reason(st))
	}
}

func TestSettleInvalidSettlement(t *testing.T) {
	tests := []struct {
		name, source string
		change       func(*settleCase)
		want         string
	}{
		{"with an unknown kind", "569-580", func(c *settleCase) { c.in.Kind = "refund" }, "kind: invalid_value"},
		{"without purchase settings on a credit purchase", "582-593", func(c *settleCase) { c.in.PurchaseSettings = nil }, "purchase_settings: value_is_mandatory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/services/x402/settlements/settle_service_spec.rb:{tt.source}
			c := newSettle(t)
			tt.change(c)
			c.refusedBeforeSettle(t, tt.want)
		})
	}
	// source: spec/services/x402/settlements/settle_service_spec.rb:595-615
	t.Run("with a connection paying out to another address", func(t *testing.T) {
		c := newSettle(t)
		c.connection = c.connection2(t)
		_, err := c.run()
		if got := errorText(err); !strings.Contains(got, "payee_address: not_connection_payout_address") {
			t.Errorf("error: got = %q, want = payee_address: not_connection_payout_address", got)
		}
		if c.fac.count("settle") != 0 {
			t.Errorf("settle calls: got = %d, want = 0", c.fac.count("settle"))
		}
	})
	// The refusal inside a database transaction
	// (settle_service_spec.rb:617-630) is not ported: Settle runs its own
	// transactions and a caller's transaction is not visible to it.
}

// connection2 creates a Base Sepolia connection paying out to another
// address.
func (c *settleCase) connection2(t *testing.T) *x402.Connection {
	t.Helper()
	return c.env.connection([]x402.Network{baseSepolia}, map[x402.Family]string{x402.FamilyEVM: otherPayTo})
}
