// Package auth authenticates and authorizes Tanvo principals (architecture
// §4.4): API keys, user sessions opened by email and password or by OpenID
// Connect (Google, Okta, Entra ID and generic providers), invitations,
// password resets and customer portal tokens.
//
// Services take their dependencies through constructors: a
// [port.Transactor] for the access repositories of libs/core, a
// [core.Clock], a [core.IDGenerator] and a source of randomness. Emails are
// not sent here: the services enqueue mail jobs ([JobKindMail]) in the same
// transaction, and libs/mail renders them. Security-relevant actions are
// reported to a [SecurityLogger].
//
// The Connect interceptor that turns credentials into a [Principal] and
// checks the permission each RPC declares lives in apps/server; it uses
// [APIKeys.Authenticate], [Sessions.Authenticate], [PortalTokens.Verify] and
// [Authorizer].
package auth
