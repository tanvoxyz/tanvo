package pricing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// loadCases decodes every testdata file matching pattern into a list of
// cases; each case must record its source.
func loadCases[T any](t *testing.T, pattern string) map[string][]T {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no testdata matches %s", pattern)
	}
	out := map[string][]T{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for i, r := range raw {
			var probe struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(r, &probe); err != nil || !strings.HasPrefix(probe.Source, "spec/") {
				t.Fatalf("%s: case %d has no spec source", f, i)
			}
			dec := json.NewDecoder(bytes.NewReader(r))
			dec.DisallowUnknownFields()
			var c T
			if err := dec.Decode(&c); err != nil {
				t.Fatalf("%s: case %d (%s): %v", f, i, probe.Source, err)
			}
			out[filepath.Base(f)] = append(out[filepath.Base(f)], c)
		}
	}
	return out
}

// caseName names a subtest after its source and description.
func caseName(source, name string) string {
	return strings.TrimPrefix(source, "spec/") + " " + name
}

// toGeneric re-encodes v as generic JSON with numbers kept exact.
func toGeneric(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return decodeGeneric(t, data)
}

func decodeGeneric(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var g any
	if err := dec.Decode(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

// match compares want (a JSON expectation) with got (generic JSON):
//   - objects match when every key of want matches (got may have more);
//   - arrays match element by element and must have the same length;
//   - a number, or a string that parses as a decimal, matches a decimal of
//     the same value, whatever its scale;
//   - a string "~x" matches a decimal that rounds (half up) to x at x's
//     scale, for values lago computes with floats, and "^x" one that
//     rounds up (toward +∞) to x, for lago's ceil(n) expectations;
//   - null matches null or an absent value.
func match(path string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got = %v, want = an object", path, got)}
		}
		var errs []string
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			errs = append(errs, match(path+"."+k, w[k], g[k])...)
		}
		return errs
	case []any:
		g, ok := got.([]any)
		if !ok {
			if got == nil && len(w) == 0 {
				return nil
			}
			return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, w)}
		}
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s: got = %d elements (%v), want = %d (%v)", path, len(g), g, len(w), w)}
		}
		var errs []string
		for i := range w {
			errs = append(errs, match(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])...)
		}
		return errs
	case nil:
		if got != nil {
			return []string{fmt.Sprintf("%s: got = %v, want = null", path, got)}
		}
		return nil
	case bool:
		if got != w {
			return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, w)}
		}
		return nil
	case json.Number:
		return matchDecimal(path, w.String(), got)
	case string:
		if _, err := money.Parse(strings.TrimLeft(w, "~^")); err == nil {
			return matchDecimal(path, w, got)
		}
		if got != w {
			return []string{fmt.Sprintf("%s: got = %v, want = %q", path, got, w)}
		}
		return nil
	default:
		return []string{fmt.Sprintf("%s: unsupported expectation %T", path, want)}
	}
}

func matchDecimal(path, want string, got any) []string {
	var gs string
	switch g := got.(type) {
	case string:
		gs = g
	case json.Number:
		gs = g.String()
	default:
		return []string{fmt.Sprintf("%s: got = %v, want = %s", path, got, want)}
	}
	gd, err := money.Parse(gs)
	if err != nil {
		return []string{fmt.Sprintf("%s: got = %q, want = %s", path, gs, want)}
	}
	wd := money.MustParse(strings.TrimLeft(want, "~^"))
	switch want[0] {
	case '~':
		gd = gd.Round(-wd.Exponent(), money.HalfUp)
	case '^':
		gd = gd.Round(-wd.Exponent(), money.Ceiling)
	}
	if !gd.Equal(wd) {
		return []string{fmt.Sprintf("%s: got = %s, want = %s", path, gd, want)}
	}
	return nil
}

// check reports the mismatches of want and got.
func check(t *testing.T, want json.RawMessage, got any) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	for _, e := range match("$", decodeGeneric(t, want), toGeneric(t, got)) {
		t.Error(e)
	}
}
