package cli_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

// creditNotes returns the finalized credit notes of the invoice id.
func (s *scenario) creditNotes(invoice string) []jsonValue {
	s.t.Helper()
	return s.api(http.MethodGet, "/v1/credit_notes?filter="+url.QueryEscape(`invoice_id = "`+invoice+`"`), nil).list("credit_notes")
}

// appliedCoupon returns the applied coupon id from the list of the
// applied coupons (the API has no Get, as lago v1.55.0).
func (s *scenario) appliedCoupon(id string) jsonValue {
	s.t.Helper()
	for _, a := range s.api(http.MethodGet, "/v1/applied_coupons", nil).list("applied_coupons") {
		if a.str("id") == id {
			return a
		}
	}
	s.t.Fatalf("applied coupon %s: not listed", id)
	return jsonValue{}
}

// wantCents checks the Money fields of v, in minor units.
func wantCents(t *testing.T, what string, v jsonValue, want map[string]int64) {
	t.Helper()
	for path, w := range want {
		if got := v.cents(path); got != w {
			t.Errorf("%s %s: got = %d, want = %d", what, path, got, w)
		}
	}
}

// TestScenarioTerminationCreditNote terminates a subscription paid in
// advance on its first day: the termination invoice is empty and the
// unused days are credited on the subscription invoice.
//
// source: spec/scenarios/terminate_pay_in_advance_spec.rb:14
func TestScenarioTerminationCreditNote(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2023, 2, 8, 0, 0, 0, 0, time.UTC), scenarioOptions{})
	s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "tax", "name": "Tax", "rate": "20"})
	entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
	s.api(http.MethodPatch, "/v1/billing_entities/"+entity, map[string]any{"tax_ids": []string{"code:tax"}})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.plan("plan", "INTERVAL_MONTHLY", "50", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
	s.idle()
	invs := s.waitInvoices("cust", 1)
	if len(invs) != 1 {
		t.Fatalf("invoices: got = %d, want = 1", len(invs))
	}
	sub := invs[0]
	// 60 / 28 * 21
	wantCents(t, "subscription invoice", sub, map[string]int64{"total_amount": 4500})

	s.api(http.MethodPost, "/v1/subscriptions/external_id%3Acust:terminate", map[string]any{})
	s.idle()
	invs = s.waitInvoices("cust", 2)
	if len(invs) != 2 {
		t.Fatalf("invoices after the termination: got = %d, want = 2", len(invs))
	}
	if !slices.Contains(s.webhookTypes(), "subscription.terminated") {
		t.Errorf("webhooks: got = %v, want = containing subscription.terminated", s.webhookTypes())
	}
	for _, inv := range invs {
		if inv.str("id") != sub.str("id") {
			wantCents(t, "termination invoice", inv, map[string]int64{"total_amount": 0})
		}
	}
	notes := s.creditNotes(sub.str("id"))
	if len(notes) != 1 {
		t.Fatalf("credit notes of the subscription invoice: got = %d, want = 1", len(notes))
	}
	// 60 / 28 * 20
	wantCents(t, "credit note", notes[0], map[string]int64{
		"sub_total_excluding_taxes_amount": 3571, "taxes_amount": 714, "credit_amount": 4285, "total_amount": 4285, "balance_amount": 4285,
	})
	// terminate_service.rb:62.
	if got := notes[0].str("reason"); got != "CREDIT_NOTE_REASON_ORDER_CANCELLATION" {
		t.Errorf("credit note reason: got = %s, want = CREDIT_NOTE_REASON_ORDER_CANCELLATION", got)
	}
	s.noFailedJobs()
}

// TestScenarioRefreshDraftCreditNote terminates a subscription paid in
// advance while its invoice is a draft of the grace period: the
// termination credit note is a draft of that invoice. Adjusting the
// invoice's subscription fee refreshes the draft, which scales the note
// item to the new fee (lago CreditNotes::RefreshDraftService), and the
// note is finalized with its invoice.
func TestScenarioRefreshDraftCreditNote(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2023, 2, 8, 0, 0, 0, 0, time.UTC), scenarioOptions{})
	s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "tax", "name": "Tax", "rate": "20"})
	entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
	s.api(http.MethodPatch, "/v1/billing_entities/"+entity, map[string]any{"tax_ids": []string{"code:tax"}})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "invoice_grace_period_days": 3})
	s.plan("plan", "INTERVAL_MONTHLY", "50", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "cust", "customer_id": "external_id:cust", "plan_id": "code:plan"})
	s.idle()
	invs := s.waitInvoices("cust", 1)
	if len(invs) != 1 || invs[0].str("status") != "INVOICE_STATUS_DRAFT" {
		t.Fatalf("invoices: got = %v, want = one draft", invs)
	}
	id := invs[0].str("id")

	// Terminated after the invoice was created, the subscription's fee is
	// rebuilt on refresh (calculate_fees_service.rb:291).
	s.at(time.Date(2023, 2, 8, 12, 0, 0, 0, time.UTC))
	s.api(http.MethodPost, "/v1/subscriptions/external_id%3Acust:terminate", map[string]any{})
	s.idle()
	if n := len(s.creditNotes(id)); n != 0 {
		t.Fatalf("credit notes served while the invoice is a draft: got = %d, want = 0", n)
	}

	inv := s.api(http.MethodGet, "/v1/invoices/"+id, nil).at("invoice")
	fee := inv.str("fees.0.id")
	wantCents(t, "draft invoice fee", inv.at("fees.0"), map[string]int64{"amount": 3750})
	s.api(http.MethodPost, "/v1/invoices/"+id+"/fees:adjust", map[string]any{
		"fee_id": fee, "mode": "FEE_ADJUSTMENT_MODE_UNITS_AND_UNIT_PRICE", "units": "1", "unit_amount": "25",
	})
	s.idle()
	inv = s.api(http.MethodGet, "/v1/invoices/"+id, nil).at("invoice")
	wantCents(t, "refreshed invoice", inv, map[string]int64{"fees_amount": 2500})

	s.api(http.MethodPost, "/v1/invoices/"+id+":finalize", map[string]any{})
	s.idle()
	notes := s.creditNotes(id)
	if len(notes) != 1 {
		t.Fatalf("credit notes after the finalization: got = %d, want = 1", len(notes))
	}
	// The item of 35.71 on a fee of 37.50 becomes 35.71 / 37.50 * 25.
	wantCents(t, "credit note", notes[0], map[string]int64{
		"sub_total_excluding_taxes_amount": 2381, "taxes_amount": 476, "credit_amount": 2857, "balance_amount": 2857,
	})
	if got := notes[0].str("status"); got != "CREDIT_NOTE_STATUS_FINALIZED" {
		t.Errorf("credit note status: got = %s, want = CREDIT_NOTE_STATUS_FINALIZED", got)
	}
	s.noFailedJobs()
}

// TestScenarioProgressiveBillingExcess bills a usage threshold, then the
// usage is clawed back: the period's invoice is credited what it uses of
// the progressive billing invoice, and the excess is a credit note on the
// progressive billing invoice (lago
// CreditNotes::CreateFromProgressiveBillingInvoice).
func TestScenarioProgressiveBillingExcess(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		// coupon is the frequency of a 20 EUR coupon; empty for none.
		coupon string
		// events are the units ingested and days the days after the
		// start they are ingested on.
		events, days []int
		// paid is the sum of the invoice totals and refund the sum of the
		// credited amounts of the credit notes.
		paid, refund int64
	}{
		// source: spec/scenarios/coupons_breakdown_spec.rb:600
		{"clawed back to zero, once coupon", "COUPON_FREQUENCY_ONCE", []int{40, -40}, []int{5, 5}, 2000, 2000},
		// source: spec/scenarios/coupons_breakdown_spec.rb:601
		{"clawed back to zero, forever coupon", "COUPON_FREQUENCY_FOREVER", []int{40, -40}, []int{5, 5}, 2000, 2000},
		// source: spec/scenarios/coupons_breakdown_spec.rb:615
		{"clawed back to half, once coupon", "COUPON_FREQUENCY_ONCE", []int{40, -20}, []int{5, 10}, 2000, 1000},
		// source: spec/scenarios/coupons_breakdown_spec.rb:616
		{"clawed back to half, forever coupon", "COUPON_FREQUENCY_FOREVER", []int{40, -20}, []int{5, 10}, 2000, 1000},
		{"clawed back to half", "", []int{40, -20}, []int{5, 10}, 4000, 2000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
			s := newScenario(t, start, scenarioOptions{})
			s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
				"name": "U", "code": "u", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "n",
			})
			s.api(http.MethodPost, "/v1/plans", map[string]any{
				"name": "P", "code": "pb", "interval": "INTERVAL_MONTHLY", "amount": money("0"),
				"charges": []map[string]any{{
					"code": "u", "billable_metric_id": "code:u", "charge_model": "CHARGE_MODEL_STANDARD",
					"properties": map[string]any{"standard": map[string]any{"amount": "1"}},
				}},
				"usage_thresholds": []map[string]any{{"amount": "40"}},
			})
			s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
			if tt.coupon != "" {
				s.api(http.MethodPost, "/v1/coupons", map[string]any{
					"name": "C", "code": "c", "coupon_type": "COUPON_TYPE_FIXED_AMOUNT", "frequency": tt.coupon,
					"amount": money("20"), "expiration": "COUPON_EXPIRATION_NO_EXPIRATION",
				})
				s.api(http.MethodPost, "/v1/applied_coupons", map[string]any{"customer_id": "external_id:cust", "coupon_id": "code:c"})
			}
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "s", "customer_id": "external_id:cust", "plan_id": "code:pb"})
			s.idle()
			for i, n := range tt.events {
				s.at(start.AddDate(0, 0, tt.days[i]))
				s.event("s", "u", "tx-"+string(rune('a'+i)), map[string]any{"n": n})
				s.idle()
				s.usageUpdate()
			}
			pb := s.waitInvoices("cust", 1)
			if len(pb) != 1 || pb[0].str("invoice_type") != "INVOICE_TYPE_PROGRESSIVE_BILLING" {
				t.Fatalf("invoices after the usage: got = %v, want = one progressive billing invoice", pb)
			}
			s.bill(start.AddDate(0, 1, 0))
			invs := s.waitInvoices("cust", 2)
			if len(invs) != 2 {
				t.Fatalf("invoices: got = %d, want = 2", len(invs))
			}
			var paid, refund int64
			for _, inv := range invs {
				paid += inv.cents("total_amount")
				for _, cn := range s.creditNotes(inv.str("id")) {
					refund += cn.cents("credit_amount")
				}
			}
			if paid != tt.paid || refund != tt.refund {
				t.Errorf("paid, refund: got = %d, %d, want = %d, %d", paid, refund, tt.paid, tt.refund)
			}
			notes := s.creditNotes(pb[0].str("id"))
			if len(notes) != 1 {
				t.Fatalf("credit notes of the progressive billing invoice: got = %d, want = 1", len(notes))
			}
			if got := notes[0].str("reason"); got != "CREDIT_NOTE_REASON_OTHER" {
				t.Errorf("credit note reason: got = %s, want = CREDIT_NOTE_REASON_OTHER", got)
			}
			s.noFailedJobs()
		})
	}
}

// TestScenarioVoidKeepsCreditNoteCredits voids an invoice paid in part
// by a credit note and by a wallet: the wallet credits return, the
// credit note's stay consumed.
//
// source: spec/services/invoices/void_service_spec.rb:159
// source: spec/services/invoices/void_service_spec.rb:167
func TestScenarioVoidKeepsCreditNoteCredits(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), scenarioOptions{})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer", "currency": "EUR"})
	s.api(http.MethodPost, "/v1/add_ons", map[string]any{"name": "Add-on", "code": "addon", "amount": money("100")})
	src := s.api(http.MethodPost, "/v1/invoices", map[string]any{
		"customer_id": "external_id:cust", "currency": "EUR", "fees": []map[string]any{{"add_on_id": "code:addon", "units": "1"}},
	}).at("invoice")
	s.idle()
	cn := s.api(http.MethodPost, "/v1/credit_notes", map[string]any{
		"invoice_id": src.str("id"), "reason": "CREDIT_NOTE_REASON_OTHER", "credit_amount": money("30"),
		"items": []map[string]any{{"fee_id": src.str("fees.0.id"), "amount": "30"}},
	}).str("credit_note.id")
	wallet := s.api(http.MethodPost, "/v1/wallets", map[string]any{
		"customer_id": "external_id:cust", "rate_amount": "1", "currency": "EUR", "granted_credits": "10",
	}).str("wallet.id")
	s.idle()
	s.plan("plan", "INTERVAL_MONTHLY", "50", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
		"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:plan", "billing_time": "BILLING_TIME_ANNIVERSARY",
	})
	s.idle()
	invs := s.waitInvoices("cust", 2)
	var inv jsonValue
	for _, i := range invs {
		if i.str("id") != src.str("id") {
			inv = s.api(http.MethodGet, "/v1/invoices/"+i.str("id"), nil).at("invoice")
		}
	}
	wantCents(t, "subscription invoice", inv, map[string]int64{"credit_notes_amount": 3000, "prepaid_credit_amount": 1000, "total_amount": 1000})
	wantCents(t, "credit note before the void", s.api(http.MethodGet, "/v1/credit_notes/"+cn, nil).at("credit_note"), map[string]int64{"balance_amount": 0})
	wantCents(t, "wallet before the void", s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).at("wallet"), map[string]int64{"balance_amount": 0})

	s.api(http.MethodPost, "/v1/invoices/"+inv.str("id")+":void", map[string]any{})
	s.idle()
	if got := s.api(http.MethodGet, "/v1/invoices/"+inv.str("id"), nil).str("invoice.status"); got != "INVOICE_STATUS_VOIDED" {
		t.Errorf("invoice status: got = %s, want = INVOICE_STATUS_VOIDED", got)
	}
	note := s.api(http.MethodGet, "/v1/credit_notes/"+cn, nil).at("credit_note")
	wantCents(t, "credit note after the void", note, map[string]int64{"balance_amount": 0})
	if got := note.str("credit_status"); got != "CREDIT_STATUS_CONSUMED" {
		t.Errorf("credit note credit status: got = %s, want = CREDIT_STATUS_CONSUMED", got)
	}
	wantCents(t, "wallet after the void", s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).at("wallet"), map[string]int64{"balance_amount": 1000})
	s.noFailedJobs()
}

// TestScenarioPaymentGateRecredit gates a subscription on its payment:
// the open gating invoice consumes a coupon, a credit note and a wallet,
// and when the payment fails the invoice is closed, the subscription
// canceled and the three are given back.
//
// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:285
// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:338
func TestScenarioPaymentGateRecredit(t *testing.T) {
	t.Parallel()
	s, f, conn := stripeScenario(t, time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), map[string]any{"external_id": "cust", "name": "Customer", "currency": "EUR"})
	s.api(http.MethodPost, "/v1/add_ons", map[string]any{"name": "Add-on", "code": "addon", "amount": money("1")})
	src := s.api(http.MethodPost, "/v1/invoices", map[string]any{
		"customer_id": "external_id:cust", "currency": "EUR", "fees": []map[string]any{{"add_on_id": "code:addon", "units": "1"}},
	}).at("invoice")
	s.idle()
	cn := s.api(http.MethodPost, "/v1/credit_notes", map[string]any{
		"invoice_id": src.str("id"), "reason": "CREDIT_NOTE_REASON_OTHER", "credit_amount": money("0.10"),
		"items": []map[string]any{{"fee_id": src.str("fees.0.id"), "amount": "0.10"}},
	}).str("credit_note.id")
	s.api(http.MethodPost, "/v1/coupons", map[string]any{
		"name": "C", "code": "c", "coupon_type": "COUPON_TYPE_FIXED_AMOUNT", "frequency": "COUPON_FREQUENCY_ONCE",
		"amount": money("0.10"), "expiration": "COUPON_EXPIRATION_NO_EXPIRATION",
	})
	applied := s.api(http.MethodPost, "/v1/applied_coupons", map[string]any{"customer_id": "external_id:cust", "coupon_id": "code:c"}).str("applied_coupon.id")
	wallet := s.api(http.MethodPost, "/v1/wallets", map[string]any{
		"customer_id": "external_id:cust", "rate_amount": "1", "currency": "EUR", "granted_credits": "0.10",
	}).str("wallet.id")
	s.idle()
	s.plan("plan", "INTERVAL_MONTHLY", "10", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
		"external_id": "gated", "customer_id": "external_id:cust", "plan_id": "code:plan",
		"activation_rules": []map[string]any{{"rule_type": "ACTIVATION_RULE_TYPE_PAYMENT", "timeout_hours": 48}},
	})
	s.idle()
	sub := s.api(http.MethodGet, "/v1/subscriptions/external_id%3Agated", nil).at("subscription")
	if got := sub.str("status"); got != "SUBSCRIPTION_STATUS_INCOMPLETE" {
		t.Fatalf("subscription status: got = %s, want = SUBSCRIPTION_STATUS_INCOMPLETE", got)
	}
	// The gating invoice is open, which the API does not serve (lago
	// invoices.visible): its status is read from the database.
	var gate string
	if err := s.pool.QueryRow(t.Context(), `SELECT i.id::text FROM invoices i JOIN invoice_subscriptions x ON x.organization_id = i.organization_id AND x.invoice_id = i.id
		JOIN subscriptions su ON su.organization_id = x.organization_id AND su.id = x.subscription_id
		WHERE i.organization_id = $1 AND su.external_id = 'gated'`, s.org).Scan(&gate); err != nil {
		t.Fatalf("gating invoice: %v", err)
	}
	status := func() string {
		var st string
		if err := s.pool.QueryRow(t.Context(), `SELECT status::text FROM invoices WHERE organization_id = $1 AND id = $2`, s.org, gate).Scan(&st); err != nil {
			t.Fatalf("gating invoice status: %v", err)
		}
		return st
	}
	if got := status(); got != "open" {
		t.Fatalf("gating invoice status: got = %s, want = open", got)
	}
	wantCents(t, "credit note while gated", s.api(http.MethodGet, "/v1/credit_notes/"+cn, nil).at("credit_note"), map[string]int64{"balance_amount": 0})
	wantCents(t, "wallet while gated", s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).at("wallet"), map[string]int64{"balance_amount": 0})
	if got := s.appliedCoupon(applied).str("status"); got != "COUPON_STATUS_TERMINATED" {
		t.Errorf("applied coupon while gated: got = %s, want = COUPON_STATUS_TERMINATED", got)
	}

	payload, sig := f.event("payment_intent.payment_failed", f.lastIntent(), s.clock.Now())
	s.webhook(conn, payload, sig)
	s.idle()

	// The API serves current subscriptions only (subscriptions_controller.rb:136).
	var subStatus, reason string
	if err := s.pool.QueryRow(t.Context(), `SELECT status, coalesce(cancellation_reason, '') FROM subscriptions WHERE organization_id = $1 AND external_id = 'gated'`, s.org).Scan(&subStatus, &reason); err != nil {
		t.Fatalf("subscription: %v", err)
	}
	if subStatus != "canceled" || reason != "payment_failed" {
		t.Errorf("subscription status, reason: got = %s, %s, want = canceled, payment_failed", subStatus, reason)
	}
	if got := status(); got != "closed" {
		t.Errorf("gating invoice status: got = %s, want = closed", got)
	}
	note := s.api(http.MethodGet, "/v1/credit_notes/"+cn, nil).at("credit_note")
	wantCents(t, "credit note after the failure", note, map[string]int64{"balance_amount": 10})
	if got := note.str("credit_status"); got != "CREDIT_STATUS_AVAILABLE" {
		t.Errorf("credit note credit status: got = %s, want = CREDIT_STATUS_AVAILABLE", got)
	}
	wantCents(t, "wallet after the failure", s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).at("wallet"), map[string]int64{"balance_amount": 10})
	ac := s.appliedCoupon(applied)
	if got := ac.str("status"); got != "COUPON_STATUS_ACTIVE" {
		t.Errorf("applied coupon after the failure: got = %s, want = COUPON_STATUS_ACTIVE", got)
	}
	s.noFailedJobs()
}
