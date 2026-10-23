package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

func TestWriteThenBill(t *testing.T) {
	failed := errors.New("failed")
	tests := []struct {
		name  string
		write []kafka.Disposition
		bill  []kafka.Disposition
		want  []kafka.Disposition
		// billed is the number of records passed to bill.
		billed int
	}{
		{"all stored and billed", []kafka.Disposition{kafka.Done, kafka.Done}, []kafka.Disposition{kafka.Done, kafka.Done}, []kafka.Disposition{kafka.Done, kafka.Done}, 2},
		{"billing retried", []kafka.Disposition{kafka.Done, kafka.Done}, []kafka.Disposition{kafka.Done, kafka.Retry}, []kafka.Disposition{kafka.Done, kafka.Retry}, 2},
		{"unstored not billed", []kafka.Disposition{kafka.Retry, kafka.Dead, kafka.Done}, []kafka.Disposition{kafka.Retry}, []kafka.Disposition{kafka.Retry, kafka.Dead, kafka.Retry}, 1},
		{"nothing stored", []kafka.Disposition{kafka.Retry}, nil, []kafka.Disposition{kafka.Retry}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			billed := -1
			write := func(_ context.Context, recs []*kgo.Record) []kafka.Result {
				out := make([]kafka.Result, len(recs))
				for i, d := range tt.write {
					out[i].Disposition = d
					if d != kafka.Done {
						out[i].Err = failed
					}
				}
				return out
			}
			bill := func(_ context.Context, recs []*kgo.Record) []kafka.Result {
				billed = len(recs)
				out := make([]kafka.Result, len(recs))
				for i, d := range tt.bill {
					out[i].Disposition = d
				}
				return out
			}
			recs := make([]*kgo.Record, len(tt.write))
			for i := range recs {
				recs[i] = &kgo.Record{}
			}
			got := writeThenBill(write, bill)(t.Context(), recs)
			for i, r := range got {
				if r.Disposition != tt.want[i] {
					t.Errorf("record %d: got = %d, want = %d", i, r.Disposition, tt.want[i])
				}
			}
			if tt.billed == 0 && billed != -1 || tt.billed > 0 && billed != tt.billed {
				t.Errorf("billed records: got = %d, want = %d", billed, tt.billed)
			}
		})
	}
}

type signalsFunc func(context.Context, []ingest.UsageSignal) error

func (f signalsFunc) SignalUsage(ctx context.Context, s []ingest.UsageSignal) error { return f(ctx, s) }

func TestUsageSignals(t *testing.T) {
	tests := []struct {
		name  string
		value string
		err   error
		want  kafka.Disposition
		calls int
	}{
		{"signal", `{"customer_id":"0190f9a8-0000-7000-8000-000000000001","subscription_ids":[]}`, nil, kafka.Done, 1},
		{"failed signal dropped", `{}`, errors.New("failed"), kafka.Done, 1},
		{"undecodable", `{`, nil, kafka.Dead, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			h := usageSignals(signalsFunc(func(context.Context, []ingest.UsageSignal) error {
				calls++
				return tt.err
			}), slog.New(slog.DiscardHandler))
			got := h(t.Context(), []*kgo.Record{{Value: []byte(tt.value)}})
			if got[0].Disposition != tt.want || calls != tt.calls {
				t.Errorf("got = %d with %d calls, want = %d with %d", got[0].Disposition, calls, tt.want, tt.calls)
			}
		})
	}
}
