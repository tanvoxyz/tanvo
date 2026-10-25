package streaming

import (
	"context"
	"log/slog"
)

// Outcome is the result of one delivery attempt, as logged.
type Outcome string

// Outcome values.
const (
	// OutcomeDelivered is a record the stream accepted.
	OutcomeDelivered Outcome = "delivered"
	// OutcomeThrottled is a record refused for lack of stream capacity.
	OutcomeThrottled Outcome = "throttled"
	// OutcomeDropped is a record refused for a configuration, identity or
	// network fault.
	OutcomeDropped Outcome = "dropped"
	// OutcomeSuperseded is a delivery not run because another one of the
	// same customer and event type is waiting or running.
	OutcomeSuperseded Outcome = "superseded"
	// OutcomeSkipped is a subscription whose usage could not be computed.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeFailed is a delivery that failed with an unexpected error.
	OutcomeFailed Outcome = "failed"
)

// Level returns the severity outcome o is logged at, and false for an
// undeclared outcome.
//
// source: app/services/event_destinations/delivery_logger.rb:7-14
func (o Outcome) Level() (slog.Level, bool) {
	switch o {
	case OutcomeDelivered, OutcomeSuperseded:
		return slog.LevelInfo, true
	case OutcomeSkipped:
		return slog.LevelWarn, true
	case OutcomeThrottled, OutcomeDropped, OutcomeFailed:
		return slog.LevelError, true
	default:
		return 0, false
	}
}

// LogMessage is the message of every delivery record.
const LogMessage = "streaming delivery"

// Logger writes the delivery records that monitors match on: the message
// [LogMessage], the outcome, the destination and its organization when
// there is one, then the given attributes, without those that have no
// value.
//
// source: app/services/event_destinations/delivery_logger.rb
type Logger struct{ l *slog.Logger }

// NewLogger returns a Logger writing to l, or to slog.Default when l is
// nil.
func NewLogger(l *slog.Logger) Logger {
	if l == nil {
		l = slog.Default()
	}
	return Logger{l}
}

// Emit logs outcome o of a delivery to d (nil when there is none to
// name). An undeclared outcome panics: it is a programming error, and a
// record no monitor matches is worse than none.
func (l Logger) Emit(ctx context.Context, o Outcome, d *Destination, attrs ...slog.Attr) {
	level, ok := o.Level()
	if !ok {
		panic("streaming: undeclared delivery outcome " + string(o))
	}
	out := make([]slog.Attr, 0, len(attrs)+3)
	out = append(out, slog.String("outcome", string(o)))
	if d != nil {
		out = append(out, slog.String("destination_id", d.ID.String()), slog.String("organization_id", d.OrganizationID.String()))
	}
	for _, a := range attrs {
		if !empty(a.Value) {
			out = append(out, a)
		}
	}
	if l.l == nil {
		l.l = slog.Default()
	}
	l.l.LogAttrs(ctx, level, LogMessage, out...)
}

func empty(v slog.Value) bool {
	switch v.Kind() {
	case slog.KindString:
		return v.String() == ""
	case slog.KindAny:
		return v.Any() == nil
	default:
		return false
	}
}
