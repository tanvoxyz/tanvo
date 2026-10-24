package integrations_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// contactFixture is a customer of type ct linked to an integration of p:
// NetSuite, or HubSpot targeting object. A create unlinks the record
// first, as a link waiting for its contact has none.
type contactFixture struct {
	*acctFixture
	set   func(integration.ContactResult, error)
	sent  func() int
	errs  func(*testing.T) []integrations.ProviderErrorDetails
	event webhook.EventType
}

func newContactFixture(t *testing.T, p integration.Provider, object string, ct customer.Type, create bool) *contactFixture {
	t.Helper()
	var f *contactFixture
	if p == integration.ProviderHubspot {
		cf := newCRMFixture(t, false)
		if err := cf.ic.SetSetting("targeted_object", object); err != nil {
			t.Fatal(err)
		}
		f = &contactFixture{
			acctFixture: cf.acctFixture,
			set:         func(r integration.ContactResult, err error) { cf.crm.contact, cf.crm.contactErr = r, err },
			sent:        func() int { return len(cf.crm.contacts) },
			errs:        cf.crmErrors,
			event:       webhook.EventTypeCustomerCRMProviderCreated,
		}
	} else {
		af := newAcctFixture(t, p, false)
		f = &contactFixture{
			acctFixture: af,
			set:         func(r integration.ContactResult, err error) { af.gw.contact, af.gw.contactErr = r, err },
			sent:        func() int { return len(af.gw.contacts) },
			errs:        af.customerErrors,
			event:       webhook.EventTypeCustomerAccountingProviderCreated,
		}
	}
	f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.CustomerType = ct })
	if create {
		f.ic.ExternalCustomerID = ""
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationCustomers.Update(ctx, f.ic) })
	return f
}

// webhookObjectTypes are the object types of the customer webhooks.
var webhookObjectTypes = map[webhook.EventType]string{
	webhook.EventTypeCustomerAccountingProviderCreated: "customer",
	webhook.EventTypeCustomerCRMProviderCreated:        "customer",
	webhook.EventTypeCustomerAccountingProviderError:   "accounting_provider_customer_error",
	webhook.EventTypeCustomerCRMProviderError:          "crm_provider_customer_error",
	webhook.EventTypeCustomerTaxProviderError:          "tax_provider_customer_error",
}

// link returns the stored integration customer, nil once removed.
func (f *contactFixture) link() *integration.IntegrationCustomer {
	var ic *integration.IntegrationCustomer
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		var err error
		ic, err = r.IntegrationCustomers.Get(ctx, f.h.OrgID(), f.ic.ID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		return err
	})
	return ic
}

// TestSyncContact ports the contacts and companies create and update
// services: a created record is stored on the link and announced; an
// answer without a record keeps the link without one; a failure emits the customer error webhook and
// fails the job, a refusal of the document's fields excepted.
func TestSyncContact(t *testing.T) {
	serverErr := &integration.ProviderError{
		Provider: integration.ProviderNetsuite, Kind: integration.KindUnavailable, HTTPStatus: 500,
		Code: "TypeError", Message: "Please enter value(s) for: Company Name",
	}
	clientErr := &integration.ProviderError{
		Provider: integration.ProviderNetsuite, Kind: integration.KindInvalidRequest, HTTPStatus: 404,
		Code: "invalid_secret_key_format", Message: "Authentication failed. The provided secret key is not a UUID v4.",
	}
	refused := &integration.ProviderError{
		Provider: integration.ProviderHubspot, Kind: integration.KindInvalidRequest, HTTPStatus: 200,
		Code: integration.CodeValidationError, Message: "Email is invalid. Name is required",
	}
	created := integration.ContactResult{ExternalID: "2e50c200-9a54-4a66-b241-1e75fb87373f", Email: "billing@example.com"}
	type want struct {
		failed  bool
		removed bool
		linked  string
		errs    []integrations.ProviderErrorDetails
		event   bool
	}
	for _, tt := range []struct {
		name     string
		sources  []string
		provider integration.Provider
		object   string
		ct       customer.Type
		update   bool
		res      integration.ContactResult
		err      error
		want     want
	}{
		{
			name: "netsuite/create",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:96, 103",
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:141, 148",
				"spec/services/integration_customers/netsuite_service_spec.rb:28, 40",
			},
			provider: integration.ProviderNetsuite, res: created,
			want: want{linked: created.ExternalID, event: true},
		},
		{
			name: "xero/create",
			sources: []string{
				"spec/services/integration_customers/xero_service_spec.rb:26, 37",
			},
			provider: integration.ProviderXero, res: created,
			want: want{linked: created.ExternalID, event: true},
		},
		{
			name: "avalara/create",
			sources: []string{
				"spec/services/integration_customers/avalara_service_spec.rb:26, 37",
			},
			provider: integration.ProviderAvalara, res: created,
			want: want{linked: created.ExternalID, event: true},
		},
		{
			// avalara_service.rb:16: no record, no link.
			name: "avalara/create without record", provider: integration.ProviderAvalara,
			want: want{removed: true},
		},
		{
			name: "hubspot/create contact",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:421",
				"spec/services/integration_customers/hubspot_service_spec.rb:28, 40",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectContacts, ct: customer.TypeIndividual, res: created,
			want: want{linked: created.ExternalID, event: true},
		},
		{
			name: "hubspot/create company",
			sources: []string{
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:45",
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:84, 92",
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:128, 135",
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:324",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, res: created,
			want: want{linked: created.ExternalID, event: true},
		},
		{
			name: "netsuite/create without record",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:165, 172",
			},
			provider: integration.ProviderNetsuite,
		},
		{
			name: "hubspot/create company without record",
			sources: []string{
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:152, 159",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany,
		},
		{
			name: "netsuite/create server error",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:304",
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:335, 343",
			},
			provider: integration.ProviderNetsuite, err: serverErr,
			want: want{failed: true, removed: true, errs: []integrations.ProviderErrorDetails{{ErrorCode: serverErr.Code, Message: serverErr.Message}}},
		},
		{
			name: "netsuite/create client error",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:370, 378",
			},
			provider: integration.ProviderNetsuite, err: clientErr,
			want: want{failed: true, removed: true, errs: []integrations.ProviderErrorDetails{{ErrorCode: clientErr.Code, Message: clientErr.Message}}},
		},
		{
			name: "hubspot/create company server error",
			sources: []string{
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:203, 211",
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:238, 246",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, err: serverErr,
			want: want{failed: true, removed: true, errs: []integrations.ProviderErrorDetails{{ErrorCode: serverErr.Code, Message: serverErr.Message}}},
		},
		{
			name: "hubspot/create company client error",
			sources: []string{
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:273, 281",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, err: clientErr,
			want: want{failed: true, removed: true, errs: []integrations.ProviderErrorDetails{{ErrorCode: clientErr.Code, Message: clientErr.Message}}},
		},
		{
			name: "hubspot/create contact refused",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:443",
				"spec/services/integrations/aggregator/contacts/create_service_spec.rb:465",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectContacts, ct: customer.TypeIndividual, err: refused,
			want: want{errs: []integrations.ProviderErrorDetails{{ErrorCode: refused.Code, Message: refused.Message}}},
		},
		{
			name: "hubspot/create company refused",
			sources: []string{
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:346",
				"spec/services/integrations/aggregator/companies/create_service_spec.rb:368",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, err: refused,
			want: want{errs: []integrations.ProviderErrorDetails{{ErrorCode: refused.Code, Message: refused.Message}}},
		},
		{
			name: "netsuite/update",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/update_service_spec.rb:76",
				"spec/services/integrations/aggregator/contacts/update_service_spec.rb:119",
			},
			provider: integration.ProviderNetsuite, update: true, res: integration.ContactResult{ExternalID: "cus_ext"},
			want: want{linked: "cus_ext"},
		},
		{
			name: "hubspot/update company",
			sources: []string{
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:42",
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:97",
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:140",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, update: true,
			res:  integration.ContactResult{ExternalID: "501"},
			want: want{linked: "501"},
		},
		{
			name: "netsuite/update server error",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/update_service_spec.rb:178, 187",
				"spec/services/integrations/aggregator/contacts/update_service_spec.rb:214, 222",
			},
			provider: integration.ProviderNetsuite, update: true, err: serverErr,
			want: want{failed: true, linked: "cus_ext", errs: []integrations.ProviderErrorDetails{{ErrorCode: serverErr.Code, Message: serverErr.Message}}},
		},
		{
			name: "netsuite/update client error",
			sources: []string{
				"spec/services/integrations/aggregator/contacts/update_service_spec.rb:249, 257",
			},
			provider: integration.ProviderNetsuite, update: true, err: clientErr,
			want: want{failed: true, linked: "cus_ext", errs: []integrations.ProviderErrorDetails{{ErrorCode: clientErr.Code, Message: clientErr.Message}}},
		},
		{
			name: "hubspot/update company server error",
			sources: []string{
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:195, 204",
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:231, 239",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, update: true, err: serverErr,
			want: want{failed: true, linked: "501", errs: []integrations.ProviderErrorDetails{{ErrorCode: serverErr.Code, Message: serverErr.Message}}},
		},
		{
			name: "hubspot/update company client error",
			sources: []string{
				"spec/services/integrations/aggregator/companies/update_service_spec.rb:266, 274",
			},
			provider: integration.ProviderHubspot, object: integration.TargetedObjectCompanies, ct: customer.TypeCompany, update: true, err: clientErr,
			want: want{failed: true, linked: "501", errs: []integrations.ProviderErrorDetails{{ErrorCode: clientErr.Code, Message: clientErr.Message}}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.sources {
				t.Log("source:", s)
			}
			f := newContactFixture(t, tt.provider, tt.object, tt.ct, !tt.update)
			f.set(tt.res, tt.err)
			// source: spec/jobs/integration_customers/create_job_spec.rb:21
			// source: spec/jobs/integration_customers/update_job_spec.rb:21
			err := f.run(kernel.JobSyncContact, kernel.IntegrationCustomerArgs{IntegrationCustomerID: f.ic.ID, Update: tt.update}, 1)
			if (err != nil) != tt.want.failed {
				t.Fatalf("job error: got = %v, want failed = %v", err, tt.want.failed)
			}
			if f.sent() != 1 {
				t.Errorf("contacts sent: got = %d, want = 1", f.sent())
			}
			ic := f.link()
			switch {
			case tt.want.removed && ic != nil:
				t.Errorf("link: got = %+v, want = removed", ic)
			case !tt.want.removed && ic == nil:
				t.Errorf("link: got = removed, want = %q", tt.want.linked)
			case ic != nil && ic.ExternalCustomerID != tt.want.linked:
				t.Errorf("external customer ID: got = %q, want = %q", ic.ExternalCustomerID, tt.want.linked)
			}
			if tt.provider == integration.ProviderHubspot && tt.want.event {
				if got, _ := ic.Setting("email"); got != created.Email {
					t.Errorf("email: got = %q, want = %q", got, created.Email)
				}
				if got, _ := ic.Setting("targeted_object"); got != tt.object {
					t.Errorf("targeted object: got = %q, want = %q", got, tt.object)
				}
			}
			if got := f.errs(t); !slices.Equal(got, tt.want.errs) {
				t.Errorf("error webhooks: got = %v, want = %v", got, tt.want.errs)
			}
			if got := slices.Contains(f.h.Webhooks(), f.event); got != tt.want.event {
				t.Errorf("%s: got = %v, want = %v", f.event, got, tt.want.event)
			}
			// source: spec/services/webhooks/integrations/accounting_customer_created_service_spec.rb:12
			// source: spec/services/webhooks/integrations/accounting_customer_error_service_spec.rb:13
			// source: spec/services/webhooks/integrations/crm_customer_created_service_spec.rb:12
			// source: spec/services/webhooks/integrations/crm_customer_error_service_spec.rb:13
			// source: spec/services/webhooks/integrations/taxes/error_service_spec.rb:17
			for _, e := range f.h.Emitted.Events() {
				if want, ok := webhookObjectTypes[e.Type]; ok && e.ObjectType != want {
					t.Errorf("%s object type: got = %q, want = %q", e.Type, e.ObjectType, want)
				}
			}
		})
	}
	t.Run("hubspot/update individual as company", func(t *testing.T) {
		t.Log("source: spec/services/integrations/aggregator/companies/update_service_spec.rb:52")
		f := newContactFixture(t, integration.ProviderHubspot, integration.TargetedObjectCompanies, customer.TypeIndividual, false)
		err := f.run(kernel.JobSyncContact, kernel.IntegrationCustomerArgs{IntegrationCustomerID: f.ic.ID, Update: true}, 1)
		if err == nil || err.Error() == "" {
			t.Fatalf("job error: got = %v, want = not a company", err)
		}
		if f.sent() != 0 {
			t.Errorf("contacts sent: got = %d, want = 0", f.sent())
		}
	})
	t.Run("netsuite/create of a linked record", func(t *testing.T) {
		t.Log("source: spec/services/integration_customers/netsuite_service_spec.rb:50, 55")
		f := newContactFixture(t, integration.ProviderNetsuite, "", "", false)
		if err := f.run(kernel.JobSyncContact, kernel.IntegrationCustomerArgs{IntegrationCustomerID: f.ic.ID}, 1); err != nil {
			t.Fatalf("job: %v", err)
		}
		if f.sent() != 0 {
			t.Errorf("contacts sent: got = %d, want = 0", f.sent())
		}
		if ic := f.link(); ic == nil || ic.ExternalCustomerID != "cus_ext" {
			t.Errorf("link: got = %+v, want = cus_ext", ic)
		}
	})
}

// TestContactTargetedObject ports the HubSpot object a new link targets:
// the one asked for, else the customer's type's, else the integration's
// default.
func TestContactTargetedObject(t *testing.T) {
	for _, tt := range []struct {
		source string
		asked  string
		ct     customer.Type
		want   integration.CRMObject
	}{
		{"spec/services/integration_customers/hubspot_service_spec.rb:51", integration.TargetedObjectContacts, customer.TypeCompany, integration.CRMObjectContacts},
		{"spec/services/integration_customers/hubspot_service_spec.rb:61", integration.TargetedObjectCompanies, customer.TypeIndividual, integration.CRMObjectCompanies},
		{"spec/services/integration_customers/hubspot_service_spec.rb:72", "", customer.TypeIndividual, integration.CRMObjectContacts},
		{"spec/services/integration_customers/hubspot_service_spec.rb:83", "", customer.TypeCompany, integration.CRMObjectCompanies},
		{"spec/services/integration_customers/hubspot_service_spec.rb:83", "", "", integration.CRMObjectCompanies},
	} {
		t.Run(tt.source+"/"+tt.asked+"/"+string(tt.ct), func(t *testing.T) {
			t.Log("source:", tt.source)
			cf := newCRMFixture(t, false)
			cf.ic.ExternalCustomerID, cf.ic.Settings = "", nil
			if tt.asked != "" {
				if err := cf.ic.SetSetting("targeted_object", tt.asked); err != nil {
					t.Fatal(err)
				}
			}
			cf.h.UpdateCustomer(cf.cust, func(c *customer.Customer) { c.CustomerType = tt.ct })
			cf.h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationCustomers.Update(ctx, cf.ic) })
			cf.crm.contact = integration.ContactResult{ExternalID: "77"}
			if err := cf.run(kernel.JobSyncContact, kernel.IntegrationCustomerArgs{IntegrationCustomerID: cf.ic.ID}, 1); err != nil {
				t.Fatalf("job: %v", err)
			}
			if !slices.Equal(cf.crm.deploys, []integration.CRMObject{tt.want}) {
				t.Errorf("deployed: got = %v, want = %v", cf.crm.deploys, tt.want)
			}
			wantObject := integration.TargetedObjectCompanies
			if tt.want == integration.CRMObjectContacts {
				wantObject = integration.TargetedObjectContacts
			}
			if len(cf.crm.contacts) != 1 || cf.crm.contacts[0].TargetedObject != wantObject {
				t.Errorf("contact sent: got = %+v, want = targeting %s", cf.crm.contacts, wantObject)
			}
		})
	}
}
