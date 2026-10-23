package event

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func testEvent() *Event {
	return &Event{
		TransactionID: "tr_1", Code: "api_calls", ExternalSubscriptionID: "sub_1",
		Timestamp: testNow, IngestedAt: testNow,
	}
}

func TestValidate(t *testing.T) {
	type tc struct {
		Name                   string            `yaml:"name"`
		Source                 string            `yaml:"source"`
		TransactionID          *string           `yaml:"transaction_id"`
		TransactionIDLength    int               `yaml:"transaction_id_length"`
		Code                   *string           `yaml:"code"`
		ExternalSubscriptionID *string           `yaml:"external_subscription_id"`
		NoTimestamp            bool              `yaml:"no_timestamp"`
		Properties             map[string]string `yaml:"properties"`
		AttributionLabels      map[string]string `yaml:"attribution_labels"`
		Errors                 []string          `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "event.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			e := testEvent()
			if c.TransactionID != nil {
				e.TransactionID = *c.TransactionID
			}
			if c.TransactionIDLength > 0 {
				e.TransactionID = strings.Repeat("t", c.TransactionIDLength)
			}
			if c.Code != nil {
				e.Code = *c.Code
			}
			if c.ExternalSubscriptionID != nil {
				e.ExternalSubscriptionID = *c.ExternalSubscriptionID
			}
			if c.NoTimestamp {
				e.Timestamp = time.Time{}
			}
			e.Properties, e.AttributionLabels = c.Properties, c.AttributionLabels
			if got := fieldErrors(e.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("errors: got = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestOperation(t *testing.T) {
	e := testEvent()
	if got := e.Operation(); got != OperationTypeAdd {
		t.Errorf("default: got = %s, want = add", got)
	}
	e.Properties = map[string]string{OperationTypeProperty: "remove"}
	if got := e.Operation(); got != OperationTypeRemove {
		t.Errorf("remove: got = %s, want = remove", got)
	}
}

func TestParseTimestamp(t *testing.T) {
	type tc struct {
		Name   string   `yaml:"name"`
		Source string   `yaml:"source"`
		Input  *string  `yaml:"input"`
		Number string   `yaml:"number"`
		Float  *float64 `yaml:"float"`
		Want   string   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "timestamp.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var in any
			switch {
			case c.Input != nil:
				in = *c.Input
			case c.Number != "":
				in = json.Number(c.Number)
			default:
				in = *c.Float
			}
			got, err := ParseTimestamp(in)
			if c.Want == "" {
				if !errors.Is(err, core.ErrInvalidValue) {
					t.Errorf("error: got = %v, want = core.ErrInvalidValue", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error: got = %v, want = nil", err)
			}
			if s := got.Format(time.RFC3339Nano); s != c.Want || got.Location() != time.UTC {
				t.Errorf("ParseTimestamp: got = %s, want = %s", s, c.Want)
			}
		})
	}
	for _, in := range []any{math.NaN(), true, nil} {
		if _, err := ParseTimestamp(in); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("ParseTimestamp(%v): got = %v, want = core.ErrInvalidValue", in, err)
		}
	}
	if got, err := ParseTimestamp(int64(1709296200)); err != nil || !got.Equal(time.Date(2024, 3, 1, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("ParseTimestamp(int64): got = %v %v, want = 2024-03-01T12:30:00Z", got, err)
	}
}

func TestNormalizeProperties(t *testing.T) {
	var decoded map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"s":"x","n":12.50,"i":3,"e":1e3,"b":true,"z":null}`))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	decoded["f"] = 0.1
	decoded["int"] = 7
	got, err := NormalizeProperties(decoded)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"s": "x", "n": "12.50", "i": "3", "e": "1000", "b": "true", "f": "0.1", "int": "7"}
	if !maps.Equal(got, want) {
		t.Errorf("NormalizeProperties: got = %v, want = %v", got, want)
	}
	for _, bad := range []any{map[string]any{"x": 1}, []any{1}} {
		if _, err := NormalizeProperties(map[string]any{"k": bad}); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("nested %v: got = %v, want = core.ErrInvalidValue", bad, err)
		}
	}
	if got, err := NormalizeProperties(nil); got != nil || err != nil {
		t.Errorf("nil: got = %v %v, want = nil nil", got, err)
	}
}

func TestValidateBatch(t *testing.T) {
	if _, err := ValidateBatch(nil); !slices.Equal(fieldErrors(err), []string{"events: value_is_mandatory"}) {
		t.Errorf("empty batch: got = %v, want = [events: value_is_mandatory]", err)
	}
	big := make([]*Event, MaxBatchSize+1)
	for i := range big {
		big[i] = testEvent()
	}
	if _, err := ValidateBatch(big); !slices.Equal(fieldErrors(err), []string{"events: " + CodeTooManyEvents}) {
		t.Errorf("oversized batch: got = %v, want = [events: %s]", err, CodeTooManyEvents)
	}
	bad := testEvent()
	bad.Code = ""
	errs, err := ValidateBatch([]*Event{testEvent(), bad})
	if err != nil || len(errs) != 2 || errs[0] != nil || errs[1] == nil {
		t.Errorf("per-event errors: got = %v %v, want = [nil, error] nil", errs, err)
	}
}

func TestClone(t *testing.T) {
	e := testEvent()
	e.Properties = map[string]string{"a": "1"}
	c := e.Clone()
	c.Properties["a"] = "2"
	if e.Properties["a"] != "1" {
		t.Errorf("Clone shares properties: got = %s, want = 1", e.Properties["a"])
	}
}
