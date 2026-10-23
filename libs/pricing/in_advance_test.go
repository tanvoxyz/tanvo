package pricing

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

type testInAdvanceEvent struct {
	Persisted               bool            `json:"persisted"`
	Units                   money.Decimal   `json:"units"`
	PreciseTotalAmountCents money.Decimal   `json:"precise_total_amount_cents"`
	Value                   money.Decimal   `json:"value"`
	ValuesWithoutEvent      []money.Decimal `json:"values_without_event"`
}

type inAdvanceCase struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	testStructure
	Aggregation testAggregation    `json:"aggregation"`
	Event       testInAdvanceEvent `json:"event"`
	Want        json.RawMessage    `json:"want"`
	WantError   string             `json:"want_error"`
}

// inAdvanceView is the JSON form of an [InAdvanceResult].
type inAdvanceView struct {
	Units              money.Decimal `json:"units"`
	Count              int64         `json:"count"`
	AmountCents        money.Decimal `json:"amount_cents"`
	PreciseAmountCents money.Decimal `json:"precise_amount_cents"`
	UnitAmount         money.Decimal `json:"unit_amount"`
	Details            Details       `json:"details"`
}

func TestApplyInAdvance(t *testing.T) {
	for file, cases := range loadCases[inAdvanceCase](t, "in_advance.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					ev := InAdvanceEvent(c.Event)
					r, err := ApplyInAdvance(c.structure(t), c.Aggregation.result(), ev)
					wantSentinel(t, err, c.WantError)
					if err != nil {
						return
					}
					check(t, c.Want, inAdvanceView(r))
				})
			}
		})
	}
}

type inAdvanceDetailsCase struct {
	Source         string          `json:"source"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	WithDetails    json.RawMessage `json:"with_details"`
	WithoutDetails json.RawMessage `json:"without_details"`
	Want           json.RawMessage `json:"want"`
}

func decodeDetails(t *testing.T, kind string, data json.RawMessage) Details {
	t.Helper()
	var d Details
	switch kind {
	case "none":
		return nil
	case "percentage":
		var p PercentageDetails
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		d = p
	case "graduated_percentage":
		var g GraduatedPercentageDetails
		if err := json.Unmarshal(data, &g); err != nil {
			t.Fatal(err)
		}
		d = g
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	return d
}

func TestInAdvanceDetails(t *testing.T) {
	for file, cases := range loadCases[inAdvanceDetailsCase](t, "in_advance_details.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					got := inAdvanceDetails(decodeDetails(t, c.Kind, c.WithDetails), decodeDetails(t, c.Kind, c.WithoutDetails))
					if c.Kind == "none" {
						if got != nil {
							t.Errorf("details: got = %v, want = nil", got)
						}
						return
					}
					check(t, c.Want, got)
				})
			}
		})
	}
}
