package dunning_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/dunning"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	coredunning "github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// campaign creates a campaign with ten days between attempts and ten
// attempts unless opts say otherwise.
func campaign(t *testing.T, s *dunning.Service, h *billingtest.Harness, code string, thresholds []dunning.ThresholdInput, opts ...func(*dunning.CreateInput)) *coredunning.Campaign {
	t.Helper()
	in := input(code, code, thresholds...)
	in.DaysBetweenAttempts, in.MaxAttempts = 10, 10
	for _, o := range opts {
		o(&in)
	}
	return create(t, s, h, in)
}

func limits(days, maxAttempts int) func(*dunning.CreateInput) {
	return func(in *dunning.CreateInput) { in.DaysBetweenAttempts, in.MaxAttempts = days, maxAttempts }
}

func threshold(cur money.Currency, amount int64) dunning.ThresholdInput {
	return dunning.ThresholdInput{Currency: cur, Amount: amount}
}

func applyToEntity(h *billingtest.Harness, c *coredunning.Campaign) {
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = c.ID })
}

// dueInvoice stores an overdue finalized invoice of c ready for payment
// processing.
func dueInvoice(h *billingtest.Harness, c *customer.Customer, cur money.Currency, total int64, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	return h.StoredInvoice(c, append([]func(*invoice.Invoice){func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusFinalized
		inv.PaymentOverdue = true
		inv.ReadyForPaymentProcessing = true
		inv.Currency = cur
		inv.TotalAmount = total
	}}, opts...)...)
}

func inEntity(be *organization.BillingEntity) func(*invoice.Invoice) {
	return func(inv *invoice.Invoice) { inv.BillingEntityID = be.ID }
}

func eurCustomer(opts ...func(*customer.Customer)) []func(*customer.Customer) {
	return append([]func(*customer.Customer){func(c *customer.Customer) { c.Currency = eur }}, opts...)
}

func takeJobs(t *testing.T, h *billingtest.Harness, kind string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	for _, j := range h.Store.TakeJobs() {
		if j.Kind == kind {
			out = append(out, j.Args)
		}
	}
	return out
}

// attemptJobs takes the enqueued attempts as "campaign:currency@entity".
func attemptJobs(t *testing.T, h *billingtest.Harness, s *dunning.Service) []string {
	t.Helper()
	var out []string
	for _, raw := range takeJobs(t, h, kernel.JobProcessDunningAttempt) {
		var a kernel.DunningAttemptArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("decode attempt: %v", err)
		}
		c, err := s.Get(context.Background(), h.OrgID(), a.DunningCampaignID)
		if err != nil {
			t.Fatalf("get campaign: %v", err)
		}
		cur := "?"
		for _, th := range c.Thresholds {
			if th.ID == a.ThresholdID {
				cur = th.Currency.Code()
			}
		}
		entity := "other"
		if a.BillingEntityID == h.BillingEntity.ID {
			entity = "default"
		}
		out = append(out, c.Code+":"+cur+"@"+entity)
	}
	slices.Sort(out)
	return out
}

func finishedEvents(h *billingtest.Harness) []webhook.Event {
	var out []webhook.Event
	for _, e := range h.Emitted.Events() {
		if e.Type == webhook.EventTypeDunningCampaignFinished {
			out = append(out, e)
		}
	}
	return out
}

func TestProcessCampaigns(t *testing.T) {
	ctx := context.Background()
	h, s := newService(t)
	eligible := h.Customer(eurCustomer()...)
	// source: spec/services/dunning_campaigns/bulk_process_service_spec.rb:41
	dueInvoice(h, eligible, eur, 5000)
	dueInvoice(h, eligible, eur, 100)
	// source: spec/services/dunning_campaigns/bulk_process_service_spec.rb:327
	withoutOverdue := h.Customer(eurCustomer()...)
	h.StoredInvoice(withoutOverdue, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized; inv.TotalAmount = 10000 })
	excluded := h.Customer(eurCustomer(func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })...)
	dueInvoice(h, excluded, eur, 5000)
	selfBilled := h.Customer(eurCustomer()...)
	dueInvoice(h, selfBilled, eur, 5000, func(inv *invoice.Invoice) { inv.SelfBilled = true })
	archived := h.Customer(eurCustomer()...)
	dueInvoice(h, archived, eur, 5000)
	h.UpdateCustomer(archived, func(c *customer.Customer) { c.DeletedAt = new(h.Now()) })
	h.Store.TakeJobs()

	for range 2 {
		if err := s.ProcessCampaigns(ctx, h.OrgID(), h.Now()); err != nil {
			t.Fatalf("process campaigns: %v", err)
		}
	}
	var got []core.CustomerID
	for _, raw := range takeJobs(t, h, kernel.JobProcessDunningCustomer) {
		var a kernel.CustomerArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = append(got, a.CustomerID)
	}
	// One job per customer while one is pending (unique :until_executed,
	// app/jobs/dunning_campaigns/process_customer_job.rb:7).
	if want := []core.CustomerID{eligible.ID}; !slices.Equal(got, want) {
		t.Errorf("customer jobs: got = %v, want = %v", got, want)
	}
	if err := s.ProcessCampaigns(ctx, h.OrgID(), time.Time{}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("zero instant: got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

func TestProcessCustomer(t *testing.T) {
	ctx := context.Background()
	type env struct {
		h     *billingtest.Harness
		s     *dunning.Service
		other *organization.BillingEntity
	}
	ago := func(e env, d time.Duration) *time.Time { return new(e.h.Now().Add(-d)) }
	day := 24 * time.Hour
	// entityCampaign applies a campaign "be" with thresholds to the default
	// billing entity.
	entityCampaign := func(t *testing.T, e env, thresholds []dunning.ThresholdInput, opts ...func(*dunning.CreateInput)) *coredunning.Campaign {
		c := campaign(t, e.s, e.h, "be", thresholds, opts...)
		applyToEntity(e.h, c)
		return c
	}
	tests := []struct {
		name     string
		source   string
		setup    func(t *testing.T, e env) *customer.Customer
		want     []string
		attempts map[string]int
		finished bool
	}{
		{
			name:   "entity campaign threshold reached",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:111",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)})
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				dueInvoice(e.h, c, eur, 100)
				return c
			},
			want:     []string{"be:EUR@default"},
			attempts: map[string]int{"EUR": 1},
		},
		{
			name:   "maximum attempts reached",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:151",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)}, limits(10, 5))
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.DunningCampaignAttempts = map[string]int{"EUR": 5} })...)
				dueInvoice(e.h, c, eur, 5100)
				return c
			},
			attempts: map[string]int{"EUR": 5},
		},
		{
			name:   "not enough days since the last attempt",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:170",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)}, limits(4, 10))
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.LastDunningCampaignAttemptAt = ago(e, 3*day) })...)
				dueInvoice(e.h, c, eur, 5100)
				return c
			},
		},
		{
			name:   "enough days since the last attempt",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:189",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)}, limits(4, 10))
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.LastDunningCampaignAttemptAt = ago(e, 4*day+time.Second) })...)
				dueInvoice(e.h, c, eur, 5100)
				return c
			},
			want:     []string{"be:EUR@default"},
			attempts: map[string]int{"EUR": 1},
		},
		{
			name:   "balance below threshold",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:203",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)})
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				return c
			},
		},
		{
			name:   "no threshold in the balance's currency",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:223",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(gbp, 1)})
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				return c
			},
		},
		{
			name:   "customer campaign overrides the entity's",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:263",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)})
				own := campaign(t, e.s, e.h, "own", []dunning.ThresholdInput{threshold(eur, 4999)})
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.AppliedDunningCampaignID = own.ID })...)
				dueInvoice(e.h, c, eur, 5000)
				return c
			},
			want:     []string{"own:EUR@default"},
			attempts: map[string]int{"EUR": 1},
		},
		{
			name:   "customer campaign below its threshold",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:276",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 99)})
				own := campaign(t, e.s, e.h, "own", []dunning.ThresholdInput{threshold(eur, 4999)})
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.AppliedDunningCampaignID = own.ID })...)
				dueInvoice(e.h, c, eur, 100)
				return c
			},
		},
		{
			name:   "excluded customer",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:312",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5099)})
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })...)
				dueInvoice(e.h, c, eur, 5100)
				return c
			},
		},
		{
			name:   "excluded after dispatch",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:65",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 5000)}, limits(10, 1))
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				e.h.UpdateCustomer(c, func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })
				return c
			},
		},
		{
			name:   "no campaign applies",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:498",
			setup: func(t *testing.T, e env) *customer.Customer {
				campaign(t, e.s, e.h, "unused", []dunning.ThresholdInput{threshold(eur, 1)})
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				return c
			},
		},
		{
			name:   "deleted campaign",
			source: "tanvo",
			setup: func(t *testing.T, e env) *customer.Customer {
				c := entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 1)})
				if _, err := e.s.Delete(context.Background(), e.h.OrgID(), c.ID); err != nil {
					t.Fatalf("delete: %v", err)
				}
				cu := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, cu, eur, 5000)
				return cu
			},
		},
		{
			name:   "both currencies exceed their thresholds",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:539",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 2000)})
				c := e.h.Customer()
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, eur, 2500)
				return c
			},
			want:     []string{"be:EUR@default", "be:USD@default"},
			attempts: map[string]int{"USD": 1, "EUR": 1},
		},
		{
			name:   "only one currency exceeds its threshold",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:560",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 2000)})
				c := e.h.Customer()
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, eur, 1500)
				return c
			},
			want:     []string{"be:USD@default"},
			attempts: map[string]int{"USD": 1},
		},
		{
			name:   "currency without a threshold",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:575",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 2000)})
				c := e.h.Customer()
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, gbp, 10000)
				return c
			},
			want:     []string{"be:USD@default"},
			attempts: map[string]int{"USD": 1},
		},
		{
			name:   "maximum attempts reached, enough days passed",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:691",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 500)}, limits(5, 3))
				c := e.h.Customer(eurCustomer(func(c *customer.Customer) {
					c.DunningCampaignAttempts = map[string]int{"EUR": 3}
					c.LastDunningCampaignAttemptAt = ago(e, 6*day)
				})...)
				dueInvoice(e.h, c, eur, 600)
				return c
			},
			attempts: map[string]int{"EUR": 3},
		},
		{
			name:   "last attempt finishes the campaign",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:722",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 500)}, limits(10, 1))
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 600)
				return c
			},
			want:     []string{"be:EUR@default"},
			attempts: map[string]int{"EUR": 1},
			finished: true,
		},
		{
			name:   "one currency maxed, the other not",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:758",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 2000)}, limits(10, 2))
				c := e.h.Customer(func(c *customer.Customer) { c.DunningCampaignAttempts = map[string]int{"USD": 2} })
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, eur, 2500)
				return c
			},
			want:     []string{"be:EUR@default"},
			attempts: map[string]int{"USD": 2, "EUR": 1},
		},
		{
			name:   "one currency maxed, the other below its threshold",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:804",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 8000)}, limits(1, 3))
				c := e.h.Customer(func(c *customer.Customer) {
					c.DunningCampaignAttempts = map[string]int{"USD": 3}
					c.LastDunningCampaignAttemptAt = ago(e, 2*day)
				})
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, eur, 5207)
				return c
			},
			attempts: map[string]int{"USD": 3},
		},
		{
			name:   "one currency maxed, nothing overdue in the other",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:843",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 2000)}, limits(1, 2))
				c := e.h.Customer(func(c *customer.Customer) {
					c.DunningCampaignAttempts = map[string]int{"USD": 2}
					c.LastDunningCampaignAttemptAt = ago(e, 2*day)
				})
				dueInvoice(e.h, c, usd, 5000)
				return c
			},
			attempts: map[string]int{"USD": 2},
		},
		{
			name:   "overdue invoices across billing entities",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:868",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 4000)}, limits(10, 2))
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				dueInvoice(e.h, c, eur, 6000, inEntity(e.other))
				return c
			},
			want:     []string{"be:EUR@default", "be:EUR@other"},
			attempts: map[string]int{"EUR": 1},
		},
		{
			name:   "billing entity of invoices not ready for payment processing",
			source: "tanvo",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(eur, 4000)})
				c := e.h.Customer(eurCustomer()...)
				dueInvoice(e.h, c, eur, 5000)
				dueInvoice(e.h, c, eur, 6000, inEntity(e.other), func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false })
				return c
			},
			want:     []string{"be:EUR@default"},
			attempts: map[string]int{"EUR": 1},
		},
		{
			name:   "all currencies maxed",
			source: "spec/services/dunning_campaigns/bulk_process_service_spec.rb:912",
			setup: func(t *testing.T, e env) *customer.Customer {
				entityCampaign(t, e, []dunning.ThresholdInput{threshold(usd, 4000), threshold(eur, 8000)}, limits(1, 3))
				c := e.h.Customer(func(c *customer.Customer) {
					c.DunningCampaignAttempts = map[string]int{"USD": 3, "EUR": 3}
					c.LastDunningCampaignAttemptAt = ago(e, 2*day)
				})
				dueInvoice(e.h, c, usd, 5000)
				dueInvoice(e.h, c, eur, 10000)
				return c
			},
			attempts: map[string]int{"USD": 3, "EUR": 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := newService(t)
			e := env{h: h, s: s, other: otherEntity(h, core.DunningCampaignID{})}
			c := tt.setup(t, e)
			before := h.ReloadCustomer(c.ID)
			h.Store.TakeJobs()
			h.Emitted.Reset()
			if err := s.ProcessCustomer(ctx, h.OrgID(), c.ID); err != nil {
				t.Fatalf("process customer: %v", err)
			}
			if got := attemptJobs(t, h, s); !slices.Equal(got, tt.want) {
				t.Errorf("attempts (%s): got = %v, want = %v", tt.source, got, tt.want)
			}
			after := h.ReloadCustomer(c.ID)
			if !maps.Equal(after.DunningCampaignAttempts, tt.attempts) {
				t.Errorf("counters (%s): got = %v, want = %v", tt.source, after.DunningCampaignAttempts, tt.attempts)
			}
			switch {
			case len(tt.want) > 0 && (after.LastDunningCampaignAttemptAt == nil || !after.LastDunningCampaignAttemptAt.Equal(h.Now())):
				t.Errorf("last attempt (%s): got = %v, want = %v", tt.source, after.LastDunningCampaignAttemptAt, h.Now())
			case len(tt.want) == 0 && !equalTime(after.LastDunningCampaignAttemptAt, before.LastDunningCampaignAttemptAt):
				t.Errorf("last attempt (%s): got = %v, want = unchanged %v", tt.source, after.LastDunningCampaignAttemptAt, before.LastDunningCampaignAttemptAt)
			}
			if got := len(finishedEvents(h)) > 0; got != tt.finished {
				t.Errorf("finished (%s): got = %v, want = %v", tt.source, got, tt.finished)
			}
		})
	}
}

func equalTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func TestProcessCustomerFinishedPayload(t *testing.T) {
	// source: spec/services/dunning_campaigns/bulk_process_service_spec.rb:722
	ctx := context.Background()
	h, s := newService(t)
	c := campaign(t, s, h, "dun", []dunning.ThresholdInput{threshold(eur, 500)}, limits(10, 1))
	applyToEntity(h, c)
	cu := h.Customer(eurCustomer()...)
	dueInvoice(h, cu, eur, 600)
	dueInvoice(h, cu, usd, 300, func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false })
	if err := s.ProcessCustomer(ctx, h.OrgID(), cu.ID); err != nil {
		t.Fatalf("process customer: %v", err)
	}
	evs := finishedEvents(h)
	if len(evs) != 1 || evs[0].ObjectType != "dunning_campaign" || evs[0].ObjectID != cu.ID.UUID() {
		t.Fatalf("finished events: got = %+v, want = one on the customer", evs)
	}
	// source: app/serializers/v1/dunning_campaign_finished_serializer.rb:5-14
	var got map[string]any
	if err := json.Unmarshal(evs[0].Payload, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	want := map[string]any{
		"external_customer_id": cu.ExternalID, "dunning_campaign_code": "dun",
		"overdue_balance_cents": 600.0, "overdue_balance_currency": "EUR",
		"overdue_balances": []any{
			map[string]any{"currency": "EUR", "amount_cents": 600.0},
			map[string]any{"currency": "USD", "amount_cents": 300.0},
		},
		"customer_external_id": cu.ExternalID,
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("payload: got = %s, want = %s", g, w)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func requests(t *testing.T, h *billingtest.Harness, c core.CustomerID) []*payment.Request {
	t.Helper()
	var out []*payment.Request
	h.Do(func(ctx context.Context, r port.Repos) error {
		p, err := r.PaymentRequests.List(ctx, h.OrgID(), payment.RequestFilter{CustomerID: c}, core.PageRequest{Size: core.MaxPageSize})
		out = p.Items
		return err
	})
	return out
}

func TestProcessAttempt(t *testing.T) {
	ctx := context.Background()
	type fixture struct {
		h      *billingtest.Harness
		s      *dunning.Service
		c      *coredunning.Campaign
		cu     *customer.Customer
		invs   map[string]*invoice.Invoice
		entity core.BillingEntityID
	}
	// setup mirrors process_attempt_service_spec.rb:6-45: a EUR threshold
	// of 99_00 applied by the default billing entity; the customer has an
	// invoice not overdue, an overdue one of 99_00, one in USD, and another
	// customer an overdue one.
	setup := func(t *testing.T, amount int64, opts ...func(*customer.Customer)) fixture {
		h, s := newService(t)
		c := campaign(t, s, h, "dun", []dunning.ThresholdInput{threshold(eur, amount)})
		applyToEntity(h, c)
		cu := h.Customer(eurCustomer(opts...)...)
		f := fixture{h: h, s: s, c: c, cu: cu, entity: h.BillingEntity.ID, invs: map[string]*invoice.Invoice{}}
		f.invs["not overdue"] = h.StoredInvoice(cu, func(inv *invoice.Invoice) {
			inv.Status, inv.Currency, inv.TotalAmount, inv.ReadyForPaymentProcessing = invoice.StatusFinalized, eur, 9900, true
		})
		f.invs["overdue"] = dueInvoice(h, cu, eur, 9900)
		f.invs["usd"] = dueInvoice(h, cu, usd, 9900)
		dueInvoice(h, h.Customer(eurCustomer()...), eur, 9900)
		return f
	}
	run := func(t *testing.T, f fixture) error {
		t.Helper()
		return f.s.ProcessAttempt(ctx, f.h.OrgID(), kernel.DunningAttemptArgs{
			CustomerID: f.cu.ID, DunningCampaignID: f.c.ID, ThresholdID: f.c.Thresholds[0].ID, BillingEntityID: f.entity,
		})
	}
	requested := func(t *testing.T, f fixture) [][]core.InvoiceID {
		var out [][]core.InvoiceID
		for _, q := range requests(t, f.h, f.cu.ID) {
			if q.DunningCampaignID != f.c.ID {
				t.Errorf("request campaign: got = %v, want = %v", q.DunningCampaignID, f.c.ID)
			}
			out = append(out, q.InvoiceIDs)
		}
		return out
	}

	t.Run("creates a payment request of the overdue invoices", func(t *testing.T) {
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:53
		f := setup(t, 9900)
		if err := run(t, f); err != nil {
			t.Fatalf("process attempt: %v", err)
		}
		got := requested(t, f)
		if len(got) != 1 || !slices.Equal(got[0], []core.InvoiceID{f.invs["overdue"].ID}) {
			t.Errorf("requests: got = %v, want = [[%v]]", got, f.invs["overdue"].ID)
		}
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:68
		if cu := f.h.ReloadCustomer(f.cu.ID); len(cu.DunningCampaignAttempts) != 0 || cu.LastDunningCampaignAttemptAt != nil {
			t.Errorf("counters: got = %v at %v, want = untouched", cu.DunningCampaignAttempts, cu.LastDunningCampaignAttemptAt)
		}
	})

	for _, tt := range []struct {
		name   string
		source string
		amount int64
		mutate func(t *testing.T, f *fixture)
	}{
		{"threshold not reached", "spec/services/dunning_campaigns/process_attempt_service_spec.rb:79", 9901, nil},
		{"campaign not applicable anymore", "spec/services/dunning_campaigns/process_attempt_service_spec.rb:100", 9900, func(t *testing.T, f *fixture) {
			own := campaign(t, f.s, f.h, "own", []dunning.ThresholdInput{threshold(eur, 1000)})
			f.h.UpdateCustomer(f.cu, func(c *customer.Customer) { c.AppliedDunningCampaignID = own.ID })
		}},
		{"customer excluded", "spec/services/dunning_campaigns/process_attempt_service_spec.rb:111", 9900, func(t *testing.T, f *fixture) {
			f.h.UpdateCustomer(f.cu, func(c *customer.Customer) { c.ExcludeFromDunningCampaign = true })
		}},
		{"threshold removed", "tanvo", 9900, func(t *testing.T, f *fixture) {
			gone := core.NewID[core.DunningCampaignThresholdID](f.h.IDs)
			f.c = &coredunning.Campaign{ID: f.c.ID, Thresholds: []coredunning.Threshold{{ID: gone}}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setup(t, tt.amount)
			if tt.mutate != nil {
				tt.mutate(t, &f)
			}
			if err := run(t, f); err != nil {
				t.Fatalf("process attempt: %v", err)
			}
			if got := requested(t, f); len(got) != 0 {
				t.Errorf("requests (%s): got = %v, want = none", tt.source, got)
			}
		})
	}

	t.Run("only invoices in the threshold's currency", func(t *testing.T) {
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:145
		f := setup(t, 10000)
		usdInv := dueInvoice(f.h, f.cu, usd, 20000)
		eurInv := dueInvoice(f.h, f.cu, eur, 15000)
		if err := run(t, f); err != nil {
			t.Fatalf("process attempt: %v", err)
		}
		got := requested(t, f)
		if len(got) != 1 || !slices.Contains(got[0], eurInv.ID) || slices.Contains(got[0], usdInv.ID) {
			t.Errorf("requests: got = %v, want = one with %v and without %v", got, eurInv.ID, usdInv.ID)
		}
	})

	t.Run("only invoices of the billing entity", func(t *testing.T) {
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:174
		f := setup(t, 9900)
		other := otherEntity(f.h, core.DunningCampaignID{})
		matching := dueInvoice(f.h, f.cu, eur, 9900)
		elsewhere := dueInvoice(f.h, f.cu, eur, 9900, inEntity(other))
		if err := run(t, f); err != nil {
			t.Fatalf("process attempt: %v", err)
		}
		got := requested(t, f)
		if len(got) != 1 || !slices.Contains(got[0], matching.ID) || slices.Contains(got[0], elsewhere.ID) {
			t.Errorf("requests: got = %v, want = one with %v and without %v", got, matching.ID, elsewhere.ID)
		}
	})

	t.Run("only invoices ready for payment processing", func(t *testing.T) {
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:189
		f := setup(t, 9900)
		dueInvoice(f.h, f.cu, eur, 9900, func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false })
		if err := run(t, f); err != nil {
			t.Fatalf("process attempt: %v", err)
		}
		got := requested(t, f)
		if len(got) != 1 || !slices.Equal(got[0], []core.InvoiceID{f.invs["overdue"].ID}) {
			t.Errorf("requests: got = %v, want = [[%v]]", got, f.invs["overdue"].ID)
		}
	})

	t.Run("payment request failure fails the attempt", func(t *testing.T) {
		// source: spec/services/dunning_campaigns/process_attempt_service_spec.rb:122
		f := setup(t, 9900)
		f.entity = otherEntity(f.h, core.DunningCampaignID{}).ID
		var nf *core.NotFoundError
		if err := run(t, f); !errors.As(err, &nf) {
			t.Errorf("error: got = %v, want = invoice not found", err)
		}
	})
}

// TestDunningScenario runs the hourly dunning of a customer over days.
func TestDunningScenario(t *testing.T) {
	// source: spec/scenarios/dunning/dunning_campaign_v1_spec.rb:90
	ctx := context.Background()
	h, s := newService(t)
	h.Clock.Set(billingtest.Date(2025, time.January, 1, 10))
	c := campaign(t, s, h, "dun", []dunning.ThresholdInput{threshold(eur, 15000), threshold(usd, 10000)}, limits(2, 2))
	applyToEntity(h, c)
	cu := h.Customer()
	dueInvoice(h, cu, eur, 14900)
	dueInvoice(h, cu, usd, 12000)

	perform := func(at time.Time) {
		t.Helper()
		h.Clock.Set(at)
		if err := s.ProcessCampaigns(ctx, h.OrgID(), at); err != nil {
			t.Fatalf("process campaigns: %v", err)
		}
		for len(h.Store.Jobs()) > 0 {
			for _, j := range h.Store.TakeJobs() {
				var err error
				switch j.Kind {
				case kernel.JobProcessDunningCustomer:
					var a kernel.CustomerArgs
					if err = json.Unmarshal(j.Args, &a); err == nil {
						err = s.ProcessCustomer(ctx, j.OrganizationID, a.CustomerID)
					}
				case kernel.JobProcessDunningAttempt:
					var a kernel.DunningAttemptArgs
					if err = json.Unmarshal(j.Args, &a); err == nil {
						err = s.ProcessAttempt(ctx, j.OrganizationID, a)
					}
				}
				if err != nil {
					t.Fatalf("%s: %v", j.Kind, err)
				}
			}
		}
	}
	check := func(day int, attempts map[string]int, perCurrency map[string]int, finished int) {
		t.Helper()
		got := h.ReloadCustomer(cu.ID).DunningCampaignAttempts
		if !maps.Equal(got, attempts) {
			t.Errorf("day %d attempts: got = %v, want = %v", day, got, attempts)
		}
		counts := map[string]int{}
		for _, q := range requests(t, h, cu.ID) {
			counts[q.Currency.Code()]++
		}
		if !maps.Equal(counts, perCurrency) {
			t.Errorf("day %d requests: got = %v, want = %v", day, counts, perCurrency)
		}
		if got := len(finishedEvents(h)); got != finished {
			t.Errorf("day %d finished webhooks: got = %d, want = %d", day, got, finished)
		}
	}

	// Day 3: EUR below its threshold (149 < 150), USD dunned.
	perform(billingtest.Date(2025, time.January, 3, 10))
	check(3, map[string]int{"USD": 1}, map[string]int{"USD": 1}, 0)
	// Day 5: within the days between attempts.
	perform(billingtest.Date(2025, time.January, 5, 10))
	check(5, map[string]int{"USD": 1}, map[string]int{"USD": 1}, 0)
	h.Clock.Set(billingtest.Date(2025, time.January, 5, 18))
	dueInvoice(h, cu, eur, 1000)
	// Day 6: both currencies reach their thresholds.
	perform(billingtest.Date(2025, time.January, 6, 10))
	check(6, map[string]int{"USD": 2, "EUR": 1}, map[string]int{"USD": 2, "EUR": 1}, 0)
	// Day 9: USD is maxed, EUR dunned and maxed: the campaign finished.
	perform(billingtest.Date(2025, time.January, 9, 10))
	check(9, map[string]int{"USD": 2, "EUR": 2}, map[string]int{"USD": 2, "EUR": 2}, 1)
	// Day 12: nothing more.
	perform(billingtest.Date(2025, time.January, 12, 10))
	check(12, map[string]int{"USD": 2, "EUR": 2}, map[string]int{"USD": 2, "EUR": 2}, 1)
}
