package subscriptions_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

type fixture struct {
	*billingtest.Harness
	svc *subscriptions.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	life := lifecycle.New(h.Deps)
	return &fixture{Harness: h, svc: subscriptions.New(h.Deps, life, usage.New(h.Deps), plans.New(h.Deps, life, nil))}
}

func (f *fixture) create(c *customer.Customer, p *catalog.Plan, opts ...func(*subscriptions.CreateInput)) (*subscriptions.View, error) {
	in := subscriptions.CreateInput{
		OrganizationID: f.OrgID(), Customer: subscriptions.CustomerRef{ExternalID: c.ExternalID},
		Plan: refs.ByCode(p.Code), ExternalID: "sub_ext",
	}
	for _, o := range opts {
		o(&in)
	}
	return f.svc.Create(f.Ctx, in)
}

func (f *fixture) reload(id core.SubscriptionID) *subscription.Subscription {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, f.OrgID(), id)
	})
}

func (f *fixture) method(c *customer.Customer, isDefault bool) *payment.Method {
	f.T.Helper()
	id := core.NewID[core.PaymentMethodID](f.IDs)
	m := &payment.Method{
		ID: id, OrganizationID: f.OrgID(), CustomerID: c.ID,
		ProviderMethodID: "pm_" + id.String(), IsDefault: isDefault, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

func (f *fixture) providerCustomer(c *customer.Customer) {
	f.T.Helper()
	pc := &payment.ProviderCustomer{
		ID: core.NewID[core.PaymentProviderCustomerID](f.IDs), OrganizationID: f.OrgID(), CustomerID: c.ID,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Create(ctx, pc) })
}

func fieldCodes(t *testing.T, err error) []string {
	t.Helper()
	var out []string
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func notFound(err error) string {
	var nf *core.NotFoundError
	if errors.As(err, &nf) {
		return nf.Resource
	}
	return ""
}

func ruleCode(err error) string {
	var re *core.RuleError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// source: spec/services/subscriptions/create_service_spec.rb:33
func TestCreateToday(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	p := f.Plan(period.Monthly, 1000)
	v, err := f.create(c, p, func(in *subscriptions.CreateInput) {
		in.Name = new("  Main  ")
		in.EndingAt = new(now.AddDate(0, 1, 0))
		in.ConsolidateInvoice = new(false)
		in.PurchaseOrderNumber = new("PO-1")
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	s := v.Subscription
	if s.Status != subscription.StatusActive || *s.Name != "Main" || s.ConsolidateInvoice || *s.PurchaseOrderNumber != "PO-1" {
		t.Errorf("subscription = status %s name %q consolidate %v po %v, want active Main false PO-1", s.Status, *s.Name, s.ConsolidateInvoice, *s.PurchaseOrderNumber)
	}
	if s.BillingTime != period.Calendar {
		// source: spec/services/subscriptions/create_service_spec.rb:307
		t.Errorf("billing_time = %v, want calendar", s.BillingTime)
	}
	if v.CurrentPeriod == nil || !v.CurrentPeriod.From.Equal(now) || !v.CurrentPeriod.To.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("current period = %v, want [started_at, 2026-04-01)", v.CurrentPeriod)
	}
	if got, want := f.Webhooks(), []webhook.EventType{webhook.EventTypeSubscriptionStarted}; !slices.Equal(got, want) {
		t.Errorf("webhooks = %v, want %v", got, want)
	}
}

// source: spec/services/subscriptions/create_service_spec.rb:683, spec/requests/api/v1/subscriptions_controller_spec.rb:237
func TestCreateNewCustomer(t *testing.T) {
	f := newFixture(t)
	p := f.Plan(period.Monthly, 1000)
	v, err := f.create(&customer.Customer{ExternalID: " new_cust "}, p)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	c := v.Customer
	if c.ExternalID != "new_cust" || c.BillingEntityID != f.BillingEntity.ID || c.Currency != money.MustCurrency("USD") {
		t.Errorf("customer = %q %s %s, want new_cust of the default billing entity in USD", c.ExternalID, c.BillingEntityID, c.Currency)
	}
	if c.SequentialID == 0 {
		t.Errorf("sequential_id = 0, want assigned")
	}
	if got := f.Webhooks(); slices.Contains(got, webhook.EventTypeCustomerCreated) {
		t.Errorf("webhooks = %v, want no customer.created (create_service.rb:58)", got)
	}
}

func TestCreateNotFound(t *testing.T) {
	tests := []struct {
		name, source string
		set          func(f *fixture, in *subscriptions.CreateInput)
		want         string
	}{
		{
			name: "plan", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:386",
			set:  func(_ *fixture, in *subscriptions.CreateInput) { in.Plan = refs.ByCode("nope") },
			want: subscriptions.ResourcePlan,
		},
		{
			name: "override plan", source: "app/controllers/api/v1/subscriptions_controller.rb:73",
			set: func(f *fixture, in *subscriptions.CreateInput) {
				parent := f.Plan(period.Monthly, 1000)
				in.Plan = refs.ByID(f.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.ParentID = parent.ID }).ID)
			},
			want: subscriptions.ResourcePlan,
		},
		{
			name: "billing entity", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:275",
			set:  func(_ *fixture, in *subscriptions.CreateInput) { in.BillingEntity = refs.ByCode("nope") },
			want: subscriptions.ResourceBillingEntity,
		},
		{
			name: "customer by ID", source: "spec/services/subscriptions/create_service_spec.rb:707",
			set: func(f *fixture, in *subscriptions.CreateInput) {
				in.Customer = subscriptions.CustomerRef{ID: core.NewID[core.CustomerID](f.IDs)}
			},
			want: subscriptions.ResourceCustomer,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newFixture(t)
			_, err := f.create(f.Customer(), f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) { tt.set(f, in) })
			if got := notFound(err); got != tt.want {
				t.Errorf("not found = %q (%v), want %q", got, err, tt.want)
			}
		})
	}
}

func TestCreateValidation(t *testing.T) {
	manual, provider := core.PaymentMethodTypeManual, core.PaymentMethodTypeProvider
	payRule := []subscriptions.ActivationRule{{Type: subscription.ActivationRuleTypePayment, TimeoutHours: 48}}
	tests := []struct {
		name, source string
		setup        func(f *fixture, c *customer.Customer)
		set          func(f *fixture, c *customer.Customer, in *subscriptions.CreateInput)
		want         []string
	}{
		{
			name: "ending_at today", source: "app/services/subscriptions/validate_service.rb:53",
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.EndingAt = new(now.Add(time.Hour))
			},
			want: []string{"ending_at: invalid_date"},
		},
		{
			name: "ending_at before subscription_at", source: "app/services/subscriptions/validate_service.rb:59",
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.SubscriptionAt, in.EndingAt = new(now.AddDate(0, 0, 10)), new(now.AddDate(0, 0, 5))
			},
			want: []string{"ending_at: invalid_date"},
		},
		{
			name: "manual payment method with an ID", source: "app/services/payment_methods/validate_service.rb:20",
			set: func(f *fixture, c *customer.Customer, in *subscriptions.CreateInput) {
				in.PaymentMethod = &subscriptions.PaymentMethod{Type: &manual, ID: new(f.method(c, false).ID)}
			},
			want: []string{"payment_method: invalid_payment_method"},
		},
		{
			name: "provider payment method not found", source: "app/services/payment_methods/validate_service.rb:20",
			set: func(f *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.PaymentMethod = &subscriptions.PaymentMethod{Type: &provider, ID: new(core.NewID[core.PaymentMethodID](f.IDs))}
			},
			want: []string{"payment_method: invalid_payment_method"},
		},
		{
			name: "invalid rule type", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:797",
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.ActivationRules = []subscriptions.ActivationRule{{Type: "invalid"}}
			},
			want: []string{"activation_rules: invalid_type"},
		},
		{
			name: "negative timeout", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:817",
			setup: func(f *fixture, c *customer.Customer) { f.providerCustomer(c); f.method(c, true) },
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.ActivationRules = []subscriptions.ActivationRule{{Type: subscription.ActivationRuleTypePayment, TimeoutHours: -1}}
			},
			want: []string{"timeout_hours: value_must_be_positive_or_zero"},
		},
		{
			name: "duplicated rule types", source: "app/services/subscriptions/activation_rules/validate_service.rb:45",
			setup: func(f *fixture, c *customer.Customer) { f.providerCustomer(c); f.method(c, true) },
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.ActivationRules = append(slices.Clone(payRule), payRule...)
			},
			want: []string{"activation_rules: duplicated_type"},
		},
		{
			name: "payment rule with manual payment method", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:837",
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.PaymentMethod = &subscriptions.PaymentMethod{Type: &manual}
				in.ActivationRules = payRule
			},
			want: []string{"customer: manual_payment_method_invalid_for_payment_activation_rules"},
		},
		{
			name: "payment rule without payment provider", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:858",
			set:  func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) { in.ActivationRules = payRule },
			want: []string{"customer: no_linked_payment_provider"},
		},
		{
			name: "payment rule without default payment method", source: "spec/services/subscriptions/activation_rules/payment/validate_service_spec.rb:85",
			set: func(_ *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.PaymentMethod = &subscriptions.PaymentMethod{Type: &provider}
				in.ActivationRules = payRule
			},
			want: []string{"customer: no_default_payment_method"},
		},
		{
			name: "payment rule with unknown payment method", source: "spec/services/subscriptions/activation_rules/payment/validate_service_spec.rb:101",
			setup: func(f *fixture, c *customer.Customer) { f.providerCustomer(c) },
			set: func(f *fixture, _ *customer.Customer, in *subscriptions.CreateInput) {
				in.ActivationRules = payRule
				// Another customer's method.
				in.PaymentMethod = &subscriptions.PaymentMethod{ID: new(f.method(f.Customer(), true).ID)}
			},
			want: []string{"payment_method: invalid_payment_method", "customer: payment_method_not_found"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newFixture(t)
			c := f.Customer()
			if tt.setup != nil {
				tt.setup(f, c)
			}
			_, err := f.create(c, f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) { tt.set(f, c, in) })
			if got := fieldCodes(t, err); !slices.Equal(got, tt.want) {
				t.Errorf("errors = %v (%v), want %v", got, err, tt.want)
			}
		})
	}
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:760
func TestCreateFutureWithRules(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	f.providerCustomer(c)
	f.method(c, true)
	v, err := f.create(c, f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) {
		in.SubscriptionAt = new(now.AddDate(0, 0, 3))
		in.ActivationRules = []subscriptions.ActivationRule{{Type: subscription.ActivationRuleTypePayment, TimeoutHours: 48}}
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if v.Subscription.Status != subscription.StatusPending || len(v.ActivationRules) != 1 || v.ActivationRules[0].TimeoutHours != 48 {
		t.Errorf("subscription = %s with %d rules, want pending with one rule of 48h", v.Subscription.Status, len(v.ActivationRules))
	}
	if v.CurrentPeriod != nil {
		t.Errorf("current period = %v, want none while pending", v.CurrentPeriod)
	}
}

// source: spec/requests/api/v1/subscriptions_controller_spec.rb:597
func TestCreateCustomSections(t *testing.T) {
	f := newFixture(t)
	sec := &invoice.CustomSection{ID: core.NewID[core.InvoiceCustomSectionID](f.IDs), OrganizationID: f.OrgID(), Code: "sec", Name: "Sec", CreatedAt: now, UpdatedAt: now}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, sec) })
	tests := []struct {
		name, source string
		cs           subscriptions.CustomSections
		skip         bool
		want         []core.InvoiceCustomSectionID
	}{
		{
			name: "codes", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:644",
			cs:   subscriptions.CustomSections{Sections: []refs.Ref{refs.ByCode("sec"), refs.ByCode("unknown")}},
			want: []core.InvoiceCustomSectionID{sec.ID},
		},
		{
			name: "skip", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:617",
			cs:   subscriptions.CustomSections{Skip: new(true), Sections: []refs.Ref{refs.ByCode("sec")}},
			skip: true,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			v, err := f.create(f.Customer(), f.Plan(period.Monthly, 1000), func(in *subscriptions.CreateInput) {
				in.ExternalID = "sub_" + tt.name
				in.InvoiceCustomSections = &tests[i].cs
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			got := f.reload(v.Subscription.ID)
			if got.SkipInvoiceCustomSections != tt.skip || !slices.Equal(got.InvoiceCustomSectionIDs, tt.want) {
				t.Errorf("sections = skip %v %v, want skip %v %v", got.SkipInvoiceCustomSections, got.InvoiceCustomSectionIDs, tt.skip, tt.want)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	arrears := f.Plan(period.Monthly, 1000)
	sub := f.Subscription(c, arrears)
	v, err := f.svc.Update(f.Ctx, subscriptions.UpdateInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: sub.ExternalID},
		Name: core.Some("Renamed"), SubscriptionAt: core.Some(now.AddDate(0, 0, -20)),
		ProgressiveBillingDisabled: core.Some(true), ConsolidateInvoice: core.Some(false),
		OnTerminationInvoice:    core.Some(subscription.TerminationInvoiceSkip),
		OnTerminationCreditNote: core.Some(subscription.TerminationCreditNoteRefund),
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	s := v.Subscription
	// source: spec/services/subscriptions/update_service_spec.rb:184
	if *s.Name != "Renamed" || !s.SubscriptionAt.Equal(sub.SubscriptionAt) {
		t.Errorf("name, subscription_at = %q %s, want Renamed and subscription_at unchanged", *s.Name, s.SubscriptionAt)
	}
	// source: spec/services/subscriptions/update_service_spec.rb:258
	if s.OnTerminationCreditNote != "" || s.OnTerminationInvoice != subscription.TerminationInvoiceSkip {
		t.Errorf("termination = %q %q, want credit note ignored on an arrears plan and invoice skip", s.OnTerminationCreditNote, s.OnTerminationInvoice)
	}
	if !s.ProgressiveBillingDisabled || s.ConsolidateInvoice {
		t.Errorf("progressive_billing_disabled, consolidate_invoice = %v %v, want true false", s.ProgressiveBillingDisabled, s.ConsolidateInvoice)
	}
	// source: spec/services/subscriptions/update_service_spec.rb:195
	if got, want := f.Webhooks(), []webhook.EventType{webhook.EventTypeSubscriptionUpdated}; !slices.Equal(got, want) {
		t.Errorf("webhooks = %v, want %v", got, want)
	}
}

func TestUpdateRefused(t *testing.T) {
	tests := []struct {
		name, source string
		status       subscription.Status
		in           subscriptions.UpdateInput
		code         string
		fields       []string
	}{
		{
			name: "incomplete", source: "spec/services/subscriptions/update_service_spec.rb:41",
			status: subscription.StatusIncomplete, in: subscriptions.UpdateInput{Name: core.Some("x")},
			code: subscriptions.CodeSubscriptionIncomplete,
		},
		{
			name: "terminated purchase order number", source: "spec/services/subscriptions/update_service_spec.rb:120",
			status: subscription.StatusTerminated, in: subscriptions.UpdateInput{PurchaseOrderNumber: core.Some(new("PO-2"))},
			code: subscriptions.CodePurchaseOrderNumberNotEditable,
		},
		{
			name: "activation rules on an active subscription", source: "app/services/subscriptions/activation_rules/validate_service.rb:28",
			status: subscription.StatusActive,
			in:     subscriptions.UpdateInput{ActivationRules: core.Some([]subscriptions.ActivationRule{})},
			fields: []string{"activation_rules: subscription_not_pending"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newFixture(t)
			sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), func(s *subscription.Subscription) {
				if err := s.Activate(now, now); err != nil {
					t.Fatal(err)
				}
				switch tt.status {
				case subscription.StatusIncomplete:
					s.Status = subscription.StatusIncomplete
				case subscription.StatusTerminated:
					s.Status, s.TerminatedAt = subscription.StatusTerminated, &now
				}
			})
			in := tt.in
			in.OrganizationID, in.Subscription = f.OrgID(), subscriptions.SubscriptionRef{ID: sub.ID}
			_, err := f.svc.Update(f.Ctx, in)
			if got := ruleCode(err); got != tt.code {
				t.Errorf("rule code = %q (%v), want %q", got, err, tt.code)
			}
			if got := fieldCodes(t, err); !slices.Equal(got, tt.fields) {
				t.Errorf("errors = %v, want %v", got, tt.fields)
			}
		})
	}
}

func TestUpdateMovesStart(t *testing.T) {
	tests := []struct {
		name, source string
		at           time.Time
		status       subscription.Status
		startedAt    *time.Time
		hooks        []webhook.EventType
	}{
		{
			name: "to today", source: "spec/services/subscriptions/update_service_spec.rb:393",
			at: now.Add(-time.Hour), status: subscription.StatusActive, startedAt: new(now.Add(-time.Hour)),
			hooks: []webhook.EventType{webhook.EventTypeSubscriptionStarted, webhook.EventTypeSubscriptionUpdated},
		},
		{
			name: "to another future date", source: "spec/services/subscriptions/update_service_spec.rb:419",
			at: now.AddDate(0, 0, 5), status: subscription.StatusPending,
		},
		{
			name: "to the past", source: "spec/services/subscriptions/update_service_spec.rb:439",
			at: now.AddDate(0, 0, -5), status: subscription.StatusActive, startedAt: new(now.AddDate(0, 0, -5)),
			hooks: []webhook.EventType{webhook.EventTypeSubscriptionStarted, webhook.EventTypeSubscriptionUpdated},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newFixture(t)
			sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, 2)))
			v, err := f.svc.Update(f.Ctx, subscriptions.UpdateInput{
				OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ID: sub.ID},
				SubscriptionAt: core.Some(tt.at),
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			s := f.reload(v.Subscription.ID)
			if s.Status != tt.status || !s.SubscriptionAt.Equal(tt.at) {
				t.Errorf("status, subscription_at = %s %s, want %s %s", s.Status, s.SubscriptionAt, tt.status, tt.at)
			}
			if (s.StartedAt == nil) != (tt.startedAt == nil) || (s.StartedAt != nil && !s.StartedAt.Equal(*tt.startedAt)) {
				t.Errorf("started_at = %v, want %v", s.StartedAt, tt.startedAt)
			}
			if got := f.Webhooks(); !slices.Equal(got, tt.hooks) {
				t.Errorf("webhooks = %v, want %v", got, tt.hooks)
			}
		})
	}
}

// source: app/controllers/concerns/subscription_index.rb:11, app/queries/subscriptions_query.rb
func TestList(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	p, other := f.Plan(period.Monthly, 1000), f.Plan(period.Monthly, 2000)
	older := f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, -1, 0)))
	newer := f.Subscription(f.Customer(), p)
	f.Subscription(c, other)
	f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, 4)))
	tests := []struct {
		name string
		in   subscriptions.ListInput
		want []core.SubscriptionID
	}{
		{name: "active only by default, most recent first", in: subscriptions.ListInput{Plan: refs.ByCode(p.Code)}, want: []core.SubscriptionID{newer.ID, older.ID}},
		{name: "customer", in: subscriptions.ListInput{Customer: subscriptions.CustomerRef{ExternalID: c.ExternalID}, Plan: refs.ByID(p.ID)}, want: []core.SubscriptionID{older.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.OrganizationID = f.OrgID()
			page, err := f.svc.List(f.Ctx, in)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			var got []core.SubscriptionID
			for _, v := range page.Items {
				got = append(got, v.Subscription.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
		})
	}
	_, err := f.svc.List(f.Ctx, subscriptions.ListInput{OrganizationID: f.OrgID(), BillingEntities: []refs.Ref{refs.ByCode("nope")}})
	// source: app/controllers/concerns/subscription_index.rb:9
	if got := notFound(err); got != subscriptions.ResourceBillingEntity {
		t.Errorf("not found = %q (%v), want billing_entity", got, err)
	}
}

// source: app/controllers/api/v1/subscriptions_controller.rb:98, :151
func TestTerminateAndCancel(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	active := f.Subscription(c, f.Plan(period.Monthly, 1000))
	pending := f.Subscription(c, f.Plan(period.Monthly, 1000), billingtest.SubscribedAt(now.AddDate(0, 0, 3)))

	if _, err := f.svc.Cancel(f.Ctx, f.OrgID(), subscriptions.SubscriptionRef{ID: active.ID}); notFound(err) != subscriptions.Resource {
		t.Errorf("Cancel(active) error = %v, want subscription not found", err)
	}
	v, err := f.svc.Cancel(f.Ctx, f.OrgID(), subscriptions.SubscriptionRef{ExternalID: pending.ExternalID})
	if err != nil || v.Subscription.Status != subscription.StatusCanceled {
		t.Fatalf("Cancel() = %v, %v, want canceled", v, err)
	}
	v, err = f.svc.Terminate(f.Ctx, subscriptions.TerminateInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: active.ExternalID},
		OnTerminationInvoice: subscription.TerminationInvoiceSkip,
	})
	if err != nil || v.Subscription.Status != subscription.StatusTerminated {
		t.Fatalf("Terminate() = %v, %v, want terminated", v, err)
	}
	if _, err := f.svc.Terminate(f.Ctx, subscriptions.TerminateInput{
		OrganizationID: f.OrgID(), Subscription: subscriptions.SubscriptionRef{ExternalID: active.ExternalID},
	}); notFound(err) != subscriptions.Resource {
		t.Errorf("Terminate(no active) error = %v, want subscription not found", err)
	}
}

func TestLifetimeUsage(t *testing.T) {
	f := newFixture(t)
	sub := f.Subscription(f.Customer(), f.Plan(period.Monthly, 1000))
	ref := subscriptions.SubscriptionRef{ExternalID: sub.ExternalID}
	// source: spec/requests/api/v1/subscriptions/lifetime_usages_controller_spec.rb:39
	if _, err := f.svc.LifetimeUsage(f.Ctx, f.OrgID(), subscriptions.SubscriptionRef{ExternalID: "nope"}); notFound(err) != subscriptions.Resource {
		t.Errorf("LifetimeUsage(unknown) error = %v, want subscription not found", err)
	}
	if _, err := f.svc.LifetimeUsage(f.Ctx, f.OrgID(), ref); notFound(err) != subscriptions.ResourceLifetimeUsage {
		t.Errorf("LifetimeUsage(none) error = %v, want lifetime_usage not found", err)
	}
	lu := &subscription.LifetimeUsage{
		ID: core.NewID[core.LifetimeUsageID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: sub.ID,
		ExternalSubscriptionID: sub.ExternalID, Currency: money.MustCurrency("USD"), CreatedAt: now, UpdatedAt: now,
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.LifetimeUsages.Upsert(ctx, lu) })

	tests := []struct {
		name, source, amount string
		want                 int64
		fields               []string
	}{
		{name: "decimal", source: "spec/requests/api/v1/subscriptions/lifetime_usages_controller_spec.rb:112", amount: "10.5", want: 1050},
		{name: "finer than cents", source: "app/models/lifetime_usage.rb", amount: "10.505", fields: []string{"external_historical_usage_amount: invalid_value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			v, err := f.svc.UpdateLifetimeUsage(f.Ctx, f.OrgID(), ref, money.MustParse(tt.amount))
			if got := fieldCodes(t, err); !slices.Equal(got, tt.fields) {
				t.Fatalf("errors = %v (%v), want %v", got, err, tt.fields)
			}
			if err == nil && v.LifetimeUsage.HistoricalUsageAmount != tt.want {
				t.Errorf("historical usage = %d, want %d", v.LifetimeUsage.HistoricalUsageAmount, tt.want)
			}
		})
	}
}

// source: spec/requests/api/v1/subscriptions/lifetime_usages_controller_spec.rb:29
func TestLifetimeUsageThresholds(t *testing.T) {
	f := newFixture(t)
	p := f.Plan(period.Monthly, 1000)
	f.UsageThreshold(p, 10000, false)
	sub := f.Subscription(f.Customer(), p)
	lu := &subscription.LifetimeUsage{
		ID: core.NewID[core.LifetimeUsageID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: sub.ID,
		ExternalSubscriptionID: sub.ExternalID, Currency: money.MustCurrency("USD"), CurrentUsageAmount: 2500,
		CreatedAt: now, UpdatedAt: now,
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.LifetimeUsages.Upsert(ctx, lu) })
	v, err := f.svc.LifetimeUsage(f.Ctx, f.OrgID(), subscriptions.SubscriptionRef{ID: sub.ID})
	if err != nil {
		t.Fatalf("LifetimeUsage() error = %v", err)
	}
	if len(v.Thresholds) != 1 || v.Thresholds[0].Amount != 10000 || v.Thresholds[0].Reached {
		t.Errorf("thresholds = %+v, want one unreached 10000", v.Thresholds)
	}
}

// source: app/services/invoices/customer_usage_service.rb:57-60
func TestCurrentUsageRefused(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	p := f.Plan(period.Monthly, 1000)
	sub := f.Subscription(c, p)
	pending := f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, 3)))
	f.Metric("other", catalog.AggregationTypeCount, "")
	tests := []struct {
		name     string
		in       subscriptions.CurrentUsageInput
		notFound string
		code     string
	}{
		{name: "unknown customer", in: subscriptions.CurrentUsageInput{Customer: subscriptions.CustomerRef{ExternalID: "nope"}, Subscription: subscriptions.SubscriptionRef{ID: sub.ID}}, notFound: subscriptions.ResourceCustomer},
		{name: "pending subscription", in: subscriptions.CurrentUsageInput{Customer: subscriptions.CustomerRef{ID: c.ID}, Subscription: subscriptions.SubscriptionRef{ID: pending.ID}}, code: subscriptions.CodeNoActiveSubscription},
		{name: "metric without charge", in: subscriptions.CurrentUsageInput{Customer: subscriptions.CustomerRef{ID: c.ID}, Subscription: subscriptions.SubscriptionRef{ExternalID: sub.ExternalID}, BillableMetricCode: "other"}, notFound: subscriptions.ResourceCharge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.OrganizationID = f.OrgID()
			_, err := f.svc.CurrentUsage(f.Ctx, in)
			if notFound(err) != tt.notFound || ruleCode(err) != tt.code {
				t.Errorf("error = %v, want not found %q, rule %q", err, tt.notFound, tt.code)
			}
		})
	}
}

// source: spec/serializers/v1/customers/charge_usage_serializer_spec.rb:87
func TestCurrentUsageBreakdown(t *testing.T) {
	f := newFixture(t)
	c := f.Customer()
	p := f.Plan(period.Monthly, 0)
	m := f.Metric("calls", catalog.AggregationTypeCount, "")
	ch := f.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) {
		ch.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu", "us"}}, `{"amount":"2"}`)}
	})
	_ = ch
	sub := f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -5)))
	f.Events.Add(f.Event(sub, "calls", now.Add(-time.Hour), "region", "eu"))
	f.Events.Add(f.Event(sub, "calls", now.Add(-time.Hour), "region", "us"))
	f.Events.Add(f.Event(sub, "calls", now.Add(-time.Hour), "region", "asia"))
	u, err := f.svc.CurrentUsage(f.Ctx, subscriptions.CurrentUsageInput{
		OrganizationID: f.OrgID(), Customer: subscriptions.CustomerRef{ExternalID: c.ExternalID},
		Subscription: subscriptions.SubscriptionRef{ExternalID: sub.ExternalID}, Projection: true,
	})
	if err != nil {
		t.Fatalf("CurrentUsage() error = %v", err)
	}
	if u.Amount != 500 || u.ProjectedAmount == nil || len(u.Charges) != 1 {
		t.Fatalf("usage = amount %d projected %v charges %d, want 500, projected, one charge", u.Amount, u.ProjectedAmount, len(u.Charges))
	}
	cu := u.Charges[0]
	if !cu.Units.Equal(money.MustParse("3")) || cu.EventsCount != 3 || cu.Metric.Code != "calls" || cu.Grouped != nil {
		t.Errorf("charge usage = units %s events %d metric %s grouped %v, want 3 3 calls none", cu.Units, cu.EventsCount, cu.Metric.Code, cu.Grouped)
	}
	got := map[string]int64{}
	for _, fu := range cu.Filters {
		key := "default"
		if fu.Filter != nil {
			key = fu.Filter.Values["region"][0]
		}
		got[key] = fu.Amount
	}
	if want := map[string]int64{"eu": 400, "default": 100}; len(got) != len(want) || got["eu"] != want["eu"] || got["default"] != want["default"] {
		t.Errorf("filter amounts = %v, want %v", got, want)
	}
}
