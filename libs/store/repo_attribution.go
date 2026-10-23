package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindAttribution binds the repositories of usage attribution.
func (t *txState) bindAttribution(r *port.Repos) {
	r.UsageAttributionTypes = attributionTypeRepo{t}
	r.UsageAttributionValues = attributionValueRepo{t}
}

// attributionTypeRepo implements attribution.TypeRepository.
type attributionTypeRepo struct{ t *txState }

var _ attribution.TypeRepository = attributionTypeRepo{}

func attributionTypeOf(r db.UsageAttributionType) (*attribution.Type, error) {
	role, err := attribution.ParseRole(r.Role)
	if err != nil {
		return nil, fmt.Errorf("usage attribution type %s: %w", r.ID, err)
	}
	keys := r.AttributionKeys
	if keys == nil {
		keys = []string{}
	}
	return &attribution.Type{
		ID:              idOf[core.UsageAttributionTypeID](r.ID),
		OrganizationID:  idOf[core.OrganizationID](r.OrganizationID),
		Code:            r.Code,
		Name:            r.Name,
		Description:     r.Description,
		AttributionKeys: keys,
		Role:            role,
		ParentID:        optIDOf[core.UsageAttributionTypeID](r.ParentID),
		CreatedAt:       utc(r.CreatedAt),
		UpdatedAt:       utc(r.UpdatedAt),
		DeletedAt:       optUTC(r.DeletedAt),
	}, nil
}

func (r attributionTypeRepo) Create(ctx context.Context, t *attribution.Type) error {
	q, err := r.t.queries(ctx, t.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateUsageAttributionType(ctx, db.CreateUsageAttributionTypeParams{
		OrganizationID: t.OrganizationID.UUID(), ID: t.ID.UUID(), Code: t.Code, Name: t.Name, Description: t.Description,
		AttributionKeys: t.AttributionKeys, Role: t.Role.String(), ParentID: optUUID(t.ParentID),
		CreatedAt: utc(t.CreatedAt), UpdatedAt: utc(t.UpdatedAt), DeletedAt: optUTC(t.DeletedAt),
	})
	return insertErr("usage_attribution_type", n, err)
}

func (r attributionTypeRepo) Update(ctx context.Context, t *attribution.Type) error {
	q, err := r.t.queries(ctx, t.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateUsageAttributionType(ctx, db.UpdateUsageAttributionTypeParams{
		OrganizationID: t.OrganizationID.UUID(), ID: t.ID.UUID(), Code: t.Code, Name: t.Name, Description: t.Description,
		AttributionKeys: t.AttributionKeys, Role: t.Role.String(), ParentID: optUUID(t.ParentID),
		CreatedAt: utc(t.CreatedAt), UpdatedAt: utc(t.UpdatedAt), DeletedAt: optUTC(t.DeletedAt),
	})
	return updateErr("usage_attribution_type", t.ID, n, err)
}

func (r attributionTypeRepo) Get(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (*attribution.Type, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetUsageAttributionType(ctx, db.GetUsageAttributionTypeParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("usage_attribution_type", id, err)
	}
	return attributionTypeOf(row)
}

func (r attributionTypeRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*attribution.Type, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetUsageAttributionTypeByCode(ctx, db.GetUsageAttributionTypeByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("usage_attribution_type", code, err)
	}
	return attributionTypeOf(row)
}

func (r attributionTypeRepo) GetMany(ctx context.Context, org core.OrganizationID, ids []core.UsageAttributionTypeID) (map[core.UsageAttributionTypeID]*attribution.Type, error) {
	out := map[core.UsageAttributionTypeID]*attribution.Type{}
	if len(ids) == 0 {
		return out, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListUsageAttributionTypesByIDs(ctx, db.ListUsageAttributionTypesByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("get usage attribution types: %w", mapErr(err))
	}
	for _, row := range rows {
		t, err := attributionTypeOf(row)
		if err != nil {
			return nil, err
		}
		out[t.ID] = t
	}
	return out, nil
}

func (r attributionTypeRepo) List(ctx context.Context, org core.OrganizationID, f attribution.TypeFilter, page core.PageRequest) (core.Page[*attribution.Type], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*attribution.Type]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*attribution.Type]{}, err
	}
	rows, err := q.ListUsageAttributionTypes(ctx, db.ListUsageAttributionTypesParams{
		OrganizationID: org.UUID(), Role: optString(f.Role.String()), Root: f.Root, Search: searchArg(f.Search),
		After: after, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*attribution.Type]{}, fmt.Errorf("list usage attribution types: %w", mapErr(err))
	}
	return pageOf(rows, page, attributionTypeOf, func(t *attribution.Type) string {
		return core.Cursor{Key: t.CreatedAt.UTC().Format(time.RFC3339Nano), ID: t.ID.UUID()}.Encode()
	})
}

func (r attributionTypeRepo) KeysTaken(ctx context.Context, org core.OrganizationID, keys []string, except core.UsageAttributionTypeID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	if keys == nil {
		keys = []string{}
	}
	taken, err := q.UsageAttributionKeysTaken(ctx, db.UsageAttributionKeysTakenParams{
		OrganizationID: org.UUID(), ExceptID: except.UUID(), Keys: keys,
	})
	if err != nil {
		return false, fmt.Errorf("usage attribution keys taken: %w", mapErr(err))
	}
	return taken, nil
}

func (r attributionTypeRepo) HasChildren(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	u := id.UUID()
	has, err := q.UsageAttributionTypeHasChildren(ctx, db.UsageAttributionTypeHasChildrenParams{OrganizationID: org.UUID(), ID: &u})
	if err != nil {
		return false, fmt.Errorf("usage attribution type %s children: %w", id, mapErr(err))
	}
	return has, nil
}

func (r attributionTypeRepo) Delete(ctx context.Context, org core.OrganizationID, id core.UsageAttributionTypeID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	at = at.UTC()
	n, err := q.DeleteUsageAttributionType(ctx, db.DeleteUsageAttributionTypeParams{OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: &at})
	return updateErr("usage_attribution_type", id, n, err)
}

// attributionValueRepo implements attribution.ValueRepository.
type attributionValueRepo struct{ t *txState }

var _ attribution.ValueRepository = attributionValueRepo{}

func attributionValueOf(r db.UsageAttributionValue) *attribution.Value {
	return &attribution.Value{
		ID:             idOf[core.UsageAttributionValueID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		TypeID:         idOf[core.UsageAttributionTypeID](r.UsageAttributionTypeID),
		CustomerID:     idOf[core.CustomerID](r.CustomerID),
		ParentID:       optIDOf[core.UsageAttributionValueID](r.ParentID),
		Value:          r.Value,
		LastSeenAt:     optUTC(r.LastSeenAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
		DeletedAt:      optUTC(r.DeletedAt),
	}
}

func (r attributionValueRepo) Create(ctx context.Context, v *attribution.Value) error {
	q, err := r.t.queries(ctx, v.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateUsageAttributionValue(ctx, db.CreateUsageAttributionValueParams{
		OrganizationID: v.OrganizationID.UUID(), ID: v.ID.UUID(), UsageAttributionTypeID: v.TypeID.UUID(),
		CustomerID: v.CustomerID.UUID(), ParentID: optUUID(v.ParentID), Value: v.Value, LastSeenAt: optUTC(v.LastSeenAt),
		CreatedAt: utc(v.CreatedAt), UpdatedAt: utc(v.UpdatedAt), DeletedAt: optUTC(v.DeletedAt),
	})
	return insertErr("usage_attribution_value", n, err)
}

func (r attributionValueRepo) Get(ctx context.Context, org core.OrganizationID, id core.UsageAttributionValueID) (*attribution.Value, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetUsageAttributionValue(ctx, db.GetUsageAttributionValueParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("usage_attribution_value", id, err)
	}
	return attributionValueOf(row), nil
}

func (r attributionValueRepo) ExistsForType(ctx context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	ok, err := q.UsageAttributionValuesExist(ctx, db.UsageAttributionValuesExistParams{OrganizationID: org.UUID(), UsageAttributionTypeID: typeID.UUID()})
	if err != nil {
		return false, fmt.Errorf("usage attribution values of %s: %w", typeID, mapErr(err))
	}
	return ok, nil
}

func (r attributionValueRepo) DeleteForType(ctx context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID, at time.Time) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	at = at.UTC()
	n, err := q.DeleteUsageAttributionValues(ctx, db.DeleteUsageAttributionValuesParams{
		OrganizationID: org.UUID(), UsageAttributionTypeID: typeID.UUID(), DeletedAt: &at,
	})
	if err != nil {
		return 0, fmt.Errorf("delete usage attribution values of %s: %w", typeID, mapErr(err))
	}
	return int(n), nil
}
