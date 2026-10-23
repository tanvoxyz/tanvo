package memstore

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/export"
)

// exportData holds the tables of package export.
type exportData struct {
	exports *table[core.DataExportID, export.DataExport]
	parts   *table[core.DataExportPartID, export.Part]
}

func newExportData() *exportData {
	return &exportData{
		exports: newTable[core.DataExportID](func(e export.DataExport) export.DataExport {
			e.ResourceQuery = slices.Clone(e.ResourceQuery)
			return e
		}),
		parts: newTable[core.DataExportPartID](func(p export.Part) export.Part {
			p.ObjectIDs = slices.Clone(p.ObjectIDs)
			return p
		}),
	}
}

func (d *exportData) snapshot() *exportData {
	return &exportData{exports: d.exports.snapshot(), parts: d.parts.snapshot()}
}

// dataExportRepo implements export.Repository.
type dataExportRepo struct{ d *exportData }

func (r dataExportRepo) Create(_ context.Context, e *export.DataExport) error {
	if r.d.exports.has(e.ID) {
		return conflict("data_export", "id")
	}
	r.d.exports.put(e.ID, *e)
	return nil
}

func (r dataExportRepo) Update(_ context.Context, e *export.DataExport) error {
	cur, ok := r.d.exports.get(e.ID)
	if !ok || cur.OrganizationID != e.OrganizationID {
		return notFound("data_export", e.ID)
	}
	r.d.exports.put(e.ID, *e)
	return nil
}

func (r dataExportRepo) Get(_ context.Context, org core.OrganizationID, id core.DataExportID) (*export.DataExport, error) {
	e, ok := r.d.exports.get(id)
	if !ok || e.OrganizationID != org {
		return nil, notFound("data_export", id)
	}
	return e, nil
}

func (r dataExportRepo) ListByMembership(_ context.Context, org core.OrganizationID, m core.MembershipID, page core.PageRequest) (core.Page[*export.DataExport], error) {
	rows := r.d.exports.find(func(x *export.DataExport) bool { return x.OrganizationID == org && x.MembershipID == m })
	return paginate(rows, func(x *export.DataExport) core.DataExportID { return x.ID }, page)
}

func (r dataExportRepo) ListExpired(_ context.Context, now time.Time, limit int) ([]*export.DataExport, error) {
	rows := r.d.exports.find(func(x *export.DataExport) bool {
		return x.Status == export.StatusCompleted && !x.File.IsZero() && x.Expired(now)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (r dataExportRepo) Delete(_ context.Context, org core.OrganizationID, id core.DataExportID) error {
	e, ok := r.d.exports.get(id)
	if !ok || e.OrganizationID != org {
		return notFound("data_export", id)
	}
	r.d.exports.del(id)
	r.d.parts.deleteWhere(func(x *export.Part) bool { return x.DataExportID == id })
	return nil
}

// dataExportPartRepo implements export.PartRepository.
type dataExportPartRepo struct{ d *exportData }

func (r dataExportPartRepo) Create(_ context.Context, p *export.Part) error {
	if r.d.parts.has(p.ID) {
		return conflict("data_export_part", "id")
	}
	if r.d.parts.any(func(x *export.Part) bool { return x.DataExportID == p.DataExportID && x.Index == p.Index }) {
		return conflict("data_export_part", "index")
	}
	r.d.parts.put(p.ID, *p)
	return nil
}

func (r dataExportPartRepo) Update(_ context.Context, p *export.Part) error {
	cur, ok := r.d.parts.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID {
		return notFound("data_export_part", p.ID)
	}
	r.d.parts.put(p.ID, *p)
	return nil
}

func (r dataExportPartRepo) Get(_ context.Context, org core.OrganizationID, id core.DataExportPartID) (*export.Part, error) {
	p, ok := r.d.parts.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("data_export_part", id)
	}
	return p, nil
}

func (r dataExportPartRepo) ListByExport(_ context.Context, org core.OrganizationID, exp core.DataExportID) ([]*export.Part, error) {
	rows := r.d.parts.find(func(x *export.Part) bool { return x.OrganizationID == org && x.DataExportID == exp })
	slices.SortStableFunc(rows, func(a, b *export.Part) int { return cmp.Compare(a.Index, b.Index) })
	return rows, nil
}

func (r dataExportPartRepo) NextUnprocessed(ctx context.Context, org core.OrganizationID, exp core.DataExportID) (*export.Part, error) {
	rows, _ := r.ListByExport(ctx, org, exp)
	for _, p := range rows {
		if !p.Completed {
			return p, nil
		}
	}
	return nil, notFound("data_export_part", exp)
}

var (
	_ export.Repository     = dataExportRepo{}
	_ export.PartRepository = dataExportPartRepo{}
)
