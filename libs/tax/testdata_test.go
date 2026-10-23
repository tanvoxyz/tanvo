package tax_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// testFile is a file of cases ported from lago-api specs.
type testFile struct {
	Cases   []testCase `yaml:"cases"`
	Skipped []struct {
		Source string `yaml:"source"`
		Reason string `yaml:"reason"`
	} `yaml:"skipped"`
}

// testCase is one case: an operation, its input, and the expected output
// (a subset: only the fields given are compared) or error.
type testCase struct {
	Name   string    `yaml:"name"`
	Source string    `yaml:"source"`
	Note   string    `yaml:"note"`
	Op     string    `yaml:"op"`
	Input  yaml.Node `yaml:"input"`
	Want   yaml.Node `yaml:"want"`
	Error  string    `yaml:"error"`
}

// loadCases reads a testdata file strictly.
func loadCases(t *testing.T, name string) testFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f testFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(f.Cases) == 0 {
		t.Fatalf("%s: no cases", name)
	}
	for _, c := range f.Cases {
		if !strings.HasPrefix(c.Source, "spec/") && !strings.HasPrefix(c.Source, "tanvo") {
			t.Fatalf("%s: case %q: source %q must start with spec/ or tanvo", name, c.Name, c.Source)
		}
	}
	for _, s := range f.Skipped {
		if s.Source == "" || s.Reason == "" {
			t.Fatalf("%s: skipped entries need a source and a reason", name)
		}
	}
	return f
}

// nodeValue converts a YAML node to a JSON-ready value, keeping numbers
// verbatim (json.Number) so that decimals keep their precision, and
// normalizing map keys (see normKey).
func nodeValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case 0:
		return nil, nil
	case yaml.DocumentNode:
		return nodeValue(n.Content[0])
	case yaml.AliasNode:
		return nodeValue(n.Alias)
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			v, err := nodeValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			if n.Content[i].Value == "<<" {
				if mm, ok := v.(map[string]any); ok {
					for k, vv := range mm {
						if _, set := m[k]; !set {
							m[k] = vv
						}
					}
				}
				continue
			}
			m[normKey(n.Content[i].Value)] = v
		}
		return m, nil
	case yaml.SequenceNode:
		s := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeValue(c)
			if err != nil {
				return nil, err
			}
			s = append(s, v)
		}
		return s, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!int", "!!float":
			return json.Number(n.Value), nil
		case "!!bool":
			return n.Value == "true", nil
		case "!!null":
			return nil, nil
		default:
			return n.Value, nil
		}
	}
	return nil, fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

// normKey makes snake_case and CamelCase keys compare equal.
func normKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "_", ""))
}

// decodeInput decodes a case's input into v, rejecting unknown fields.
func decodeInput(t *testing.T, n *yaml.Node, v any) {
	t.Helper()
	val, err := nodeValue(n)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(val)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode input %s: %v", b, err)
	}
}

// normalize marshals v to JSON and back with normalized keys.
func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	return normKeys(out)
}

func normKeys(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, vv := range v {
			m[normKey(k)] = normKeys(vv)
		}
		return m
	case []any:
		for i := range v {
			v[i] = normKeys(v[i])
		}
	}
	return v
}

// match reports the differences between got and the subset want.
func match(path string, got, want any) []string {
	switch w := want.(type) {
	case nil:
		if got == nil {
			return nil
		}
		if s, ok := got.([]any); ok && len(s) == 0 {
			return nil
		}
		if m, ok := got.(map[string]any); ok && len(m) == 0 {
			return nil
		}
		return []string{fmt.Sprintf("%s: got = %v, want = nil", path, got)}
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, want)}
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var diffs []string
		for _, k := range keys {
			gv, ok := g[k]
			if !ok {
				diffs = append(diffs, fmt.Sprintf("%s.%s: missing, want = %v", path, k, w[k]))
				continue
			}
			diffs = append(diffs, match(path+"."+k, gv, w[k])...)
		}
		return diffs
	case []any:
		g, ok := got.([]any)
		if !ok && got == nil && len(w) == 0 {
			return nil
		}
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, want)}
		}
		var diffs []string
		for i := range w {
			diffs = append(diffs, match(fmt.Sprintf("%s[%d]", path, i), g[i], w[i])...)
		}
		return diffs
	case json.Number, string:
		if equalScalar(got, want) {
			return nil
		}
		return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, want)}
	default:
		if got == want {
			return nil
		}
		return []string{fmt.Sprintf("%s: got = %v, want = %v", path, got, want)}
	}
}

// equalScalar compares scalars, as decimals when the wanted value is a
// number or both parse as numbers.
func equalScalar(got, want any) bool {
	gs, ok := scalarString(got)
	if !ok {
		return false
	}
	ws, _ := scalarString(want)
	if gs == ws {
		return true
	}
	gd, err1 := money.Parse(gs)
	wd, err2 := money.Parse(ws)
	if err1 != nil || err2 != nil {
		return false
	}
	return gd.Equal(wd)
}

func scalarString(v any) (string, bool) {
	switch v := v.(type) {
	case json.Number:
		return string(v), true
	case string:
		return v, true
	}
	return "", false
}

// runFile runs every case of a testdata file through run.
func runFile(t *testing.T, name string, run func(t *testing.T, c testCase) (any, error)) {
	t.Helper()
	f := loadCases(t, name)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := run(t, c)
			if c.Error != "" {
				want, ok := errorCode(c.Error)
				if !ok {
					t.Fatalf("%s: unknown error code %q", c.Source, c.Error)
				}
				if !errors.Is(err, want) {
					t.Fatalf("%s: got error = %v, want = %v", c.Source, err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: got error = %v, want = nil", c.Source, err)
			}
			want, err := nodeValue(&c.Want)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range match("", normalize(t, got), want) {
				t.Errorf("%s: %s", c.Source, d)
			}
		})
	}
}
