package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxIntegerDigits bounds the integer digits of a numeric property and of
// the precise amount: ClickHouse reads aggregated values as
// Decimal(76, 26), which has 50 integer digits, and reads a wider value as
// zero (architecture §7.4).
const MaxIntegerDigits = 50

// Field error codes of ingestion, beyond those of core and core/event.
const (
	// CodeExpressionFailed: the billable metric's expression could not be
	// evaluated for the event (a property is missing, a division by zero,
	// …).
	CodeExpressionFailed = "expression_evaluation_failed"
)

// Input is one event as the API receives it (POST /v1/events and each
// item of /v1/events:batch). Decode it with encoding/json: numbers are
// kept as [json.Number], so that timestamps and amounts keep their
// precision.
type Input struct {
	// TransactionID is the client's idempotency key.
	TransactionID string `json:"transaction_id"`
	// Code is the billable metric code.
	Code string `json:"code"`
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string `json:"external_subscription_id"`
	// ExternalContractID is an alias of ExternalSubscriptionID, which wins
	// when both are set.
	ExternalContractID string `json:"external_contract_id"`
	// Timestamp is an RFC 3339 string or Unix seconds with an optional
	// fraction (a string or a number); nil means the ingestion time.
	Timestamp any `json:"timestamp"`
	// Properties are the event properties: strings, numbers, booleans or
	// nulls.
	Properties map[string]any `json:"properties"`
	// PreciseTotalAmountCents is the amount of a dynamic charge's event, a
	// decimal string or number; nil when absent.
	PreciseTotalAmountCents any `json:"precise_total_amount_cents"`
}

// UnmarshalJSON decodes the input keeping numbers as [json.Number].
func (in *Input) UnmarshalJSON(b []byte) error {
	type plain Input
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("ingest: event: %w", err)
	}
	*in = Input(p)
	return nil
}

// Metadata describes the request an event arrived with (lago-api's
// events.metadata).
type Metadata struct {
	// UserAgent is the client's User-Agent.
	UserAgent string `json:"user_agent,omitempty"`
	// IPAddress is the client's address.
	IPAddress string `json:"ip_address,omitempty"`
}

// numericText is the form of a property that numeric aggregations read
// (metering.Properties).
var numericText = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// integerDigits returns the number of integer digits of a decimal, ignoring
// leading zeros.
func integerDigits(d money.Decimal) int {
	s := d.Abs().String()
	s, _, _ = strings.Cut(s, ".")
	s = strings.TrimLeft(s, "0")
	return len(s)
}

// checkProperties records an out-of-range error for every numeric property
// with more than [MaxIntegerDigits] integer digits.
func checkProperties(v *core.Validator, props map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(props)) {
		s := props[k]
		if !numericText.MatchString(s) {
			continue
		}
		intPart, _, _ := strings.Cut(strings.TrimPrefix(s, "-"), ".")
		v.Check(len(strings.TrimLeft(intPart, "0")) <= MaxIntegerDigits, "properties."+k, core.CodeOutOfRange)
	}
}

// parseAmount parses a precise amount: a decimal string or number.
func parseAmount(v any) (*money.Decimal, bool) {
	var s string
	switch x := v.(type) {
	case nil:
		return nil, true
	case string:
		s = strings.TrimSpace(x)
		if s == "" {
			return nil, true
		}
	case json.Number:
		s = x.String()
	case float64:
		d, err := money.NewFromFloat(x)
		if err != nil {
			return nil, false
		}
		return &d, true
	case int:
		d := money.NewFromInt(int64(x))
		return &d, true
	case int64:
		d := money.NewFromInt(x)
		return &d, true
	default:
		return nil, false
	}
	d, err := money.Parse(s)
	if err != nil {
		return nil, false
	}
	return &d, true
}

// normalize turns an input into an event of org received at now, or
// returns the input's field errors as a *core.ValidationError. The event
// has no ID yet.
func normalize(org core.OrganizationID, in Input, now time.Time) (*event.Event, error) {
	var v core.Validator
	e := &event.Event{
		OrganizationID:         org,
		TransactionID:          in.TransactionID,
		Code:                   in.Code,
		ExternalSubscriptionID: in.ExternalSubscriptionID,
		Timestamp:              now.UTC().Truncate(time.Microsecond),
		IngestedAt:             now.UTC().Truncate(time.Microsecond),
	}
	if strings.TrimSpace(e.ExternalSubscriptionID) == "" {
		e.ExternalSubscriptionID = in.ExternalContractID
	}
	if in.Timestamp != nil {
		t, err := event.ParseTimestamp(in.Timestamp)
		if v.Check(err == nil, "timestamp", event.CodeInvalidTimestamp) {
			e.Timestamp = t
		}
	}
	props, err := event.NormalizeProperties(in.Properties)
	if v.Check(err == nil, "properties", event.CodeInvalidProperties) {
		e.Properties = props
		checkProperties(&v, props)
	}
	if amount, ok := parseAmount(in.PreciseTotalAmountCents); v.Check(ok, "precise_total_amount_cents", core.CodeInvalid) && amount != nil {
		v.Check(integerDigits(*amount) <= MaxIntegerDigits, "precise_total_amount_cents", core.CodeOutOfRange)
		e.PreciseTotalAmount = amount
	}
	v.Merge("", e.Validate())
	if err := v.Err(); err != nil {
		return nil, err
	}
	if e.Properties == nil {
		e.Properties = map[string]string{}
	}
	return e, nil
}
