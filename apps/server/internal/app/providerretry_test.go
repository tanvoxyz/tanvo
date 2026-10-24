package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// TestProviderRetry checks how many times the provider tax jobs run a
// failing call: contention of the tax provider (its rate limits, its
// unavailability, a timeout) 6 times, and, when pulling taxes, the lock
// and numbering failures as the billing jobs do.
func TestProviderRetry(t *testing.T) {
	contention := fmt.Errorf("pull: %w", kernel.ErrTaxContention)
	lock := fmt.Errorf("pull: %w", &pgconn.PgError{Code: pgLockNotAvailable})
	conflict := fmt.Errorf("pull: %w", core.ErrConflict)
	refused := &kernel.TaxProviderError{Code: "taxDateTooFarInFuture"}
	for _, tt := range []struct {
		source string
		kind   string
		err    error
		want   int
	}{
		{"spec/jobs/invoices/provider_taxes/pull_taxes_and_apply_job_spec.rb:56", kernel.JobPullProviderTaxes, contention, jobs.HTTPRetryAttempts},
		{"spec/jobs/invoices/provider_taxes/pull_taxes_and_apply_job_spec.rb:56", kernel.JobPullProviderTaxes, lock, jobs.LockRetryAttempts},
		{"spec/jobs/invoices/provider_taxes/pull_taxes_and_apply_job_spec.rb:56", kernel.JobPullProviderTaxes, conflict, jobs.SequenceRetryAttempts},
		{"spec/jobs/invoices/provider_taxes/pull_taxes_and_apply_job_spec.rb:56", kernel.JobPullProviderTaxes, refused, 0},
		{"spec/jobs/invoices/provider_taxes/void_job_spec.rb:75", kernel.JobVoidProviderTaxes, contention, jobs.HTTPRetryAttempts},
		{"spec/jobs/invoices/provider_taxes/void_job_spec.rb:75", kernel.JobVoidProviderTaxes, lock, 0},
	} {
		t.Run(tt.kind+"/"+tt.err.Error(), func(t *testing.T) {
			t.Log("source:", tt.source)
			got := 0
			for _, r := range providerRetry(tt.kind) {
				if r.Match(tt.err) {
					got = r.Attempts
					break
				}
			}
			if got != tt.want {
				t.Errorf("attempts: got = %d, want = %d", got, tt.want)
			}
		})
	}
	if errors.Is(refused, kernel.ErrTaxContention) {
		t.Errorf("a refusal is contention")
	}
}
