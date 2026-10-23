package billing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// enqueued returns the jobs of kind enqueued by committed transactions and
// not yet taken, without taking them.
func (s *scenario) enqueued(kind string) []port.Job {
	var out []port.Job
	for _, j := range s.Store.Jobs() {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	return out
}

// argsOf decodes the args of the jobs.
func argsOf[T any](t *testing.T, jobs []port.Job) []T {
	t.Helper()
	out := make([]T, len(jobs))
	for i, j := range jobs {
		if err := json.Unmarshal(j.Args, &out[i]); err != nil {
			t.Fatalf("decode %s args: %v", j.Kind, err)
		}
	}
	return out
}

func checkRuleCode(t *testing.T, err error, code string) {
	t.Helper()
	if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != code {
		t.Fatalf("err got = %v, want = %s", err, code)
	}
}

// source: spec/jobs/clock/mark_invoices_as_payment_overdue_job_spec.rb:12
func TestMarkOverdueInvoices(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	s := newScenario(t, now)
	c := s.Customer()
	today := core.DateOf(now, time.UTC)
	stored := func(opts ...func(*invoice.Invoice)) *invoice.Invoice {
		return s.StoredInvoice(c, append([]func(*invoice.Invoice){func(inv *invoice.Invoice) {
			inv.Status, inv.PaymentDueDate = invoice.StatusFinalized, today.AddDays(-1)
		}}, opts...)...)
	}
	overdue1 := stored()
	overdue2 := stored(func(inv *invoice.Invoice) { inv.PaymentDueDate = today.AddDays(-2) })
	stored(func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
	stored(func(inv *invoice.Invoice) { inv.PaymentStatus = core.PaymentStatusSucceeded })
	stored(func(inv *invoice.Invoice) { inv.PaymentDisputeLostAt = &now })
	stored(func(inv *invoice.Invoice) { inv.PaymentDueDate = core.Date{} })
	stored(func(inv *invoice.Invoice) { inv.PaymentDueDate = today.AddDays(1) })
	// Due today: its midnight is before the instant (payment_due_date <
	// Time.current).
	dueToday := stored(func(inv *invoice.Invoice) { inv.PaymentDueDate = today })
	// Already overdue.
	stored(func(inv *invoice.Invoice) { inv.PaymentOverdue = true })

	n, err := s.E.MarkOverdueInvoices(s.Ctx, s.OrgID(), now)
	s.must(err)
	jobs := s.enqueued(kernel.JobMarkInvoiceOverdue)
	var got []core.InvoiceID
	for _, a := range argsOf[kernel.InvoiceArgs](t, jobs) {
		got = append(got, a.InvoiceID)
	}
	want := []core.InvoiceID{overdue1.ID, overdue2.ID, dueToday.ID}
	slices.SortFunc(want, func(a, b core.InvoiceID) int { return a.Compare(b) })
	slices.SortFunc(got, func(a, b core.InvoiceID) int { return a.Compare(b) })
	if n != len(want) || !slices.Equal(got, want) {
		t.Fatalf("enqueued got = %d %v, want = %v", n, got, want)
	}
	for _, j := range jobs {
		var a kernel.InvoiceArgs
		s.must(json.Unmarshal(j.Args, &a))
		if j.UniqueKey != kernel.OverdueKey(a.InvoiceID) {
			t.Errorf("unique key got = %q, want = %q", j.UniqueKey, kernel.OverdueKey(a.InvoiceID))
		}
	}

	// At midnight, an invoice due that day is not yet overdue.
	s.Store.TakeJobs()
	_, err = s.E.MarkOverdueInvoices(s.Ctx, s.OrgID(), today.Midnight(time.UTC))
	s.must(err)
	for _, a := range argsOf[kernel.InvoiceArgs](t, s.enqueued(kernel.JobMarkInvoiceOverdue)) {
		if a.InvoiceID == dueToday.ID {
			t.Errorf("at midnight got = invoice due today enqueued, want = not enqueued")
		}
	}
}

// source: spec/services/invoices/payments/mark_overdue_service_spec.rb
func TestMarkInvoiceOverdue(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		source string
		set    func(*invoice.Invoice)
		code   string
	}{
		{name: "overdue", source: ":21"},
		{name: "not finalized", source: ":46", set: func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft }, code: billing.CodeInvoiceNotFinalized},
		{name: "paid", source: ":56", set: func(inv *invoice.Invoice) { inv.PaymentStatus = core.PaymentStatusSucceeded }, code: billing.CodeInvoicePaymentAlreadySucceeded},
		{name: "due in future", source: ":66", set: func(inv *invoice.Invoice) {
			inv.PaymentDueDate = core.DateOf(now, time.UTC).AddDays(5)
		}, code: billing.CodeInvoiceDueDateInFuture},
		{name: "dispute lost", source: ":76", set: func(inv *invoice.Invoice) {
			lost := now.AddDate(0, 0, -1)
			inv.PaymentDisputeLostAt = &lost
		}, code: billing.CodeInvoiceDisputeLost},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, now)
			inv := s.StoredInvoice(s.Customer(), func(inv *invoice.Invoice) {
				inv.Status, inv.PaymentDueDate = invoice.StatusFinalized, core.DateOf(now, time.UTC).AddDays(-1)
				if tc.set != nil {
					tc.set(inv)
				}
			})
			got, err := s.E.MarkInvoiceOverdue(s.Ctx, s.OrgID(), inv.ID)
			if tc.code != "" {
				checkRuleCode(t, err, tc.code)
				// The job completes on the refusal (mark_overdue_job.rb:12).
				raw, _ := json.Marshal(kernel.InvoiceArgs{InvoiceID: inv.ID})
				if ok, err := s.E.HandleJob(s.Ctx, port.Job{Kind: kernel.JobMarkInvoiceOverdue, OrganizationID: s.OrgID(), Args: raw}); !ok || err != nil {
					t.Errorf("HandleJob(%s) got = (%v, %v), want = (true, <nil>)", kernel.JobMarkInvoiceOverdue, ok, err)
				}
				if s.Invoice(inv.ID).PaymentOverdue {
					t.Errorf("payment_overdue got = true, want = false")
				}
				if hooks := s.Webhooks(); len(hooks) != 0 {
					t.Errorf("webhooks got = %v, want = none", hooks)
				}
				return
			}
			s.must(err)
			if !got.PaymentOverdue || !s.Invoice(inv.ID).PaymentOverdue {
				t.Errorf("payment_overdue got = false, want = true")
			}
			if hooks, want := s.Webhooks(), []webhook.EventType{webhook.EventTypeInvoicePaymentOverdue}; !slices.Equal(hooks, want) {
				t.Errorf("webhooks got = %v, want = %v", hooks, want)
			}
		})
	}
	// source: mark_overdue_service_spec.rb:36
	t.Run("not found", func(t *testing.T) {
		s := newScenario(t, now)
		_, err := s.E.MarkInvoiceOverdue(s.Ctx, s.OrgID(), core.NewID[core.InvoiceID](s.IDs))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("err got = %v, want = %v", err, core.ErrNotFound)
		}
	})
}

// alertScenario is the setup of subscriptions_to_be_terminated_job_spec.rb:
// a customer, and the clock on 2026-06-01.
type alertScenario struct {
	*scenario
	c *customer.Customer
}

var alertNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func newAlertScenario(t *testing.T, opts ...billing.Option) *alertScenario {
	t.Helper()
	h := billingtest.New(t, alertNow)
	e, err := billing.New(*h.Deps, opts...)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	s := &scenario{Harness: h, E: e}
	return &alertScenario{scenario: s, c: s.Customer()}
}

// ending creates a subscription ending at end, in status st.
func (s *alertScenario) ending(end time.Time, st subscription.Status) *subscription.Subscription {
	s.T.Helper()
	return s.Subscription(s.c, s.Plan(period.Monthly, 1000), billingtest.SubscribedAt(alertNow.AddDate(0, -1, 0)), func(sub *subscription.Subscription) {
		sub.EndingAt = &end
		if st != subscription.StatusActive {
			sub.Status = st
			switch st {
			case subscription.StatusTerminated:
				sub.TerminatedAt = &alertNow
			case subscription.StatusCanceled:
				sub.CanceledAt = &alertNow
			}
		}
	})
}

// sent records a domain event of type typ on object type objectType and id
// of sub that occurred at at.
func (s *alertScenario) sent(sub *subscription.Subscription, typ webhook.EventType, objectType string, at time.Time) {
	s.T.Helper()
	s.tx(func(ctx context.Context, r port.Repos) error {
		return r.DomainEvents.Record(ctx, &webhook.Event{
			ID: core.NewID[core.AuditEventID](s.IDs), OrganizationID: s.OrgID(), Type: typ,
			ObjectType: objectType, ObjectID: sub.ID.UUID(), Payload: json.RawMessage(`{}`), OccurredAt: at,
		})
	})
}

// alerted runs the job at alertNow and returns the alerted subscriptions.
func (s *alertScenario) alerted() []uuid.UUID {
	s.T.Helper()
	before := len(s.Emitted.Events())
	_, err := s.E.AlertTerminatingSubscriptions(s.Ctx, s.OrgID(), alertNow)
	s.must(err)
	var out []uuid.UUID
	for _, e := range s.Emitted.Events()[before:] {
		if e.Type == webhook.EventTypeSubscriptionTerminationAlert && e.ObjectType == "subscription" {
			out = append(out, e.ObjectID)
		}
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

// source: spec/jobs/clock/subscriptions_to_be_terminated_job_spec.rb
func TestAlertTerminatingSubscriptions(t *testing.T) {
	in15 := alertNow.AddDate(0, 0, 15)
	in45 := alertNow.AddDate(0, 0, 45)
	tests := []struct {
		name   string
		source string
		opts   []billing.Option
		// setup creates the subscriptions and returns those alerted.
		setup func(s *alertScenario) []*subscription.Subscription
	}{
		{name: "ending in 15 days", source: ":20", setup: func(s *alertScenario) []*subscription.Subscription {
			s.ending(in15.AddDate(1, 0, 0), subscription.StatusActive)
			return []*subscription.Subscription{s.ending(in15, subscription.StatusActive)}
		}},
		{name: "ending in 45 days", source: ":33", setup: func(s *alertScenario) []*subscription.Subscription {
			return []*subscription.Subscription{s.ending(in45, subscription.StatusActive)}
		}},
		{name: "no ending_at", source: ":45", setup: func(s *alertScenario) []*subscription.Subscription {
			s.Subscription(s.c, s.Plan(period.Monthly, 1000), billingtest.SubscribedAt(alertNow.AddDate(0, -1, 0)))
			return nil
		}},
		{name: "ending in 10 days", source: ":56", setup: func(s *alertScenario) []*subscription.Subscription {
			s.ending(alertNow.AddDate(0, 0, 10), subscription.StatusActive)
			return nil
		}},
		{name: "pending", source: ":68", setup: func(s *alertScenario) []*subscription.Subscription {
			s.Subscription(s.c, s.Plan(period.Monthly, 1000), billingtest.SubscribedAt(alertNow.AddDate(0, 0, 1)), func(sub *subscription.Subscription) {
				sub.EndingAt = &in15
			})
			return nil
		}},
		{name: "terminated", source: ":79", setup: func(s *alertScenario) []*subscription.Subscription {
			s.ending(in15, subscription.StatusTerminated)
			return nil
		}},
		{name: "canceled", source: ":90", setup: func(s *alertScenario) []*subscription.Subscription {
			s.ending(in15, subscription.StatusCanceled)
			return nil
		}},
		{name: "both windows", source: ":103", setup: func(s *alertScenario) []*subscription.Subscription {
			return []*subscription.Subscription{s.ending(in15, subscription.StatusActive), s.ending(in45, subscription.StatusActive)}
		}},
		{name: "already alerted today", source: ":118", setup: func(s *alertScenario) []*subscription.Subscription {
			sub := s.ending(in15, subscription.StatusActive)
			s.sent(sub, webhook.EventTypeSubscriptionTerminationAlert, "subscription", alertNow)
			return nil
		}},
		{name: "alerted yesterday", source: ":140", setup: func(s *alertScenario) []*subscription.Subscription {
			sub := s.ending(in15, subscription.StatusActive)
			s.sent(sub, webhook.EventTypeSubscriptionTerminationAlert, "subscription", alertNow.AddDate(0, 0, -1))
			return []*subscription.Subscription{sub}
		}},
		{name: "alerted 30 days ago", source: ":162", setup: func(s *alertScenario) []*subscription.Subscription {
			sub := s.ending(in15, subscription.StatusActive)
			s.sent(sub, webhook.EventTypeSubscriptionTerminationAlert, "subscription", alertNow.AddDate(0, 0, -30))
			return []*subscription.Subscription{sub}
		}},
		{name: "other event type today", source: ":184", setup: func(s *alertScenario) []*subscription.Subscription {
			sub := s.ending(in15, subscription.StatusActive)
			s.sent(sub, webhook.EventTypeSubscriptionStarted, "subscription", alertNow)
			return []*subscription.Subscription{sub}
		}},
		// One domain event per alert whatever the endpoints and delivery
		// states (:207, :229, :251, :277): the event records the alert.
		{name: "end of day", source: ":306", setup: func(s *alertScenario) []*subscription.Subscription {
			return []*subscription.Subscription{s.ending(in15.Add(24*time.Hour-time.Microsecond), subscription.StatusActive)}
		}},
		{name: "other object type today", source: ":339", setup: func(s *alertScenario) []*subscription.Subscription {
			sub := s.ending(in15, subscription.StatusActive)
			s.sent(sub, webhook.EventTypeSubscriptionTerminationAlert, "invoice", alertNow)
			return []*subscription.Subscription{sub}
		}},
		{
			name: "custom days", source: ":409", opts: []billing.Option{billing.WithTerminationAlertDays(1, 15, 45)},
			setup: func(s *alertScenario) []*subscription.Subscription {
				return []*subscription.Subscription{
					s.ending(alertNow.AddDate(0, 0, 1), subscription.StatusActive),
					s.ending(in15, subscription.StatusActive), s.ending(in45, subscription.StatusActive),
				}
			},
		},
		{
			name: "custom days skip 30", source: ":423", opts: []billing.Option{billing.WithTerminationAlertDays(1, 15, 45)},
			setup: func(s *alertScenario) []*subscription.Subscription {
				s.ending(alertNow.AddDate(0, 0, 30), subscription.StatusActive)
				return nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newAlertScenario(t, tc.opts...)
			subs := tc.setup(s)
			var want []uuid.UUID
			for _, sub := range subs {
				want = append(want, sub.ID.UUID())
			}
			slices.SortFunc(want, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			got := s.alerted()
			if !slices.Equal(got, want) {
				t.Errorf("alerted got = %v, want = %v", got, want)
			}
			// A second run the same day sends nothing.
			if again := s.alerted(); len(again) != 0 {
				t.Errorf("second run got = %v, want = none", again)
			}
		})
	}
}

// source: spec/jobs/clock/expire_incomplete_subscriptions_job_spec.rb:14
func TestExpireIncompleteSubscriptions(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	s := newScenario(t, now)
	c := s.Customer()
	p := s.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = true })
	withRule := func(st subscription.Status, rs subscription.ActivationRuleStatus, expiresAt time.Time) *subscription.Subscription {
		sub := s.Subscription(c, p, billingtest.SubscribedAt(now.Add(time.Hour)))
		if st == subscription.StatusIncomplete {
			s.UpdateSubscription(sub, func(sub *subscription.Subscription) { s.must(sub.MarkIncomplete(now.Add(-48 * time.Hour))) })
		} else {
			s.UpdateSubscription(sub, func(sub *subscription.Subscription) { s.must(sub.Activate(now.Add(-48*time.Hour), now)) })
		}
		rule := subscription.NewActivationRule(s.OrgID(), core.NewID[core.ActivationRuleID](s.IDs), sub.ID, subscription.ActivationRuleTypePayment, 48, now)
		rule.Status, rule.ExpiresAt = rs, &expiresAt
		s.tx(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
		return sub
	}
	expirable := withRule(subscription.StatusIncomplete, subscription.ActivationRuleStatusPending, now.Add(-time.Hour))
	withRule(subscription.StatusIncomplete, subscription.ActivationRuleStatusPending, now.Add(12*time.Hour))
	withRule(subscription.StatusActive, subscription.ActivationRuleStatusSatisfied, now.Add(-time.Hour))

	n, err := s.E.ExpireIncompleteSubscriptions(s.Ctx, s.OrgID(), now)
	s.must(err)
	jobs := s.enqueued(kernel.JobExpireIncompleteSubscription)
	args := argsOf[kernel.SubscriptionArgs](t, jobs)
	if n != 1 || len(args) != 1 || args[0].SubscriptionID != expirable.ID {
		t.Fatalf("enqueued got = %d %v, want = [%v]", n, args, expirable.ID)
	}
	if got, want := jobs[0].UniqueKey, kernel.ExpireIncompleteKey(expirable.ID); got != want {
		t.Errorf("unique key got = %q, want = %q", got, want)
	}
}

// quoteFixture creates a quote of customer c with an approved version and
// its generated order form expiring at expiresAt (nil never expires), in
// status st.
func (s *scenario) quoteFixture(c *customer.Customer, expiresAt *time.Time, st quote.OrderFormStatus) (*quote.Version, *quote.OrderForm) {
	s.T.Helper()
	now := s.Now()
	q := &quote.Quote{
		ID: core.NewID[core.QuoteID](s.IDs), OrganizationID: s.OrgID(), CustomerID: c.ID,
		OrderType: quote.OrderTypeOneOff, CreatedAt: now, UpdatedAt: now,
	}
	q.AssignNumber(int(q.ID.UUID()[15]) + 1)
	v := quote.FirstVersion(core.NewID[core.QuoteVersionID](s.IDs), q, nil, now)
	s.must(v.Approve(now))
	f, err := quote.NewOrderForm(core.NewID[core.OrderFormID](s.IDs), q, v, expiresAt, now)
	s.must(err)
	f.AssignNumber(int(f.ID.UUID()[15]) + 1)
	switch st {
	case quote.OrderFormStatusExpired:
		s.must(f.Expire(v, now))
	case quote.OrderFormStatusVoided:
		s.must(f.Void(quote.OrderFormVoidReasonManual, v, now))
	case quote.OrderFormStatusSigned:
		s.must(f.Sign("", now))
	}
	s.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Quotes.Create(ctx, q); err != nil {
			return err
		}
		if err := r.QuoteVersions.Create(ctx, v); err != nil {
			return err
		}
		return r.OrderForms.Create(ctx, f)
	})
	return v, f
}

// source: spec/jobs/clock/expire_order_forms_job_spec.rb:15
func TestExpireOrderForms(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	s := newScenario(t, now)
	c := s.Customer()
	yesterday, tomorrow := now.AddDate(0, 0, -1), now.AddDate(0, 0, 1)
	_, expired := s.quoteFixture(c, &yesterday, quote.OrderFormStatusGenerated)
	s.quoteFixture(c, &tomorrow, quote.OrderFormStatusGenerated)
	s.quoteFixture(c, nil, quote.OrderFormStatusGenerated)
	s.quoteFixture(c, &yesterday, quote.OrderFormStatusExpired)
	s.quoteFixture(c, &yesterday, quote.OrderFormStatusVoided)
	// Expiring later today, in UTC: its date is today's (DATE(expires_at)
	// <= DATE(now), order_form.rb:54).
	later := now.Add(8 * time.Hour)
	_, today := s.quoteFixture(c, &later, quote.OrderFormStatusGenerated)
	// In the customer's time zone (Honolulu, UTC−10) the instant is
	// 2026-06-01 00:00 and 2026-06-02 05:00 UTC is 2026-06-01 19:00: due,
	// although its UTC date is tomorrow (order_form.rb:50-55).
	hnl := s.Customer(func(c *customer.Customer) { c.Timezone = mustLoc("Pacific/Honolulu") })
	local := time.Date(2026, 6, 2, 5, 0, 0, 0, time.UTC)
	_, inZone := s.quoteFixture(hnl, &local, quote.OrderFormStatusGenerated)
	// 2026-06-02 11:00 UTC is 2026-06-02 01:00 there: not due.
	nextDay := time.Date(2026, 6, 2, 11, 0, 0, 0, time.UTC)
	s.quoteFixture(hnl, &nextDay, quote.OrderFormStatusGenerated)

	n, err := s.E.ExpireOrderForms(s.Ctx, s.OrgID(), now)
	s.must(err)
	jobs := s.enqueued(kernel.JobExpireOrderForm)
	var got []core.OrderFormID
	for _, a := range argsOf[kernel.OrderFormArgs](t, jobs) {
		got = append(got, a.OrderFormID)
	}
	want := []core.OrderFormID{expired.ID, today.ID, inZone.ID}
	slices.SortFunc(got, func(a, b core.OrderFormID) int { return a.Compare(b) })
	slices.SortFunc(want, func(a, b core.OrderFormID) int { return a.Compare(b) })
	if n != len(want) || !slices.Equal(got, want) {
		t.Fatalf("enqueued got = %d %v, want = %v", n, got, want)
	}
	for i, j := range jobs {
		if want := kernel.ExpireOrderFormKey(argsOf[kernel.OrderFormArgs](t, jobs)[i].OrderFormID); j.UniqueKey != want {
			t.Errorf("unique key got = %q, want = %q", j.UniqueKey, want)
		}
	}
}

// source: spec/services/order_forms/expire_service_spec.rb
func TestExpireOrderForm(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)
	tests := []struct {
		name   string
		source string
		status quote.OrderFormStatus
		code   string
	}{
		{name: "generated", source: ":121", status: quote.OrderFormStatusGenerated},
		{name: "already expired", source: ":84", status: quote.OrderFormStatusExpired},
		{name: "voided", source: ":104", status: quote.OrderFormStatusVoided, code: billing.CodeOrderFormIsVoided},
		{name: "signed", source: ":115", status: quote.OrderFormStatusSigned, code: billing.CodeOrderFormIsSigned},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t, now)
			v, f := s.quoteFixture(s.Customer(), &yesterday, tc.status)
			s.travelTo(now.Add(time.Hour))
			got, err := s.E.ExpireOrderForm(s.Ctx, s.OrgID(), f.ID)
			if tc.code != "" {
				checkRuleCode(t, err, tc.code)
				// The job ends on the refusal without a retry.
				raw, _ := json.Marshal(kernel.OrderFormArgs{OrderFormID: f.ID})
				if ok, err := s.E.HandleJob(s.Ctx, port.Job{Kind: kernel.JobExpireOrderForm, OrganizationID: s.OrgID(), Args: raw}); !ok || err != nil {
					t.Errorf("HandleJob(%s) got = (%v, %v), want = (true, <nil>)", kernel.JobExpireOrderForm, ok, err)
				}
				if hooks := s.Webhooks(); len(hooks) != 0 {
					t.Errorf("webhooks got = %v, want = none", hooks)
				}
				return
			}
			s.must(err)
			if got.Status != quote.OrderFormStatusExpired {
				t.Errorf("status got = %s, want = %s", got.Status, quote.OrderFormStatusExpired)
			}
			if tc.status == quote.OrderFormStatusExpired {
				// source: expire_service_spec.rb:93
				if hooks := s.Webhooks(); len(hooks) != 0 {
					t.Errorf("webhooks got = %v, want = none", hooks)
				}
				return
			}
			var stored *quote.OrderForm
			var version *quote.Version
			s.tx(func(ctx context.Context, r port.Repos) error {
				var err error
				if stored, err = r.OrderForms.Get(ctx, s.OrgID(), f.ID); err != nil {
					return err
				}
				version, err = r.QuoteVersions.Get(ctx, s.OrgID(), v.ID)
				return err
			})
			// source: expire_service_spec.rb:122
			if stored.Status != quote.OrderFormStatusExpired || stored.VoidReason != quote.OrderFormVoidReasonExpired ||
				stored.VoidedAt == nil || !stored.VoidedAt.Equal(s.Now()) {
				t.Errorf("order form got = %s %s %v, want = expired expired %v", stored.Status, stored.VoidReason, stored.VoidedAt, s.Now())
			}
			// source: expire_service_spec.rb:130
			if version.Status != quote.VersionStatusVoided || version.VoidReason != quote.VersionVoidReasonCascadeOfExpired {
				t.Errorf("version got = %s %s, want = voided cascade_of_expired", version.Status, version.VoidReason)
			}
			// source: expire_service_spec.rb:137, :143
			var types []string
			for _, e := range s.Emitted.Events() {
				types = append(types, string(e.Type)+"/"+e.ObjectType)
			}
			if want := []string{"order_form.expired/order_form", "quote.voided/quote"}; !slices.Equal(types, want) {
				t.Errorf("webhooks got = %v, want = %v", types, want)
			}
		})
	}
	// source: expire_service_spec.rb:37
	t.Run("not found", func(t *testing.T) {
		s := newScenario(t, now)
		_, err := s.E.ExpireOrderForm(s.Ctx, s.OrgID(), core.NewID[core.OrderFormID](s.IDs))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("err got = %v, want = %v", err, core.ErrNotFound)
		}
	})
}

// source: spec/jobs/clock/*: every clock job above runs as the
// per-organization job its clock kind dispatches.
func TestClockJobsHandled(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	for _, k := range []string{
		kernel.JobMarkOverdueInvoices, kernel.JobAlertTerminatingSubscriptions,
		kernel.JobExpireIncompleteSubscriptions, kernel.JobExpireOrderForms,
	} {
		t.Run(k, func(t *testing.T) {
			s := newScenario(t, now)
			s.run(k, kernel.InstantArgs{At: now})
			ok, err := s.E.HandleJob(s.Ctx, port.Job{Kind: k, OrganizationID: s.OrgID(), Args: []byte(`{}`)})
			if !ok || !errors.Is(err, core.ErrInvalidValue) {
				t.Errorf("HandleJob(%s, no instant) got = (%v, %v), want = (true, %v)", k, ok, err, core.ErrInvalidValue)
			}
		})
	}
}
