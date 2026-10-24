package integrations_test

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// TestIntegrationCreateUpdate ports the create and update services of the
// tax, accounting and CRM integrations: each creates and updates its
// integration with a security log, and requires a name.
func TestIntegrationCreateUpdate(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		in                               integrations.Input
		created, createLog, createNoName string
		updated, updateLog, updateNoName string
		// model cites the model validations; required are the values
		// the integration cannot do without.
		model    string
		required []string
	}{
		{
			in: integrations.Input{Code: "anrok1", Name: "Anrok 1", Provider: integration.ProviderAnrok, Values: map[string]string{integrations.KeyAPIKey: "key"}},
			// The update spec has no validation case.
			created:   "spec/services/integrations/anrok/create_service_spec.rb:42, 50",
			createLog: "spec/services/integrations/anrok/create_service_spec.rb:56",
			updated:   "spec/services/integrations/anrok/update_service_spec.rb:46",
			updateLog: "spec/services/integrations/anrok/update_service_spec.rb:52",
			model:     "spec/models/integrations/anrok_integration_spec.rb:8, 13",
		},
		{
			in: integrations.Input{Code: "avalara1", Name: "Avalara 1", Provider: integration.ProviderAvalara, Values: map[string]string{
				integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT", integrations.KeyLicenseKey: "lic",
			}},
			created:      "spec/services/integrations/avalara/create_service_spec.rb:57, 61",
			createLog:    "spec/services/integrations/avalara/create_service_spec.rb:81",
			createNoName: "spec/services/integrations/avalara/create_service_spec.rb:89",
			updated:      "spec/services/integrations/avalara/update_service_spec.rb:53, 61",
			updateLog:    "spec/services/integrations/avalara/update_service_spec.rb:67",
			updateNoName: "spec/services/integrations/avalara/update_service_spec.rb:75",
			model:        "spec/models/integrations/avalara_integration_spec.rb:8, 10, 11, 12, 16",
			required:     []string{integrations.KeyAccountID, integrations.KeyCompanyCode, integrations.KeyLicenseKey},
		},
		{
			in: integrations.Input{Code: "hubspot1", Name: "Hubspot 1", Provider: integration.ProviderHubspot, Values: map[string]string{
				integrations.KeyDefaultTargetedObject: "companies",
			}},
			created:      "spec/services/integrations/hubspot/create_service_spec.rb:59, 72",
			createLog:    "spec/services/integrations/hubspot/create_service_spec.rb:86",
			createNoName: "spec/services/integrations/hubspot/create_service_spec.rb:94",
			updated:      "spec/services/integrations/hubspot/update_service_spec.rb:50, 57",
			updateLog:    "spec/services/integrations/hubspot/update_service_spec.rb:63",
			updateNoName: "spec/services/integrations/hubspot/update_service_spec.rb:71",
			model:        "spec/models/integrations/hubspot_integration_spec.rb:8, 10, 13",
			required:     []string{integrations.KeyDefaultTargetedObject},
		},
		{
			in:        netsuiteInput(),
			created:   "spec/services/integrations/netsuite/create_service_spec.rb:71",
			createLog: "spec/services/integrations/netsuite/create_service_spec.rb:88",
			updated:   "spec/services/integrations/netsuite/update_service_spec.rb:61",
			updateLog: "spec/services/integrations/netsuite/update_service_spec.rb:67",
			model:     "spec/models/integrations/netsuite_integration_spec.rb:8, 11",
		},
		{
			in: integrations.Input{Code: "salesforce1", Name: "Salesforce 1", Provider: integration.ProviderSalesforce, Values: map[string]string{
				integrations.KeyInstanceID: "https://example.my.salesforce.com",
			}},
			created:      "spec/services/integrations/salesforce/create_service_spec.rb:52",
			createLog:    "spec/services/integrations/salesforce/create_service_spec.rb:59",
			createNoName: "spec/services/integrations/salesforce/create_service_spec.rb:67",
			updated:      "spec/services/integrations/salesforce/update_service_spec.rb:45, 52",
			updateLog:    "spec/services/integrations/salesforce/update_service_spec.rb:58",
			updateNoName: "spec/services/integrations/salesforce/update_service_spec.rb:66",
			model:        "spec/models/integrations/salesforce_integration_spec.rb:8, 9, 13",
		},
		{
			in:           integrations.Input{Code: "xero1", Name: "Xero 1", Provider: integration.ProviderXero, SyncInvoices: true},
			created:      "spec/services/integrations/xero/create_service_spec.rb:55, 62",
			createLog:    "spec/services/integrations/xero/create_service_spec.rb:72",
			createNoName: "spec/services/integrations/xero/create_service_spec.rb:80",
			updated:      "spec/services/integrations/xero/update_service_spec.rb:51, 58",
			updateNoName: "spec/services/integrations/xero/update_service_spec.rb:72",
			model:        "spec/models/integrations/xero_integration_spec.rb:8, 12",
		},
	} {
		p := tt.in.Provider
		newLogged := func(t *testing.T) (*billingtest.Harness, *integrations.Service, *securityLog) {
			t.Helper()
			h, svc := newService(t)
			l := &securityLog{}
			return h, svc.WithSecurityLog(l), l
		}
		t.Run(string(p)+"/create", func(t *testing.T) {
			t.Log("source:", tt.created, tt.createLog)
			h, svc, l := newLogged(t)
			i, err := svc.Create(ctx, h.OrgID(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := svc.Get(ctx, h.OrgID(), i.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.in.Name || got.Code != tt.in.Code || got.Provider != p || got.OrganizationID != h.OrgID() ||
				got.SyncInvoices != tt.in.SyncInvoices {
				t.Errorf("got = %+v, want = %+v", got, tt.in)
			}
			want := []logEntry{{integrations.EventCreated, map[string]any{"integration_name": tt.in.Name, "integration_type": string(p)}}}
			if !reflect.DeepEqual(l.entries, want) {
				t.Errorf("security log: got = %v, want = %v", l.entries, want)
			}
		})
		if tt.createNoName != "" {
			t.Run(string(p)+"/create without name", func(t *testing.T) {
				t.Log("source:", tt.createNoName)
				h, svc := newService(t)
				in := tt.in
				in.Name = ""
				_, err := svc.Create(ctx, h.OrgID(), in)
				if got := fieldCodes(err)["name"]; got != core.CodeRequired {
					t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
				}
			})
		}
		t.Run(string(p)+"/model validations", func(t *testing.T) {
			t.Log("source:", tt.model)
			h, svc := newService(t)
			for _, field := range append([]string{"name", "code"}, tt.required...) {
				in := tt.in
				in.Values = maps.Clone(tt.in.Values)
				switch field {
				case "name":
					in.Name = ""
				case "code":
					in.Code = ""
				default:
					delete(in.Values, field)
				}
				_, err := svc.Create(ctx, h.OrgID(), in)
				if got := fieldCodes(err)[field]; got != core.CodeRequired {
					t.Errorf("without %s: got = %q (%v), want = %q", field, got, err, core.CodeRequired)
				}
			}
			if _, err := svc.Create(ctx, h.OrgID(), tt.in); err != nil {
				t.Fatalf("Create: %v", err)
			}
			_, err := svc.Create(ctx, h.OrgID(), tt.in)
			if got := fieldCodes(err)["code"]; got != core.CodeAlreadyExists {
				t.Errorf("duplicate code: got = %q (%v), want = %q", got, err, core.CodeAlreadyExists)
			}
		})
		t.Run(string(p)+"/update", func(t *testing.T) {
			t.Log("source:", tt.updated, tt.updateLog)
			h, svc, l := newLogged(t)
			i, err := svc.Create(ctx, h.OrgID(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Name: core.Some("Renamed")})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if got.Name != "Renamed" {
				t.Errorf("name: got = %q, want = Renamed", got.Name)
			}
			if n := len(l.entries); n != 2 || l.entries[1].event != integrations.EventUpdated {
				t.Errorf("security log: got = %v, want = created, updated", l.entries)
			}
		})
		if tt.updateNoName != "" {
			t.Run(string(p)+"/update without name", func(t *testing.T) {
				t.Log("source:", tt.updateNoName)
				h, svc := newService(t)
				i, err := svc.Create(ctx, h.OrgID(), tt.in)
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				_, err = svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Name: core.Some("")})
				if got := fieldCodes(err)["name"]; got != core.CodeRequired {
					t.Errorf("got = %q (%v), want = %q", got, err, core.CodeRequired)
				}
			})
		}
	}
}

// TestConnectionJobs checks the jobs a usable connection starts: Avalara's
// on creation, Xero's and HubSpot's once their OAuth grant is stored.
func TestConnectionJobs(t *testing.T) {
	ctx := context.Background()
	t.Run("avalara", func(t *testing.T) {
		// source: spec/services/integrations/avalara/create_service_spec.rb:74
		h, svc := newService(t)
		if _, err := svc.Create(ctx, h.OrgID(), integrations.Input{Code: "avalara1", Name: "Avalara 1", Provider: integration.ProviderAvalara, Values: map[string]string{
			integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT", integrations.KeyLicenseKey: "lic",
		}}); err != nil {
			t.Fatal(err)
		}
		if got, want := h.JobKinds(), []string{kernel.JobFetchTaxCompany}; !slices.Equal(got, want) {
			t.Errorf("jobs: got = %v, want = %v", got, want)
		}
	})
	t.Run("netsuite", func(t *testing.T) {
		// NetSuite's create and update fetch no items (PerformSyncJob with
		// sync_items false, netsuite/create_service.rb:41,
		// update_service.rb:33); the rest of that job and the restlet job
		// only feed the aggregator.
		// source: spec/jobs/integrations/aggregator/perform_sync_job_spec.rb:37
		h, svc := newService(t)
		i, err := svc.Create(ctx, h.OrgID(), netsuiteInput())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Update(ctx, h.OrgID(), i.ID, integrations.Update{Name: core.Some("Netsuite 2")}); err != nil {
			t.Fatal(err)
		}
		if got := h.JobKinds(); len(got) != 0 {
			t.Errorf("jobs: got = %v, want = none", got)
		}
	})
	for _, tt := range []struct {
		source string
		in     integrations.Input
		want   []string
	}{
		{
			"spec/services/integrations/xero/create_service_spec.rb:68",
			integrations.Input{Code: "xero1", Name: "Xero 1", Provider: integration.ProviderXero},
			[]string{kernel.JobFetchIntegrationItems},
		},
		{
			"spec/services/integrations/hubspot/create_service_spec.rb:78",
			integrations.Input{Code: "hubspot1", Name: "Hubspot 1", Provider: integration.ProviderHubspot, Values: map[string]string{
				integrations.KeyDefaultTargetedObject: "companies",
			}},
			[]string{kernel.JobDeployCRM, kernel.JobSaveCRMAccount},
		},
	} {
		t.Run(string(tt.in.Provider), func(t *testing.T) {
			t.Log("source:", tt.source)
			h := billingtest.New(t, now)
			var gw integration.Gateway = fakeOAuth{tt.in.Provider}
			if tt.in.Provider == integration.ProviderXero {
				gw = oauthAccounting{&fakeAccounting{provider: tt.in.Provider}}
			}
			set, err := port.NewIntegrationProviderSet(gw)
			if err != nil {
				t.Fatal(err)
			}
			svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set))
			i, err := svc.Create(ctx, h.OrgID(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got := h.JobKinds(); len(got) != 0 {
				t.Errorf("jobs before the grant: got = %v, want = none", got)
			}
			start, err := svc.StartOAuth(ctx, h.OrgID(), i.ID, "https://app.test/callback")
			if err != nil {
				t.Fatalf("StartOAuth: %v", err)
			}
			if _, err := svc.CompleteOAuth(ctx, h.OrgID(), i.ID, "code", start.State); err != nil {
				t.Fatalf("CompleteOAuth: %v", err)
			}
			if got := h.JobKinds(); !slices.Equal(got, tt.want) {
				t.Errorf("jobs: got = %v, want = %v", got, tt.want)
			}
		})
	}
}

// oauthAccounting is an accounting adapter connected by OAuth, as Xero is.
type oauthAccounting struct{ *fakeAccounting }

func (g oauthAccounting) AuthorizationURL(conn integration.Connection, redirectURI, state string) (string, error) {
	return fakeOAuth{g.provider}.AuthorizationURL(conn, redirectURI, state)
}

func (g oauthAccounting) ExchangeCode(ctx context.Context, conn integration.Connection, redirectURI, code string) (integration.ConnectionUpdate, error) {
	return fakeOAuth{g.provider}.ExchangeCode(ctx, conn, redirectURI, code)
}

// TestStoredValues covers the settings and credentials of each provider:
// what is given is read back, the credentials sealed apart.
func TestStoredValues(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		source            string
		in                integrations.Input
		settings, secrets map[string]string
	}{
		{
			source: "spec/models/integrations/netsuite_integration_spec.rb:24, 45, 54, 61, 68, 75",
			in: integrations.Input{
				Code: "netsuite", Name: "NetSuite", Provider: integration.ProviderNetsuite,
				SyncCreditNotes: true, SyncInvoices: true, SyncPayments: true,
				Values: map[string]string{
					integrations.KeyAccountID: "acc", integrations.KeyClientID: "client", integrations.KeyClientSecret: "secret",
					integrations.KeyTokenID: "token", integrations.KeyTokenSecret: "token-secret",
					integrations.KeyScriptEndpointURL: "https://example.com/restlet",
				},
			},
			settings: map[string]string{
				integrations.KeyAccountID: "acc", integrations.KeyClientID: "client", integrations.KeyTokenID: "token",
				integrations.KeyScriptEndpointURL: "https://example.com/restlet",
			},
			secrets: map[string]string{integrations.KeyClientSecret: "secret", integrations.KeyTokenSecret: "token-secret"},
		},
		{
			source:  "spec/models/integrations/anrok_integration_spec.rb:19",
			in:      integrations.Input{Code: "anrok", Name: "Anrok", Provider: integration.ProviderAnrok, Values: map[string]string{integrations.KeyAPIKey: "key"}},
			secrets: map[string]string{integrations.KeyAPIKey: "key"},
		},
		{
			source: "spec/models/integrations/avalara_integration_spec.rb:22, 36, 43",
			in: integrations.Input{Code: "avalara", Name: "Avalara", Provider: integration.ProviderAvalara, Values: map[string]string{
				integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT", integrations.KeyLicenseKey: "lic",
			}},
			settings: map[string]string{integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT"},
			secrets:  map[string]string{integrations.KeyLicenseKey: "lic"},
		},
		{
			source: "spec/models/integrations/xero_integration_spec.rb:25, 32, 39",
			in: integrations.Input{
				Code: "xero", Name: "Xero", Provider: integration.ProviderXero,
				SyncCreditNotes: true, SyncInvoices: true, SyncPayments: true,
			},
		},
		{
			source: "spec/models/integrations/hubspot_integration_spec.rb:26, 40, 47",
			in: integrations.Input{
				Code: "hubspot", Name: "HubSpot", Provider: integration.ProviderHubspot, SyncInvoices: true, SyncSubscriptions: true,
				Values: map[string]string{integrations.KeyDefaultTargetedObject: "contacts"},
			},
			settings: map[string]string{integrations.KeyDefaultTargetedObject: "contacts"},
		},
		{
			source: "spec/models/integrations/salesforce_integration_spec.rb:19",
			in: integrations.Input{Code: "salesforce", Name: "Salesforce", Provider: integration.ProviderSalesforce, Values: map[string]string{
				integrations.KeyInstanceID: "https://example.my.salesforce.com",
			}},
			settings: map[string]string{integrations.KeyInstanceID: "https://example.my.salesforce.com"},
		},
	} {
		t.Run(string(tt.in.Provider), func(t *testing.T) {
			t.Log("source:", tt.source)
			h, svc := newService(t)
			created, err := svc.Create(ctx, h.OrgID(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			i, err := svc.Get(ctx, h.OrgID(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			settings, err := integrations.Settings(i)
			if err != nil {
				t.Fatal(err)
			}
			for k, want := range tt.settings {
				if settings[k] != want {
					t.Errorf("setting %s: got = %q, want = %q", k, settings[k], want)
				}
			}
			secrets, err := svc.Secrets(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			for k, want := range tt.secrets {
				if secrets[k] != want {
					t.Errorf("secret %s: got = %q, want = %q", k, secrets[k], want)
				}
				if _, ok := settings[k]; ok {
					t.Errorf("secret %s: stored in the settings", k)
				}
			}
			if i.SyncInvoices != tt.in.SyncInvoices || i.SyncCreditNotes != tt.in.SyncCreditNotes ||
				i.SyncPayments != tt.in.SyncPayments || i.SyncSubscriptions != tt.in.SyncSubscriptions {
				t.Errorf("sync flags: got = %v %v %v %v, want = %v %v %v %v",
					i.SyncInvoices, i.SyncCreditNotes, i.SyncPayments, i.SyncSubscriptions,
					tt.in.SyncInvoices, tt.in.SyncCreditNotes, tt.in.SyncPayments, tt.in.SyncSubscriptions)
			}
		})
	}
}
