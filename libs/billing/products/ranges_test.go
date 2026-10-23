package products_test

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
)

func props(t *testing.T, s string) catalog.Properties {
	t.Helper()
	p, err := catalog.NewProperties([]byte(s))
	if err != nil {
		t.Fatalf("NewProperties(%s) error = %v", s, err)
	}
	return p
}

func bounds(t *testing.T, p catalog.Properties, key string) [][2]any {
	t.Helper()
	var m map[string]json.RawMessage
	var list []map[string]any
	if err := json.Unmarshal(p.Raw(), &m); err != nil {
		t.Fatalf("decode %s: %v", p.Raw(), err)
	}
	if err := json.Unmarshal(m[key], &list); err != nil {
		t.Fatalf("decode %s: %v", m[key], err)
	}
	var out [][2]any
	for _, r := range list {
		out = append(out, [2]any{r["from_value"], r["to_value"]})
	}
	return out
}

func TestNormalizeRanges(t *testing.T) {
	t.Parallel()
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:17
	t.Run("starts each tier where the previous one ends", func(t *testing.T) {
		t.Parallel()
		got, err := products.NormalizeRanges(props(t, `{"amount":"1","graduated_ranges":[
			{"to_value":"10","flat_amount":"10","per_unit_amount":"0.5"},
			{"to_value":"20.5","flat_amount":"10","per_unit_amount":"0.5"},
			{"to_value":null,"flat_amount":"0","per_unit_amount":"0.4"}]}`))
		if err != nil {
			t.Fatalf("NormalizeRanges() error = %v", err)
		}
		want := [][2]any{{0.0, 10.0}, {10.0, 20.5}, {20.5, nil}}
		if b := bounds(t, got, "graduated_ranges"); len(b) != 3 || b[0] != want[0] || b[1] != want[1] || b[2] != want[2] {
			t.Errorf("bounds = %v, want = %v", b, want)
		}
	})
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:27
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:37
	for _, key := range []string{"graduated_ranges", "graduated_percentage_ranges", "volume_ranges"} {
		t.Run("derives the lower bounds of "+key, func(t *testing.T) {
			t.Parallel()
			got, err := products.NormalizeRanges(props(t, `{"`+key+`":[{"to_value":5,"flat_amount":"0"},{"to_value":null,"flat_amount":"0"}]}`))
			if err != nil {
				t.Fatalf("NormalizeRanges() error = %v", err)
			}
			if b := bounds(t, got, key); len(b) != 2 || b[0] != [2]any{0.0, 5.0} || b[1] != [2]any{5.0, nil} {
				t.Errorf("bounds = %v, want = [[0 5] [5 <nil>]]", b)
			}
		})
	}
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:69
	t.Run("leaves properties without tiers as they are", func(t *testing.T) {
		t.Parallel()
		in := props(t, `{"amount":"1"}`)
		got, err := products.NormalizeRanges(in)
		if err != nil || string(got.Raw()) != string(in.Raw()) {
			t.Errorf("NormalizeRanges() = %s, %v, want it unchanged", got.Raw(), err)
		}
	})
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:77
	t.Run("rejects a tier naming its lower bound", func(t *testing.T) {
		t.Parallel()
		_, err := products.NormalizeRanges(props(t, `{"graduated_ranges":[{"from_value":0,"to_value":"10"},{"to_value":null}]}`))
		wantFieldError(t, err, "properties", catalog.CodeFromValueNotAllowed)
	})
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:46
	// source: spec/services/rate_properties/normalize_ranges_service_spec.rb:95
	for name, tiers := range map[string]string{
		"not an object":    `["10",{"to_value":null}]`,
		"not rising":       `[{"to_value":"10"},{"to_value":"10"},{"to_value":null}]`,
		"closed last tier": `[{"to_value":"10"},{"to_value":"20"}]`,
		"open before last": `[{"to_value":null},{"to_value":"20"},{"to_value":null}]`,
		"not a number":     `[{"to_value":"ten"},{"to_value":null}]`,
		"not positive":     `[{"to_value":"0"},{"to_value":null}]`,
		"infinite":         `[{"to_value":"10"},{"to_value":"Infinity"},{"to_value":null}]`,
	} {
		t.Run("rejects tiers "+name, func(t *testing.T) {
			t.Parallel()
			_, err := products.NormalizeRanges(props(t, `{"graduated_ranges":`+tiers+`}`))
			wantFieldError(t, err, "properties", "invalid_graduated_ranges")
		})
	}
	// Tanvo keeps bounds as exact decimals: those lago refuses as beyond a
	// float's precision are accepted and kept apart.
	t.Run("keeps bounds beyond float precision exact", func(t *testing.T) {
		t.Parallel()
		got, err := products.NormalizeRanges(props(t, `{"graduated_ranges":[{"to_value":"1.00000000000000001"},{"to_value":"1.00000000000000002"},{"to_value":null}]}`))
		if err != nil {
			t.Fatalf("NormalizeRanges() error = %v", err)
		}
		shown, err := products.PresentRanges(got)
		if err != nil {
			t.Fatalf("PresentRanges() error = %v", err)
		}
		want := `{"graduated_ranges":[{"to_value":"1.00000000000000001"},{"to_value":"1.00000000000000002"},{"to_value":null}]}`
		if string(shown.Raw()) != want {
			t.Errorf("shown = %s, want = %s", shown.Raw(), want)
		}
	})
}
