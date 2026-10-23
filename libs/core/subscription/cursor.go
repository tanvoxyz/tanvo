package subscription

import (
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// RecentCursor is the position of s in the core.OrderRecent list:
// subscription_at descending, created_at descending, then ID ascending
// (lago SubscriptionsQuery, app/queries/subscriptions_query.rb:24-30 and
// app/queries/base_query.rb:111-112).
func RecentCursor(s *Subscription) core.Cursor {
	return core.Cursor{
		Key: s.SubscriptionAt.UTC().Format(time.RFC3339Nano) + " " + s.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:  s.ID.UUID(),
	}
}

// ParseRecentCursor returns the subscription_at and created_at of a
// [RecentCursor]; a malformed key fails with core.ErrInvalidValue.
func ParseRecentCursor(c core.Cursor) (subscriptionAt, createdAt time.Time, err error) {
	a, b, ok := strings.Cut(c.Key, " ")
	if ok {
		subscriptionAt, err = time.Parse(time.RFC3339Nano, a)
		if err == nil {
			createdAt, err = time.Parse(time.RFC3339Nano, b)
		}
	}
	if !ok || err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return subscriptionAt, createdAt, nil
}

// RecentBefore reports whether s comes before the position c in the
// core.OrderRecent list (c from [ParseRecentCursor]).
func RecentBefore(s *Subscription, subscriptionAt, createdAt time.Time, id core.SubscriptionID) bool {
	if c := s.SubscriptionAt.Compare(subscriptionAt); c != 0 {
		return c > 0
	}
	if c := s.CreatedAt.Compare(createdAt); c != 0 {
		return c > 0
	}
	return s.ID.Compare(id) < 0
}
