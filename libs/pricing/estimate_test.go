package pricing

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

type estimateCase struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	testStructure
	Units money.Decimal   `json:"units"`
	Want  json.RawMessage `json:"want"`
}

type estimateView struct {
	Units  money.Decimal `json:"units"`
	Amount money.Decimal `json:"amount"`
}

func TestEstimateInstant(t *testing.T) {
	for file, cases := range loadCases[estimateCase](t, "estimate.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					e, err := EstimateInstant(c.structure(t).Properties, c.Units)
					if err != nil {
						t.Fatal(err)
					}
					check(t, c.Want, estimateView(e))
				})
			}
		})
	}
	if _, err := EstimateInstant(GraduatedProperties{}, money.One); err == nil {
		t.Error("graduated: got = nil error, want = an error")
	}
}

func TestPrice(t *testing.T) {
	for file, cases := range loadCases[estimateCase](t, "price.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					got, err := Price(c.structure(t), c.Units)
					if err != nil {
						t.Fatal(err)
					}
					check(t, c.Want, got)
				})
			}
		})
	}
}
