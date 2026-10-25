package cli_test

import (
	"net/http"
	"testing"
	"time"
)

// money is a Money JSON value in EUR.
func money(amount string) map[string]any { return map[string]any{"currency": "EUR", "amount": amount} }

// TestScenarioPartialCreditNote bills two in-advance subscriptions, one
// with a coupon limited to its plan and taxed twice, pays the invoice
// manually in two parts, estimates credit notes and refunds part of a fee.
//
// source: spec/scenarios/credit_notes/credit_note_spec.rb:56
func TestScenarioPartialCreditNote(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2022, 12, 19, 12, 0, 0, 0, time.UTC), scenarioOptions{})

	s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "tax", "name": "Tax", "rate": "10"})
	s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "plan_tax", "name": "Plan Tax", "rate": "10"})
	entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
	s.api(http.MethodPatch, "/v1/billing_entities/"+entity, map[string]any{"tax_ids": []string{"code:tax"}})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "plan1", "code": "plan1", "interval": "INTERVAL_MONTHLY", "pay_in_advance": true, "amount": money("179"),
	})
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "plan2", "code": "plan2", "interval": "INTERVAL_MONTHLY", "pay_in_advance": true, "amount": money("399"),
		"tax_ids": []string{"code:plan_tax", "code:tax"},
	})
	s.api(http.MethodPost, "/v1/coupons", map[string]any{
		"name": "coupon", "code": "coupon", "coupon_type": "COUPON_TYPE_FIXED_AMOUNT", "frequency": "COUPON_FREQUENCY_FOREVER",
		"amount": money("200"), "expiration": "COUPON_EXPIRATION_NO_EXPIRATION", "limited_plan_ids": []string{"code:plan2"},
	})
	for _, plan := range []string{"plan1", "plan2"} {
		s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
			"external_id": "cust_" + plan[len(plan)-1:], "customer_id": "external_id:cust", "plan_id": "code:" + plan,
			"billing_time": "BILLING_TIME_ANNIVERSARY",
		})
	}
	s.idle()
	s.at(time.Date(2023, 8, 29, 12, 0, 0, 0, time.UTC))
	s.api(http.MethodPost, "/v1/applied_coupons", map[string]any{
		"customer_id": "external_id:cust", "coupon_id": "code:coupon", "amount": money("250"),
	})
	before := len(s.invoices("cust"))
	s.bill(time.Date(2023, 10, 19, 12, 0, 0, 0, time.UTC))

	invs := s.waitInvoices("cust", before+1)
	inv := s.api(http.MethodGet, "/v1/invoices/"+invs[len(invs)-1].str("id"), nil).at("invoice")
	for path, want := range map[string]int64{
		"fees_amount": 57_800, "coupons_amount": 25_000, "taxes_amount": 4770, "total_amount": 37_570,
	} {
		if got := inv.cents(path); got != want {
			t.Errorf("invoice %s: got = %d, want = %d", path, got, want)
		}
	}
	if got := inv.str("taxes_rate"); got != "14.54268" {
		t.Errorf("invoice taxes_rate: got = %s, want = 14.54268", got)
	}
	var fee1, fee2 string
	for _, f := range inv.list("fees") {
		switch f.cents("amount") {
		case 17_900:
			fee1 = f.str("id")
			if got := f.str("precise_coupons_amount"); got != "0" && got != "" {
				t.Errorf("fee1 coupons: got = %s, want = 0", got)
			}
		case 39_900:
			fee2 = f.str("id")
			if got := f.str("precise_coupons_amount"); got != "25000" {
				t.Errorf("fee2 coupons: got = %s, want = 25000", got)
			}
		}
	}
	if fee1 == "" || fee2 == "" {
		t.Fatalf("fees: got = %v, want = 179.00 and 399.00", inv.at("fees"))
	}
	id := inv.str("id")

	s.at(time.Date(2023, 10, 23, 12, 0, 0, 0, time.UTC))
	s.api(http.MethodPost, "/v1/payments", map[string]any{"invoice_id": id, "amount": "12.00", "reference": "ref1"})
	s.idle()

	estimate := func(items []map[string]any, want map[string]int64, rate string) {
		t.Helper()
		e := s.api(http.MethodPost, "/v1/credit_notes:estimate", map[string]any{"invoice_id": id, "items": items})
		got := map[string]int64{
			"taxes": e.cents("credit_note.taxes_amount"), "sub_total": e.cents("credit_note.sub_total_excluding_taxes_amount"),
			"max_creditable": e.cents("max_creditable_amount"), "max_refundable": e.cents("max_refundable_amount"),
			"coupons_adjustment": e.cents("credit_note.coupons_adjustment_amount"),
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("estimate %s: got = %d, want = %d", k, got[k], w)
			}
		}
		if got := e.str("credit_note.taxes_rate"); got != rate {
			t.Errorf("estimate taxes_rate: got = %s, want = %s", got, rate)
		}
	}
	estimate([]map[string]any{{"fee_id": fee1, "amount": "179"}, {"fee_id": fee2, "amount": "399"}}, map[string]int64{
		"taxes": 4770, "sub_total": 32_800, "max_creditable": 37_570, "max_refundable": 1200, "coupons_adjustment": 25_000,
	}, "14.54268")
	estimate([]map[string]any{{"fee_id": fee2, "amount": "262.60"}}, map[string]int64{
		"taxes": 1961, "sub_total": 9806, "max_creditable": 11_767, "max_refundable": 1200, "coupons_adjustment": 16_454,
	}, "20.00000")

	s.api(http.MethodPost, "/v1/payments", map[string]any{"invoice_id": id, "amount": "105.67", "reference": "ref1"})
	s.idle()
	cn := s.api(http.MethodPost, "/v1/credit_notes", map[string]any{
		"invoice_id": id, "reason": "CREDIT_NOTE_REASON_OTHER", "credit_amount": money("0"), "refund_amount": money("117.67"),
		"items": []map[string]any{{"fee_id": fee2, "amount": "262.60"}},
	}).at("credit_note")
	for path, want := range map[string]int64{
		"sub_total_excluding_taxes_amount": 9806, "taxes_amount": 1961, "refund_amount": 11_767,
		"total_amount": 11_767, "coupons_adjustment_amount": 16_454,
	} {
		if got := cn.cents(path); got != want {
			t.Errorf("credit note %s: got = %d, want = %d", path, got, want)
		}
	}
	s.idle()
	s.noFailedJobs()
}
