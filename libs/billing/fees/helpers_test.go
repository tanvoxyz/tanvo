package fees_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fixture is a harness with the fees service.
type fixture struct {
	*billingtest.Harness
	svc *fees.Service
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	return &fixture{Harness: h, svc: fees.New(h.Deps)}
}

// env loads the environment of a subscription.
func (f *fixture) env(s *subscription.Subscription) *kernel.SubscriptionEnv {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*kernel.SubscriptionEnv, error) {
		return kernel.LoadSubscriptionEnv(ctx, r, f.OrgID(), s.ID)
	})
}

// boundaries returns the boundaries of a billing run of s at ts.
func (f *fixture) boundaries(s *subscription.Subscription, ts time.Time, mode period.Mode) period.Boundaries {
	f.T.Helper()
	d, err := f.env(s).Dates(ts, mode)
	if err != nil {
		f.T.Fatalf("dates: %v", err)
	}
	b, ok := d.Boundaries()
	if !ok {
		f.T.Fatalf("boundaries: subscription not started")
	}
	return b
}

// invoiceOpt changes an invoice before it is stored.
type invoiceOpt func(*invoice.Invoice)

// draft makes the invoice a draft.
func draft(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft }

// scope stores a subscription invoice of s billing the boundaries b at ts
// and returns the scope of its fees.
func (f *fixture) scope(s *subscription.Subscription, b period.Boundaries, ts time.Time, opts ...invoiceOpt) fees.Scope {
	f.T.Helper()
	env := f.env(s)
	inv := kernel.NewInvoice(f.Deps, env.Customer, kernel.InvoiceKind{Type: invoice.TypeSubscription, Recurring: true}, env.Plan.Currency, ts)
	inv.Status = invoice.StatusFinalized
	for _, o := range opts {
		o(inv)
	}
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](f.IDs), OrganizationID: f.OrgID(),
		InvoiceID: inv.ID, SubscriptionID: s.ID, Reason: invoice.InvoicingReasonSubscriptionPeriodic,
		Period: b.Subscription, ChargesPeriod: b.Charges, FixedChargesPeriod: b.FixedCharges,
		Timestamp: ts, Recurring: true, CreatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Invoices.Create(ctx, inv); err != nil {
			return err
		}
		return r.InvoiceSubscriptions.Create(ctx, is)
	})
	// The environment reflects the invoice subscription just stored.
	return fees.Scope{Env: f.env(s), Invoice: inv, InvoiceSubscription: is, Boundaries: b, Timestamp: ts}
}

// usageScope returns the current usage scope of s at ts.
func (f *fixture) usageScope(s *subscription.Subscription, ts time.Time) fees.Scope {
	f.T.Helper()
	return fees.Scope{Env: f.env(s), Boundaries: f.boundaries(s, ts, period.CurrentUsage), Timestamp: ts}
}

// store stores fees.
func (f *fixture) store(fs ...*invoice.Fee) {
	f.T.Helper()
	f.Do(func(ctx context.Context, r port.Repos) error {
		for _, fee := range fs {
			if err := r.Fees.Create(ctx, fee); err != nil {
				return err
			}
		}
		return nil
	})
}

// adjust stores an adjusted fee.
func (f *fixture) adjust(a *invoice.AdjustedFee) {
	f.T.Helper()
	if a.ID.IsZero() {
		a.ID = core.NewID[core.AdjustedFeeID](f.IDs)
	}
	a.OrganizationID = f.OrgID()
	f.Do(func(ctx context.Context, r port.Repos) error { return r.AdjustedFees.Create(ctx, a) })
}

// period returns the half-open period [from, to).
func span(from, to time.Time) period.Period { return period.Period{From: from, To: to} }

// monthly returns the boundaries of a calendar month billed in full.
func monthly(from, to time.Time) period.Boundaries {
	p := span(from, to)
	days := int(to.Sub(from).Hours() / 24)
	return period.Boundaries{
		Subscription: p, Charges: p, ChargesBilled: true, FixedCharges: p, FixedChargesBilled: true,
		ChargesDays: days, FixedChargesDays: days,
	}
}

// dec parses a decimal.
func dec(s string) money.Decimal { return money.MustParse(s) }

// eqDec reports whether two decimals are equal.
func eqDec(a, b money.Decimal) bool { return a.Cmp(b) == 0 }

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// mustNoErr fails the test on err.
func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error = %v, want nil", err)
	}
}

// checkAmount checks a fee's amount and precise amount.
func checkAmount(t *testing.T, f *invoice.Fee, amount int64, precise string) {
	t.Helper()
	if f == nil {
		t.Fatalf("got fee = nil, want one")
	}
	if f.Amount != amount {
		t.Errorf("got amount = %d, want = %d", f.Amount, amount)
	}
	if precise != "" && f.PreciseAmount.Round(10, money.HalfUp).Cmp(dec(precise).Round(10, money.HalfUp)) != 0 {
		t.Errorf("got precise amount = %s, want = %s", f.PreciseAmount, precise)
	}
}
