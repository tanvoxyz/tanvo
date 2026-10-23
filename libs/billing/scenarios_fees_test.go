package billing_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// chgParse parses an RFC 3339 instant.
func chgParse(t testing.TB, v string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatalf("parse %s: %v", v, err)
	}
	return at.UTC()
}

// chgChargeFeesSince returns the fees of charge c of sub created at or
// after since (lago Fee.where(subscription:, charge:, created_at: since..)).
func chgChargeFeesSince(s *scenario, sub core.SubscriptionID, c core.ChargeID, since time.Time) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	for _, f := range chgAllFees(s, sub) {
		if f.ChargeID == c && !f.CreatedAt.Before(since) {
			out = append(out, f)
		}
	}
	return out
}

// chgDay is the start of the UTC day of t.
func chgDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// chgFeeWebhooks decodes the fee.created payloads emitted since the
// emitter held n events.
func chgFeeWebhooks(s *scenario, n int) []invoice.Fee {
	s.T.Helper()
	var out []invoice.Fee
	for _, e := range s.Emitted.Events()[n:] {
		if e.Type != webhook.EventTypeFeeCreated {
			continue
		}
		var f invoice.Fee
		if err := json.Unmarshal(e.Payload, &f); err != nil {
			s.T.Fatalf("decode fee.created: %v", err)
		}
		out = append(out, f)
	}
	return out
}

// chgCheckFees asserts every fee has the units, invoice presence, flag and
// amount (lago all(have_attributes(...))); amount < 0 is not checked.
func chgCheckFees(t *testing.T, fs []*invoice.Fee, units int64, withInvoice, payInAdvance bool, amount int64) {
	t.Helper()
	for _, f := range fs {
		if !f.Units.Equal(money.NewFromInt(units)) {
			t.Errorf("fee units = %s, want = %d", f.Units, units)
		}
		if got := !f.InvoiceID.IsZero(); got != withInvoice {
			t.Errorf("fee has invoice = %v, want = %v", got, withInvoice)
		}
		if f.PayInAdvance != payInAdvance {
			t.Errorf("fee pay_in_advance = %v, want = %v", f.PayInAdvance, payInAdvance)
		}
		if amount >= 0 && f.Amount != amount {
			t.Errorf("fee amount = %d, want = %d", f.Amount, amount)
		}
	}
}

// chgRecurringSetup is the setup of recurring_fees_spec.rb: a monthly
// in-advance plan of 49 cents with a prorated recurring unique count
// charge of 30 on seats.
type chgRecurringSetup struct {
	s      *scenario
	sub    *subscription.Subscription
	charge *catalog.Charge
}

func newChgRecurring(t *testing.T, payInAdvance, invoiceable bool, groupedBy bool, grace int) *chgRecurringSetup {
	t.Helper()
	s := newScenario(t, chgParse(t, "2024-06-05T12:12:00Z"))
	if grace > 0 {
		s.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = grace })
	}
	m := s.Metric("seats", catalog.AggregationTypeUniqueCount, "item_id", func(m *catalog.BillableMetric) { m.Recurring = true })
	p := s.Plan(period.Monthly, 49, func(p *catalog.Plan) { p.PayInAdvance = true })
	props := `{"amount":"30"}`
	if groupedBy {
		props = `{"amount":"30","pricing_group_keys":["item_id"]}`
	}
	c := s.Charge(p, m, catalog.ChargeModelStandard, props, func(c *catalog.Charge) {
		c.PayInAdvance, c.Invoiceable, c.Prorated = payInAdvance, invoiceable, true
	})
	sub := chgSubscribe(s, s.Customer(), p)
	s.performBilling()
	return &chgRecurringSetup{s: s, sub: sub, charge: c}
}

// send sends a seats event for item.
func (r *chgRecurringSetup) send(item string) { r.s.ingest(r.sub, "seats", "item_id", item) }

// terminate terminates the subscription now, then runs the billing.
func (r *chgRecurringSetup) terminate() {
	r.s.T.Helper()
	_, err := r.s.E.Lifecycle.Terminate(r.s.Ctx, lifecycle.TerminateInput{OrganizationID: r.s.OrgID(), SubscriptionID: r.sub.ID})
	r.s.must(err)
	r.s.drain()
	r.s.performBilling()
	if got := chgReloadSub(r.s, r.sub.ID).Status; got != subscription.StatusTerminated {
		r.s.T.Errorf("status = %s, want = terminated", got)
	}
}

// invoices counts the subscription's invoices.
func (r *chgRecurringSetup) invoices() []*invoice.Invoice { return chgSubInvoices(r.s, r.sub.ID) }

// lastInvoice is the most recently created invoice of the subscription.
func (r *chgRecurringSetup) lastInvoice() *invoice.Invoice {
	invs := r.invoices()
	slices.SortStableFunc(invs, func(a, b *invoice.Invoice) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return invs[len(invs)-1]
}

// TestScenarioRecurringFeesInAdvanceNotInvoiceable ports
// spec/scenarios/fees/recurring_fees_spec.rb "with invoiceable = false"
// without grace period.
func TestScenarioRecurringFeesInAdvanceNotInvoiceable(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		// source: spec/scenarios/fees/recurring_fees_spec.rb:74 (without grouped_by), :129 (grouped_by item_id)
		t.Run(fmt.Sprintf("grouped_by=%v", grouped), func(t *testing.T) {
			r := newChgRecurring(t, true, false, grouped, 0)
			s := r.s
			if n := len(s.Invoices(r.sub.CustomerID)); n != 1 {
				t.Fatalf("invoices = %d, want = 1", n)
			}
			for i := 1; i <= 5; i++ {
				s.travelTo(time.Date(2024, 6, 10+i, 10, 0, 0, 0, time.UTC))
				r.send(fmt.Sprintf("user_%d", i))
				fs := chgChargeFeesSince(s, r.sub.ID, r.charge.ID, time.Time{})
				if len(fs) != i {
					t.Fatalf("charge fees = %d, want = %d", len(fs), i)
				}
				if got, want := fs[len(fs)-1].Amount, int64((21-i)*100); got != want {
					t.Errorf("fee %d amount = %d, want = %d", i, got, want)
				}
			}

			s.travelTo(chgParse(t, "2024-07-01T00:10:00Z"))
			if !grouped {
				s.performBilling()
				if n := len(r.invoices()); n != 2 {
					t.Errorf("invoices = %d, want = 2", n)
				}
				fs := chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))
				if len(fs) != 1 {
					t.Fatalf("recurring fees = %d, want = 1", len(fs))
				}
				chgCheckFees(t, fs, 5, false, fs[0].PayInAdvance, 30*5*100)
			} else {
				if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))); n != 0 {
					t.Errorf("fees before billing = %d, want = 0", n)
				}
				s.performBilling()
				if n := len(r.invoices()); n != 2 {
					t.Errorf("invoices = %d, want = 2", n)
				}
				fs := chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))
				if len(fs) != 5 {
					t.Fatalf("recurring fees = %d, want = 5", len(fs))
				}
				chgCheckFees(t, fs, 1, false, true, 30*100)
			}

			s.travelTo(chgParse(t, "2024-07-12T01:10:00Z"))
			r.send("user_july_1")
			r.send("user_july_2")

			s.travelTo(chgParse(t, "2024-08-01T01:10:00Z"))
			if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))); n != 0 {
				t.Errorf("fees before billing = %d, want = 0", n)
			}
			mark := len(s.Emitted.Events())
			s.performBilling()
			if n := len(r.invoices()); n != 3 {
				t.Errorf("invoices = %d, want = 3", n)
			}
			wantUnits, wantHooks := int64(7), 1
			if grouped {
				wantUnits, wantHooks = 1, 7
			}
			hooks := 0
			for _, f := range chgFeeWebhooks(s, mark) {
				if f.Units.Equal(money.NewFromInt(wantUnits)) && f.InvoiceID.IsZero() &&
					f.Properties.From.Equal(chgParse(t, "2024-08-01T00:00:00Z")) && f.Properties.To.Equal(chgParse(t, "2024-09-01T00:00:00Z")) {
					hooks++
				}
			}
			if hooks != wantHooks {
				t.Errorf("fee.created webhooks = %d, want = %d", hooks, wantHooks)
			}
			fs := chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))
			if len(fs) != wantHooks {
				t.Fatalf("recurring fees = %d, want = %d", len(fs), wantHooks)
			}
			if grouped {
				chgCheckFees(t, fs, 1, false, true, 30*100)
			} else {
				chgCheckFees(t, fs, 7, false, fs[0].PayInAdvance, 30*7*100)
			}

			s.travelTo(chgParse(t, "2024-08-15T01:10:00Z"))
			r.terminate()
			if n := len(r.invoices()); n != 4 {
				t.Errorf("invoices = %d, want = 4", n)
			}
			fs = chgChargeFeesSince(s, r.sub.ID, r.charge.ID, billingtest.Date(2024, 8, 1))
			if len(fs) != wantHooks {
				t.Fatalf("fees of August = %d, want = %d", len(fs), wantHooks)
			}
		})
	}
}

// TestScenarioRecurringFeesGracePeriod ports
// spec/scenarios/fees/recurring_fees_spec.rb "with grace period".
//
// source: spec/scenarios/fees/recurring_fees_spec.rb:187
func TestScenarioRecurringFeesGracePeriod(t *testing.T) {
	r := newChgRecurring(t, true, false, true, 3)
	s := r.s
	drafts := func() (draft, finalized int) {
		for _, inv := range r.invoices() {
			switch inv.Status {
			case invoice.StatusDraft:
				draft++
			case invoice.StatusFinalized:
				finalized++
			}
		}
		return draft, finalized
	}
	if d, _ := drafts(); d != 1 {
		t.Fatalf("drafts = %d, want = 1", d)
	}
	s.travelTo(time.Date(2024, 6, 10, 10, 0, 0, 0, time.UTC))
	r.send("user_1")
	r.send("user_2")
	fs := chgChargeFeesSince(s, r.sub.ID, r.charge.ID, time.Time{})
	if len(fs) != 2 {
		t.Fatalf("charge fees = %d, want = 2", len(fs))
	}
	chgCheckFees(t, fs, 1, false, true, 2100)

	s.travelTo(chgParse(t, "2024-07-01T00:10:00Z"))
	if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))); n != 0 {
		t.Errorf("fees before billing = %d, want = 0", n)
	}
	mark := len(s.Emitted.Events())
	s.performBilling()
	hooks := 0
	for _, f := range chgFeeWebhooks(s, mark) {
		if f.InvoiceID.IsZero() && f.Units.Equal(money.One) && f.Properties.From.Equal(billingtest.Date(2024, 7, 1)) && f.Properties.To.Equal(billingtest.Date(2024, 8, 1)) {
			hooks++
		}
	}
	if hooks != 2 {
		t.Errorf("fee.created webhooks = %d, want = 2", hooks)
	}
	if d, f := drafts(); d != 2 || f != 0 {
		t.Errorf("drafts, finalized = %d, %d, want = 2, 0", d, f)
	}
	fs = chgChargeFeesSince(s, r.sub.ID, r.charge.ID, billingtest.Date(2024, 7, 1))
	if len(fs) != 2 {
		t.Fatalf("recurring fees = %d, want = 2", len(fs))
	}
	chgCheckFees(t, fs, 1, false, true, 30*100)

	s.travelTo(chgParse(t, "2024-07-04T01:10:00Z"))
	mark = len(s.Emitted.Events())
	s.performFinalize()
	for _, f := range chgFeeWebhooks(s, mark) {
		if f.InvoiceID.IsZero() {
			t.Errorf("fee.created webhook of an unbilled fee on finalization")
		}
	}
	if d, f := drafts(); d != 0 || f != 2 {
		t.Errorf("drafts, finalized = %d, %d, want = 0, 2", d, f)
	}
	if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, billingtest.Date(2024, 7, 1))); n != 2 {
		t.Errorf("recurring fees = %d, want = 2", n)
	}

	s.travelTo(chgParse(t, "2024-07-05T01:30:00Z"))
	r.terminate()
	if d, f := drafts(); d != 1 || f != 2 {
		t.Errorf("drafts, finalized = %d, %d, want = 1, 2", d, f)
	}
	if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, billingtest.Date(2024, 7, 1))); n != 2 {
		t.Errorf("recurring fees = %d, want = 2", n)
	}
}

// TestScenarioRecurringFeesInAdvanceInvoiceable ports
// spec/scenarios/fees/recurring_fees_spec.rb "with invoiceable = true".
func TestScenarioRecurringFeesInAdvanceInvoiceable(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		// source: spec/scenarios/fees/recurring_fees_spec.rb:288 (without grouped_by), :316 (grouped_by item_id)
		t.Run(fmt.Sprintf("grouped_by=%v", grouped), func(t *testing.T) {
			r := newChgRecurring(t, true, true, grouped, 0)
			s := r.s
			for i := 1; i <= 5; i++ {
				s.travelTo(time.Date(2024, 6, 10+i, 10, 0, 0, 0, time.UTC))
				r.send(fmt.Sprintf("user_%d", i))
				if n := len(r.invoices()); n != i+1 {
					t.Fatalf("invoices = %d, want = %d", n, i+1)
				}
				fs := s.Fees(r.lastInvoice().ID)
				if len(fs) != 1 || fs[0].Amount != int64((21-i)*100) {
					t.Errorf("event invoice fees = %v, want = [%d]", chgAmounts(fs), (21-i)*100)
				}
			}
			s.travelTo(chgParse(t, "2024-07-01T00:10:00Z"))
			s.performBilling()
			if n := len(r.invoices()); n != 7 {
				t.Errorf("invoices = %d, want = 7", n)
			}
			fs := chgFeesOf(s, r.lastInvoice(), core.FeeTypeCharge)
			if grouped {
				if n := len(chgChargeFeesSince(s, r.sub.ID, r.charge.ID, chgDay(s.Now()))); n != 5 {
					t.Errorf("recurring fees = %d, want = 5", n)
				}
				if len(fs) != 5 {
					t.Fatalf("renewal charge fees = %d, want = 5", len(fs))
				}
				chgCheckFees(t, fs, 1, true, false, 30*100)
			} else {
				if len(fs) != 1 {
					t.Fatalf("renewal charge fees = %d, want = 1", len(fs))
				}
				chgCheckFees(t, fs, 5, true, false, 30*5*100)
			}

			s.travelTo(chgParse(t, "2024-07-15T01:10:00Z"))
			r.terminate()
			if n := len(chgFeesOf(s, r.lastInvoice(), core.FeeTypeCharge)); n != 0 {
				t.Errorf("termination charge fees = %d, want = 0", n)
			}
		})
	}
}

// TestScenarioRecurringFeesInArrears ports
// spec/scenarios/fees/recurring_fees_spec.rb "when charge is pay in arrears".
func TestScenarioRecurringFeesInArrears(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		// source: spec/scenarios/fees/recurring_fees_spec.rb:377 (without grouped_by), :407 (grouped_by item_id)
		t.Run(fmt.Sprintf("grouped_by=%v", grouped), func(t *testing.T) {
			r := newChgRecurring(t, false, true, grouped, 0)
			s := r.s
			for i := 1; i <= 5; i++ {
				s.travelTo(time.Date(2024, 6, 10+i, 10, 0, 0, 0, time.UTC))
				r.send(fmt.Sprintf("user_%d", i))
			}
			if n := len(r.invoices()); n != 1 {
				t.Fatalf("invoices = %d, want = 1", n)
			}
			s.travelTo(chgParse(t, "2024-07-01T00:10:00Z"))
			s.performBilling()
			if n := len(r.invoices()); n != 2 {
				t.Errorf("invoices = %d, want = 2", n)
			}
			fs := chgFeesOf(s, r.lastInvoice(), core.FeeTypeCharge)
			if grouped {
				if len(fs) != 5 {
					t.Fatalf("renewal charge fees = %d, want = 5", len(fs))
				}
				chgCheckFees(t, fs, 1, true, false, -1)
				got := chgAmounts(fs)
				slices.Sort(got)
				if want := []int64{1600, 1700, 1800, 1900, 2000}; !slices.Equal(got, want) {
					t.Errorf("amounts = %v, want = %v", got, want)
				}
			} else {
				if len(fs) != 1 {
					t.Fatalf("renewal charge fees = %d, want = 1", len(fs))
				}
				chgCheckFees(t, fs, 5, true, false, (20+19+18+17+16)*100)
			}

			s.travelTo(chgParse(t, "2024-07-15T01:10:00Z"))
			r.terminate()
			fs = chgFeesOf(s, r.lastInvoice(), core.FeeTypeCharge)
			if grouped {
				if n := len(r.invoices()); n != 3 {
					t.Errorf("invoices = %d, want = 3", n)
				}
				if len(fs) != 5 {
					t.Fatalf("termination charge fees = %d, want = 5", len(fs))
				}
				chgCheckFees(t, fs, 1, true, false, 1452)
			} else {
				if len(fs) != 1 {
					t.Fatalf("termination charge fees = %d, want = 1", len(fs))
				}
				chgCheckFees(t, fs, 5, true, false, 7258)
			}
		})
	}
}

// chgUnbilledSince returns the fees of the subscriptions without invoice
// created in [from, to) (lago Fee.where(invoice_id: nil, created_at:)); a
// zero to is unbounded.
func chgUnbilledSince(s *scenario, from, to time.Time, subs ...core.SubscriptionID) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	for _, id := range subs {
		for _, f := range chgAllFees(s, id) {
			if f.InvoiceID.IsZero() && !f.CreatedAt.Before(from) && (to.IsZero() || f.CreatedAt.Before(to)) {
				out = append(out, f)
			}
		}
	}
	return out
}

// chgUpgradeSetup is the setup of recurring_fee_upgrade_spec.rb and
// recurring_fee_downgrade_spec.rb: two in-advance plans with a prorated,
// non-invoiceable, in-advance recurring unique count charge grouped by
// item_id.
type chgUpgradeSetup struct {
	s          *scenario
	sub        *subscription.Subscription
	from, to   *catalog.Plan
	calendar   bool
	customerID core.CustomerID
}

func newChgUpgrade(t *testing.T, start time.Time, calendar bool, fromAmount, toAmount int64, fromCharge, toCharge string) *chgUpgradeSetup {
	t.Helper()
	s := newScenario(t, start)
	m := s.Metric("seats", catalog.AggregationTypeUniqueCount, "item_id", func(m *catalog.BillableMetric) { m.Recurring = true })
	plan := func(amount int64, charge string) *catalog.Plan {
		p := s.Plan(period.Monthly, amount, func(p *catalog.Plan) { p.PayInAdvance = true })
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+charge+`","pricing_group_keys":["item_id"]}`, func(c *catalog.Charge) {
			c.PayInAdvance, c.Invoiceable, c.Prorated = true, false, true
		})
		return p
	}
	from, to := plan(fromAmount, fromCharge), plan(toAmount, toCharge)
	c := s.Customer()
	sub := chgSubscribe(s, c, from, chgBillingTime(calendar))
	s.performBilling()
	return &chgUpgradeSetup{s: s, sub: sub, from: from, to: to, calendar: calendar, customerID: c.ID}
}

// sendAt sends a seats event for item at at.
func (u *chgUpgradeSetup) sendAt(at time.Time, items ...string) {
	u.s.travelTo(at)
	for _, it := range items {
		u.s.ingest(u.sub, "seats", "item_id", it)
	}
}

// change changes the subscription to the other plan now (lago
// create_subscription with the same external_id) and returns the
// subscription on the new plan.
func (u *chgUpgradeSetup) change() *subscription.Subscription {
	u.s.T.Helper()
	next, err := u.s.E.Lifecycle.ChangePlan(u.s.Ctx, lifecycle.ChangePlanInput{OrganizationID: u.s.OrgID(), SubscriptionID: u.sub.ID, PlanID: u.to.ID})
	u.s.must(err)
	u.s.drain()
	return next
}

// TestScenarioRecurringFeeUpgrade ports
// spec/scenarios/fees/recurring_fee_upgrade_spec.rb.
func TestScenarioRecurringFeeUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name     string
		calendar bool
		start    string
		// checks are the instants of perform_billing with the count of
		// unbilled fees created since the start of the month.
		checks []struct {
			at   string
			want int
		}
	}{
		{
			// source: spec/scenarios/fees/recurring_fee_upgrade_spec.rb:58
			name: "calendar", calendar: true, start: "2024-06-01T00:00:00Z",
			checks: []struct {
				at   string
				want int
			}{{"2024-07-01T00:10:00Z", 4}},
		},
		{
			// source: spec/scenarios/fees/recurring_fee_upgrade_spec.rb:114
			name: "anniversary", start: "2024-06-04T00:00:00Z",
			checks: []struct {
				at   string
				want int
			}{{"2024-07-01T00:10:00Z", 0}, {"2024-07-01T00:10:00Z", 0}, {"2024-07-04T14:00:00Z", 4}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newChgUpgrade(t, chgParse(t, tc.start), tc.calendar, 49, 99, "30", "60")
			s := u.s
			u.sendAt(chgParse(t, "2024-06-05T00:00:00Z"), "user_1")
			u.sendAt(chgParse(t, "2024-06-15T10:05:59Z"), "user_2")

			s.travelTo(chgParse(t, "2024-06-15T10:06:00Z"))
			next := u.change()
			if got := chgReloadSub(s, u.sub.ID).Status; got != subscription.StatusTerminated {
				t.Errorf("first subscription status = %s, want = terminated", got)
			}
			if got := len(s.Invoices(u.customerID)); got != 2 {
				t.Errorf("customer invoices = %d, want = 2", got)
			}
			next = chgReloadSub(s, next.ID)
			if next.PlanID != u.to.ID || next.Status != subscription.StatusActive {
				t.Errorf("new subscription plan, status = %s, %s, want = %s, active", next.PlanID, next.Status, u.to.ID)
			}
			if got := len(chgUnbilledSince(s, time.Time{}, s.Now(), u.sub.ID, next.ID)); got != 2 {
				t.Errorf("unbilled fees before upgrade = %d, want = 2", got)
			}
			if got := len(chgUnbilledSince(s, s.Now(), time.Time{}, u.sub.ID, next.ID)); got != 0 {
				t.Errorf("unbilled fees at upgrade = %d, want = 0", got)
			}

			u.sub = next
			u.sendAt(chgParse(t, "2024-06-19T00:00:00Z"), "user_3", "user_4")
			for _, c := range tc.checks {
				s.travelTo(chgParse(t, c.at))
				s.performBilling()
				month := time.Date(s.Now().Year(), s.Now().Month(), 1, 0, 0, 0, 0, time.UTC)
				if got := len(chgUnbilledSince(s, month, time.Time{}, next.ID)); got != c.want {
					t.Errorf("at %s: unbilled fees this month = %d, want = %d", c.at, got, c.want)
				}
			}
		})
	}
}

// chgCheckDowngrade asserts the plan change at the current instant left
// the current subscription active with a pending next subscription on the
// new plan, and the customer with invoices invoices.
func (u *chgUpgradeSetup) chgCheckDowngrade(t *testing.T, next *subscription.Subscription, invoices int) {
	t.Helper()
	s := u.s
	if got := chgReloadSub(s, u.sub.ID).Status; got != subscription.StatusActive {
		t.Errorf("first subscription status = %s, want = active", got)
	}
	if got := len(s.Invoices(u.customerID)); got != invoices {
		t.Errorf("customer invoices = %d, want = %d", got, invoices)
	}
	next = chgReloadSub(s, next.ID)
	if next.PlanID != u.to.ID || next.Status != subscription.StatusPending {
		t.Errorf("new subscription plan, status = %s, %s, want = %s, pending", next.PlanID, next.Status, u.to.ID)
	}
}

// chgCheckSwapped asserts the downgrade was swapped in.
func (u *chgUpgradeSetup) chgCheckSwapped(t *testing.T, next core.SubscriptionID) {
	t.Helper()
	if got := chgReloadSub(u.s, u.sub.ID).Status; got != subscription.StatusTerminated {
		t.Errorf("first subscription status = %s, want = terminated", got)
	}
	if n := chgReloadSub(u.s, next); n.PlanID != u.to.ID || n.Status != subscription.StatusActive {
		t.Errorf("new subscription plan, status = %s, %s, want = %s, active", n.PlanID, n.Status, u.to.ID)
	}
}

// TestScenarioRecurringFeeDowngrade ports
// spec/scenarios/fees/recurring_fee_downgrade_spec.rb "when downgrading
// subscription".
func TestScenarioRecurringFeeDowngrade(t *testing.T) {
	type check struct {
		at      string
		want    int
		swapped bool
	}
	for _, tc := range []struct {
		name     string
		calendar bool
		start    string
		checks   []check
	}{
		{
			// source: spec/scenarios/fees/recurring_fee_downgrade_spec.rb:58
			name: "calendar", calendar: true, start: "2024-06-01T00:00:00Z",
			checks: []check{{"2024-07-01T00:10:00Z", 4, true}},
		},
		{
			// source: spec/scenarios/fees/recurring_fee_downgrade_spec.rb:117
			name: "anniversary", start: "2024-06-04T00:00:00Z",
			checks: []check{{"2024-07-01T00:10:00Z", 0, false}, {"2024-07-01T00:10:00Z", 0, false}, {"2024-07-04T14:00:00Z", 4, true}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newChgUpgrade(t, chgParse(t, tc.start), tc.calendar, 99, 49, "30", "60")
			s := u.s
			u.sendAt(chgParse(t, "2024-06-05T00:00:00Z"), "user_1")
			u.sendAt(chgParse(t, "2024-06-15T10:05:59Z"), "user_2")

			s.travelTo(chgParse(t, "2024-06-15T10:06:00Z"))
			next := u.change()
			u.chgCheckDowngrade(t, next, 1)
			if got := len(chgUnbilledSince(s, time.Time{}, s.Now(), u.sub.ID, next.ID)); got != 2 {
				t.Errorf("unbilled fees before downgrade = %d, want = 2", got)
			}
			if got := len(chgUnbilledSince(s, s.Now(), time.Time{}, u.sub.ID, next.ID)); got != 0 {
				t.Errorf("unbilled fees at downgrade = %d, want = 0", got)
			}

			u.sendAt(chgParse(t, "2024-06-19T00:00:00Z"), "user_3", "user_4")
			for _, c := range tc.checks {
				s.travelTo(chgParse(t, c.at))
				s.performBilling()
				if c.swapped {
					u.chgCheckSwapped(t, next.ID)
				}
				month := time.Date(s.Now().Year(), s.Now().Month(), 1, 0, 0, 0, 0, time.UTC)
				if got := len(chgUnbilledSince(s, month, time.Time{}, u.sub.ID, next.ID)); got != c.want {
					t.Errorf("at %s: unbilled fees this month = %d, want = %d", c.at, got, c.want)
				}
			}
		})
	}
}

// TestScenarioRecurringFeeDowngradeBoundaries ports
// spec/scenarios/fees/recurring_fee_downgrade_spec.rb "when testing the
// boundaries": an in-arrears recurring charge of 31 whose single unit is
// active the whole month bills 31 on the termination invoice.
//
// source: spec/scenarios/fees/recurring_fee_downgrade_spec.rb:220
func TestScenarioRecurringFeeDowngradeBoundaries(t *testing.T) {
	s := newScenario(t, chgParse(t, "2024-06-01T00:00:00Z"))
	m := s.Metric("seats", catalog.AggregationTypeUniqueCount, "item_id", func(m *catalog.BillableMetric) { m.Recurring = true })
	plan := func(amount int64, charge string) *catalog.Plan {
		p := s.Plan(period.Monthly, amount, func(p *catalog.Plan) { p.PayInAdvance = true })
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"`+charge+`","pricing_group_keys":["item_id"]}`, func(c *catalog.Charge) {
			c.PayInAdvance, c.Invoiceable, c.Prorated = false, true, true
		})
		return p
	}
	from, to := plan(99, "31"), plan(49, "60")
	c := s.Customer()
	u := &chgUpgradeSetup{s: s, from: from, to: to, calendar: true, customerID: c.ID}
	u.sub = chgSubscribe(s, c, from)
	s.performBilling()
	u.sendAt(chgParse(t, "2024-06-15T10:05:59Z"), "user_2")
	s.travelTo(chgParse(t, "2024-07-01T00:00:00Z"))
	s.performBilling()

	s.travelTo(chgParse(t, "2024-07-15T10:06:00Z"))
	next := u.change()
	u.chgCheckDowngrade(t, next, 2)
	var before, after int
	for _, f := range append(chgAllFees(s, u.sub.ID), chgAllFees(s, next.ID)...) {
		if f.Type != core.FeeTypeCharge {
			continue
		}
		if f.CreatedAt.Before(s.Now()) {
			before++
		} else {
			after++
		}
	}
	if before != 1 || after != 0 {
		t.Errorf("charge fees before, at downgrade = %d, %d, want = 1, 0", before, after)
	}

	s.travelTo(chgParse(t, "2024-08-01T00:00:00Z"))
	s.performBilling()
	invs := s.Invoices(c.ID)
	slices.SortStableFunc(invs, func(a, b *invoice.Invoice) int { return a.CreatedAt.Compare(b.CreatedAt) })
	fs := chgFeesOf(s, invs[len(invs)-1], core.FeeTypeCharge)
	if len(fs) == 0 {
		t.Fatalf("termination invoice has no charge fee")
	}
	if got := fs[len(fs)-1].Amount; got != 3100 {
		t.Errorf("termination charge fee amount = %d, want = 3100", got)
	}
}
