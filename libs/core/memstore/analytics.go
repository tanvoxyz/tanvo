package memstore

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
)

// errAnalytics reports that the reports are only computed by the
// PostgreSQL store, whose SQL they are.
var errAnalytics = errors.New("memstore: analytics reports need the PostgreSQL store")

// analyticsRepo implements analytics.Repository by refusing every report.
type analyticsRepo struct{}

var _ analytics.Repository = analyticsRepo{}

func (analyticsRepo) GrossRevenue(context.Context, core.OrganizationID, analytics.Scope) ([]analytics.Revenue, error) {
	return nil, errAnalytics
}

func (analyticsRepo) MRR(context.Context, core.OrganizationID, analytics.Scope) ([]analytics.Revenue, error) {
	return nil, errAnalytics
}

func (analyticsRepo) InvoiceCollection(context.Context, core.OrganizationID, analytics.Scope) ([]analytics.Collection, error) {
	return nil, errAnalytics
}

func (analyticsRepo) InvoicedUsage(context.Context, core.OrganizationID, analytics.Scope) ([]analytics.Usage, error) {
	return nil, errAnalytics
}

func (analyticsRepo) OverdueBalance(context.Context, core.OrganizationID, analytics.Scope) ([]analytics.Overdue, error) {
	return nil, errAnalytics
}
