package pricing

import (
	"encoding/json"
	"slices"
	"testing"
)

type validateCase struct {
	Source            string          `json:"source"`
	Name              string          `json:"name"`
	Model             ChargeModel     `json:"model"`
	Properties        json.RawMessage `json:"properties"`
	LatestAggregation bool            `json:"latest_aggregation"`
	ProductCatalog    bool            `json:"product_catalog"`
	Valid             bool            `json:"valid"`
	// Includes are errors the result must contain.
	Includes []FieldError `json:"includes"`
	// Fields, when set, are exactly the distinct fields in error, in order.
	Fields []string `json:"fields"`
}

func TestValidate(t *testing.T) {
	for file, cases := range loadCases[validateCase](t, "validate_*.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					opts := ValidateOptions{LatestAggregation: c.LatestAggregation, ProductCatalog: c.ProductCatalog}
					errs, err := ValidateJSON(c.Model, c.Properties, opts)
					if err != nil {
						t.Fatal(err)
					}
					if c.Valid {
						if len(errs) != 0 {
							t.Errorf("errors: got = %v, want = none", errs)
						}
						return
					}
					if len(errs) == 0 {
						t.Fatal("errors: got = none, want = some")
					}
					for _, want := range c.Includes {
						if !slices.Contains(errs, want) {
							t.Errorf("errors: got = %v, want = including %v", errs, want)
						}
					}
					if c.Fields != nil {
						var fields []string
						for _, e := range errs {
							if !slices.Contains(fields, e.Field) {
								fields = append(fields, e.Field)
							}
						}
						if !slices.Equal(fields, c.Fields) {
							t.Errorf("fields: got = %v, want = %v", fields, c.Fields)
						}
					}
				})
			}
		})
	}
}
