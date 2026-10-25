package cli_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// date returns the google.type.Date at path as YYYY-MM-DD.
func (j jsonValue) date(path string) string {
	d := j.at(path)
	n := func(k string) int {
		v, _ := d.get(k).(float64)
		return int(v)
	}
	return fmt.Sprintf("%04d-%02d-%02d", n("year"), n("month"), n("day"))
}

// TestScenarioDunning retries the overdue balance of a customer whose
// card Stripe declines: the dunning campaign requests the payment of the
// overdue invoices once their total reaches its threshold, emails the
// customer, and stops after its last attempt.
//
// source: spec/scenarios/dunning/dunning_campaign_v1_spec.rb:164
func TestScenarioDunning(t *testing.T) {
	t.Parallel()
	smtp := newFakeSMTP(t)
	s, f, conn := stripeScenario(t, time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), map[string]any{
		"external_id": "cust", "name": "Customer", "email": "billing@customer.test", "net_payment_term_days": 2, "currency": "EUR",
	}, smtp.environ()...)
	f.mu.Lock()
	f.decline = true
	f.mu.Unlock()
	campaign := s.admin(http.MethodPost, "/v1/dunning_campaigns", map[string]any{
		"code": "dunning", "name": "Dunning", "days_between_attempts": 2, "max_attempts": 2,
		"thresholds": []map[string]any{{"amount": money("150")}},
	}).str("dunning_campaign.id")
	entity := s.api(http.MethodGet, "/v1/billing_entities", nil).str("billing_entities.0.id")
	s.api(http.MethodPatch, "/v1/billing_entities/"+entity, map[string]any{
		"name": "ACME Corp", "email": "billing@acme.test", "applied_dunning_campaign_id": campaign,
	})

	// failIntents declines the new payment intents with the
	// payment_intent.payment_failed webhook, as lago's
	// MockStripeWebhookEventJob does after each intent.
	seen := 0
	failIntents := func() {
		t.Helper()
		for ; seen < f.intentsCount(); seen++ {
			f.mu.Lock()
			id := f.order[seen]
			f.mu.Unlock()
			payload, sig := f.event("payment_intent.payment_failed", id, s.clock.Now())
			s.webhook(conn, payload, sig)
		}
		s.idle()
	}
	overdueAndDun := func(at time.Time) {
		t.Helper()
		s.at(at)
		s.trigger(jobs.KindMarkInvoicesPaymentOverdue)
		s.idle()
		s.trigger(jobs.KindProcessDunningCampaigns)
		s.idle()
		failIntents()
	}
	const subject = "Your overdue balance from ACME Corp"
	dunningEmails := func() int {
		n := 0
		for _, m := range smtp.messages() {
			if m.Subject() == subject {
				n++
			}
		}
		return n
	}
	paymentRequests := func() []jsonValue {
		return s.api(http.MethodGet, "/v1/customers/external_id%3Acust/payment_requests?page_size=100", nil).list("payment_requests")
	}

	s.plan("plan", "INTERVAL_MONTHLY", "149", true)
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
		"external_id": "sub_overdue-dunning-campaign-v1", "customer_id": "external_id:cust", "plan_id": "code:plan",
	})
	s.idle()
	s.bill(s.clock.Now())
	failIntents()
	// The webhooks of the same instant have no order here.
	for _, w := range []string{"subscription.started", "invoice.created", "invoice.payment_failure"} {
		if !slices.Contains(s.webhookTypes(), w) {
			t.Errorf("webhooks: got = %v, want = containing %s", s.webhookTypes(), w)
		}
	}
	invs := s.waitInvoices("cust", 1)
	inv := invs[0]
	if got := inv.str("payment_status"); got != "PAYMENT_STATUS_FAILED" {
		t.Errorf("invoice payment status: got = %s, want = PAYMENT_STATUS_FAILED", got)
	}
	if got := inv.date("payment_due_date"); got != "2025-01-03" {
		t.Errorf("invoice payment due date: got = %s, want = 2025-01-03", got)
	}

	// The day after the due date the invoice is overdue, below the
	// threshold.
	overdueAndDun(time.Date(2025, 1, 4, 13, 0, 0, 0, time.UTC))
	if got := s.api(http.MethodGet, "/v1/invoices/"+inv.str("id"), nil).str("invoice.payment_overdue"); got != "true" {
		t.Errorf("invoice payment overdue: got = %s, want = true", got)
	}
	if n := len(paymentRequests()); n != 0 {
		t.Errorf("payment requests below the threshold: got = %d, want = 0", n)
	}

	// A one-off invoice reaches the threshold.
	s.at(time.Date(2025, 1, 4, 18, 0, 0, 0, time.UTC))
	s.api(http.MethodPost, "/v1/add_ons", map[string]any{"name": "Add-on", "code": "addon", "amount": money("2")})
	oneOff := s.api(http.MethodPost, "/v1/invoices", map[string]any{
		"customer_id": "external_id:cust", "currency": "EUR", "fees": []map[string]any{{"add_on_id": "code:addon", "units": "3"}},
	}).str("invoice.id")
	s.idle()
	failIntents()
	oo := s.api(http.MethodGet, "/v1/invoices/"+oneOff, nil).at("invoice")
	if got := oo.str("payment_status"); got != "PAYMENT_STATUS_FAILED" {
		t.Errorf("one-off invoice payment status: got = %s, want = PAYMENT_STATUS_FAILED", got)
	}
	if got := oo.date("payment_due_date"); got != "2025-01-06" {
		t.Errorf("one-off invoice payment due date: got = %s, want = 2025-01-06", got)
	}

	if n := dunningEmails(); n != 0 {
		t.Errorf("dunning emails before the first attempt: got = %d, want = 0", n)
	}
	overdueAndDun(time.Date(2025, 1, 7, 10, 0, 0, 0, time.UTC))
	if got := s.api(http.MethodGet, "/v1/invoices/"+oneOff, nil).str("invoice.payment_overdue"); got != "true" {
		t.Errorf("one-off invoice payment overdue: got = %s, want = true", got)
	}
	// One email when Stripe declines the payment, one when its webhook
	// arrives.
	if n := dunningEmails(); n != 2 {
		t.Errorf("dunning emails after the first attempt: got = %d, want = 2", n)
	}
	prs := paymentRequests()
	if len(prs) != 1 || prs[0].cents("amount") != 15_500 {
		t.Errorf("payment requests: got = %v, want = one of 155.00", prs)
	}

	for _, at := range []time.Time{time.Date(2025, 1, 8, 10, 0, 0, 0, time.UTC), time.Date(2025, 1, 9, 10, 0, 0, 0, time.UTC)} {
		overdueAndDun(at)
		if n := dunningEmails(); n != 2 {
			t.Errorf("dunning emails on %s: got = %d, want = 2", at.Format(time.DateOnly), n)
		}
	}
	overdueAndDun(time.Date(2025, 1, 10, 10, 0, 0, 0, time.UTC))
	if n := dunningEmails(); n != 4 {
		t.Errorf("dunning emails after the second attempt: got = %d, want = 4", n)
	}
	prs = paymentRequests()
	for _, pr := range prs {
		if got := pr.cents("amount"); got != 15_500 {
			t.Errorf("payment request amount: got = %d, want = 15500", got)
		}
	}
	if len(prs) != 2 {
		t.Errorf("payment requests: got = %d, want = 2", len(prs))
	}

	// The campaign is over: the invoices stay overdue, without attempts.
	overdueAndDun(time.Date(2025, 1, 13, 13, 0, 0, 0, time.UTC))
	if n := dunningEmails(); n != 4 {
		t.Errorf("dunning emails after the last attempt: got = %d, want = 4", n)
	}
	for _, m := range smtp.messages() {
		if m.Subject() == subject && (len(m.To) != 1 || m.To[0] != "billing@customer.test") {
			t.Errorf("dunning email recipients: got = %v, want = [billing@customer.test]", m.To)
		}
	}
}
