package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var _ port.Slots = (*Store)(nil)

// errLeaseLost is the renewal failure of a lease that another holder took
// after it expired.
var errLeaseLost = errors.New("lease taken by another holder")

// acquireSlotSQL claims the first slot of $1 among 0..$2-1 that has no
// lease or an expired one, for holder $3 until $4 milliseconds from now.
// It returns the slot it chose (NULL when every slot is held) and the
// slot it claimed (NULL when another acquirer claimed the chosen slot
// first). Two acquirers that choose the same slot serialize on its
// primary key: the second's ON CONFLICT re-reads the committed lease,
// which is no longer expired, and claims nothing.
const acquireSlotSQL = `WITH free AS (
	SELECT i FROM generate_series(0, $2::integer - 1) AS i
	WHERE NOT EXISTS (SELECT 1 FROM concurrency_slots c WHERE c.name = $1 AND c.slot = i AND c.expires_at >= now())
	ORDER BY i
	LIMIT 1
), claimed AS (
	INSERT INTO concurrency_slots AS s (name, slot, holder, expires_at)
	SELECT $1, i, $3, now() + $4::bigint * interval '1 millisecond' FROM free
	ON CONFLICT (name, slot) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
	WHERE s.expires_at < now()
	RETURNING s.slot
)
SELECT (SELECT i FROM free), (SELECT slot FROM claimed)`

// renewSlotSQL extends the lease of holder $3 on slot $2 of $1.
const renewSlotSQL = `UPDATE concurrency_slots SET expires_at = now() + $4::bigint * interval '1 millisecond'
WHERE name = $1 AND slot = $2 AND holder = $3`

// releaseSlotSQL deletes the lease of holder $3 on slot $2 of $1.
const releaseSlotSQL = `DELETE FROM concurrency_slots WHERE name = $1 AND slot = $2 AND holder = $3`

// AcquireSlot implements [port.Slots] with the leases of the
// concurrency_slots table, shared by every process on the database. Each
// statement runs on its own in the store's pool, so that it works through
// a pooler in transaction mode (PgBouncer), and no connection is held
// between them. Expiry follows the database clock. A lease that expired
// is overwritten by the next acquirer of its slot, so the rows of crashed
// holders need no cleanup job.
func (s *Store) AcquireSlot(ctx context.Context, name string, limit int) (release func(), ok bool, err error) {
	if name == "" || limit < 1 {
		return nil, false, fmt.Errorf("slot %q: %w", name, core.ErrInvalidValue)
	}
	holder := uuid.New()
	ttl := max(s.slotTTL.Milliseconds(), 1)
	// An attempt claims nothing only when another acquirer claimed the
	// slot it chose at the same time; limit attempts bound the retries.
	for range limit {
		var chosen, claimed *int32
		if err := s.pool.QueryRow(ctx, acquireSlotSQL, name, limit, holder, ttl).Scan(&chosen, &claimed); err != nil {
			return nil, false, fmt.Errorf("slot %s: %w", name, mapErr(err))
		}
		if chosen == nil {
			return nil, false, nil
		}
		if claimed == nil {
			continue
		}
		slot := *claimed
		renew := func(ctx context.Context) error {
			tag, err := s.pool.Exec(ctx, renewSlotSQL, name, slot, holder, ttl)
			if err != nil {
				return fmt.Errorf("slot %s/%d: %w", name, slot, mapErr(err))
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("slot %s/%d: %w", name, slot, errLeaseLost)
			}
			return nil
		}
		drop := func(ctx context.Context) error {
			if _, err := s.pool.Exec(ctx, releaseSlotSQL, name, slot, holder); err != nil {
				return fmt.Errorf("slot %s/%d: %w", name, slot, mapErr(err))
			}
			return nil
		}
		return port.HoldSlot(ctx, name, s.slotTTL/3, renew, drop, s.log), true, nil
	}
	return nil, false, nil
}
