package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/taxes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	taxv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/tax/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/tax/v1/taxv1connect"
)

// Taxes serves tanvo.tax.v1.TaxService over the billing library's tax
// service (lago Api::V1::TaxesController).
type Taxes struct {
	taxv1connect.UnimplementedTaxServiceHandler

	svc *taxes.Service
}

var _ taxv1connect.TaxServiceHandler = (*Taxes)(nil)

// NewTaxes returns the service over svc.
func NewTaxes(svc *taxes.Service) *Taxes { return &Taxes{svc: svc} }

// taxFilterFields are the fields ListTaxes filters on.
var taxFilterFields = []string{"auto_generated", "applied_to_organization"}

// taxUpdateFields are the update_mask paths of UpdateTax.
var taxUpdateFields = []string{"code", "name", "rate", "description", "applied_to_organization"}

// CreateTax implements TaxService.CreateTax.
func (s *Taxes) CreateTax(ctx context.Context, req *taxv1.CreateTaxRequest) (*taxv1.CreateTaxResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetTax()
	in := taxes.CreateInput{
		Code:        b.GetCode(),
		Name:        b.GetName(),
		Description: Opt(b.HasDescription(), b.GetDescription()),

		AppliedToOrganization: b.GetAppliedToOrganization(),
	}
	if b.HasRate() {
		rate, err := ParseDecimal("tax.rate", b.GetRate())
		if err != nil {
			return nil, err
		}
		in.Rate = &rate
	}
	t, err := s.svc.Create(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, taxError(ctx, err)
	}
	out, err := s.proto(ctx, p.OrganizationID, t)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return taxv1.CreateTaxResponse_builder{Tax: out}.Build(), nil
}

// GetTax implements TaxService.GetTax.
func (s *Taxes) GetTax(ctx context.Context, req *taxv1.GetTaxRequest) (*taxv1.GetTaxResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.lookup(p.OrganizationID).Find(ctx, "tax_id", req.GetTaxId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.proto(ctx, p.OrganizationID, t)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return taxv1.GetTaxResponse_builder{Tax: out}.Build(), nil
}

// ListTaxes implements TaxService.ListTaxes: by name, then newest first,
// filtered on auto_generated and applied_to_organization (a tax of the
// default billing entity) and searched by name or code (lago TaxesQuery).
func (s *Taxes) ListTaxes(ctx context.Context, req *taxv1.ListTaxesRequest) (*taxv1.ListTaxesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := taxListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	apps, err := s.svc.Applications(ctx, p.OrganizationID, page.Items)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return taxv1.ListTaxesResponse_builder{
		Taxes:         Items(page, func(t *catalog.Tax) *taxv1.Tax { return taxProto(t, apps[t.ID]) }),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateTax implements TaxService.UpdateTax. tax.id is an ID or
// code:<code>. The fields updated are the update_mask paths or, without
// one, the fields set; the draft invoices of the customers the tax applies
// to are flagged for refresh (lago Taxes::UpdateService).
func (s *Taxes) UpdateTax(ctx context.Context, req *taxv1.UpdateTaxRequest) (*taxv1.UpdateTaxResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetTax()
	paths, err := UpdatePaths(req.GetUpdateMask(), taxUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "rate":
			return b.HasRate()
		case "description":
			return b.HasDescription()
		case "applied_to_organization":
			return b.HasAppliedToOrganization()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "tax.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in taxes.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "rate":
			rate, err := taxRate(b.GetRate())
			if err != nil {
				return nil, err
			}
			in.Rate = core.Some(rate)
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "applied_to_organization":
			in.AppliedToOrganization = core.Some(b.GetAppliedToOrganization())
		}
	}
	t, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, taxError(ctx, err)
	}
	out, err := s.proto(ctx, p.OrganizationID, t)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return taxv1.UpdateTaxResponse_builder{Tax: out}.Build(), nil
}

// DeleteTax implements TaxService.DeleteTax.
func (s *Taxes) DeleteTax(ctx context.Context, req *taxv1.DeleteTaxRequest) (*taxv1.DeleteTaxResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "tax_id", req.GetTaxId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	// The tax shows the billing entities it had: lago serializes the
	// association loaded before detaching it (taxes/destroy_service.rb:25).
	apps, err := s.svc.Applications(ctx, p.OrganizationID, []*catalog.Tax{cur})
	if err != nil {
		return nil, Error(ctx, err)
	}
	t, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return taxv1.DeleteTaxResponse_builder{Tax: taxProto(t, apps[cur.ID])}.Build(), nil
}

// proto returns t with where it applies (lago
// TaxWithBillingEntitiesSerializer).
func (s *Taxes) proto(ctx context.Context, org core.OrganizationID, t *catalog.Tax) (*taxv1.Tax, error) {
	apps, err := s.svc.Applications(ctx, org, []*catalog.Tax{t})
	if err != nil {
		return nil, err
	}
	return taxProto(t, apps[t.ID]), nil
}

func (s *Taxes) lookup(org core.OrganizationID) Lookup[core.TaxID, *catalog.Tax] {
	return Lookup[core.TaxID, *catalog.Tax]{
		ByID:   func(ctx context.Context, id core.TaxID) (*catalog.Tax, error) { return s.svc.Get(ctx, org, id) },
		ByCode: func(ctx context.Context, code string) (*catalog.Tax, error) { return s.svc.GetByCode(ctx, org, code) },
	}
}

// taxRate parses the rate of an update: an empty rate is rate
// value_is_mandatory, as lago's presence validation of a nil rate.
func taxRate(s string) (money.Decimal, error) {
	if s == "" {
		return money.Decimal{}, withReason(InvalidArgument("invalid tax", []FieldViolation{{Field: "tax.rate", Reason: core.CodeRequired}}), ReasonValidationErrors)
	}
	return ParseDecimal("tax.rate", s)
}

// taxError maps the service errors, whose field paths are the tax's, onto
// the request's tax message.
func taxError(ctx context.Context, err error) error {
	if v, ok := validationViolations("tax.", err); ok {
		return withReason(InvalidArgument("invalid tax", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func taxListFilter(s string) (catalog.TaxFilter, error) {
	var f catalog.TaxFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, taxFilterFields...)
	if err != nil {
		return f, err
	}
	if f.AutoGenerated, err = FilterBool("auto_generated", eq["auto_generated"]); err != nil {
		return f, err
	}
	f.AppliedToOrganization, err = FilterBool("applied_to_organization", eq["applied_to_organization"])
	return f, err
}

func taxProto(t *catalog.Tax, a taxes.Application) *taxv1.Tax {
	return taxv1.Tax_builder{
		AppliedToOrganization:         new(a.ToOrganization),
		AppliedToBillingEntitiesCodes: a.BillingEntityCodes,
		Id:                            new(t.ID.String()),
		Code:                          new(t.Code),
		Name:                          new(t.Name),
		Rate:                          new(Decimal(t.Rate)),
		Description:                   t.Description,
		AutoGenerated:                 new(t.AutoGenerated),
		// Lago serializes zero counters (V1::TaxSerializer).
		CustomersCount: new(int32(0)),
		CreatedAt:      Timestamp(t.CreatedAt),
		UpdatedAt:      Timestamp(t.UpdatedAt),
	}.Build()
}
