package dunning_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/dunning"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	coredunning "github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var now = billingtest.Date(2026, time.March, 10, 12)

var (
	usd = money.MustCurrency("USD")
	eur = money.MustCurrency("EUR")
	gbp = money.MustCurrency("GBP")
	chf = money.MustCurrency("CHF")
)

func newService(t *testing.T) (*billingtest.Harness, *dunning.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, dunning.New(h.Deps)
}

func fieldCodes(err error) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		out = append(out, e.Field+":"+e.Code)
	}
	return out
}

func input(code, name string, thresholds ...dunning.ThresholdInput) dunning.CreateInput {
	return dunning.CreateInput{Code: code, Name: name, DaysBetweenAttempts: 1, MaxAttempts: 3, Thresholds: thresholds}
}

func create(t *testing.T, s *dunning.Service, h *billingtest.Harness, in dunning.CreateInput) *coredunning.Campaign {
	t.Helper()
	c, err := s.Create(context.Background(), h.OrgID(), in)
	if err != nil {
		t.Fatalf("create %q: %v", in.Code, err)
	}
	return c
}

// attempted is a customer with four attempts in USD made yesterday.
func attempted(campaign core.DunningCampaignID, be core.BillingEntityID) func(*customer.Customer) {
	return func(c *customer.Customer) {
		yesterday := now.AddDate(0, 0, -1)
		c.AppliedDunningCampaignID = campaign
		if !be.IsZero() {
			c.BillingEntityID = be
		}
		c.DunningCampaignAttempts = map[string]int{"USD": 4}
		c.LastDunningCampaignAttemptAt = &yesterday
	}
}

func overdueInvoice(h *billingtest.Harness, c *customer.Customer, cur money.Currency, total int64) {
	h.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusFinalized
		inv.PaymentOverdue = true
		inv.Currency = cur
		inv.TotalAmount = total
	})
}

func otherEntity(h *billingtest.Harness, campaign core.DunningCampaignID) *organization.BillingEntity {
	be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", now)
	be.AppliedDunningCampaignID = campaign
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	return be
}

func reset(c *customer.Customer) bool {
	return len(c.DunningCampaignAttempts) == 0 && c.LastDunningCampaignAttemptAt == nil
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)

	// source: spec/services/dunning_campaigns/create_service_spec.rb:60
	in := input("dunning-campaign", "Dunning Campaign",
		dunning.ThresholdInput{Currency: usd, Amount: 10000}, dunning.ThresholdInput{Currency: eur, Amount: 20000})
	in.Description = new("Dunning Campaign Description")
	// source: spec/services/dunning_campaigns/create_service_spec.rb:72
	in.BCCEmails = []string{"earl@example.com"}
	c := create(t, s, h, in)
	got, err := s.Get(ctx, h.OrgID(), c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Code != "dunning-campaign" || got.MaxAttempts != 3 || len(got.Thresholds) != 2 || !slices.Equal(got.BCCEmails, in.BCCEmails) {
		t.Errorf("campaign: got = %+v, want = the input", got)
	}

	tests := []struct {
		name   string
		source string
		in     dunning.CreateInput
		want   []string
	}{
		{"code taken", "spec/services/dunning_campaigns/create_service_spec.rb:130", input("dunning-campaign", "Other", dunning.ThresholdInput{Currency: usd}), []string{"code:value_already_exist"}},
		{"no thresholds", "spec/services/dunning_campaigns/create_service_spec.rb:142", input("other", "Other"), []string{"thresholds:value_is_mandatory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Create(ctx, h.OrgID(), tt.in)
			if got := fieldCodes(err); !slices.Equal(got, tt.want) {
				t.Errorf("errors (%s): got = %v, want = %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()

	// source: spec/services/dunning_campaigns/update_service_spec.rb:119
	t.Run("fields and thresholds", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd, Amount: 100}))
		keep := c.Thresholds[0].ID
		got, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{
			Name:                core.Some("Updated Dunning Campaign"),
			Code:                core.Some("updated-dunning-campaign"),
			DaysBetweenAttempts: core.Some(12),
			MaxAttempts:         core.Some(34),
			Description:         core.Some(new("Updated Dunning Campaign Description")),
			Thresholds: core.Some([]dunning.ThresholdInput{
				{ID: keep, Currency: gbp, Amount: 99999}, {Currency: chf, Amount: 555},
			}),
		})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if got.Name != "Updated Dunning Campaign" || got.Code != "updated-dunning-campaign" || got.DaysBetweenAttempts != 12 || got.MaxAttempts != 34 {
			t.Errorf("campaign: got = %+v, want = the input", got)
		}
		if g, _ := got.Threshold("GBP"); g.ID != keep || g.Amount != 99999 {
			t.Errorf("GBP threshold: got = %+v, want = id %s amount 99999", g, keep)
		}
		if g, ok := got.Threshold("CHF"); !ok || g.Amount != 555 || g.ID == keep {
			t.Errorf("CHF threshold: got = %+v, want = a new threshold of amount 555", g)
		}
	})

	// source: spec/services/dunning_campaigns/update_service_spec.rb:479
	t.Run("thresholds left out are removed", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd, Amount: 100}, dunning.ThresholdInput{Currency: eur, Amount: 100}))
		keep := c.Thresholds[0].ID
		got, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Thresholds: core.Some([]dunning.ThresholdInput{
			{ID: keep, Currency: gbp, Amount: 99999}, {Currency: chf, Amount: 555},
		})})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if _, ok := got.Threshold("EUR"); ok || len(got.Thresholds) != 2 {
			t.Errorf("thresholds: got = %+v, want = GBP and CHF only", got.Thresholds)
		}
	})

	// source: spec/services/dunning_campaigns/update_service_spec.rb:402
	t.Run("no thresholds left", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd, Amount: 100}))
		got, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Thresholds: core.Some([]dunning.ThresholdInput{})})
		if err != nil || len(got.Thresholds) != 0 {
			t.Fatalf("update: got = %v, %v, want = no thresholds", got, err)
		}
	})

	// source: spec/services/dunning_campaigns/update_service_spec.rb:144
	t.Run("bcc emails reset", func(t *testing.T) {
		h, s := newService(t)
		in := input("dun", "Dun", dunning.ThresholdInput{Currency: usd})
		in.BCCEmails = []string{"earl@example.com"}
		c := create(t, s, h, in)
		got, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Name: core.Some("Updated"), BCCEmails: core.Some([]string{})})
		if err != nil || len(got.BCCEmails) != 0 {
			t.Fatalf("update: got = %v, %v, want = no bcc emails", got, err)
		}
	})

	// source: spec/services/dunning_campaigns/update_service_spec.rb:573
	t.Run("missing", func(t *testing.T) {
		h, s := newService(t)
		_, err := s.Update(ctx, h.OrgID(), core.NewID[core.DunningCampaignID](h.IDs), dunning.UpdateInput{Name: core.Some("x")})
		if !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("got = %v, want = not found", err)
		}
	})

	// The customers dunned by the campaign: assigned to it, falling back
	// to it through the default billing entity, and falling back to
	// another billing entity's campaign.
	type who int
	const (
		assigned who = iota
		defaulting
		another
	)
	tests := []struct {
		name      string
		source    string
		threshold int64
		// keep gives the first input the ID of the campaign's threshold.
		keep       bool
		thresholds []dunning.ThresholdInput
		invoice    int64
		who        who
		wantReset  bool
	}{
		{"amount no longer applies, assigned", "spec/services/dunning_campaigns/update_service_spec.rb:210", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 99999}}, 99998, assigned, true},
		{"amount no longer applies, defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:214", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 99999}}, 99998, defaulting, true},
		{"amount no longer applies, other entity", "spec/services/dunning_campaigns/update_service_spec.rb:218", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 99999}}, 99998, another, false},
		{"currency no longer applies, assigned", "spec/services/dunning_campaigns/update_service_spec.rb:249", 100, true, []dunning.ThresholdInput{{Currency: gbp, Amount: 100}}, 101, assigned, true},
		{"currency no longer applies, defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:253", 100, true, []dunning.ThresholdInput{{Currency: gbp, Amount: 100}}, 101, defaulting, true},
		{"currency no longer applies, other entity", "spec/services/dunning_campaigns/update_service_spec.rb:257", 100, true, []dunning.ThresholdInput{{Currency: gbp, Amount: 100}}, 101, another, false},
		{"amount still applies, assigned", "spec/services/dunning_campaigns/update_service_spec.rb:290", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 50}}, 101, assigned, false},
		{"amount still applies, defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:294", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 50}}, 101, defaulting, false},
		{"amount still applies, other entity", "spec/services/dunning_campaigns/update_service_spec.rb:298", 100, true, []dunning.ThresholdInput{{Currency: usd, Amount: 50}}, 101, another, false},
		{"threshold discarded, assigned", "spec/services/dunning_campaigns/update_service_spec.rb:420", 100, false, []dunning.ThresholdInput{}, 101, assigned, true},
		{"threshold discarded, defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:424", 100, false, []dunning.ThresholdInput{}, 101, defaulting, true},
		{"threshold discarded, other entity", "spec/services/dunning_campaigns/update_service_spec.rb:428", 100, false, []dunning.ThresholdInput{}, 101, another, false},
		{"threshold replaced, still applies, assigned", "spec/services/dunning_campaigns/update_service_spec.rb:460", 100, false, []dunning.ThresholdInput{{Currency: usd, Amount: 100}}, 101, assigned, false},
		{"threshold replaced, still applies, defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:464", 100, false, []dunning.ThresholdInput{{Currency: usd, Amount: 100}}, 101, defaulting, false},
		{"threshold replaced, still applies, other entity", "spec/services/dunning_campaigns/update_service_spec.rb:468", 100, false, []dunning.ThresholdInput{{Currency: usd, Amount: 100}}, 101, another, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := newService(t)
			c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd, Amount: tt.threshold}))
			h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
			var cust *customer.Customer
			switch tt.who {
			case assigned:
				h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = core.DunningCampaignID{} })
				cust = h.Customer(attempted(c.ID, core.BillingEntityID{}))
			case defaulting:
				cust = h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
			case another:
				other := create(t, s, h, input("other", "Other", dunning.ThresholdInput{Currency: usd, Amount: 1}))
				cust = h.Customer(attempted(core.DunningCampaignID{}, otherEntity(h, other.ID).ID))
			}
			overdueInvoice(h, cust, usd, tt.invoice)
			thresholds := slices.Clone(tt.thresholds)
			if tt.keep {
				thresholds[0].ID = c.Thresholds[0].ID
			}
			if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Thresholds: core.Some(thresholds)}); err != nil {
				t.Fatalf("update: %v", err)
			}
			if got := reset(h.ReloadCustomer(cust.ID)); got != tt.wantReset {
				t.Errorf("reset (%s): got = %v, want = %v", tt.source, got, tt.wantReset)
			}
		})
	}

	// source: spec/services/dunning_campaigns/update_service_spec.rb:350
	t.Run("overdue in another currency matching a threshold", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: eur, Amount: 100}, dunning.ThresholdInput{Currency: usd, Amount: 5000}))
		cust := h.Customer(attempted(c.ID, core.BillingEntityID{}))
		overdueInvoice(h, cust, usd, 4500)
		if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Thresholds: core.Some([]dunning.ThresholdInput{
			{ID: c.Thresholds[0].ID, Currency: eur, Amount: 100}, {ID: c.Thresholds[1].ID, Currency: usd, Amount: 4000},
		})}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if reset(h.ReloadCustomer(cust.ID)) {
			t.Errorf("reset: got = true, want = false")
		}
	})
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)
	c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd, Amount: 100}))
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
	assigned := h.Customer(attempted(c.ID, core.BillingEntityID{}))
	defaulting := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))

	if _, err := s.Delete(ctx, h.OrgID(), c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// source: spec/services/dunning_campaigns/destroy_service_spec.rb:59
	got := h.ReloadCustomer(assigned.ID)
	if !reset(got) || !got.AppliedDunningCampaignID.IsZero() {
		t.Errorf("assigned customer: got = %+v, want = reset without campaign", got)
	}
	if !reset(h.ReloadCustomer(defaulting.ID)) {
		t.Errorf("defaulting customer: got = not reset, want = reset")
	}
	// source: spec/services/dunning_campaigns/destroy_service_spec.rb:66
	if _, err := s.Get(ctx, h.OrgID(), c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get: got = %v, want = not found", err)
	}
	// source: spec/services/dunning_campaigns/destroy_service_spec.rb:83
	h.Do(func(ctx context.Context, r port.Repos) error {
		be, err := r.BillingEntities.Get(ctx, h.OrgID(), h.BillingEntity.ID)
		if err == nil && !be.AppliedDunningCampaignID.IsZero() {
			t.Errorf("billing entity campaign: got = %s, want = none", be.AppliedDunningCampaignID)
		}
		return err
	})
	// source: spec/services/dunning_campaigns/destroy_service_spec.rb:23
	if _, err := s.Delete(ctx, h.OrgID(), c.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete again: got = %v, want = not found", err)
	}
	// The code is free again.
	create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
}

func TestList(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)
	first := create(t, s, h, input("11", "defgh", dunning.ThresholdInput{Currency: usd}, dunning.ThresholdInput{Currency: gbp}))
	second := create(t, s, h, input("22", "abcde", dunning.ThresholdInput{Currency: chf}))
	third := create(t, s, h, input("33", "presuv", dunning.ThresholdInput{Currency: eur}))
	fourth := create(t, s, h, input("44", "qwerty", dunning.ThresholdInput{Currency: eur}, dunning.ThresholdInput{Currency: usd}))

	ids := func(f coredunning.Filter, size int) []core.DunningCampaignID {
		t.Helper()
		var out []core.DunningCampaignID
		for page := (core.PageRequest{Size: size}); ; {
			p, err := s.List(ctx, h.OrgID(), f, page)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			for _, c := range p.Items {
				out = append(out, c.ID)
			}
			if p.NextToken == "" {
				return out
			}
			page.Token = p.NextToken
		}
	}
	tests := []struct {
		name   string
		source string
		f      coredunning.Filter
		want   []core.DunningCampaignID
	}{
		{"by name", "spec/queries/dunning_campaigns_query_spec.rb:45", coredunning.Filter{}, []core.DunningCampaignID{second.ID, first.ID, third.ID, fourth.ID}},
		{"search", "spec/queries/dunning_campaigns_query_spec.rb:89", coredunning.Filter{Search: "de"}, []core.DunningCampaignID{second.ID, first.ID}},
		{"currency", "spec/queries/dunning_campaigns_query_spec.rb:133", coredunning.Filter{Currencies: []string{"GBP"}}, []core.DunningCampaignID{first.ID}},
		{"currencies", "spec/queries/dunning_campaigns_query_spec.rb:156", coredunning.Filter{Currencies: []string{"USD", "EUR"}}, []core.DunningCampaignID{first.ID, third.ID, fourth.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: spec/queries/dunning_campaigns_query_spec.rb:75
			if got := ids(tt.f, 2); !slices.Equal(got, tt.want) {
				t.Errorf("ids (%s): got = %v, want = %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestCustomersCount(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)
	c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
	other := create(t, s, h, input("other", "Other", dunning.ThresholdInput{Currency: usd}))
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
	h.Customer(attempted(c.ID, core.BillingEntityID{}))
	h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
	h.Customer(attempted(other.ID, core.BillingEntityID{}))
	h.Customer(func(cu *customer.Customer) { cu.ExcludeFromDunningCampaign = true })
	h.Customer(attempted(core.DunningCampaignID{}, otherEntity(h, other.ID).ID))

	// source: app/graphql/types/dunning_campaigns/object.rb:29-44
	for _, tt := range []struct {
		c    *coredunning.Campaign
		want int
	}{{c, 2}, {other, 2}} {
		got, err := s.CustomersCount(ctx, tt.c)
		if err != nil || got != tt.want {
			t.Errorf("customers count of %s: got = %d, %v, want = %d", tt.c.Code, got, err, tt.want)
		}
	}
}

// appliedToDefault returns the campaign of the default billing entity.
func appliedToDefault(t *testing.T, h *billingtest.Harness) core.DunningCampaignID {
	t.Helper()
	var id core.DunningCampaignID
	h.Do(func(ctx context.Context, r port.Repos) error {
		be, err := r.BillingEntities.GetDefault(ctx, h.OrgID())
		if err != nil {
			return err
		}
		id = be.AppliedDunningCampaignID
		return nil
	})
	return id
}

func TestCreateAppliedToOrganization(t *testing.T) {
	ctx := context.Background()

	// source: spec/services/dunning_campaigns/create_service_spec.rb:87
	t.Run("not applied", func(t *testing.T) {
		h, s := newService(t)
		prev := create(t, s, h, input("prev", "Prev", dunning.ThresholdInput{Currency: usd}))
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = prev.ID })
		cust := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
		create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
		if got := appliedToDefault(t, h); got != prev.ID {
			t.Errorf("applied: got = %s, want = %s", got, prev.ID)
		}
		if reset(h.ReloadCustomer(cust.ID)) {
			t.Errorf("reset: got = true, want = false")
		}
	})

	// source: spec/services/dunning_campaigns/create_service_spec.rb:96
	// source: spec/services/dunning_campaigns/create_service_spec.rb:110
	// source: spec/services/dunning_campaigns/create_service_spec.rb:117
	t.Run("applied", func(t *testing.T) {
		h, s := newService(t)
		prev := create(t, s, h, input("prev", "Prev", dunning.ThresholdInput{Currency: usd}))
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = prev.ID })
		cust := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
		in := input("dun", "Dun", dunning.ThresholdInput{Currency: usd})
		in.AppliedToOrganization = true
		c := create(t, s, h, in)
		if got := appliedToDefault(t, h); got != c.ID {
			t.Errorf("applied: got = %s, want = %s", got, c.ID)
		}
		if !reset(h.ReloadCustomer(cust.ID)) {
			t.Errorf("reset: got = false, want = true")
		}
		if got, err := s.AppliedToOrganization(ctx, c); err != nil || !got {
			t.Errorf("AppliedToOrganization: got = %v, %v, want = true", got, err)
		}
		if got, err := s.AppliedToOrganization(ctx, prev); err != nil || got {
			t.Errorf("AppliedToOrganization of the previous campaign: got = %v, %v, want = false", got, err)
		}
	})
}

func TestUpdateAppliedToOrganization(t *testing.T) {
	ctx := context.Background()

	// source: spec/services/dunning_campaigns/update_service_spec.rb:496
	t.Run("false", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
		assigned := h.Customer(attempted(c.ID, core.BillingEntityID{}))
		defaulting := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
		other := create(t, s, h, input("other", "Other", dunning.ThresholdInput{Currency: usd}))
		another := h.Customer(attempted(core.DunningCampaignID{}, otherEntity(h, other.ID).ID))
		if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{AppliedToOrganization: core.Some(false)}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if got := appliedToDefault(t, h); !got.IsZero() {
			t.Errorf("applied: got = %s, want = none", got)
		}
		for _, tt := range []struct {
			name   string
			source string
			id     core.CustomerID
			want   bool
		}{
			{"defaulting", "spec/services/dunning_campaigns/update_service_spec.rb:501", defaulting.ID, true},
			{"other entity", "spec/services/dunning_campaigns/update_service_spec.rb:506", another.ID, false},
			{"assigned", "spec/services/dunning_campaigns/update_service_spec.rb:511", assigned.ID, false},
		} {
			if got := reset(h.ReloadCustomer(tt.id)); got != tt.want {
				t.Errorf("reset %s (%s): got = %v, want = %v", tt.name, tt.source, got, tt.want)
			}
		}
	})

	// source: spec/services/dunning_campaigns/update_service_spec.rb:528
	// source: spec/services/dunning_campaigns/update_service_spec.rb:542
	// source: spec/services/dunning_campaigns/update_service_spec.rb:548
	t.Run("true", func(t *testing.T) {
		h, s := newService(t)
		prev := create(t, s, h, input("prev", "Prev", dunning.ThresholdInput{Currency: usd}))
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = prev.ID })
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
		cust := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
		if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{AppliedToOrganization: core.Some(true)}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if got := appliedToDefault(t, h); got != c.ID {
			t.Errorf("applied: got = %s, want = %s", got, c.ID)
		}
		if !reset(h.ReloadCustomer(cust.ID)) {
			t.Errorf("reset: got = false, want = true")
		}
	})

	// Applying the campaign already applied changes nothing
	// (app/services/dunning_campaigns/update_service.rb:100).
	t.Run("unchanged", func(t *testing.T) {
		h, s := newService(t)
		c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: usd}))
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
		cust := h.Customer(attempted(core.DunningCampaignID{}, core.BillingEntityID{}))
		if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{AppliedToOrganization: core.Some(true)}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if reset(h.ReloadCustomer(cust.ID)) {
			t.Errorf("reset: got = true, want = false")
		}
	})
}

// source: spec/services/dunning_campaigns/update_service_spec.rb:389
func TestUpdateThresholdCurrencyStillApplies(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)
	c := create(t, s, h, input("dun", "Dun", dunning.ThresholdInput{Currency: chf, Amount: 100}, dunning.ThresholdInput{Currency: usd, Amount: 100}))
	cust := h.Customer(attempted(c.ID, core.BillingEntityID{}))
	overdueInvoice(h, cust, usd, 101)
	if _, err := s.Update(ctx, h.OrgID(), c.ID, dunning.UpdateInput{Thresholds: core.Some([]dunning.ThresholdInput{
		{ID: c.Thresholds[0].ID, Currency: gbp, Amount: 99999}, {ID: c.Thresholds[1].ID, Currency: usd, Amount: 100},
	})}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if reset(h.ReloadCustomer(cust.ID)) {
		t.Errorf("reset: got = true, want = false")
	}
}
