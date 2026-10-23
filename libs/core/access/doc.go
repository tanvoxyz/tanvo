// Package access holds who may act in an organization (domain §5): users
// ([User]), their memberships ([Membership]) with a [Role], invites
// ([Invite]), password resets ([PasswordReset]) and API keys ([APIKey]).
//
// The rules that need counts of other rows (the last admin, the last
// non-expiring key, an email already member) are pure functions taking
// those counts: [CheckRevoke], [CheckRoleChange], [CheckRoleDeletion],
// [CheckInvite], [CheckAPIKeyDelete] and [CheckAPIKeyExpiry].
// Permissions come from package permission.
package access
