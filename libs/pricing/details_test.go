package pricing

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

type rangeDetailsCase struct {
	Source     string          `json:"source"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Range      json.RawMessage `json:"range"`
	TotalUnits money.Decimal   `json:"total_units"`
	Adjacent   bool            `json:"adjacent"`
	Want       json.RawMessage `json:"want"`
}

func TestRangeDetails(t *testing.T) {
	for file, cases := range loadCases[rangeDetailsCase](t, "range_details.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					switch c.Kind {
					case "graduated":
						var r Range
						if err := json.Unmarshal(c.Range, &r); err != nil {
							t.Fatal(err)
						}
						check(t, c.Want, graduatedRangeDetails(r, c.TotalUnits, c.Adjacent, money.One))
					case "graduated_percentage":
						var r PercentageRange
						if err := json.Unmarshal(c.Range, &r); err != nil {
							t.Fatal(err)
						}
						check(t, c.Want, graduatedPercentageRangeDetails(r, c.TotalUnits, c.Adjacent))
					default:
						t.Fatalf("unknown kind %q", c.Kind)
					}
				})
			}
		})
	}
}
