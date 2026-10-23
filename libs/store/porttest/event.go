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
	t.Run("append and get", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			full := eventFixture(e, org, "tr_full", "1")
			full.Properties = map[string]string{"value": "12.5", "region": "eu", "empty": ""}
			full.PreciseTotalAmount = ptr(money.MustParse("1234.000000000001"))
			full.AttributionLabels = map[string]string{"team": "core"}
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
			stored.AttributionLabels = map[string]string{"team": "core"}
			_, _, err := r.Events.Append(ctx, org, []*event.Event{stored})
			noErr(t, "Append()", err)

			fix := eventFixture(e, org, "tr_1", "10")
			fix.Code = "storage"
			fix.Timestamp = at(-time.Hour)
			fix.IngestedAt, fix.CreatedAt, fix.UpdatedAt = at(time.Hour), at(time.Hour), at(time.Hour)
			noErr(t, "Replace()", r.Events.Replace(ctx, fix))
			want := *fix
			want.ID, want.IngestedAt, want.CreatedAt = stored.ID, stored.IngestedAt, stored.CreatedAt
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
