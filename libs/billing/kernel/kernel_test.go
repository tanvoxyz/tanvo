package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
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

func TestNewInvoiceNetPaymentTerm(t *testing.T) {
	tests := []struct {
		name, source string
		customer     *int
		entity       int
		want         int
		wantDue      core.Date
	}{
		{"billing entity default", "spec/services/invoices/create_generating_service_spec.rb:38", nil, 0, 0, core.NewDate(2024, time.March, 5)},
		{"customer", "spec/services/invoices/create_generating_service_spec.rb:86", new(3), 30, 3, core.NewDate(2024, time.March, 8)},
		{"billing entity", "spec/models/customer_spec.rb:928", nil, 30, 30, core.NewDate(2024, time.April, 4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h := billingtest.New(t, billingtest.Date(2024, time.March, 5))
			h.UpdateBillingEntity(func(b *organization.BillingEntity) { b.NetPaymentTermDays = tt.entity })
			c := h.Customer(func(c *customer.Customer) { c.NetPaymentTermDays = tt.customer })
			h.Do(func(ctx context.Context, r port.Repos) error {
				env, err := kernel.LoadCustomerEnv(ctx, r, h.OrgID(), c.ID, h.BillingEntity.ID)
				if err != nil {
					return err
				}
				inv := kernel.NewInvoice(h.Deps, env, kernel.InvoiceKind{Type: invoice.TypeOneOff}, c.Currency, h.Now())
				if inv.NetPaymentTermDays != tt.want {
					t.Errorf("net payment term: got = %d, want = %d", inv.NetPaymentTermDays, tt.want)
				}
				if inv.PaymentDueDate != tt.wantDue {
					t.Errorf("payment due date: got = %v, want = %v", inv.PaymentDueDate, tt.wantDue)
				}
				return nil
			})
		})
	}
}

// Finalizing a draft redates it from the customer's current settings: the
// due date takes the applicable net payment term at that moment, while the
// invoice keeps its snapshot.
func TestRedateOnFinalization(t *testing.T) {
	drafted := core.NewDate(2024, time.March, 1)
	tests := []struct {
		name, source string
		recurring    bool
		adjustment   core.IssuingDateAdjustment
		term         *int
		wantIssuing  core.Date
		wantDue      core.Date
	}{
		{
			"non-recurring", "spec/services/invoices/refresh_draft_and_finalize_service_spec.rb:91",
			false, core.IssuingDateAdjustmentKeepAnchor, nil, core.NewDate(2024, time.March, 5), core.NewDate(2024, time.March, 5),
		},
		{
			"recurring, keep anchor", "spec/services/invoices/refresh_draft_and_finalize_service_spec.rb:115",
			true, core.IssuingDateAdjustmentKeepAnchor, nil, drafted, drafted,
		},
		{
			"recurring, align with finalization date", "spec/services/invoices/refresh_draft_and_finalize_service_spec.rb:123",
			true, core.IssuingDateAdjustmentAlignWithFinalizationDate, nil, core.NewDate(2024, time.March, 5), core.NewDate(2024, time.March, 5),
		},
		{
			"term changed since drafting", "tanvo: app/services/invoices/refresh_draft_and_finalize_service.rb:86",
			false, "", new(10), core.NewDate(2024, time.March, 5), core.NewDate(2024, time.March, 15),
		},
		{
			"keep anchor, term changed since drafting", "tanvo: app/services/invoices/refresh_draft_and_finalize_service.rb:74-87",
			true, core.IssuingDateAdjustmentKeepAnchor, new(10), drafted, core.NewDate(2024, time.March, 11),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h := billingtest.New(t, billingtest.Date(2024, time.March, 5))
			c := h.Customer(func(c *customer.Customer) {
				c.IssuingDateAdjustment = tt.adjustment
				c.NetPaymentTermDays = tt.term
			})
			h.Do(func(ctx context.Context, r port.Repos) error {
				env, err := kernel.LoadCustomerEnv(ctx, r, h.OrgID(), c.ID, h.BillingEntity.ID)
				if err != nil {
					return err
				}
				inv := &invoice.Invoice{IssuingDate: drafted, PaymentDueDate: drafted}
				kernel.RedateOnFinalization(inv, env, kernel.InvoiceKind{Type: invoice.TypeSubscription, Recurring: tt.recurring}, h.Now())
				if inv.IssuingDate != tt.wantIssuing {
					t.Errorf("issuing date: got = %v, want = %v", inv.IssuingDate, tt.wantIssuing)
				}
				if inv.PaymentDueDate != tt.wantDue {
					t.Errorf("payment due date: got = %v, want = %v", inv.PaymentDueDate, tt.wantDue)
				}
				if inv.NetPaymentTermDays != 0 {
					t.Errorf("net payment term snapshot: got = %d, want = 0", inv.NetPaymentTermDays)
				}
				return nil
			})
		})
	}
}

// A regenerated invoice is issued on the regeneration date and due after
// the customer's current net payment term.
// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:307
func TestRedateRegenerated(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2023, time.March, 10))
	c := h.Customer(func(c *customer.Customer) { c.NetPaymentTermDays = new(30) })
	h.Do(func(ctx context.Context, r port.Repos) error {
		env, err := kernel.LoadCustomerEnv(ctx, r, h.OrgID(), c.ID, h.BillingEntity.ID)
		if err != nil {
			return err
		}
		inv := &invoice.Invoice{IssuingDate: core.NewDate(2023, time.April, 2), PaymentDueDate: core.NewDate(2023, time.April, 2)}
		kernel.RedateRegenerated(inv, env, h.Now())
		if want := core.NewDate(2023, time.March, 10); inv.IssuingDate != want {
			t.Errorf("issuing date: got = %v, want = %v", inv.IssuingDate, want)
		}
		if want := core.NewDate(2023, time.April, 9); inv.PaymentDueDate != want {
			t.Errorf("payment due date: got = %v, want = %v", inv.PaymentDueDate, want)
		}
		return nil
	})
}

// The pay-in-advance interval is the charges period of the fee's billing
// instant as the subscription is when the document renders: a termination
// after billing ends it.
// source: app/models/fee.rb:342 (Fee#date_boundaries)
func TestPayInAdvanceInterval(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2024, time.March, 5))
	c := h.Customer()
	p := h.Plan(period.Monthly, 1000)
	m := h.Metric("calls", catalog.AggregationTypeCount, "")
	ch := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.PayInAdvance = true })
	s := h.Subscription(c, p, billingtest.SubscribedAt(billingtest.Date(2024, time.March, 1)))
	fee := &invoice.Fee{
		ID: core.NewID[core.FeeID](h.IDs), OrganizationID: h.OrgID(), SubscriptionID: s.ID, ChargeID: ch.ID, Type: core.FeeTypeCharge,
		Properties: invoice.FeeProperties{Timestamp: billingtest.Date(2024, time.March, 5)},
	}
	interval := func(f *invoice.Fee) (period.Period, bool) {
		t.Helper()
		var got period.Period
		var ok bool
		h.Do(func(ctx context.Context, r port.Repos) error {
			var err error
			got, ok, err = kernel.PayInAdvanceInterval(ctx, r, f, ch, nil)
			return err
		})
		return got, ok
	}
	got, ok := interval(fee)
	if want := (period.Period{From: billingtest.Date(2024, time.March, 1), To: billingtest.Date(2024, time.April, 1)}); !ok || !got.From.Equal(want.From) || !got.To.Equal(want.To) {
		t.Errorf("PayInAdvanceInterval() got = %v, %v, want = %v, true", got, ok, want)
	}

	advance := *fee
	advance.PayInAdvance = true
	if _, ok := interval(&advance); ok {
		t.Errorf("PayInAdvanceInterval() of an invoiceable pay-in-advance fee got = ok, want = none")
	}

	terminated := billingtest.Date(2024, time.March, 20)
	h.UpdateSubscription(s, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, &terminated
	})
	got, ok = interval(fee)
	if !ok || !got.From.Equal(billingtest.Date(2024, time.March, 1)) || got.To.After(billingtest.Date(2024, time.March, 21)) {
		t.Errorf("PayInAdvanceInterval() after termination got = %v, %v, want = the period ending at the termination", got, ok)
	}
}
