package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// CatalogChannel is the PostgreSQL notification channel on which every
// transaction that writes the catalog ingestion reads (billable metrics,
// charges and their filters, subscriptions) announces its changes. The
// notifications are sent inside the writing transaction, so PostgreSQL
// delivers them only when it commits, and to every listening process.
const CatalogChannel = "tanvo_catalog_changes"

// Kinds of [CatalogChange], the change kinds of libs/ingest's catalog
// cache.
const (
	// CatalogMetric: a billable metric was written; the key is its code,
	// empty for every metric of the organization.
	CatalogMetric = "metric"
	// CatalogSubscription: a subscription was written; the key is its
	// external ID.
	CatalogSubscription = "subscription"
	// CatalogPlan: the charges of a plan were written; the key is the plan
	// ID, empty for every plan of the organization.
	CatalogPlan = "plan"
)

// CatalogChange is the payload of a notification on [CatalogChannel], in
// JSON: {"organization_id": …, "kind": …, "key": …}, the encoding of
// libs/ingest's Change.
type CatalogChange struct {
	// OrganizationID is the organization of the change.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// Kind is one of the Catalog* kinds.
	Kind string `json:"kind"`
	// Key identifies the changed object (see the kinds).
	Key string `json:"key,omitempty"`
}

// WithCatalogChanges makes the store call f with the catalog changes of
// each transaction once it commits, so that the caches of this process are
// invalidated before InTx returns; the other processes learn of them from
// [CatalogChannel]. f must not block.
func WithCatalogChanges(f func(context.Context, []CatalogChange)) Option {
	return func(s *Store) { s.onCatalog = f }
}

// noteCatalog records ch as a change of the transaction when err is nil,
// and returns err.
func (t *txState) noteCatalog(err error, ch CatalogChange) error {
	if err != nil {
		return err
	}
	for _, x := range t.catalog {
		if x == ch {
			return nil
		}
	}
	t.catalog = append(t.catalog, ch)
	return nil
}

// notifyCatalog sends the catalog changes of the transaction on
// [CatalogChannel]; PostgreSQL delivers them when the transaction commits.
func (t *txState) notifyCatalog(ctx context.Context) error {
	if len(t.catalog) == 0 {
		return nil
	}
	payloads := make([]string, len(t.catalog))
	for i, ch := range t.catalog {
		b, err := json.Marshal(ch)
		if err != nil {
			return fmt.Errorf("store: encode catalog change: %w", err)
		}
		payloads[i] = string(b)
	}
	if _, err := t.tx.Exec(ctx, "SELECT pg_notify($1, p) FROM unnest($2::text[]) AS p", CatalogChannel, payloads); err != nil {
		return fmt.Errorf("store: notify catalog changes: %w", mapErr(err))
	}
	return nil
}
