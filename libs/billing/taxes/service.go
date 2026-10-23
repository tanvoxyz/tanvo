package taxes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Resource is the resource name of the not-found errors.
const Resource = "tax"

// Service manages taxes. It uses d.Tx, d.Clock and d.IDs only: lago emits
// no webhook for taxes.
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

// CreateInput is a tax to create.
type CreateInput struct {
	// Code is the unique code.
	Code string
	// Name is the display name.
	Name string
	// Rate is the rate in percent; nil is rate value_is_mandatory.
	Rate *money.Decimal
	// Description is free text.
	Description *string
	// AppliedToOrganization, when true, makes the tax a default tax of the
	// organization's default billing entity.
	AppliedToOrganization bool
}

// Create creates a tax; applied to the organization, it is attached to the
// default billing entity, whose draft invoices are flagged for refresh. A
// code used by another tax of the organization is code
// value_already_exist.
//
// source: app/services/taxes/create_service.rb:14-31
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*catalog.Tax, error) {
	now := s.d.Now()
	t := &catalog.Tax{
		ID:             core.NewID[core.TaxID](s.d.IDs),
		OrganizationID: org,
		Code:           in.Code,
		Name:           in.Name,
		Description:    in.Description,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if in.Rate != nil {
		t.Rate = *in.Rate
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := validate(ctx, r, t, in.Rate != nil); err != nil {
			return err
		}
		if err := r.Taxes.Create(ctx, t); err != nil {
			return codeConflict(err)
		}
		if in.AppliedToOrganization {
			return s.applyToOrganization(ctx, r, t, true)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create tax: %w", err)
	}
	return t, nil
}

// UpdateInput is a partial update of a tax: only the set fields change.
type UpdateInput struct {
	// Name is the display name.
	Name core.Optional[string]
	// Code is the unique code.
	Code core.Optional[string]
	// Rate is the rate in percent.
	Rate core.Optional[money.Decimal]
	// Description is free text.
	Description core.Optional[*string]
	// AppliedToOrganization attaches the tax to (true) or detaches it from
	// (false) the default billing entity.
	AppliedToOrganization core.Optional[bool]
}

// Update changes the tax id. The draft invoices of the customers the tax
// applies to, before or after the change, are flagged for refresh, and so
// are those of the default billing entity when AppliedToOrganization is
// set.
//
// source: app/services/taxes/update_service.rb:14-36
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.TaxID, in UpdateInput) (*catalog.Tax, error) {
	var out *catalog.Tax
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		before, err := drafts(ctx, r, t)
		if err != nil {
			return err
		}
		in.Name.Apply(&t.Name)
		in.Code.Apply(&t.Code)
		in.Rate.Apply(&t.Rate)
		in.Description.Apply(&t.Description)
		if err := validate(ctx, r, t, true); err != nil {
			return err
		}
		t.UpdatedAt = s.d.Now()
		if err := r.Taxes.Update(ctx, t); err != nil {
			return codeConflict(err)
		}
		if in.AppliedToOrganization.Set {
			if err := s.applyToOrganization(ctx, r, t, in.AppliedToOrganization.Value); err != nil {
				return err
			}
		}
		after, err := drafts(ctx, r, t)
		if err != nil {
			return err
		}
		out = t
		return s.requestRefresh(ctx, r, append(before, after...))
	})
	if err != nil {
		return nil, fmt.Errorf("update tax %s: %w", id, err)
	}
	return out, nil
}

// Delete discards the tax id, detaching it from every owner (billing
// entities, customers, plans, charges, fixed charges, add-ons and
// commitments), and flags for refresh the draft invoices of the customers
// it applied to and of the billing entities that had it. The discarded
// tax is returned with DeletedAt set.
//
// source: app/services/taxes/destroy_service.rb:13-47
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.TaxID) (*catalog.Tax, error) {
	var out *catalog.Tax
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		// The drafts are found through the tax's owners, so before
		// detaching it (destroy_service.rb:20-22).
		ds, err := drafts(ctx, r, t)
		if err != nil {
			return err
		}
		entities, err := kernel.All(func(p core.PageRequest) (core.Page[*organization.BillingEntity], error) {
			return r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{}, p)
		})
		if err != nil {
			return fmt.Errorf("list billing entities: %w", err)
		}
		for _, be := range entities {
			if !slices.Contains(be.TaxIDs, t.ID) {
				continue
			}
			bd, err := entityDrafts(ctx, r, be)
			if err != nil {
				return err
			}
			ds = append(ds, bd...)
		}
		// The store detaches the tax from its owners with it.
		now := s.d.Now()
		if err := r.Taxes.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard tax: %w", err)
		}
		t.DeletedAt, t.UpdatedAt = &now, now
		out = t
		return s.requestRefresh(ctx, r, ds)
	})
	if err != nil {
		return nil, fmt.Errorf("delete tax %s: %w", id, err)
	}
	return out, nil
}

// Get returns the tax id.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.TaxID) (*catalog.Tax, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Tax, error) {
		return kept(ctx, r, org, id)
	})
}

// GetByCode returns the tax with code (the API finds taxes by code,
// taxes_controller.rb:23).
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.Tax, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Tax, error) {
		t, err := r.Taxes.GetByCode(ctx, org, code)
		return t, notFound(err)
	})
}

// List returns a page of the organization's taxes matching f, by name,
// then most recent first (lago TaxesQuery's default order).
//
// source: app/queries/taxes_query.rb:7-20
func (s *Service) List(ctx context.Context, org core.OrganizationID, f catalog.TaxFilter, page core.PageRequest) (core.Page[*catalog.Tax], error) {
	f.Order = core.OrderRecent
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Tax], error) {
		return r.Taxes.List(ctx, org, f, page)
	})
}

// AppliedToOrganization reports whether t is a default tax of the
// organization's default billing entity (lago
// TaxWithBillingEntitiesSerializer#applied_to_organization?).
func (s *Service) AppliedToOrganization(ctx context.Context, t *catalog.Tax) (bool, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (bool, error) {
		be, err := r.BillingEntities.GetDefault(ctx, t.OrganizationID)
		if errors.Is(err, core.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("load default billing entity: %w", err)
		}
		return slices.Contains(be.TaxIDs, t.ID), nil
	})
}

// Application is where a tax applies by default.
type Application struct {
	// ToOrganization reports whether the tax is a default tax of the
	// organization's default billing entity.
	ToOrganization bool
	// BillingEntityCodes are the codes of the billing entities, archived
	// ones included, that have the tax as a default tax, in code order.
	BillingEntityCodes []string
}

// Applications returns where each of ts applies by default, by tax ID
// (lago TaxWithBillingEntitiesSerializer).
//
// source: app/serializers/v1/tax_with_billing_entities_serializer.rb:5-21
func (s *Service) Applications(ctx context.Context, org core.OrganizationID, ts []*catalog.Tax) (map[core.TaxID]Application, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (map[core.TaxID]Application, error) {
		entities, err := kernel.All(func(p core.PageRequest) (core.Page[*organization.BillingEntity], error) {
			return r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{IncludeArchived: true}, p)
		})
		if err != nil {
			return nil, fmt.Errorf("list billing entities: %w", err)
		}
		out := make(map[core.TaxID]Application, len(ts))
		for _, t := range ts {
			a := Application{BillingEntityCodes: []string{}}
			for _, be := range entities {
				if !slices.Contains(be.TaxIDs, t.ID) {
					continue
				}
				a.BillingEntityCodes = append(a.BillingEntityCodes, be.Code)
				a.ToOrganization = a.ToOrganization || be.IsDefault
			}
			slices.Sort(a.BillingEntityCodes)
			out[t.ID] = a
		}
		return out, nil
	})
}

// applyToOrganization attaches t to (apply) or detaches it from the
// default billing entity and flags that entity's drafts for refresh.
//
// source: app/services/billing_entities/taxes/apply_taxes_service.rb:15-29
// source: app/services/billing_entities/taxes/remove_taxes_service.rb:15-25
func (s *Service) applyToOrganization(ctx context.Context, r port.Repos, t *catalog.Tax, apply bool) error {
	be, err := r.BillingEntities.GetDefault(ctx, t.OrganizationID)
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound("billing_entity")
	}
	if err != nil {
		return fmt.Errorf("load default billing entity: %w", err)
	}
	has := slices.Contains(be.TaxIDs, t.ID)
	switch {
	case apply && !has:
		be.TaxIDs = append(slices.Clone(be.TaxIDs), t.ID)
	case !apply && has:
		be.TaxIDs = slices.DeleteFunc(slices.Clone(be.TaxIDs), func(id core.TaxID) bool { return id == t.ID })
	}
	if apply != has {
		be.UpdatedAt = s.d.Now()
		if err := r.BillingEntities.Update(ctx, be); err != nil {
			return fmt.Errorf("update billing entity %s: %w", be.ID, err)
		}
	}
	// Lago refreshes the entity's drafts even when nothing changed
	// (RefreshDraftInvoicesJob).
	ds, err := entityDrafts(ctx, r, be)
	if err != nil {
		return err
	}
	return s.requestRefresh(ctx, r, ds)
}

// requestRefresh flags the draft invoices ds for refresh, once each.
func (s *Service) requestRefresh(ctx context.Context, r port.Repos, ds []*invoice.Invoice) error {
	done := map[core.InvoiceID]bool{}
	now := s.d.Now()
	for _, inv := range ds {
		if done[inv.ID] || inv.RefreshRequested {
			continue
		}
		done[inv.ID] = true
		inv.RefreshRequested = true
		inv.UpdatedAt = now
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("flag draft invoice %s: %w", inv.ID, err)
		}
	}
	return nil
}

// drafts returns the draft invoices of the customers the tax id applies
// to: the kept customers that have the tax, and those without taxes of
// their own whose billing entity has it.
//
// source: app/models/tax.rb:57-71
func drafts(ctx context.Context, r port.Repos, t *catalog.Tax) ([]*invoice.Invoice, error) {
	all, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, t.OrganizationID, invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list draft invoices: %w", err)
	}
	customers := map[core.CustomerID]bool{}
	entities := map[core.BillingEntityID]*organization.BillingEntity{}
	var out []*invoice.Invoice
	for _, inv := range all {
		ok, seen := customers[inv.CustomerID]
		if !seen {
			c, err := r.Customers.Get(ctx, t.OrganizationID, inv.CustomerID)
			if err != nil {
				return nil, fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
			}
			be, cached := entities[c.BillingEntityID]
			if !cached {
				if be, err = r.BillingEntities.Get(ctx, t.OrganizationID, c.BillingEntityID); err != nil {
					return nil, fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
				}
				entities[c.BillingEntityID] = be
			}
			ok = applies(t.ID, c, be)
			customers[inv.CustomerID] = ok
		}
		if ok {
			out = append(out, inv)
		}
	}
	return out, nil
}

// entityDrafts returns the draft invoices of the billing entity be.
//
// source: app/jobs/billing_entities/taxes/refresh_draft_invoices_job.rb:8-16
func entityDrafts(ctx context.Context, r port.Repos, be *organization.BillingEntity) ([]*invoice.Invoice, error) {
	ds, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, be.OrganizationID, invoice.Filter{BillingEntityID: be.ID, Statuses: []invoice.Status{invoice.StatusDraft}}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list draft invoices of billing entity %s: %w", be.ID, err)
	}
	return ds, nil
}

// validate checks t as lago's model does: the domain rules, a rate given
// (hasRate) and the code's uniqueness, reported together.
//
// source: app/models/tax.rb:41-42
func validate(ctx context.Context, r port.Repos, t *catalog.Tax, hasRate bool) error {
	var v core.Validator
	v.Merge("", t.Validate())
	v.Check(hasRate, "rate", core.CodeRequired)
	if t.Code != "" {
		other, err := r.Taxes.GetByCode(ctx, t.OrganizationID, t.Code)
		switch {
		case err == nil:
			v.Check(other.ID == t.ID, "code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("check code: %w", err)
		}
	}
	return v.Err()
}

// applies reports whether the tax id applies to the customer c, whose
// billing entity is be: archived customers are out of lago's default scope
// (Tax#applicable_customers).
func applies(id core.TaxID, c *customer.Customer, be *organization.BillingEntity) bool {
	if c.DeletedAt != nil {
		return false
	}
	if len(c.TaxIDs) > 0 {
		return slices.Contains(c.TaxIDs, id)
	}
	return slices.Contains(be.TaxIDs, id)
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

// notFound turns a missing tax into core.NotFound("tax").
// kept returns the kept tax id; a missing or discarded one is not found.
func kept(ctx context.Context, r port.Repos, org core.OrganizationID, id core.TaxID) (*catalog.Tax, error) {
	t, err := r.Taxes.Get(ctx, org, id)
	if err != nil {
		return nil, notFound(err)
	}
	if t.DeletedAt != nil {
		return nil, core.NotFound(Resource)
	}
	return t, nil
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
