package billingentities_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// securityEntry is one recorded billing entity security log.
type securityEntry struct {
	event     string
	resources map[string]any
}

// securityLog records the entries in memory.
type securityLog struct{ entries []securityEntry }

func (l *securityLog) RecordBillingEntity(_ context.Context, _ core.OrganizationID, event string, resources map[string]any) error {
	l.entries = append(l.entries, securityEntry{event, resources})
	return nil
}

func TestSecurityLogs(t *testing.T) {
	t.Parallel()

	t.Run("create", func(t *testing.T) {
		// source: spec/services/billing_entities/create_service_spec.rb:46
		t.Parallel()
		h, s, _ := setup(t)
		l := &securityLog{}
		s.WithSecurityLog(l)
		if _, err := s.Create(h.Ctx, h.OrgID(), "billing-entity", billingentities.Input{Name: core.Some("Billing Entity")}); err != nil {
			t.Fatal(err)
		}
		want := []securityEntry{{billingentities.EventCreated, map[string]any{"billing_entity_name": "Billing Entity", "billing_entity_code": "billing-entity"}}}
		if !reflect.DeepEqual(l.entries, want) {
			t.Errorf("entries got = %v, want = %v", l.entries, want)
		}
		// source: spec/services/billing_entities/create_service_spec.rb:30
		// source: spec/services/billing_entities/create_service_spec.rb:102
		// Lago refuses the entity over its plan's cap, which Tanvo does not
		// have (parity §1.3); a refused creation logs nothing either way.
		l.entries = nil
		if _, err := s.Create(h.Ctx, h.OrgID(), "billing-entity", billingentities.Input{Name: core.Some("Again")}); err == nil {
			t.Fatal("Create() of a taken code got = nil, want an error")
		}
		if len(l.entries) != 0 {
			t.Errorf("entries after a failure got = %v, want none", l.entries)
		}
	})

	t.Run("update", func(t *testing.T) {
		// source: spec/services/billing_entities/update_service_spec.rb:84
		t.Parallel()
		h, s, _ := setup(t)
		l := &securityLog{}
		s.WithSecurityLog(l)
		be := h.BillingEntity
		if _, err := s.Update(h.Ctx, h.OrgID(), be.ID, billingentities.Input{
			Name: core.Some("New Name"), LegalName: core.Some(ptr("Foobar")), City: core.Some("Foobar"),
			Country: core.Some("fr"), InvoiceGracePeriodDays: core.Some(be.InvoiceGracePeriodDays),
		}); err != nil {
			t.Fatal(err)
		}
		if len(l.entries) != 1 || l.entries[0].event != billingentities.EventUpdated {
			t.Fatalf("entries got = %v, want one billing_entity.updated", l.entries)
		}
		res := l.entries[0].resources
		want := map[string]any{
			"billing_entity_name": "New Name", "billing_entity_code": be.Code,
			"name":       map[string]any{"deleted": be.Name, "added": "New Name"},
			"legal_name": map[string]any{"added": "Foobar"},
			"city":       map[string]any{"added": "Foobar"},
			"country":    map[string]any{"added": "FR"},
		}
		if be.Address.City != "" || be.Address.Country != "" || be.LegalName != nil {
			t.Fatalf("billing entity got = %+v, want no legal name, city or country", be)
		}
		if !reflect.DeepEqual(res, want) {
			t.Errorf("resources got = %v, want = %v", res, want)
		}
	})

	t.Run("update tax codes", func(t *testing.T) {
		// source: spec/services/billing_entities/update_service_spec.rb:100
		t.Parallel()
		h, s, _ := setup(t)
		h.Tax("old_tax", "10")
		h.Tax("kept_tax", "5")
		h.Tax("new_tax", "20")
		be := h.BillingEntity
		if _, err := s.Update(h.Ctx, h.OrgID(), be.ID, billingentities.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("old_tax"), refs.ByCode("kept_tax")})}); err != nil {
			t.Fatal(err)
		}
		l := &securityLog{}
		s.WithSecurityLog(l)
		if _, err := s.Update(h.Ctx, h.OrgID(), be.ID, billingentities.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("new_tax"), refs.ByCode("kept_tax")})}); err != nil {
			t.Fatal(err)
		}
		if len(l.entries) != 1 {
			t.Fatalf("entries got = %v, want one", l.entries)
		}
		got := l.entries[0].resources["tax_codes"]
		want := map[string]any{"deleted": []string{"old_tax"}, "added": []string{"new_tax"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("tax_codes got = %v, want = %v", got, want)
		}
	})

	t.Run("update of a missing entity", func(t *testing.T) {
		// source: spec/services/billing_entities/update_service_spec.rb:450
		t.Parallel()
		h, s, _ := setup(t)
		l := &securityLog{}
		s.WithSecurityLog(l)
		if _, err := s.Update(h.Ctx, h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), billingentities.Input{Name: core.Some("X")}); err == nil {
			t.Fatal("Update() of a missing entity got = nil, want an error")
		}
		if len(l.entries) != 0 {
			t.Errorf("entries got = %v, want none", l.entries)
		}
	})

	t.Run("dunning campaign", func(t *testing.T) {
		t.Parallel()
		h, s, _ := setup(t)
		l := &securityLog{}
		s.WithSecurityLog(l)
		c1, c2 := campaign(h, "first"), campaign(h, "second")
		be := h.BillingEntity
		set := func(c refs.Ref) {
			t.Helper()
			l.entries = nil
			if _, err := s.Update(h.Ctx, h.OrgID(), be.ID, billingentities.Input{AppliedDunningCampaign: core.Some(c)}); err != nil {
				t.Fatal(err)
			}
		}
		// source: spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:88
		set(refs.ByCode(c2.Code))
		want := []securityEntry{{billingentities.EventUpdated, map[string]any{
			"billing_entity_name": be.Name, "billing_entity_code": be.Code,
			"applied_dunning_campaign": map[string]any{"added": "second"},
		}}}
		if !reflect.DeepEqual(l.entries, want) {
			t.Errorf("entries got = %v, want = %v", l.entries, want)
		}
		// source: spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:135
		set(refs.ByCode(c2.Code))
		if len(l.entries) != 0 {
			t.Errorf("entries of an unchanged campaign got = %v, want none", l.entries)
		}
		set(refs.ByCode(c1.Code))
		got := l.entries[0].resources["applied_dunning_campaign"]
		if w := map[string]any{"deleted": "second", "added": "first"}; !reflect.DeepEqual(got, w) {
			t.Errorf("applied_dunning_campaign got = %v, want = %v", got, w)
		}
		set(refs.Ref{})
		got = l.entries[0].resources["applied_dunning_campaign"]
		if w := map[string]any{"deleted": "first"}; !reflect.DeepEqual(got, w) {
			t.Errorf("applied_dunning_campaign got = %v, want = %v", got, w)
		}
	})
}
