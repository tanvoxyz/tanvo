package store_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/store/porttest"
)

// TestAnalyticsPostgres runs the analytics reports, which only the
// PostgreSQL store computes.
func TestAnalyticsPostgres(t *testing.T) {
	t.Run("rls", func(t *testing.T) { t.Parallel(); porttest.RunAnalytics(t, storeHarness(true)) })
	t.Run("owner", func(t *testing.T) { t.Parallel(); porttest.RunAnalytics(t, storeHarness(false)) })
}
