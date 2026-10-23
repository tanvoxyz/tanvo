package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// catalogChannel is the PostgreSQL notification channel of the catalog
// changes.
const catalogChannel = store.CatalogChannel

// listenRetry is the pause before listening again after a lost
// connection.
const listenRetry = time.Second

// catalogChanges expires the catalog that ingestion caches when the
// catalog changes, in every process, as lago expires its expression
// cache (BillableMetrics::ExpressionCacheService.expire_cache on create,
// on the update of the expression or field name, and on destroy).
//
// The store sends every write of a billable metric, a charge (with its
// filters) or a subscription as a notification on store.CatalogChannel
// inside the writing transaction, so PostgreSQL delivers it to every
// listening process when, and only when, the transaction commits; the
// store also hands the changes to [catalogChanges.committed] after the
// commit, so that this process's caches are fresh before the write
// returns. [catalogChanges.run] receives the notifications in the API and
// consumer processes (PostgreSQL, ClickHouse and Kafka modes alike, as
// every mode reads the catalog from PostgreSQL) and purges the caches
// whenever it (re)connects, since notifications sent while no connection
// listened are lost. ingest.DefaultCacheTTL remains the fallback.
type catalogChanges struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu     sync.Mutex
	caches []*ingest.Cache
}

// newCatalogChanges returns the listener on pool; a nil pool keeps the
// changes in the process.
func newCatalogChanges(pool *pgxpool.Pool, log *slog.Logger) *catalogChanges {
	return &catalogChanges{pool: pool, log: log}
}

// register makes c invalidate cache.
func (c *catalogChanges) register(cache *ingest.Cache) *ingest.Cache {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caches = append(c.caches, cache)
	return cache
}

// committed invalidates the caches of this process for the catalog
// changes of a committed transaction (store.WithCatalogChanges).
func (c *catalogChanges) committed(_ context.Context, chs []store.CatalogChange) {
	for _, ch := range chs {
		c.invalidate(ingest.Change{OrganizationID: ch.OrganizationID, Kind: ingest.ChangeKind(ch.Kind), Key: ch.Key})
	}
}

// purge drops every entry of the caches of this process.
func (c *catalogChanges) purge() {
	c.mu.Lock()
	caches := c.caches
	c.mu.Unlock()
	for _, cache := range caches {
		cache.Purge()
	}
}

func (c *catalogChanges) invalidate(ch ingest.Change) {
	c.mu.Lock()
	caches := c.caches
	c.mu.Unlock()
	for _, cache := range caches {
		cache.Invalidate(ch)
	}
}

// run receives the notifications of the other processes until ctx ends,
// listening again after a lost connection.
func (c *catalogChanges) run(ctx context.Context) error {
	if c.pool == nil {
		return nil
	}
	for {
		err := c.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		c.log.WarnContext(ctx, "catalog changes: listen", "error", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(listenRetry):
		}
	}
}

// listen holds one connection listening on [catalogChannel] until it
// fails or ctx ends.
func (c *catalogChanges) listen(ctx context.Context) error {
	conn, err := c.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	// The connection is left in the LISTEN state: it is closed, not
	// returned to the pool.
	defer func() {
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+catalogChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// Changes committed while no connection listened were not received.
	c.purge()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("wait: %w", err)
		}
		var ch ingest.Change
		if err := json.Unmarshal([]byte(n.Payload), &ch); err != nil || ch.OrganizationID.IsZero() {
			c.log.WarnContext(ctx, "catalog changes: invalid notification", "payload", n.Payload)
			continue
		}
		c.invalidate(ch)
	}
}
