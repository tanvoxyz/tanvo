package kafka

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

func TestReplayRecord(t *testing.T) {
	t.Parallel()
	topics := DefaultTopics()
	customer := core.NewID[core.CustomerID](ids)
	signal, _ := json.Marshal(ingest.UsageSignal{OrganizationID: org, CustomerID: customer})
	dl := func(topic, event string) ingest.DeadLetterRecord {
		return ingest.DeadLetterRecord{
			DeadLetter:     ingest.DeadLetter{Event: json.RawMessage(event), Topic: topic},
			OrganizationID: org.String(), ExternalSubscriptionID: "sub", TransactionID: "tx",
		}
	}
	cases := []struct {
		name      string
		d         ingest.DeadLetterRecord
		wantKey   string
		wantValue string
		wantErr   bool
	}{
		{"raw keyed by deduplication key", dl(topics.Raw, `{"a":1}`), string(ingest.PartitionKey(org, "sub", "tx")), `{"a":1}`, false},
		{"enriched keyed by subscription", dl(topics.Enriched, `{"a":1}`), string(ingest.SubscriptionPartitionKey(org, "sub")), `{"a":1}`, false},
		{"signal keyed by customer", dl(topics.Signals, string(signal)), org.String() + "/" + customer.String(), string(signal), false},
		{"message that is not JSON", dl(topics.Raw, `"garbage"`), string(ingest.PartitionKey(org, "sub", "tx")), `garbage`, false},
		{"another topic", dl("elsewhere", `{}`), "", "", true},
		{"no organization", func() ingest.DeadLetterRecord { d := dl(topics.Raw, `{}`); d.OrganizationID = ""; return d }(), "", "", true},
		{"empty message", dl(topics.Raw, ``), "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := topics.replayRecord(tc.d)
			if tc.wantErr {
				if !errors.Is(err, errNotReplayable) {
					t.Errorf("error got = %v, want = %v", err, errNotReplayable)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Topic != tc.d.Topic || string(r.Key) != tc.wantKey || string(r.Value) != tc.wantValue {
				t.Errorf("record got = %s %q %q, want = %s %q %q", r.Topic, r.Key, r.Value, tc.d.Topic, tc.wantKey, tc.wantValue)
			}
		})
	}
}
