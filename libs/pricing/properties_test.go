package pricing

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

type defaultCase struct {
	Source string          `json:"source"`
	Name   string          `json:"name"`
	Model  ChargeModel     `json:"model"`
	Want   json.RawMessage `json:"want"`
}

func TestDefaultProperties(t *testing.T) {
	for file, cases := range loadCases[defaultCase](t, "default_properties.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					p, err := DefaultProperties(c.Model)
					if err != nil {
						t.Fatal(err)
					}
					if p.Model() != c.Model {
						t.Errorf("model: got = %s, want = %s", p.Model(), c.Model)
					}
					check(t, c.Want, p)
					// The defaults are exactly the expected keys.
					got := toGeneric(t, p).(map[string]any)
					want := decodeGeneric(t, c.Want).(map[string]any)
					if g, w := slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)); !slices.Equal(g, w) {
						t.Errorf("keys: got = %v, want = %v", g, w)
					}
					// And they decode back to themselves.
					data, err := json.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					back, err := DecodeProperties(c.Model, data)
					if err != nil {
						t.Fatal(err)
					}
					check(t, data, back)
				})
			}
		})
	}
	if _, err := DefaultProperties("custom"); err == nil {
		t.Error("custom: got = nil error, want = an error")
	}
}

type filterCase struct {
	Source     string          `json:"source"`
	Name       string          `json:"name"`
	Chargeable string          `json:"chargeable"`
	Model      ChargeModel     `json:"model"`
	Properties map[string]any  `json:"properties"`
	WantKeys   []string        `json:"want_keys"`
	Want       json.RawMessage `json:"want"`
}

func TestFilterProperties(t *testing.T) {
	for file, cases := range loadCases[filterCase](t, "filter_properties.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					chargeable := UsageCharge
					if c.Chargeable == "fixed_charge" {
						chargeable = FixedCharge
					}
					got := FilterProperties(chargeable, c.Model, c.Properties)
					if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, c.WantKeys) {
						t.Errorf("keys: got = %v, want = %v", keys, c.WantKeys)
					}
					check(t, c.Want, got)
				})
			}
		})
	}
}
