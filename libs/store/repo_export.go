package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindExport binds the repositories of the export aggregates.
func (t *txState) bindExport(r *port.Repos) {
	r.DataExports = dataExportRepo{t}
	r.DataExportParts = dataExportPartRepo{t}
}

// dataExportRepo implements export.Repository.
type dataExportRepo struct{ t *txState }

var _ export.Repository = dataExportRepo{}

func dataExportParams(e *export.DataExport) db.CreateDataExportParams {
	return db.CreateDataExportParams{
		OrganizationID: e.OrganizationID.UUID(),
		ID:             e.ID.UUID(),
		MembershipID:   e.MembershipID.UUID(),
		Format:         string(e.Format),
		ResourceType:   string(e.ResourceType),
		ResourceQuery:  rawJSON(e.ResourceQuery),
		Status:         string(e.Status),
		File:           optString(string(e.File)),
		StartedAt:      optUTC(e.StartedAt),
		CompletedAt:    optUTC(e.CompletedAt),
		ExpiresAt:      optUTC(e.ExpiresAt),
		CreatedAt:      utc(e.CreatedAt),
		UpdatedAt:      utc(e.UpdatedAt),
	}
}

func dataExportOf(r db.DataExport) (*export.DataExport, error) {
	return &export.DataExport{
		ID:             idOf[core.DataExportID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		MembershipID:   idOf[core.MembershipID](r.MembershipID),
		Format:         export.Format(r.Format),
		ResourceType:   export.Resource(r.ResourceType),
		ResourceQuery:  rawJSON(r.ResourceQuery),
		Status:         export.Status(r.Status),
		File:           core.BlobRef(deref(r.File)),
		StartedAt:      optUTC(r.StartedAt),
		CompletedAt:    optUTC(r.CompletedAt),
		ExpiresAt:      optUTC(r.ExpiresAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r dataExportRepo) Create(ctx context.Context, e *export.DataExport) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateDataExport(ctx, dataExportParams(e))
	return insertErr("data_export", n, err)
}

func (r dataExportRepo) Update(ctx context.Context, e *export.DataExport) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateDataExport(ctx, db.UpdateDataExportParams(dataExportParams(e)))
		return err
	})
	return updateErr("data_export", e.ID, n, err)
}

func (r dataExportRepo) Get(ctx context.Context, org core.OrganizationID, id core.DataExportID) (*export.DataExport, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDataExport(ctx, db.GetDataExportParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("data_export", id, err)
	}
	return dataExportOf(row)
}

func (r dataExportRepo) ListByMembership(ctx context.Context, org core.OrganizationID, m core.MembershipID, page core.PageRequest) (core.Page[*export.DataExport], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*export.DataExport]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*export.DataExport]{}, err
	}
	rows, err := q.ListDataExportsByMembership(ctx, db.ListDataExportsByMembershipParams{
		OrganizationID: org.UUID(), MembershipID: m.UUID(), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*export.DataExport]{}, fmt.Errorf("list data exports: %w", mapErr(err))
	}
	return pageOf(rows, page, dataExportOf, func(e *export.DataExport) string { return e.ID.String() })
}

// ListExpired is not organization-scoped: the file cleanup job scans every
// organization.
func (r dataExportRepo) ListExpired(ctx context.Context, now time.Time, limit int) ([]*export.DataExport, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	lim := int32(math.MaxInt32)
	if limit > 0 && limit < math.MaxInt32 {
		lim = int32(limit)
	}
	at := now.UTC()
	rows, err := q.ListExpiredDataExports(ctx, db.ListExpiredDataExportsParams{Now: &at, Lim: lim})
	if err != nil {
		return nil, fmt.Errorf("list expired data exports: %w", mapErr(err))
	}
	return convAll(rows, dataExportOf)
}

// Delete removes the export; its parts go with it (ON DELETE CASCADE).
func (r dataExportRepo) Delete(ctx context.Context, org core.OrganizationID, id core.DataExportID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteDataExport(ctx, db.DeleteDataExportParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("data_export", id, n, err)
}

// dataExportPartRepo implements export.PartRepository.
type dataExportPartRepo struct{ t *txState }

var _ export.PartRepository = dataExportPartRepo{}

func dataExportPartParams(p *export.Part) db.CreateDataExportPartParams {
	return db.CreateDataExportPartParams{
		OrganizationID: p.OrganizationID.UUID(),
		ID:             p.ID.UUID(),
		DataExportID:   p.DataExportID.UUID(),
		Index:          int32(p.Index), // batch indexes are small
		ObjectIds:      append([]uuid.UUID{}, p.ObjectIDs...),
		Completed:      p.Completed,
		CsvLines:       p.CSVLines,
		CreatedAt:      utc(p.CreatedAt),
		UpdatedAt:      utc(p.UpdatedAt),
	}
}

func dataExportPartOf(r db.DataExportPart) (*export.Part, error) {
	return &export.Part{
		ID:             idOf[core.DataExportPartID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		DataExportID:   idOf[core.DataExportID](r.DataExportID),
		Index:          int(r.Index),
		ObjectIDs:      r.ObjectIds,
		Completed:      r.Completed,
		CSVLines:       r.CsvLines,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r dataExportPartRepo) Create(ctx context.Context, p *export.Part) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateDataExportPart(ctx, dataExportPartParams(p))
	return insertErr("data_export_part", n, err)
}

func (r dataExportPartRepo) Update(ctx context.Context, p *export.Part) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateDataExportPart(ctx, db.UpdateDataExportPartParams(dataExportPartParams(p)))
		return err
	})
	return updateErr("data_export_part", p.ID, n, err)
}

func (r dataExportPartRepo) Get(ctx context.Context, org core.OrganizationID, id core.DataExportPartID) (*export.Part, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDataExportPart(ctx, db.GetDataExportPartParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("data_export_part", id, err)
	}
	return dataExportPartOf(row)
}

func (r dataExportPartRepo) ListByExport(ctx context.Context, org core.OrganizationID, exp core.DataExportID) ([]*export.Part, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListDataExportPartsByExport(ctx, db.ListDataExportPartsByExportParams{OrganizationID: org.UUID(), DataExportID: exp.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list parts of data export %s: %w", exp, mapErr(err))
	}
	return convAll(rows, dataExportPartOf)
}

func (r dataExportPartRepo) NextUnprocessed(ctx context.Context, org core.OrganizationID, exp core.DataExportID) (*export.Part, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.NextUnprocessedDataExportPart(ctx, db.NextUnprocessedDataExportPartParams{OrganizationID: org.UUID(), DataExportID: exp.UUID()})
	if err != nil {
		return nil, getErr("data_export_part", "next unprocessed of "+exp.String(), err)
	}
	return dataExportPartOf(row)
}
