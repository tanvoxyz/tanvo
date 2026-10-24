package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// WithWork implements organization.Repository. It is not
// organization-scoped: the clock jobs dispatch per-organization jobs from
// it (architecture §9).
func (r organizationRepo) WithWork(ctx context.Context, w organization.Work, at time.Time) ([]core.OrganizationID, error) {
	if err := organization.CheckWork(w, at); err != nil {
		return nil, err
	}
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	at = at.UTC()
	until := at
	var rows []uuid.UUID
	switch w {
	case organization.WorkActiveSubscriptions:
		rows, err = q.ListOrganizationsWithActiveSubscriptions(ctx)
	case organization.WorkPendingSubscriptions:
		rows, err = q.ListOrganizationsWithPendingSubscriptions(ctx, at.Add(organization.ScanDayMargin))
	case organization.WorkEndingSubscriptions:
		since, until := at.Add(-organization.EndingMargin), at.Add(organization.EndingMargin)
		rows, err = q.ListOrganizationsWithEndingSubscriptions(ctx, db.ListOrganizationsWithEndingSubscriptionsParams{Since: &since, Until: &until})
	case organization.WorkDraftInvoices, organization.WorkRefreshRequested:
		rows, err = q.ListOrganizationsWithDraftInvoices(ctx, w == organization.WorkRefreshRequested)
	case organization.WorkLifetimeUsages:
		rows, err = q.ListOrganizationsWithLifetimeUsagesToRecalculate(ctx)
	case organization.WorkAwaitingWalletRefresh:
		rows, err = q.ListOrganizationsWithCustomersAwaitingWalletRefresh(ctx)
	case organization.WorkExpiredWallets:
		rows, err = q.ListOrganizationsWithExpiredWallets(ctx, at)
	case organization.WorkIntervalRules:
		rows, err = q.ListOrganizationsWithIntervalRules(ctx)
	case organization.WorkExpiredCoupons:
		rows, err = q.ListOrganizationsWithExpiredCoupons(ctx, &until)
	case organization.WorkOverdueInvoices:
		rows, err = q.ListOrganizationsWithOverdueInvoices(ctx, core.DateOf(at, time.UTC).Midnight(time.UTC))
	case organization.WorkStuckGeneratingInvoices:
		rows, err = q.ListOrganizationsWithStuckGeneratingInvoices(ctx, at.Add(-invoice.GeneratingRetryAge))
	case organization.WorkTaxRetriedInvoices:
		rows, err = q.ListOrganizationsWithTaxRetriedInvoices(ctx, invoice.RetriedTaxErrorMessage)
	case organization.WorkEndingAtSubscriptions:
		rows, err = q.ListOrganizationsWithEndingAtSubscriptions(ctx, core.DateOf(at, time.UTC).Midnight(time.UTC))
	case organization.WorkExpirableSubscriptions:
		rows, err = q.ListOrganizationsWithExpirableSubscriptions(ctx, at)
	case organization.WorkExpiringOrderForms:
		rows, err = q.ListOrganizationsWithExpiringOrderForms(ctx, at.Add(organization.ScanDayMargin))
	case organization.WorkExecutableOrders:
		rows, err = q.ListOrganizationsWithExecutableOrders(ctx, at)
	case organization.WorkDailyUsages:
		rows, err = q.ListOrganizationsWithDailyUsages(ctx)
	case organization.WorkEventsToValidate:
		from, to := event.ValidationWindow(at)
		rows, err = q.ListOrganizationsWithEventsToValidate(ctx, db.ListOrganizationsWithEventsToValidateParams{FromAt: from, ToAt: to})
	case organization.WorkExpiredRecordDeletions:
		rows, err = q.ListOrganizationsWithExpiredRecordDeletions(ctx, export.RecordDeletionCutoff(at))
	case organization.WorkDunningCustomers:
		rows, err = q.ListOrganizationsWithDunningCustomers(ctx)
	case organization.WorkAwaitingBillingSegments:
		rows, err = q.ListOrganizationsWithAwaitingBillingSegments(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list organizations with %s: %w", w, mapErr(err))
	}
	out := make([]core.OrganizationID, len(rows))
	for i, u := range rows {
		out[i] = idOf[core.OrganizationID](u)
	}
	return out, nil
}
