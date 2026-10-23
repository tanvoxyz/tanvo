package lifecycle_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// build builds an unsaved subscription of the customer to the plan.
func (f *fixture) build(c *customer.Customer, p *catalog.Plan, externalID string, at time.Time) *subscription.Subscription {
	s := subscription.New(f.OrgID(), core.NewID[core.SubscriptionID](f.IDs), c.ID, p.ID, externalID, at,
		subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, f.Now())
	f.names[s.ID] = "sub"
	return s
}

func TestCreateByDate(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		at        time.Time
		plan      []func(*catalog.Plan)
		wantState subscription.Status
		want      []string
		wantHooks []webhook.EventType
	}{
		{
			name: "today, in-advance plan", source: "spec/services/subscriptions/create_service_spec.rb:720",
			at: now, plan: []func(*catalog.Plan){inAdvance}, wantState: subscription.StatusActive,
			want: []string{"2026-03-10 12:00:00 skip sub:subscription_starting"}, wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "earlier today", source: "spec/services/subscriptions/create_service_spec.rb:1025",
			at: now.Add(-3 * time.Hour), plan: []func(*catalog.Plan){inAdvance}, wantState: subscription.StatusActive,
			want: []string{"2026-03-10 09:00:00 skip sub:subscription_starting"}, wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "today with a trial", source: "spec/services/subscriptions/create_service_spec.rb:734",
			at: now, plan: []func(*catalog.Plan){inAdvance, trial(7)}, wantState: subscription.StatusActive,
			wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
		{
			name: "in the future", source: "spec/services/subscriptions/create_service_spec.rb:726",
			at: now.AddDate(0, 0, 3), plan: []func(*catalog.Plan){inAdvance}, wantState: subscription.StatusPending,
		},
		{
			name: "in the past is back-dated without a starting invoice", source: "spec/services/subscriptions/create_service_spec.rb:893",
			at: now.AddDate(0, 0, -3), plan: []func(*catalog.Plan){inAdvance}, wantState: subscription.StatusActive,
			wantHooks: hooks(webhook.EventTypeSubscriptionStarted),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			sub := f.build(f.Customer(), f.Plan(period.Monthly, 1000, tc.plan...), "ext", tc.at)
			got, err := f.svc.Create(f.Ctx, sub)
			must(t, err)
			stored := f.reload(got.ID)
			check(t, "status", stored.Status, tc.wantState)
			if tc.wantState == subscription.StatusActive {
				check(t, "started_at", stamp(*stored.StartedAt), stamp(tc.at))
			}
			checkSlice(t, "bills", f.bills(), tc.want)
			checkSlice(t, "webhooks", f.Webhooks(), tc.wantHooks)
		})
	}
}

func TestCreateBackdatedAfterTermination(t *testing.T) {
	terminatedAt := now.AddDate(0, 0, -2)
	tests := []struct {
		name   string
		source string
		set    func(*subscription.Subscription)
		want   time.Time
	}{
		{
			name: "starts at the invoiced termination", source: "spec/services/subscriptions/create_service_spec.rb:925",
			want: terminatedAt,
		},
		{
			name: "skipped termination invoice keeps the date", source: "spec/services/subscriptions/create_service_spec.rb:980",
			set:  func(s *subscription.Subscription) { s.OnTerminationInvoice = subscription.TerminationInvoiceSkip },
			want: now.AddDate(0, 0, -5),
		},
		{
			name: "other external ID keeps the date", source: "spec/services/subscriptions/create_service_spec.rb:1002",
			set:  func(s *subscription.Subscription) { s.ExternalID = "other" },
			want: now.AddDate(0, 0, -5),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			c := f.Customer()
			p := f.Plan(period.Monthly, 1000)
			old := f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, -1, 0)), func(s *subscription.Subscription) { s.ExternalID = "ext" })
			f.UpdateSubscription(old, func(s *subscription.Subscription) {
				must(t, s.Terminate(terminatedAt))
				if tc.set != nil {
					tc.set(s)
				}
			})
			got, err := f.svc.Create(f.Ctx, f.build(c, p, "ext", now.AddDate(0, 0, -5)))
			must(t, err)
			check(t, "started_at", stamp(*f.reload(got.ID).StartedAt), stamp(tc.want))
		})
	}
}

// source: spec/services/subscriptions/create_service_spec.rb:1127
func TestCreateSamePlanReturnsCurrent(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer()
	p := f.Plan(period.Monthly, 1000)
	cur := f.Subscription(c, p, func(s *subscription.Subscription) { s.ExternalID = "ext" })
	got, err := f.svc.Create(f.Ctx, f.build(c, p, "ext", now))
	must(t, err)
	check(t, "id", got.ID, cur.ID)
	checkSlice(t, "webhooks", f.Webhooks(), nil)
}

func TestCreateChangesPlan(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		amount    int64
		wantState subscription.Status
		wantOld   subscription.Status
	}{
		{
			name: "upgrade", source: "spec/services/subscriptions/create_service_spec.rb:1161", amount: 2000,
			wantState: subscription.StatusActive, wantOld: subscription.StatusTerminated,
		},
		{
			name: "downgrade", source: "spec/services/subscriptions/create_service_spec.rb:1453", amount: 10,
			wantState: subscription.StatusPending, wantOld: subscription.StatusActive,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			c := f.Customer()
			cur := f.Subscription(c, f.Plan(period.Monthly, 1000), func(s *subscription.Subscription) { s.ExternalID = "ext" })
			name := "renamed"
			sub := f.build(c, f.Plan(period.Monthly, tc.amount), "ext", now)
			sub.Name = &name
			got, err := f.svc.Create(f.Ctx, sub)
			must(t, err)
			stored := f.reload(got.ID)
			check(t, "status", stored.Status, tc.wantState)
			check(t, "previous", stored.PreviousSubscriptionID, cur.ID)
			check(t, "name", *stored.Name, name)
			check(t, "old status", f.reload(cur.ID).Status, tc.wantOld)
		})
	}
}

func TestCreateRefused(t *testing.T) {
	tests := []struct {
		name   string
		source string
		setup  func(f *fixture, c *customer.Customer, p *catalog.Plan)
		rule   string
		field  string
	}{
		{
			name: "incomplete subscription with the external ID", source: "spec/services/subscriptions/create_service_spec.rb:1735",
			setup: func(f *fixture, c *customer.Customer, p *catalog.Plan) {
				s := f.Subscription(c, p, billingtest.SubscribedAt(now.Add(time.Hour)), func(s *subscription.Subscription) { s.ExternalID = "ext" })
				f.UpdateSubscription(s, func(s *subscription.Subscription) { must(f.T, s.MarkIncomplete(now)) })
			},
			rule: lifecycle.CodeSubscriptionIncomplete,
		},
		{
			name: "external ID active for another customer", source: "spec/services/subscriptions/create_service_spec.rb:1751",
			setup: func(f *fixture, _ *customer.Customer, p *catalog.Plan) {
				f.Subscription(f.Customer(), p, func(s *subscription.Subscription) { s.ExternalID = "ext" })
			},
			field: "external_id",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, now)
			c := f.Customer()
			p := f.Plan(period.Monthly, 1000)
			tc.setup(f, c, p)
			_, err := f.svc.Create(f.Ctx, f.build(c, p, "ext", now))
			if tc.rule != "" {
				checkRule(t, err, tc.rule)
				return
			}
			var ve *core.ValidationError
			if !asValidation(err, &ve) || !ve.Has(tc.field, lifecycle.CodeValueAlreadyExist) {
				t.Fatalf("error: got = %v, want = %s %s", err, tc.field, lifecycle.CodeValueAlreadyExist)
			}
		})
	}
}

// The customer's currency is a default: a plan in another currency is
// subscribed and the customer keeps its currency.
//
// source: spec/services/subscriptions/create_service_spec.rb:1151
func TestCreateAcceptsPlanInAnotherCurrency(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer(func(c *customer.Customer) { c.Currency = money.MustCurrency("EUR") })
	p := f.Plan(period.Monthly, 1000)
	sub, err := f.svc.Create(f.Ctx, f.build(c, p, "ext", now))
	must(t, err)
	check(t, "status", sub.Status, subscription.StatusActive)
	check(t, "customer currency", f.ReloadCustomer(c.ID).Currency.Code(), "EUR")
}

// source: app/services/customers/update_currency_service.rb
func TestCreateSetsCustomerCurrency(t *testing.T) {
	f := newFixture(t, now)
	c := f.Customer(func(c *customer.Customer) { c.Currency = money.Currency{} })
	p := f.Plan(period.Monthly, 1000)
	_, err := f.svc.Create(f.Ctx, f.build(c, p, "ext", now))
	must(t, err)
	check(t, "currency", f.ReloadCustomer(c.ID).Currency.Code(), p.Currency.Code())
}
