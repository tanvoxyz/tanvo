package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// eventFixture returns an unstored event of org.
func eventFixture(e *env, org core.OrganizationID, transactionID, value string) *event.Event {
	return &event.Event{
		ID: core.NewID[core.EventID](e.ids), OrganizationID: org, TransactionID: transactionID, Code: "api_calls",
		ExternalSubscriptionID: "sub_1", Timestamp: at(-time.Minute), Properties: map[string]string{"value": value},
		IngestedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func eventTxID(e *event.Event) string { return e.TransactionID }

func testEvent(t *testing.T, h Harness) {
	t.Run("cached aggregations", func(t *testing.T) {
		t.Parallel()
		testCachedAggregations(t, h)
	})
	t.Run("append and get", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			full := eventFixture(e, org, "tr_full", "1")
			full.Properties = map[string]string{"value": "12.5", "region": "eu", "empty": ""}
			full.PreciseTotalAmount = ptr(money.MustParse("1234.000000000001"))
			full.Metadata = event.Metadata{UserAgent: "tanvo-go/1.0", IPAddress: "203.0.113.7"}
			bare := eventFixture(e, org, "tr_bare", "")
			bare.Properties = nil
			accepted, dupes, err := r.Events.Append(ctx, org, []*event.Event{full, bare})
			noErr(t, "Append()", err)
			equal(t, "Append() accepted", accepted, []*event.Event{full, bare})
			equal(t, "Append() duplicates", len(dupes), 0)
			got, err := r.Events.Get(ctx, org, "sub_1", "tr_full")
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Events.Get(ctx, org, "sub_1", "tr_bare")
			noErr(t, "Get() bare", err)
			equal(t, "Get() bare", got, bare)

			// The first event of a deduplication key wins, within a call
			// and across calls (domain §24 item 11).
			first, second, repeat := eventFixture(e, org, "tr_1", "1"), eventFixture(e, org, "tr_2", "2"), eventFixture(e, org, "tr_1", "3")
			accepted, dupes, err = r.Events.Append(ctx, org, []*event.Event{first, second, repeat})
			noErr(t, "Append() with a repeat", err)
			equal(t, "Append() with a repeat accepted", ids(accepted, eventTxID), []string{"tr_1", "tr_2"})
			equal(t, "Append() with a repeat duplicates", dupes, []*event.Event{repeat})
			resent := eventFixture(e, org, "tr_2", "4")
			accepted, dupes, err = r.Events.Append(ctx, org, []*event.Event{resent, eventFixture(e, org, "tr_3", "5")})
			noErr(t, "Append() re-sent", err)
			equal(t, "Append() re-sent accepted", ids(accepted, eventTxID), []string{"tr_3"})
			equal(t, "Append() re-sent duplicates", dupes, []*event.Event{resent})
			got, err = r.Events.Get(ctx, org, "sub_1", "tr_1")
			noErr(t, "Get() first wins", err)
			equal(t, "Get() first wins", got, first)

			accepted, dupes, err = r.Events.Append(ctx, org, nil)
			noErr(t, "Append() of nothing", err)
			equal(t, "Append() of nothing", len(accepted)+len(dupes), 0)

			// The same transaction ID in another organization is new.
			o := eventFixture(e, other, "tr_1", "9")
			accepted, _, err = r.Events.Append(ctx, other, []*event.Event{o})
			noErr(t, "Append() in another organization", err)
			equal(t, "Append() in another organization", accepted, []*event.Event{o})
			got, err = r.Events.Get(ctx, other, "sub_1", "tr_1")
			noErr(t, "Get() in another organization", err)
			equal(t, "Get() in another organization", got, o)
			_, err = r.Events.Get(ctx, other, "sub_1", "tr_2")
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Events.Get(ctx, org, "sub_1", "tr_404")
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// An event of another organization is refused.
			_, _, err = r.Events.Append(ctx, org, []*event.Event{eventFixture(e, other, "tr_9", "6")})
			wantErr(t, "Append() of a foreign event", err, core.ErrNotFound)
			_, err = r.Events.Get(ctx, org, "sub_1", "tr_9")
			wantErr(t, "Get() of a refused event", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("taken id", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, be := e.org("acme")
		org := be.OrganizationID
		stored := eventFixture(e, org, "tr_1", "1")
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, _, err := r.Events.Append(ctx, org, []*event.Event{stored})
			return err
		})
		err := e.txErr(func(ctx context.Context, r port.Repos) error {
			clash := eventFixture(e, org, "tr_2", "2")
			clash.ID = stored.ID
			_, _, err := r.Events.Append(ctx, org, []*event.Event{clash})
			return err
		})
		wantErr(t, "Append() with a taken ID", err, core.ErrConflict)
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, err := r.Events.Get(ctx, org, "sub_1", "tr_2")
			wantErr(t, "Get() after a refused append", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			stored := eventFixture(e, org, "tr_1", "1")
			stored.PreciseTotalAmount = ptr(money.MustParse("3"))
			stored.Metadata = event.Metadata{UserAgent: "curl/8", IPAddress: "198.51.100.1"}
			_, _, err := r.Events.Append(ctx, org, []*event.Event{stored})
			noErr(t, "Append()", err)

			fix := eventFixture(e, org, "tr_1", "10")
			fix.Code = "storage"
			fix.Timestamp = at(-time.Hour)
			fix.IngestedAt, fix.CreatedAt, fix.UpdatedAt = at(time.Hour), at(time.Hour), at(time.Hour)
			fix.Metadata = event.Metadata{UserAgent: "other"}
			noErr(t, "Replace()", r.Events.Replace(ctx, fix))
			want := *fix
			want.ID, want.IngestedAt, want.CreatedAt, want.Metadata = stored.ID, stored.IngestedAt, stored.CreatedAt, stored.Metadata
			got, err := r.Events.Get(ctx, org, "sub_1", "tr_1")
			noErr(t, "Get() after Replace()", err)
			equal(t, "Get() after Replace()", got, &want)

			moved := eventFixture(e, org, "tr_1", "0")
			moved.ExternalSubscriptionID = "sub_2"
			wantErr(t, "Replace() of another subscription", r.Events.Replace(ctx, moved), core.ErrNotFound)
			wantErr(t, "Replace() unknown", r.Events.Replace(ctx, eventFixture(e, org, "tr_404", "0")), core.ErrNotFound)
			wantErr(t, "Replace() in another organization", r.Events.Replace(ctx, eventFixture(e, other, "tr_1", "0")), core.ErrNotFound)
			return nil
		})
	})

	t.Run("deduplication key", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, be := e.org("acme")
		org := be.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			// lago db/structure.sql:12094 index_unique_transaction_id: one
			// transaction ID under two subscriptions is two events, and a
			// copy at another timestamp is a duplicate.
			first := eventFixture(e, org, "tr_1", "1")
			other := eventFixture(e, org, "tr_1", "2")
			other.ExternalSubscriptionID, other.IngestedAt = "sub_2", at(time.Minute)
			later := eventFixture(e, org, "tr_1", "3")
			later.Timestamp = at(-time.Hour)
			accepted, dupes, err := r.Events.Append(ctx, org, []*event.Event{other, first, later})
			noErr(t, "Append()", err)
			equal(t, "Append() accepted", accepted, []*event.Event{other, first})
			equal(t, "Append() duplicates", dupes, []*event.Event{later})
			got, err := r.Events.Get(ctx, org, "sub_2", "tr_1")
			noErr(t, "Get() of a subscription", err)
			equal(t, "Get() of a subscription", got, other)
			got, err = r.Events.Get(ctx, org, "", "tr_1")
			noErr(t, "Get() of any subscription", err)
			equal(t, "Get() of any subscription: the earliest ingested", got, first)
			return nil
		})
	})
}

func testCachedAggregations(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	org, other := a.OrganizationID, b.OrganizationID
	charge, filter := core.NewID[core.ChargeID](e.ids), core.NewID[core.ChargeFilterID](e.ids)
	base := event.CachedAggregationScope{ExternalSubscriptionID: "sub_1", ChargeID: charge}
	filtered := base
	filtered.ChargeFilterID = filter
	mk := func(o core.OrganizationID, sc event.CachedAggregationScope, ts time.Time, tx string, groups map[string]*string, created time.Time) *event.CachedAggregation {
		return &event.CachedAggregation{
			ID: core.NewID[core.CachedAggregationID](e.ids), OrganizationID: o, ExternalSubscriptionID: sc.ExternalSubscriptionID,
			ChargeID: sc.ChargeID, ChargeFilterID: sc.ChargeFilterID, Timestamp: ts, EventTransactionID: tx, GroupedBy: groups,
			CurrentAggregation: ptr(money.MustParse("0")), CreatedAt: created,
		}
	}
	eu := map[string]*string{"region": ptr("eu"), "team": nil}
	full := mk(org, base, at(time.Hour), "tr_1", eu, now)
	full.CurrentAggregation, full.MaxAggregation = ptr(money.MustParse("12.000000000001")), ptr(money.MustParse("15"))
	full.MaxAggregationWithProration = ptr(money.MustParse("7.5"))
	full.PresentationBreakdowns = []event.GroupedValue{
		{Groups: map[string]*string{"dept": ptr("rd"), "site": nil}, Value: money.MustParse("8")},
		{Groups: map[string]*string{"dept": ptr("ops")}, Value: money.MustParse("4.000000000001")},
	}
	plain := mk(org, base, at(time.Hour), "", nil, at(time.Minute)) // same instant, created later
	plain.CurrentAggregation = nil                                  // no aggregation value at all
	early := mk(org, base, at(-time.Hour), "tr_0", nil, now)
	late := mk(org, base, at(3*time.Hour), "tr_3", nil, now)
	sub := mk(org, base, at(30*time.Minute+500*time.Millisecond), "tr_sub", nil, now) // a sub-second instant
	inFilter := mk(org, filtered, at(time.Hour), "tr_f", nil, now)
	otherSub := mk(org, event.CachedAggregationScope{ExternalSubscriptionID: "sub_2", ChargeID: charge}, at(time.Hour), "", nil, now)
	otherOrg := mk(other, base, at(time.Hour), "", nil, now)
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, x := range []*event.CachedAggregation{full, plain, early, late, sub, inFilter, otherSub, otherOrg} {
			noErr(t, "Create()", r.CachedAggregations.Create(ctx, x))
		}
		wantErr(t, "Create() same ID", r.CachedAggregations.Create(ctx, full), core.ErrConflict)

		got, err := r.CachedAggregations.LatestInPeriod(ctx, org, base, now, at(2*time.Hour), eu, "")
		noErr(t, "LatestInPeriod() grouped", err)
		equal(t, "LatestInPeriod() grouped", got, full)
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, now, at(2*time.Hour), nil, "")
		noErr(t, "LatestInPeriod()", err)
		equal(t, "LatestInPeriod() latest created of an instant", got, plain)
		// Both bounds are truncated to the second.
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, at(30*time.Minute+700*time.Millisecond), at(31*time.Minute), nil, "")
		noErr(t, "LatestInPeriod() truncated from", err)
		equal(t, "LatestInPeriod() truncated from", got, sub)
		_, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, now, at(30*time.Minute+999*time.Millisecond), nil, "")
		wantErr(t, "LatestInPeriod() truncated to", err, core.ErrNotFound)
		_, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, now, at(29*time.Minute), nil, "")
		wantErr(t, "LatestInPeriod() empty period", err, core.ErrNotFound)
		// Excluding a transaction also drops the states without one (plain),
		// and the charge's default scope matches every filter (inFilter).
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, at(-2*time.Hour), at(4*time.Hour), nil, "tr_3")
		noErr(t, "LatestInPeriod() excluding a transaction", err)
		equal(t, "LatestInPeriod() excluding a transaction", got, inFilter)
		_, err = r.CachedAggregations.LatestInPeriod(ctx, org, filtered, at(-2*time.Hour), at(4*time.Hour), nil, "tr_f")
		wantErr(t, "LatestInPeriod() filter excluding its transaction", err, core.ErrNotFound)
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, filtered, now, at(2*time.Hour), nil, "")
		noErr(t, "LatestInPeriod() filter", err)
		equal(t, "LatestInPeriod() filter", got, inFilter)
		_, err = r.CachedAggregations.LatestInPeriod(ctx, org, base, now, at(2*time.Hour), map[string]*string{"region": ptr("eu")}, "")
		wantErr(t, "LatestInPeriod() other groups", err, core.ErrNotFound)

		before, err := r.CachedAggregations.Before(ctx, org, base, at(3*time.Hour))
		noErr(t, "Before()", err)
		equal(t, "Before()", before, []*event.CachedAggregation{plain, inFilter, full, sub, early})
		before, err = r.CachedAggregations.Before(ctx, org, filtered, at(3*time.Hour))
		noErr(t, "Before() filter", err)
		equal(t, "Before() filter", before, []*event.CachedAggregation{inFilter})
		// A default bucket reads only the states without a filter.
		bucketOnly := base
		bucketOnly.DefaultBucket = true
		before, err = r.CachedAggregations.Before(ctx, org, bucketOnly, at(3*time.Hour))
		noErr(t, "Before() default bucket", err)
		equal(t, "Before() default bucket", before, []*event.CachedAggregation{plain, full, sub, early})
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, bucketOnly, at(-2*time.Hour), at(4*time.Hour), nil, "tr_3")
		noErr(t, "LatestInPeriod() default bucket", err)
		equal(t, "LatestInPeriod() default bucket", got, sub)
		before, err = r.CachedAggregations.Before(ctx, other, base, at(3*time.Hour))
		noErr(t, "Before() other organization", err)
		equal(t, "Before() other organization", before, []*event.CachedAggregation{otherOrg})

		again := mk(org, base, full.Timestamp, "", map[string]*string{"team": nil, "region": ptr("eu")}, at(time.Hour))
		created, err := r.CachedAggregations.CreateIfAbsent(ctx, again)
		noErr(t, "CreateIfAbsent() existing", err)
		equal(t, "CreateIfAbsent() existing", created, false)
		fresh := mk(org, filtered, full.Timestamp, "", eu, at(time.Hour))
		created, err = r.CachedAggregations.CreateIfAbsent(ctx, fresh)
		noErr(t, "CreateIfAbsent() new", err)
		equal(t, "CreateIfAbsent() new", created, true)
		got, err = r.CachedAggregations.LatestInPeriod(ctx, org, filtered, now, at(2*time.Hour), eu, "")
		noErr(t, "LatestInPeriod() created if absent", err)
		equal(t, "LatestInPeriod() created if absent", got, fresh)
		// CreateIfAbsent matches the filter exactly: a filtered state does
		// not stand for the default bucket's.
		bucket := mk(org, base, inFilter.Timestamp, "", map[string]*string{"site": ptr("x")}, at(time.Hour))
		filteredBucket := mk(org, filtered, bucket.Timestamp, "", bucket.GroupedBy, at(time.Hour))
		noErr(t, "Create() filtered", r.CachedAggregations.Create(ctx, filteredBucket))
		created, err = r.CachedAggregations.CreateIfAbsent(ctx, bucket)
		noErr(t, "CreateIfAbsent() default bucket", err)
		equal(t, "CreateIfAbsent() default bucket", created, true)
		return nil
	})
}
