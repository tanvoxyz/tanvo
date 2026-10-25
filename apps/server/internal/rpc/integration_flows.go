package rpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	creditnotev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1"
	integrationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
)

// StartIntegrationOauth implements IntegrationService.StartIntegrationOauth:
// the provider's consent page, which redirects to redirect_uri with the
// code and the state (replacing lago's Nango connect flow).
func (s *Integrations) StartIntegrationOauth(ctx context.Context, req *integrationv1.StartIntegrationOauthRequest) (*integrationv1.StartIntegrationOauthResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	start, err := s.svc.StartOAuth(ctx, p.OrganizationID, i.ID, req.GetRedirectUri())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.StartIntegrationOauthResponse_builder{
		AuthorizationUrl: new(start.AuthorizationURL), State: new(start.State),
	}.Build(), nil
}

// CompleteIntegrationOauth implements
// IntegrationService.CompleteIntegrationOauth: the code is exchanged, the
// grant stored and the connection's follow-up jobs queued (lago
// Integrations::Xero::CreateService and Hubspot::CreateService after the
// Nango connection, PaymentProviders::GocardlessService).
func (s *Integrations) CompleteIntegrationOauth(ctx context.Context, req *integrationv1.CompleteIntegrationOauthRequest) (*integrationv1.CompleteIntegrationOauthResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if i, err = s.svc.CompleteOAuth(ctx, p.OrganizationID, i.ID, req.GetCode(), req.GetState()); err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.message(ctx, i)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.CompleteIntegrationOauthResponse_builder{Integration: out}.Build(), nil
}

// SyncIntegrationItems implements IntegrationService.SyncIntegrationItems
// (lago FetchIntegrationItems and FetchIntegrationAccounts mutations).
func (s *Integrations) SyncIntegrationItems(ctx context.Context, req *integrationv1.SyncIntegrationItemsRequest) (*integrationv1.SyncIntegrationItemsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if err := s.svc.SyncItems(ctx, p.OrganizationID, i.ID, integrationItemTypes.Core(req.GetItemType())); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.SyncIntegrationItemsResponse_builder{}.Build(), nil
}

// ListIntegrationSubsidiaries implements
// IntegrationService.ListIntegrationSubsidiaries (lago
// IntegrationSubsidiariesResolver).
func (s *Integrations) ListIntegrationSubsidiaries(ctx context.Context, req *integrationv1.ListIntegrationSubsidiariesRequest) (*integrationv1.ListIntegrationSubsidiariesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := s.find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	recs, err := s.svc.Subsidiaries(ctx, p.OrganizationID, i.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*integrationv1.IntegrationSubsidiary, 0, len(recs))
	for _, r := range recs {
		out = append(out, integrationv1.IntegrationSubsidiary_builder{ExternalId: new(r.ID), ExternalName: new(r.Name)}.Build())
	}
	return integrationv1.ListIntegrationSubsidiariesResponse_builder{Subsidiaries: out}.Build(), nil
}

// SyncHubspotInvoice implements IntegrationService.SyncHubspotInvoice
// (lago SyncHubspotInvoice mutation).
func (s *Integrations) SyncHubspotInvoice(ctx context.Context, req *integrationv1.SyncHubspotInvoiceRequest) (*integrationv1.SyncHubspotInvoiceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	if err := s.svc.SyncCRMInvoice(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.SyncHubspotInvoiceResponse_builder{}.Build(), nil
}

// SyncSalesforceInvoice implements IntegrationService.SyncSalesforceInvoice
// (lago SyncSalesforceInvoice mutation).
func (s *Integrations) SyncSalesforceInvoice(ctx context.Context, req *integrationv1.SyncSalesforceInvoiceRequest) (*integrationv1.SyncSalesforceInvoiceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.ResyncSalesforceInvoice(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return integrationv1.SyncSalesforceInvoiceResponse_builder{}.Build(), nil
}

// WithIntegrations sets the integrations service of the invoice sync
// RPCs.
func (s *Invoices) WithIntegrations(svc *integrations.Service) *Invoices {
	s.integ = svc
	return s
}

// RetryInvoiceSync implements InvoiceService.RetryInvoiceSync (lago
// SyncIntegrationInvoice mutation).
func (s *Invoices) RetryInvoiceSync(ctx context.Context, req *invoicev1.RetryInvoiceSyncRequest) (*invoicev1.RetryInvoiceSyncResponse, error) {
	if s.integ == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no integrations are wired")
	}
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	if err := s.integ.SyncInvoice(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RetryInvoiceSyncResponse_builder{}.Build(), nil
}

// RetryInvoiceTaxVoiding implements InvoiceService.RetryInvoiceTaxVoiding
// (lago RetryTaxProviderVoiding mutation).
func (s *Invoices) RetryInvoiceTaxVoiding(ctx context.Context, req *invoicev1.RetryInvoiceTaxVoidingRequest) (*invoicev1.RetryInvoiceTaxVoidingResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.RetryTaxVoidingView(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RetryInvoiceTaxVoidingResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// SetInvoiceSalesforceId implements InvoiceService.SetInvoiceSalesforceId
// (lago Api::V1::InvoicesController#sync_salesforce_id).
func (s *Invoices) SetInvoiceSalesforceId(ctx context.Context, req *invoicev1.SetInvoiceSalesforceIdRequest) (*invoicev1.SetInvoiceSalesforceIdResponse, error) {
	if s.integ == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no integrations are wired")
	}
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	i, err := (&Integrations{svc: s.integ}).find(ctx, p.OrganizationID, req.GetIntegrationId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.integ.SetInvoiceSalesforceID(ctx, p.OrganizationID, id, i.Code, req.GetExternalId()); err != nil {
		return nil, Error(ctx, err)
	}
	v, err := s.svc.View(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.SetInvoiceSalesforceIdResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// WithIntegrations sets the integrations service of RetryCreditNoteSync.
func (s *CreditNotes) WithIntegrations(svc *integrations.Service) *CreditNotes {
	s.integ = svc
	return s
}

// RetryCreditNoteSync implements CreditNoteService.RetryCreditNoteSync
// (lago SyncIntegrationCreditNote mutation).
func (s *CreditNotes) RetryCreditNoteSync(ctx context.Context, req *creditnotev1.RetryCreditNoteSyncRequest) (*creditnotev1.RetryCreditNoteSyncResponse, error) {
	if s.integ == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "no integrations are wired")
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	if err := s.integ.SyncCreditNote(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return creditnotev1.RetryCreditNoteSyncResponse_builder{}.Build(), nil
}

// RetryCreditNoteTaxReporting implements
// CreditNoteService.RetryCreditNoteTaxReporting (lago RetryTaxReporting
// mutation): a refusal fails with the tax_error field.
func (s *CreditNotes) RetryCreditNoteTaxReporting(ctx context.Context, req *creditnotev1.RetryCreditNoteTaxReportingRequest) (*creditnotev1.RetryCreditNoteTaxReportingResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	cn, err := s.svc.RetryTaxReporting(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	m, err := s.proto(ctx, p.OrganizationID, cn)
	if err != nil {
		return nil, err
	}
	return creditnotev1.RetryCreditNoteTaxReportingResponse_builder{CreditNote: m}.Build(), nil
}

// PreviewInvoiceTaxes implements IntegrationService.PreviewInvoiceTaxes
// (lago FetchDraftInvoiceTaxes mutation).
func (s *Integrations) PreviewInvoiceTaxes(ctx context.Context, req *integrationv1.PreviewInvoiceTaxesRequest) (*integrationv1.PreviewInvoiceTaxesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var in integrations.DraftInvoiceTaxesInput
	c, err := ParseRef("customer_id", req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	switch {
	case c.Code != "":
		return nil, InvalidArgument("invalid reference", []FieldViolation{{
			Field: "customer_id", Reason: core.CodeInvalid, Description: "a customer has no code",
		}})
	case c.ExternalID != "":
		in.CustomerExternalID = c.ExternalID
	default:
		if in.CustomerID, err = ParseID[core.CustomerID]("customer_id", c.ID.String()); err != nil {
			return nil, err
		}
	}
	if v := req.GetCurrency(); v != "" {
		if in.Currency, err = money.LookupCurrency(v); err != nil {
			return nil, InvalidArgument("invalid currency", []FieldViolation{{Field: "currency", Reason: core.CodeInvalidCurrency}})
		}
	}
	for i, b := range req.GetFees() {
		field := fmt.Sprintf("fees[%d]", i)
		var f integrations.DraftInvoiceFee
		if f.AddOn, err = CatalogRef(field+".add_on_id", b.GetAddOnId()); err != nil {
			return nil, err
		}
		if b.HasUnits() {
			d, err := ParseDecimal(field+".units", b.GetUnits())
			if err != nil {
				return nil, err
			}
			f.Units = &d
		}
		if b.HasUnitAmount() {
			d, err := ParseDecimal(field+".unit_amount", b.GetUnitAmount())
			if err != nil {
				return nil, err
			}
			f.UnitAmount = &d
		}
		in.Fees = append(in.Fees, f)
	}
	res, cur, err := s.svc.DraftInvoiceTaxes(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*integrationv1.TaxFee, len(res))
	for i, r := range res {
		b := integrationv1.TaxFee_builder{
			ItemId: new(r.ItemID), ItemCode: new(r.ItemCode), Amount: exactMoney(r.AmountCents, cur),
		}
		if r.TaxAmountCents != nil {
			b.TaxAmount = Money(*r.TaxAmountCents, cur)
		}
		for _, t := range r.Breakdown {
			tb := integrationv1.TaxBreakdown_builder{
				Name: new(t.Name), Rate: new(t.Rate.String()), TaxAmount: exactMoney(t.TaxAmount, cur), Type: new(t.Type),
			}
			if t.AppliedOnWholeInvoice() {
				tb.WholeInvoiceTaxCode = new(t.Code())
			}
			b.TaxBreakdown = append(b.TaxBreakdown, tb.Build())
		}
		out[i] = b.Build()
	}
	return integrationv1.PreviewInvoiceTaxesResponse_builder{Fees: out}.Build(), nil
}

// exactMoney converts a possibly fractional amount in minor units of c to
// a Money in major units, keeping its precision.
func exactMoney(minor money.Decimal, c money.Currency) *commonv1.Money {
	if c.IsZero() {
		return nil
	}
	amount := c.MinorToUnits(minor).String()
	return commonv1.Money_builder{Currency: new(c.Code()), Amount: &amount}.Build()
}
