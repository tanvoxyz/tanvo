package invoicing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// previewDay is the clock of the preview tests (lago's 30 Mar 2024).
var previewDay = billingtest.Date(2024, time.March, 30)

// source: spec/services/invoices/preview_service_spec.rb:164
func TestPreviewNewSubscription(t *testing.T) {
	f := newFixture(t, previewDay, func(p *catalog.Plan) { p.Amount = 3100 })
	f.taxCustomer("50")
	p, err := f.svc.Preview(f.Ctx, invoicing.PreviewInput{
		OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
		New: []*subscription.Subscription{{PlanID: f.plan.ID, SubscriptionAt: previewDay, BillingTime: period.Calendar, ConsolidateInvoice: true}},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Two days are billed, 30 and 31 March.
	checkAmounts(t, p.Invoice, 200, 100, 300)
	if len(p.Fees) != 1 || len(p.Subscriptions) != 1 {
		t.Errorf("fees, subscriptions = %d, %d, want = 1, 1", len(p.Fees), len(p.Subscriptions))
	}
	if got, want := p.Invoice.IssuingDate, (core.Date{Year: 2024, Month: time.April, Day: 1}); got != want {
		t.Errorf("issuing date = %v, want = %v", got, want)
	}
	if got := len(f.Invoices(f.cust.ID)); got != 0 {
		t.Errorf("stored invoices = %d, want = 0", got)
	}
	if got := len(f.JobKinds()); got != 0 || len(f.Webhooks()) != 0 {
		t.Errorf("jobs, webhooks = %v, %v, want = none", f.JobKinds(), f.Webhooks())
	}
}

// source: spec/services/invoices/preview_service_spec.rb:394
func TestPreviewPersistedSubscription(t *testing.T) {
	f := newFixture(t, previewDay, func(p *catalog.Plan) { p.Amount = 3100 })
	f.taxCustomer("50")
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(previewDay))
	f.Event(sub, "api_calls", previewDay.Add(time.Hour))
	p, err := f.svc.Preview(f.Ctx, invoicing.PreviewInput{OrganizationID: f.OrgID(), CustomerID: f.cust.ID, SubscriptionIDs: []core.SubscriptionID{sub.ID}})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// source: spec/services/invoices/preview_service_spec.rb:445
	checkAmounts(t, p.Invoice, 300, 150, 450)
	if got := len(f.Invoices(f.cust.ID)); got != 0 {
		t.Errorf("stored invoices = %d, want = 0", got)
	}
}

// source: spec/services/invoices/preview_service_spec.rb:54
func TestPreviewRejections(t *testing.T) {
	tests := []struct {
		name string
		code string
		in   func(f *fixture) invoicing.PreviewInput
	}{
		// source: spec/services/invoices/preview_service_spec.rb:57
		{"no subscriptions", invoicing.CodeNoSubscriptions, func(f *fixture) invoicing.PreviewInput {
			return invoicing.PreviewInput{OrganizationID: f.OrgID(), CustomerID: f.cust.ID}
		}},
		// source: spec/services/invoices/preview_service_spec.rb:155
		{"billing periods differ", invoicing.CodePreviewBillingPeriods, func(f *fixture) invoicing.PreviewInput {
			return invoicing.PreviewInput{OrganizationID: f.OrgID(), CustomerID: f.cust.ID, New: []*subscription.Subscription{
				{PlanID: f.plan.ID, SubscriptionAt: previewDay, BillingTime: period.Calendar},
				{PlanID: f.plan.ID, SubscriptionAt: previewDay, BillingTime: period.Anniversary},
			}}
		}},
		{"currencies differ", invoicing.CodePreviewCurrencies, func(f *fixture) invoicing.PreviewInput {
			eur := f.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Currency = money.MustCurrency("EUR") })
			return invoicing.PreviewInput{OrganizationID: f.OrgID(), CustomerID: f.cust.ID, New: []*subscription.Subscription{
				{PlanID: f.plan.ID, SubscriptionAt: previewDay}, {PlanID: eur.ID, SubscriptionAt: previewDay},
			}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, previewDay)
			_, err := f.svc.Preview(f.Ctx, tt.in(f))
			var re *core.RuleError
			if !errors.As(err, &re) || re.Code != tt.code {
				t.Fatalf("err = %v, want = %s", err, tt.code)
			}
		})
	}
}
