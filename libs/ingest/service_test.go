package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 123456000, time.UTC)

type harness struct {
	cat *fakeCatalog
	tx  *memTx
	rec *recorder
	svc *Service
}

func newHarness(t *testing.T, mode Mode) *harness {
	t.Helper()
	h := &harness{cat: &fakeCatalog{}, tx: newMemTx(), rec: &recorder{}}
	svc, err := NewService(Config{
		Catalog:   h.cat,
		Clock:     core.FixedClock(testNow),
		Mode:      mode,
		Postgres:  h.tx,
		Producer:  h.rec,
		InAdvance: h.rec,
		Signals:   h.rec,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	h.svc = svc
	return h
}

// batch builds lago's build_params: count events txn_i with foo=bar_i.
// source: spec/services/events/create_batch_service_spec.rb:24
func batch(count int, edit func(*Input, int)) []Input {
	out := make([]Input, count)
	for i := range out {
		out[i] = Input{
			TransactionID:           fmt.Sprintf("txn_%d", i),
			Code:                    "sum_agg",
			ExternalSubscriptionID:  "sub_12345",
			Timestamp:               json.Number("1704070923.123456"),
			Properties:              map[string]any{"foo": fmt.Sprintf("bar_%d", i)},
			PreciseTotalAmountCents: "123.34",
		}
		if edit != nil {
			edit(&out[i], i)
		}
	}
	return out
}

func hasField(err error, field, code string) bool {
	var ve *core.ValidationError
	return errors.As(err, &ve) && ve.Has(field, code)
}

func TestCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// source: spec/services/events/create_service_spec.rb:39
	t.Run("creates an event", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		out, err := h.svc.Create(ctx, testOrg, Input{
			TransactionID: "tx", Code: "sum_agg", ExternalSubscriptionID: "sub",
			Properties: map[string]any{"foo": "bar"}, Timestamp: json.Number("1704070923.5"),
		}, Metadata{})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		e := out.Event
		if out.Duplicate || e.TransactionID != "tx" || e.Code != "sum_agg" || e.ExternalSubscriptionID != "sub" || e.Properties["foo"] != "bar" || e.PreciseTotalAmount != nil {
			t.Errorf("Create() = %+v", e)
		}
		if want := time.Unix(1704070923, 5e8).UTC(); !e.Timestamp.Equal(want) {
			t.Errorf("timestamp got = %v, want = %v", e.Timestamp, want)
		}
		if e.ID.IsZero() {
			t.Error("event ID got = zero, want = set")
		}
		if got := len(h.tx.writer.events()); got != 1 {
			t.Errorf("stored got = %d, want = 1", got)
		}
	})

	// source: spec/services/events/create_service_spec.rb:59 (the event is not
	// kept when its post-processing cannot be routed: here the in-advance
	// biller fails inside the transaction)
	t.Run("rolls back when routing fails", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		m := h.cat.metric("sum_agg", catalog.AggregationTypeSum, "value")
		s := h.cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
		h.cat.charge(s.PlanID, m, true, "")
		h.rec.err = errors.New("boom")
		_, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "sum_agg", ExternalSubscriptionID: "sub"}, Metadata{})
		if err == nil {
			t.Fatal("Create() error = nil, want = an error")
		}
		if got := len(h.tx.writer.events()); got != 0 {
			t.Errorf("stored got = %d, want = 0", got)
		}
		h.rec.err = nil
		if out, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "sum_agg", ExternalSubscriptionID: "sub"}, Metadata{}); err != nil || out.Duplicate {
			t.Errorf("retry got = %+v, %v, want = accepted", out, err)
		}
	})

	// source: spec/services/events/create_service_spec.rb:100 (Tanvo:
	// acknowledged as a duplicate, domain §24 item 11)
	t.Run("event already exists", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		in := Input{TransactionID: "tx", Code: "sum_agg", ExternalSubscriptionID: "sub"}
		if _, err := h.svc.Create(ctx, testOrg, in, Metadata{}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		out, err := h.svc.Create(ctx, testOrg, in, Metadata{})
		if err != nil || !out.Duplicate {
			t.Errorf("Create() got = %+v, %v, want = a duplicate", out, err)
		}
		if got := len(h.tx.writer.events()); got != 1 {
			t.Errorf("stored got = %d, want = 1", got)
		}
	})

	timestamps := []struct {
		name   string
		source string
		ts     any
		want   time.Time
	}{
		{"timestamp not present", "spec/services/events/create_service_spec.rb:124", nil, testNow.Truncate(time.Microsecond)},
		{"timestamp as string", "spec/services/events/create_service_spec.rb:135", "1704070923.25", time.Unix(1704070923, 25e7).UTC()},
		{"millisecond precision", "spec/services/events/create_service_spec.rb:146", 1693842312.344, time.Date(2023, 9, 4, 15, 45, 12, 344e6, time.UTC)},
		{"repeating float .1", "spec/services/events/create_service_spec.rb:157", "1780586634.1", time.Date(2026, 6, 4, 15, 23, 54, 1e8, time.UTC)},
		{"repeating float .2", "spec/services/events/create_service_spec.rb:157", "1780586634.2", time.Date(2026, 6, 4, 15, 23, 54, 2e8, time.UTC)},
		{"repeating float .3", "spec/services/events/create_service_spec.rb:157", "1780586634.3", time.Date(2026, 6, 4, 15, 23, 54, 3e8, time.UTC)},
		{"RFC 3339", "", "2026-06-04T15:23:54.3Z", time.Date(2026, 6, 4, 15, 23, 54, 3e8, time.UTC)},
	}
	for _, tc := range timestamps {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// source: see tc.source
			h := newHarness(t, ModePostgres)
			out, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "c", ExternalSubscriptionID: "s", Timestamp: tc.ts}, Metadata{})
			if err != nil {
				t.Fatalf("Create() error = %v (%s)", err, tc.source)
			}
			if !out.Event.Timestamp.Equal(tc.want) {
				t.Errorf("timestamp got = %v, want = %v (%s)", out.Event.Timestamp, tc.want, tc.source)
			}
		})
	}

	// source: spec/services/events/create_service_spec.rb:179
	t.Run("timestamp in a wrong format", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		_, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "c", ExternalSubscriptionID: "s", Timestamp: "2026-06-04 15:23:54 UTC"}, Metadata{})
		if !hasField(err, "timestamp", event.CodeInvalidTimestamp) {
			t.Errorf("Create() error got = %v, want = timestamp: %s", err, event.CodeInvalidTimestamp)
		}
	})

	// source: spec/services/events/create_service_spec.rb:205
	t.Run("expression", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		m := h.cat.metric("sum_agg", catalog.AggregationTypeSum, "result")
		m.Expression = ptr("event.properties.left + event.properties.right")
		out, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "sum_agg", ExternalSubscriptionID: "s", Properties: map[string]any{"left": "1", "right": "2"}}, Metadata{})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got := out.Event.Properties["result"]; got != "3" {
			t.Errorf("result got = %q, want = %q", got, "3")
		}
		// source: spec/services/events/create_service_spec.rb:230
		_, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "tx2", Code: "sum_agg", ExternalSubscriptionID: "s", Properties: map[string]any{}}, Metadata{})
		if !hasField(err, "properties", CodeExpressionFailed) {
			t.Errorf("Create() error got = %v, want = properties: %s", err, CodeExpressionFailed)
		}
	})

	// source: spec/services/events/create_service_spec.rb:250
	t.Run("precise_total_amount_cents", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		out, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "c", ExternalSubscriptionID: "s", PreciseTotalAmountCents: "123.45"}, Metadata{})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if got := out.Event.PreciseTotalAmount; got == nil || got.String() != "123.45" {
			t.Errorf("amount got = %v, want = 123.45", got)
		}
		// source: spec/services/events/create_service_spec.rb:260 (Tanvo
		// rejects an invalid amount instead of storing 0)
		_, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "tx2", Code: "c", ExternalSubscriptionID: "s", PreciseTotalAmountCents: "asdfa"}, Metadata{})
		if !hasField(err, "precise_total_amount_cents", core.CodeInvalid) {
			t.Errorf("Create() error got = %v, want = precise_total_amount_cents: %s", err, core.CodeInvalid)
		}
	})

	t.Run("more than 50 integer digits", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		big := "1" + fmt.Sprintf("%050d", 0) // 51 integer digits
		ok := fmt.Sprintf("%050d", 7) + ".123"
		if _, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "a", Code: "c", ExternalSubscriptionID: "s", Properties: map[string]any{"v": json.Number(ok)}}, Metadata{}); err != nil {
			t.Errorf("50 digits error got = %v, want = nil", err)
		}
		_, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "b", Code: "c", ExternalSubscriptionID: "s", Properties: map[string]any{"v": json.Number(big)}}, Metadata{})
		if !hasField(err, "properties.v", core.CodeOutOfRange) {
			t.Errorf("property error got = %v, want = properties.v: %s", err, core.CodeOutOfRange)
		}
		_, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "c", Code: "c", ExternalSubscriptionID: "s", Properties: map[string]any{"v": "-" + big + ".5"}}, Metadata{})
		if !hasField(err, "properties.v", core.CodeOutOfRange) {
			t.Errorf("string property error got = %v, want = properties.v: %s", err, core.CodeOutOfRange)
		}
		_, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "d", Code: "c", ExternalSubscriptionID: "s", PreciseTotalAmountCents: big}, Metadata{})
		if !hasField(err, "precise_total_amount_cents", core.CodeOutOfRange) {
			t.Errorf("amount error got = %v, want = precise_total_amount_cents: %s", err, core.CodeOutOfRange)
		}
	})

	t.Run("external_contract_id alias", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		out, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "a", Code: "c", ExternalContractID: "contract"}, Metadata{})
		if err != nil || out.Event.ExternalSubscriptionID != "contract" {
			t.Errorf("alias got = %v, %v, want = contract", out.Event, err)
		}
		out, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "b", Code: "c", ExternalSubscriptionID: "sub", ExternalContractID: "contract"}, Metadata{})
		if err != nil || out.Event.ExternalSubscriptionID != "sub" {
			t.Errorf("both got = %v, %v, want = sub", out.Event, err)
		}
		_, err = h.svc.Create(ctx, testOrg, Input{TransactionID: "c", Code: "c"}, Metadata{})
		if !hasField(err, "external_subscription_id", core.CodeRequired) {
			t.Errorf("neither error got = %v, want = external_subscription_id: %s", err, core.CodeRequired)
		}
	})
}

func TestCreateBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// source: spec/services/events/create_batch_service_spec.rb:55
	// source: spec/services/events/create_batch_service_spec.rb:63
	t.Run("creates all events", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		out, err := h.svc.CreateBatch(ctx, testOrg, batch(3, nil), Metadata{})
		if err != nil {
			t.Fatalf("CreateBatch() error = %v", err)
		}
		for i, o := range out {
			e := o.Event
			if o.Duplicate || e.TransactionID != fmt.Sprintf("txn_%d", i) || e.Properties["foo"] != fmt.Sprintf("bar_%d", i) ||
				e.PreciseTotalAmount.String() != "123.34" || !e.Timestamp.Equal(time.Date(2024, 1, 1, 1, 2, 3, 123456000, time.UTC)) {
				t.Errorf("event %d got = %+v", i, e)
			}
		}
		if got := len(h.tx.writer.events()); got != 3 {
			t.Errorf("stored got = %d, want = 3", got)
		}
	})

	sizes := []struct {
		name, source string
		count        int
		code         string
	}{
		{"no events", "spec/services/events/create_batch_service_spec.rb:112", 0, core.CodeRequired},
		{"at the limit", "spec/services/events/create_batch_service_spec.rb:120", 100, ""},
		{"too many events", "spec/services/events/create_batch_service_spec.rb:132", 101, event.CodeTooManyEvents},
	}
	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, ModePostgres)
			_, err := h.svc.CreateBatch(ctx, testOrg, batch(tc.count, nil), Metadata{})
			if tc.code == "" {
				if err != nil || len(h.tx.writer.events()) != tc.count {
					t.Errorf("CreateBatch() got = %v, %d stored, want = %d stored (%s)", err, len(h.tx.writer.events()), tc.count, tc.source)
				}
				return
			}
			if !hasField(err, "events", tc.code) {
				t.Errorf("CreateBatch() error got = %v, want = events: %s (%s)", err, tc.code, tc.source)
			}
		})
	}

	dups := []struct {
		name, source string
		count        int
		txs          map[int]string
		existing     string
		want         []int
	}{
		{"consecutive", "spec/services/events/create_batch_service_spec.rb:141", 2, map[int]string{0: "d", 1: "d"}, "", []int{1}},
		{"existing at beginning", "spec/services/events/create_batch_service_spec.rb:152", 5, map[int]string{0: "old"}, "old", []int{0}},
		{"existing in the middle", "spec/services/events/create_batch_service_spec.rb:152", 5, map[int]string{2: "old"}, "old", []int{2}},
		{"existing at end", "spec/services/events/create_batch_service_spec.rb:152", 5, map[int]string{4: "old"}, "old", []int{4}},
		{"non-consecutive", "spec/services/events/create_batch_service_spec.rb:178", 4, map[int]string{0: "d", 2: "d"}, "", []int{2}},
		{"multiple", "spec/services/events/create_batch_service_spec.rb:190", 4, map[int]string{0: "a", 1: "b", 2: "a", 3: "b"}, "", []int{2, 3}},
		{"three", "spec/services/events/create_batch_service_spec.rb:206", 3, map[int]string{0: "d", 1: "d", 2: "d"}, "", []int{1, 2}},
		{"existing second", "spec/services/events/create_batch_service_spec.rb:217", 2, map[int]string{1: "old"}, "old", []int{1}},
		{"existing first", "spec/services/events/create_batch_service_spec.rb:233", 2, map[int]string{0: "old"}, "old", []int{0}},
	}
	for _, mode := range []Mode{ModePostgres, ModeKafka} {
		for _, tc := range dups {
			if mode == ModeKafka && tc.existing != "" {
				continue // stored duplicates are dropped by the ClickHouse writer
			}
			t.Run(string(mode)+"/duplicate "+tc.name, func(t *testing.T) {
				t.Parallel()
				h := newHarness(t, mode)
				if tc.existing != "" {
					if _, err := h.svc.Create(ctx, testOrg, Input{TransactionID: tc.existing, Code: "c", ExternalSubscriptionID: "s"}, Metadata{}); err != nil {
						t.Fatalf("Create() error = %v", err)
					}
				}
				out, err := h.svc.CreateBatch(ctx, testOrg, batch(tc.count, func(in *Input, i int) {
					if tx, ok := tc.txs[i]; ok {
						in.TransactionID = tx
					}
				}), Metadata{})
				if err != nil {
					t.Fatalf("CreateBatch() error = %v (%s)", err, tc.source)
				}
				var got []int
				for i, o := range out {
					if o.Duplicate {
						got = append(got, i)
					}
				}
				if fmt.Sprint(got) != fmt.Sprint(tc.want) {
					t.Errorf("duplicates got = %v, want = %v (%s)", got, tc.want, tc.source)
				}
			})
		}
	}

	// source: spec/services/events/create_batch_service_spec.rb:274
	t.Run("invalid event fails the batch", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		_, err := h.svc.CreateBatch(ctx, testOrg, batch(3, func(in *Input, i int) {
			if i == 1 {
				in.Timestamp = "2026-06-04 15:23:54 UTC"
			}
		}), Metadata{})
		var be *BatchError
		if !errors.As(err, &be) || len(be.Errors) != 1 || !hasField(be.Errors[1], "timestamp", event.CodeInvalidTimestamp) {
			t.Fatalf("CreateBatch() error got = %v, want = [1] timestamp: %s", err, event.CodeInvalidTimestamp)
		}
		if !errors.Is(err, core.ErrInvalidValue) {
			t.Error("errors.Is(err, ErrInvalidValue) got = false, want = true")
		}
		if got := len(h.tx.writer.events()); got != 0 {
			t.Errorf("stored got = %d, want = 0", got)
		}
	})

	// source: spec/services/events/create_batch_service_spec.rb:283
	t.Run("expression", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		m := h.cat.metric("sum_agg", catalog.AggregationTypeSum, "result")
		m.Expression = ptr("concat(event.properties.foo, '-bar')")
		out, err := h.svc.CreateBatch(ctx, testOrg, batch(3, nil), Metadata{})
		if err != nil {
			t.Fatalf("CreateBatch() error = %v", err)
		}
		for i, o := range out {
			if got, want := o.Event.Properties["result"], fmt.Sprintf("bar_%d-bar", i); got != want {
				t.Errorf("result %d got = %q, want = %q", i, got, want)
			}
		}
		// source: spec/services/events/create_batch_service_spec.rb:297
		_, err = h.svc.CreateBatch(ctx, testOrg, batch(1, func(in *Input, _ int) { in.Properties = map[string]any{} }), Metadata{})
		var be *BatchError
		if !errors.As(err, &be) || !hasField(be.Errors[0], "properties", CodeExpressionFailed) {
			t.Errorf("CreateBatch() error got = %v, want = [0] properties: %s", err, CodeExpressionFailed)
		}
	})

	// source: spec/services/events/create_batch_service_spec.rb:338
	// source: spec/services/events/kafka_producer_service_spec.rb:16
	// source: spec/services/events/create_batch_service_spec.rb:381
	t.Run("kafka message format", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModeKafka)
		md := Metadata{UserAgent: "ua", IPAddress: "127.0.0.1"}
		if _, err := h.svc.CreateBatch(ctx, testOrg, batch(2, nil), md); err != nil {
			t.Fatalf("CreateBatch() error = %v", err)
		}
		if got := len(h.tx.writer.events()); got != 0 {
			t.Errorf("stored in postgres got = %d, want = 0", got)
		}
		if len(h.rec.raw) != 2 {
			t.Fatalf("messages got = %d, want = 2", len(h.rec.raw))
		}
		for i, m := range h.rec.raw {
			b, _ := json.Marshal(m)
			var payload map[string]any
			_ = json.Unmarshal(b, &payload)
			want := map[string]any{
				"organization_id":            testOrg.String(),
				"external_subscription_id":   "sub_12345",
				"transaction_id":             fmt.Sprintf("txn_%d", i),
				"code":                       "sum_agg",
				"precise_total_amount_cents": "123.34",
				"timestamp":                  "2024-01-01T01:02:03.123456Z",
				"ingested_at":                "2026-06-01T12:00:00.123456Z",
				"source":                     SourceAPI,
				"expression_evaluated":       true,
			}
			for k, v := range want {
				if fmt.Sprint(payload[k]) != fmt.Sprint(v) {
					t.Errorf("message %d %s got = %v, want = %v", i, k, payload[k], v)
				}
			}
			if got := string(m.Key()); got != testOrg.String()+"/"+m.TransactionID {
				t.Errorf("key got = %q, want = org/tx", got)
			}
			if m.Metadata == nil || *m.Metadata != md {
				t.Errorf("metadata got = %v, want = %v", m.Metadata, md)
			}
		}
	})

	t.Run("mode per organization", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, ModePostgres)
		h.svc.cfg.ModeFor = func(core.OrganizationID) Mode { return ModeKafka }
		if _, err := h.svc.CreateBatch(ctx, testOrg, batch(1, nil), Metadata{}); err != nil {
			t.Fatalf("CreateBatch() error = %v", err)
		}
		if len(h.rec.raw) != 1 || len(h.tx.writer.events()) != 0 {
			t.Errorf("got = %d produced, %d stored, want = 1, 0", len(h.rec.raw), len(h.tx.writer.events()))
		}
	})
}

// source: spec/services/events/post_process_service_spec.rb
func TestCreatePostgresRouting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	started := testNow.Add(-72 * time.Hour)
	cases := []struct {
		name, source string
		recurring    bool
		status       subscription.Status
		at           time.Time
		inAdvance    bool
		wantBilled   bool
		wantSignal   bool
	}{
		{"in-advance charge", "spec/services/events/post_process_service_spec.rb:118", false, subscription.StatusActive, testNow.Add(-time.Second), true, true, true},
		{"signals usage", "spec/services/events/post_process_service_spec.rb:38", false, subscription.StatusActive, testNow.Add(-time.Second), false, false, true},
		{"backdated recurring", "spec/services/events/post_process_service_spec.rb:69", true, subscription.StatusActive, started.Add(-time.Hour), true, true, true},
		{"backdated non-recurring", "spec/services/events/post_process_service_spec.rb:89", false, subscription.StatusActive, started.Add(-time.Hour), true, false, false},
		{"incomplete subscription", "spec/services/events/post_process_service_spec.rb:100", false, subscription.StatusIncomplete, testNow.Add(-time.Second), true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, ModePostgres)
			m := h.cat.metric("bm", catalog.AggregationTypeCount, "")
			m.Recurring = tc.recurring
			s := h.cat.subscription("sub", tc.status, started)
			h.cat.charge(s.PlanID, m, tc.inAdvance, "")
			_, err := h.svc.Create(ctx, testOrg, Input{TransactionID: "tx", Code: "bm", ExternalSubscriptionID: "sub", Timestamp: tc.at.Format(time.RFC3339Nano)}, Metadata{})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if got := len(h.rec.inAdvance) == 1; got != tc.wantBilled {
				t.Errorf("billed got = %v, want = %v (%s)", got, tc.wantBilled, tc.source)
			}
			if tc.wantBilled && h.rec.inAdvance[0].SubscriptionID != s.ID {
				t.Errorf("billed subscription got = %v, want = %v", h.rec.inAdvance[0].SubscriptionID, s.ID)
			}
			if got := len(h.rec.signals) == 1; got != tc.wantSignal {
				t.Errorf("signalled got = %v, want = %v (%s)", got, tc.wantSignal, tc.source)
			}
			if tc.wantSignal {
				sig := h.rec.signals[0]
				if sig.CustomerID != s.CustomerID || len(sig.SubscriptionIDs) != 1 || sig.SubscriptionIDs[0] != s.ID || !sig.Watermark.Equal(testNow.Truncate(time.Microsecond)) {
					t.Errorf("signal got = %+v", sig)
				}
			}
		})
	}
}

func TestCreatePostgresDuplicatesNotRouted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t, ModePostgres)
	m := h.cat.metric("bm", catalog.AggregationTypeCount, "")
	s := h.cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
	h.cat.charge(s.PlanID, m, true, "")
	in := Input{TransactionID: "tx", Code: "bm", ExternalSubscriptionID: "sub"}
	for range 2 {
		if _, err := h.svc.Create(ctx, testOrg, in, Metadata{}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	if len(h.rec.inAdvance) != 1 || len(h.rec.signals) != 1 {
		t.Errorf("routed got = %d billed, %d signals, want = 1, 1", len(h.rec.inAdvance), len(h.rec.signals))
	}
}

func TestInputJSON(t *testing.T) {
	t.Parallel()
	var in Input
	if err := json.Unmarshal([]byte(`{"transaction_id":"t","code":"c","external_subscription_id":"s","timestamp":1704070923.123456,"properties":{"n":123456789012345678901234567890.5,"b":true,"x":null},"precise_total_amount_cents":1.10}`), &in); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	e, err := normalize(testOrg, in, testNow)
	if err != nil {
		t.Fatalf("normalize() error = %v", err)
	}
	if got := e.Properties["n"]; got != "123456789012345678901234567890.5" {
		t.Errorf("n got = %q, want = exact", got)
	}
	if got := e.Properties["b"]; got != "true" {
		t.Errorf("b got = %q, want = true", got)
	}
	if _, ok := e.Properties["x"]; ok {
		t.Error("x got = present, want = dropped")
	}
	if want := money.MustParse("1.10"); e.PreciseTotalAmount == nil || e.PreciseTotalAmount.Cmp(want) != 0 {
		t.Errorf("amount got = %v, want = 1.10", e.PreciseTotalAmount)
	}
	if want := time.Date(2024, 1, 1, 1, 2, 3, 123456000, time.UTC); !e.Timestamp.Equal(want) {
		t.Errorf("timestamp got = %v, want = %v", e.Timestamp, want)
	}
	_, err = normalize(testOrg, Input{TransactionID: "t", Code: "c", ExternalSubscriptionID: "s", Properties: map[string]any{"o": map[string]any{}}}, testNow)
	if !hasField(err, "properties", event.CodeInvalidProperties) {
		t.Errorf("nested error got = %v, want = properties: %s", err, event.CodeInvalidProperties)
	}
}
