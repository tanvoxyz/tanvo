package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
)

// attributionData holds usage attribution types and values.
type attributionData struct {
	types  *table[core.UsageAttributionTypeID, attribution.Type]
	values *table[core.UsageAttributionValueID, attribution.Value]
}

func newAttributionData() *attributionData {
	return &attributionData{
		types: newTable[core.UsageAttributionTypeID](func(t attribution.Type) attribution.Type {
			t.Name = clonePtr(t.Name)
			t.Description = clonePtr(t.Description)
			t.AttributionKeys = slices.Clone(t.AttributionKeys)
			t.DeletedAt = clonePtr(t.DeletedAt)
			return t
		}),
		values: newTable[core.UsageAttributionValueID](func(v attribution.Value) attribution.Value {
			v.LastSeenAt = clonePtr(v.LastSeenAt)
			v.DeletedAt = clonePtr(v.DeletedAt)
			return v
		}),
	}
}

func (d *attributionData) snapshot() *attributionData {
	return &attributionData{types: d.types.snapshot(), values: d.values.snapshot()}
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// attributionTypeRepo implements attribution.TypeRepository.
type attributionTypeRepo struct{ d *attributionData }

func (r attributionTypeRepo) kept(org core.OrganizationID) func(*attribution.Type) bool {
	return func(t *attribution.Type) bool { return t.OrganizationID == org && t.DeletedAt == nil }
}

func (r attributionTypeRepo) check(t *attribution.Type) error {
	if t.DeletedAt == nil && r.d.types.any(func(x *attribution.Type) bool {
		return x.ID != t.ID && x.OrganizationID == t.OrganizationID && x.DeletedAt == nil && x.Code == t.Code
	}) {
		return conflict("usage_attribution_type", "code")
	}
	return nil
}

func (r attributionTypeRepo) Create(_ context.Context, t *attribution.Type) error {
	if r.d.types.has(t.ID) {
		return conflict("usage_attribution_type", "id")
	}
	if err := r.check(t); err != nil {
		return err
	}
	r.d.types.put(t.ID, *t)
	return nil
}

func (r attributionTypeRepo) Update(_ context.Context, t *attribution.Type) error {
	cur, ok := r.d.types.get(t.ID)
	if !ok || cur.OrganizationID != t.OrganizationID {
		return notFound("usage_attribution_type", t.ID)
	}
	if err := r.check(t); err != nil {
		return err
	}
	r.d.types.put(t.ID, *t)
	return nil
}

func (r attributionTypeRepo) Get(_ context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (*attribution.Type, error) {
	t, ok := r.d.types.get(id)
	if !ok || !r.kept(org)(t) {
		return nil, notFound("usage_attribution_type", id)
	}
	return t, nil
}

func (r attributionTypeRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*attribution.Type, error) {
	kept := r.kept(org)
	t, ok := r.d.types.first(func(x *attribution.Type) bool { return kept(x) && x.Code == code })
	if !ok {
		return nil, notFound("usage_attribution_type", stringer(code))
	}
	return t, nil
}

func (r attributionTypeRepo) GetMany(_ context.Context, org core.OrganizationID, ids []core.UsageAttributionTypeID) (map[core.UsageAttributionTypeID]*attribution.Type, error) {
	out := map[core.UsageAttributionTypeID]*attribution.Type{}
	for _, id := range ids {
		if t, ok := r.d.types.get(id); ok && t.OrganizationID == org {
			out[id] = t
		}
	}
	return out, nil
}

func (r attributionTypeRepo) List(_ context.Context, org core.OrganizationID, f attribution.TypeFilter, page core.PageRequest) (core.Page[*attribution.Type], error) {
	kept := r.kept(org)
	rows := r.d.types.find(func(t *attribution.Type) bool {
		if !kept(t) || (f.Role != "" && t.Role != f.Role) {
			return false
		}
		if f.Root != nil {
			parent, ok := r.d.types.get(t.ParentID)
			hasParent := !t.ParentID.IsZero() && ok && kept(parent)
			if hasParent == *f.Root {
				return false
			}
		}
		name := ""
		if t.Name != nil {
			name = *t.Name
		}
		return matchesNameOrCode(f.Search, name, t.Code)
	})
	return paginateRecent(rows, func(t *attribution.Type) (time.Time, uuid.UUID) { return t.CreatedAt, t.ID.UUID() }, page)
}

func (r attributionTypeRepo) KeysTaken(_ context.Context, org core.OrganizationID, keys []string, except core.UsageAttributionTypeID) (bool, error) {
	kept := r.kept(org)
	return r.d.types.any(func(t *attribution.Type) bool {
		return kept(t) && t.ID != except && slices.ContainsFunc(t.AttributionKeys, func(k string) bool { return slices.Contains(keys, k) })
	}), nil
}

func (r attributionTypeRepo) HasChildren(_ context.Context, org core.OrganizationID, id core.UsageAttributionTypeID) (bool, error) {
	kept := r.kept(org)
	return r.d.types.any(func(t *attribution.Type) bool { return kept(t) && t.ParentID == id }), nil
}

func (r attributionTypeRepo) Delete(_ context.Context, org core.OrganizationID, id core.UsageAttributionTypeID, at time.Time) error {
	t, ok := r.d.types.get(id)
	if !ok || !r.kept(org)(t) {
		return notFound("usage_attribution_type", id)
	}
	at = at.UTC()
	t.DeletedAt = &at
	r.d.types.put(id, *t)
	return nil
}

// attributionValueRepo implements attribution.ValueRepository.
type attributionValueRepo struct{ d *attributionData }

func (r attributionValueRepo) Create(_ context.Context, v *attribution.Value) error {
	if r.d.values.has(v.ID) {
		return conflict("usage_attribution_value", "id")
	}
	if r.d.values.any(func(x *attribution.Value) bool {
		return x.OrganizationID == v.OrganizationID && x.CustomerID == v.CustomerID && x.TypeID == v.TypeID && x.Value == v.Value
	}) {
		return conflict("usage_attribution_value", "value")
	}
	r.d.values.put(v.ID, *v)
	return nil
}

func (r attributionValueRepo) Get(_ context.Context, org core.OrganizationID, id core.UsageAttributionValueID) (*attribution.Value, error) {
	v, ok := r.d.values.get(id)
	if !ok || v.OrganizationID != org {
		return nil, notFound("usage_attribution_value", id)
	}
	return v, nil
}

func (r attributionValueRepo) ExistsForType(_ context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID) (bool, error) {
	return r.d.values.any(func(v *attribution.Value) bool { return v.OrganizationID == org && v.TypeID == typeID }), nil
}

func (r attributionValueRepo) DeleteForType(_ context.Context, org core.OrganizationID, typeID core.UsageAttributionTypeID, at time.Time) (int, error) {
	rows := r.d.values.find(func(v *attribution.Value) bool {
		return v.OrganizationID == org && v.TypeID == typeID && v.DeletedAt == nil
	})
	at = at.UTC()
	for _, v := range rows {
		v.DeletedAt = &at
		r.d.values.put(v.ID, *v)
	}
	return len(rows), nil
}

var (
	_ attribution.TypeRepository  = attributionTypeRepo{}
	_ attribution.ValueRepository = attributionValueRepo{}
)
