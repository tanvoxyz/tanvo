package invoicing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// lockRecorder records the locks taken through the repositories.
type lockRecorder struct {
	inner port.Locker
	keys  []port.LockKey
}

func (l *lockRecorder) Lock(ctx context.Context, org core.OrganizationID, k port.LockKey) error {
	l.keys = append(l.keys, k)
	return l.inner.Lock(ctx, org, k)
}

// recordingTx hands out repositories whose locker is a lockRecorder.
type recordingTx struct {
	inner port.Transactor
	rec   *lockRecorder
}

func (t *recordingTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	return t.inner.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t.rec.inner = r.Locks
		r.Locks = t.rec
		return fn(ctx, r)
	})
}

// source: app/services/invoices/refresh_draft_and_finalize_service.rb:16
// Finalizing a draft takes the invoice's lock before loading it (lago
// invoice.with_lock), so concurrent finalizations number it once.
func TestFinalizeDraftLocksInvoice(t *testing.T) {
	f, _, draft := draftFixture(t)
	f.Clock.Set(billingtest.Date(2026, time.February, 4))
	rec := &lockRecorder{}
	d := *f.Deps
	d.Tx = &recordingTx{inner: f.Deps.Tx, rec: rec}
	svc := invoicing.New(&d)
	if _, err := svc.FinalizeDraft(f.Ctx, f.OrgID(), draft.ID); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	want := port.LockKey{Scope: "invoice", ID: draft.ID.UUID()}
	if len(rec.keys) == 0 || rec.keys[0] != want {
		t.Errorf("locks = %v, want = %v first", rec.keys, want)
	}
	if !slices.Contains(rec.keys, want) {
		t.Errorf("locks = %v, want = %v", rec.keys, want)
	}
}

// source: app/services/fees/create_pay_in_advance_service.rb:46
// Billing an in-advance event takes its event lock before checking for
// fees already billed, so concurrent deliveries of one event bill it
// once (lago's unique index on the event transaction and charge).
func TestInAdvanceChargeLocksEvent(t *testing.T) {
	ts := billingtest.Date(2026, time.January, 15, 10)
	f, sub := inAdvance(t, ts)
	rec := &lockRecorder{}
	d := *f.Deps
	d.Tx = &recordingTx{inner: f.Deps.Tx, rec: rec}
	svc := invoicing.New(&d)
	e := f.Event(sub, "calls", ts)
	if _, err := svc.InAdvanceCharge(f.Ctx, f.OrgID(), kernel.InAdvanceChargeArgs{
		SubscriptionID: sub.ID, TransactionID: e.TransactionID, Timestamp: ts, Code: "calls",
	}); err != nil {
		t.Fatalf("in-advance charge: %v", err)
	}
	want := kernel.EventLock(sub.ID, e.TransactionID)
	if len(rec.keys) == 0 || rec.keys[0] != want {
		t.Errorf("locks = %v, want = %v first", rec.keys, want)
	}
}

// Billing takes the customer's billing_schedule lock and then the
// subscription locks, ordered by ID, before reading what was billed, as
// the lifecycle does, so that it serializes with a concurrent termination
// or plan change. Lago locks the subscription rows ordered by ID for
// activation billing and relies on its unique indexes otherwise.
//
// source: app/services/invoices/subscription_service.rb:156
func TestBillSubscriptionLocksSchedule(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	rec := &lockRecorder{}
	d := *f.Deps
	d.Tx = &recordingTx{inner: f.Deps.Tx, rec: rec}
	svc := invoicing.New(&d)
	if _, err := svc.BillSubscription(f.Ctx, f.OrgID(), kernel.BillSubscriptionArgs{SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: billingDay}); err != nil {
		t.Fatalf("bill: %v", err)
	}
	want := []port.LockKey{port.CustomerLock(customer.LockScopeBillingSchedule, f.cust.ID), port.SubscriptionLock(sub.ID)}
	if len(rec.keys) < 2 || !slices.Equal(rec.keys[:2], want) {
		t.Errorf("locks = %v, want = %v first", rec.keys, want)
	}
}

// Billing the in-advance charges of a period takes the same locks.
//
// source: app/services/invoices/advance_charges_service.rb:1
func TestAdvanceChargesLocksSchedule(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	rec := &lockRecorder{}
	f.Do(func(ctx context.Context, r port.Repos) error {
		rec.inner, r.Locks = r.Locks, rec
		_, err := f.svc.AdvanceCharges(ctx, r, f.OrgID(), []core.SubscriptionID{sub.ID}, billingDay)
		return err
	})
	want := []port.LockKey{port.CustomerLock(customer.LockScopeBillingSchedule, f.cust.ID), port.SubscriptionLock(sub.ID)}
	if len(rec.keys) < 2 || !slices.Equal(rec.keys[:2], want) {
		t.Errorf("locks = %v, want = %v first", rec.keys, want)
	}
}
