package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// rootField matches the fields a Bloblang mapping sets.
var rootField = regexp.MustCompile(`(?m)^\s*root\.([a-z_]+)\s*=`)

// TestConnectorMappings checks that the Redpanda Connect configurations in
// connectors/ set only fields of [RawMessage], set the required ones, and
// key the records as [PartitionKey] does.
func TestConnectorMappings(t *testing.T) {
	t.Parallel()
	known := map[string]bool{}
	rt := reflect.TypeFor[RawMessage]()
	for i := range rt.NumField() {
		if name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); name != "" {
			known[name] = true
		}
	}
	required := []string{"organization_id", "transaction_id", "code", "timestamp", "ingested_at", "source"}
	const key = `key: '${! json("organization_id") }/${! json("external_subscription_id") }/${! json("transaction_id") }'`
	files, err := filepath.Glob(filepath.Join("connectors", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(files), 3; got != want {
		t.Fatalf("connector configurations got = %d, want = %d", got, want)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var set []string
			for _, m := range rootField.FindAllStringSubmatch(string(b), -1) {
				if !known[m[1]] {
					t.Errorf("mapped field got = %s, want = a RawMessage field", m[1])
				}
				set = append(set, m[1])
			}
			for _, r := range required {
				if !slices.Contains(set, r) {
					t.Errorf("mapped fields got = %v, want = containing %s", set, r)
				}
			}
			if !strings.Contains(string(b), key) {
				t.Errorf("record key got = missing, want = %s", key)
			}
		})
	}
}
