package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// runWatermark checks [metering.IngestionWatermarks] on a store that
// implements it: the watermark covers the events the query selects and
// none of the others. A store may stamp its own write instant on top of
// the events' IngestedAt, so the checks are bounds: the events outside
// the query are ingested far in the future.
func runWatermark(t *testing.T, newStore NewStore) {
	t.Helper()
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	ingested := func(e metering.Event, at time.Time) metering.Event {
		e.IngestedAt = at
		return e
	}
	other := func(e metering.Event, sub, code string) metering.Event {
		e.SubscriptionID, e.Code = sub, code
		return e
	}
	eu := day(2025, time.January, 10).Add(time.Second)
	us := day(2098, time.January, 1)
	history := day(2099, time.January, 1)
	outside := day(2100, time.January, 1)
	events := []metering.Event{
		ingested(specEvent("eu", day(2025, time.January, 10), "region", "eu"), eu),
		ingested(specEvent("us", day(2025, time.January, 12), "region", "us"), us),
		ingested(specEvent("history", day(2024, time.December, 15), "region", "us"), history),
		ingested(other(specEvent("code", day(2025, time.January, 15)), DefaultSubscriptionID, "other"), outside),
		ingested(other(specEvent("sub", day(2025, time.January, 15)), "sub_2", DefaultCode), outside),
		ingested(specEvent("later", day(2025, time.February, 2)), outside),
	}
	s, ok := newStore(t, events).(metering.IngestionWatermarks)
	if !ok {
		t.Skip("the store reports no ingestion watermark")
	}
	period := metering.Query{
		OrganizationID: DefaultOrganizationID, SubscriptionID: DefaultSubscriptionID, Code: DefaultCode,
		From: day(2025, time.January, 1), To: day(2025, time.January, 31).Add(24*time.Hour - time.Microsecond),
	}
	withHistory := period
	withHistory.IncludeHistory = true
	europe := period
	europe.Filter = metering.Filter{Matching: map[string][]string{"region": {"eu"}}}
	none := period
	none.Code = "none"
	for _, tc := range []struct {
		name     string
		q        metering.Query
		min, max time.Time
		zero     bool
	}{
		{name: "period", q: period, min: us, max: history},
		{name: "history", q: withHistory, min: history, max: outside},
		{name: "filter", q: europe, min: eu, max: us},
		{name: "no event", q: none, zero: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.LastIngestedAt(context.Background(), tc.q)
			switch {
			case err != nil:
				t.Fatalf("LastIngestedAt() error = %v", err)
			case tc.zero && !got.IsZero():
				t.Errorf("got = %s, want = zero", got)
			case !tc.zero && (got.Before(tc.min) || !got.Before(tc.max)):
				t.Errorf("got = %s, want = in [%s, %s)", got, tc.min, tc.max)
			}
		})
	}
}
