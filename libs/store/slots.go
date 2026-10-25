package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// slotReleaseTimeout bounds the unlock of a released slot.
const slotReleaseTimeout = 5 * time.Second

// AcquireSlot takes one of the limit slots named name, shared by every
// process on the database, and returns the function that frees it; ok is
// false, with nothing held, when all are taken. A slot is a session
// advisory lock held on a connection taken from the pool until release,
// so a crashed holder frees it with its connection.
func (s *Store) AcquireSlot(ctx context.Context, name string, limit int) (release func(), ok bool, err error) {
	if name == "" || limit < 1 {
		return nil, false, fmt.Errorf("slot %q: %w", name, core.ErrInvalidValue)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("slot %s: %w", name, mapErr(err))
	}
	for i := range limit {
		key := "slot/" + name + "/" + strconv.Itoa(i)
		var got bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", key).Scan(&got); err != nil {
			conn.Release()
			return nil, false, fmt.Errorf("slot %s: %w", name, mapErr(err))
		}
		if !got {
			continue
		}
		return func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), slotReleaseTimeout)
			defer cancel()
			if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", key); err != nil {
				// Closing the session frees its locks.
				_ = conn.Conn().Close(ctx)
			}
			conn.Release()
		}, true, nil
	}
	conn.Release()
	return nil, false, nil
}
