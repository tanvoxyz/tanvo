package auth

import (
	"errors"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Sentinel errors of authentication and authorization.
var (
	// ErrUnauthenticated is returned when a credential is missing, unknown,
	// expired or revoked. It never says which.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrPermissionDenied is returned when an authenticated principal lacks
	// the permission an operation needs.
	ErrPermissionDenied = errors.New("permission denied")
)

// Error codes returned by the services, as [core.RuleError] codes or
// [core.FieldError] codes. They follow lago's codes unless noted.
const (
	// CodeIncorrectLoginOrPassword: unknown email, wrong password or no
	// active membership.
	CodeIncorrectLoginOrPassword = "incorrect_login_or_password"
	// CodeLoginMethodNotAuthorized: no organization of the user allows the
	// sign-in method (field: the method).
	CodeLoginMethodNotAuthorized = "login_method_not_authorized"
	// CodeUserAlreadyExists: registration with an email already used
	// (field: email).
	CodeUserAlreadyExists = "user_already_exists"
	// CodeUserDoesNotExist: Google sign-in of an email without an active
	// membership.
	CodeUserDoesNotExist = "user_does_not_exist"
	// CodeSignupDisabled: self sign-up is turned off.
	CodeSignupDisabled = "signup_disabled"
	// CodeCannotGrantAdmin: only admins may invite or promote to admin.
	CodeCannotGrantAdmin = "cannot_grant_admin"
	// CodeInviteAlreadyExists: a pending invite exists for the email
	// (field: invite).
	CodeInviteAlreadyExists = "invite_already_exists"
	// CodeInvalidRole: no role is given, or one does not exist in the
	// organization (field: roles).
	CodeInvalidRole = "invalid_role"
	// CodeCannotUpdateAcceptedInvite: an accepted invite is immutable.
	CodeCannotUpdateAcceptedInvite = "cannot_update_accepted_invite"
	// CodeCannotUpdateRevokedInvite: a revoked invite is immutable.
	CodeCannotUpdateRevokedInvite = "cannot_update_revoked_invite"
	// CodeInviteEmailMismatch: the identity's email is not the invited one.
	// Lago's Google flow spells it "invite_email_mistmatch" (parity §4).
	CodeInviteEmailMismatch = "invite_email_mismatch"
	// CodeMissingPassword: password reset without a new password (field:
	// new_password).
	CodeMissingPassword = "missing_password"
	// CodeMissingToken: password reset without a token (field: token).
	CodeMissingToken = "missing_token"
	// CodeStateNotFound: the OIDC state is missing, unknown, used or
	// expired.
	CodeStateNotFound = "state_not_found"
	// CodeCodeNotFound: the OIDC callback has no authorization code.
	CodeCodeNotFound = "code_not_found"
	// CodeDomainNotConfigured: no connection claims the email's domain.
	CodeDomainNotConfigured = "domain_not_configured"
	// CodeOIDCMissingSetup: the provider is not configured (lago
	// "google_auth_missing_setup").
	CodeOIDCMissingSetup = "oidc_missing_setup"
	// CodeOIDCRequestError: the provider could not be reached or refused
	// the code (lago "okta_request_error", "invalid_google_code").
	CodeOIDCRequestError = "oidc_request_error"
	// CodeOIDCInvalidToken: the ID token failed verification (lago
	// "invalid_google_token").
	CodeOIDCInvalidToken = "oidc_invalid_token"
	// CodeOIDCEmailNotVerified: the provider says the email is not
	// verified. New in Tanvo.
	CodeOIDCEmailNotVerified = "oidc_email_not_verified"
	// CodeMembershipRevoked: SSO sign-in of a user whose membership in the
	// connection's organization was revoked.
	CodeMembershipRevoked = "membership_revoked"
	// CodeEmailAlreadyUsed: the email already has an active membership
	// (field: email).
	CodeEmailAlreadyUsed = "email_already_used"
)

// UserinfoErrorCode returns lago's code for an identity whose email is not
// the one typed at the start of the flow: "okta_userinfo_error",
// "entra_id_userinfo_error", or "<method>_userinfo_error".
func UserinfoErrorCode(provider string) string { return provider + "_userinfo_error" }

// NotFoundError reports a missing resource with lago's error code
// "<resource>_not_found". It matches [core.ErrNotFound].
type NotFoundError struct {
	// Resource names the missing resource ("invite", "api_key", …).
	Resource string
}

// Error implements error.
func (e *NotFoundError) Error() string { return e.Resource + "_not_found" }

// Is reports whether target is [core.ErrNotFound].
func (e *NotFoundError) Is(target error) bool { return target == core.ErrNotFound }

func notFound(resource string) error { return &NotFoundError{Resource: resource} }

func rule(code string) error { return core.NewRuleError(code, "") }

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

// ErrorCode returns the stable code carried by err: a rule code, the first
// field error code, or "<resource>_not_found"; "" otherwise.
func ErrorCode(err error) string {
	if re, ok := errors.AsType[*core.RuleError](err); ok {
		return re.Code
	}
	if nf, ok := errors.AsType[*NotFoundError](err); ok {
		return nf.Error()
	}
	if fe := core.FieldErrors(err); len(fe) > 0 {
		return fe[0].Code
	}
	return ""
}
