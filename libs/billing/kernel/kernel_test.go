package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestAssignNumber(t *testing.T) {
	tests := []struct {
		name      string
		numbering organization.DocumentNumbering
		want      []string
	}{
		{"per customer", organization.DocumentNumberingPerCustomer, []string{"ORG-001-001", "ORG-001-002"}},
		{"per billing entity", organization.DocumentNumberingPerBillingEntity, []string{"ORG-202403-001", "ORG-202403-002"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := billingtest.New(t, billingtest.Date(2024, time.March, 5))
			h.UpdateBillingEntity(func(b *organization.BillingEntity) { b.DocumentNumbering = tt.numbering })
			c := h.Customer()
			for i, want := range tt.want {
				h.Do(func(ctx context.Context, r port.Repos) error {
					env, err := kernel.LoadCustomerEnv(ctx, r, h.OrgID(), c.ID, h.BillingEntity.ID)
					if err != nil {
						return err
					}
					inv := kernel.NewInvoice(h.Deps, env, kernel.InvoiceKind{Type: invoice.TypeOneOff}, c.Currency, h.Now())
					if err := kernel.AssignNumber(ctx, r, env, inv, h.Now()); err != nil {
						return err
					}
					if inv.Number != want {
						t.Errorf("invoice %d: got = %s, want = %s", i, inv.Number, want)
					}
					return nil
				})
			}
		})
	}
}

func TestSubscriptionEnvDates(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2024, time.March, 1))
	c := h.Customer()
	p := h.Plan(period.Monthly, 1000)
	s := h.Subscription(c, p, billingtest.SubscribedAt(billingtest.Date(2024, time.February, 10)))
	h.Do(func(ctx context.Context, r port.Repos) error {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, h.OrgID(), s.ID)
		if err != nil {
			return err
		}
		d, err := env.Dates(h.Now(), period.Billing)
		if err != nil {
			return err
		}
		b, ok := d.Boundaries()
		if !ok {
			t.Fatalf("no boundaries")
		}
		if want := billingtest.Date(2024, time.February, 10); !b.Subscription.From.Equal(want) {
			t.Errorf("from: got = %s, want = %s", b.Subscription.From, want)
		}
		if want := billingtest.Date(2024, time.March, 1); !b.Subscription.To.Equal(want) {
			t.Errorf("to: got = %s, want = %s", b.Subscription.To, want)
		}
		return nil
	})
}

func TestTxRollsBackCredits(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2024, time.March, 1))
	var inv core.InvoiceID
	err := h.Deps.Tx.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
		inv = core.NewID[core.InvoiceID](h.IDs)
		if err := h.Deps.Credits.Create(ctx, &kernel.Credit{OrganizationID: h.OrgID(), InvoiceID: inv}); err != nil {
			return err
		}
		return billingtest.ErrFake
	})
	if err == nil {
		t.Fatalf("got = nil, want = error")
	}
	got, err := h.Credits.ListByInvoice(h.Ctx, h.OrgID(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("credits: got = %d, want = 0", len(got))
	}
}

// source: app/models/invoice.rb:141 (sequenced scope: customer and billing entity)
// The per-customer sequence belongs to the (customer, billing entity) pair:
// moving the customer to another entity neither continues nor restarts the
// sequence of the entity it left.
func TestAssignNumberPerCustomerAndEntity(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2024, time.March, 5))
	a := h.BillingEntity
	b := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", h.Now())
	b.DocumentNumberPrefix = "OTH"
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, b) })
	c := h.Customer()
	number := func(be *organization.BillingEntity) int64 {
		t.Helper()
		var seq int64
		h.Do(func(ctx context.Context, r port.Repos) error {
			env, err := kernel.LoadCustomerEnv(ctx, r, h.OrgID(), c.ID, be.ID)
			if err != nil {
				return err
			}
			inv := kernel.NewInvoice(h.Deps, env, kernel.InvoiceKind{Type: invoice.TypeOneOff}, c.Currency, h.Now())
			if err := kernel.AssignNumber(ctx, r, env, inv, h.Now()); err != nil {
				return err
			}
			seq = *inv.SequentialID
			return nil
		})
		return seq
	}
	steps := []struct {
		name string
		be   *organization.BillingEntity
		move bool
		want int64
	}{
		{"first on the customer's entity", a, false, 1},
		{"first on an override entity", b, false, 1},
		{"after the move, on the new entity", b, true, 2},
		{"after the move, on the entity left", a, false, 2},
	}
	for _, st := range steps {
		if st.move {
			h.UpdateCustomer(c, func(x *customer.Customer) { x.BillingEntityID = b.ID })
		}
		if got := number(st.be); got != st.want {
			t.Errorf("%s: sequential id got = %d, want = %d", st.name, got, st.want)
		}
	}
}
