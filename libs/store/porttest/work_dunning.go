package porttest

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testDunningWork checks the scan of the dunning campaigns clock job
// (organization.WorkDunningCustomers): organization a has an overdue
// invoice of a customer open to dunning; organization b has an overdue
// self-billed invoice, an overdue invoice of a customer excluded from
// dunning and one of an archived customer, so it has no work.
func testDunningWork(t *testing.T, h Harness) {
	t.Run("organizations with dunning customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		pa, pb := subscriptionFixtures(e)
		a, b := pa.org, pb.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			overdue := func(c *customer.Customer, selfBilled bool) {
				inv := subscriptionFixtureInvoice(ctx, e, r, c)
				noErr(t, "Transition()", inv.Transition(invoice.StatusFinalized, now))
				inv.PaymentOverdue = true
				inv.SelfBilled = selfBilled
				noErr(t, "Invoices.Update() overdue", r.Invoices.Update(ctx, inv))
			}
			overdue(pa.customer, false)
			overdue(pb.customer, true)

			excluded := subscriptionFixtureCustomer(ctx, e, r, pb.entity, "cus_excluded")
			excluded.ExcludeFromDunningCampaign = true
			noErr(t, "Customers.Update() excluded", r.Customers.Update(ctx, excluded))
			overdue(excluded, false)

			archived := subscriptionFixtureCustomer(ctx, e, r, pb.entity, "cus_archived")
			overdue(archived, false)
			archived.DeletedAt = ptr(now)
			noErr(t, "Customers.Update() archived", r.Customers.Update(ctx, archived))
			return nil
		})

		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Organizations.WithWork(ctx, organization.WorkDunningCustomers, now)
			noErr(t, "WithWork(dunning_customers)", err)
			// Other tests may share the database: only a and b count.
			got = slices.DeleteFunc(got, func(o core.OrganizationID) bool { return o != a && o != b })
			if want := []core.OrganizationID{a}; !slices.Equal(got, want) {
				t.Errorf("WithWork(dunning_customers) got = %v, want = %v", got, want)
			}
			return nil
		})
	})
}
