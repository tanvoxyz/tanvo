package quotes

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// logQuote records the activity log t of q, whose attributes before an
// update are before.
func (s *Service) logQuote(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, q *quote.Quote) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: q.OrganizationID, ResourceType: auditlog.ResourceQuote, ResourceID: q.ID.UUID(),
		Object: q, Before: before, CustomerID: q.CustomerID,
	})
}

// logVersion records the activity log t of version v of q: as in lago
// (app/services/utils/activity_log.rb:187-190), a version's log is on its
// quote, so that one resource carries the quote's whole timeline, and its
// object is the version.
func logVersion(ctx context.Context, d *kernel.Deps, r port.Repos, t auditlog.ActivityType, before map[string]any, q *quote.Quote, v *quote.Version) error {
	return d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: q.OrganizationID, ResourceType: auditlog.ResourceQuote, ResourceID: q.ID.UUID(),
		Object: v, Before: before, CustomerID: q.CustomerID,
	})
}

// LogOrderForm records the activity log t of order form f.
func LogOrderForm(ctx context.Context, d *kernel.Deps, r port.Repos, t auditlog.ActivityType, f *quote.OrderForm) error {
	return d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: f.OrganizationID, ResourceType: auditlog.ResourceOrderForm, ResourceID: f.ID.UUID(),
		Object: f, CustomerID: f.CustomerID,
	})
}

// LogVoided records quote.voided of version v of q, as
// QuoteVersions::VoidService does (void_service.rb:35).
func LogVoided(ctx context.Context, d *kernel.Deps, r port.Repos, q *quote.Quote, v *quote.Version) error {
	return logVersion(ctx, d, r, auditlog.ActivityQuoteVoided, nil, q, v)
}

// logOrder records the activity log t of order o.
func (s *Service) logOrder(ctx context.Context, r port.Repos, t auditlog.ActivityType, o *quote.Order) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: o.OrganizationID, ResourceType: auditlog.ResourceOrder, ResourceID: o.ID.UUID(),
		Object: o, CustomerID: o.CustomerID,
	})
}
