package subscription_test

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// snapshotOf decodes a snapshot written in its JSON form.
func snapshotOf(t *testing.T, v map[string]any) *subscription.UsageSnapshot {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var u subscription.UsageSnapshot
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return &u
}

func encode(t *testing.T, u subscription.UsageSnapshot) string {
	t.Helper()
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDiffUsage(t *testing.T) {
	type tc struct {
		Name     string         `yaml:"name"`
		Source   string         `yaml:"source"`
		Previous map[string]any `yaml:"previous"`
		Current  map[string]any `yaml:"current"`
		Want     map[string]any `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "daily_usage_diff.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cur := snapshotOf(t, c.Current)
			before := encode(t, *cur)
			got := subscription.DiffUsage(*cur, snapshotOf(t, c.Previous))
			if g, w := encode(t, got), encode(t, *snapshotOf(t, c.Want)); g != w {
				t.Errorf("%s: DiffUsage()\ngot  = %s\nwant = %s", c.Source, g, w)
			}
			if after := encode(t, *cur); after != before {
				t.Errorf("%s: DiffUsage() changed current: got = %s, want = %s", c.Source, after, before)
			}
		})
	}
}

func TestDailyUsageValidate(t *testing.T) {
	var d subscription.DailyUsage
	err := d.Validate()
	if err == nil {
		t.Fatalf("Validate() of an empty snapshot: got = nil, want = an error")
	}
	for _, field := range []string{"customer_id", "subscription_id", "external_subscription_id", "from_datetime", "to_datetime", "refreshed_at", "usage_date"} {
		if !contains(err.Error(), field) {
			t.Errorf("Validate() = %v: got = no %s error, want = one", err, field)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
