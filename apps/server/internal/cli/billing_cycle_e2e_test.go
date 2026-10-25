package cli_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// TestScenarioBillingCycle bills a monthly plan in arrears with taxes on
// the plan and on each charge, a charge minimum with its true-up fee and
// a one-time coupon, all set up and read back through the REST API.
//
// source: spec/scenarios/taxes_on_invoice_spec.rb:14
func TestScenarioBillingCycle(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), scenarioOptions{})

	for _, tax := range []struct{ code, name, rate string }{
		{"banking_rates", "Banking rates", "10"}, {"sales_tax_fr", "Sales tax - FR", "0"}, {"sales_tax", "Sales tax", "20"},
	} {
		s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": tax.code, "name": tax.name, "rate": tax.rate})
	}
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "customer-1", "name": "Customer 1"})
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
		"name": "FX Transfers", "code": "fx_transfers", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "total",
	})
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{"name": "Cards", "code": "cards", "aggregation_type": "AGGREGATION_TYPE_COUNT"})
	plan := s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "P1", "code": "plan_code", "interval": "INTERVAL_MONTHLY", "amount": map[string]any{"currency": "EUR", "amount": "100"},
		"tax_ids": []string{"code:banking_rates"},
		"charges": []map[string]any{
			{
				"code": "fx_transfers", "billable_metric_id": "code:fx_transfers", "charge_model": "CHARGE_MODEL_STANDARD",
				"properties": map[string]any{"standard": map[string]any{"amount": "1"}}, "tax_ids": []string{"code:sales_tax_fr"},
			},
			{
				"code": "cards", "billable_metric_id": "code:cards", "charge_model": "CHARGE_MODEL_STANDARD", "min_amount": "50",
				"properties": map[string]any{"standard": map[string]any{"amount": "30"}}, "tax_ids": []string{"code:sales_tax"},
			},
		},
	})
	charges := map[string]string{}
	for _, c := range plan.list("plan.charges") {
		charges[c.str("id")] = c.str("code")
	}
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
		"external_id": "sub_external_id", "customer_id": "external_id:customer-1", "plan_id": "code:plan_code",
	})
	s.api(http.MethodPost, "/v1/coupons", map[string]any{
		"name": "coupon1", "code": "coupon1_code", "coupon_type": "COUPON_TYPE_FIXED_AMOUNT", "frequency": "COUPON_FREQUENCY_ONCE",
		"amount": map[string]any{"currency": "EUR", "amount": "10"}, "expiration": "COUPON_EXPIRATION_TIME_LIMIT",
		"expiration_at": s.clock.Now().Add(15 * 24 * time.Hour).Format(time.RFC3339), "reusable": false,
	})
	s.api(http.MethodPost, "/v1/applied_coupons", map[string]any{"customer_id": "external_id:customer-1", "coupon_id": "code:coupon1_code"})
	s.event("sub_external_id", "fx_transfers", "tx-1", map[string]any{"total": 50})
	s.event("sub_external_id", "cards", "tx-2", nil)

	s.at(time.Date(2023, 2, 1, 0, 0, 0, 0, time.UTC))
	s.trigger(jobs.KindBillSubscriptions)
	s.idle()

	invs := s.waitInvoices("customer-1", 1)
	if len(invs) != 1 {
		t.Fatalf("invoices: got = %d, want = 1", len(invs))
	}
	inv := s.api(http.MethodGet, "/v1/invoices/"+invs[0].str("id"), nil).at("invoice")
	// The fees: amount, taxes, taxes rate, coupon share (precise, in cents)
	// and units, keyed by
	// their type and charge (the true-up fee is the charge's second fee).
	got := map[string]string{}
	for _, f := range inv.list("fees") {
		key := "subscription"
		if f.str("fee_type") == "FEE_TYPE_CHARGE" {
			key = charges[f.str("charge_id")]
			if f.str("true_up_parent_fee_id") != "" {
				key += ":true_up"
			}
		}
		got[key] = fmt.Sprintf("%d/%d/%s/%s/%s", f.cents("amount"), f.cents("taxes_amount"), f.str("taxes_rate"), f.str("precise_coupons_amount"), f.str("units"))
	}
	want := map[string]string{
		"subscription":  "10000/950/10/500/1",
		"fx_transfers":  "5000/0/0/250/50",
		"cards":         "3000/570/20/150/1",
		"cards:true_up": "2000/380/20/100/1",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("fee %s (amount/taxes/rate/coupons/units): got = %s, want = %s", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("fees: got = %v, want = %v", got, want)
	}
	for path, want := range map[string]int64{
		"fees_amount": 20_000, "coupons_amount": 1000, "taxes_amount": 1900,
		"sub_total_excluding_taxes_amount": 19_000, "sub_total_including_taxes_amount": 20_900, "total_amount": 20_900,
	} {
		if got := inv.cents(path); got != want {
			t.Errorf("invoice %s: got = %d, want = %d", path, got, want)
		}
	}
	if st := inv.str("status"); st != "INVOICE_STATUS_FINALIZED" {
		t.Errorf("invoice status: got = %s, want = INVOICE_STATUS_FINALIZED", st)
	}
	s.noFailedJobs()
}

// TestScenarioPrepaidCredits bills in-advance charges as events arrive,
// taxed, and pays them from a wallet limited to two of the metrics.
//
// source: spec/scenarios/invoices/invoicing_with_prepaid_credits_spec.rb:30
func TestScenarioPrepaidCredits(t *testing.T) {
	t.Parallel()
	s := newScenario(t, time.Date(2024, 6, 3, 10, 0, 0, 0, time.UTC), scenarioOptions{})

	s.api(http.MethodPost, "/v1/taxes", map[string]any{"code": "tax_24", "name": "Tax", "rate": "24"})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "customer-1", "name": "Customer 1", "currency": "EUR"})
	for _, code := range []string{"count_1", "count_2"} {
		s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{"name": code, "code": code, "aggregation_type": "AGGREGATION_TYPE_COUNT"})
	}
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{"name": "Sum", "code": "sum_bm", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "item_id"})
	charge := func(code, amount string, taxed bool) map[string]any {
		c := map[string]any{
			"code": code, "billable_metric_id": "code:" + code, "charge_model": "CHARGE_MODEL_STANDARD", "pay_in_advance": true, "invoiceable": true,
			"properties": map[string]any{"standard": map[string]any{"amount": amount}},
		}
		if taxed {
			c["tax_ids"] = []string{"code:tax_24"}
		}
		return c
	}
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": map[string]any{"currency": "EUR", "amount": "0"},
		"charges": []map[string]any{charge("count_1", "14.29", true), charge("count_2", "14.27", true), charge("sum_bm", "10", false)},
	})
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "sub-1", "customer_id": "external_id:customer-1", "plan_id": "code:plan"})
	wallet := s.api(http.MethodPost, "/v1/wallets", map[string]any{
		"customer_id": "external_id:customer-1", "rate_amount": "1", "currency": "EUR", "granted_credits": "39.00",
		"invoice_requires_successful_payment": false, "billable_metric_ids": []string{"code:count_1", "code:count_2"},
	}).str("wallet.id")
	s.idle()
	if n := len(s.invoices("customer-1")); n != 0 {
		t.Fatalf("invoices before the events: got = %d, want = 0", n)
	}

	// An event of a metric the wallet does not pay for.
	s.event("sub-1", "sum_bm", "tx-1", map[string]any{"item_id": "1"})
	s.idle()
	invs := s.waitInvoices("customer-1", 1)
	if got := invs[0].cents("prepaid_credit_amount"); got != 0 {
		t.Errorf("first invoice prepaid credit: got = %d, want = 0", got)
	}

	// Events of the wallet's metrics.
	for i, tt := range []struct {
		code             string
		subTotal, wallet int64
	}{
		{"count_1", 1772, 2128},
		{"count_2", 1769, 359},
	} {
		s.event("sub-1", tt.code, fmt.Sprintf("tx-%d", i+2), nil)
		s.idle()
		invs := s.waitInvoices("customer-1", i+2)
		inv := invs[len(invs)-1]
		if got := inv.cents("total_amount"); got != 0 {
			t.Errorf("%s invoice total: got = %d, want = 0", tt.code, got)
		}
		if got := inv.cents("sub_total_including_taxes_amount"); got != tt.subTotal {
			t.Errorf("%s invoice sub total including taxes: got = %d, want = %d", tt.code, got, tt.subTotal)
		}
		if got := inv.cents("prepaid_credit_amount"); got != tt.subTotal {
			t.Errorf("%s invoice prepaid credit: got = %d, want = %d", tt.code, got, tt.subTotal)
		}
		w := s.api(http.MethodGet, "/v1/wallets/"+wallet, nil).at("wallet")
		if got := w.cents("balance_amount"); got != tt.wallet {
			t.Errorf("%s wallet balance: got = %d, want = %d", tt.code, got, tt.wallet)
		}
	}
	s.noFailedJobs()
}
