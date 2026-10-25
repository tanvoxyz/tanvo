package ingest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestDecodeRaw(t *testing.T) {
	t.Parallel()
	in := testEvent("bm", map[string]string{"v": "1"})
	in.Metadata = Metadata{UserAgent: "ua"}
	valid := rawMessage(in, SourceAPI)
	b, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	m, e, err := DecodeRaw(b)
	if err != nil {
		t.Fatalf("DecodeRaw() error = %v", err)
	}
	if !m.ExpressionEvaluated || m.Metadata.UserAgent != "ua" || e.Metadata != in.Metadata || e.Properties["v"] != "1" || !e.Timestamp.Equal(valid.Timestamp) || e.OrganizationID != testOrg {
		t.Errorf("DecodeRaw() got = %+v, %+v", m, e)
	}

	cases := []struct {
		name, body string
	}{
		{"not JSON", `nope`},
		{"no organization", `{"transaction_id":"t","code":"c","external_subscription_id":"s","timestamp":"2026-01-01T00:00:00Z"}`},
		{"no code", `{"organization_id":"` + testOrg.String() + `","transaction_id":"t","external_subscription_id":"s","timestamp":"2026-01-01T00:00:00Z"}`},
		{"no timestamp", `{"organization_id":"` + testOrg.String() + `","transaction_id":"t","code":"c","external_subscription_id":"s"}`},
		{"too many digits", `{"organization_id":"` + testOrg.String() + `","transaction_id":"t","code":"c","external_subscription_id":"s","timestamp":"2026-01-01T00:00:00Z","properties":{"v":"1` + strings.Repeat("0", 50) + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := DecodeRaw([]byte(tc.body)); err == nil {
				t.Errorf("DecodeRaw() error = nil, want = an error")
			}
		})
	}
	_, _, err = DecodeRaw([]byte(cases[2].body))
	if !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("DecodeRaw() error got = %v, want = ErrInvalidValue", err)
	}
}

// source: events-processor/processors/events_processor/event_producer_service_test.go:81
func TestMarshalDeadLetter(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, ev := range []string{`{"a":1}`, `not json`} {
		b, err := MarshalDeadLetter(DeadLetter{Event: json.RawMessage(ev), Topic: "events.raw", Partition: 2, Offset: 7, ErrorCode: ErrorDecode, ErrorMessage: "bad", FailedAt: at})
		if err != nil {
			t.Fatalf("MarshalDeadLetter() error = %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if got["error_code"] != ErrorDecode || got["error_message"] != "bad" || got["failed_at"] != "2026-01-01T00:00:00Z" || got["offset"] != 7.0 || got["event"] == nil {
			t.Errorf("dead letter got = %v", got)
		}
	}
}

func TestPartitionKeys(t *testing.T) {
	t.Parallel()
	org := testOrg.String()
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"raw by deduplication key", (&RawMessage{OrganizationID: testOrg, TransactionID: "tr_1", ExternalSubscriptionID: "sub_1"}).Key(), org + "/sub_1/tr_1"},
		{"raw of another subscription", (&RawMessage{OrganizationID: testOrg, TransactionID: "tr_1", ExternalSubscriptionID: "sub_2"}).Key(), org + "/sub_2/tr_1"},
		{"raw without a subscription", (&RawMessage{OrganizationID: testOrg, TransactionID: "tr_1"}).Key(), org + "//tr_1"},
		{"enriched by subscription", (&Enriched{OrganizationID: testOrg, TransactionID: "tr_1", ExternalSubscriptionID: "sub_1"}).Key(), org + "/sub_1"},
		{"enriched of another transaction", (&Enriched{OrganizationID: testOrg, TransactionID: "tr_2", ExternalSubscriptionID: "sub_1"}).Key(), org + "/sub_1"},
		{"enriched resend with another subscription", (&Enriched{OrganizationID: testOrg, TransactionID: "tr_1", ExternalSubscriptionID: "sub_2"}).Key(), org + "/sub_2"},
		{"enriched of an unmatched subscription", (&Enriched{OrganizationID: testOrg, TransactionID: "tr_3", ExternalSubscriptionID: "sub_x"}).Key(), org + "/sub_x"},
		{"enriched without a subscription", (&Enriched{OrganizationID: testOrg, TransactionID: "tr_4"}).Key(), org + "/"},
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("%s: key got = %q, want = %q", tt.name, tt.got, tt.want)
		}
	}
}

// TestDecodeConnectorRaw decodes the messages the connectors of
// deploy/connectors map from events of the API's format (lago
// connectors/sqs.yml tests).
func TestDecodeConnectorRaw(t *testing.T) {
	t.Parallel()
	const org = "0199a000-0000-7000-8000-0000000000aa"
	tests := []struct {
		name    string
		msg     string
		ts      time.Time
		props   map[string]string
		precise string // "" for none
		wantErr bool
	}{
		{
			name: "all fields",
			msg: `{"organization_id":"` + org + `","external_subscription_id":"123","transaction_id":"456","code":"789","timestamp":1609459200,
				"properties":{"key1":"value1","key2":2.50,"key3":true,"key4":null},"ingested_at":1609459300,"source":"sqs_connector","precise_total_amount_cents":100}`,
			ts: time.Unix(1609459200, 0).UTC(), props: map[string]string{"key1": "value1", "key2": "2.50", "key3": "true"}, precise: "100",
		},
		{
			name: "without precise amount",
			msg: `{"organization_id":"` + org + `","external_subscription_id":"123","transaction_id":"456","code":"789","timestamp":"1609459200.5",
				"properties":{"key1":"value1"},"ingested_at":1609459300,"precise_total_amount_cents":"0"}`,
			ts: time.Unix(1609459200, 500_000_000).UTC(), props: map[string]string{"key1": "value1"}, precise: "0",
		},
		{
			name: "without timestamp",
			msg:  `{"organization_id":"` + org + `","external_subscription_id":"123","transaction_id":"456","code":"789","ingested_at":1609459300}`,
			ts:   time.Unix(1609459300, 0).UTC(), props: map[string]string{},
		},
		{
			name:    "nested property",
			msg:     `{"organization_id":"` + org + `","transaction_id":"456","code":"789","properties":{"k":{"a":1}},"ingested_at":1609459300}`,
			wantErr: true,
		},
		{
			name:    "bad timestamp",
			msg:     `{"organization_id":"` + org + `","transaction_id":"456","code":"789","timestamp":"soon","ingested_at":1609459300}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, e, err := DecodeRaw([]byte(tt.msg))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DecodeRaw() error got = nil, want = an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeRaw() error got = %v, want = nil", err)
			}
			if !e.Timestamp.Equal(tt.ts) || len(e.Properties) != len(tt.props) {
				t.Errorf("event got = %v %v, want = %v %v", e.Timestamp, e.Properties, tt.ts, tt.props)
			}
			for k, v := range tt.props {
				if e.Properties[k] != v {
					t.Errorf("properties[%s] got = %q, want = %q", k, e.Properties[k], v)
				}
			}
			switch got := m.PreciseTotalAmountCents; {
			case tt.precise == "" && got != nil, tt.precise != "" && (got == nil || got.String() != tt.precise):
				t.Errorf("precise amount got = %v, want = %q", got, tt.precise)
			}
			if !e.IngestedAt.Equal(time.Unix(1609459300, 0)) {
				t.Errorf("ingested_at got = %v, want = %v", e.IngestedAt, time.Unix(1609459300, 0).UTC())
			}
		})
	}
}

// TestDeadLetterRecord reads the keys of dead letters as lago's
// events_dead_letter_mv does: the timestamp falls back to the ingestion
// time, and that to the failure's.
func TestDeadLetterRecord(t *testing.T) {
	t.Parallel()
	failed := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name               string
		event              string
		org, sub, code, tx string
		ts, ingested       time.Time
	}{
		{
			name:  "raw message",
			event: `{"organization_id":"o","external_subscription_id":"s","code":"c","transaction_id":"t","timestamp":"2026-03-05T10:00:00Z","ingested_at":"2026-03-06T11:00:00Z"}`,
			org:   "o", sub: "s", code: "c", tx: "t",
			ts: time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC), ingested: time.Date(2026, 3, 6, 11, 0, 0, 0, time.UTC),
		},
		{
			name:  "connector message with unix times",
			event: `{"organization_id":"o","transaction_id":"t","timestamp":1772704800,"ingested_at":1772791200.5}`,
			org:   "o", tx: "t",
			ts: time.Unix(1772704800, 0).UTC(), ingested: time.Unix(1772791200, 500_000_000).UTC(),
		},
		{
			name:  "bad timestamp",
			event: `{"transaction_id":"t","timestamp":"soon","ingested_at":"2026-03-06T11:00:00Z"}`,
			tx:    "t", ts: time.Date(2026, 3, 6, 11, 0, 0, 0, time.UTC), ingested: time.Date(2026, 3, 6, 11, 0, 0, 0, time.UTC),
		},
		{name: "not an object", event: `"garbage"`, ts: failed, ingested: failed},
		{name: "keys of another type", event: `{"organization_id":7,"code":["x"]}`, ts: failed, ingested: failed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := DeadLetter{Event: json.RawMessage(tt.event), ErrorCode: ErrorDecode, FailedAt: failed}.Record()
			if got, want := strings.Join([]string{r.OrganizationID, r.ExternalSubscriptionID, r.Code, r.TransactionID}, "|"), strings.Join([]string{tt.org, tt.sub, tt.code, tt.tx}, "|"); got != want {
				t.Errorf("keys got = %s, want = %s", got, want)
			}
			if !r.Timestamp.Equal(tt.ts) || !r.IngestedAt.Equal(tt.ingested) {
				t.Errorf("times got = %v, %v, want = %v, %v", r.Timestamp, r.IngestedAt, tt.ts, tt.ingested)
			}
			if r.ErrorCode != ErrorDecode || string(r.Event) != tt.event {
				t.Errorf("dead letter got = %+v, want = the original", r.DeadLetter)
			}
		})
	}
}
