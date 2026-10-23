package period

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// A testdata file holds cases ported from lago's specs. Each case is merged
// over the file's defaults, then decoded strictly into the case type.
type testFile struct {
	Defaults map[string]any   `yaml:"defaults"`
	Cases    []map[string]any `yaml:"cases"`
}

// loadCases decodes testdata/name into cases of type T. Each case must have a
// "source" naming the spec line it is ported from.
func loadCases[T any](t *testing.T, name string, fill func(map[string]any)) []T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var f testFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	cases := make([]T, 0, len(f.Cases))
	for i, c := range f.Cases {
		merged := maps.Clone(f.Defaults)
		if merged == nil {
			merged = map[string]any{}
		}
		maps.Copy(merged, c)
		if src, _ := merged["source"].(string); !strings.HasPrefix(src, "spec/") && !strings.HasPrefix(src, "tanvo") {
			t.Fatalf("%s: case %d: source = %q, want = spec/...:<line> or tanvo", name, i, src)
		}
		if fill != nil {
			fill(merged)
		}
		b, err := yaml.Marshal(merged)
		if err != nil {
			t.Fatal(err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		var v T
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: case %d (%v): %v", name, i, c["name"], err)
		}
		cases = append(cases, v)
	}
	return cases
}

// ts is an instant in testdata: RFC 3339, "2006-01-02 15:04:05" or a date,
// the last two in UTC.
type ts struct{ time.Time }

func parseTS(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad instant %q", s)
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (t *ts) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	v, err := parseTS(n.Value)
	t.Time = v
	return err
}

func loadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// wantTime decodes an expected instant.
func wantTime(v any) (time.Time, error) {
	switch v := v.(type) {
	case time.Time:
		return v, nil
	case string:
		return parseTS(v)
	}
	return time.Time{}, fmt.Errorf("bad instant %v", v)
}
