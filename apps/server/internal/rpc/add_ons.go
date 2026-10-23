package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/addons"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// AddOns serves tanvo.catalog.v1.AddOnService over the billing library's
// add-on service (lago Api::V1::AddOnsController).
type AddOns struct {
	catalogv1connect.UnimplementedAddOnServiceHandler

	svc *addons.Service
}

var _ catalogv1connect.AddOnServiceHandler = (*AddOns)(nil)

// NewAddOns returns the service over svc.
func NewAddOns(svc *addons.Service) *AddOns { return &AddOns{svc: svc} }

// addOnUpdateFields are the update_mask paths of UpdateAddOn.
var addOnUpdateFields = []string{"code", "name", "invoice_display_name", "description", "amount", "tax_ids"}

// CreateAddOn implements AddOnService.CreateAddOn.
func (s *AddOns) CreateAddOn(ctx context.Context, req *catalogv1.CreateAddOnRequest) (*catalogv1.CreateAddOnResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetAddOn()
	amount, currency, err := FromMoney("add_on.amount", b.GetAmount())
	if err != nil {
		return nil, err
	}
	taxes, err := CatalogRefs("add_on.tax_ids", b.GetTaxIds())
	if err != nil {
		return nil, err
	}
	a, err := s.svc.Create(ctx, p.OrganizationID, addons.CreateInput{
		Code:               b.GetCode(),
		Name:               b.GetName(),
		InvoiceDisplayName: Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()),
		Description:        Opt(b.HasDescription(), b.GetDescription()),
		Amount:             amount,
		Currency:           currency,
		Taxes:              taxes,
	})
	if err != nil {
		return nil, addOnError(ctx, err)
	}
	return catalogv1.CreateAddOnResponse_builder{AddOn: addOnProto(a)}.Build(), nil
}

// GetAddOn implements AddOnService.GetAddOn.
func (s *AddOns) GetAddOn(ctx context.Context, req *catalogv1.GetAddOnRequest) (*catalogv1.GetAddOnResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	a, err := s.lookup(p.OrganizationID).Find(ctx, "add_on_id", req.GetAddOnId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetAddOnResponse_builder{AddOn: addOnProto(a)}.Build(), nil
}

// ListAddOns implements AddOnService.ListAddOns: most recent first,
// searched by name or code (lago AddOnsQuery).
func (s *AddOns) ListAddOns(ctx context.Context, req *catalogv1.ListAddOnsRequest) (*catalogv1.ListAddOnsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListAddOnsResponse_builder{
		AddOns:        Items(page, addOnProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateAddOn implements AddOnService.UpdateAddOn. add_on.id is an ID or
// code:<code>. The fields updated are the update_mask paths or, without
// one, the fields set; tax_ids replace the add-on's taxes.
func (s *AddOns) UpdateAddOn(ctx context.Context, req *catalogv1.UpdateAddOnRequest) (*catalogv1.UpdateAddOnResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetAddOn()
	paths, err := UpdatePaths(req.GetUpdateMask(), addOnUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		case "description":
			return b.HasDescription()
		case "amount":
			return b.HasAmount()
		case "tax_ids":
			return len(b.GetTaxIds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "add_on.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in addons.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()))
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "amount":
			amount, currency, err := FromMoney("add_on.amount", b.GetAmount())
			if err != nil {
				return nil, err
			}
			in.Amount, in.Currency = core.Some(amount), core.Some(currency)
		case "tax_ids":
			taxes, err := CatalogRefs("add_on.tax_ids", b.GetTaxIds())
			if err != nil {
				return nil, err
			}
			if taxes == nil {
				taxes = []refs.Ref{}
			}
			in.Taxes = core.Some(taxes)
		}
	}
	a, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, addOnError(ctx, err)
	}
	return catalogv1.UpdateAddOnResponse_builder{AddOn: addOnProto(a)}.Build(), nil
}

// DeleteAddOn implements AddOnService.DeleteAddOn; the add-on's fixed
// charges are deleted with it.
func (s *AddOns) DeleteAddOn(ctx context.Context, req *catalogv1.DeleteAddOnRequest) (*catalogv1.DeleteAddOnResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "add_on_id", req.GetAddOnId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	a, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteAddOnResponse_builder{AddOn: addOnProto(a)}.Build(), nil
}

func (s *AddOns) lookup(org core.OrganizationID) Lookup[core.AddOnID, *catalog.AddOn] {
	return Lookup[core.AddOnID, *catalog.AddOn]{
		ByID:   func(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) { return s.svc.Get(ctx, org, id) },
		ByCode: func(ctx context.Context, code string) (*catalog.AddOn, error) { return s.svc.GetByCode(ctx, org, code) },
	}
}

// addOnError maps the service errors, whose field paths are the add-on's,
// onto the request's add_on message.
func addOnError(ctx context.Context, err error) error {
	if v, ok := validationViolations("add_on.", err); ok {
		return withReason(InvalidArgument("invalid add-on", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func addOnProto(a *catalog.AddOn) *catalogv1.AddOn {
	taxIDs := make([]string, len(a.TaxIDs))
	for i, id := range a.TaxIDs {
		taxIDs[i] = id.String()
	}
	return catalogv1.AddOn_builder{
		Id:                 new(a.ID.String()),
		Code:               new(a.Code),
		Name:               new(a.Name),
		InvoiceDisplayName: a.InvoiceDisplayName,
		Description:        a.Description,
		Amount:             Money(a.Amount, a.Currency),
		TaxIds:             taxIDs,
		CreatedAt:          Timestamp(a.CreatedAt),
		UpdatedAt:          Timestamp(a.UpdatedAt),
	}.Build()
}
