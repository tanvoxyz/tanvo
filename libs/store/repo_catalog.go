package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindCatalog binds the repositories of the catalog aggregates: billable
// metrics, plans, charges, fixed charges, add-ons and pricing units.
func (t *txState) bindCatalog(r *port.Repos) {
	r.Metrics = metricRepo{t}
	r.Plans = planRepo{t}
	r.Charges = chargeRepo{t}
	r.FixedCharges = fixedChargeRepo{t}
	r.AddOns = addOnRepo{t}
	r.PricingUnits = pricingUnitRepo{t}
}

// catalogInOrder returns items in the order of keys, failing with
// core.ErrNotFound naming entity for the first key without an item.
func catalogInOrder[K comparable, T any](entity string, keys []K, items []T, key func(T) K) ([]T, error) {
	by := make(map[K]T, len(items))
	for _, it := range items {
		by[key(it)] = it
	}
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		it, ok := by[k]
		if !ok {
			return nil, notFound(entity, k)
		}
		out = append(out, it)
	}
	return out, nil
}

// catalogConv converts every row with conv, which cannot fail.
func catalogConv[R, T any](rows []R, conv func(R) T) []T {
	out := make([]T, len(rows))
	for i, r := range rows {
		out[i] = conv(r)
	}
	return out
}

// ---------------------------------------------------------------------------
// Pricing units.

// pricingUnitRepo implements catalog.PricingUnitRepository.
type pricingUnitRepo struct{ t *txState }

var _ catalog.PricingUnitRepository = pricingUnitRepo{}

func pricingUnitParams(p *catalog.PricingUnit) db.CreatePricingUnitParams {
	return db.CreatePricingUnitParams{
		OrganizationID: p.OrganizationID.UUID(),
		ID:             p.ID.UUID(),
		Code:           p.Code,
		Name:           p.Name,
		ShortName:      p.ShortName,
		Description:    p.Description,
		CreatedAt:      utc(p.CreatedAt),
		UpdatedAt:      utc(p.UpdatedAt),
	}
}

func pricingUnitOf(r db.PricingUnit) (*catalog.PricingUnit, error) {
	return &catalog.PricingUnit{
		ID:             idOf[core.PricingUnitID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Code:           r.Code,
		Name:           r.Name,
		ShortName:      r.ShortName,
		Description:    r.Description,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r pricingUnitRepo) Create(ctx context.Context, p *catalog.PricingUnit) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePricingUnit(ctx, pricingUnitParams(p))
	return insertErr("pricing_unit", n, err)
}

func (r pricingUnitRepo) Update(ctx context.Context, p *catalog.PricingUnit) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePricingUnit(ctx, db.UpdatePricingUnitParams(pricingUnitParams(p)))
		return err
	})
	return updateErr("pricing_unit", p.ID, n, err)
}

func (r pricingUnitRepo) Get(ctx context.Context, org core.OrganizationID, id core.PricingUnitID) (*catalog.PricingUnit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPricingUnit(ctx, db.GetPricingUnitParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("pricing_unit", id, err)
	}
	return pricingUnitOf(row)
}

func (r pricingUnitRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.PricingUnit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPricingUnitByCode(ctx, db.GetPricingUnitByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("pricing_unit", code, err)
	}
	return pricingUnitOf(row)
}

func (r pricingUnitRepo) List(ctx context.Context, org core.OrganizationID, f catalog.PricingUnitFilter, page core.PageRequest) (core.Page[*catalog.PricingUnit], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.PricingUnit]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*catalog.PricingUnit]{}, fmt.Errorf("list pricing units: %w: order", core.ErrInvalidValue)
	}
	byName := f.Order == core.OrderRecent
	var (
		after     *uuid.UUID
		afterName *string
		afterAt   *time.Time
		lim       int32
	)
	if byName {
		after, afterName, afterAt, lim, err = nameCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*catalog.PricingUnit]{}, err
	}
	rows, err := q.ListPricingUnits(ctx, db.ListPricingUnitsParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search),
		After: after, ByName: byName, AfterName: afterName, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.PricingUnit]{}, fmt.Errorf("list pricing units: %w", mapErr(err))
	}
	return pageOf(rows, page, pricingUnitOf, func(p *catalog.PricingUnit) string {
		if byName {
			return nameToken(p.Name, p.CreatedAt, p.ID.UUID())
		}
		return p.ID.String()
	})
}

// ---------------------------------------------------------------------------
// Billable metrics.

// metricRepo implements catalog.MetricRepository.
type metricRepo struct{ t *txState }

var _ catalog.MetricRepository = metricRepo{}

func metricParams(m *catalog.BillableMetric) db.CreateBillableMetricParams {
	return db.CreateBillableMetricParams{
		OrganizationID:    m.OrganizationID.UUID(),
		ID:                m.ID.UUID(),
		Code:              m.Code,
		Name:              m.Name,
		Description:       m.Description,
		AggregationType:   string(m.AggregationType),
		FieldName:         m.FieldName,
		WeightedInterval:  optString(string(m.WeightedInterval)),
		Recurring:         m.Recurring,
		Expression:        m.Expression,
		RoundingFunction:  optString(string(m.RoundingFunction)),
		RoundingPrecision: optInt32(m.RoundingPrecision),
		CreatedAt:         utc(m.CreatedAt),
		UpdatedAt:         utc(m.UpdatedAt),
	}
}

func metricOf(r db.BillableMetric, filters []catalog.MetricFilter) *catalog.BillableMetric {
	return &catalog.BillableMetric{
		ID:                idOf[core.BillableMetricID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		Code:              r.Code,
		Name:              r.Name,
		Description:       r.Description,
		AggregationType:   catalog.AggregationType(r.AggregationType),
		FieldName:         r.FieldName,
		WeightedInterval:  catalog.WeightedInterval(deref(r.WeightedInterval)),
		Recurring:         r.Recurring,
		Expression:        r.Expression,
		RoundingFunction:  catalog.RoundingFunction(deref(r.RoundingFunction)),
		RoundingPrecision: optInt(r.RoundingPrecision),
		Filters:           filters,
		CreatedAt:         utc(r.CreatedAt),
		UpdatedAt:         utc(r.UpdatedAt),
		DeletedAt:         optUTC(r.DeletedAt),
	}
}

// metrics attaches their filters to rows.
func (r metricRepo) metrics(ctx context.Context, q *db.Queries, org core.OrganizationID, rows []db.BillableMetric) ([]*catalog.BillableMetric, error) {
	out := make([]*catalog.BillableMetric, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	frows, err := q.ListBillableMetricFilters(ctx, db.ListBillableMetricFiltersParams{OrganizationID: org.UUID(), BillableMetricIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list billable metric filters: %w", mapErr(err))
	}
	filters := map[uuid.UUID][]catalog.MetricFilter{}
	for _, f := range frows {
		filters[f.BillableMetricID] = append(filters[f.BillableMetricID], catalog.MetricFilter{
			ID:     idOf[core.BillableMetricFilterID](f.ID),
			Key:    f.Key,
			Values: f.FilterValues,
		})
	}
	for _, row := range rows {
		out = append(out, metricOf(row, filters[row.ID]))
	}
	return out, nil
}

func (r metricRepo) one(ctx context.Context, q *db.Queries, org core.OrganizationID, row db.BillableMetric) (*catalog.BillableMetric, error) {
	ms, err := r.metrics(ctx, q, org, []db.BillableMetric{row})
	if err != nil {
		return nil, err
	}
	return ms[0], nil
}

// writeFilters (re)writes the filters of m. On replace, the kept filters
// m no longer has are discarded at m.UpdatedAt and the others updated in
// place, so their IDs stay (lago CreateOrUpdateBatchService).
func (r metricRepo) writeFilters(ctx context.Context, q *db.Queries, m *catalog.BillableMetric, replace bool) error {
	org, id := m.OrganizationID.UUID(), m.ID.UUID()
	if replace {
		kept := make([]uuid.UUID, len(m.Filters))
		for i, f := range m.Filters {
			kept[i] = f.ID.UUID()
		}
		at := utc(m.UpdatedAt)
		if _, err := q.DiscardBillableMetricFilters(ctx, db.DiscardBillableMetricFiltersParams{
			OrganizationID: org, BillableMetricID: id, DeletedAt: &at, KeptIds: kept,
		}); err != nil {
			return err
		}
	}
	for i, f := range m.Filters {
		values := f.Values
		if values == nil {
			values = []string{}
		}
		if err := q.UpsertBillableMetricFilter(ctx, db.UpsertBillableMetricFilterParams{
			OrganizationID: org, ID: f.ID.UUID(), BillableMetricID: id, Key: f.Key, FilterValues: values, Position: int32(i), //nolint:gosec // few filters
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r metricRepo) Create(ctx context.Context, m *catalog.BillableMetric) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreateBillableMetric(ctx, metricParams(m))
		if err != nil || n == 0 {
			return err
		}
		return r.writeFilters(ctx, q, m, false)
	})
	return r.t.noteCatalog(insertErr("billable_metric", n, err), CatalogChange{OrganizationID: m.OrganizationID, Kind: CatalogMetric, Key: m.Code})
}

func (r metricRepo) Update(ctx context.Context, m *catalog.BillableMetric) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateBillableMetric(ctx, db.UpdateBillableMetricParams(metricParams(m)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeFilters(ctx, q, m, true)
	})
	// The previous code is unknown here: every metric is evicted.
	return r.t.noteCatalog(updateErr("billable_metric", m.ID, n, err), CatalogChange{OrganizationID: m.OrganizationID, Kind: CatalogMetric})
}

func (r metricRepo) Discard(ctx context.Context, org core.OrganizationID, id core.BillableMetricID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	at = utc(at)
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DiscardBillableMetric(ctx, db.DiscardBillableMetricParams{OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: &at})
		return err
	})
	return r.t.noteCatalog(updateErr("billable_metric", id, n, err), CatalogChange{OrganizationID: org, Kind: CatalogMetric})
}

func (r metricRepo) DiscardFilters(ctx context.Context, org core.OrganizationID, id core.BillableMetricID, at time.Time) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	at = utc(at)
	n, err := q.DiscardBillableMetricFilters(ctx, db.DiscardBillableMetricFiltersParams{
		OrganizationID: org.UUID(), BillableMetricID: id.UUID(), DeletedAt: &at, KeptIds: []uuid.UUID{},
	})
	if err != nil {
		return 0, fmt.Errorf("discard billable metric filters: %w", mapErr(err))
	}
	return int(n), r.t.noteCatalog(nil, CatalogChange{OrganizationID: org, Kind: CatalogMetric})
}

func (r metricRepo) Get(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetBillableMetric(ctx, db.GetBillableMetricParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("billable_metric", id, err)
	}
	return r.one(ctx, q, org, row)
}

func (r metricRepo) GetWithDiscarded(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetBillableMetricWithDiscarded(ctx, db.GetBillableMetricWithDiscardedParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("billable_metric", id, err)
	}
	return r.one(ctx, q, org, row)
}

func (r metricRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetBillableMetricByCode(ctx, db.GetBillableMetricByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("billable_metric", code, err)
	}
	return r.one(ctx, q, org, row)
}

func (r metricRepo) ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.BillableMetricID) ([]*catalog.BillableMetric, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBillableMetricsByIDs(ctx, db.ListBillableMetricsByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("list billable metrics: %w", mapErr(err))
	}
	ms, err := r.metrics(ctx, q, org, rows)
	if err != nil {
		return nil, err
	}
	return catalogInOrder("billable_metric", ids, ms, func(m *catalog.BillableMetric) core.BillableMetricID { return m.ID })
}

func (r metricRepo) ListByCodes(ctx context.Context, org core.OrganizationID, codes []string) ([]*catalog.BillableMetric, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	if codes == nil {
		codes = []string{}
	}
	rows, err := q.ListBillableMetricsByCodes(ctx, db.ListBillableMetricsByCodesParams{OrganizationID: org.UUID(), Codes: codes})
	if err != nil {
		return nil, fmt.Errorf("list billable metrics: %w", mapErr(err))
	}
	ms, err := r.metrics(ctx, q, org, rows)
	if err != nil {
		return nil, err
	}
	return catalogInOrder("billable_metric", codes, ms, func(m *catalog.BillableMetric) string { return m.Code })
}

func (r metricRepo) InUse(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	used, err := q.BillableMetricInUse(ctx, db.BillableMetricInUseParams{OrganizationID: org.UUID(), BillableMetricID: id.UUID()})
	if err != nil {
		return false, fmt.Errorf("billable metric %s in use: %w", id, mapErr(err))
	}
	return used, nil
}

func (r metricRepo) List(ctx context.Context, org core.OrganizationID, f catalog.MetricListFilter, page core.PageRequest) (core.Page[*catalog.BillableMetric], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.BillableMetric]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*catalog.BillableMetric]{}, fmt.Errorf("list billable metrics: %w: order", core.ErrInvalidValue)
	}
	recent := f.Order == core.OrderRecent
	var (
		after   *uuid.UUID
		afterAt *time.Time
		lim     int32
	)
	if recent {
		after, afterAt, lim, err = timeCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*catalog.BillableMetric]{}, err
	}
	rows, err := q.ListBillableMetrics(ctx, db.ListBillableMetricsParams{
		OrganizationID:   org.UUID(),
		AggregationTypes: enumStrings(f.AggregationTypes),
		Recurring:        f.Recurring,
		PlanID:           optUUID(f.PlanID),
		Search:           searchArg(f.Search),
		After:            after,
		Recent:           recent,
		AfterAt:          afterAt,
		Lim:              lim,
	})
	if err != nil {
		return core.Page[*catalog.BillableMetric]{}, fmt.Errorf("list billable metrics: %w", mapErr(err))
	}
	// The extra row that tells whether a next page exists gets its filters
	// too; pageOf drops it.
	ms, err := r.metrics(ctx, q, org, rows)
	if err != nil {
		return core.Page[*catalog.BillableMetric]{}, err
	}
	return pageOf(ms, page,
		func(m *catalog.BillableMetric) (*catalog.BillableMetric, error) { return m, nil },
		func(m *catalog.BillableMetric) string {
			if recent {
				return core.Cursor{Key: m.CreatedAt.UTC().Format(time.RFC3339Nano), ID: m.ID.UUID()}.Encode()
			}
			return m.ID.String()
		})
}

// ---------------------------------------------------------------------------
// Plans.

// planRepo implements catalog.PlanRepository.
type planRepo struct{ t *txState }

var _ catalog.PlanRepository = planRepo{}

// catalogPlanRow is the common shape of the plan reads.
type catalogPlanRow struct {
	Plan   db.Plan
	TaxIds []uuid.UUID
}

func planParams(p *catalog.Plan) db.CreatePlanParams {
	return db.CreatePlanParams{
		OrganizationID:          p.OrganizationID.UUID(),
		ID:                      p.ID.UUID(),
		Code:                    p.Code,
		Name:                    p.Name,
		InvoiceDisplayName:      p.InvoiceDisplayName,
		Description:             p.Description,
		BillingInterval:         p.Interval.String(),
		Amount:                  p.Amount,
		Currency:                p.Currency.Code(),
		PayInAdvance:            p.PayInAdvance,
		TrialPeriodDays:         int32(p.TrialPeriodDays), //nolint:gosec // validated small
		BillChargesMonthly:      p.BillChargesMonthly,
		BillFixedChargesMonthly: p.BillFixedChargesMonthly,
		ParentID:                optUUID(p.ParentID),
		Metadata:                encodeItemMetadata(p.Metadata),
		Status:                  string(p.Status),
		CreatedAt:               utc(p.CreatedAt),
		UpdatedAt:               utc(p.UpdatedAt),
	}
}

func planOf(row catalogPlanRow) (*catalog.Plan, error) {
	r := row.Plan
	interval, err := period.ParseInterval(r.BillingInterval)
	if err != nil {
		return nil, fmt.Errorf("plan %s: %w", r.ID, err)
	}
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("plan %s: %w", r.ID, err)
	}
	md, err := decodeItemMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("plan %s: %w", r.ID, err)
	}
	return &catalog.Plan{
		ID:                      idOf[core.PlanID](r.ID),
		OrganizationID:          idOf[core.OrganizationID](r.OrganizationID),
		Code:                    r.Code,
		Name:                    r.Name,
		InvoiceDisplayName:      r.InvoiceDisplayName,
		Description:             r.Description,
		Interval:                interval,
		Amount:                  r.Amount,
		Currency:                cur,
		PayInAdvance:            r.PayInAdvance,
		TrialPeriodDays:         int(r.TrialPeriodDays),
		BillChargesMonthly:      r.BillChargesMonthly,
		BillFixedChargesMonthly: r.BillFixedChargesMonthly,
		ParentID:                optIDOf[core.PlanID](r.ParentID),
		TaxIDs:                  idsOf[core.TaxID](row.TaxIds),
		Metadata:                md,
		Status:                  catalog.PlanStatus(r.Status),
		CreatedAt:               utc(r.CreatedAt),
		UpdatedAt:               utc(r.UpdatedAt),
	}, nil
}

func (r planRepo) writeTaxes(ctx context.Context, q *db.Queries, p *catalog.Plan, replace bool) error {
	org, id := p.OrganizationID.UUID(), p.ID.UUID()
	if replace {
		if err := q.DeletePlanTaxes(ctx, db.DeletePlanTaxesParams{OrganizationID: org, PlanID: id}); err != nil {
			return err
		}
	}
	if len(p.TaxIDs) == 0 {
		return nil
	}
	return q.InsertPlanTaxes(ctx, db.InsertPlanTaxesParams{OrganizationID: org, PlanID: id, TaxIds: uuids(p.TaxIDs)})
}

func (r planRepo) Create(ctx context.Context, p *catalog.Plan) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreatePlan(ctx, planParams(p))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, p, false)
	})
	return insertErr("plan", n, err)
}

func (r planRepo) Update(ctx context.Context, p *catalog.Plan) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePlan(ctx, db.UpdatePlanParams(planParams(p)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, p, true)
	})
	return updateErr("plan", p.ID, n, err)
}

func (r planRepo) Get(ctx context.Context, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPlan(ctx, db.GetPlanParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("plan", id, err)
	}
	return planOf(catalogPlanRow(row))
}

func (r planRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.Plan, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPlanByCode(ctx, db.GetPlanByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("plan", code, err)
	}
	return planOf(catalogPlanRow(row))
}

func (r planRepo) ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.PlanID) ([]*catalog.Plan, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPlansByIDs(ctx, db.ListPlansByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", mapErr(err))
	}
	ps, err := convAll(rows, func(r db.ListPlansByIDsRow) (*catalog.Plan, error) { return planOf(catalogPlanRow(r)) })
	if err != nil {
		return nil, err
	}
	return catalogInOrder("plan", ids, ps, func(p *catalog.Plan) core.PlanID { return p.ID })
}

func (r planRepo) ListChildren(ctx context.Context, org core.OrganizationID, parent core.PlanID) ([]*catalog.Plan, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	pid := parent.UUID()
	rows, err := q.ListPlanChildren(ctx, db.ListPlanChildrenParams{OrganizationID: org.UUID(), ParentID: &pid})
	if err != nil {
		return nil, fmt.Errorf("list plan children: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListPlanChildrenRow) (*catalog.Plan, error) { return planOf(catalogPlanRow(r)) })
}

func (r planRepo) List(ctx context.Context, org core.OrganizationID, f catalog.PlanListFilter, page core.PageRequest) (core.Page[*catalog.Plan], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.Plan]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*catalog.Plan]{}, fmt.Errorf("list plans: %w: order", core.ErrInvalidValue)
	}
	byName := f.Order == core.OrderRecent
	var (
		after     *uuid.UUID
		afterName *string
		afterAt   *time.Time
		lim       int32
	)
	if byName {
		after, afterName, afterAt, lim, err = nameCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*catalog.Plan]{}, err
	}
	rows, err := q.ListPlans(ctx, db.ListPlansParams{
		OrganizationID: org.UUID(), IncludeChildren: f.IncludeChildren, Statuses: enumStrings(f.Statuses),
		Search: searchArg(f.Search), After: after, ByName: byName, AfterName: afterName, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.Plan]{}, fmt.Errorf("list plans: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListPlansRow) (*catalog.Plan, error) { return planOf(catalogPlanRow(r)) },
		func(p *catalog.Plan) string {
			if byName {
				return nameToken(p.Name, p.CreatedAt, p.ID.UUID())
			}
			return p.ID.String()
		})
}

// ---------------------------------------------------------------------------
// Add-ons.

// addOnRepo implements catalog.AddOnRepository.
type addOnRepo struct{ t *txState }

var _ catalog.AddOnRepository = addOnRepo{}

// catalogAddOnRow is the common shape of the add-on reads.
type catalogAddOnRow struct {
	AddOn  db.AddOn
	TaxIds []uuid.UUID
}

func addOnParams(a *catalog.AddOn) db.CreateAddOnParams {
	return db.CreateAddOnParams{
		OrganizationID:     a.OrganizationID.UUID(),
		ID:                 a.ID.UUID(),
		Code:               a.Code,
		Name:               a.Name,
		InvoiceDisplayName: a.InvoiceDisplayName,
		Description:        a.Description,
		Amount:             a.Amount,
		Currency:           a.Currency.Code(),
		CreatedAt:          utc(a.CreatedAt),
		UpdatedAt:          utc(a.UpdatedAt),
	}
}

func addOnOf(row catalogAddOnRow) (*catalog.AddOn, error) {
	r := row.AddOn
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("add_on %s: %w", r.ID, err)
	}
	return &catalog.AddOn{
		ID:                 idOf[core.AddOnID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		Code:               r.Code,
		Name:               r.Name,
		InvoiceDisplayName: r.InvoiceDisplayName,
		Description:        r.Description,
		Amount:             r.Amount,
		Currency:           cur,
		TaxIDs:             idsOf[core.TaxID](row.TaxIds),
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

func (r addOnRepo) writeTaxes(ctx context.Context, q *db.Queries, a *catalog.AddOn, replace bool) error {
	org, id := a.OrganizationID.UUID(), a.ID.UUID()
	if replace {
		if err := q.DeleteAddOnTaxes(ctx, db.DeleteAddOnTaxesParams{OrganizationID: org, AddOnID: id}); err != nil {
			return err
		}
	}
	if len(a.TaxIDs) == 0 {
		return nil
	}
	return q.InsertAddOnTaxes(ctx, db.InsertAddOnTaxesParams{OrganizationID: org, AddOnID: id, TaxIds: uuids(a.TaxIDs)})
}

func (r addOnRepo) Create(ctx context.Context, a *catalog.AddOn) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreateAddOn(ctx, addOnParams(a))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, a, false)
	})
	return insertErr("add_on", n, err)
}

func (r addOnRepo) Update(ctx context.Context, a *catalog.AddOn) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateAddOn(ctx, db.UpdateAddOnParams(addOnParams(a)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, a, true)
	})
	return updateErr("add_on", a.ID, n, err)
}

func (r addOnRepo) Discard(ctx context.Context, org core.OrganizationID, id core.AddOnID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	at = utc(at)
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DiscardAddOn(ctx, db.DiscardAddOnParams{OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: &at})
		return err
	})
	return updateErr("add_on", id, n, err)
}

func (r addOnRepo) Get(ctx context.Context, org core.OrganizationID, id core.AddOnID) (*catalog.AddOn, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAddOn(ctx, db.GetAddOnParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("add_on", id, err)
	}
	return addOnOf(catalogAddOnRow(row))
}

func (r addOnRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.AddOn, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAddOnByCode(ctx, db.GetAddOnByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("add_on", code, err)
	}
	return addOnOf(catalogAddOnRow(row))
}

func (r addOnRepo) ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.AddOnID) ([]*catalog.AddOn, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAddOnsByIDs(ctx, db.ListAddOnsByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("list add-ons: %w", mapErr(err))
	}
	as, err := convAll(rows, func(r db.ListAddOnsByIDsRow) (*catalog.AddOn, error) { return addOnOf(catalogAddOnRow(r)) })
	if err != nil {
		return nil, err
	}
	return catalogInOrder("add_on", ids, as, func(a *catalog.AddOn) core.AddOnID { return a.ID })
}

func (r addOnRepo) List(ctx context.Context, org core.OrganizationID, f catalog.AddOnFilter, page core.PageRequest) (core.Page[*catalog.AddOn], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*catalog.AddOn]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*catalog.AddOn]{}, fmt.Errorf("list add-ons: %w: order", core.ErrInvalidValue)
	}
	recent := f.Order == core.OrderRecent
	var (
		after   *uuid.UUID
		afterAt *time.Time
		lim     int32
	)
	if recent {
		after, afterAt, lim, err = timeCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*catalog.AddOn]{}, err
	}
	rows, err := q.ListAddOns(ctx, db.ListAddOnsParams{
		OrganizationID: org.UUID(), Search: searchArg(f.Search), After: after, Recent: recent, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*catalog.AddOn]{}, fmt.Errorf("list add-ons: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListAddOnsRow) (*catalog.AddOn, error) { return addOnOf(catalogAddOnRow(r)) },
		func(a *catalog.AddOn) string {
			if recent {
				return core.Cursor{Key: a.CreatedAt.UTC().Format(time.RFC3339Nano), ID: a.ID.UUID()}.Encode()
			}
			return a.ID.String()
		})
}

// ---------------------------------------------------------------------------
// Charges.

// chargeRepo implements catalog.ChargeRepository.
type chargeRepo struct{ t *txState }

var _ catalog.ChargeRepository = chargeRepo{}

// catalogChargeRow is the common shape of the charge reads.
type catalogChargeRow struct {
	Charge db.Charge
	TaxIds []uuid.UUID
}

func chargeParams(c *catalog.Charge) db.CreateChargeParams {
	p := db.CreateChargeParams{
		OrganizationID:      c.OrganizationID.UUID(),
		ID:                  c.ID.UUID(),
		PlanID:              c.PlanID.UUID(),
		ParentID:            optUUID(c.ParentID),
		Code:                c.Code,
		BillableMetricID:    c.BillableMetricID.UUID(),
		ChargeModel:         string(c.ChargeModel),
		Properties:          c.Properties.Raw(),
		PayInAdvance:        c.PayInAdvance,
		Invoiceable:         c.Invoiceable,
		RegroupPaidFees:     optString(string(c.RegroupPaidFees)),
		Prorated:            c.Prorated,
		AcceptsTargetWallet: c.AcceptsTargetWallet,
		MinAmount:           c.MinAmount,
		InvoiceDisplayName:  c.InvoiceDisplayName,
		CreatedAt:           utc(c.CreatedAt),
		UpdatedAt:           utc(c.UpdatedAt),
	}
	if a := c.AppliedPricingUnit; a != nil {
		p.AppliedPricingUnitID = optUUID(a.PricingUnitID)
		rate := a.ConversionRate
		p.ConversionRate = &rate
	}
	return p
}

func chargeOf(row catalogChargeRow, filters []catalog.ChargeFilter) (*catalog.Charge, error) {
	r := row.Charge
	props, err := catalog.NewProperties(r.Properties)
	if err != nil {
		return nil, fmt.Errorf("charge %s: %w", r.ID, err)
	}
	c := &catalog.Charge{
		ID:                  idOf[core.ChargeID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		PlanID:              idOf[core.PlanID](r.PlanID),
		ParentID:            optIDOf[core.ChargeID](r.ParentID),
		Code:                r.Code,
		BillableMetricID:    idOf[core.BillableMetricID](r.BillableMetricID),
		ChargeModel:         catalog.ChargeModel(r.ChargeModel),
		Properties:          props,
		Filters:             filters,
		PayInAdvance:        r.PayInAdvance,
		Invoiceable:         r.Invoiceable,
		RegroupPaidFees:     catalog.RegroupPaidFees(deref(r.RegroupPaidFees)),
		Prorated:            r.Prorated,
		AcceptsTargetWallet: r.AcceptsTargetWallet,
		MinAmount:           r.MinAmount,
		InvoiceDisplayName:  r.InvoiceDisplayName,
		TaxIDs:              idsOf[core.TaxID](row.TaxIds),
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
		DeletedAt:           optUTC(r.DeletedAt),
	}
	if r.AppliedPricingUnitID != nil {
		if r.ConversionRate == nil {
			return nil, fmt.Errorf("charge %s: pricing unit without a conversion rate", r.ID)
		}
		rate := *r.ConversionRate
		c.AppliedPricingUnit = &catalog.AppliedPricingUnit{
			PricingUnitID:  idOf[core.PricingUnitID](*r.AppliedPricingUnitID),
			ConversionRate: rate,
		}
	}
	return c, nil
}

func chargeFilterOf(r db.ChargeFilter) (*catalog.ChargeFilter, error) {
	props, err := catalog.NewProperties(r.Properties)
	if err != nil {
		return nil, fmt.Errorf("charge filter %s: %w", r.ID, err)
	}
	var values map[string][]string
	if err := decodeJSON(r.FilterValues, &values); err != nil {
		return nil, fmt.Errorf("charge filter %s: %w", r.ID, err)
	}
	return &catalog.ChargeFilter{
		ID:                 idOf[core.ChargeFilterID](r.ID),
		ChargeID:           idOf[core.ChargeID](r.ChargeID),
		Values:             values,
		Properties:         props,
		InvoiceDisplayName: r.InvoiceDisplayName,
		Code:               r.Code,
		CreatedAt:          utc(r.CreatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

// charges attaches their filters to rows.
func (r chargeRepo) charges(ctx context.Context, q *db.Queries, org core.OrganizationID, rows []catalogChargeRow) ([]*catalog.Charge, error) {
	out := make([]*catalog.Charge, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.Charge.ID
	}
	frows, err := q.ListChargeFiltersByCharges(ctx, db.ListChargeFiltersByChargesParams{OrganizationID: org.UUID(), ChargeIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list charge filters: %w", mapErr(err))
	}
	filters := map[uuid.UUID][]catalog.ChargeFilter{}
	for _, fr := range frows {
		f, err := chargeFilterOf(fr)
		if err != nil {
			return nil, err
		}
		filters[fr.ChargeID] = append(filters[fr.ChargeID], *f)
	}
	for _, row := range rows {
		c, err := chargeOf(row, filters[row.Charge.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (r chargeRepo) one(ctx context.Context, q *db.Queries, org core.OrganizationID, row catalogChargeRow) (*catalog.Charge, error) {
	cs, err := r.charges(ctx, q, org, []catalogChargeRow{row})
	if err != nil {
		return nil, err
	}
	return cs[0], nil
}

// writeChildren (re)writes the filters and taxes of c; on replace, the
// kept filters missing from c.Filters are discarded at c.UpdatedAt first.
func (r chargeRepo) writeChildren(ctx context.Context, q *db.Queries, c *catalog.Charge, replace bool) error {
	org, id := c.OrganizationID.UUID(), c.ID.UUID()
	if replace {
		at := utc(c.UpdatedAt)
		keep := make([]uuid.UUID, len(c.Filters))
		for i := range c.Filters {
			keep[i] = c.Filters[i].ID.UUID()
		}
		if err := q.DiscardChargeFiltersExcept(ctx, db.DiscardChargeFiltersExceptParams{
			OrganizationID: org, ChargeID: id, KeepIds: keep, DeletedAt: &at,
		}); err != nil {
			return err
		}
		if err := q.DeleteChargeTaxes(ctx, db.DeleteChargeTaxesParams{OrganizationID: org, ChargeID: id}); err != nil {
			return err
		}
	}
	for i := range c.Filters {
		f := &c.Filters[i]
		values := f.Values
		if values == nil {
			values = map[string][]string{}
		}
		raw, err := encodeJSON(values)
		if err != nil {
			return err
		}
		if err := q.UpsertChargeFilter(ctx, db.UpsertChargeFilterParams{
			OrganizationID:     org,
			ID:                 f.ID.UUID(),
			ChargeID:           id,
			FilterValues:       raw,
			Properties:         f.Properties.Raw(),
			InvoiceDisplayName: f.InvoiceDisplayName,
			Code:               f.Code,
			Position:           int32(i), //nolint:gosec // few filters
			CreatedAt:          utc(f.CreatedAt),
		}); err != nil {
			return err
		}
	}
	if len(c.TaxIDs) > 0 {
		return q.InsertChargeTaxes(ctx, db.InsertChargeTaxesParams{OrganizationID: org, ChargeID: id, TaxIds: uuids(c.TaxIDs)})
	}
	return nil
}

func (r chargeRepo) Create(ctx context.Context, c *catalog.Charge) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreateCharge(ctx, chargeParams(c))
		if err != nil || n == 0 {
			return err
		}
		return r.writeChildren(ctx, q, c, false)
	})
	return r.t.noteCatalog(insertErr("charge", n, err), CatalogChange{OrganizationID: c.OrganizationID, Kind: CatalogPlan, Key: c.PlanID.String()})
}

func (r chargeRepo) Update(ctx context.Context, c *catalog.Charge) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateCharge(ctx, db.UpdateChargeParams(chargeParams(c)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeChildren(ctx, q, c, true)
	})
	return r.t.noteCatalog(updateErr("charge", c.ID, n, err), CatalogChange{OrganizationID: c.OrganizationID, Kind: CatalogPlan, Key: c.PlanID.String()})
}

// discard discards the kept charges selected by sel, with their kept
// filters, and returns how many charges.
func (r chargeRepo) discard(ctx context.Context, org core.OrganizationID, sel db.DiscardChargesParams, at time.Time) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	at = utc(at)
	sel.OrganizationID, sel.DeletedAt = org.UUID(), &at
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		if err := q.DiscardChargeFiltersOfCharges(ctx, db.DiscardChargeFiltersOfChargesParams{
			OrganizationID: sel.OrganizationID, DeletedAt: sel.DeletedAt,
			ID: sel.ID, PlanID: sel.PlanID, BillableMetricID: sel.BillableMetricID,
		}); err != nil {
			return err
		}
		n, err = q.DiscardCharges(ctx, sel)
		return err
	})
	return n, err
}

func (r chargeRepo) Discard(ctx context.Context, org core.OrganizationID, id core.ChargeID, at time.Time) error {
	n, err := r.discard(ctx, org, db.DiscardChargesParams{ID: optUUID(id), Touch: true}, at)
	return r.t.noteCatalog(updateErr("charge", id, n, err), CatalogChange{OrganizationID: org, Kind: CatalogPlan})
}

func (r chargeRepo) DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error) {
	n, err := r.discard(ctx, org, db.DiscardChargesParams{PlanID: optUUID(plan)}, at)
	if err != nil {
		return 0, fmt.Errorf("discard charges of plan %s: %w", plan, err)
	}
	if n > 0 {
		_ = r.t.noteCatalog(nil, CatalogChange{OrganizationID: org, Kind: CatalogPlan, Key: plan.String()})
	}
	return int(n), nil
}

func (r chargeRepo) DiscardByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID, at time.Time) (int, error) {
	n, err := r.discard(ctx, org, db.DiscardChargesParams{BillableMetricID: optUUID(metric)}, at)
	if err != nil {
		return 0, fmt.Errorf("discard charges of billable metric %s: %w", metric, err)
	}
	if n > 0 {
		_ = r.t.noteCatalog(nil, CatalogChange{OrganizationID: org, Kind: CatalogPlan})
	}
	return int(n), nil
}

func (r chargeRepo) Get(ctx context.Context, org core.OrganizationID, id core.ChargeID) (*catalog.Charge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCharge(ctx, db.GetChargeParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("charge", id, err)
	}
	return r.one(ctx, q, org, catalogChargeRow(row))
}

func (r chargeRepo) GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*catalog.Charge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetChargeByCode(ctx, db.GetChargeByCodeParams{OrganizationID: org.UUID(), PlanID: plan.UUID(), Code: code})
	if err != nil {
		return nil, getErr("charge", code, err)
	}
	return r.one(ctx, q, org, catalogChargeRow(row))
}

func (r chargeRepo) GetFilter(ctx context.Context, org core.OrganizationID, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetChargeFilter(ctx, db.GetChargeFilterParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("charge_filter", id, err)
	}
	return chargeFilterOf(row)
}

func (r chargeRepo) ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*catalog.Charge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListChargesByPlan(ctx, db.ListChargesByPlanParams{OrganizationID: org.UUID(), PlanID: plan.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list charges of plan %s: %w", plan, mapErr(err))
	}
	return r.charges(ctx, q, org, catalogConv(rows, func(r db.ListChargesByPlanRow) catalogChargeRow { return catalogChargeRow(r) }))
}

func (r chargeRepo) ListByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) ([]*catalog.Charge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListChargesByMetric(ctx, db.ListChargesByMetricParams{OrganizationID: org.UUID(), BillableMetricID: metric.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list charges of billable metric %s: %w", metric, mapErr(err))
	}
	return r.charges(ctx, q, org, catalogConv(rows, func(r db.ListChargesByMetricRow) catalogChargeRow { return catalogChargeRow(r) }))
}

func (r chargeRepo) ListChildren(ctx context.Context, org core.OrganizationID, parent core.ChargeID) ([]*catalog.Charge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	pid := parent.UUID()
	rows, err := q.ListChargeChildren(ctx, db.ListChargeChildrenParams{OrganizationID: org.UUID(), ParentID: &pid})
	if err != nil {
		return nil, fmt.Errorf("list charge children: %w", mapErr(err))
	}
	return r.charges(ctx, q, org, catalogConv(rows, func(r db.ListChargeChildrenRow) catalogChargeRow { return catalogChargeRow(r) }))
}

func (r chargeRepo) ListFilters(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*catalog.ChargeFilter, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListChargeFilters(ctx, db.ListChargeFiltersParams{OrganizationID: org.UUID(), ChargeID: charge.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list charge filters: %w", mapErr(err))
	}
	if len(rows) == 0 {
		// An unknown or discarded charge is not found; a kept one may have
		// no filters.
		row, err := q.GetCharge(ctx, db.GetChargeParams{OrganizationID: org.UUID(), ID: charge.UUID()})
		if err != nil {
			return nil, getErr("charge", charge, err)
		}
		if row.Charge.DeletedAt != nil {
			return nil, notFound("charge", charge)
		}
	}
	return convAll(rows, chargeFilterOf)
}

// ---------------------------------------------------------------------------
// Fixed charges.

// fixedChargeRepo implements catalog.FixedChargeRepository.
type fixedChargeRepo struct{ t *txState }

var _ catalog.FixedChargeRepository = fixedChargeRepo{}

// catalogFixedChargeRow is the common shape of the fixed charge reads.
type catalogFixedChargeRow struct {
	FixedCharge db.FixedCharge
	TaxIds      []uuid.UUID
}

func fixedChargeParams(f *catalog.FixedCharge) db.CreateFixedChargeParams {
	return db.CreateFixedChargeParams{
		OrganizationID:     f.OrganizationID.UUID(),
		ID:                 f.ID.UUID(),
		PlanID:             f.PlanID.UUID(),
		ParentID:           optUUID(f.ParentID),
		Code:               f.Code,
		AddOnID:            f.AddOnID.UUID(),
		ChargeModel:        string(f.ChargeModel),
		Properties:         f.Properties.Raw(),
		Units:              f.Units,
		PayInAdvance:       f.PayInAdvance,
		Prorated:           f.Prorated,
		InvoiceDisplayName: f.InvoiceDisplayName,
		CreatedAt:          utc(f.CreatedAt),
		UpdatedAt:          utc(f.UpdatedAt),
	}
}

func fixedChargeOf(row catalogFixedChargeRow) (*catalog.FixedCharge, error) {
	r := row.FixedCharge
	props, err := catalog.NewProperties(r.Properties)
	if err != nil {
		return nil, fmt.Errorf("fixed charge %s: %w", r.ID, err)
	}
	return &catalog.FixedCharge{
		ID:                 idOf[core.FixedChargeID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		PlanID:             idOf[core.PlanID](r.PlanID),
		ParentID:           optIDOf[core.FixedChargeID](r.ParentID),
		Code:               r.Code,
		AddOnID:            idOf[core.AddOnID](r.AddOnID),
		ChargeModel:        catalog.FixedChargeModel(r.ChargeModel),
		Properties:         props,
		Units:              r.Units,
		PayInAdvance:       r.PayInAdvance,
		Prorated:           r.Prorated,
		InvoiceDisplayName: r.InvoiceDisplayName,
		TaxIDs:             idsOf[core.TaxID](row.TaxIds),
		CreatedAt:          utc(r.CreatedAt),
		UpdatedAt:          utc(r.UpdatedAt),
		DeletedAt:          optUTC(r.DeletedAt),
	}, nil
}

func (r fixedChargeRepo) writeTaxes(ctx context.Context, q *db.Queries, f *catalog.FixedCharge, replace bool) error {
	org, id := f.OrganizationID.UUID(), f.ID.UUID()
	if replace {
		if err := q.DeleteFixedChargeTaxes(ctx, db.DeleteFixedChargeTaxesParams{OrganizationID: org, FixedChargeID: id}); err != nil {
			return err
		}
	}
	if len(f.TaxIDs) == 0 {
		return nil
	}
	return q.InsertFixedChargeTaxes(ctx, db.InsertFixedChargeTaxesParams{OrganizationID: org, FixedChargeID: id, TaxIds: uuids(f.TaxIDs)})
}

func (r fixedChargeRepo) Create(ctx context.Context, f *catalog.FixedCharge) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreateFixedCharge(ctx, fixedChargeParams(f))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, f, false)
	})
	return insertErr("fixed_charge", n, err)
}

func (r fixedChargeRepo) Update(ctx context.Context, f *catalog.FixedCharge) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateFixedCharge(ctx, db.UpdateFixedChargeParams(fixedChargeParams(f)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeTaxes(ctx, q, f, true)
	})
	return updateErr("fixed_charge", f.ID, n, err)
}

// discard discards the kept fixed charges selected by sel and returns how
// many.
func (r fixedChargeRepo) discard(ctx context.Context, org core.OrganizationID, sel db.DiscardFixedChargesParams, at time.Time) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	at = utc(at)
	sel.OrganizationID, sel.DeletedAt = org.UUID(), &at
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DiscardFixedCharges(ctx, sel)
		return err
	})
	return n, err
}

func (r fixedChargeRepo) Discard(ctx context.Context, org core.OrganizationID, id core.FixedChargeID, at time.Time) error {
	n, err := r.discard(ctx, org, db.DiscardFixedChargesParams{ID: optUUID(id), Touch: true}, at)
	return updateErr("fixed_charge", id, n, err)
}

func (r fixedChargeRepo) DiscardByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID, at time.Time) (int, error) {
	n, err := r.discard(ctx, org, db.DiscardFixedChargesParams{PlanID: optUUID(plan)}, at)
	if err != nil {
		return 0, fmt.Errorf("discard fixed charges of plan %s: %w", plan, err)
	}
	return int(n), nil
}

func (r fixedChargeRepo) DiscardByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID, at time.Time) (int, error) {
	n, err := r.discard(ctx, org, db.DiscardFixedChargesParams{AddOnID: optUUID(addOn)}, at)
	if err != nil {
		return 0, fmt.Errorf("discard fixed charges of add-on %s: %w", addOn, err)
	}
	return int(n), nil
}

func (r fixedChargeRepo) Get(ctx context.Context, org core.OrganizationID, id core.FixedChargeID) (*catalog.FixedCharge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetFixedCharge(ctx, db.GetFixedChargeParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("fixed_charge", id, err)
	}
	return fixedChargeOf(catalogFixedChargeRow(row))
}

func (r fixedChargeRepo) GetByCode(ctx context.Context, org core.OrganizationID, plan core.PlanID, code string) (*catalog.FixedCharge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetFixedChargeByCode(ctx, db.GetFixedChargeByCodeParams{OrganizationID: org.UUID(), PlanID: plan.UUID(), Code: code})
	if err != nil {
		return nil, getErr("fixed_charge", code, err)
	}
	return fixedChargeOf(catalogFixedChargeRow(row))
}

func (r fixedChargeRepo) ListByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*catalog.FixedCharge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListFixedChargesByPlan(ctx, db.ListFixedChargesByPlanParams{OrganizationID: org.UUID(), PlanID: plan.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list fixed charges of plan %s: %w", plan, mapErr(err))
	}
	return convAll(rows, func(r db.ListFixedChargesByPlanRow) (*catalog.FixedCharge, error) {
		return fixedChargeOf(catalogFixedChargeRow(r))
	})
}

func (r fixedChargeRepo) ListByAddOn(ctx context.Context, org core.OrganizationID, addOn core.AddOnID) ([]*catalog.FixedCharge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListFixedChargesByAddOn(ctx, db.ListFixedChargesByAddOnParams{OrganizationID: org.UUID(), AddOnID: addOn.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list fixed charges of add-on %s: %w", addOn, mapErr(err))
	}
	return convAll(rows, func(r db.ListFixedChargesByAddOnRow) (*catalog.FixedCharge, error) {
		return fixedChargeOf(catalogFixedChargeRow(r))
	})
}

func (r fixedChargeRepo) ListChildren(ctx context.Context, org core.OrganizationID, parent core.FixedChargeID) ([]*catalog.FixedCharge, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	pid := parent.UUID()
	rows, err := q.ListFixedChargeChildren(ctx, db.ListFixedChargeChildrenParams{OrganizationID: org.UUID(), ParentID: &pid})
	if err != nil {
		return nil, fmt.Errorf("list fixed charge children: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListFixedChargeChildrenRow) (*catalog.FixedCharge, error) {
		return fixedChargeOf(catalogFixedChargeRow(r))
	})
}
