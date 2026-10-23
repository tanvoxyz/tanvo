package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testDunning(t *testing.T, h Harness) {
	t.Run("campaigns", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		c := dunningFixtureCampaign(e, org, "reminders", "EUR", "USD")
		c.Description = ptr("Three reminders")
		c.DaysBetweenAttempts = 3
		c.MaxAttempts = 5
		c.BCCEmails = []string{"finance@acme.test", "cfo@acme.test"}
		minimal := dunningFixtureCampaign(e, org, "minimal", "GBP")
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.DunningCampaigns.Create(ctx, c))
			noErr(t, "Create() minimal", r.DunningCampaigns.Create(ctx, minimal))
			wantErr(t, "Create() with the same ID", r.DunningCampaigns.Create(ctx, c), core.ErrConflict)
			got, err := r.DunningCampaigns.Get(ctx, org, c.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, c)
			got, err = r.DunningCampaigns.Get(ctx, org, minimal.ID)
			noErr(t, "Get() minimal", err)
			equal(t, "Get() minimal", got, minimal)
			got, err = r.DunningCampaigns.GetByCode(ctx, org, "reminders")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, c)
			_, err = r.DunningCampaigns.Get(ctx, org, core.NewID[core.DunningCampaignID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.DunningCampaigns.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			// Isolation.
			_, err = r.DunningCampaigns.Get(ctx, b.OrganizationID, c.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.DunningCampaigns.GetByCode(ctx, b.OrganizationID, "reminders")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			other := *c
			other.OrganizationID = b.OrganizationID
			other.Thresholds = nil
			wantErr(t, "Update() through another organization", r.DunningCampaigns.Update(ctx, &other), core.ErrNotFound)
			p, err := r.DunningCampaigns.List(ctx, b.OrganizationID, dunning.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(p.Items), 0)
			noErr(t, "Create() with the code in another organization", r.DunningCampaigns.Create(ctx,
				dunningFixtureCampaign(e, b.OrganizationID, "reminders", "EUR")))

			// Codes are unique among live campaigns.
			wantErr(t, "Create() with a taken code", r.DunningCampaigns.Create(ctx, dunningFixtureCampaign(e, org, "reminders", "EUR")), core.ErrConflict)
			minimal.Code = "reminders"
			wantErr(t, "Update() to a taken code", r.DunningCampaigns.Update(ctx, minimal), core.ErrConflict)
			minimal.Code = "minimal"

			// Update replaces the fields and the thresholds.
			c.Name = "Renamed"
			c.Description = nil
			c.BCCEmails = nil
			c.Thresholds = []dunning.Threshold{
				{ID: core.NewID[core.DunningCampaignThresholdID](e.ids), Currency: money.MustCurrency("JPY"), Amount: 0},
				c.Thresholds[0],
			}
			c.Thresholds[1].Amount = 99
			c.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.DunningCampaigns.Update(ctx, c))
			got, err = r.DunningCampaigns.Get(ctx, org, c.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, c)
			missing := dunningFixtureCampaign(e, org, "missing", "EUR")
			wantErr(t, "Update() unknown", r.DunningCampaigns.Update(ctx, missing), core.ErrNotFound)

			// A deleted campaign frees its code and stays readable.
			noErr(t, "Delete()", c.Delete(at(time.Hour)))
			noErr(t, "Update() deleted", r.DunningCampaigns.Update(ctx, c))
			got, err = r.DunningCampaigns.Get(ctx, org, c.ID)
			noErr(t, "Get() deleted", err)
			equal(t, "Get() deleted", got, c)
			_, err = r.DunningCampaigns.GetByCode(ctx, org, "reminders")
			wantErr(t, "GetByCode() deleted", err, core.ErrNotFound)
			again := dunningFixtureCampaign(e, org, "reminders", "EUR")
			noErr(t, "Create() with a freed code", r.DunningCampaigns.Create(ctx, again))
			got, err = r.DunningCampaigns.GetByCode(ctx, org, "reminders")
			noErr(t, "GetByCode() of the new campaign", err)
			equal(t, "GetByCode() of the new campaign", got, again)
			return nil
		})
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		c1 := dunningFixtureCampaign(e, org, "c1", "EUR")
		c2 := dunningFixtureCampaign(e, org, "c2", "USD", "EUR")
		c3 := dunningFixtureCampaign(e, org, "c3", "GBP")
		c4 := dunningFixtureCampaign(e, org, "c4", "EUR")
		noErr(t, "Delete()", c4.Delete(now))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*dunning.Campaign{c1, c2, c3, c4} {
				noErr(t, "Create()", r.DunningCampaigns.Create(ctx, c))
			}
			noErr(t, "Create() in another organization", r.DunningCampaigns.Create(ctx, dunningFixtureCampaign(e, b.OrganizationID, "c1", "EUR")))
			cid := func(c *dunning.Campaign) core.DunningCampaignID { return c.ID }
			for _, tc := range []struct {
				name string
				f    dunning.Filter
				want []*dunning.Campaign
			}{
				{"all live", dunning.Filter{}, []*dunning.Campaign{c1, c2, c3}},
				{"one currency", dunning.Filter{Currencies: []string{"EUR"}}, []*dunning.Campaign{c1, c2}},
				{"currencies", dunning.Filter{Currencies: []string{"GBP", "USD"}}, []*dunning.Campaign{c2, c3}},
				{"unused currency", dunning.Filter{Currencies: []string{"CHF"}}, nil},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*dunning.Campaign], error) {
					return r.DunningCampaigns.List(ctx, org, tc.f, p)
				})
				equal(t, "List() "+tc.name, ids(got, cid), ids(tc.want, cid))
			}
			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*dunning.Campaign], error) {
				return r.DunningCampaigns.List(ctx, org, dunning.Filter{}, p)
			})
			equal(t, "List() items", all, []*dunning.Campaign{c1, c2, c3})
			return nil
		})
	})

	t.Run("applied campaigns", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		org := a.OrganizationID
		c := dunningFixtureCampaign(e, org, "reminders", "EUR")
		cust := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust", a.ID, now)
		cust.AppliedDunningCampaignID = c.ID
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.DunningCampaigns.Create(ctx, c))
			a.AppliedDunningCampaignID = c.ID
			noErr(t, "BillingEntities.Update()", r.BillingEntities.Update(ctx, a))
			got, err := r.BillingEntities.Get(ctx, org, a.ID)
			noErr(t, "BillingEntities.Get()", err)
			equal(t, "BillingEntity.AppliedDunningCampaignID", got.AppliedDunningCampaignID, c.ID)
			noErr(t, "Customers.Create()", r.Customers.Create(ctx, cust))
			gotCust, err := r.Customers.Get(ctx, org, cust.ID)
			noErr(t, "Customers.Get()", err)
			equal(t, "Customer.AppliedDunningCampaignID", gotCust.AppliedDunningCampaignID, c.ID)
			if h.Name != "memstore" {
				// The store refuses a campaign that does not exist; memstore
				// does not check references.
				a.AppliedDunningCampaignID = core.NewID[core.DunningCampaignID](e.ids)
				wantErr(t, "BillingEntities.Update() with an unknown campaign", r.BillingEntities.Update(ctx, a), core.ErrNotFound)
				cust.AppliedDunningCampaignID = core.NewID[core.DunningCampaignID](e.ids)
				wantErr(t, "Customers.Update() with an unknown campaign", r.Customers.Update(ctx, cust), core.ErrNotFound)
			}
			return nil
		})
	})
}

// dunningFixtureCampaign returns an unsaved campaign with one threshold per
// currency code, of 1000 minor units.
func dunningFixtureCampaign(e *env, org core.OrganizationID, code string, currencies ...string) *dunning.Campaign {
	c := dunning.New(org, core.NewID[core.DunningCampaignID](e.ids), code, "Campaign "+code, now)
	for _, cur := range currencies {
		c.Thresholds = append(c.Thresholds, dunning.Threshold{
			ID: core.NewID[core.DunningCampaignThresholdID](e.ids), Currency: money.MustCurrency(cur), Amount: 1000,
		})
	}
	return c
}
