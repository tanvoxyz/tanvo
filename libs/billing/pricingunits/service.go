package pricingunits

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Resource is the resource name of the not-found errors.
const Resource = "pricing_unit"

// Service manages pricing units. It uses d.Tx, d.Clock and d.IDs only:
// lago emits no webhook for pricing units, and the premium license check
// of lago is not a Tanvo gate (architecture §14).
type Service struct {
	d *kernel.Deps
}

// New returns the service over d. It fails when a dependency the service
// uses is nil.
func New(d *kernel.Deps) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	}
	return &Service{d: d}, nil
}

// CreateInput is a pricing unit to create.
type CreateInput struct {
	// Code is the unique, immutable code.
	Code string
	// Name is the display name.
	Name string
	// ShortName is the symbol printed with amounts.
	ShortName string
	// Description is free text.
	Description *string
}

// Create creates a pricing unit. A code used by another pricing unit of
// the organization is code value_already_exist.
//
// source: app/services/pricing_units/create_service.rb:11-23
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*catalog.PricingUnit, error) {
	now := s.d.Now()
	p := &catalog.PricingUnit{
		ID:             core.NewID[core.PricingUnitID](s.d.IDs),
		OrganizationID: org,
		Code:           in.Code,
		Name:           in.Name,
		ShortName:      in.ShortName,
		Description:    in.Description,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := validate(ctx, r, p, p.Validate()); err != nil {
			return err
		}
		if err := r.PricingUnits.Create(ctx, p); err != nil {
			return codeConflict(err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create pricing unit: %w", err)
	}
	return p, nil
}

// UpdateInput is a partial update of a pricing unit: only the set fields
// change. The code is immutable.
type UpdateInput struct {
	// Name is the display name.
	Name core.Optional[string]
	// ShortName is the symbol printed with amounts.
	ShortName core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
}

// Update changes the pricing unit id.
//
// source: app/services/pricing_units/update_service.rb:12-25
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.PricingUnitID, in UpdateInput) (*catalog.PricingUnit, error) {
	var out *catalog.PricingUnit
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := r.PricingUnits.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		p := *old
		in.Name.Apply(&p.Name)
		in.ShortName.Apply(&p.ShortName)
		in.Description.Apply(&p.Description)
		if err := validate(ctx, r, &p, catalog.ValidatePricingUnitUpdate(old, &p)); err != nil {
			return err
		}
		p.UpdatedAt = s.d.Now()
		if err := r.PricingUnits.Update(ctx, &p); err != nil {
			return fmt.Errorf("update pricing unit: %w", err)
		}
		out = &p
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update pricing unit %s: %w", id, err)
	}
	return out, nil
}

// Get returns the pricing unit id.
//
// source: app/graphql/resolvers/pricing_unit_resolver.rb:15-19
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.PricingUnitID) (*catalog.PricingUnit, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.PricingUnit, error) {
		p, err := r.PricingUnits.Get(ctx, org, id)
		return p, notFound(err)
	})
}

// GetByCode returns the pricing unit with code.
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.PricingUnit, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.PricingUnit, error) {
		p, err := r.PricingUnits.GetByCode(ctx, org, code)
		return p, notFound(err)
	})
}

// List returns a page of the organization's pricing units whose name or
// code contains search, by name, then most recent first (lago
// PricingUnitsQuery).
//
// source: app/queries/pricing_units_query.rb:6-30
func (s *Service) List(ctx context.Context, org core.OrganizationID, search string, page core.PageRequest) (core.Page[*catalog.PricingUnit], error) {
	f := catalog.PricingUnitFilter{Search: search, Order: core.OrderRecent}
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.PricingUnit], error) {
		return r.PricingUnits.List(ctx, org, f, page)
	})
}

// validate adds the code's uniqueness to the domain errors errs, as lago's
// model does.
//
// source: app/models/pricing_unit.rb:7-10
func validate(ctx context.Context, r port.Repos, p *catalog.PricingUnit, errs error) error {
	var v core.Validator
	v.Merge("", errs)
	if p.Code != "" {
		other, err := r.PricingUnits.GetByCode(ctx, p.OrganizationID, p.Code)
		switch {
		case err == nil:
			v.Check(other.ID == p.ID, "code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("check code: %w", err)
		}
	}
	return v.Err()
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// notFound turns a missing pricing unit into core.NotFound("pricing_unit").
func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after validate, into code value_already_exist.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: core.CodeAlreadyExists}}}
	}
	return err
}
