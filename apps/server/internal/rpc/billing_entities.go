package rpc

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	organizationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1/organizationv1connect"
)

// BillingEntities serves tanvo.organization.v1.BillingEntityService over
// the billing library's billing entity service (lago
// Api::V1::BillingEntitiesController and the BillingEntities GraphQL
// mutations). ArchiveBillingEntity is not served: no released version of
// the reference archives billing entities (its destroy mutation is a
// no-op).
type BillingEntities struct {
	organizationv1connect.UnimplementedBillingEntityServiceHandler

	svc *billingentities.Service
}

var _ organizationv1connect.BillingEntityServiceHandler = (*BillingEntities)(nil)

// NewBillingEntities returns the service over svc.
func NewBillingEntities(svc *billingentities.Service) *BillingEntities {
	return &BillingEntities{svc: svc}
}

var (
	beDocumentNumberings = NewEnum(map[organization.DocumentNumbering]organizationv1.DocumentNumbering{
		organization.DocumentNumberingPerCustomer:      organizationv1.DocumentNumbering_DOCUMENT_NUMBERING_PER_CUSTOMER,
		organization.DocumentNumberingPerBillingEntity: organizationv1.DocumentNumbering_DOCUMENT_NUMBERING_PER_BILLING_ENTITY,
	})
	beEmailSettings = NewEnum(map[organization.EmailSetting]organizationv1.EmailSetting{
		organization.EmailSettingInvoiceFinalized:      organizationv1.EmailSetting_EMAIL_SETTING_INVOICE_FINALIZED,
		organization.EmailSettingCreditNoteCreated:     organizationv1.EmailSetting_EMAIL_SETTING_CREDIT_NOTE_CREATED,
		organization.EmailSettingPaymentReceiptCreated: organizationv1.EmailSetting_EMAIL_SETTING_PAYMENT_RECEIPT_CREATED,
	})
	beIssuingDateAnchors = NewEnum(map[core.IssuingDateAnchor]commonv1.IssuingDateAnchor{
		core.IssuingDateAnchorCurrentPeriodEnd: commonv1.IssuingDateAnchor_ISSUING_DATE_ANCHOR_CURRENT_PERIOD_END,
		core.IssuingDateAnchorNextPeriodStart:  commonv1.IssuingDateAnchor_ISSUING_DATE_ANCHOR_NEXT_PERIOD_START,
	})
	beIssuingDateAdjustments = NewEnum(map[core.IssuingDateAdjustment]commonv1.IssuingDateAdjustment{
		core.IssuingDateAdjustmentKeepAnchor:                commonv1.IssuingDateAdjustment_ISSUING_DATE_ADJUSTMENT_KEEP_ANCHOR,
		core.IssuingDateAdjustmentAlignWithFinalizationDate: commonv1.IssuingDateAdjustment_ISSUING_DATE_ADJUSTMENT_ALIGN_WITH_FINALIZATION_DATE,
	})
)

// beFilterFields are the fields ListBillingEntities filters on.
var beFilterFields = []string{"code", "name", "is_default", "archived"}

// beAddressFields are the update_mask paths of the address's fields.
var beAddressFields = []string{
	"address.line1", "address.line2", "address.city", "address.state", "address.zipcode", "address.country",
}

// beUpdateFields are the update_mask paths of UpdateBillingEntity;
// "address" stands for every address field.
var beUpdateFields = slices.Concat([]string{
	"name", "legal_name", "legal_number", "tax_identification_number", "email", "phone", "address",
}, beAddressFields, []string{
	"logo", "timezone", "default_currency", "document_locale", "document_numbering", "document_number_prefix",
	"invoice_footer", "invoice_grace_period_days", "net_payment_term_days", "finalize_zero_amount_invoice",
	"issuing_date_anchor", "issuing_date_adjustment", "email_settings", "eu_tax_management", "einvoicing",
	"applied_dunning_campaign_id", "tax_ids", "invoice_custom_section_ids",
})

// CreateBillingEntity implements BillingEntityService.CreateBillingEntity
// (lago BillingEntities::CreateService). Every field set is assigned,
// taxes, invoice custom sections and the dunning campaign included.
func (s *BillingEntities) CreateBillingEntity(ctx context.Context, req *organizationv1.CreateBillingEntityRequest) (*organizationv1.CreateBillingEntityResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetBillingEntity()
	paths, err := UpdatePaths(nil, beUpdateFields, beHas(b))
	if err != nil {
		return nil, err
	}
	in, err := beInput(b, paths)
	if err != nil {
		return nil, err
	}
	be, err := s.svc.Create(ctx, p.OrganizationID, b.GetCode(), in)
	if err != nil {
		return nil, beError(ctx, err)
	}
	out, err := s.billingEntity(ctx, be)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return organizationv1.CreateBillingEntityResponse_builder{BillingEntity: out}.Build(), nil
}

// GetBillingEntity implements BillingEntityService.GetBillingEntity:
// billing_entity_id is an ID or code:<code>; archived entities are found
// too (lago billing_entities_controller.rb:15-18).
func (s *BillingEntities) GetBillingEntity(ctx context.Context, req *organizationv1.GetBillingEntityRequest) (*organizationv1.GetBillingEntityResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	be, err := s.beLookup(p.OrganizationID).Find(ctx, "billing_entity_id", req.GetBillingEntityId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.billingEntity(ctx, be)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return organizationv1.GetBillingEntityResponse_builder{BillingEntity: out}.Build(), nil
}

// ListBillingEntities implements BillingEntityService.ListBillingEntities:
// the active entities in creation order (lago
// Organization#billing_entities), filtered on code, name, is_default and
// archived (archived = true lists the archived ones).
func (s *BillingEntities) ListBillingEntities(ctx context.Context, req *organizationv1.ListBillingEntitiesRequest) (*organizationv1.ListBillingEntitiesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := beListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*organizationv1.BillingEntity, len(page.Items))
	for i, be := range page.Items {
		if out[i], err = s.billingEntity(ctx, be); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return organizationv1.ListBillingEntitiesResponse_builder{
		BillingEntities: out,
		NextPageToken:   new(page.NextToken),
	}.Build(), nil
}

// UpdateBillingEntity implements BillingEntityService.UpdateBillingEntity
// (lago BillingEntities::UpdateService and the ApplyTaxes, RemoveTaxes and
// UpdateAppliedDunningCampaign mutations). billing_entity.id is an ID or
// code:<code>. The fields updated are the update_mask paths or, without
// one, the fields set (repeated fields when not empty); the code never
// changes.
func (s *BillingEntities) UpdateBillingEntity(ctx context.Context, req *organizationv1.UpdateBillingEntityRequest) (*organizationv1.UpdateBillingEntityResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetBillingEntity()
	paths, err := UpdatePaths(req.GetUpdateMask(), beUpdateFields, beHas(b))
	if err != nil {
		return nil, err
	}
	in, err := beInput(b, paths)
	if err != nil {
		return nil, err
	}
	cur, err := s.beLookup(p.OrganizationID).Find(ctx, "billing_entity.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	be, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, beError(ctx, err)
	}
	out, err := s.billingEntity(ctx, be)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return organizationv1.UpdateBillingEntityResponse_builder{BillingEntity: out}.Build(), nil
}

func (s *BillingEntities) beLookup(org core.OrganizationID) Lookup[core.BillingEntityID, *organization.BillingEntity] {
	return Lookup[core.BillingEntityID, *organization.BillingEntity]{
		ByID: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// beHas reports whether the field of a path is set in b.
func beHas(b *organizationv1.BillingEntity) func(path string) bool {
	a := b.GetAddress()
	return func(path string) bool {
		switch path {
		case "name":
			return b.HasName()
		case "legal_name":
			return b.HasLegalName()
		case "legal_number":
			return b.HasLegalNumber()
		case "tax_identification_number":
			return b.HasTaxIdentificationNumber()
		case "email":
			return b.HasEmail()
		case "phone":
			return b.HasPhone()
		case "address.line1":
			return a.HasLine1()
		case "address.line2":
			return a.HasLine2()
		case "address.city":
			return a.HasCity()
		case "address.state":
			return a.HasState()
		case "address.zipcode":
			return a.HasZipcode()
		case "address.country":
			return a.HasCountry()
		case "logo":
			return b.HasLogo()
		case "timezone":
			return b.HasTimezone()
		case "default_currency":
			return b.HasDefaultCurrency()
		case "document_locale":
			return b.HasDocumentLocale()
		case "document_numbering":
			return b.HasDocumentNumbering()
		case "document_number_prefix":
			return b.HasDocumentNumberPrefix()
		case "invoice_footer":
			return b.HasInvoiceFooter()
		case "invoice_grace_period_days":
			return b.HasInvoiceGracePeriodDays()
		case "net_payment_term_days":
			return b.HasNetPaymentTermDays()
		case "finalize_zero_amount_invoice":
			return b.HasFinalizeZeroAmountInvoice()
		case "issuing_date_anchor":
			return b.HasIssuingDateAnchor()
		case "issuing_date_adjustment":
			return b.HasIssuingDateAdjustment()
		case "email_settings":
			return len(b.GetEmailSettings()) > 0
		case "eu_tax_management":
			return b.HasEuTaxManagement()
		case "einvoicing":
			return b.HasEinvoicing()
		case "applied_dunning_campaign_id":
			return b.HasAppliedDunningCampaignId()
		case "tax_ids":
			return len(b.GetTaxIds()) > 0
		case "invoice_custom_section_ids":
			return len(b.GetInvoiceCustomSectionIds()) > 0
		}
		return false
	}
}

// beInput returns the service input of the paths of b; "address" expands
// to every address field. Text fields left empty clear the attribute.
func beInput(b *organizationv1.BillingEntity, paths []string) (billingentities.Input, error) {
	var in billingentities.Input
	if slices.Contains(paths, "address") {
		paths = slices.Concat(paths, beAddressFields)
	}
	a := b.GetAddress()
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = core.Some(b.GetName())
		case "legal_name":
			in.LegalName = core.Some(StringPtr(b.GetLegalName()))
		case "legal_number":
			in.LegalNumber = core.Some(StringPtr(b.GetLegalNumber()))
		case "tax_identification_number":
			in.TaxIdentificationNumber = core.Some(StringPtr(b.GetTaxIdentificationNumber()))
		case "email":
			in.Email = core.Some(StringPtr(b.GetEmail()))
		case "phone":
			in.Phone = core.Some(StringPtr(b.GetPhone()))
		case "address.line1":
			in.AddressLine1 = core.Some(a.GetLine1())
		case "address.line2":
			in.AddressLine2 = core.Some(a.GetLine2())
		case "address.city":
			in.City = core.Some(a.GetCity())
		case "address.state":
			in.State = core.Some(a.GetState())
		case "address.zipcode":
			in.Zipcode = core.Some(a.GetZipcode())
		case "address.country":
			in.Country = core.Some(a.GetCountry())
		case "logo":
			in.Logo = core.Some(b.GetLogo())
		case "timezone":
			loc, err := beTimezone(b.GetTimezone())
			if err != nil {
				return in, err
			}
			in.Timezone = core.Some(loc)
		case "default_currency":
			c, err := money.LookupCurrency(b.GetDefaultCurrency())
			if err != nil {
				return in, beViolation("default_currency", core.CodeInvalidCurrency)
			}
			in.DefaultCurrency = core.Some(c)
		case "document_locale":
			in.DocumentLocale = core.Some(b.GetDocumentLocale())
		case "document_numbering":
			in.DocumentNumbering = core.Some(beDocumentNumberings.Core(b.GetDocumentNumbering()))
		case "document_number_prefix":
			in.DocumentNumberPrefix = core.Some(b.GetDocumentNumberPrefix())
		case "invoice_footer":
			in.InvoiceFooter = core.Some(StringPtr(b.GetInvoiceFooter()))
		case "invoice_grace_period_days":
			in.InvoiceGracePeriodDays = core.Some(int(b.GetInvoiceGracePeriodDays()))
		case "net_payment_term_days":
			in.NetPaymentTermDays = core.Some(int(b.GetNetPaymentTermDays()))
		case "finalize_zero_amount_invoice":
			in.FinalizeZeroAmountInvoice = core.Some(b.GetFinalizeZeroAmountInvoice())
		case "issuing_date_anchor":
			in.IssuingDateAnchor = core.Some(beIssuingDateAnchors.Core(b.GetIssuingDateAnchor()))
		case "issuing_date_adjustment":
			in.IssuingDateAdjustment = core.Some(beIssuingDateAdjustments.Core(b.GetIssuingDateAdjustment()))
		case "email_settings":
			settings := make([]organization.EmailSetting, len(b.GetEmailSettings()))
			for i, e := range b.GetEmailSettings() {
				settings[i] = beEmailSettings.Core(e)
			}
			in.EmailSettings = core.Some(settings)
		case "eu_tax_management":
			in.EUTaxManagement = core.Some(b.GetEuTaxManagement())
		case "einvoicing":
			in.EInvoicing = core.Some(b.GetEinvoicing())
		case "applied_dunning_campaign_id":
			r, err := CatalogRef("billing_entity.applied_dunning_campaign_id", b.GetAppliedDunningCampaignId())
			if err != nil {
				return in, err
			}
			in.AppliedDunningCampaign = core.Some(r)
		case "tax_ids":
			rs, err := CatalogRefs("billing_entity.tax_ids", b.GetTaxIds())
			if err != nil {
				return in, err
			}
			in.Taxes = core.Some(rs)
		case "invoice_custom_section_ids":
			rs, err := CatalogRefs("billing_entity.invoice_custom_section_ids", b.GetInvoiceCustomSectionIds())
			if err != nil {
				return in, err
			}
			in.InvoiceCustomSections = core.Some(rs)
		}
	}
	return in, nil
}

// beTimezone loads an IANA time zone; "" and "Local" are invalid.
func beTimezone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, beViolation("timezone", core.CodeInvalidTimezone)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, beViolation("timezone", core.CodeInvalidTimezone)
	}
	return loc, nil
}

// beViolation is the validation error of a billing_entity field.
func beViolation(field, code string) error {
	return withReason(InvalidArgument("invalid billing entity", []FieldViolation{{Field: "billing_entity." + field, Reason: code}}), ReasonValidationErrors)
}

// beError maps the service errors, whose field paths are the entity's,
// onto the request's billing_entity message.
func beError(ctx context.Context, err error) error {
	if v, ok := validationViolations("billing_entity.", err); ok {
		return withReason(InvalidArgument("invalid billing entity", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func beListFilter(s string) (billingentities.Filter, error) {
	var f billingentities.Filter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, beFilterFields...)
	if err != nil {
		return f, err
	}
	if f.Code, err = FilterSingle("code", eq["code"]); err != nil {
		return f, err
	}
	if f.Name, err = FilterSingle("name", eq["name"]); err != nil {
		return f, err
	}
	if f.IsDefault, err = FilterBool("is_default", eq["is_default"]); err != nil {
		return f, err
	}
	if f.Archived, err = FilterBool("archived", eq["archived"]); err != nil {
		return f, err
	}
	return f, nil
}

func (s *BillingEntities) billingEntity(ctx context.Context, be *organization.BillingEntity) (*organizationv1.BillingEntity, error) {
	logo, err := s.svc.LogoURL(ctx, be)
	if err != nil {
		return nil, err
	}
	b := organizationv1.BillingEntity_builder{
		Id:                        new(be.ID.String()),
		Code:                      new(be.Code),
		Name:                      new(be.Name),
		LegalName:                 be.LegalName,
		LegalNumber:               be.LegalNumber,
		TaxIdentificationNumber:   be.TaxID,
		Email:                     be.Email,
		Phone:                     be.Phone,
		Address:                   beAddress(be.Address),
		LogoUrl:                   StringPtr(logo),
		DefaultCurrency:           new(be.DefaultCurrency.Code()),
		DocumentLocale:            new(be.DocumentLocale),
		DocumentNumberPrefix:      new(be.DocumentNumberPrefix),
		InvoiceFooter:             be.InvoiceFooter,
		InvoiceGracePeriodDays:    new(int32(be.InvoiceGracePeriodDays)),
		NetPaymentTermDays:        new(int32(be.NetPaymentTermDays)),
		FinalizeZeroAmountInvoice: new(be.FinalizeZeroAmountInvoice),
		EuTaxManagement:           new(be.EUTaxManagement),
		Einvoicing:                new(be.EInvoicing),
		IsDefault:                 new(be.IsDefault),
		ArchivedAt:                TimestampPtr(be.ArchivedAt),
		CreatedAt:                 Timestamp(be.CreatedAt),
		UpdatedAt:                 Timestamp(be.UpdatedAt),
	}
	if be.Timezone != nil {
		b.Timezone = new(be.Timezone.String())
	}
	if n := beDocumentNumberings.Proto(be.DocumentNumbering); n != 0 {
		b.DocumentNumbering = &n
	}
	if a := beIssuingDateAnchors.Proto(be.IssuingDateAnchor); a != 0 {
		b.IssuingDateAnchor = &a
	}
	if a := beIssuingDateAdjustments.Proto(be.IssuingDateAdjustment); a != 0 {
		b.IssuingDateAdjustment = &a
	}
	for _, e := range be.EmailSettings {
		b.EmailSettings = append(b.EmailSettings, beEmailSettings.Proto(e))
	}
	if !be.AppliedDunningCampaignID.IsZero() {
		b.AppliedDunningCampaignId = new(be.AppliedDunningCampaignID.String())
	}
	for _, id := range be.TaxIDs {
		b.TaxIds = append(b.TaxIds, id.String())
	}
	for _, id := range be.InvoiceCustomSectionIDs {
		b.InvoiceCustomSectionIds = append(b.InvoiceCustomSectionIds, id.String())
	}
	return b.Build(), nil
}

func beAddress(a core.Address) *commonv1.Address {
	if a.IsZero() {
		return nil
	}
	return commonv1.Address_builder{
		Line1:   StringPtr(a.Line1),
		Line2:   StringPtr(a.Line2),
		City:    StringPtr(a.City),
		State:   StringPtr(a.State),
		Zipcode: StringPtr(a.Zipcode),
		Country: StringPtr(a.Country),
	}.Build()
}
