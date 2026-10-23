package fees_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// commitmentFixture is the setup of
// spec/services/fees/commitments/minimum/create_service_spec.rb: a yearly
// subscription started on 2024-01-01, billed on 2025-01-01.
type commitmentFixture struct {
	*fixture
	sub  *subscription.Subscription
	plan *catalog.Plan
	b    period.Boundaries
}

func newCommitmentFixture(t *testing.T, advance, commitment bool) *commitmentFixture {
	t.Helper()
	f := newFixture(t, date(2024, time.January, 1))
	plan := f.Plan(period.Yearly, 0, func(p *catalog.Plan) { p.PayInAdvance = advance })
	if commitment {
		f.Commitment(plan, 1000)
	}
	s := f.Subscription(f.Customer(), plan, billingtest.SubscribedAt(date(2024, time.January, 1)))
	f.Clock.Set(date(2025, time.January, 1, 10))
	return &commitmentFixture{fixture: f, sub: s, plan: plan, b: monthly(date(2024, time.January, 1), date(2025, time.January, 1))}
}

// subscriptionFee returns a subscription fee of s on the invoice of sc.
func (f *commitmentFixture) subscriptionFee(sc fees.Scope, amount int64) *invoice.Fee {
	return &invoice.Fee{
		ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: sc.Invoice.ID, SubscriptionID: f.sub.ID,
		Type: core.FeeTypeSubscription, Currency: billingtest.USD, Amount: amount, PreciseAmount: kernel.Cents(amount),
		Units: dec("1"), Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending, CreatedAt: f.Now(),
		Properties: invoice.FeeProperties{From: sc.Boundaries.Subscription.From, To: sc.Boundaries.Subscription.To},
	}
}

func (f *commitmentFixture) fee(sc fees.Scope, pending ...*invoice.Fee) *invoice.Fee {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return f.svc.CommitmentFee(ctx, r, sc, pending)
	})
}

func TestCommitmentFee(t *testing.T) {
	t.Parallel()
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:28
	t.Run("no minimum commitment", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, false, false)
		if got := f.fee(f.scope(f.sub, f.b, date(2025, time.January, 1, 10))); got != nil {
			t.Errorf("got fee = %+v, want nil", got)
		}
	})
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:39
	t.Run("already billed", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, false, true)
		sc := f.scope(f.sub, f.b, date(2025, time.January, 1, 10))
		billed := f.subscriptionFee(sc, 0)
		billed.Type = core.FeeTypeCommitment
		f.store(billed)
		if got := f.fee(sc); got != nil {
			t.Errorf("got fee = %+v, want nil", got)
		}
	})
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:47
	t.Run("billed for another subscription", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, false, true)
		sc := f.scope(f.sub, f.b, date(2025, time.January, 1, 10))
		other := f.subscriptionFee(sc, 0)
		other.Type, other.SubscriptionID = core.FeeTypeCommitment, core.NewID[core.SubscriptionID](f.IDs)
		f.store(other)
		checkAmount(t, f.fee(sc), 1000, "1000")
	})
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:59
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:76
	t.Run("in arrears", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, false, true)
		sc := f.scope(f.sub, f.b, date(2025, time.January, 1, 10))
		got := f.fee(sc)
		checkAmount(t, got, 1000, "1000")
		if got.Type != core.FeeTypeCommitment || got.InvoiceID != sc.Invoice.ID || !got.TaxesPreciseAmount.IsZero() {
			t.Errorf("got fee = %+v, want an untaxed commitment fee of the invoice", got)
		}
		if !got.Properties.From.Equal(f.b.Subscription.From) || !got.Properties.To.Equal(f.b.Subscription.To) {
			t.Errorf("got boundaries = [%s, %s], want the current period", got.Properties.From, got.Properties.To)
		}
	})
	// The commitment is reduced by the period's fees, pending ones included.
	// source: spec/services/commitments/minimum/calculate_true_up_fee_service_spec.rb:61 (amounts adapted)
	t.Run("with usage", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, false, true)
		sc := f.scope(f.sub, f.b, date(2025, time.January, 1, 10))
		checkAmount(t, f.fee(sc, f.subscriptionFee(sc, 300)), 700, "700")
		checkAmount(t, f.fee(sc, f.subscriptionFee(sc, 1500)), 0, "0")
	})
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:104
	t.Run("in advance, first period", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, true, true)
		if got := f.fee(f.scope(f.sub, f.b, date(2025, time.January, 1, 10))); got != nil {
			t.Errorf("got fee = %+v, want nil", got)
		}
	})
	// source: spec/services/fees/commitments/minimum/create_service_spec.rb:142
	t.Run("in advance, previous period", func(t *testing.T) {
		t.Parallel()
		f := newCommitmentFixture(t, true, true)
		prev := f.scope(f.sub, monthly(date(2024, time.January, 1), date(2025, time.January, 1)), date(2024, time.January, 1, 10))
		f.store(f.subscriptionFee(prev, 0))
		sc := f.scope(f.sub, monthly(date(2025, time.January, 1), date(2026, time.January, 1)), date(2025, time.January, 1, 10))
		got := f.fee(sc)
		if got == nil {
			t.Fatalf("got fee = nil, want one")
		}
		if !got.Properties.From.Equal(date(2024, time.January, 1)) || !got.Properties.To.Equal(date(2025, time.January, 1)) {
			t.Errorf("got boundaries = [%s, %s], want the previous period", got.Properties.From, got.Properties.To)
		}
	})
}

// A subscription paid in arrears ending for good the day after its period
// ended, before that period was billed, owes the commitment of the ended
// period, not of the day it ran in the next one.
// source: app/services/subscriptions/terminated_dates_service.rb:35
func TestCommitmentFeeTerminatedAfterPeriodEnd(t *testing.T) {
	t.Parallel()
	f := newCommitmentFixture(t, false, true)
	end := date(2025, time.January, 1, 10)
	f.UpdateSubscription(f.sub, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(end)
	})
	sc := f.scope(f.sub, f.b, end)
	got := f.fee(sc)
	checkAmount(t, got, 1000, "1000")
	if !got.Properties.From.Equal(date(2024, time.January, 1)) || !got.Properties.To.Equal(date(2025, time.January, 1)) {
		t.Errorf("got boundaries = [%s, %s], want = [2024-01-01, 2025-01-01)", got.Properties.From, got.Properties.To)
	}
}

// A subscription ended for good at a local midnight is billed the day it
// ends on: lago counts the days to terminated_at with
// Utils::Datetime.date_diff_with_timezone, which adds a second to an end at
// the beginning of a day. Terminated on 2024-07-01 00:00, the yearly
// commitment of 1000 is prorated on 183 of 366 days.
// source: app/services/commitments/calculate_prorated_coefficient_service.rb:45
func TestCommitmentFeeTerminatedAtMidnight(t *testing.T) {
	t.Parallel()
	f := newCommitmentFixture(t, false, true)
	end := date(2024, time.July, 1)
	f.Clock.Set(end)
	f.UpdateSubscription(f.sub, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, ptr(end)
	})
	sc := f.scope(f.sub, monthly(date(2024, time.January, 1), end), end)
	checkAmount(t, f.fee(sc), 500, "500")
}
