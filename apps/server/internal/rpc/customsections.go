package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/customsections"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1/invoicev1connect"
)

// InvoiceCustomSections serves tanvo.invoice.v1.InvoiceCustomSectionService
// over the billing library's custom section service (lago
// InvoiceCustomSection GraphQL mutations and resolvers).
type InvoiceCustomSections struct {
	invoicev1connect.UnimplementedInvoiceCustomSectionServiceHandler

	svc *customsections.Service
}

var _ invoicev1connect.InvoiceCustomSectionServiceHandler = (*InvoiceCustomSections)(nil)

// NewInvoiceCustomSections returns the service over svc.
func NewInvoiceCustomSections(svc *customsections.Service) *InvoiceCustomSections {
	return &InvoiceCustomSections{svc: svc}
}

var icsTypes = NewEnum(map[invoice.CustomSectionType]invoicev1.InvoiceCustomSectionType{
	invoice.CustomSectionTypeManual:          invoicev1.InvoiceCustomSectionType_INVOICE_CUSTOM_SECTION_TYPE_MANUAL,
	invoice.CustomSectionTypeSystemGenerated: invoicev1.InvoiceCustomSectionType_INVOICE_CUSTOM_SECTION_TYPE_SYSTEM_GENERATED,
})

// icsUpdateFields are the update_mask paths of UpdateInvoiceCustomSection;
// the code stays editable, as in lago's UpdateInput.
var icsUpdateFields = []string{"code", "name", "display_name", "details", "description"}

// CreateInvoiceCustomSection implements
// InvoiceCustomSectionService.CreateInvoiceCustomSection: a manual section.
func (s *InvoiceCustomSections) CreateInvoiceCustomSection(ctx context.Context, req *invoicev1.CreateInvoiceCustomSectionRequest) (*invoicev1.CreateInvoiceCustomSectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetInvoiceCustomSection()
	sec, err := s.svc.Create(ctx, p.OrganizationID, customsections.CreateInput{
		Code:        b.GetCode(),
		Name:        b.GetName(),
		DisplayName: Opt(b.HasDisplayName(), b.GetDisplayName()),
		Details:     Opt(b.HasDetails(), b.GetDetails()),
		Description: Opt(b.HasDescription(), b.GetDescription()),
	})
	if err != nil {
		return nil, icsError(ctx, err)
	}
	return invoicev1.CreateInvoiceCustomSectionResponse_builder{InvoiceCustomSection: invoiceCustomSection(sec)}.Build(), nil
}

// GetInvoiceCustomSection implements
// InvoiceCustomSectionService.GetInvoiceCustomSection.
func (s *InvoiceCustomSections) GetInvoiceCustomSection(ctx context.Context, req *invoicev1.GetInvoiceCustomSectionRequest) (*invoicev1.GetInvoiceCustomSectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	sec, err := s.lookup(p.OrganizationID).Find(ctx, "invoice_custom_section_id", req.GetInvoiceCustomSectionId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.GetInvoiceCustomSectionResponse_builder{InvoiceCustomSection: invoiceCustomSection(sec)}.Build(), nil
}

// ListInvoiceCustomSections implements
// InvoiceCustomSectionService.ListInvoiceCustomSections: the manual
// sections by name. It takes no filter or search (lago
// InvoiceCustomSectionsResolver has neither).
func (s *InvoiceCustomSections) ListInvoiceCustomSections(ctx context.Context, req *invoicev1.ListInvoiceCustomSectionsRequest) (*invoicev1.ListInvoiceCustomSectionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetFilter() != "" {
		return nil, filterError("invoice custom sections take no filter")
	}
	if req.GetSearch() != "" {
		return nil, InvalidArgument("invalid search", []FieldViolation{{
			Field: "search", Reason: core.CodeInvalid, Description: "invoice custom sections take no search",
		}})
	}
	page, err := s.svc.List(ctx, p.OrganizationID, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.ListInvoiceCustomSectionsResponse_builder{
		InvoiceCustomSections: Items(page, invoiceCustomSection),
		NextPageToken:         new(page.NextToken),
	}.Build(), nil
}

// UpdateInvoiceCustomSection implements
// InvoiceCustomSectionService.UpdateInvoiceCustomSection.
// invoice_custom_section.id is an ID or code:<code>; the fields updated
// are the update_mask paths or, without one, the fields set.
func (s *InvoiceCustomSections) UpdateInvoiceCustomSection(ctx context.Context, req *invoicev1.UpdateInvoiceCustomSectionRequest) (*invoicev1.UpdateInvoiceCustomSectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetInvoiceCustomSection()
	paths, err := UpdatePaths(req.GetUpdateMask(), icsUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "display_name":
			return b.HasDisplayName()
		case "details":
			return b.HasDetails()
		case "description":
			return b.HasDescription()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "invoice_custom_section.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in customsections.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "display_name":
			in.DisplayName = core.Some(Opt(b.HasDisplayName(), b.GetDisplayName()))
		case "details":
			in.Details = core.Some(Opt(b.HasDetails(), b.GetDetails()))
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		}
	}
	sec, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, icsError(ctx, err)
	}
	return invoicev1.UpdateInvoiceCustomSectionResponse_builder{InvoiceCustomSection: invoiceCustomSection(sec)}.Build(), nil
}

// DeleteInvoiceCustomSection implements
// InvoiceCustomSectionService.DeleteInvoiceCustomSection: the section and
// its selections are deleted.
func (s *InvoiceCustomSections) DeleteInvoiceCustomSection(ctx context.Context, req *invoicev1.DeleteInvoiceCustomSectionRequest) (*invoicev1.DeleteInvoiceCustomSectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "invoice_custom_section_id", req.GetInvoiceCustomSectionId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID); err != nil {
		return nil, Error(ctx, err)
	}
	return &invoicev1.DeleteInvoiceCustomSectionResponse{}, nil
}

func (s *InvoiceCustomSections) lookup(org core.OrganizationID) Lookup[core.InvoiceCustomSectionID, *invoice.CustomSection] {
	return Lookup[core.InvoiceCustomSectionID, *invoice.CustomSection]{
		ByID: func(ctx context.Context, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*invoice.CustomSection, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// icsError maps the service errors, whose field paths are the section's,
// onto the request's invoice_custom_section message.
func icsError(ctx context.Context, err error) error {
	if v, ok := validationViolations("invoice_custom_section.", err); ok {
		return withReason(InvalidArgument("invalid invoice custom section", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// invoiceCustomSection converts a section to its proto message.
func invoiceCustomSection(s *invoice.CustomSection) *invoicev1.InvoiceCustomSection {
	return invoicev1.InvoiceCustomSection_builder{
		Id:          new(s.ID.String()),
		Code:        new(s.Code),
		Name:        new(s.Name),
		DisplayName: s.DisplayName,
		Details:     s.Details,
		Description: s.Description,
		SectionType: new(icsTypes.Proto(s.SectionType)),
		CreatedAt:   Timestamp(s.CreatedAt),
		UpdatedAt:   Timestamp(s.UpdatedAt),
	}.Build()
}
