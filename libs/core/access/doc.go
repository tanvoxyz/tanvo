// Package access holds who may act in an organization (domain §5): users
// ([User]), their memberships ([Membership]) with one or more [Role]s, invites
// ([Invite]), password resets ([PasswordReset]), API keys ([APIKey]),
// sessions ([Session]), OpenID Connect sign-in states ([OIDCState]) and
// known devices ([UserDevice]).
//
// The rules that need counts of other rows (the last admin, the last
// non-expiring key, an email already member) are pure functions taking
// those counts: [CheckRevoke], [CheckRoleChange], [CheckRoleDeletion],
// [CheckInvite], [CheckAPIKeyDelete] and [CheckAPIKeyExpiry].
// Permissions come from package permission; a membership holds the union
// of its roles' permissions ([AnyGrants]).
package access
