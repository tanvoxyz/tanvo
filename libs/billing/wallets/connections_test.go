package wallets_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var (
	stripeUS = map[string]kernel.ConnectionChoice{"payment": {Code: "stripe_us"}}
	nope     = map[string]kernel.ConnectionChoice{"payment": {Code: "nope"}}
)

func stripeConnection(h *billingtest.Harness, c *customer.Customer) core.PaymentProviderCustomerID {
	h.T.Helper()
	return h.PaymentConnection(c, h.Integration("stripe_us", integration.ProviderStripe), true).ID
}

// pinned returns the payment connection a billing object chose with its
// behavior, failing unless it has at most that one choice.
func pinned(h *billingtest.Harness, owner integration.ObjectOwner) (integration.ConnectionBehavior, core.PaymentProviderCustomerID) {
	h.T.Helper()
	rows := h.ObjectConnections(owner)
	switch {
	case len(rows) == 0:
		return "", core.PaymentProviderCustomerID{}
	case len(rows) > 1 || rows[0].Category != integration.CategoryPayment:
		h.T.Fatalf("connections of %v got = %+v, want = one payment choice", owner, rows)
	}
	return rows[0].Behavior, rows[0].PaymentProviderCustomerID
}

func monthly(cs map[string]kernel.ConnectionChoice) wallets.RuleInput {
	return wallets.RuleInput{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Connections: cs}
}

func walletCount(h *billingtest.Harness, c core.CustomerID) int {
	h.T.Helper()
	return len(billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.Wallet, error) {
		return kernel.All(func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
			return r.Wallets.List(ctx, h.OrgID(), wallet.Filter{CustomerID: c}, p)
		})
	}))
}

func TestCreateConnections(t *testing.T) {
	t.Run("wallet", func(t *testing.T) {
		h, s := setup(t, t0)
		c := eurCustomer(h)
		pc := stripeConnection(h, c)
		in := createInput(c)
		in.Connections = stripeUS
		w, err := s.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		owner := integration.ObjectOwner{WalletID: w.ID}
		if b, got := pinned(h, owner); b != integration.ConnectionBehaviorSpecific || got != pc {
			t.Errorf("pinned got = %s %s, want = specific %s", b, got, pc)
		}
		routes, err := s.Connections(h.Ctx, h.OrgID(), c.ID, owner)
		if err != nil {
			t.Fatalf("Connections() error = %v", err)
		}
		if got := routes[0]; got.Behavior != integration.ConnectionBehaviorSpecific || got.Code != "stripe_us" {
			t.Errorf("Connections()[0] got = %v, want = specific stripe_us", got)
		}
	})
	t.Run("wallet with an unresolved code", func(t *testing.T) {
		h, s := setup(t, t0)
		c := eurCustomer(h)
		in := createInput(c)
		in.Connections = nope
		_, err := s.Create(h.Ctx, h.OrgID(), in)
		if got := codes(err); !slices.Equal(got, []string{"connections/connection_not_found"}) {
			t.Errorf("Create() codes got = %v, want = [connections/connection_not_found]", got)
		}
		if n := walletCount(h, c.ID); n != 0 {
			t.Errorf("wallets got = %d, want = 0", n)
		}
	})
	t.Run("wallet with an invalid choice", func(t *testing.T) {
		h, s := setup(t, t0)
		in := createInput(eurCustomer(h))
		in.Connections = map[string]kernel.ConnectionChoice{"shipping": {Behavior: "skip"}, "tax": {Behavior: "whatever"}}
		_, err := s.Create(h.Ctx, h.OrgID(), in)
		want := []string{"connections/invalid_connection_category", "connections/invalid_connection_behavior"}
		if got := codes(err); !slices.Equal(got, want) {
			t.Errorf("Create() codes got = %v, want = %v", got, want)
		}
	})
	for _, tt := range []struct {
		name, source string
		resolves     bool
	}{
		{"rule with an unresolved code", "spec/services/wallets/create_service_spec.rb:1373", false},
		{"rule", "spec/services/wallets/create_service_spec.rb:1386", true},
		{"rule alone with an unresolved code", "spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:478", false},
		{"rule alone", "spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:464", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			h, s := setup(t, t0)
			c := eurCustomer(h)
			var pc core.PaymentProviderCustomerID
			if tt.resolves {
				pc = stripeConnection(h, c)
			}
			in := createInput(c)
			in.RecurringTransactionRules = []wallets.RuleInput{monthly(stripeUS)}
			w, err := s.Create(h.Ctx, h.OrgID(), in)
			if !tt.resolves {
				if got := codes(err); !slices.Equal(got, []string{"connections/connection_not_found"}) {
					t.Errorf("Create() codes got = %v, want = [connections/connection_not_found]", got)
				}
				if n := walletCount(h, c.ID); n != 0 {
					t.Errorf("wallets got = %d, want = 0", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			rules, err := s.ListRules(h.Ctx, h.OrgID(), w.ID)
			if err != nil || len(rules) != 1 {
				t.Fatalf("ListRules() got = %d, %v, want = 1 rule", len(rules), err)
			}
			if b, got := pinned(h, integration.ObjectOwner{RecurringTransactionRuleID: rules[0].ID}); b != integration.ConnectionBehaviorSpecific || got != pc {
				t.Errorf("pinned on the rule got = %s %s, want = specific %s", b, got, pc)
			}
			if b, _ := pinned(h, integration.ObjectOwner{WalletID: w.ID}); b != "" {
				t.Errorf("pinned on the wallet got = %s, want = none", b)
			}
		})
	}
	t.Run("rule with an invalid choice", func(t *testing.T) {
		h, s := setup(t, t0)
		in := createInput(eurCustomer(h))
		in.RecurringTransactionRules = []wallets.RuleInput{monthly(map[string]kernel.ConnectionChoice{"shipping": {}})}
		_, err := s.Create(h.Ctx, h.OrgID(), in)
		// The first code only (create_service.rb:117-124).
		if got := codes(err); !slices.Equal(got, []string{"connections/invalid_connection_category"}) {
			t.Errorf("Create() codes got = %v, want = [connections/invalid_connection_category]", got)
		}
	})
}

func TestUpdateConnections(t *testing.T) {
	// newWallet creates a wallet with a monthly rule and a Stripe
	// connection of its customer.
	newWallet := func(t *testing.T) (*billingtest.Harness, *wallets.Service, *wallet.Wallet, *wallet.RecurringTransactionRule, core.PaymentProviderCustomerID) {
		h, s := setup(t, t0)
		c := eurCustomer(h)
		pc := stripeConnection(h, c)
		in := createInput(c)
		in.RecurringTransactionRules = []wallets.RuleInput{monthly(nil)}
		w, err := s.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		rules, err := s.ListRules(h.Ctx, h.OrgID(), w.ID)
		if err != nil || len(rules) != 1 {
			t.Fatalf("ListRules() got = %d, %v, want = 1 rule", len(rules), err)
		}
		return h, s, w, rules[0], pc
	}
	skipRow := func(h *billingtest.Harness, owner integration.ObjectOwner) {
		c := &integration.ObjectConnection{
			ID: core.NewID[core.ObjectConnectionID](h.IDs), OrganizationID: h.OrgID(), Owner: owner,
			Category: integration.CategoryPayment, Behavior: integration.ConnectionBehaviorSkip, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		}
		h.Do(func(ctx context.Context, r port.Repos) error { return r.ObjectConnections.Create(ctx, c) })
	}
	weekly := func(id core.RecurringTransactionRuleID, cs map[string]kernel.ConnectionChoice) wallets.UpdateInput {
		return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{
			ID: id, Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, Connections: cs,
		}})}
	}

	t.Run("wallet", func(t *testing.T) {
		h, s, w, _, pc := newWallet(t)
		owner := integration.ObjectOwner{WalletID: w.ID}
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{Connections: stripeUS}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if b, got := pinned(h, owner); b != integration.ConnectionBehaviorSpecific || got != pc {
			t.Errorf("pinned got = %s %s, want = specific %s", b, got, pc)
		}
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{Connections: map[string]kernel.ConnectionChoice{"payment": {Behavior: "inherit"}}}); err != nil {
			t.Fatalf("Update() inherit error = %v", err)
		}
		if b, _ := pinned(h, owner); b != "" {
			t.Errorf("pinned after inherit got = %s, want = none", b)
		}
		_, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{Connections: map[string]kernel.ConnectionChoice{"payment": {}}})
		if got := codes(err); !slices.Equal(got, []string{"connections/invalid_connection_choice"}) {
			t.Errorf("Update() codes got = %v, want = [connections/invalid_connection_choice]", got)
		}
	})

	t.Run("rule matched by ID", func(t *testing.T) {
		t.Log("source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:506-517")
		h, s, w, rule, pc := newWallet(t)
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, weekly(rule.ID, stripeUS)); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		rules, _ := s.ListRules(h.Ctx, h.OrgID(), w.ID)
		if len(rules) != 1 || rules[0].ID != rule.ID || rules[0].Interval != period.Weekly {
			t.Fatalf("rules got = %+v, want = %s updated in place", rules, rule.ID)
		}
		if b, got := pinned(h, integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}); b != integration.ConnectionBehaviorSpecific || got != pc {
			t.Errorf("pinned got = %s %s, want = specific %s", b, got, pc)
		}
	})

	t.Run("rule with a choice of the category", func(t *testing.T) {
		t.Log("source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:521")
		h, s, w, rule, pc := newWallet(t)
		owner := integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}
		skipRow(h, owner)
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, weekly(rule.ID, stripeUS)); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if b, got := pinned(h, owner); b != integration.ConnectionBehaviorSpecific || got != pc {
			t.Errorf("pinned got = %s %s, want = specific %s", b, got, pc)
		}
	})

	t.Run("rule replaced", func(t *testing.T) {
		t.Log("source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:544")
		h, s, w, rule, pc := newWallet(t)
		old := integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}
		skipRow(h, old)
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, weekly(core.RecurringTransactionRuleID{}, stripeUS)); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		rules, _ := s.ListRules(h.Ctx, h.OrgID(), w.ID)
		if len(rules) != 1 || rules[0].ID == rule.ID {
			t.Fatalf("rules got = %+v, want = one new rule", rules)
		}
		if b, got := pinned(h, integration.ObjectOwner{RecurringTransactionRuleID: rules[0].ID}); b != integration.ConnectionBehaviorSpecific || got != pc {
			t.Errorf("pinned on the new rule got = %s %s, want = specific %s", b, got, pc)
		}
		// The terminated rule keeps its choice.
		if b, _ := pinned(h, old); b != integration.ConnectionBehaviorSkip {
			t.Errorf("choice of the terminated rule got = %q, want = skip", b)
		}
	})

	for _, tt := range []struct {
		name, source string
		cs           map[string]kernel.ConnectionChoice
		want         string
	}{
		{"rule with an unresolved code", "spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:584", nope, "connections/connection_not_found"},
		{"rule with an invalid behavior", "spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:602", map[string]kernel.ConnectionChoice{"payment": {Behavior: "nonsense"}}, "connections/invalid_connection_behavior"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			h, s, w, rule, _ := newWallet(t)
			_, err := s.Update(h.Ctx, h.OrgID(), w.ID, weekly(rule.ID, tt.cs))
			if got := codes(err); !slices.Equal(got, []string{tt.want}) {
				t.Errorf("Update() codes got = %v, want = [%s]", got, tt.want)
			}
			if b, _ := pinned(h, integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}); b != "" {
				t.Errorf("pinned got = %s, want = none", b)
			}
			rules, _ := s.ListRules(h.Ctx, h.OrgID(), w.ID)
			if len(rules) != 1 || rules[0].Interval != period.Monthly {
				t.Errorf("rules got = %+v, want = the monthly rule unchanged", rules)
			}
		})
	}
}
