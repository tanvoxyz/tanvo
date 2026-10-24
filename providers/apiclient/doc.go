// Package apiclient is the HTTP client shared by the tax, accounting and
// CRM adapters (providers/anrok, avalara, netsuite, xero, hubspot,
// salesforce; architecture §12). It carries what every adapter needs and
// none of their wire formats: OpenTelemetry instrumentation through the
// injected providers (a client span per operation with the organization
// and integration IDs, the otelhttp transport below it), a token-bucket
// rate limit per connection or per credential, response capture with a
// bounded body, and the default classification of HTTP failures into
// [integration.ProviderError] kinds.
package apiclient
