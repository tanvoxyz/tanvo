// Package spectest loads test cases ported from lago-api specs (testing
// §2.1). A case file is YAML:
//
//	defaults: {…}          # merged under every case
//	cases:
//	  - name: …
//	    source: "spec/models/invoice_spec.rb:42"   # or "tanvo"
//	    …
//	skipped:
//	  - source: "spec/models/invoice_spec.rb:99"
//	    reason: "asserts ActiveRecord associations"
//
// Every case must record its source; every skipped entry its reason.
package spectest

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

	"github.com/tanvoxyz/tanvo/libs/money"
)

type file struct {
	Defaults map[string]any   `yaml:"defaults"`
	Cases    []map[string]any `yaml:"cases"`
	Skipped  []struct {
		Source string `yaml:"source"`
		Reason string `yaml:"reason"`
	} `yaml:"skipped"`
}

// Load decodes testdata/<name> into cases of type T, strictly (unknown
// fields fail). T should have Name and Source fields tagged "name" and
// "source".
func Load[T any](t testing.TB, name string) []T {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for i, s := range f.Skipped {
		if !strings.HasPrefix(s.Source, "spec/") || s.Reason == "" {
			t.Fatalf("%s: skipped %d: source = %q, reason = %q, want = spec/...:<line> and a reason", name, i, s.Source, s.Reason)
		}
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

// Time is an instant in testdata: RFC 3339, "2006-01-02 15:04:05" or a
// date, the last two in UTC. A null or absent value is the zero time.
type Time struct{ time.Time }

// UnmarshalYAML implements yaml.Unmarshaler.
func (t *Time) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", time.DateOnly} {
		if p, err := time.Parse(layout, n.Value); err == nil {
			t.Time = p.UTC()
			return nil
		}
	}
	return fmt.Errorf("line %d: bad instant %q", n.Line, n.Value)
}

// Decimal is a money.Decimal in testdata, written as a string or a number.
type Decimal struct{ money.Decimal }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Decimal) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		return nil
	}
	v, err := money.Parse(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Decimal = v
	return nil
}
