package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Settings keys of an integration customer.
const (
	// icTargetedObject is the HubSpot object of the customer.
	icTargetedObject = "targeted_object"
	// icEmail is the email HubSpot stored for the customer.
	icEmail = "email"
)

// codeValidationError is the code of a document the provider refused.
const codeValidationError = integration.CodeValidationError

// errNotCompany fails the update of an individual's HubSpot company.
var errNotCompany = errors.New("integration customer is not a company")

// WithAppURL sets the web console URL the links sent to providers point
// into (lago LAGO_FRONT_URL); without it they are empty.
func WithAppURL(u string) Option { return func(s *Service) { s.appURL = strings.TrimSuffix(u, "/") } }

// WithBlobs sets the blob store the URLs of invoice files are signed by
// (HubSpot's lago_invoice_file_url).
func WithBlobs(b port.BlobStore) Option { return func(s *Service) { s.blobs = b } }

// link returns the console URL of path in the organization's space (lago
// URI.join(lago_front_url, "/<slug>/…")).
func (s *Service) link(slug string, path ...string) string {
	if s.appURL == "" {
		return ""
	}
	parts := append([]string{s.appURL, url.PathEscape(slug)}, path...)
	return strings.Join(parts, "/")
}

// CustomerErrorPayload is the payload of customer.accounting_provider_error
// and customer.crm_provider_error (lago
// V1::Integrations::CustomerErrorSerializer).
type CustomerErrorPayload struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// AccountingProvider is the vendor.
	AccountingProvider integration.Provider `json:"accounting_provider"`
	// AccountingProviderCode is the integration's code.
	AccountingProviderCode string `json:"accounting_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// emitCustomerError emits the customer error webhook of i's provider (lago
// BaseService#deliver_error_webhook and #error_webhook_code): HubSpot's is
// customer.crm_provider_error, Avalara's customer.tax_provider_error, the
// others' customer.accounting_provider_error.
func (s *Service) emitCustomerError(ctx context.Context, c *customer.Customer, i *integration.Integration, d ProviderErrorDetails) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, _ port.Repos) error {
		switch i.Provider {
		case integration.ProviderAvalara:
			return kernel.Emit(ctx, s.d, c.OrganizationID, webhook.EventTypeCustomerTaxProviderError, "tax_provider_customer_error", c.ID.UUID(),
				TaxProviderCustomerError{
					CustomerID: c.ID, ExternalCustomerID: c.ExternalID, TaxProvider: i.Provider, TaxProviderCode: i.Code, ProviderError: d,
				})
		case integration.ProviderHubspot:
			return kernel.Emit(ctx, s.d, c.OrganizationID, webhook.EventTypeCustomerCRMProviderError, "crm_provider_customer_error", c.ID.UUID(),
				customerError(c, i, d))
		default:
			return kernel.Emit(ctx, s.d, c.OrganizationID, webhook.EventTypeCustomerAccountingProviderError, "accounting_provider_customer_error", c.ID.UUID(),
				customerError(c, i, d))
		}
	})
}

func customerError(c *customer.Customer, i *integration.Integration, d ProviderErrorDetails) CustomerErrorPayload {
	return CustomerErrorPayload{
		CustomerID: c.ID, ExternalCustomerID: c.ExternalID, AccountingProvider: i.Provider, AccountingProviderCode: i.Code, ProviderError: d,
	}
}

// contactEnv is what a contact sync needs.
type contactEnv struct {
	ic   *integration.IntegrationCustomer
	c    *customer.Customer
	i    *integration.Integration
	conn integration.Connection
	slug string
	sfID string
}

func (s *Service) loadContact(ctx context.Context, org core.OrganizationID, id core.IntegrationCustomerID) (*contactEnv, error) {
	var e *contactEnv
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ic, err := r.IntegrationCustomers.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		} else if err != nil {
			return fmt.Errorf("load integration customer %s: %w", id, err)
		}
		c, err := r.Customers.Get(ctx, org, ic.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", ic.CustomerID, err)
		}
		i, conn, err := s.loadConnection(ctx, r, org, ic.IntegrationID)
		if err != nil {
			return err
		}
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return fmt.Errorf("load organization %s: %w", org, err)
		}
		e = &contactEnv{ic: ic, c: c, i: i, conn: conn, slug: o.Slug}
		crm, err := r.IntegrationCustomers.ListByCustomer(ctx, org, c.ID, integration.CategoryCRM)
		if err != nil {
			return fmt.Errorf("load CRM links of customer %s: %w", c.ID, err)
		}
		for _, l := range crm {
			if l.ID == ic.ID || l.ExternalCustomerID == "" {
				continue
			}
			li, err := r.Integrations.Get(ctx, org, l.IntegrationID)
			if err != nil {
				return fmt.Errorf("load integration %s: %w", l.IntegrationID, err)
			}
			if li.Provider == integration.ProviderSalesforce {
				e.sfID = l.ExternalCustomerID
			}
		}
		return nil
	})
	return e, err
}

// contact returns the neutral document of e's customer.
func (s *Service) contact(e *contactEnv) *integration.Contact {
	c := e.c
	ct := &integration.Contact{
		CustomerID: c.ID, ExternalID: c.ExternalID, LinkedID: e.ic.ExternalCustomerID,
		Individual: c.CustomerType == customer.TypeIndividual,
		Name:       deref(c.Name), DisplayName: c.DisplayName(false),
		Firstname: deref(c.Firstname), Lastname: deref(c.Lastname), LegalName: deref(c.LegalName),
		Email: deref(c.Email), Phone: deref(c.Phone), URL: deref(c.URL), TaxNumber: deref(c.TaxIdentificationNumber),
		BillingAddress: c.BillingAddress, ShippingAddress: c.ShippingAddress,
		SalesforceID: e.sfID, SubsidiaryID: deref(e.ic.SubsidiaryID),
		Link: s.link(e.slug, "customer", c.ID.String()),
	}
	ct.TargetedObject, _ = e.ic.Setting(icTargetedObject)
	return ct
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// targetedObject is the HubSpot object of a new link (lago
// IntegrationCustomers::HubspotService#targeted_object): the customer's
// type decides, else the integration's default.
func targetedObject(c *customer.Customer, conn integration.Connection) string {
	switch c.CustomerType {
	case customer.TypeIndividual:
		return integration.TargetedObjectContacts
	case customer.TypeCompany:
		return integration.TargetedObjectCompanies
	default:
		return conn.Setting(KeyDefaultTargetedObject)
	}
}

// syncContact creates or updates the record of an integration customer at
// its provider (lago IntegrationCustomers::CreateJob and UpdateJob). A
// created record stores its ID on the link and emits
// customer.<category>_provider_created. A failure emits the provider's
// customer error webhook; a link being created is then removed, as lago
// creates it only once the provider answered. An answer without a record,
// a refusal of the document's fields included, keeps the link without
// one, Avalara's excepted.
func (s *Service) syncContact(ctx context.Context, org core.OrganizationID, a kernel.IntegrationCustomerArgs) error {
	e, err := s.loadContact(ctx, org, a.IntegrationCustomerID)
	if err != nil || e == nil {
		return err
	}
	if a.Update && e.ic.ExternalCustomerID == "" || !a.Update && e.ic.ExternalCustomerID != "" {
		return nil
	}
	g, err := s.gateway(e.i.Provider)
	if err != nil {
		return err
	}
	cg, ok := g.(integration.ContactGateway)
	if !ok {
		return fmt.Errorf("integration %s: contacts: %w", e.i.Code, integration.ErrUnsupported)
	}
	if e.i.Provider == integration.ProviderHubspot {
		if !a.Update {
			if _, set := e.ic.Setting(icTargetedObject); !set {
				if err := e.ic.SetSetting(icTargetedObject, targetedObject(e.c, e.conn)); err != nil {
					return err
				}
			}
		}
		// contacts/create_service.rb:13 and companies/create_service.rb:
		// the properties are deployed first.
		object := integration.CRMObjectCompanies
		if t, _ := e.ic.Setting(icTargetedObject); t == integration.TargetedObjectContacts {
			object = integration.CRMObjectContacts
		} else if a.Update && e.c.CustomerType == customer.TypeIndividual {
			// companies/update_service.rb:10: an individual is not
			// updated as a company.
			return fmt.Errorf("integration %s: update contact of customer %s: %w", e.i.Code, e.c.ID, errNotCompany)
		}
		if err := s.deploy(ctx, org, e.i.ID, object, integration.CRMSchemaProperties); err != nil {
			return err
		}
		if e.conn, err = s.reload(ctx, e.i); err != nil {
			return err
		}
	}
	ct := s.contact(e)
	var res integration.ContactResult
	if a.Update {
		res, err = cg.UpdateContact(ctx, e.conn, ct)
	} else {
		res, err = cg.CreateContact(ctx, e.conn, ct)
	}
	if err != nil {
		return s.contactFailure(ctx, e, a.Update, err)
	}
	if a.Update {
		return nil
	}
	if res.ExternalID == "" {
		// avalara_service.rb:16: no contact, no Avalara link; the
		// others keep theirs without a record (netsuite_service.rb:24,
		// xero_service.rb:21, hubspot_service.rb:24).
		if e.i.Provider == integration.ProviderAvalara {
			return s.dropLink(ctx, e)
		}
		return nil
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ic, err := r.IntegrationCustomers.Get(ctx, org, e.ic.ID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		} else if err != nil {
			return fmt.Errorf("load integration customer %s: %w", e.ic.ID, err)
		}
		ic.ExternalCustomerID, ic.SyncWithProvider, ic.UpdatedAt = res.ExternalID, true, s.d.Now()
		if t, ok := e.ic.Setting(icTargetedObject); ok {
			if err := ic.SetSetting(icTargetedObject, t); err != nil {
				return err
			}
		}
		if res.Email != "" {
			if err := ic.SetSetting(icEmail, res.Email); err != nil {
				return err
			}
		}
		if err := r.IntegrationCustomers.Update(ctx, ic); err != nil {
			return fmt.Errorf("update integration customer %s: %w", ic.ID, err)
		}
		t := webhook.EventTypeCustomerAccountingProviderCreated
		if e.i.Provider == integration.ProviderHubspot {
			t = webhook.EventTypeCustomerCRMProviderCreated
		}
		return kernel.Emit(ctx, s.d, org, t, "customer", e.c.ID.UUID(), e.c)
	})
}

// contactFailure handles the failure err of a contact call (lago
// contacts/create_service.rb:25-31): a rate limit is retried; anything
// else emits the customer error webhook and fails the job, a refusal of
// the document's fields excepted (failedContacts: the job succeeds).
func (s *Service) contactFailure(ctx context.Context, e *contactEnv, update bool, err error) error {
	if errors.Is(err, integration.ErrRequestLimit) || errors.Is(err, context.Canceled) {
		return err
	}
	d := errorDetails(err)
	if err := s.emitCustomerError(ctx, e.c, e.i, d); err != nil {
		return err
	}
	if d.ErrorCode == codeValidationError {
		// contacts/base_service.rb:38-47: a refusal is an answer without
		// a record.
		if !update && e.i.Provider == integration.ProviderAvalara {
			return s.dropLink(ctx, e)
		}
		return nil
	}
	if !update {
		if err := s.dropLink(ctx, e); err != nil {
			return err
		}
	}
	return fmt.Errorf("integration %s: sync contact of customer %s: %s: %s", e.i.Code, e.c.ID, d.ErrorCode, d.Message)
}

// dropLink removes the link of a contact the provider did not create.
func (s *Service) dropLink(ctx context.Context, e *contactEnv) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ic, err := r.IntegrationCustomers.Get(ctx, e.ic.OrganizationID, e.ic.ID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		} else if err != nil {
			return fmt.Errorf("load integration customer %s: %w", e.ic.ID, err)
		}
		if ic.ExternalCustomerID != "" {
			return nil
		}
		if err := r.IntegrationCustomers.Delete(ctx, ic.OrganizationID, ic.ID); err != nil {
			return fmt.Errorf("delete integration customer %s: %w", ic.ID, err)
		}
		return nil
	})
}

// reload opens the connection of i again after a change.
func (s *Service) reload(ctx context.Context, i *integration.Integration) (integration.Connection, error) {
	_, conn, err := s.load(ctx, i.OrganizationID, i.ID)
	return conn, err
}
