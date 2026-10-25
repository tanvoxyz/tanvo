package streaming_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

func capture() (streaming.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	})
	return streaming.NewLogger(slog.New(h)), &buf
}

func TestLoggerEmit(t *testing.T) {
	d := kinesis()
	tests := []struct {
		name    string
		source  string
		outcome streaming.Outcome
		dest    *streaming.Destination
		attrs   []slog.Attr
		want    string
	}{
		{
			name: "names the destination and organization", source: "spec/services/event_destinations/delivery_logger_spec.rb:10",
			outcome: streaming.OutcomeDelivered, dest: d, attrs: []slog.Attr{slog.String("partition_key", "cust_1")},
			want: `level=INFO msg="streaming delivery" outcome=delivered destination_id=` + d.ID.String() +
				" organization_id=" + d.OrganizationID.String() + " partition_key=cust_1",
		},
		{
			name: "omits the destination when there is none", source: "spec/services/event_destinations/delivery_logger_spec.rb:19",
			outcome: streaming.OutcomeSuperseded, attrs: []slog.Attr{slog.String("customer_id", "cust_1")},
			want: `level=INFO msg="streaming delivery" outcome=superseded customer_id=cust_1`,
		},
		{
			name: "quotes a value that would break logfmt", source: "spec/services/event_destinations/delivery_logger_spec.rb:27",
			outcome: streaming.OutcomeDropped, attrs: []slog.Attr{slog.String("message", "User is not authorized to perform: kinesis:PutRecord")},
			want: `level=ERROR msg="streaming delivery" outcome=dropped message="User is not authorized to perform: kinesis:PutRecord"`,
		},
		{
			name: "drops keys with no value", source: "spec/services/event_destinations/delivery_logger_spec.rb:37",
			outcome: streaming.OutcomeDelivered, attrs: []slog.Attr{slog.String("partition_key", "cust_1"), slog.Any("bytes", nil), slog.String("shard", "")},
			want: `level=INFO msg="streaming delivery" outcome=delivered partition_key=cust_1`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, buf := capture()
			l.Emit(context.Background(), tt.outcome, tt.dest, tt.attrs...)
			if got := strings.TrimSpace(buf.String()); got != tt.want {
				t.Errorf("got = %s, want = %s", got, tt.want)
			}
		})
	}
}

// source: spec/services/event_destinations/delivery_logger_spec.rb:45
func TestOutcomeLevels(t *testing.T) {
	want := map[streaming.Outcome]slog.Level{
		streaming.OutcomeDelivered: slog.LevelInfo, streaming.OutcomeThrottled: slog.LevelError,
		streaming.OutcomeDropped: slog.LevelError, streaming.OutcomeSuperseded: slog.LevelInfo,
		streaming.OutcomeSkipped: slog.LevelWarn, streaming.OutcomeFailed: slog.LevelError,
	}
	for o, w := range want {
		if got, ok := o.Level(); !ok || got != w {
			t.Errorf("%s.Level() = %v, %v, want = %v, true", o, got, ok, w)
		}
	}
}

// source: spec/services/event_destinations/delivery_logger_spec.rb:52
func TestLoggerUnknownOutcome(t *testing.T) {
	l, buf := capture()
	defer func() {
		if recover() == nil {
			t.Error("Emit(vanished): got = no panic, want = a panic")
		}
		if buf.Len() != 0 {
			t.Errorf("got = %q logged, want = nothing", buf.String())
		}
	}()
	l.Emit(context.Background(), "vanished", nil)
}
