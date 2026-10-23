package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// scopeAll is the value of tanvo.organization_id that admits every
// organization's rows, for the explicitly cross-organization methods.
const scopeAll = "*"

// txState is one running transaction and the repositories bound to it. It
// is not safe for concurrent use, like the pgx.Tx it wraps.
type txState struct {
	store *Store
	tx    pgx.Tx
	q     *db.Queries
	rls   bool
	// scope is the current value of tanvo.organization_id ("" when unset).
	scope string
	repos port.Repos
}

// setScope sets tanvo.organization_id to v for the rest of the transaction
// when row-level security is on and the value changes.
func (t *txState) setScope(ctx context.Context, v string) error {
	if !t.rls || t.scope == v {
		return nil
	}
	if _, err := t.tx.Exec(ctx, "SELECT set_config('tanvo.organization_id', $1, true)", v); err != nil {
		return fmt.Errorf("store: set organization scope: %w", mapErr(err))
	}
	t.scope = v
	return nil
}

// queries returns the sqlc queries bound to the transaction, with the
// row-level security scope set to org.
func (t *txState) queries(ctx context.Context, org core.OrganizationID) (*db.Queries, error) {
	if org.IsZero() {
		return nil, fmt.Errorf("store: organization: %w", core.ErrInvalidValue)
	}
	if err := t.setScope(ctx, org.String()); err != nil {
		return nil, err
	}
	if t.q == nil {
		t.q = db.New(t.tx)
	}
	return t.q, nil
}

// queriesAll returns the queries with the row-level security scope opened
// to every organization. Only the methods documented as not
// organization-scoped (token lookups, sign-in, scheduler scans) use it.
func (t *txState) queriesAll(ctx context.Context) (*db.Queries, error) {
	if err := t.setScope(ctx, scopeAll); err != nil {
		return nil, err
	}
	if t.q == nil {
		t.q = db.New(t.tx)
	}
	return t.q, nil
}

// guard runs fn under a savepoint, so that a constraint violation it
// raises (a unique key taken by an update, a delete blocked by a foreign
// key) is rolled back to the savepoint and the transaction stays usable.
// The error returned by fn is returned mapped by [mapErr].
//
// Inserts do not need it: they use ON CONFLICT DO NOTHING. Each guard costs
// two round trips (SAVEPOINT, RELEASE), so only writes that can fail on a
// rule of the repository contract use it.
func (t *txState) guard(ctx context.Context, fn func() error) error {
	if _, err := t.tx.Exec(ctx, "SAVEPOINT tanvo_guard"); err != nil {
		return fmt.Errorf("store: savepoint: %w", mapErr(err))
	}
	if err := fn(); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if _, rbErr := t.tx.Exec(ctx, "ROLLBACK TO SAVEPOINT tanvo_guard; RELEASE SAVEPOINT tanvo_guard"); rbErr != nil {
				return errors.Join(mapErr(err), fmt.Errorf("store: rollback to savepoint: %w", rbErr))
			}
		} else if _, rlErr := t.tx.Exec(ctx, "RELEASE SAVEPOINT tanvo_guard"); rlErr != nil {
			return errors.Join(mapErr(err), fmt.Errorf("store: release savepoint: %w", rlErr))
		}
		return mapErr(err)
	}
	if _, err := t.tx.Exec(ctx, "RELEASE SAVEPOINT tanvo_guard"); err != nil {
		return fmt.Errorf("store: release savepoint: %w", mapErr(err))
	}
	return nil
}
