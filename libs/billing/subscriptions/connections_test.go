package subscriptions_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var stripeUS = map[string]kernel.ConnectionChoice{"payment": {Code: "stripe_us"}}

// stripeConnection links c to a Stripe integration with code stripe_us.
func (f *fixture) stripeConnection(c *customer.Customer) core.PaymentProviderCustomerID {
	f.T.Helper()
	return f.PaymentConnection(c, f.Integration("stripe_us", integration.ProviderStripe), true).ID
}

// pinned returns the payment connection a subscription chose, zero for
// none, failing unless it has at most that one choice.
func (f *fixture) pinned(id core.SubscriptionID) core.PaymentProviderCustomerID {
	f.T.Helper()
	rows := f.ObjectConnections(integration.ObjectOwner{SubscriptionID: id})
	switch {
	case len(rows) == 0:
		return core.PaymentProviderCustomerID{}
	case len(rows) > 1 || rows[0].Category != integration.CategoryPayment || rows[0].Behavior != integration.ConnectionBehaviorSpecific:
		f.T.Fatalf("connections of %s got = %+v, want = one specific payment connection", id, rows)
	}
	return rows[0].PaymentProviderCustomerID
}

func TestCreateConnections(t *testing.T) {
	t.Run("pins the connection", func(t *testing.T) {
		t.Log("source: spec/services/subscriptions/create_service_spec.rb:2241")
		f := newFixture(t)
		c := f.Customer()
		pc := f.stripeConnection(c)
		v, err := f.create(c, f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) { in.Connections = stripeUS })
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got := f.pinned(v.Subscription.ID); got != pc {
			t.Errorf("pinned got = %s, want = %s", got, pc)
		}
		want := []kernel.ConnectionRoute{
			{Category: integration.CategoryPayment, Behavior: integration.ConnectionBehaviorSpecific, Code: "stripe_us"},
			{Category: integration.CategoryTax, Behavior: integration.ConnectionBehaviorInherit},
			{Category: integration.CategoryAccounting, Behavior: integration.ConnectionBehaviorInherit},
			{Category: integration.CategoryCRM, Behavior: integration.ConnectionBehaviorInherit},
		}
		if !slices.Equal(v.Connections, want) {
			t.Errorf("View.Connections got = %v, want = %v", v.Connections, want)
		}
	})

	t.Run("unresolved code", func(t *testing.T) {
		t.Log("source: spec/services/subscriptions/create_service_spec.rb:2252")
		f := newFixture(t)
		c := f.Customer()
		f.stripeConnection(c)
		_, err := f.create(c, f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) {
			in.Connections = map[string]kernel.ConnectionChoice{"payment": {Code: "nope"}}
		})
		if got := fieldCodes(t, err); !slices.Equal(got, []string{"connections: connection_not_found"}) {
			t.Fatalf("Create() codes got = %v, want = [connections: connection_not_found]", got)
		}
		subs := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
			return r.Subscriptions.ListByCustomer(ctx, f.OrgID(), c.ID)
		})
		if len(subs) != 0 {
			t.Errorf("subscriptions got = %d, want = 0", len(subs))
		}
	})

	t.Run("invalid choice", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/subscriptions_controller_spec.rb:923")
		f := newFixture(t)
		_, err := f.create(f.Customer(), f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) {
			in.Connections = map[string]kernel.ConnectionChoice{"payment": {Behavior: "nonsense"}}
		})
		if got := fieldCodes(t, err); !slices.Equal(got, []string{"connections: invalid_connection_behavior"}) {
			t.Errorf("Create() codes got = %v, want = [connections: invalid_connection_behavior]", got)
		}
	})

	for _, tt := range []struct {
		name, source string
		future       bool
		amount       int64 // of the new plan; the current one is at 500
		inPlace      bool
		next         bool // the new plan's subscription is a pending downgrade
	}{
		{name: "upgrade", source: "spec/services/subscriptions/create_service_spec.rb:2272", amount: 5000},
		{name: "downgrade", source: "spec/services/subscriptions/create_service_spec.rb:2289", amount: 50, next: true},
		{name: "upgrade before the start", source: "spec/services/subscriptions/create_service_spec.rb:2314", future: true, amount: 5000, inPlace: true},
		{name: "downgrade before the start", source: "spec/services/subscriptions/create_service_spec.rb:2331", future: true, amount: 50, inPlace: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newBilledFixture(t)
			c := f.Customer()
			pc := f.stripeConnection(c)
			old := f.Plan(period.Monthly, 500)
			cur, err := f.create(c, old, func(in *subscriptions.CreateInput) {
				if tt.future {
					in.SubscriptionAt = new(now.AddDate(0, 0, 10))
				}
			})
			if err != nil {
				t.Fatalf("Create() current error = %v", err)
			}
			v, err := f.create(c, f.Plan(period.Monthly, tt.amount), func(in *subscriptions.CreateInput) { in.Connections = stripeUS })
			if err != nil {
				t.Fatalf("Create() plan change error = %v", err)
			}
			got := v.Subscription
			if (got.ID == cur.Subscription.ID) != tt.inPlace {
				t.Fatalf("changed in place got = %v, want = %v", got.ID == cur.Subscription.ID, tt.inPlace)
			}
			if tt.next && (got.Status != subscription.StatusPending || got.PreviousSubscriptionID != cur.Subscription.ID) {
				t.Fatalf("downgrade got = %s after %s, want = pending after %s", got.Status, got.PreviousSubscriptionID, cur.Subscription.ID)
			}
			if p := f.pinned(got.ID); p != pc {
				t.Errorf("pinned on the new plan's subscription got = %s, want = %s", p, pc)
			}
			if !tt.inPlace {
				if p := f.pinned(cur.Subscription.ID); !p.IsZero() {
					t.Errorf("pinned on the current subscription got = %s, want = none", p)
				}
			}
			subs := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
				return r.Subscriptions.ListByCustomer(ctx, f.OrgID(), c.ID)
			})
			if want := map[bool]int{true: 1, false: 2}[tt.inPlace]; len(subs) != want {
				t.Errorf("subscriptions got = %d, want = %d", len(subs), want)
			}
		})
	}
}

func TestChangePlanConnections(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		amount       int64
	}{
		{"upgrade", "spec/services/subscriptions/create_service_spec.rb:2280", 5000},
		{"downgrade", "spec/services/subscriptions/create_service_spec.rb:2296-2311", 50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newBilledFixture(t)
			c := f.Customer()
			pc := f.stripeConnection(c)
			cur := f.Subscription(c, f.Plan(period.Monthly, 500))
			p := f.Plan(period.Monthly, tt.amount)
			out, err := f.svc.ChangePlan(f.Ctx, subscriptions.ChangePlanInput{
				OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: cur.ExternalID},
				Plan: refs.ByCode(p.Code), Connections: stripeUS,
			})
			if err != nil {
				t.Fatalf("ChangePlan() error = %v", err)
			}
			if got := f.pinned(out.Subscription.Subscription.ID); got != pc {
				t.Errorf("pinned on the new plan's subscription got = %s, want = %s", got, pc)
			}
			if got := f.pinned(cur.ID); !got.IsZero() {
				t.Errorf("pinned on the current subscription got = %s, want = none", got)
			}
		})
	}
	t.Run("invalid choice", func(t *testing.T) {
		f := newFixture(t)
		c := f.Customer()
		cur := f.Subscription(c, f.Plan(period.Monthly, 500))
		p := f.Plan(period.Monthly, 5000)
		_, err := f.svc.ChangePlan(f.Ctx, subscriptions.ChangePlanInput{
			OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: cur.ExternalID},
			Plan: refs.ByCode(p.Code), Connections: map[string]kernel.ConnectionChoice{"shipping": {Behavior: "skip"}},
		})
		if got := fieldCodes(t, err); !slices.Equal(got, []string{"connections: invalid_connection_category"}) {
			t.Errorf("ChangePlan() codes got = %v, want = [connections: invalid_connection_category]", got)
		}
	})
}

// The custom sections of a downgrade go on the pending subscription.
//
// source: spec/services/subscriptions/plan_downgrade_service_spec.rb:83-101
func TestCreateDowngradeCustomSections(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	cur := f.Subscription(c, f.Plan(period.Monthly, 500))
	sec := f.customSection("section_code_1")
	v, err := f.create(c, f.Plan(period.Monthly, 50), func(in *subscriptions.CreateInput) {
		in.ExternalID = cur.ExternalID
		in.InvoiceCustomSections = &subscriptions.CustomSections{Sections: []refs.Ref{refs.ByCode(sec.Code)}}
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	next := f.reload(v.Subscription.ID)
	if next.Status != subscription.StatusPending || !slices.Equal(next.InvoiceCustomSectionIDs, []core.InvoiceCustomSectionID{sec.ID}) {
		t.Errorf("pending downgrade got = %s with %v, want = pending with [%s]", next.Status, next.InvoiceCustomSectionIDs, sec.ID)
	}
}

func TestUpdateConnections(t *testing.T) {
	update := func(f *fixture, s *subscription.Subscription, cs map[string]kernel.ConnectionChoice) (*subscriptions.View, error) {
		return f.svc.Update(f.Ctx, subscriptions.UpdateInput{
			OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: s.ExternalID}, Connections: cs,
		})
	}
	t.Run("pins the connection", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/subscriptions_controller_spec.rb:2008")
		f := newFixture(t)
		c := f.Customer()
		pc := f.stripeConnection(c)
		s := f.Subscription(c, f.Plan(period.Monthly, 1000))
		v, err := update(f, s, stripeUS)
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got := f.pinned(s.ID); got != pc {
			t.Errorf("pinned got = %s, want = %s", got, pc)
		}
		if got := v.Connections[0]; got.Behavior != integration.ConnectionBehaviorSpecific || got.Code != "stripe_us" {
			t.Errorf("View.Connections[0] got = %v, want = specific stripe_us", got)
		}
	})
	t.Run("inherit clears the choice", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/subscriptions_controller_spec.rb:2015")
		f := newFixture(t)
		c := f.Customer()
		f.stripeConnection(c)
		s := f.Subscription(c, f.Plan(period.Monthly, 1000))
		if _, err := update(f, s, map[string]kernel.ConnectionChoice{"payment": {Behavior: "skip"}}); err != nil {
			t.Fatalf("Update() skip error = %v", err)
		}
		v, err := update(f, s, map[string]kernel.ConnectionChoice{"payment": {Behavior: "inherit"}})
		if err != nil {
			t.Fatalf("Update() inherit error = %v", err)
		}
		if rows := f.ObjectConnections(integration.ObjectOwner{SubscriptionID: s.ID}); len(rows) != 0 {
			t.Errorf("connections got = %d, want = 0", len(rows))
		}
		if got := v.Connections[0]; got.Behavior != integration.ConnectionBehaviorInherit || got.Code != "stripe_us" {
			t.Errorf("View.Connections[0] got = %v, want = inherit stripe_us", got)
		}
	})
	t.Run("invalid choice", func(t *testing.T) {
		f := newFixture(t)
		s := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
		_, err := update(f, s, map[string]kernel.ConnectionChoice{"payment": {Code: "stripe_us", Behavior: "skip"}})
		if got := fieldCodes(t, err); !slices.Equal(got, []string{"connections: invalid_connection_choice"}) {
			t.Errorf("Update() codes got = %v, want = [connections: invalid_connection_choice]", got)
		}
	})
}

// nopBiller bills nothing.
type nopBiller struct{}

func (nopBiller) Bill(context.Context, port.Repos, invoicing.BillRequest) ([]*invoice.Invoice, error) {
	return nil, nil
}

// newBilledFixture is newFixture with a lifecycle that bills, for plan
// changes that terminate a subscription.
func newBilledFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	life := lifecycle.New(h.Deps).WithBiller(nopBiller{})
	return &fixture{Harness: h, svc: subscriptions.New(h.Deps, life, usage.New(h.Deps), plans.New(h.Deps, life, nil))}
}

// customSection creates an invoice custom section with code.
func (f *fixture) customSection(code string) *invoice.CustomSection {
	f.T.Helper()
	sec := &invoice.CustomSection{ID: core.NewID[core.InvoiceCustomSectionID](f.IDs), OrganizationID: f.OrgID(), Code: code, Name: code, CreatedAt: now, UpdatedAt: now}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, sec) })
	return sec
}
