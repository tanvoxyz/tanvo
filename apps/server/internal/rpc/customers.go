package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1/customerv1connect"
)

// Customers serves tanvo.customer.v1.CustomerService over the billing
// library's customer service (lago Api::V1::CustomersController, its
// portal_url and checkout_url actions, and the integration customer
// services of the GraphQL API).
type Customers struct {
	customerv1connect.UnimplementedCustomerServiceHandler

	svc    *customers.Service
	portal *auth.PortalTokens
	tx     port.Transactor
}

var _ customerv1connect.CustomerServiceHandler = (*Customers)(nil)

// NewCustomers returns the service over svc. portal issues the customer
// portal URLs and tx reads their customer; a nil portal leaves
// GeneratePortalUrl unavailable.
func NewCustomers(svc *customers.Service, portal *auth.PortalTokens, tx port.Transactor) *Customers {
	return &Customers{svc: svc, portal: portal, tx: tx}
}

var (
	customerTypes = NewEnum(map[customer.Type]customerv1.CustomerType{
		customer.TypeCompany:    customerv1.CustomerType_CUSTOMER_TYPE_COMPANY,
		customer.TypeIndividual: customerv1.CustomerType_CUSTOMER_TYPE_INDIVIDUAL,
	})
	customerAccountTypes = NewEnum(map[customer.AccountType]customerv1.AccountType{
		customer.AccountTypeCustomer: customerv1.AccountType_ACCOUNT_TYPE_CUSTOMER,
		customer.AccountTypePartner:  customerv1.AccountType_ACCOUNT_TYPE_PARTNER,
	})
	customerZeroAmountFinalizations = NewEnum(map[customer.ZeroAmountFinalization]customerv1.ZeroAmountFinalization{
		customer.ZeroAmountFinalizationInherit:  customerv1.ZeroAmountFinalization_ZERO_AMOUNT_FINALIZATION_INHERIT,
		customer.ZeroAmountFinalizationSkip:     customerv1.ZeroAmountFinalization_ZERO_AMOUNT_FINALIZATION_SKIP,
		customer.ZeroAmountFinalizationFinalize: customerv1.ZeroAmountFinalization_ZERO_AMOUNT_FINALIZATION_FINALIZE,
	})
)

// customerAddressFields are the fields of a customer address.
var customerAddressFields = []string{"line1", "line2", "city", "state", "zipcode", "country"}

// customerUpdateFields are the update_mask paths of UpdateCustomer;
// "billing_address" and "shipping_address" stand for each of their
// fields.
var customerUpdateFields = slices.Concat([]string{
	"external_id", "billing_entity_id", "customer_type", "account_type", "name", "firstname", "lastname",
	"legal_name", "legal_number", "tax_identification_number", "email", "phone", "url", "logo_url",
	"billing_address", "shipping_address",
}, customerAddressPaths("billing_address"), customerAddressPaths("shipping_address"), []string{
	"currency", "timezone", "document_locale", "invoice_grace_period_days", "net_payment_term_days",
	"finalize_zero_amount_invoice", "issuing_date_anchor", "issuing_date_adjustment",
	"skip_invoice_custom_sections", "invoice_custom_section_ids", "applied_dunning_campaign_id",
	"exclude_from_dunning_campaign", "tax_ids", "metadata", "payment_method_type", "x402_agent_address",
})

func customerAddressPaths(prefix string) []string {
	out := make([]string, len(customerAddressFields))
	for i, f := range customerAddressFields {
		out[i] = prefix + "." + f
	}
	return out
}

// customerFilterFields are the fields ListCustomers filters on besides
// metadata.<key>.
var customerFilterFields = []string{
	"external_id", "billing_entity_id", "customer_type", "has_customer_type", "account_type", "currency",
	"billing_address.country", "billing_address.state", "billing_address.zipcode",
	"has_tax_identification_number", "deleted",
	"active_subscriptions_count_from", "active_subscriptions_count_to",
}

// CreateCustomer implements CustomerService.CreateCustomer (lago POST
// /api/v1/customers, Customers::UpsertFromApiService, for a new external
// ID). An external ID a live customer has is customer.external_id
// value_already_exist: UpdateCustomer with allow_missing upserts.
func (s *Customers) CreateCustomer(ctx context.Context, req *customerv1.CreateCustomerRequest) (*customerv1.CreateCustomerResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCustomer()
	paths, err := UpdatePaths(nil, customerUpdateFields, customerHas(b))
	if err != nil {
		return nil, err
	}
	paths = slices.DeleteFunc(paths, func(p string) bool { return p == "external_id" })
	in, err := customerInput(b, paths)
	if err != nil {
		return nil, err
	}
	c, err := s.svc.Create(ctx, p.OrganizationID, b.GetExternalId(), in)
	if err != nil {
		return nil, customerError(ctx, err)
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.CreateCustomerResponse_builder{Customer: out}.Build(), nil
}

// GetCustomer implements CustomerService.GetCustomer (lago GET
// /api/v1/customers/:external_id): customer_id is an ID, which finds
// deleted customers too, or external_id:<external_id>, which finds the
// live customer.
func (s *Customers) GetCustomer(ctx context.Context, req *customerv1.GetCustomerRequest) (*customerv1.GetCustomerResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.GetCustomerResponse_builder{Customer: out}.Build(), nil
}

// ListCustomers implements CustomerService.ListCustomers (lago GET
// /api/v1/customers, CustomersQuery): the live customers, newest first,
// filtered and searched.
func (s *Customers) ListCustomers(ctx context.Context, req *customerv1.ListCustomersRequest) (*customerv1.ListCustomersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.listFilter(ctx, p.OrganizationID, req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	if f.Order, err = customerOrder(req.GetOrderBy()); err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, customerError(ctx, err)
	}
	views, err := s.svc.Views(ctx, page.Items...)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*customerv1.Customer, len(views))
	for i, v := range views {
		out[i] = customerProto(v)
	}
	return customerv1.ListCustomersResponse_builder{
		Customers:     out,
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateCustomer implements CustomerService.UpdateCustomer (lago
// Customers::UpdateService and, for an existing external ID, POST
// /api/v1/customers). customer.id is an ID or external_id:<external_id>;
// with allow_missing and an external ID that no live customer has, the
// customer is created (lago Customers::UpsertFromApiService). The fields
// updated are the update_mask paths or, without one, the fields set
// (repeated fields when not empty).
func (s *Customers) UpdateCustomer(ctx context.Context, req *customerv1.UpdateCustomerRequest) (*customerv1.UpdateCustomerResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCustomer()
	paths, err := UpdatePaths(req.GetUpdateMask(), customerUpdateFields, customerHas(b))
	if err != nil {
		return nil, err
	}
	ref, err := ParseRef("customer.id", b.GetId())
	if err != nil {
		return nil, err
	}
	if ref.Code != "" {
		return nil, customerViolation("id", core.CodeInvalid)
	}
	var c *customer.Customer
	if req.GetAllowMissing() && ref.ExternalID != "" {
		if slices.Contains(paths, "external_id") && b.GetExternalId() != ref.ExternalID {
			return nil, customerViolation("external_id", core.CodeInvalid)
		}
		paths = slices.DeleteFunc(paths, func(p string) bool { return p == "external_id" })
		in, err := customerInput(b, paths)
		if err != nil {
			return nil, err
		}
		if c, _, err = s.svc.Upsert(ctx, p.OrganizationID, ref.ExternalID, in); err != nil {
			return nil, customerError(ctx, err)
		}
	} else {
		in, err := customerInput(b, paths)
		if err != nil {
			return nil, err
		}
		cur, err := s.lookup(p.OrganizationID).Find(ctx, "customer.id", b.GetId())
		if err != nil {
			return nil, Error(ctx, err)
		}
		if c, err = s.svc.Update(ctx, p.OrganizationID, cur.ID, in); err != nil {
			return nil, customerError(ctx, err)
		}
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.UpdateCustomerResponse_builder{Customer: out}.Build(), nil
}

// DeleteCustomer implements CustomerService.DeleteCustomer (lago DELETE
// /api/v1/customers/:external_id, Customers::DestroyService): archives the
// customer and queues the termination of its subscriptions, applied
// coupons and wallets and the finalization of its draft invoices.
func (s *Customers) DeleteCustomer(ctx context.Context, req *customerv1.DeleteCustomerRequest) (*customerv1.DeleteCustomerResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	c, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.DeleteCustomerResponse_builder{Customer: out}.Build(), nil
}

// GenerateCheckoutUrl implements CustomerService.GenerateCheckoutUrl (lago
// Customers::GenerateCheckoutUrlService). The checks run; generating the
// provider's URL needs the payment provider clients, so it is
// Unimplemented until they are wired.
func (s *Customers) GenerateCheckoutUrl(ctx context.Context, req *customerv1.GenerateCheckoutUrlRequest) (*customerv1.GenerateCheckoutUrlResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var ref refs.Ref
	if req.HasIntegrationId() {
		if ref, err = CatalogRef("integration_id", req.GetIntegrationId()); err != nil {
			return nil, err
		}
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	u, exp, err := s.svc.CheckoutURL(ctx, p.OrganizationID, c.ID, ref)
	if errors.Is(err, customers.ErrCheckoutUnavailable) {
		return nil, connect.NewError(connect.CodeUnimplemented, "checkout URLs are not available")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.GenerateCheckoutUrlResponse_builder{
		CheckoutUrl: new(u),
		ExpiresAt:   TimestampPtr(nonZero(exp)),
	}.Build(), nil
}

// GeneratePortalUrl implements CustomerService.GeneratePortalUrl (lago
// CustomerPortal::GenerateUrlService): a URL of the customer portal valid
// for 12 hours.
func (s *Customers) GeneratePortalUrl(ctx context.Context, req *customerv1.GeneratePortalUrlRequest) (*customerv1.GeneratePortalUrlResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if s.portal == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "the customer portal is not configured")
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	u, exp, err := s.portal.GenerateExpiringURL(ctx, s.tx, p.OrganizationID, c.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return customerv1.GeneratePortalUrlResponse_builder{
		PortalUrl: new(u),
		ExpiresAt: Timestamp(exp),
	}.Build(), nil
}

// ListCustomerConnections implements
// CustomerService.ListCustomerConnections: the customer's payment
// provider customers, then its integration customers.
func (s *Customers) ListCustomerConnections(ctx context.Context, req *customerv1.ListCustomerConnectionsRequest) (*customerv1.ListCustomerConnectionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	conns, err := s.svc.ListConnections(ctx, p.OrganizationID, c.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*customerv1.CustomerConnection, len(conns))
	for i, cn := range conns {
		out[i] = connectionProto(cn)
	}
	return customerv1.ListCustomerConnectionsResponse_builder{Connections: out}.Build(), nil
}

// UpsertCustomerConnection implements
// CustomerService.UpsertCustomerConnection (lago
// PaymentProviderCustomers::CreateOrUpdateBatchService,
// IntegrationCustomers::CreateOrUpdateBatchService and
// IntegrationCustomers::SetAsDefaultService). The fields set are
// assigned.
func (s *Customers) UpsertCustomerConnection(ctx context.Context, req *customerv1.UpsertCustomerConnectionRequest) (*customerv1.UpsertCustomerConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetConnection()
	ref, err := CatalogRef("connection.integration_id", b.GetIntegrationId())
	if err != nil {
		return nil, err
	}
	var in customers.ConnectionInput
	if b.HasExternalCustomerId() {
		in.ExternalCustomerID = core.Some(b.GetExternalCustomerId())
	}
	if b.HasSyncWithProvider() {
		in.SyncWithProvider = core.Some(b.GetSyncWithProvider())
	}
	if b.HasSubsidiaryId() {
		in.SubsidiaryID = core.Some(StringPtr(b.GetSubsidiaryId()))
	}
	if len(b.GetProviderPaymentMethods()) > 0 {
		in.ProviderPaymentMethods = core.Some(slices.Clone(b.GetProviderPaymentMethods()))
	}
	if b.HasIsDefault() {
		in.IsDefault = core.Some(b.GetIsDefault())
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	cn, err := s.svc.UpsertConnection(ctx, p.OrganizationID, c.ID, ref, in)
	if err != nil {
		return nil, connectionError(ctx, err)
	}
	return customerv1.UpsertCustomerConnectionResponse_builder{Connection: connectionProto(cn)}.Build(), nil
}

// DeleteCustomerConnection implements
// CustomerService.DeleteCustomerConnection (lago
// IntegrationCustomers::DestroyService for integrations; a payment
// provider customer is discarded).
func (s *Customers) DeleteCustomerConnection(ctx context.Context, req *customerv1.DeleteCustomerConnectionRequest) (*customerv1.DeleteCustomerConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("integration_id", req.GetIntegrationId())
	if err != nil {
		return nil, err
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if err := s.svc.DeleteConnection(ctx, p.OrganizationID, c.ID, ref); err != nil {
		return nil, Error(ctx, err)
	}
	return &customerv1.DeleteCustomerConnectionResponse{}, nil
}

func (s *Customers) lookup(org core.OrganizationID) Lookup[core.CustomerID, *customer.Customer] {
	return Lookup[core.CustomerID, *customer.Customer]{
		ByID: func(ctx context.Context, id core.CustomerID) (*customer.Customer, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByExternalID: func(ctx context.Context, externalID string) (*customer.Customer, error) {
			return s.svc.GetByExternalID(ctx, org, externalID)
		},
	}
}

func (s *Customers) customer(ctx context.Context, c *customer.Customer) (*customerv1.Customer, error) {
	views, err := s.svc.Views(ctx, c)
	if err != nil {
		return nil, err
	}
	return customerProto(views[0]), nil
}

// customerHas reports whether the field of a path is set in b.
func customerHas(b *customerv1.Customer) func(path string) bool {
	addr := func(a *commonv1.Address, f string) bool {
		switch f {
		case "line1":
			return a.HasLine1()
		case "line2":
			return a.HasLine2()
		case "city":
			return a.HasCity()
		case "state":
			return a.HasState()
		case "zipcode":
			return a.HasZipcode()
		case "country":
			return a.HasCountry()
		}
		return false
	}
	return func(path string) bool {
		if f, ok := strings.CutPrefix(path, "billing_address."); ok {
			return addr(b.GetBillingAddress(), f)
		}
		if f, ok := strings.CutPrefix(path, "shipping_address."); ok {
			return addr(b.GetShippingAddress(), f)
		}
		switch path {
		case "external_id":
			return b.HasExternalId()
		case "billing_entity_id":
			return b.HasBillingEntityId()
		case "customer_type":
			return b.HasCustomerType()
		case "account_type":
			return b.HasAccountType()
		case "name":
			return b.HasName()
		case "firstname":
			return b.HasFirstname()
		case "lastname":
			return b.HasLastname()
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
		case "url":
			return b.HasUrl()
		case "logo_url":
			return b.HasLogoUrl()
		case "currency":
			return b.HasCurrency()
		case "timezone":
			return b.HasTimezone()
		case "document_locale":
			return b.HasDocumentLocale()
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
		case "skip_invoice_custom_sections":
			return b.HasSkipInvoiceCustomSections()
		case "invoice_custom_section_ids":
			return len(b.GetInvoiceCustomSectionIds()) > 0
		case "applied_dunning_campaign_id":
			return b.HasAppliedDunningCampaignId()
		case "exclude_from_dunning_campaign":
			return b.HasExcludeFromDunningCampaign()
		case "tax_ids":
			return len(b.GetTaxIds()) > 0
		case "metadata":
			return len(b.GetMetadata()) > 0
		case "payment_method_type":
			return b.HasPaymentMethodType()
		case "x402_agent_address":
			return b.HasX402AgentAddress()
		}
		return false
	}
}

// customerInput returns the service input of the paths of b; an address
// path stands for each of its fields. Text fields left empty, and
// overrides left unset, clear the attribute (the customer then inherits
// its billing entity's setting).
func customerInput(b *customerv1.Customer, paths []string) (customers.Input, error) {
	var in customers.Input
	for _, a := range []string{"billing_address", "shipping_address"} {
		if slices.Contains(paths, a) {
			paths = slices.Concat(paths, customerAddressPaths(a))
		}
	}
	text := func(has bool, s string) core.Optional[*string] {
		if !has {
			return core.Some[*string](nil)
		}
		return core.Some(StringPtr(s))
	}
	days := func(has bool, n int32) core.Optional[*int] { return core.Some(OptInt(has, n)) }
	for _, path := range paths {
		if f, ok := strings.CutPrefix(path, "billing_address."); ok {
			setAddress(&in.BillingAddress, f, b.GetBillingAddress())
			continue
		}
		if f, ok := strings.CutPrefix(path, "shipping_address."); ok {
			setAddress(&in.ShippingAddress, f, b.GetShippingAddress())
			continue
		}
		switch path {
		case "external_id":
			in.ExternalID = core.Some(b.GetExternalId())
		case "billing_entity_id":
			var r refs.Ref
			if b.GetBillingEntityId() != "" {
				var err error
				if r, err = CatalogRef("customer.billing_entity_id", b.GetBillingEntityId()); err != nil {
					return in, err
				}
			}
			in.BillingEntity = core.Some(r)
		case "customer_type":
			in.CustomerType = core.Some(customerTypes.Core(b.GetCustomerType()))
		case "account_type":
			t := customerAccountTypes.Core(b.GetAccountType())
			if t == "" {
				t = customer.AccountTypeCustomer
			}
			in.AccountType = core.Some(t)
		case "name":
			in.Name = text(b.HasName(), b.GetName())
		case "firstname":
			in.Firstname = text(b.HasFirstname(), b.GetFirstname())
		case "lastname":
			in.Lastname = text(b.HasLastname(), b.GetLastname())
		case "legal_name":
			in.LegalName = text(b.HasLegalName(), b.GetLegalName())
		case "legal_number":
			in.LegalNumber = text(b.HasLegalNumber(), b.GetLegalNumber())
		case "tax_identification_number":
			in.TaxIdentificationNumber = text(b.HasTaxIdentificationNumber(), b.GetTaxIdentificationNumber())
		case "email":
			in.Email = text(b.HasEmail(), b.GetEmail())
		case "phone":
			in.Phone = text(b.HasPhone(), b.GetPhone())
		case "url":
			in.URL = text(b.HasUrl(), b.GetUrl())
		case "logo_url":
			in.LogoURL = text(b.HasLogoUrl(), b.GetLogoUrl())
		case "currency":
			var c money.Currency
			if code := b.GetCurrency(); code != "" {
				var err error
				if c, err = money.LookupCurrency(code); err != nil {
					return in, customerViolation("currency", core.CodeInvalidCurrency)
				}
			}
			in.Currency = core.Some(c)
		case "timezone":
			var loc *time.Location
			if name := b.GetTimezone(); name != "" {
				l, err := time.LoadLocation(name)
				if err != nil || name == "Local" {
					return in, customerViolation("timezone", core.CodeInvalidTimezone)
				}
				loc = l
			}
			in.Timezone = core.Some(loc)
		case "document_locale":
			in.DocumentLocale = text(b.HasDocumentLocale(), b.GetDocumentLocale())
		case "invoice_grace_period_days":
			in.InvoiceGracePeriodDays = days(b.HasInvoiceGracePeriodDays(), b.GetInvoiceGracePeriodDays())
		case "net_payment_term_days":
			in.NetPaymentTermDays = days(b.HasNetPaymentTermDays(), b.GetNetPaymentTermDays())
		case "finalize_zero_amount_invoice":
			z := customerZeroAmountFinalizations.Core(b.GetFinalizeZeroAmountInvoice())
			if z == "" {
				z = customer.ZeroAmountFinalizationInherit
			}
			in.FinalizeZeroAmountInvoice = core.Some(z)
		case "issuing_date_anchor":
			in.IssuingDateAnchor = core.Some(beIssuingDateAnchors.Core(b.GetIssuingDateAnchor()))
		case "issuing_date_adjustment":
			in.IssuingDateAdjustment = core.Some(beIssuingDateAdjustments.Core(b.GetIssuingDateAdjustment()))
		case "skip_invoice_custom_sections":
			in.SkipInvoiceCustomSections = core.Some(b.GetSkipInvoiceCustomSections())
		case "invoice_custom_section_ids":
			rs, err := CatalogRefs("customer.invoice_custom_section_ids", b.GetInvoiceCustomSectionIds())
			if err != nil {
				return in, err
			}
			in.InvoiceCustomSections = core.Some(rs)
		case "applied_dunning_campaign_id":
			var r refs.Ref
			if b.GetAppliedDunningCampaignId() != "" {
				var err error
				if r, err = CatalogRef("customer.applied_dunning_campaign_id", b.GetAppliedDunningCampaignId()); err != nil {
					return in, err
				}
			}
			in.AppliedDunningCampaign = core.Some(r)
		case "exclude_from_dunning_campaign":
			in.ExcludeFromDunningCampaign = core.Some(b.GetExcludeFromDunningCampaign())
		case "tax_ids":
			rs, err := CatalogRefs("customer.tax_ids", b.GetTaxIds())
			if err != nil {
				return in, err
			}
			in.Taxes = core.Some(rs)
		case "metadata":
			in.Metadata = core.Some(FromMetadata(b.GetMetadata()))
		case "payment_method_type":
			if b.HasPaymentMethodType() {
				return in, connect.NewError(connect.CodeUnimplemented, "customer.payment_method_type is not supported")
			}
		case "x402_agent_address":
			in.X402AgentAddress = text(b.HasX402AgentAddress(), b.GetX402AgentAddress())
		}
	}
	return in, nil
}

func setAddress(dst *customers.Address, field string, a *commonv1.Address) {
	switch field {
	case "line1":
		dst.Line1 = core.Some(a.GetLine1())
	case "line2":
		dst.Line2 = core.Some(a.GetLine2())
	case "city":
		dst.City = core.Some(a.GetCity())
	case "state":
		dst.State = core.Some(a.GetState())
	case "zipcode":
		dst.Zipcode = core.Some(a.GetZipcode())
	case "country":
		dst.Country = core.Some(a.GetCountry())
	}
}

// customerFieldNames renames the service's field paths to the request's.
var customerFieldNames = map[string]string{
	"invoice_custom_sections":  "invoice_custom_section_ids",
	"taxes":                    "tax_ids",
	"billing_entity":           "billing_entity_id",
	"applied_dunning_campaign": "applied_dunning_campaign_id",
}

func renameFields(v []FieldViolation, prefix string, names map[string]string, bare ...string) []FieldViolation {
	for i := range v {
		f := strings.TrimPrefix(v[i].Field, prefix)
		head, rest, _ := strings.Cut(f, ".")
		if n, ok := names[head]; ok {
			f = n
			if rest != "" {
				f += "." + rest
			}
		}
		if !slices.Contains(bare, f) {
			f = prefix + f
		}
		v[i].Field = f
	}
	return v
}

// customerViolation is the validation error of a customer field.
func customerViolation(field, code string) error {
	return withReason(InvalidArgument("invalid customer", []FieldViolation{{Field: "customer." + field, Reason: code}}), ReasonValidationErrors)
}

// customerError maps the service errors, whose field paths are the
// customer's, onto the request's customer message.
func customerError(ctx context.Context, err error) error {
	if v, ok := validationViolations("", err); ok {
		return withReason(InvalidArgument("invalid customer", renameFields(v, "customer.", customerFieldNames)), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// connectionError maps the connection service errors onto the request's
// connection message; "connection" (the customer's only payment
// connection) and "base" stay as they are.
func connectionError(ctx context.Context, err error) error {
	if v, ok := validationViolations("", err); ok {
		return withReason(InvalidArgument("invalid connection", renameFields(v, "connection.", nil, "connection", "base")), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// customerOrder parses the order_by of ListCustomers: created_at, newest
// first by default.
func customerOrder(s string) (core.ListOrder, error) {
	switch strings.Join(strings.Fields(s), " ") {
	case "", "created_at desc":
		return core.OrderRecent, nil
	case "created_at", "created_at asc":
		return core.OrderCreated, nil
	}
	return 0, InvalidArgument("invalid order_by", []FieldViolation{{
		Field: "order_by", Reason: core.CodeInvalid, Description: "supported: created_at desc, created_at asc",
	}})
}

// listFilter parses the filter of ListCustomers (lago CustomersQuery):
// equalities on customerFilterFields, values of one field joined by OR,
// and metadata.<key> equalities (an empty value keeps customers without
// the key).
func (s *Customers) listFilter(ctx context.Context, org core.OrganizationID, filter string) (customer.Filter, error) {
	var f customer.Filter
	e, err := ParseFilter(filter)
	if err != nil || e == nil {
		return f, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	var rest FilterAnd
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok || !strings.HasPrefix(r.Field, "metadata.") {
			rest = append(rest, t)
			continue
		}
		key := strings.TrimPrefix(r.Field, "metadata.")
		if r.Op != CmpEq || key == "" {
			return f, filterError("metadata takes `metadata.<key> = value` restrictions")
		}
		if f.Metadata == nil {
			f.Metadata = core.MetadataFilter{}
		}
		if _, dup := f.Metadata[key]; dup {
			return f, filterError(fmt.Sprintf("field %q is restricted twice", r.Field))
		}
		f.Metadata[key] = r.Value
	}
	var others FilterExpr
	if len(rest) > 0 {
		others = rest
	}
	eq, err := Equalities(others, customerFilterFields...)
	if err != nil {
		return f, err
	}
	if f.ExternalID, err = FilterSingle("external_id", eq["external_id"]); err != nil {
		return f, err
	}
	types, err := FilterEnum[customerv1.CustomerType]("customer_type", eq["customer_type"], customerv1.CustomerType(0).Descriptor())
	if err != nil {
		return f, err
	}
	if len(types) > 1 {
		return f, filterError(`field "customer_type" takes one value`)
	}
	if len(types) == 1 {
		f.CustomerType = customerTypes.Core(types[0])
	}
	if f.HasCustomerType, err = FilterBool("has_customer_type", eq["has_customer_type"]); err != nil {
		return f, err
	}
	accounts, err := FilterEnum[customerv1.AccountType]("account_type", eq["account_type"], customerv1.AccountType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, a := range accounts {
		f.AccountTypes = append(f.AccountTypes, customerAccountTypes.Core(a))
	}
	for _, code := range eq["currency"] {
		c, err := money.LookupCurrency(strings.ToUpper(code))
		if err != nil {
			return f, filterError(fmt.Sprintf("field %q: unknown currency %q", "currency", code))
		}
		f.Currencies = append(f.Currencies, c)
	}
	for _, c := range eq["billing_address.country"] {
		f.Countries = append(f.Countries, strings.ToUpper(c))
	}
	f.States = eq["billing_address.state"]
	f.Zipcodes = eq["billing_address.zipcode"]
	if f.HasTaxIdentificationNumber, err = FilterBool("has_tax_identification_number", eq["has_tax_identification_number"]); err != nil {
		return f, err
	}
	deleted, err := FilterBool("deleted", eq["deleted"])
	if err != nil {
		return f, err
	}
	f.IncludeDeleted = deleted != nil && *deleted
	if f.ActiveSubscriptionsCountFrom, err = filterCount("active_subscriptions_count_from", eq["active_subscriptions_count_from"]); err != nil {
		return f, err
	}
	if f.ActiveSubscriptionsCountTo, err = filterCount("active_subscriptions_count_to", eq["active_subscriptions_count_to"]); err != nil {
		return f, err
	}
	if vs := eq["billing_entity_id"]; len(vs) > 0 {
		rs, err := CatalogRefs(FilterField, vs)
		if err != nil {
			return f, err
		}
		if f.BillingEntityIDs, err = s.svc.BillingEntityIDs(ctx, org, rs); err != nil {
			return f, Error(ctx, err)
		}
	}
	return f, nil
}

func customerProto(v customers.View) *customerv1.Customer {
	c := v.Customer
	b := customerv1.Customer_builder{
		Id:                         new(c.ID.String()),
		ExternalId:                 new(c.ExternalID),
		BillingEntityId:            new(c.BillingEntityID.String()),
		Name:                       c.Name,
		Firstname:                  c.Firstname,
		Lastname:                   c.Lastname,
		LegalName:                  c.LegalName,
		LegalNumber:                c.LegalNumber,
		TaxIdentificationNumber:    c.TaxIdentificationNumber,
		Email:                      c.Email,
		Phone:                      c.Phone,
		Url:                        c.URL,
		LogoUrl:                    c.LogoURL,
		BillingAddress:             beAddress(c.BillingAddress),
		ShippingAddress:            beAddress(c.ShippingAddress),
		DocumentLocale:             c.DocumentLocale,
		X402AgentAddress:           c.X402AgentAddress,
		SkipInvoiceCustomSections:  new(c.SkipInvoiceCustomSections),
		ExcludeFromDunningCampaign: new(c.ExcludeFromDunningCampaign),
		Metadata:                   Metadata(c.Metadata),
		SequentialId:               new(c.SequentialID),
		Slug:                       new(c.Slug),
		DeletedAt:                  TimestampPtr(c.DeletedAt),
		CreatedAt:                  Timestamp(c.CreatedAt),
		UpdatedAt:                  Timestamp(c.UpdatedAt),
	}
	if t := customerTypes.Proto(c.CustomerType); t != 0 {
		b.CustomerType = &t
	}
	if t := customerAccountTypes.Proto(c.AccountType); t != 0 {
		b.AccountType = &t
	}
	if !c.Currency.IsZero() {
		b.Currency = new(c.Currency.Code())
	}
	if c.Timezone != nil {
		b.Timezone = new(c.Timezone.String())
	}
	if v.ApplicableTimezone != nil {
		b.ApplicableTimezone = new(v.ApplicableTimezone.String())
	}
	if c.InvoiceGracePeriodDays != nil {
		b.InvoiceGracePeriodDays = new(int32(*c.InvoiceGracePeriodDays))
	}
	if c.NetPaymentTermDays != nil {
		b.NetPaymentTermDays = new(int32(*c.NetPaymentTermDays))
	}
	if z := customerZeroAmountFinalizations.Proto(c.FinalizeZeroAmountInvoice); z != 0 {
		b.FinalizeZeroAmountInvoice = &z
	}
	if a := beIssuingDateAnchors.Proto(c.IssuingDateAnchor); a != 0 {
		b.IssuingDateAnchor = &a
	}
	if a := beIssuingDateAdjustments.Proto(c.IssuingDateAdjustment); a != 0 {
		b.IssuingDateAdjustment = &a
	}
	for _, id := range c.InvoiceCustomSectionIDs {
		b.InvoiceCustomSectionIds = append(b.InvoiceCustomSectionIds, id.String())
	}
	if !c.AppliedDunningCampaignID.IsZero() {
		b.AppliedDunningCampaignId = new(c.AppliedDunningCampaignID.String())
	}
	for _, id := range c.TaxIDs {
		b.TaxIds = append(b.TaxIds, id.String())
	}
	for _, cn := range v.Connections {
		b.Connections = append(b.Connections, connectionProto(cn))
	}
	return b.Build()
}

func connectionProto(cn customers.Connection) *customerv1.CustomerConnection {
	b := customerv1.CustomerConnection_builder{
		IntegrationId:          new(cn.Integration.ID.String()),
		Provider:               new(string(cn.Integration.Provider)),
		ExternalCustomerId:     StringPtr(cn.ExternalCustomerID),
		SyncWithProvider:       new(cn.SyncWithProvider),
		SubsidiaryId:           cn.SubsidiaryID,
		ProviderPaymentMethods: cn.ProviderPaymentMethods,
		IsDefault:              new(cn.IsDefault),
		CreatedAt:              Timestamp(cn.CreatedAt),
		UpdatedAt:              Timestamp(cn.UpdatedAt),
	}
	if c := integrationCategories.Proto(cn.Integration.Category); c != 0 {
		b.Category = &c
	}
	return b.Build()
}

// nonZero returns &t, nil for the zero time.
func nonZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// filterCount parses the single non-negative integer value of a count
// field of the ListCustomers filter; nil when the field is absent.
func filterCount(field string, values []string) (*int, error) {
	v, err := FilterSingle(field, values)
	if err != nil || v == "" {
		return nil, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return nil, filterError(fmt.Sprintf("field %q takes a non-negative integer", field))
	}
	return &n, nil
}
