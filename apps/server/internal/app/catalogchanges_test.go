package app

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// countingCatalog is an ingest.Catalog of no objects counting its metric
// lookups.
type countingCatalog struct {
	mu    sync.Mutex
	calls int
}

func (c *countingCatalog) Metric(context.Context, core.OrganizationID, string) (*catalog.BillableMetric, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil, nil
}

func (c *countingCatalog) Subscriptions(context.Context, core.OrganizationID, string) ([]*subscription.Subscription, error) {
	return nil, nil
}

func (c *countingCatalog) Charges(context.Context, core.OrganizationID, core.PlanID, core.BillableMetricID) ([]*catalog.Charge, error) {
	return nil, nil
}

func (c *countingCatalog) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestCatalogChangesListen checks that a process listening on the catalog
// channel evicts the organization a committed notification names, and
// that the committed hook evicts the caches of the writing process.
func TestCatalogChangesListen(t *testing.T) {
	db := pgtest.New(t, pgtest.Template{Name: "empty"})
	pool := pgtest.Pool(t, db.AdminDSN)
	log := slog.New(slog.DiscardHandler)
	org := core.MustParseID[core.OrganizationID](uuid.Must(uuid.NewV7()).String())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	remoteCat := &countingCatalog{}
	remote := newCatalogChanges(pool, log)
	remoteCache := remote.register(ingest.NewCache(remoteCat))
	lookup := func(c *ingest.Cache) {
		t.Helper()
		if _, err := c.Metric(ctx, org, "calls"); err != nil {
			t.Fatalf("Metric() error got = %v, want = nil", err)
		}
	}
	lookup(remoteCache)
	done := make(chan error, 1)
	go func() { done <- remote.run(ctx) }()
	// The listener purges the cache when it connects.
	waitFor(t, func() bool { lookup(remoteCache); return remoteCat.count() == 2 })
	lookup(remoteCache)
	if got := remoteCat.count(); got != 2 {
		t.Fatalf("lookups after the purge: got = %d, want = 2", got)
	}

	// A rolled back notification is never delivered; a committed one is.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin() error got = %v, want = nil", err)
	}
	payload := `{"organization_id":"` + org.String() + `","kind":"metric","key":"calls"}`
	if _, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", store.CatalogChannel, payload); err != nil {
		t.Fatalf("pg_notify error got = %v, want = nil", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback() error got = %v, want = nil", err)
	}
	time.Sleep(200 * time.Millisecond)
	lookup(remoteCache)
	if got := remoteCat.count(); got != 2 {
		t.Errorf("lookups after a rollback: got = %d, want = 2", got)
	}
	if _, err := pool.Exec(ctx, "SELECT pg_notify($1, $2)", store.CatalogChannel, payload); err != nil {
		t.Fatalf("pg_notify error got = %v, want = nil", err)
	}
	waitFor(t, func() bool { lookup(remoteCache); return remoteCat.count() == 3 })

	// The writing process evicts its own caches synchronously.
	localCat := &countingCatalog{}
	local := newCatalogChanges(nil, log)
	localCache := local.register(ingest.NewCache(localCat))
	lookup(localCache)
	local.committed(ctx, []store.CatalogChange{{OrganizationID: org, Kind: store.CatalogMetric, Key: "calls"}})
	lookup(localCache)
	if got := localCat.count(); got != 2 {
		t.Errorf("local lookups after a commit: got = %d, want = 2", got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("run() error got = %v, want = nil", err)
	}
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
