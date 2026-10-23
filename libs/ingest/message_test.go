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
