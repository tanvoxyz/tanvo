package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Keys of the pending OAuth grant of a connection, kept with its secrets
// between [Service.StartOAuth] and [Service.CompleteOAuth].
const (
	keyOAuthState       = "oauth_state"
	keyOAuthRedirectURI = "oauth_redirect_uri"
)

// codeIntegrationError is the code of a failed HubSpot deployment (lago
// hubspot/*/deploy_*_service.rb).
const codeIntegrationError = "integration_error"

// CodeOAuthStateMismatch is the field error of an OAuth completion whose
// state is not the one [Service.StartOAuth] returned.
const CodeOAuthStateMismatch = "oauth_state_mismatch"

// oauthProvider reports the providers connected by an OAuth grant: their
// follow-up jobs wait for [Service.CompleteOAuth].
func oauthProvider(p integration.Provider) bool {
	return p == integration.ProviderXero || p == integration.ProviderHubspot || p == integration.ProviderGocardless
}

// connectionCreated enqueues, in the transaction of r, the jobs lago runs
// once a connection exists (Integrations::<Provider>::CreateService):
// Avalara resolves its company, Xero fetches its items, HubSpot deploys
// its objects and stores its portal. A connection of an OAuth provider is
// not usable before its grant, so these run on [Service.CompleteOAuth].
func (s *Service) connectionCreated(ctx context.Context, r port.Repos, i *integration.Integration) error {
	if oauthProvider(i.Provider) {
		return nil
	}
	return s.connected(ctx, r, i)
}

// connected enqueues the follow-up jobs of the usable connection i.
func (s *Service) connected(ctx context.Context, r port.Repos, i *integration.Integration) error {
	// A payment connection registers its webhook endpoint
	// (stripe_service.rb:37-39); GoCardless's runs once it holds its grant.
	if err := kernel.EnqueuePaymentConnectionCreated(ctx, r, i); err != nil {
		return err
	}
	args := kernel.IntegrationArgs{IntegrationID: i.ID}
	key := "integration:" + i.ID.String()
	switch i.Provider {
	case integration.ProviderAvalara:
		// avalara/create_service.rb:33
		return kernel.Enqueue(ctx, r, i.OrganizationID, kernel.JobFetchTaxCompany, args, key+":company", time.Time{})
	case integration.ProviderXero:
		// xero/create_service.rb:25: PerformSyncJob, 2 seconds later.
		return kernel.Enqueue(ctx, r, i.OrganizationID, kernel.JobFetchIntegrationItems, args, key+":items", s.d.Now().Add(2*time.Second))
	case integration.ProviderHubspot:
		// hubspot/create_service.rb:34-35
		if err := kernel.Enqueue(ctx, r, i.OrganizationID, kernel.JobDeployCRM, args, key+":deploy", time.Time{}); err != nil {
			return err
		}
		return kernel.Enqueue(ctx, r, i.OrganizationID, kernel.JobSaveCRMAccount, args, key+":account", time.Time{})
	default:
		// NetSuite's create only starts the aggregator's syncs
		// (netsuite/create_service.rb:40-41), which direct calls do not
		// need.
		return nil
	}
}

// OAuthStart is the consent page of an OAuth connection.
type OAuthStart struct {
	// AuthorizationURL is the provider's consent page.
	AuthorizationURL string
	// State is the value the provider sends back with the code.
	State string
}

// StartOAuth returns the provider's consent page of the integration id,
// redirecting to redirectURI (replacing lago's Nango connect flow). The
// state and redirect URI are kept until [Service.CompleteOAuth].
func (s *Service) StartOAuth(ctx context.Context, org core.OrganizationID, id core.IntegrationID, redirectURI string) (OAuthStart, error) {
	i, conn, g, err := s.oauth(ctx, org, id)
	if err != nil {
		return OAuthStart{}, err
	}
	b := make([]byte, 32)
	if _, err := s.rand.Read(b); err != nil {
		return OAuthStart{}, fmt.Errorf("integration %s: oauth state: %w", i.Code, err)
	}
	state := hex.EncodeToString(b)
	u, err := g.AuthorizationURL(conn, redirectURI, state)
	if err != nil {
		return OAuthStart{}, fmt.Errorf("integration %s: authorization url: %w", i.Code, err)
	}
	err = s.UpdateConnection(ctx, conn, integration.ConnectionUpdate{Secrets: map[string]string{
		keyOAuthState: state, keyOAuthRedirectURI: redirectURI,
	}})
	if err != nil {
		return OAuthStart{}, err
	}
	return OAuthStart{AuthorizationURL: u, State: state}, nil
}

// CompleteOAuth exchanges the provider's authorization code of the
// integration id, stores the grant and enqueues the connection's
// follow-up jobs.
func (s *Service) CompleteOAuth(ctx context.Context, org core.OrganizationID, id core.IntegrationID, code, state string) (*integration.Integration, error) {
	i, conn, g, err := s.oauth(ctx, org, id)
	if err != nil {
		return nil, err
	}
	want := conn.Secret(keyOAuthState)
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(state)) != 1 {
		return nil, &core.ValidationError{Errors: []core.FieldError{{Field: "state", Code: CodeOAuthStateMismatch}}}
	}
	u, err := g.ExchangeCode(ctx, conn, conn.Secret(keyOAuthRedirectURI), code)
	if err != nil {
		return nil, fmt.Errorf("integration %s: exchange code: %w", i.Code, err)
	}
	if u.Secrets == nil {
		u.Secrets = map[string]string{}
	}
	u.Secrets[keyOAuthState], u.Secrets[keyOAuthRedirectURI] = "", ""
	if err := s.UpdateConnection(ctx, conn, u); err != nil {
		return nil, err
	}
	var out *integration.Integration
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if out, err = get(ctx, r, org, id); err != nil {
			return err
		}
		return s.connected(ctx, r, out)
	})
	return out, err
}

func (s *Service) oauth(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, integration.Connection, integration.OAuthGateway, error) {
	i, conn, err := s.load(ctx, org, id)
	if err != nil {
		return nil, conn, nil, err
	}
	g, err := s.gateway(i.Provider)
	if err != nil {
		return nil, conn, nil, err
	}
	og, ok := g.(integration.OAuthGateway)
	if !ok {
		return nil, conn, nil, fmt.Errorf("integration %s: oauth: %w", i.Code, integration.ErrUnsupported)
	}
	return i, conn, og, nil
}

// load loads the integration id and opens its connection.
func (s *Service) load(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, integration.Connection, error) {
	var (
		i    *integration.Integration
		conn integration.Connection
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		i, conn, err = s.loadConnection(ctx, r, org, id)
		return err
	})
	return i, conn, err
}

// accounting returns the accounting adapter of i.
func (s *Service) accounting(i *integration.Integration) (integration.AccountingGateway, error) {
	if s.gw == nil {
		return nil, fmt.Errorf("integration %s: %w", i.Code, integration.ErrUnsupported)
	}
	g, err := s.gw.AccountingGateway(i.Provider)
	if err != nil {
		return nil, fmt.Errorf("integration %s: %w", i.Code, err)
	}
	return g, nil
}

// crm returns the CRM adapter of i.
func (s *Service) crm(i *integration.Integration) (integration.CRMGateway, error) {
	if s.gw == nil {
		return nil, fmt.Errorf("integration %s: %w", i.Code, integration.ErrUnsupported)
	}
	g, err := s.gw.CRMGateway(i.Provider)
	if err != nil {
		return nil, fmt.Errorf("integration %s: %w", i.Code, err)
	}
	return g, nil
}

// SyncItems fetches the provider's records of type t again, or every type
// the provider has when t is "" (lago fetch_integration_items and
// fetch_integration_accounts mutations). Tax items are not fetched: they
// are mapped by hand.
func (s *Service) SyncItems(ctx context.Context, org core.OrganizationID, id core.IntegrationID, t integration.ItemType) error {
	if t == "" || t == integration.ItemTypeStandard {
		if _, err := s.FetchItems(ctx, org, id); err != nil {
			return err
		}
	}
	if t == "" || t == integration.ItemTypeAccount {
		if _, err := s.FetchAccounts(ctx, org, id); err != nil && (t != "" || !errors.Is(err, integration.ErrUnsupported)) {
			return err
		}
	}
	return nil
}

// FetchItems replaces the cached standard items of the accounting
// integration id with the provider's (lago Integrations::Aggregator::
// ItemsService).
func (s *Service) FetchItems(ctx context.Context, org core.OrganizationID, id core.IntegrationID) ([]*integration.Item, error) {
	return s.fetchRecords(ctx, org, id, integration.ItemTypeStandard, integration.AccountingGateway.FetchItems)
}

// FetchAccounts replaces the cached ledger accounts of the accounting
// integration id with the provider's (lago Integrations::Aggregator::
// AccountsService).
func (s *Service) FetchAccounts(ctx context.Context, org core.OrganizationID, id core.IntegrationID) ([]*integration.Item, error) {
	return s.fetchRecords(ctx, org, id, integration.ItemTypeAccount, integration.AccountingGateway.FetchAccounts)
}

func (s *Service) fetchRecords(ctx context.Context, org core.OrganizationID, id core.IntegrationID, t integration.ItemType,
	fetch func(integration.AccountingGateway, context.Context, integration.Connection) ([]integration.ExternalRecord, error),
) ([]*integration.Item, error) {
	i, conn, err := s.load(ctx, org, id)
	if err != nil {
		return nil, err
	}
	g, err := s.accounting(i)
	if err != nil {
		return nil, err
	}
	recs, err := fetch(g, ctx, conn)
	if err != nil {
		return nil, retriedFailure(fmt.Errorf("integration %s: fetch %s items: %w", i.Code, t, err))
	}
	var out []*integration.Item
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		out = out[:0]
		if _, err := r.IntegrationItems.DeleteByType(ctx, org, id, t); err != nil {
			return fmt.Errorf("delete %s items of integration %s: %w", t, id, err)
		}
		now := s.d.Now()
		for _, rec := range recs {
			it := &integration.Item{
				ID: core.NewID[core.IntegrationItemID](s.d.IDs), OrganizationID: org, IntegrationID: id,
				ItemType: t, ExternalID: rec.ID, ExternalAccountCode: optional(rec.AccountCode), ExternalName: optional(rec.Name),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := it.Validate(); err != nil {
				return err
			}
			if err := r.IntegrationItems.Upsert(ctx, it); err != nil {
				return fmt.Errorf("store %s item %s: %w", t, rec.ID, err)
			}
			out = append(out, it)
		}
		return nil
	})
	return out, err
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Subsidiaries returns the provider's subsidiaries of the integration id
// (lago Integrations::Aggregator::SubsidiariesService, not stored).
func (s *Service) Subsidiaries(ctx context.Context, org core.OrganizationID, id core.IntegrationID) ([]integration.ExternalRecord, error) {
	i, conn, err := s.load(ctx, org, id)
	if err != nil {
		return nil, err
	}
	g, err := s.gateway(i.Provider)
	if err != nil {
		return nil, err
	}
	l, ok := g.(integration.SubsidiaryLister)
	if !ok {
		return nil, fmt.Errorf("integration %s: subsidiaries: %w", i.Code, integration.ErrUnsupported)
	}
	return l.Subsidiaries(ctx, conn)
}

// crmDeploys are what a CRM connection deploys, in lago's order
// (aggregator/sync_custom_objects_and_properties_job.rb:11-14): the
// invoice and subscription objects, then the company and contact
// properties. The properties of the custom objects follow before their
// first record sync.
var crmDeploys = []struct {
	object integration.CRMObject
	parts  integration.CRMSchemaPart
}{
	{integration.CRMObjectInvoices, integration.CRMSchemaObject},
	{integration.CRMObjectSubscriptions, integration.CRMSchemaObject},
	{integration.CRMObjectCompanies, integration.CRMSchemaProperties},
	{integration.CRMObjectContacts, integration.CRMSchemaProperties},
}

// deployCRM deploys the custom objects and the company and contact
// properties of the CRM integration id.
func (s *Service) deployCRM(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error {
	for _, d := range crmDeploys {
		if err := s.deploy(ctx, org, id, d.object, d.parts); err != nil {
			return err
		}
	}
	return nil
}

// deploy deploys object o of the CRM integration id when its version is
// outdated (lago Integrations::Hubspot::*::DeployObjectService and
// DeployPropertiesService). A provider failure emits
// integration.provider_error and is otherwise ignored, as lago does.
func (s *Service) deploy(ctx context.Context, org core.OrganizationID, id core.IntegrationID, o integration.CRMObject, parts integration.CRMSchemaPart) error {
	i, conn, err := s.load(ctx, org, id)
	if err != nil {
		return err
	}
	g, err := s.crm(i)
	if err != nil {
		return err
	}
	u, err := g.Deploy(ctx, conn, o, parts)
	if err != nil {
		if errors.Is(err, integration.ErrNotConnected) {
			return err
		}
		return s.emitIntegrationError(ctx, i, ProviderErrorDetails{ErrorCode: codeIntegrationError, Message: errorDetails(err).Message})
	}
	return s.UpdateConnection(ctx, conn, u)
}

// saveCRMAccount stores the provider account of the CRM integration id
// when unknown (lago Integrations::Hubspot::SavePortalIdService).
func (s *Service) saveCRMAccount(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error {
	i, conn, err := s.load(ctx, org, id)
	if err != nil {
		return err
	}
	if conn.Setting(KeyPortalID) != "" {
		return nil
	}
	g, err := s.crm(i)
	if err != nil {
		return err
	}
	acct, err := g.AccountID(ctx, conn)
	if err != nil {
		return retriedFailure(fmt.Errorf("integration %s: account: %w", i.Code, err))
	}
	return s.UpdateConnection(ctx, conn, integration.ConnectionUpdate{Settings: map[string]string{KeyPortalID: acct}})
}

// invalidLoginAttempt is the NetSuite code of rejected credentials, not
// retried (lago aggregator/invoices/create_service.rb).
const invalidLoginAttempt = "INVALID_LOGIN_ATTEMPT"

// retriedFailure marks err [ErrSyncRetryable] when it wraps a provider
// answer with an HTTP status: lago's item, account and reconcile calls do
// not rescue LagoHttpClient::HttpError, which their jobs retry whatever
// the status (fetch_items_job.rb:8, save_portal_id_job.rb:8,
// invoices/create_job.rb:19). A rate limit keeps its class; other errors,
// a timeout among them, are returned unchanged.
func retriedFailure(err error) error {
	pe, ok := integration.AsProviderError(err)
	if !ok || pe.Kind == integration.KindRequestLimit || pe.HTTPStatus == 0 {
		return err
	}
	return fmt.Errorf("%w: %w", ErrSyncRetryable, err)
}
