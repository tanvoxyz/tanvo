package porttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// testBillingRetry checks the reads and writes of the billing retries and
// the daily usage rebuild: the organizations with stuck generating
// invoices (organization.WorkStuckGeneratingInvoices, lago
// Clock::RetryGeneratingSubscriptionInvoicesJob,
// app/jobs/clock/retry_generating_subscription_invoices_job.rb:9-17), the
// organizations with invoices failed on the tax provider's API limit
// (organization.WorkTaxRetriedInvoices, lago Clock::RetryFailedInvoicesJob,
// app/jobs/clock/retry_failed_invoices_job.rb:8-14), the
// discarded metrics a usage snapshot names (catalog.MetricRepository
// GetWithDiscarded, app/models/fee.rb:33) and the batched deletion of daily
// usages (subscription.DailyUsageRepository DeleteFrom,
// lib/tasks/daily_usages.rake:63-71).
func testBillingRetry(t *testing.T, h Harness) {
	t.Run("stuck generating invoices", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		_, third := e.org("third")
		e.tx(func(ctx context.Context, r port.Repos) error {
			c := subscriptionFixtureCustomer(ctx, e, r, third, "cus_3")
			generating := func(c *customer.Customer, typ invoice.Type, st invoice.Status, created time.Time) *invoice.Invoice {
				inv := &invoice.Invoice{
					ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: c.OrganizationID, CustomerID: c.ID,
					BillingEntityID: c.BillingEntityID, Type: typ, Status: st,
					PaymentStatus: core.PaymentStatusPending, Currency: money.MustCurrency("EUR"),
					IssuingDate: core.NewDate(2025, time.March, 14), PaymentDueDate: core.NewDate(2025, time.March, 14),
					ExpectedFinalizationDate: core.NewDate(2025, time.March, 14),
					PaymentMethodType:        core.PaymentMethodTypeProvider, CreatedAt: created, UpdatedAt: created,
				}
				noErr(t, "Invoices.Create()", r.Invoices.Create(ctx, inv))
				return inv
			}
			old := now.Add(-invoice.GeneratingRetryAge - time.Hour)
			// p's organization: a stuck subscription invoice.
			generating(p.customer, invoice.TypeSubscription, invoice.StatusGenerating, old)
			// other: a stuck one with a generation error, a recent one and
			// a stuck one-off invoice.
			failed := generating(other.customer, invoice.TypeSubscription, invoice.StatusGenerating, old)
			noErr(t, "ErrorDetails.Create()", r.ErrorDetails.Create(ctx, &invoice.ErrorDetail{
				ID: core.NewID[core.ErrorDetailID](e.ids), OrganizationID: other.org, InvoiceID: failed.ID,
				Code: invoice.ErrorCodeInvoiceGenerationError, Details: json.RawMessage(`{"error":"boom"}`), CreatedAt: now,
			}))
			generating(other.customer, invoice.TypeSubscription, invoice.StatusGenerating, now.Add(-time.Hour))
			generating(other.customer, invoice.TypeOneOff, invoice.StatusGenerating, old)
			// third: an old subscription invoice, but a draft.
			generating(c, invoice.TypeSubscription, invoice.StatusDraft, old)

			got, err := r.Organizations.WithWork(ctx, organization.WorkStuckGeneratingInvoices, now)
			noErr(t, "WithWork()", err)
			equal(t, "WithWork()", got, []core.OrganizationID{p.org})
			return nil
		})
	})

	// source: spec/jobs/clock/retry_failed_invoices_job_spec.rb:6-60
	t.Run("tax retried invoices", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		_, third := e.org("third")
		e.tx(func(ctx context.Context, r port.Repos) error {
			c := subscriptionFixtureCustomer(ctx, e, r, third, "cus_3")
			withError := func(c *customer.Customer, st invoice.Status, code invoice.ErrorCode, details string) {
				inv := &invoice.Invoice{
					ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: c.OrganizationID, CustomerID: c.ID,
					BillingEntityID: c.BillingEntityID, Type: invoice.TypeSubscription, Status: st,
					PaymentStatus: core.PaymentStatusPending, Currency: money.MustCurrency("EUR"),
					IssuingDate: core.NewDate(2025, time.March, 14), PaymentDueDate: core.NewDate(2025, time.March, 14),
					ExpectedFinalizationDate: core.NewDate(2025, time.March, 14),
					PaymentMethodType:        core.PaymentMethodTypeProvider, CreatedAt: now, UpdatedAt: now,
				}
				noErr(t, "Invoices.Create()", r.Invoices.Create(ctx, inv))
				noErr(t, "ErrorDetails.Create()", r.ErrorDetails.Create(ctx, &invoice.ErrorDetail{
					ID: core.NewID[core.ErrorDetailID](e.ids), OrganizationID: c.OrganizationID, InvoiceID: inv.ID,
					Code: code, Details: json.RawMessage(details), CreatedAt: now,
				}))
			}
			// p's organization: a failed invoice on the API limit, matched
			// case-insensitively.
			withError(p.customer, invoice.StatusFailed, invoice.ErrorCodeTaxError,
				`{"tax_error":"taxDateTooFarInFuture","tax_error_message":"You've exceeded your api LIMIT of 10 per second"}`)
			// other: a failed invoice on an unknown product (no message)
			// and a finalized one on the API limit.
			withError(other.customer, invoice.StatusFailed, invoice.ErrorCodeTaxError, `{"tax_error":"productExternalIdUnknown"}`)
			withError(other.customer, invoice.StatusFinalized, invoice.ErrorCodeTaxError,
				`{"tax_error":"taxDateTooFarInFuture","tax_error_message":"API limit"}`)
			// third: the API limit in another error code.
			withError(c, invoice.StatusFailed, invoice.ErrorCodeTaxVoidingError, `{"tax_voiding_error":"x","tax_error_message":"API limit"}`)

			got, err := r.Organizations.WithWork(ctx, organization.WorkTaxRetriedInvoices, now)
			noErr(t, "WithWork()", err)
			equal(t, "WithWork()", got, []core.OrganizationID{p.org})
			return nil
		})
	})

	t.Run("discarded metric", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		e.tx(func(ctx context.Context, r port.Repos) error {
			m := catalogFixtureMetric(e, a.OrganizationID, "gone")
			m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "region", Values: []string{"eu"}}}
			noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, m))
			got, err := r.Metrics.GetWithDiscarded(ctx, a.OrganizationID, m.ID)
			noErr(t, "GetWithDiscarded() kept", err)
			equal(t, "GetWithDiscarded() kept", got, m)

			noErr(t, "Metrics.Discard()", r.Metrics.Discard(ctx, a.OrganizationID, m.ID, now))
			_, err = r.Metrics.Get(ctx, a.OrganizationID, m.ID)
			wantErr(t, "Get() discarded", err, core.ErrNotFound)
			got, err = r.Metrics.GetWithDiscarded(ctx, a.OrganizationID, m.ID)
			noErr(t, "GetWithDiscarded() discarded", err)
			equal(t, "GetWithDiscarded() code", got.Code, "gone")
			equal(t, "GetWithDiscarded() deleted_at set", got.DeletedAt != nil, true)

			_, err = r.Metrics.GetWithDiscarded(ctx, b.OrganizationID, m.ID)
			wantErr(t, "GetWithDiscarded() from another organization", err, core.ErrNotFound)
			_, err = r.Metrics.GetWithDiscarded(ctx, a.OrganizationID, core.NewID[core.BillableMetricID](e.ids))
			wantErr(t, "GetWithDiscarded() unknown", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("delete daily usages from a day", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		s1, s2, s3 := p.newSub(e, "s1"), p.newSub(e, "s2"), p.newSub(e, "s3")
		foreign := other.newSub(e, "s1")
		day := core.NewDate(2025, time.March, 10)
		usage := func(s *subscription.Subscription, d core.Date) *subscription.DailyUsage {
			return &subscription.DailyUsage{
				ID: core.NewID[core.DailyUsageID](e.ids), OrganizationID: s.OrganizationID, CustomerID: s.CustomerID,
				SubscriptionID: s.ID, ExternalSubscriptionID: s.ExternalID,
				Usage: subscription.UsageSnapshot{Charges: []subscription.ChargeUsage{}},
				From:  d.Midnight(time.UTC), To: d.AddDays(1).Midnight(time.UTC), RefreshedAt: now, UsageDate: d,
				CreatedAt: now, UpdatedAt: now,
			}
		}
		count := func(ctx context.Context, r port.Repos, s *subscription.Subscription) int {
			pg, err := r.DailyUsages.ListBySubscription(ctx, s.OrganizationID, s.ID, core.PageRequest{Size: 100})
			noErr(t, "ListBySubscription()", err)
			return len(pg.Items)
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, s := range []*subscription.Subscription{s1, s2, s3, foreign} {
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
			}
			for _, du := range []*subscription.DailyUsage{
				usage(s1, day.AddDays(-1)), usage(s1, day), usage(s1, day.AddDays(1)), usage(s1, day.AddDays(2)),
				usage(s2, day), usage(s3, day), usage(foreign, day),
			} {
				noErr(t, "DailyUsages.Create()", r.DailyUsages.Create(ctx, du))
			}
			ids := []core.SubscriptionID{s1.ID, s2.ID, foreign.ID}

			// Batches of two: the rows from day on of s1 and s2 (four), not
			// the day before, not s3, not the other organization's.
			total := 0
			for {
				n, err := r.DailyUsages.DeleteFrom(ctx, p.org, ids, day, 2)
				noErr(t, "DeleteFrom()", err)
				if n > 2 {
					t.Errorf("DeleteFrom() batch got = %d, want <= 2", n)
				}
				total += n
				if n < 2 {
					break
				}
			}
			equal(t, "DeleteFrom() total", total, 4)
			equal(t, "daily usages of s1 left", count(ctx, r, s1), 1)
			equal(t, "daily usages of s2 left", count(ctx, r, s2), 0)
			equal(t, "daily usages of s3 left", count(ctx, r, s3), 1)
			equal(t, "daily usages of another organization left", count(ctx, r, foreign), 1)

			n, err := r.DailyUsages.DeleteFrom(ctx, p.org, nil, day.AddDays(-5), 10)
			noErr(t, "DeleteFrom() of no subscription", err)
			equal(t, "DeleteFrom() of no subscription", n, 0)
			return nil
		})
	})
}
