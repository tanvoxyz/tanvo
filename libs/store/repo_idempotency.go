package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindIdempotency binds the idempotency key repository.
func (t *txState) bindIdempotency(r *port.Repos) {
	r.IdempotencyKeys = idempotencyRepo{t}
}

// idempotencyRepo implements idempotency.Repository.
type idempotencyRepo struct{ t *txState }

var _ idempotency.Repository = idempotencyRepo{}

func (r idempotencyRepo) Claim(ctx context.Context, rec *idempotency.Record) (bool, error) {
	q, err := r.t.queries(ctx, rec.OrganizationID)
	if err != nil {
		return false, err
	}
	n, err := q.ClaimIdempotencyKey(ctx, db.ClaimIdempotencyKeyParams{
		OrganizationID: rec.OrganizationID.UUID(),
		Key:            rec.Key,
		Procedure:      rec.Procedure,
		RequestHash:    rec.RequestHash,
		CreatedAt:      rec.CreatedAt,
		ExpiresAt:      rec.ExpiresAt,
	})
	if err != nil {
		return false, fmt.Errorf("claim idempotency key: %w", mapErr(err))
	}
	return n > 0, nil
}

func (r idempotencyRepo) Get(ctx context.Context, org core.OrganizationID, key string) (*idempotency.Record, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{OrganizationID: org.UUID(), Key: key})
	if err != nil {
		return nil, getErr("idempotency_key", key, err)
	}
	return &idempotency.Record{
		OrganizationID: idOf[core.OrganizationID](row.OrganizationID),
		Key:            row.Key,
		Procedure:      row.Procedure,
		RequestHash:    row.RequestHash,
		Response:       row.Response,
		CreatedAt:      row.CreatedAt.UTC(),
		ExpiresAt:      row.ExpiresAt.UTC(),
	}, nil
}

func (r idempotencyRepo) Complete(ctx context.Context, org core.OrganizationID, key string, response []byte, expiresAt time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if response == nil {
		response = []byte{}
	}
	n, err := q.CompleteIdempotencyKey(ctx, db.CompleteIdempotencyKeyParams{OrganizationID: org.UUID(), Key: key, Response: response, ExpiresAt: expiresAt})
	return updateErr("idempotency_key", key, n, err)
}

func (r idempotencyRepo) Release(ctx context.Context, org core.OrganizationID, key string) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.ReleaseIdempotencyKey(ctx, db.ReleaseIdempotencyKeyParams{OrganizationID: org.UUID(), Key: key}); err != nil {
		return fmt.Errorf("release idempotency key: %w", mapErr(err))
	}
	return nil
}

func (r idempotencyRepo) Purge(ctx context.Context, org core.OrganizationID, now time.Time, limit int) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.PurgeIdempotencyKeys(ctx, db.PurgeIdempotencyKeysParams{OrganizationID: org.UUID(), Now: now, Lim: int32(min(max(limit, 0), 1<<20))}) //nolint:gosec // bounded above
	if err != nil {
		return 0, fmt.Errorf("purge idempotency keys: %w", mapErr(err))
	}
	return int(n), nil
}

// Organizations is not organization-scoped: it feeds the purge clock job.
func (r idempotencyRepo) Organizations(ctx context.Context, now time.Time) ([]core.OrganizationID, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIdempotencyKeyOrganizations(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("list idempotency key organizations: %w", mapErr(err))
	}
	out := make([]core.OrganizationID, len(rows))
	for i, u := range rows {
		out[i] = idOf[core.OrganizationID](u)
	}
	return out, nil
}
