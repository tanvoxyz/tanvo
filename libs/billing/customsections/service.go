package customsections

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Resource is the resource name of the errors.
const Resource = "invoice_custom_section"

// Service manages invoice custom sections.
type Service struct {
	d *kernel.Deps
}

// New returns the service over d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// CreateInput is a manual section to create.
type CreateInput struct {
	// Code is unique in the organization.
	Code string
	// Name is the section's name.
	Name string
	// DisplayName is the printed title.
	DisplayName *string
	// Details is the printed text.
	Details *string
	// Description is free text, not printed.
	Description *string
}

// Create creates a manual section (lago InvoiceCustomSections::CreateService).
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*invoice.CustomSection, error) {
	now := s.d.Now()
	sec := &invoice.CustomSection{
		ID:             core.NewID[core.InvoiceCustomSectionID](s.d.IDs),
		OrganizationID: org,
		Code:           in.Code,
		Name:           in.Name,
		DisplayName:    in.DisplayName,
		Details:        in.Details,
		Description:    in.Description,
		SectionType:    invoice.CustomSectionTypeManual,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*invoice.CustomSection, error) {
		if err := s.validate(ctx, r, sec); err != nil {
			return nil, err
		}
		if err := r.InvoiceCustomSections.Create(ctx, sec); err != nil {
			return nil, codeConflict(err)
		}
		return sec, nil
	})
}

// UpdateInput changes a section; unset fields are kept.
type UpdateInput struct {
	// Code is the unique code.
	Code core.Optional[string]
	// Name is the section's name.
	Name core.Optional[string]
	// DisplayName is the printed title.
	DisplayName core.Optional[*string]
	// Details is the printed text.
	Details core.Optional[*string]
	// Description is free text, not printed.
	Description core.Optional[*string]
}

// Update changes the section id (lago InvoiceCustomSections::UpdateService).
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID, in UpdateInput) (*invoice.CustomSection, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*invoice.CustomSection, error) {
		sec, err := r.InvoiceCustomSections.Get(ctx, org, id)
		if err != nil {
			return nil, notFound(err)
		}
		changed := in.Code.Apply(&sec.Code)
		changed = in.Name.Apply(&sec.Name) || changed
		changed = in.DisplayName.Apply(&sec.DisplayName) || changed
		changed = in.Details.Apply(&sec.Details) || changed
		changed = in.Description.Apply(&sec.Description) || changed
		if !changed {
			return sec, nil
		}
		sec.UpdatedAt = s.d.Now()
		if err := s.validate(ctx, r, sec); err != nil {
			return nil, err
		}
		if err := r.InvoiceCustomSections.Update(ctx, sec); err != nil {
			return nil, codeConflict(err)
		}
		return sec, nil
	})
}

// Delete soft-deletes the section id (its code is free again) and removes
// it from the selections of billing entities, customers and subscriptions
// (lago InvoiceCustomSections::DestroyService and DeselectAllService). It
// returns the deleted section.
//
// source: app/services/invoice_custom_sections/destroy_service.rb:12-21
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*invoice.CustomSection, error) {
		sec, err := r.InvoiceCustomSections.Get(ctx, org, id)
		if err != nil {
			return nil, notFound(err)
		}
		now := s.d.Now()
		if err := r.InvoiceCustomSections.Delete(ctx, org, id, now); err != nil {
			return nil, notFound(err)
		}
		sec.DeletedAt = &now
		sec.UpdatedAt = now
		return sec, nil
	})
}

// Get returns the section id.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*invoice.CustomSection, error) {
		sec, err := r.InvoiceCustomSections.Get(ctx, org, id)
		return sec, notFound(err)
	})
}

// GetByCode returns the section with code.
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*invoice.CustomSection, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*invoice.CustomSection, error) {
		sec, err := r.InvoiceCustomSections.GetByCode(ctx, org, code)
		return sec, notFound(err)
	})
}

// List returns the organization's manual sections by name (lago
// InvoiceCustomSectionsResolver: manual_invoice_custom_sections ordered by
// name).
func (s *Service) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*invoice.CustomSection], error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*invoice.CustomSection], error) {
		p, err := r.InvoiceCustomSections.ListManual(ctx, org, page)
		if err != nil {
			return core.Page[*invoice.CustomSection]{}, fmt.Errorf("list invoice custom sections: %w", err)
		}
		return p, nil
	})
}

// validate checks sec and the uniqueness of its code.
func (s *Service) validate(ctx context.Context, r port.Repos, sec *invoice.CustomSection) error {
	if err := sec.Validate(); err != nil {
		return err
	}
	other, err := r.InvoiceCustomSections.GetByCode(ctx, sec.OrganizationID, sec.Code)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("load invoice custom section %q: %w", sec.Code, err)
	case other.ID != sec.ID:
		return fieldError("code", core.CodeAlreadyExists)
	}
	return nil
}

func inTx[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// notFound turns a missing section into core.NotFound(Resource).
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
		return fieldError("code", core.CodeAlreadyExists)
	}
	return err
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}
