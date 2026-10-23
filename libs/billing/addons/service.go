package addons

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Resource is the resource name of the not-found errors.
const Resource = "add_on"

// Service manages add-ons. It uses d.Tx, d.Clock and d.IDs only: lago
// emits no webhook for add-ons.
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

// CreateInput is an add-on to create.
type CreateInput struct {
	// Code is the unique code.
	Code string
	// Name is the display name.
	Name string
	// InvoiceDisplayName replaces Name on invoices.
	InvoiceDisplayName *string
	// Description is free text.
	Description *string
	// Amount is the default price, > 0, in minor units of Currency.
	Amount int64
	// Currency is the add-on currency.
	Currency money.Currency
	// Taxes reference the add-on's taxes; nil sets none.
	Taxes []refs.Ref
}

// Create creates an add-on with its taxes. A code used by another add-on
// of the organization is code value_already_exist, a missing tax
// core.NotFound("tax").
//
// source: app/services/add_ons/create_service.rb:13-36
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*catalog.AddOn, error) {
	now := s.d.Now()
	a := &catalog.AddOn{
		ID:                 core.NewID[core.AddOnID](s.d.IDs),
		OrganizationID:     org,
		Code:               in.Code,
		Name:               in.Name,
		InvoiceDisplayName: in.InvoiceDisplayName,
		Description:        in.Description,
		Amount:             in.Amount,
		Currency:           in.Currency,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		// The add-on's own errors come before the taxes'
		// (create_service.rb:15-28).
		if err := validate(ctx, r, a); err != nil {
			return err
		}
		ids, err := refs.Taxes(r, org).IDs(ctx, in.Taxes)
		if err != nil {
			return err
		}
		a.TaxIDs = ids
		if err := r.AddOns.Create(ctx, a); err != nil {
			return codeConflict(err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create add-on: %w", err)
	}
	return a, nil
}

// UpdateInput is a partial update of an add-on: only the set fields
// change.
type UpdateInput struct {
	// Code is the unique code.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// InvoiceDisplayName replaces Name on invoices.
	InvoiceDisplayName core.Optional[*string]
	// Description is free text.
	Description core.Optional[*string]
	// Amount is the default price, > 0, in minor units of the currency.
	Amount core.Optional[int64]
	// Currency is the add-on currency.
	Currency core.Optional[money.Currency]
	// Taxes replace the add-on's taxes; an empty list removes them.
	Taxes core.Optional[[]refs.Ref]
}

// Update changes the add-on id. Every field may change, also while
// customers use the add-on.
//
// source: app/services/add_ons/update_service.rb:13-37
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.AddOnID, in UpdateInput) (*catalog.AddOn, error) {
	var out *catalog.AddOn
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		a, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		in.Name.Apply(&a.Name)
		in.InvoiceDisplayName.Apply(&a.InvoiceDisplayName)
		in.Description.Apply(&a.Description)
		in.Code.Apply(&a.Code)
		in.Amount.Apply(&a.Amount)
		in.Currency.Apply(&a.Currency)
		if err := validate(ctx, r, a); err != nil {
			return err
		}
		if in.Taxes.Set {
			ids, err := refs.Taxes(r, org).IDs(ctx, in.Taxes.Value)
			if err != nil {
				return err
			}
			a.TaxIDs = ids
		}
		a.UpdatedAt = s.d.Now()
		if err := r.AddOns.Update(ctx, a); err != nil {
			return codeConflict(err)
		}
		out = a
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update add-on %s: %w", id, err)
	}
	return out, nil
}

// Delete discards the add-on id with its fixed charges on every plan; the
// fees that reference them keep them. The discarded add-on is returned
// with DeletedAt set.
//
// source: app/services/add_ons/destroy_service.rb:13-25
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.AddOnID) (*catalog.AddOn, error) {
	var out *catalog.AddOn
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		a, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		now := s.d.Now()
		if err := r.AddOns.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard add-on: %w", err)
		}
		if _, err := r.FixedCharges.DiscardByAddOn(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard fixed charges: %w", err)
		}
		a.DeletedAt, a.UpdatedAt = &now, now
		out = a
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete add-on %s: %w", id, err)
	}
	return out, nil
}

// Get returns the add-on id.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.AddOnID) (*catalog.AddOn, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.AddOn, error) {
		return kept(ctx, r, org, id)
	})
}

// GetByCode returns the add-on with code (the API finds add-ons by code,
// add_ons_controller.rb:45-47).
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.AddOn, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.AddOn, error) {
		a, err := r.AddOns.GetByCode(ctx, org, code)
		return a, notFound(err)
	})
}

// List returns a page of the organization's add-ons whose name or code
// contains search, most recent first (lago AddOnsQuery).
//
// source: app/queries/add_ons_query.rb:6-27
func (s *Service) List(ctx context.Context, org core.OrganizationID, search string, page core.PageRequest) (core.Page[*catalog.AddOn], error) {
	f := catalog.AddOnFilter{Search: search, Order: core.OrderRecent}
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.AddOn], error) {
		return r.AddOns.List(ctx, org, f, page)
	})
}

// Taxes returns the taxes of a, in order.
func (s *Service) Taxes(ctx context.Context, a *catalog.AddOn) ([]*catalog.Tax, error) {
	if len(a.TaxIDs) == 0 {
		return nil, nil
	}
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*catalog.Tax, error) {
		return r.Taxes.ListByIDs(ctx, a.OrganizationID, a.TaxIDs)
	})
}

// validate checks a as lago's model does: the domain rules and the code's
// uniqueness, reported together.
//
// source: app/models/add_on.rb:23-29
func validate(ctx context.Context, r port.Repos, a *catalog.AddOn) error {
	var v core.Validator
	v.Merge("", a.Validate())
	if a.Code != "" {
		other, err := r.AddOns.GetByCode(ctx, a.OrganizationID, a.Code)
		switch {
		case err == nil:
			v.Check(other.ID == a.ID, "code", core.CodeAlreadyExists)
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

// notFound turns a missing add-on into core.NotFound("add_on").
// kept returns the kept add-on id; a missing or discarded one is not
// found.
func kept(ctx context.Context, r port.Repos, org core.OrganizationID, id core.AddOnID) (*catalog.AddOn, error) {
	a, err := r.AddOns.Get(ctx, org, id)
	if err != nil {
		return nil, notFound(err)
	}
	if a.DeletedAt != nil {
		return nil, core.NotFound(Resource)
	}
	return a, nil
}

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
