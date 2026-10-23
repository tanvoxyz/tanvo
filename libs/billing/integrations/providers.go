package integrations

import (
	"net/url"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Field error codes of the service, named after lago's.
const (
	// CodeURLInvalid: the value is not an absolute http(s) URL (lago
	// UrlValidator).
	CodeURLInvalid = "url_invalid"
)

// Setting keys: the names of lago's settings and secrets accessors.
const (
	KeySecretKey             = "secret_key"
	KeySuccessRedirectURL    = "success_redirect_url"
	KeyWebhookID             = "webhook_id"
	KeyWebhookSecret         = "webhook_secret"
	KeyAPIKey                = "api_key"
	KeyMerchantAccount       = "merchant_account"
	KeyLivePrefix            = "live_prefix"
	KeyHMACKey               = "hmac_key"
	KeyAccessToken           = "access_token"
	KeyClientID              = "client_id"
	KeyClientSecret          = "client_secret"
	KeyFlowID                = "flow_id"
	KeySignatureKey          = "signature_key"
	KeyAccountID             = "account_id"
	KeyTokenID               = "token_id"
	KeyTokenSecret           = "token_secret"
	KeyScriptEndpointURL     = "script_endpoint_url"
	KeyLicenseKey            = "license_key"
	KeyCompanyCode           = "company_code"
	KeyCompanyID             = "company_id"
	KeyDefaultTargetedObject = "default_targeted_object"
	KeyPortalID              = "portal_id"
	KeyInstanceID            = "instance_id"
	KeyExternalAccountID     = "external_account_id"
	// KeySupports3DS and KeyRequireTermsOfServiceConsent are Stripe
	// flags, stored as "true" and left out when false.
	KeySupports3DS                  = "supports_3ds"
	KeyRequireTermsOfServiceConsent = "require_terms_of_service_consent"
)

// Hubspot targeted objects (lago HubspotIntegration::TARGETED_OBJECTS).
const (
	TargetedObjectCompanies = "companies"
	TargetedObjectContacts  = "contacts"
)

// Sync flags, the fields of [Input] and [Update] a provider may support.
const (
	syncInvoices      = "sync_invoices"
	syncCreditNotes   = "sync_credit_notes"
	syncPayments      = "sync_payments"
	syncSubscriptions = "sync_subscriptions"
)

// spec is what a provider accepts, after its lago model and its create
// and update services.
type spec struct {
	// settings are the non-secret keys given by the caller.
	settings []string
	// secrets are the credential keys given by the caller.
	secrets []string
	// required are the keys a create must give (lago presence
	// validations; the Nango connection_id is not used).
	required []string
	// updatable are the keys an update may change.
	updatable []string
	// urls are the keys holding URLs.
	urls []string
	// maxLen bounds keys beyond the API's limits.
	maxLen map[string]int
	// bools are the keys holding flags: "true", or "false" (stored as
	// absent).
	bools []string
	// sync are the sync flags the provider stores.
	sync []string
	// generated are the secrets created with the integration, by key.
	generated map[string]func(r randReader) (string, error)
}

// specs are the providers served (lago PaymentProviders::* and
// Integrations::* models); the auth providers are managed by libs/auth.
var specs = map[integration.Provider]spec{
	// payment_providers/stripe_provider.rb:35-41, stripe_service.rb:27-34:
	// the secret key is set on creation only.
	integration.ProviderStripe: {
		settings: []string{KeySuccessRedirectURL, KeySupports3DS, KeyRequireTermsOfServiceConsent}, secrets: []string{KeySecretKey},
		required:  []string{KeySecretKey},
		updatable: []string{KeySuccessRedirectURL, KeySupports3DS, KeyRequireTermsOfServiceConsent},
		urls:      []string{KeySuccessRedirectURL},
		bools:     []string{KeySupports3DS, KeyRequireTermsOfServiceConsent},
	},
	// adyen_provider.rb:14-18, adyen_service.rb:25-31.
	integration.ProviderAdyen: {
		settings: []string{KeyMerchantAccount, KeyLivePrefix, KeySuccessRedirectURL}, secrets: []string{KeyAPIKey, KeyHMACKey},
		required:  []string{KeyAPIKey, KeyMerchantAccount},
		updatable: []string{KeyAPIKey, KeyMerchantAccount, KeyLivePrefix, KeyHMACKey, KeySuccessRedirectURL},
		urls:      []string{KeySuccessRedirectURL},
	},
	// gocardless_provider.rb:11-14, gocardless_service.rb:33-37: the
	// access token comes from the OAuth exchange; the webhook secret is
	// 50 random alphanumerics.
	integration.ProviderGocardless: {
		settings: []string{KeySuccessRedirectURL}, updatable: []string{KeySuccessRedirectURL}, urls: []string{KeySuccessRedirectURL},
		generated: map[string]func(randReader) (string, error){KeyWebhookSecret: func(r randReader) (string, error) { return alphanumeric(r, 50) }},
	},
	// cashfree_provider.rb:15-19, cashfree_service.rb:23-27.
	integration.ProviderCashfree: {
		settings: []string{KeyClientID, KeySuccessRedirectURL}, secrets: []string{KeyClientSecret},
		required: []string{KeyClientID, KeyClientSecret}, updatable: []string{KeyClientID, KeyClientSecret, KeySuccessRedirectURL},
		urls: []string{KeySuccessRedirectURL},
	},
	// moneyhash_provider.rb:22-26, moneyhash_service.rb:23-26.
	integration.ProviderMoneyhash: {
		settings: []string{KeyFlowID}, secrets: []string{KeyAPIKey},
		required: []string{KeyAPIKey, KeyFlowID}, updatable: []string{KeyAPIKey, KeyFlowID},
		maxLen: map[string]int{KeyFlowID: 20},
	},
	// flutterwave_provider.rb:13-32, flutterwave_service.rb:23-26: the
	// webhook secret is 32 random bytes in hex.
	integration.ProviderFlutterwave: {
		settings: []string{KeySuccessRedirectURL}, secrets: []string{KeySecretKey},
		required: []string{KeySecretKey}, updatable: []string{KeySecretKey, KeySuccessRedirectURL},
		urls:      []string{KeySuccessRedirectURL},
		generated: map[string]func(randReader) (string, error){KeyWebhookSecret: func(r randReader) (string, error) { return hexString(r, 32) }},
	},
	// integrations/netsuite_integration.rb:5-22, netsuite/create_service.rb
	// and update_service.rb:23-28.
	integration.ProviderNetsuite: {
		settings: []string{KeyAccountID, KeyClientID, KeyTokenID, KeyScriptEndpointURL}, secrets: []string{KeyClientSecret, KeyTokenSecret},
		required:  []string{KeyClientID, KeyClientSecret, KeyAccountID, KeyScriptEndpointURL},
		updatable: []string{KeyScriptEndpointURL}, urls: []string{KeyScriptEndpointURL},
		sync: []string{syncCreditNotes, syncInvoices, syncPayments},
	},
	// xero_integration.rb:5-8, xero/update_service.rb.
	integration.ProviderXero: {sync: []string{syncCreditNotes, syncInvoices, syncPayments}},
	// anrok_integration.rb:9-11, anrok/update_service.rb.
	integration.ProviderAnrok: {secrets: []string{KeyAPIKey}, required: []string{KeyAPIKey}, updatable: []string{KeyAPIKey}},
	// avalara_integration.rb:9-12, avalara/update_service.rb (name only).
	integration.ProviderAvalara: {
		settings: []string{KeyAccountID, KeyCompanyCode}, secrets: []string{KeyLicenseKey},
		required: []string{KeyCompanyCode, KeyAccountID, KeyLicenseKey},
	},
	// hubspot_integration.rb:5-12, hubspot/update_service.rb.
	integration.ProviderHubspot: {
		settings: []string{KeyDefaultTargetedObject}, required: []string{KeyDefaultTargetedObject},
		updatable: []string{KeyDefaultTargetedObject}, sync: []string{syncInvoices, syncSubscriptions},
	},
	// salesforce_integration.rb:5-8, salesforce/update_service.rb.
	integration.ProviderSalesforce: {
		settings: []string{KeyInstanceID}, required: []string{KeyInstanceID}, updatable: []string{KeyInstanceID},
	},
}

// Supported reports whether the service manages integrations of p.
func Supported(p integration.Provider) bool {
	_, ok := specs[p]
	return ok
}

// isSecret reports whether key is a credential of p.
func (s spec) isSecret(key string) bool {
	return slices.Contains(s.secrets, key) || s.generated[key] != nil
}

// accepts reports whether key is a setting or credential of p.
func (s spec) accepts(key string) bool {
	return slices.Contains(s.settings, key) || slices.Contains(s.secrets, key)
}

// normalize applies lago's attribute writers: NetSuite account IDs are
// lowercased, trimmed and dash-joined (netsuite_integration.rb:16-18).
// A false flag is stored as absent, as lago reads it with present?
// (stripe_provider.rb:50).
func normalize(p integration.Provider, key, value string) string {
	if p == integration.ProviderNetsuite && key == KeyAccountID {
		return strings.Join(strings.Fields(strings.ToLower(value)), "-")
	}
	if slices.Contains(specs[p].bools, key) && value == "false" {
		return ""
	}
	return value
}

// validURL reports whether s is an absolute http(s) URL with a host
// (lago UrlValidator#url_valid?).
func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// checkValues validates the given values of p.
func (s spec) checkValues(v *core.Validator, values map[string]string) {
	for _, k := range s.urls {
		if val, ok := values[k]; ok && val != "" {
			if v.MaxLen(k, val, 1024) {
				v.Check(validURL(val), k, CodeURLInvalid)
			}
		}
	}
	for _, k := range s.bools {
		if val, ok := values[k]; ok {
			v.Check(val == "" || val == "true", k, core.CodeInvalid)
		}
	}
	for k, n := range s.maxLen {
		if val, ok := values[k]; ok {
			v.MaxLen(k, val, n)
		}
	}
}
