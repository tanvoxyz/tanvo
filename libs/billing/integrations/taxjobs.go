package integrations

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Error of an Avalara connection whose company code names no company
// (lago aggregator/taxes/avalara/fetch_company_id_service.rb:19-20).
const (
	codeCompanyNotFound    = "company_not_found"
	messageCompanyNotFound = "Company cannot be found in Avalara based on the provided code"
)

// codeUnexpected is the code of a provider failure without one (lago
// Integrations::Aggregator::BaseService#code).
const codeUnexpected = "unexpected_error"

// ProviderErrorPayload is the payload of integration.provider_error
// (lago V1::Integrations::ProviderErrorSerializer).
type ProviderErrorPayload struct {
	// IntegrationID is the integration.
	IntegrationID core.IntegrationID `json:"integration_id"`
	// Provider is the vendor.
	Provider integration.Provider `json:"provider"`
	// ProviderCode is the integration's code.
	ProviderCode string `json:"provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// errorDetails returns the provider's code and message of err (lago
// BaseService#code and #message).
func errorDetails(err error) ProviderErrorDetails {
	if pe, ok := integration.AsProviderError(err); ok {
		return ProviderErrorDetails{ErrorCode: cmp.Or(pe.Code, codeUnexpected), Message: cmp.Or(pe.Message, pe.Error())}
	}
	return ProviderErrorDetails{ErrorCode: codeUnexpected, Message: err.Error()}
}

// emitIntegrationError emits integration.provider_error for i (lago
// BaseService#deliver_integration_error_webhook).
func (s *Service) emitIntegrationError(ctx context.Context, i *integration.Integration, d ProviderErrorDetails) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, _ port.Repos) error {
		return kernel.Emit(ctx, s.d, i.OrganizationID, webhook.EventTypeIntegrationProviderError, "provider_error", i.ID.UUID(),
			ProviderErrorPayload{IntegrationID: i.ID, Provider: i.Provider, ProviderCode: i.Code, ProviderError: d})
	})
}

// fetchTaxCompany resolves the Avalara company of the integration id from
// its company code and stores its ID (lago
// Integrations::Avalara::FetchCompanyIdService). A connection that has
// one is skipped. A company not found or a provider failure emits
// integration.provider_error and fails the job without retry (call!).
func (s *Service) fetchTaxCompany(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error {
	var (
		i    *integration.Integration
		conn integration.Connection
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		i, conn, err = s.loadConnection(ctx, r, org, id)
		return err
	})
	if err != nil {
		return err
	}
	if i.Provider != integration.ProviderAvalara || conn.Setting(KeyCompanyID) != "" {
		return nil
	}
	g, err := s.gateway(i.Provider)
	if err != nil {
		return err
	}
	f, ok := g.(integration.TaxCompanyFinder)
	if !ok {
		return fmt.Errorf("integration %s: find company: %w", i.Code, integration.ErrUnsupported)
	}
	company, err := f.FindCompany(ctx, conn)
	if err != nil {
		d := errorDetails(err)
		if errors.Is(err, integration.ErrCompanyNotFound) {
			d = ProviderErrorDetails{ErrorCode: codeCompanyNotFound, Message: messageCompanyNotFound}
		}
		if err := s.emitIntegrationError(ctx, i, d); err != nil {
			return err
		}
		return fmt.Errorf("integration %s: find company: %s: %s", i.Code, d.ErrorCode, d.Message)
	}
	return s.UpdateConnection(ctx, conn, integration.ConnectionUpdate{Settings: map[string]string{KeyCompanyID: company}})
}

// gateway returns the adapter of provider p.
func (s *Service) gateway(p integration.Provider) (integration.Gateway, error) {
	if s.gw == nil {
		return nil, fmt.Errorf("provider %s: %w", p, integration.ErrUnsupported)
	}
	return s.gw.IntegrationGateway(p)
}

// reportInvoiceTaxes reports the taxes of the finalized invoice id to its
// customer's tax provider (lago Integrations::Aggregator::Taxes::Invoices::
// CreateJob, enqueued once an invoice held by a payment gate is
// finalized). A customer without a tax integration is skipped. A refusal
// emits customer.tax_provider_error and fails the job; a retryable failure
// is retried.
func (s *Service) reportInvoiceTaxes(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	if s.taxes == nil {
		return fmt.Errorf("report taxes of invoice %s: %w", id, integration.ErrUnsupported)
	}
	var (
		inv  *invoice.Invoice
		c    *customer.Customer
		fees []*invoice.Fee
		on   bool
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = r.Invoices.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		if c, err = r.Customers.Get(ctx, org, inv.CustomerID); err != nil {
			return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
		}
		ic, err := taxCustomer(ctx, r, c)
		if err != nil || ic == nil {
			return err
		}
		on = true
		if fees, err = r.Fees.ListByInvoice(ctx, org, id); err != nil {
			return fmt.Errorf("load fees of invoice %s: %w", id, err)
		}
		return nil
	})
	if err != nil || !on {
		return err
	}
	_, err = s.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: c, Invoice: inv, Fees: fees})
	return err
}
