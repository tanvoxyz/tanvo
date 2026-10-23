package auth

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Security log types (lago Utils::SecurityLog log_type).
const (
	// SecurityLogUser covers sign-ins, sign-ups, invites and passwords.
	SecurityLogUser = "user"
	// SecurityLogAPIKey covers API key changes.
	SecurityLogAPIKey = "api_key"
	// SecurityLogRole covers custom role changes.
	SecurityLogRole = "role"
)

// Security log events (lago log_event).
const (
	// EventUserSignedUp: a user registered and created an organization.
	EventUserSignedUp = "user.signed_up"
	// EventUserSignedIn: a user opened a session. New in Tanvo (lago logs
	// new devices only).
	EventUserSignedIn = "user.signed_in"
	// EventNewDeviceLoggedIn: a user signed in from a device never seen
	// before; the resources describe the client.
	EventNewDeviceLoggedIn = "user.new_device_logged_in"
	// EventUserInvited: an invite was created.
	EventUserInvited = "user.invited"
	// EventUserJoined: an invite was accepted.
	EventUserJoined = "user.joined"
	// EventUserRoleEdited: a membership's roles changed; the resources
	// list the added and deleted role codes.
	EventUserRoleEdited = "user.role_edited"
	// EventUserDeleted: a membership was revoked.
	EventUserDeleted = "user.deleted"
	// EventPasswordResetRequested: a password reset was requested.
	EventPasswordResetRequested = "user.password_reset_requested"
	// EventPasswordEdited: a password was changed by reset.
	EventPasswordEdited = "user.password_edited"
	// EventAPIKeyCreated: an API key was created.
	EventAPIKeyCreated = "api_key.created"
	// EventAPIKeyUpdated: an API key's name or permissions changed.
	EventAPIKeyUpdated = "api_key.updated"
	// EventAPIKeyRotated: an API key was rotated.
	EventAPIKeyRotated = "api_key.rotated"
	// EventAPIKeyDeleted: an API key was destroyed.
	EventAPIKeyDeleted = "api_key.deleted"
	// EventRoleDeleted: a custom role was discarded; the resources hold
	// its code.
	EventRoleDeleted = "role.deleted"
)

// SecurityEvent is one security log entry (parity §1.45).
type SecurityEvent struct {
	// OrganizationID is the organization the event belongs to.
	OrganizationID core.OrganizationID
	// UserID is the acting or affected user, if any.
	UserID core.UserID
	// Type is the log type ([SecurityLogUser], [SecurityLogAPIKey],
	// [SecurityLogRole]).
	Type string
	// Event is the event name ("user.signed_up", …).
	Event string
	// Resources describes the affected objects; never secrets, only the
	// last characters of keys.
	Resources map[string]any
	// At is the instant of the event; zero means now.
	At time.Time
}

// SecurityLogger records security events; apps/server wires the log store.
type SecurityLogger interface {
	// Record stores e.
	Record(ctx context.Context, e SecurityEvent) error
}
