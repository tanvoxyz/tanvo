package billingtest

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Integration creates an integration of provider p with code, in the
// provider's category.
func (h *Harness) Integration(code string, p integration.Provider) *integration.Integration {
	h.T.Helper()
	cat, ok := integration.CategoryOf(p)
	if !ok {
		h.T.Fatalf("Integration(): provider %q has no category", p)
	}
	in := &integration.Integration{
		ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code,
		Category: cat, Provider: p, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, in) })
	return in
}

// PaymentConnection links customer c to the payment integration in.
func (h *Harness) PaymentConnection(c *customer.Customer, in *integration.Integration, isDefault bool) *payment.ProviderCustomer {
	h.T.Helper()
	id := core.NewID[core.PaymentProviderCustomerID](h.IDs)
	pc := &payment.ProviderCustomer{
		ID: id, OrganizationID: h.OrgID(), CustomerID: c.ID, IntegrationID: in.ID,
		ProviderCustomerID: new("cus_" + id.String()[30:]), IsDefault: isDefault, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Create(ctx, pc) })
	return pc
}

// IntegrationConnection links customer c to the tax, accounting or CRM
// integration in.
func (h *Harness) IntegrationConnection(c *customer.Customer, in *integration.Integration, isDefault bool) *integration.IntegrationCustomer {
	h.T.Helper()
	id := core.NewID[core.IntegrationCustomerID](h.IDs)
	ic := &integration.IntegrationCustomer{
		ID: id, OrganizationID: h.OrgID(), IntegrationID: in.ID, CustomerID: c.ID, Category: in.Category,
		ExternalCustomerID: "ext_" + id.String()[30:], IsDefault: isDefault, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationCustomers.Create(ctx, ic) })
	return ic
}

// ObjectConnections returns the connection choices of a billing object.
func (h *Harness) ObjectConnections(owner integration.ObjectOwner) []*integration.ObjectConnection {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*integration.ObjectConnection, error) {
		return r.ObjectConnections.ListByOwner(ctx, h.OrgID(), owner)
	})
}

// Rule creates an active threshold recurring rule of wallet w.
func (h *Harness) Rule(w core.WalletID) *wallet.RecurringTransactionRule {
	h.T.Helper()
	rule := &wallet.RecurringTransactionRule{
		ID: core.NewID[core.RecurringTransactionRuleID](h.IDs), OrganizationID: h.OrgID(), WalletID: w,
		Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: new(money.MustParse("10")),
		Method: wallet.RecurringMethodFixed, PaidCredits: money.MustParse("20"), Status: wallet.RuleStatusActive,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.RecurringTransactionRules.Create(ctx, rule) })
	return rule
}
