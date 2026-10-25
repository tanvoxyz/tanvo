package kernel_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

type choices = map[string]kernel.ConnectionChoice

func TestConnectionErrorCodes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		source  string
		choices choices
		want    []string
	}{
		{"absent", "spec/services/billing_object_connections/validate_service_spec.rb:11", nil, nil},
		{"empty", "spec/services/billing_object_connections/validate_service_spec.rb:19", choices{}, nil},
		{"code", "spec/services/billing_object_connections/validate_service_spec.rb:27", choices{"payment": {Code: "stripe_us"}}, nil},
		{"inherit", "spec/services/billing_object_connections/validate_service_spec.rb:35-44", choices{"tax": {Behavior: "inherit"}}, nil},
		{"skip", "spec/services/billing_object_connections/validate_service_spec.rb:35-44", choices{"tax": {Behavior: "skip"}}, nil},
		{"every category", "spec/services/billing_object_connections/validate_service_spec.rb:47", choices{
			"payment": {Behavior: "skip"}, "tax": {Behavior: "skip"}, "accounting": {Code: "netsuite_main"}, "crm": {Behavior: "inherit"},
		}, nil},
		{"unknown category", "spec/services/billing_object_connections/validate_service_spec.rb:62", choices{"shipping": {Behavior: "skip"}}, []string{kernel.CodeInvalidConnectionCategory}},
		{"auth category", "", choices{"auth": {Behavior: "skip"}}, []string{kernel.CodeInvalidConnectionCategory}},
		{"unknown behavior", "spec/services/billing_object_connections/validate_service_spec.rb:71", choices{"payment": {Behavior: "whatever"}}, []string{kernel.CodeInvalidConnectionBehavior}},
		{"specific behavior", "spec/services/billing_object_connections/validate_service_spec.rb:80", choices{"payment": {Behavior: "specific"}}, []string{kernel.CodeInvalidConnectionBehavior}},
		{"code and behavior", "spec/services/billing_object_connections/validate_service_spec.rb:89", choices{"payment": {Code: "stripe_us", Behavior: "skip"}}, []string{kernel.CodeInvalidConnectionChoice}},
		{"neither", "spec/services/billing_object_connections/validate_service_spec.rb:98", choices{"payment": {}}, []string{kernel.CodeInvalidConnectionChoice}},
		{"blank code", "", choices{"payment": {Code: "  "}}, []string{kernel.CodeInvalidConnectionChoice}},
		{"unknown category and choice", "", choices{"shipping": {}}, []string{kernel.CodeInvalidConnectionCategory, kernel.CodeInvalidConnectionChoice}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Log("source:", tt.source)
			}
			if got := kernel.ConnectionErrorCodes(tt.choices); !slices.Equal(got, tt.want) {
				t.Errorf("ConnectionErrorCodes() got = %v, want = %v", got, tt.want)
			}
			var v core.Validator
			kernel.ValidateConnections(&v, tt.choices)
			var got []string
			for _, fe := range core.FieldErrors(v.Err()) {
				if fe.Field != kernel.FieldConnections {
					t.Errorf("field got = %q, want = %q", fe.Field, kernel.FieldConnections)
				}
				got = append(got, fe.Code)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ValidateConnections() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

// connFixture is a customer with a wallet, a recurring rule, a Stripe
// connection with code stripe_us and a NetSuite connection with code
// netsuite_main.
type connFixture struct {
	*billingtest.Harness
	stripe   *integration.Integration
	netsuite *integration.Integration
	pcID     core.PaymentProviderCustomerID
	icID     core.IntegrationCustomerID
	cust     core.CustomerID
	wallet   integration.ObjectOwner
}

func newConnFixture(t *testing.T) *connFixture {
	h := billingtest.New(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	c := h.Customer()
	f := &connFixture{
		Harness: h, cust: c.ID,
		stripe:   h.Integration("stripe_us", integration.ProviderStripe),
		netsuite: h.Integration("netsuite_main", integration.ProviderNetsuite),
	}
	f.pcID = h.PaymentConnection(c, f.stripe, true).ID
	f.icID = h.IntegrationConnection(c, f.netsuite, true).ID
	f.wallet = integration.ObjectOwner{WalletID: h.Wallet(c, "0").ID}
	return f
}

// attach applies cs to owner in its own transaction.
func (f *connFixture) attach(owner integration.ObjectOwner, cs choices) error {
	return f.Deps.Tx.InTx(f.Ctx, func(ctx context.Context, r port.Repos) error {
		return kernel.AttachConnections(ctx, f.Deps, r, f.OrgID(), f.cust, owner, cs)
	})
}

func (f *connFixture) row(owner integration.ObjectOwner, cat integration.Category, b integration.ConnectionBehavior) {
	f.T.Helper()
	c := &integration.ObjectConnection{
		ID: core.NewID[core.ObjectConnectionID](f.IDs), OrganizationID: f.OrgID(), Owner: owner,
		Category: cat, Behavior: b, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.ObjectConnections.Create(ctx, c) })
}

// summary is a row's category, behavior and connection.
type summary struct {
	cat integration.Category
	b   integration.ConnectionBehavior
	pc  core.PaymentProviderCustomerID
	ic  core.IntegrationCustomerID
}

func (f *connFixture) rows(owner integration.ObjectOwner) []summary {
	f.T.Helper()
	var out []summary
	for _, c := range f.ObjectConnections(owner) {
		if c.OrganizationID != f.OrgID() || c.Owner != owner {
			f.T.Errorf("row owner got = %v/%v, want = %v/%v", c.OrganizationID, c.Owner, f.OrgID(), owner)
		}
		out = append(out, summary{c.Category, c.Behavior, c.PaymentProviderCustomerID, c.IntegrationCustomerID})
	}
	slices.SortFunc(out, func(a, b summary) int {
		return slices.Index(integration.RoutedCategories(), a.cat) - slices.Index(integration.RoutedCategories(), b.cat)
	})
	return out
}

func connectionCodes(err error) []string {
	var out []string
	for _, fe := range core.FieldErrors(err) {
		if fe.Field == kernel.FieldConnections {
			out = append(out, fe.Code)
		}
	}
	return out
}

func TestAttachConnections(t *testing.T) {
	specific := integration.ConnectionBehaviorSpecific
	skip := integration.ConnectionBehaviorSkip
	for _, tt := range []struct {
		name   string
		source string
		before []integration.Category // skip rows stored beforehand
		other  bool                   // the code names another customer's connection
		in     choices
		rule   bool
		want   func(f *connFixture) []summary
		err    string
	}{
		{name: "absent", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:16", want: func(*connFixture) []summary { return nil }},
		{name: "empty", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:25", in: choices{}, want: func(*connFixture) []summary { return nil }},
		{name: "specific payment", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:34", in: choices{"payment": {Code: "stripe_us"}}, want: func(f *connFixture) []summary {
			return []summary{{integration.CategoryPayment, specific, f.pcID, core.IntegrationCustomerID{}}}
		}},
		{name: "specific accounting", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:53", in: choices{"accounting": {Code: "netsuite_main"}}, want: func(f *connFixture) []summary {
			return []summary{{integration.CategoryAccounting, specific, core.PaymentProviderCustomerID{}, f.icID}}
		}},
		{name: "skip", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:71", in: choices{"tax": {Behavior: "skip"}}, want: func(*connFixture) []summary {
			return []summary{{cat: integration.CategoryTax, b: skip}}
		}},
		{name: "inherit removes the choice", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:90", before: []integration.Category{integration.CategoryTax}, in: choices{"tax": {Behavior: "inherit"}}, want: func(*connFixture) []summary { return nil }},
		{name: "inherit without a choice", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:99", in: choices{"tax": {Behavior: "inherit"}}, want: func(*connFixture) []summary { return nil }},
		{name: "omitted category kept", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:107", before: []integration.Category{integration.CategoryTax}, in: choices{"payment": {Behavior: "skip"}}, want: func(*connFixture) []summary {
			return []summary{{cat: integration.CategoryPayment, b: skip}, {cat: integration.CategoryTax, b: skip}}
		}},
		{name: "same category rewritten", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:118", before: []integration.Category{integration.CategoryPayment}, in: choices{"payment": {Code: "stripe_us"}}, want: func(f *connFixture) []summary {
			return []summary{{integration.CategoryPayment, specific, f.pcID, core.IntegrationCustomerID{}}}
		}},
		{name: "unknown code", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:136", in: choices{"payment": {Code: "does_not_exist"}}, err: kernel.CodeConnectionNotFound},
		{name: "another customer's code", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:149", other: true, in: choices{"payment": {Code: "stripe_other"}}, err: kernel.CodeConnectionNotFound},
		{name: "code of another category", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:161", in: choices{"crm": {Code: "netsuite_main"}}, err: kernel.CodeConnectionNotFound},
		{name: "rolls back earlier categories", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:172", in: choices{"payment": {Code: "stripe_us"}, "accounting": {Code: "nope"}}, err: kernel.CodeConnectionNotFound},
		{name: "recurring rule", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:185", rule: true, in: choices{"payment": {Code: "stripe_us"}}, want: func(f *connFixture) []summary {
			return []summary{{integration.CategoryPayment, specific, f.pcID, core.IntegrationCustomerID{}}}
		}},
		{name: "every category", source: "spec/services/billing_object_connections/attach_to_resource_service_spec.rb:204", in: choices{
			"payment": {Code: "stripe_us"}, "tax": {Behavior: "skip"}, "accounting": {Code: "netsuite_main"}, "crm": {Behavior: "skip"},
		}, want: func(f *connFixture) []summary {
			return []summary{
				{integration.CategoryPayment, specific, f.pcID, core.IntegrationCustomerID{}},
				{cat: integration.CategoryTax, b: skip},
				{integration.CategoryAccounting, specific, core.PaymentProviderCustomerID{}, f.icID},
				{cat: integration.CategoryCRM, b: skip},
			}
		}},
		{name: "blank choice ignored", in: choices{"tax": {Code: " "}}, want: func(*connFixture) []summary { return nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Log("source:", tt.source)
			}
			f := newConnFixture(t)
			owner := f.wallet
			if tt.rule {
				owner = integration.ObjectOwner{RecurringTransactionRuleID: f.Rule(f.wallet.WalletID).ID}
			}
			for _, cat := range tt.before {
				f.row(owner, cat, skip)
			}
			if tt.other {
				f.PaymentConnection(f.Customer(), f.Integration("stripe_other", integration.ProviderStripe), true)
			}
			before := f.rows(owner)
			err := f.attach(owner, tt.in)
			if tt.err != "" {
				if got := connectionCodes(err); !slices.Equal(got, []string{tt.err}) {
					t.Fatalf("AttachConnections() codes got = %v (%v), want = [%s]", got, err, tt.err)
				}
				if got := f.rows(owner); !slices.Equal(got, before) {
					t.Errorf("rows after a failure got = %v, want = %v", got, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("AttachConnections() error = %v", err)
			}
			if got, want := f.rows(owner), tt.want(f); !slices.Equal(got, want) {
				t.Errorf("rows got = %v, want = %v", got, want)
			}
		})
	}
}

func TestConnectionRouting(t *testing.T) {
	const (
		inherit  = integration.ConnectionBehaviorInherit
		specific = integration.ConnectionBehaviorSpecific
		skip     = integration.ConnectionBehaviorSkip
	)
	route := func(cat integration.Category, b integration.ConnectionBehavior, code string) kernel.ConnectionRoute {
		return kernel.ConnectionRoute{Category: cat, Behavior: b, Code: code}
	}
	routing := func(f *connFixture, owner integration.ObjectOwner) []kernel.ConnectionRoute {
		t.Helper()
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]kernel.ConnectionRoute, error) {
			return kernel.ConnectionRouting(ctx, r, f.OrgID(), f.cust, owner)
		})
	}

	t.Run("inherit the customer's defaults", func(t *testing.T) {
		f := newConnFixture(t)
		want := []kernel.ConnectionRoute{
			route(integration.CategoryPayment, inherit, "stripe_us"), route(integration.CategoryTax, inherit, ""),
			route(integration.CategoryAccounting, inherit, "netsuite_main"), route(integration.CategoryCRM, inherit, ""),
		}
		if got := routing(f, f.wallet); !slices.Equal(got, want) {
			t.Errorf("ConnectionRouting() got = %v, want = %v", got, want)
		}
	})

	t.Run("choices of the object", func(t *testing.T) {
		f := newConnFixture(t)
		if err := f.attach(f.wallet, choices{"payment": {Code: "stripe_us"}, "accounting": {Behavior: "skip"}}); err != nil {
			t.Fatalf("AttachConnections() error = %v", err)
		}
		want := []kernel.ConnectionRoute{
			route(integration.CategoryPayment, specific, "stripe_us"), route(integration.CategoryTax, inherit, ""),
			route(integration.CategoryAccounting, skip, ""), route(integration.CategoryCRM, inherit, ""),
		}
		if got := routing(f, f.wallet); !slices.Equal(got, want) {
			t.Errorf("ConnectionRouting() got = %v, want = %v", got, want)
		}
	})

	t.Run("several connections without a default", func(t *testing.T) {
		f := newConnFixture(t)
		cust := f.ReloadCustomer(f.cust)
		// The only connection of a category is in effect even when not the
		// default; among several, only the default is.
		f.Do(func(ctx context.Context, r port.Repos) error {
			pc, err := r.PaymentProviderCustomers.Get(ctx, f.OrgID(), f.pcID)
			if err != nil {
				return err
			}
			pc.IsDefault = false
			return r.PaymentProviderCustomers.Update(ctx, pc)
		})
		if got := routing(f, f.wallet)[0]; got != route(integration.CategoryPayment, inherit, "stripe_us") {
			t.Errorf("only connection got = %v, want = stripe_us", got)
		}
		f.PaymentConnection(cust, f.Integration("adyen_eu", integration.ProviderAdyen), false)
		if got := routing(f, f.wallet)[0]; got != route(integration.CategoryPayment, inherit, "") {
			t.Errorf("no default among two got = %v, want = none", got)
		}
	})
}

func TestAttachConnectionsDeletedConnection(t *testing.T) {
	f := newConnFixture(t)
	f.Do(func(ctx context.Context, r port.Repos) error {
		pc, err := r.PaymentProviderCustomers.Get(ctx, f.OrgID(), f.pcID)
		if err != nil {
			return err
		}
		pc.IsDefault = false
		pc.DeletedAt = new(f.Now())
		return r.PaymentProviderCustomers.Update(ctx, pc)
	})
	err := f.attach(f.wallet, choices{"payment": {Code: "stripe_us"}})
	if got := connectionCodes(err); !slices.Equal(got, []string{kernel.CodeConnectionNotFound}) {
		t.Errorf("deleted connection got = %v, want = [%s]", got, kernel.CodeConnectionNotFound)
	}
}
