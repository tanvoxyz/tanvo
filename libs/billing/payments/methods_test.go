package payments_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestDeleteMethod(t *testing.T) {
	t.Run("missing method", func(t *testing.T) {
		t.Log("source: spec/services/payment_methods/destroy_service_spec.rb:22")
		h, svc := newHarness(t)
		cust := h.Customer()
		_, err := svc.DeleteMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: cust.ID}, core.NewID[core.PaymentMethodID](h.IDs))
		wantNotFound(t, err, "payment_method")
	})
	t.Run("method", func(t *testing.T) {
		t.Log("source: spec/services/payment_methods/destroy_service_spec.rb:31")
		t.Log("source: spec/services/payment_methods/destroy_service_spec.rb:38")
		h, svc := newHarness(t)
		cust := h.Customer()
		m := storedMethod(h, cust, true)
		if _, err := svc.DeleteMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: cust.ID}, m.ID); err != nil {
			t.Fatal(err)
		}
		got := method(h, m.ID)
		if got.IsDefault || got.DeletedAt == nil || !got.DeletedAt.Equal(now) {
			t.Errorf("method: got = default %t deleted %v, want = not default, deleted at %v", got.IsDefault, got.DeletedAt, now)
		}
		_, err := svc.DeleteMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: cust.ID}, m.ID)
		wantNotFound(t, err, "payment_method")
	})
	t.Run("method of another customer", func(t *testing.T) {
		h, svc := newHarness(t)
		m := storedMethod(h, h.Customer(), true)
		_, err := svc.DeleteMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: h.Customer().ID}, m.ID)
		wantNotFound(t, err, "payment_method")
	})
}

func TestSetDefaultMethod(t *testing.T) {
	t.Run("methods", func(t *testing.T) {
		t.Log("source: spec/services/payment_methods/set_as_default_service_spec.rb:33")
		t.Log("source: spec/services/payment_methods/set_as_default_service_spec.rb:41")
		h, svc := newHarness(t)
		cust := h.Customer()
		m1, m2, m3 := storedMethod(h, cust, false), storedMethod(h, cust, true), storedMethod(h, cust, false)
		h.Clock.Advance(time.Second)
		if _, err := svc.SetDefaultMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ExternalID: cust.ExternalID}, m1.ID); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			id   core.PaymentMethodID
			want bool
		}{{m1.ID, true}, {m2.ID, false}, {m3.ID, false}} {
			if got := method(h, c.id).IsDefault; got != c.want {
				t.Errorf("default of %s: got = %t, want = %t", c.id, got, c.want)
			}
		}
		if got := method(h, m2.ID).UpdatedAt; !got.After(now) {
			t.Errorf("demoted updated at: got = %v, want = after %v", got, now)
		}
	})
	t.Run("missing method", func(t *testing.T) {
		t.Log("source: spec/services/payment_methods/set_as_default_service_spec.rb:72")
		h, svc := newHarness(t)
		cust := h.Customer()
		_, err := svc.SetDefaultMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: cust.ID}, core.NewID[core.PaymentMethodID](h.IDs))
		wantNotFound(t, err, "payment_method")
	})
	t.Run("missing customer", func(t *testing.T) {
		h, svc := newHarness(t)
		m := storedMethod(h, h.Customer(), false)
		_, err := svc.SetDefaultMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ExternalID: "unknown"}, m.ID)
		wantNotFound(t, err, "customer")
	})
}

func TestListMethods(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer()
	var ids []core.PaymentMethodID
	for range 3 {
		ids = append(ids, storedMethod(h, cust, false).ID)
		h.Clock.Advance(time.Second)
	}
	deleted := storedMethod(h, cust, false)
	if _, err := svc.DeleteMethod(h.Ctx, h.OrgID(), payments.CustomerRef{ID: cust.ID}, deleted.ID); err != nil {
		t.Fatal(err)
	}
	storedMethod(h, h.Customer(), false)
	ref := payments.CustomerRef{ID: cust.ID}
	page, err := svc.ListMethods(h.Ctx, h.OrgID(), ref, core.PageRequest{Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != ids[2] || page.Items[1].ID != ids[1] || page.NextToken == "" {
		t.Fatalf("first page: got = %d items %q, want = the two newest and a token", len(page.Items), page.NextToken)
	}
	page, err = svc.ListMethods(h.Ctx, h.OrgID(), ref, core.PageRequest{Size: 2, Token: page.NextToken})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ids[0] || page.NextToken != "" {
		t.Fatalf("second page: got = %v %v, want = the oldest", page.Items, err)
	}
	_, err = svc.ListMethods(h.Ctx, h.OrgID(), payments.CustomerRef{ExternalID: "unknown"}, core.PageRequest{})
	wantNotFound(t, err, "customer")
}
