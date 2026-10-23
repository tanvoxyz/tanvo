package plans_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// amountJobs returns the pending update_plan_amount jobs.
func amountJobs(h *billingtest.Harness) []port.Job {
	return slices.DeleteFunc(h.Store.Jobs(), func(j port.Job) bool { return j.Kind != kernel.JobUpdatePlanAmount })
}

// pendingAfter is a subscription option making it the pending next
// subscription of prev.
func pendingAfter(prev *subscription.Subscription) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) {
		s.PreviousSubscriptionID = prev.ID
		s.SubscriptionAt = prev.SubscriptionAt.AddDate(0, 1, 0)
	}
}

// draftOf creates a draft invoice billing the subscription.
func draftOf(h *billingtest.Harness, c *customer.Customer, sub *subscription.Subscription, status invoice.Status) *invoice.Invoice {
	h.T.Helper()
	inv := h.StoredInvoice(c, func(i *invoice.Invoice) { i.Status = status })
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.InvoiceSubscriptions.Create(ctx, &invoice.Subscription{
			ID: core.NewID[core.InvoiceSubscriptionID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
			SubscriptionID: sub.ID, Reason: invoice.InvoicingReasonSubscriptionPeriodic, Timestamp: h.Now(), CreatedAt: h.Now(),
		})
	})
	return inv
}

func TestUpdate(t *testing.T) {
	// source: spec/services/plans/update_service_spec.rb:169
	t.Run("updates a plan", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{
			Name: core.Some("Updated plan name"), Code: core.Some("new_plan"), Interval: core.Some(period.Monthly),
			Amount: core.Some(int64(200)), Currency: core.Some(eur), InvoiceDisplayName: core.Some(new("Updated")),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Name != "Updated plan name" || got.Code != "new_plan" || got.Amount != 200 || got.Currency.Code() != "EUR" {
			t.Errorf("plan = %+v, want = updated", got)
		}
		if got, want := h.Webhooks(), []webhook.EventType{webhook.EventTypePlanUpdated}; !slices.Equal(got, want) {
			t.Errorf("webhooks = %v, want = %v", got, want)
		}
	})

	// source: spec/services/plans/update_service_spec.rb:674
	t.Run("plan not found", func(t *testing.T) {
		h, e := setup(t)
		deleted := h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted })
		for _, id := range []core.PlanID{core.NewID[core.PlanID](h.IDs), deleted.ID} {
			if _, err := e.Plans.Update(h.Ctx, h.OrgID(), id, plans.UpdateInput{Name: core.Some("x")}); !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Update(%s) error = %v, want = not found", id, err)
			}
		}
	})

	// source: spec/services/plans/update_service_spec.rb:686
	t.Run("validation error", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		_, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("")})
		if !hasFieldError(err, "name", core.CodeRequired) {
			t.Errorf("Update() error = %v, want = name value_is_mandatory", err)
		}
	})

	// source: spec/services/plans/update_service_spec.rb:779, spec/services/plans/update_service_spec.rb:1496
	t.Run("refuses missing chargeables", func(t *testing.T) {
		tests := []struct {
			name string
			in   func(m *catalog.BillableMetric) plans.UpdateInput
			want string
		}{
			// source: spec/services/plans/update_service_spec.rb:782
			{"metric of another organization", func(m *catalog.BillableMetric) plans.UpdateInput {
				return plans.UpdateInput{Name: core.Some("n"), Charges: core.Some([]plans.ChargeInput{{BillableMetric: refs.ByID(m.ID)}})}
			}, "billable_metrics_not_found"},
			// source: spec/services/plans/update_service_spec.rb:1511
			{"unknown add-on", func(*catalog.BillableMetric) plans.UpdateInput {
				return plans.UpdateInput{Name: core.Some("n"), FixedCharges: core.Some([]plans.FixedChargeInput{{AddOn: refs.ByCode("unknown")}})}
			}, "add_ons_not_found"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 100)
				other := core.NewID[core.OrganizationID](h.IDs)
				m := h.Metric("other_calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) { m.OrganizationID = other })
				_, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, tt.in(m))
				var nf *core.NotFoundError
				if !errors.As(err, &nf) || nf.Error() != tt.want {
					t.Fatalf("Update() error = %v, want = %s", err, tt.want)
				}
				if got := reloadPlan(h, p.ID).Name; got != p.Name {
					t.Errorf("name = %q, want = %q unchanged", got, p.Name)
				}
			})
		}
	})

	// source: spec/services/plans/update_service_spec.rb:196-256
	t.Run("flags the draft invoices for refresh", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		other := h.Plan(period.Monthly, 100)
		c := h.Customer()
		sub := h.Subscription(c, p)
		terminated := h.Subscription(c, p, func(s *subscription.Subscription) {
			s.Status, s.TerminatedAt = subscription.StatusTerminated, new(h.Now())
		})
		draft := draftOf(h, c, sub, invoice.StatusDraft)
		termDraft := draftOf(h, c, terminated, invoice.StatusDraft)
		finalized := draftOf(h, c, sub, invoice.StatusFinalized)
		otherDraft := draftOf(h, c, h.Subscription(c, other), invoice.StatusDraft)
		if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("n")}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		for _, tt := range []struct {
			name string
			inv  *invoice.Invoice
			want bool
		}{
			{"draft", draft, true},
			// source: spec/services/plans/update_service_spec.rb:223
			{"draft of a terminated subscription", termDraft, true},
			// source: spec/services/plans/update_service_spec.rb:231
			{"finalized", finalized, false},
			// source: spec/services/plans/update_service_spec.rb:241
			{"draft of another plan", otherDraft, false},
		} {
			if got := h.Invoice(tt.inv.ID).RefreshRequested; got != tt.want {
				t.Errorf("%s: refresh requested = %v, want = %v", tt.name, got, tt.want)
			}
		}
	})

	// source: spec/services/plans/update_service_spec.rb:278-326
	t.Run("cascade of the amount", func(t *testing.T) {
		tests := []struct {
			name     string
			cascade  bool
			children []int64
			want     int
		}{
			// source: spec/services/plans/update_service_spec.rb:290
			{"cascade without override plans", true, nil, 0},
			// source: spec/services/plans/update_service_spec.rb:300
			{"cascade to an override plan already changed", true, []int64{50}, 0},
			// source: spec/services/plans/update_service_spec.rb:308
			{"cascade to untouched override plans", true, []int64{100}, 1},
			// source: spec/services/plans/update_service_spec.rb:320
			{"no cascade", false, []int64{100}, 0},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 100)
				for _, a := range tt.children {
					h.Plan(period.Monthly, a, overrideOf(p))
				}
				if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Amount: core.Some(int64(200)), Cascade: tt.cascade}); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				if got := len(amountJobs(h)); got != tt.want {
					t.Errorf("update_plan_amount jobs = %d, want = %d", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/plans/update_service_spec.rb:359-470, spec/services/plans/update_usage_thresholds_service_spec.rb
	t.Run("usage thresholds", func(t *testing.T) {
		tests := []struct {
			name     string
			existing bool
			in       core.Optional[[]plans.ThresholdInput]
			want     []int64
		}{
			// source: spec/services/plans/update_usage_thresholds_service_spec.rb:50
			{"creates thresholds", false, core.Some([]plans.ThresholdInput{{Amount: new(int64(1000)), ThresholdDisplayName: new("Threshold 1")}}), []int64{1000}},
			// source: spec/services/plans/update_service_spec.rb:396
			{"replaces the thresholds", true, core.Some([]plans.ThresholdInput{{Amount: new(int64(1)), ThresholdDisplayName: new("Other threshold")}, {Amount: new(int64(2))}}), []int64{1, 2}},
			// source: spec/services/plans/update_service_spec.rb:389
			{"rewrites a threshold of the same amount", true, core.Some([]plans.ThresholdInput{{Amount: new(int64(500))}}), []int64{500}},
			// source: spec/services/plans/update_service_spec.rb:409
			{"clears the thresholds", true, core.Some([]plans.ThresholdInput{}), nil},
			// source: spec/services/plans/update_service_spec.rb:415
			{"leaves the thresholds", true, core.Optional[[]plans.ThresholdInput]{}, []int64{500}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 100)
				if tt.existing {
					h.UsageThreshold(p, 500, false)
				}
				if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{UsageThresholds: tt.in}); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				var got []int64
				for _, u := range thresholds(h, p.ID) {
					got = append(got, u.Amount)
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("thresholds = %v, want = %v", got, tt.want)
				}
			})
		}
	})

	// source: spec/services/plans/update_service_spec.rb:497-630
	t.Run("amount changes", func(t *testing.T) {
		// source: spec/services/plans/update_service_spec.rb:513
		t.Run("updates the plan", func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 100)
			h.Subscription(h.Customer(), p)
			got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("Updated plan name"), Amount: core.Some(int64(5))})
			if err != nil || got.Amount != 5 || got.Name != "Updated plan name" {
				t.Errorf("Update() = %v, %v, want = 5", got, err)
			}
		})

		// source: spec/services/plans/update_service_spec.rb:529
		t.Run("cancels the downgrades that became upgrades", func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 100)
			pendingPlan := h.Plan(period.Monthly, 10)
			c := h.Customer()
			sub := h.Subscription(c, p)
			next := h.Subscription(c, pendingPlan, pendingAfter(sub))
			if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Amount: core.Some(int64(5))}); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got := reloadSubscription(h, next.ID).Status; got != subscription.StatusCanceled {
				t.Errorf("pending subscription status = %s, want = canceled", got)
			}
		})

		// source: spec/services/plans/update_service_spec.rb:565
		t.Run("upgrades the pending subscriptions that became upgrades", func(t *testing.T) {
			h, e := setup(t)
			original := h.Plan(period.Monthly, 150)
			p := h.Plan(period.Monthly, 100)
			c := h.Customer()
			sub := h.Subscription(c, original)
			pending := h.Subscription(c, p, pendingAfter(sub))
			got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Amount: core.Some(int64(200))})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			// source: spec/services/plans/update_service_spec.rb:575
			if got.Amount != 200 {
				t.Errorf("amount = %d, want = 200", got.Amount)
			}
			if st := reloadSubscription(h, sub.ID).Status; st != subscription.StatusTerminated {
				t.Errorf("previous subscription status = %s, want = terminated", st)
			}
			if st := reloadSubscription(h, pending.ID).Status; st != subscription.StatusCanceled {
				t.Errorf("pending subscription status = %s, want = canceled", st)
			}
			active := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
				return r.Subscriptions.ListByCustomer(ctx, h.OrgID(), c.ID, subscription.StatusActive)
			})
			if len(active) != 1 || active[0].PlanID != p.ID || !equalPtr(active[0].Name, pending.Name) {
				t.Errorf("active subscriptions = %v, want = one on %s named %v", active, p.ID, deref(pending.Name))
			}
		})

		// source: spec/services/plans/update_service_spec.rb:606
		t.Run("leaves a pending subscription without a previous one", func(t *testing.T) {
			h, e := setup(t)
			p := h.Plan(period.Monthly, 100)
			pending := h.Subscription(h.Customer(), p, billingtest.SubscribedAt(h.Now().AddDate(0, 1, 0)))
			if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Amount: core.Some(int64(200))}); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if st := reloadSubscription(h, pending.ID).Status; st != subscription.StatusPending {
				t.Errorf("status = %s, want = pending", st)
			}
		})
	})

	// source: spec/services/plans/update_service_spec.rb:790-940
	t.Run("minimum commitment", func(t *testing.T) {
		tests := []struct {
			name     string
			existing bool
			in       core.Optional[*plans.CommitmentInput]
			want     *int64
			wantName *string
		}{
			// source: spec/services/plans/update_service_spec.rb:795
			{"creates", false, core.Some(&plans.CommitmentInput{Amount: core.Some(int64(1000)), InvoiceDisplayName: core.Some(new("Minimum"))}), new(int64(1000)), new("Minimum")},
			// source: spec/services/plans/update_service_spec.rb:815
			{"absent creates none", false, core.Optional[*plans.CommitmentInput]{}, nil, nil},
			// source: spec/services/plans/update_service_spec.rb:835
			{"blank creates none", false, core.Some(&plans.CommitmentInput{}), nil, nil},
			// source: spec/services/plans/update_service_spec.rb:861
			{"updates", true, core.Some(&plans.CommitmentInput{Amount: core.Some(int64(2000)), InvoiceDisplayName: core.Some(new("Updated"))}), new(int64(2000)), new("Updated")},
			// source: spec/services/plans/update_service_spec.rb:885
			{"updates the given fields only", true, core.Some(&plans.CommitmentInput{Amount: core.Some(int64(2000))}), new(int64(2000)), new("Old")},
			// source: spec/services/plans/update_service_spec.rb:905
			{"absent keeps", true, core.Optional[*plans.CommitmentInput]{}, new(int64(500)), new("Old")},
			// source: spec/services/plans/update_service_spec.rb:925
			{"blank deletes", true, core.Some(&plans.CommitmentInput{}), nil, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 100)
				if tt.existing {
					h.Commitment(p, 500, func(c *catalog.Commitment) { c.InvoiceDisplayName = new("Old") })
				}
				if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{MinimumCommitment: tt.in}); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				c := commitment(h, p.ID)
				switch {
				case tt.want == nil && c != nil:
					t.Errorf("commitment = %+v, want = none", c)
				case tt.want != nil && c == nil:
					t.Errorf("commitment = none, want = %d", *tt.want)
				case tt.want != nil && (c.Amount != *tt.want || !equalPtr(c.InvoiceDisplayName, tt.wantName)):
					t.Errorf("commitment = %d %v, want = %d %v", c.Amount, deref(c.InvoiceDisplayName), *tt.want, deref(tt.wantName))
				}
			})
		}
	})

	// source: spec/services/plans/update_service_spec.rb:1266
	t.Run("locked fields of a plan with subscriptions", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		h.Subscription(h.Customer(), p)
		got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Name: core.Some("Updated plan name"), Amount: core.Some(int64(200))})
		if err != nil || got.Name != "Updated plan name" {
			t.Fatalf("Update() = %v, %v, want = renamed", got, err)
		}
		// lago ignores the locked fields of an attached plan
		// (update_service.rb:39-49); tanvo refuses them.
		_, err = e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Code: core.Some("new_plan"), Interval: core.Some(period.Yearly)})
		for _, f := range []string{"code", "interval"} {
			if !hasFieldError(err, f, core.CodeImmutable) {
				t.Errorf("Update() error = %v, want = %s value_is_immutable", err, f)
			}
		}
		if got := reloadPlan(h, p.ID); got.Code != p.Code || got.Interval != period.Monthly {
			t.Errorf("plan = %s %s, want = unchanged", got.Code, got.Interval)
		}
	})

	// source: spec/services/plans/update_service_spec.rb:1320-1480
	t.Run("bill monthly settings", func(t *testing.T) {
		tests := []struct {
			name     string
			interval period.Interval
			in       core.Optional[*bool]
			want     *bool
		}{
			// source: spec/services/plans/update_service_spec.rb:1330
			{"yearly with the flag", period.Yearly, core.Some(new(true)), new(true)},
			// source: spec/services/plans/update_service_spec.rb:1345
			{"yearly without the flag", period.Yearly, core.Optional[*bool]{}, new(false)},
			// source: spec/services/plans/update_service_spec.rb:1361
			{"semiannual with the flag", period.Semiannual, core.Some(new(true)), new(true)},
			// source: spec/services/plans/update_service_spec.rb:1376
			{"semiannual without the flag", period.Semiannual, core.Optional[*bool]{}, new(false)},
			// source: spec/services/plans/update_service_spec.rb:1392
			{"monthly", period.Monthly, core.Some(new(true)), nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				h, e := setup(t)
				p := h.Plan(period.Monthly, 100)
				got, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{
					Interval: core.Some(tt.interval), BillChargesMonthly: tt.in, BillFixedChargesMonthly: tt.in,
				})
				if err != nil {
					t.Fatalf("Update() error = %v", err)
				}
				if !equalPtr(got.BillChargesMonthly, tt.want) || !equalPtr(got.BillFixedChargesMonthly, tt.want) {
					t.Errorf("bill monthly = %v %v, want = %v", deref(got.BillChargesMonthly), deref(got.BillFixedChargesMonthly), deref(tt.want))
				}
			})
		}
	})
}

func TestUpdateAmount(t *testing.T) {
	// source: spec/services/plans/update_amount_service_spec.rb:17
	t.Run("updates the subscription fee", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 111)
		if _, err := e.Plans.UpdateAmount(h.Ctx, h.OrgID(), p.ID, 222, 111); err != nil {
			t.Fatalf("UpdateAmount() error = %v", err)
		}
		if got := reloadPlan(h, p.ID).Amount; got != 222 {
			t.Errorf("amount = %d, want = 222", got)
		}
	})

	// source: spec/services/plans/update_amount_service_spec.rb:26
	t.Run("plan not found", func(t *testing.T) {
		h, e := setup(t)
		if _, err := e.Plans.UpdateAmount(h.Ctx, h.OrgID(), core.NewID[core.PlanID](h.IDs), 222, 111); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("UpdateAmount() error = %v, want = not found", err)
		}
	})

	// source: spec/services/plans/update_amount_service_spec.rb:37
	t.Run("skips an amount no longer expected", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 111)
		if _, err := e.Plans.UpdateAmount(h.Ctx, h.OrgID(), p.ID, 222, 333); err != nil {
			t.Fatalf("UpdateAmount() error = %v", err)
		}
		if got := reloadPlan(h, p.ID).Amount; got != 111 {
			t.Errorf("amount = %d, want = 111", got)
		}
	})

	// source: spec/services/plans/update_amount_service_spec.rb:61
	t.Run("upgrades the pending subscriptions", func(t *testing.T) {
		h, e := setup(t)
		original := h.Plan(period.Monthly, 150)
		p := h.Plan(period.Monthly, 100)
		c := h.Customer()
		sub := h.Subscription(c, original)
		h.Subscription(c, p, pendingAfter(sub))
		if _, err := e.Plans.UpdateAmount(h.Ctx, h.OrgID(), p.ID, 200, 100); err != nil {
			t.Fatalf("UpdateAmount() error = %v", err)
		}
		if st := reloadSubscription(h, sub.ID).Status; st != subscription.StatusTerminated {
			t.Errorf("previous subscription status = %s, want = terminated", st)
		}
	})

	t.Run("runs the cascaded job", func(t *testing.T) {
		h, e := setup(t)
		p := h.Plan(period.Monthly, 100)
		child := h.Plan(period.Monthly, 100, overrideOf(p))
		if _, err := e.Plans.Update(h.Ctx, h.OrgID(), p.ID, plans.UpdateInput{Amount: core.Some(int64(300)), Cascade: true}); err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		jobs := amountJobs(h)
		if len(jobs) != 1 {
			t.Fatalf("jobs = %d, want = 1", len(jobs))
		}
		if ok, err := e.HandleJob(h.Ctx, jobs[0]); !ok || err != nil {
			t.Fatalf("HandleJob() = %t, %v, want = true, nil", ok, err)
		}
		if got := reloadPlan(h, child.ID).Amount; got != 300 {
			t.Errorf("override amount = %d, want = 300", got)
		}
	})
}
