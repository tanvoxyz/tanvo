package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

// TestCatalogChanges checks that catalog writes are notified on
// store.CatalogChannel when, and only when, their transaction commits, and
// handed to the WithCatalogChanges hook after the commit.
func TestCatalogChanges(t *testing.T) {
	db := pgtest.New(t, template)
	var hooked [][]store.CatalogChange
	s, err := store.Open(t.Context(), db.AdminDSN, store.WithCatalogChanges(func(_ context.Context, chs []store.CatalogChange) {
		hooked = append(hooked, slices.Clone(chs))
	}))
	if err != nil {
		t.Fatalf("Open() error got = %v, want = nil", err)
	}
	t.Cleanup(s.Pool().Close)

	listener, err := s.Pool().Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire() error got = %v, want = nil", err)
	}
	defer listener.Release()
	if _, err := listener.Exec(t.Context(), "LISTEN "+store.CatalogChannel); err != nil {
		t.Fatalf("LISTEN error got = %v, want = nil", err)
	}
	next := func() *store.CatalogChange {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		n, err := listener.Conn().WaitForNotification(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err != nil {
			t.Fatalf("WaitForNotification() error got = %v, want = nil", err)
		}
		var ch store.CatalogChange
		if err := json.Unmarshal([]byte(n.Payload), &ch); err != nil {
			t.Fatalf("payload %q: %v", n.Payload, err)
		}
		return &ch
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newID := func() string { return uuid.Must(uuid.NewV7()).String() }
	org := organization.New(core.MustParseID[core.OrganizationID](newID()), "Acme", now)
	m := &catalog.BillableMetric{
		ID: core.MustParseID[core.BillableMetricID](newID()), OrganizationID: org.ID, Code: "calls", Name: "Calls",
		AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		return r.Organizations.Create(ctx, org)
	}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	if len(hooked) != 0 {
		t.Errorf("hook after a non-catalog write: got = %v, want = none", hooked)
	}

	// A rolled back write is neither notified nor hooked.
	errRollback := errors.New("rollback")
	err = s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		if err := r.Metrics.Create(ctx, m); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("InTx() error got = %v, want = %v", err, errRollback)
	}
	if got := next(); got != nil {
		t.Errorf("notification after a rollback: got = %+v, want = none", got)
	}
	if len(hooked) != 0 {
		t.Errorf("hook after a rollback: got = %v, want = none", hooked)
	}

	// The writes of a committed transaction are notified, in order.
	if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		if err := r.Metrics.Create(ctx, m); err != nil {
			return err
		}
		return r.Metrics.Discard(ctx, org.ID, m.ID, now)
	}); err != nil {
		t.Fatalf("InTx() error got = %v, want = nil", err)
	}
	want := []store.CatalogChange{
		{OrganizationID: org.ID, Kind: store.CatalogMetric, Key: "calls"},
		{OrganizationID: org.ID, Kind: store.CatalogMetric},
	}
	var got []store.CatalogChange
	for ch := next(); ch != nil; ch = next() {
		got = append(got, *ch)
	}
	if !slices.Equal(got, want) {
		t.Errorf("notifications got = %+v, want = %+v", got, want)
	}
	if len(hooked) != 1 || !slices.Equal(hooked[0], want) {
		t.Errorf("hook got = %+v, want = [%+v]", hooked, want)
	}

	// A renamed subscription expires its old external ID and its new one.
	be := organization.NewBillingEntity(org.ID, core.MustParseID[core.BillingEntityID](newID()), "main", "Acme", now)
	be.IsDefault = true
	c := customer.New(org.ID, core.MustParseID[core.CustomerID](newID()), "cus_1", be.ID, now)
	plan := &catalog.Plan{
		ID: core.MustParseID[core.PlanID](newID()), OrganizationID: org.ID, Code: "basic", Name: "Basic",
		Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"), Status: catalog.PlanStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	sub := subscription.New(org.ID, core.MustParseID[core.SubscriptionID](newID()), c.ID, plan.ID, "sub_old", now, subscription.PlanFacts{}, now)
	if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		if err := r.BillingEntities.Create(ctx, be); err != nil {
			return err
		}
		if err := r.Customers.Create(ctx, c); err != nil {
			return err
		}
		if err := r.Plans.Create(ctx, plan); err != nil {
			return err
		}
		return r.Subscriptions.Create(ctx, sub)
	}); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	// Drain the notifications of the creation.
	for drained := next(); drained != nil; drained = next() {
		_ = drained
	}
	hooked = nil
	sub.ExternalID = "sub_new"
	if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		return r.Subscriptions.Update(ctx, sub)
	}); err != nil {
		t.Fatalf("rename subscription: %v", err)
	}
	want = []store.CatalogChange{
		{OrganizationID: org.ID, Kind: store.CatalogSubscription, Key: "sub_old"},
		{OrganizationID: org.ID, Kind: store.CatalogSubscription, Key: "sub_new"},
	}
	got = nil
	for ch := next(); ch != nil; ch = next() {
		got = append(got, *ch)
	}
	if !slices.Equal(got, want) {
		t.Errorf("rename notifications got = %+v, want = %+v", got, want)
	}
	if len(hooked) != 1 || !slices.Equal(hooked[0], want) {
		t.Errorf("rename hook got = %+v, want = [%+v]", hooked, want)
	}
}
