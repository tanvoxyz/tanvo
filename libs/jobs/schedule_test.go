package jobs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/jobs"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTimingNext(t *testing.T) {
	t.Parallel()
	paris := time.FixedZone("CET", 3600)
	tests := []struct {
		timing jobs.Timing
		from   time.Time
		want   time.Time
	}{
		{jobs.Every(5 * time.Minute), at("2026-10-23T10:00:00Z"), at("2026-10-23T10:05:00Z")},
		{jobs.Every(5 * time.Minute), at("2026-10-23T10:04:59Z"), at("2026-10-23T10:05:00Z")},
		{jobs.Every(15 * time.Minute), at("2026-10-23T23:59:00Z"), at("2026-10-24T00:00:00Z")},
		{jobs.HourlyAt(5), at("2026-10-23T10:04:00Z"), at("2026-10-23T10:05:00Z")},
		{jobs.HourlyAt(5), at("2026-10-23T10:05:00Z"), at("2026-10-23T11:05:00Z")},
		{jobs.HourlyAt(55), at("2026-10-23T23:56:00Z"), at("2026-10-24T00:55:00Z")},
		{jobs.DailyAt(1, 10), at("2026-10-23T01:09:00Z"), at("2026-10-23T01:10:00Z")},
		{jobs.DailyAt(1, 10), at("2026-10-23T01:10:00Z"), at("2026-10-24T01:10:00Z")},
		{jobs.DailyAt(1, 0), at("2026-12-31T02:00:00Z"), at("2027-01-01T01:00:00Z")},
		// Timings are UTC whatever the location of the current time.
		{jobs.DailyAt(1, 0), at("2026-10-23T00:30:00Z").In(paris), at("2026-10-23T01:00:00Z")},
	}
	for _, tt := range tests {
		if got := tt.timing.Next(tt.from); !got.Equal(tt.want) {
			t.Errorf("%s.Next(%v) = %v, want = %v", tt.timing, tt.from, got, tt.want)
		}
	}
}

// lagoClock is lago-api's clockwork schedule at 25b8b7b09 (clock.rb), as
// (clockwork name, timing), less the entries Tanvo leaves out.
var lagoClock = map[string]string{
	"activate_subscriptions":                     "every 5m0s",
	"activate_contracts":                         "every 5m0s",
	"refresh_draft_invoices":                     "every 5m0s",
	"process_subscription_activity":              "every 1m0s",
	"refresh_lifetime_usages":                    "every 5m0s",
	"refresh_wallets_ongoing_balance":            "every 5m0s",
	"terminate_ended_subscriptions":              "hourly at :05",
	"bill_customers":                             "hourly at :10",
	"create_billing_segments":                    "hourly at :12",
	"api_keys_track_usage":                       "hourly at :15",
	"process_billing_segments":                   "hourly at :17",
	"expire_incomplete_subscriptions":            "hourly at :20",
	"retry_generating_subscription_invoices":     "hourly at :30",
	"finalize_invoices":                          "hourly at :20",
	"mark_invoices_as_payment_overdue":           "hourly at :25",
	"terminate_coupons":                          "hourly at :30",
	"bill_ended_trial_subscriptions":             "hourly at :35",
	"terminate_wallets":                          "hourly at :45",
	"termination_alert":                          "hourly at :50",
	"terminate_expired_wallet_transaction_rules": "hourly at :50",
	"top_up_wallet_interval_credits":             "hourly at :55",
	"expire_order_forms":                         "hourly at :40",
	"execute_scheduled_orders":                   "hourly at :45",
	"clean_webhooks":                             "daily at 01:00",
	"clean_inbound_webhooks":                     "daily at 01:10",
	"clean_record_deletions":                     "daily at 01:20",
	"post_validate_events":                       "hourly at :05",
	"compute_daily_usage":                        "hourly at :15",
	"cancel_abandoned_payments":                  "hourly at :40",
	"process_dunning_campaigns":                  "hourly at :45",
	"retry_failed_invoices":                      "every 15m0s",
	"retry_inbound_webhooks":                     "every 15m0s",
}

// tanvoClock is the schedule of Tanvo's own entries, without a lago-api
// clock entry: kind → timing.
var tanvoClock = map[string]string{
	jobs.KindSweepWebhooks:          "every 5m0s",
	jobs.KindRepairUsageBuckets:     "every 10m0s",
	jobs.KindReportUsageBucketStats: "hourly at :00",
}

func TestScheduleMatchesLagoClock(t *testing.T) {
	t.Parallel()
	kinds := map[string]bool{}
	names := map[string]bool{}
	for _, p := range jobs.Schedule() {
		if kinds[p.Kind] {
			t.Errorf("kind %s scheduled twice", p.Kind)
		}
		kinds[p.Kind] = true
		if !strings.HasPrefix(p.Kind, "clock.") || !p.Queue.Valid() {
			t.Errorf("%s: kind or queue %q invalid", p.Kind, p.Queue)
		}
		if want, ok := tanvoClock[p.Kind]; ok {
			if p.Source != "" || p.Job != "" || p.Timing.String() != want {
				t.Errorf("%s: source, job, timing got = %q, %q, %s, want = Tanvo's own, %s", p.Kind, p.Source, p.Job, p.Timing, want)
			}
			continue
		}
		name, ok := strings.CutPrefix(p.Source, "clock.rb schedule:")
		if !ok {
			t.Errorf("%s: source = %q, want a clock.rb entry", p.Kind, p.Source)
		}
		names[name] = true
		if want, ok := lagoClock[name]; !ok || p.Timing.String() != want {
			t.Errorf("%s: timing = %s, want = %s (lago %s)", p.Kind, p.Timing, want, name)
		}
	}
	for name := range lagoClock {
		if !names[name] {
			t.Errorf("lago clock entry %s is not scheduled", name)
		}
	}
	for kind := range tanvoClock {
		if !kinds[kind] {
			t.Errorf("Tanvo clock entry %s is not scheduled", kind)
		}
	}
}

// TestScheduleSources checks the lago-api references against a checkout
// at ~/Code/frameworks/lago/api, when there is one.
func TestScheduleSources(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	root := filepath.Join(home, "Code/frameworks/lago/api")
	clock, err := os.ReadFile(filepath.Join(root, "clock.rb"))
	if err != nil {
		t.Skipf("no lago-api checkout: %v", err)
	}
	for _, p := range jobs.Schedule() {
		if p.Source == "" {
			continue
		}
		name := strings.TrimPrefix(p.Source, "clock.rb ")
		if !strings.Contains(string(clock), `"`+name+`"`) {
			t.Errorf("%s: %s not in clock.rb", p.Kind, name)
		}
		if _, err := os.Stat(filepath.Join(root, p.Job)); err != nil {
			t.Errorf("%s: %v", p.Kind, err)
		}
	}
}
