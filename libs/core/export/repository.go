package export

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Repository stores data exports.
type Repository interface {
	// Create inserts e.
	Create(ctx context.Context, e *DataExport) error
	// Update replaces the stored export e.ID with e.
	Update(ctx context.Context, e *DataExport) error
	// Get returns the export id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.DataExportID) (*DataExport, error)
	// ListByMembership returns the exports requested by a member in
	// creation order.
	ListByMembership(ctx context.Context, org core.OrganizationID, m core.MembershipID, page core.PageRequest) (core.Page[*DataExport], error)
	// ListExpired returns up to limit completed exports of every
	// organization that expired at now and still reference a file, for
	// the file cleanup job.
	ListExpired(ctx context.Context, now time.Time, limit int) ([]*DataExport, error)
	// Delete removes the export id and its parts, or fails with
	// core.ErrNotFound.
	Delete(ctx context.Context, org core.OrganizationID, id core.DataExportID) error
}

// PartRepository stores export parts.
type PartRepository interface {
	// Create inserts p; an index already used in the export fails with
	// core.ErrConflict.
	Create(ctx context.Context, p *Part) error
	// Update replaces the stored part p.ID with p.
	Update(ctx context.Context, p *Part) error
	// Get returns the part id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.DataExportPartID) (*Part, error)
	// ListByExport returns the parts of an export ordered by index.
	ListByExport(ctx context.Context, org core.OrganizationID, export core.DataExportID) ([]*Part, error)
	// NextUnprocessed returns the incomplete part of lowest index of an
	// export, or core.ErrNotFound when every part is completed.
	NextUnprocessed(ctx context.Context, org core.OrganizationID, export core.DataExportID) (*Part, error)
}
