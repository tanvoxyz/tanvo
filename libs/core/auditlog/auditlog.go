package auditlog

import (
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// SecurityLogsRetention is how long security logs are listed (lago
// Organization::SECURITY_LOGS_RETENTION_DAYS, app/models/organization.rb:174).
const SecurityLogsRetention = 90 * 24 * time.Hour

// SecurityLog is one security log entry (lago Clickhouse::SecurityLog).
type SecurityLog struct {
	// ID identifies the entry (lago log_id).
	ID uuid.UUID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// UserID is the acting or affected user; zero when none.
	UserID core.UserID
	// APIKeyID is the API key concerned; zero when none.
	APIKeyID core.APIKeyID
	// Type is the log type ("user", "api_key", "role", …).
	Type string
	// Event is the event name ("user.signed_up", …).
	Event string
	// Resources describes the affected objects.
	Resources map[string]any
	// DeviceInfo describes the client device, when known.
	DeviceInfo map[string]any
	// LoggedAt is the instant of the event.
	LoggedAt time.Time
	// CreatedAt is the instant the entry was stored.
	CreatedAt time.Time
}

// APILog is one API request log entry (lago Clickhouse::ApiLog): a write
// request made with an API key.
type APILog struct {
	// ID is the request ID (lago request_id).
	ID uuid.UUID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// APIKeyID is the API key of the request.
	APIKeyID core.APIKeyID
	// Client is the user agent.
	Client string
	// HTTPMethod is the lower-case HTTP method ("post", …).
	HTTPMethod string
	// HTTPStatus is the response status code.
	HTTPStatus int
	// RequestOrigin is the request URL origin.
	RequestOrigin string
	// RequestPath is the request path.
	RequestPath string
	// RequestBody is the JSON request body; nil when none.
	RequestBody map[string]any
	// Response is the JSON response body; nil when none.
	Response map[string]any
	// APIVersion is the API version ("v1").
	APIVersion string
	// LoggedAt is the instant of the request.
	LoggedAt time.Time
	// CreatedAt is the instant the entry was stored.
	CreatedAt time.Time
}

// ActivitySource is where an activity originated.
type ActivitySource string

// ActivitySource values (lago Clickhouse::ActivityLog activity_source).
const (
	// ActivitySourceAPI is a request made with an API key.
	ActivitySourceAPI ActivitySource = "api"
	// ActivitySourceFront is a dashboard request made by a user.
	ActivitySourceFront ActivitySource = "front"
	// ActivitySourceSystem is a background job.
	ActivitySourceSystem ActivitySource = "system"
)

// Valid reports whether s is a declared source.
func (s ActivitySource) Valid() bool {
	return s == ActivitySourceAPI || s == ActivitySourceFront || s == ActivitySourceSystem
}

// ActivityLog is one resource change (lago Clickhouse::ActivityLog).
type ActivityLog struct {
	// ID identifies the entry (lago activity_id).
	ID uuid.UUID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// UserID is the acting user; zero when none.
	UserID core.UserID
	// APIKeyID is the acting API key; zero when none.
	APIKeyID core.APIKeyID
	// Type is the activity type ("customer.created", …).
	Type string
	// Source is where the activity originated.
	Source ActivitySource
	// Object is the resource after the change.
	Object map[string]any
	// ObjectChanges maps each changed attribute to its old and new value.
	ObjectChanges map[string]any
	// ResourceID is the changed resource.
	ResourceID uuid.UUID
	// ResourceType is the changed resource's type ("customer", …).
	ResourceType string
	// ExternalCustomerID is the external ID of the related customer, if any.
	ExternalCustomerID string
	// ExternalSubscriptionID is the external ID of the related
	// subscription, if any.
	ExternalSubscriptionID string
	// LoggedAt is the instant of the change.
	LoggedAt time.Time
	// CreatedAt is the instant the entry was stored.
	CreatedAt time.Time
}

// Range bounds LoggedAt; nil bounds are open, both are inclusive.
type Range struct {
	// From is the lower bound.
	From *time.Time
	// To is the upper bound.
	To *time.Time
}

// Contains reports whether t is within r.
func (r Range) Contains(t time.Time) bool {
	return (r.From == nil || !t.Before(*r.From)) && (r.To == nil || !t.After(*r.To))
}

// SecurityLogFilter narrows a security log list; empty fields do not
// filter, several values match any (lago SecurityLogsQuery).
type SecurityLogFilter struct {
	// LoggedAt bounds the log instant.
	LoggedAt Range
	// UserIDs keeps the entries of these users.
	UserIDs []core.UserID
	// APIKeyIDs keeps the entries of these API keys.
	APIKeyIDs []core.APIKeyID
	// Types keeps these log types.
	Types []string
	// Events keeps these log events.
	Events []string
}

// APILogFilter narrows an API log list (lago ApiLogsQuery).
type APILogFilter struct {
	// LoggedAt bounds the request instant.
	LoggedAt Range
	// IDs keeps these request IDs.
	IDs []uuid.UUID
	// APIKeyIDs keeps the requests of these API keys.
	APIKeyIDs []core.APIKeyID
	// HTTPMethods keeps these lower-case methods.
	HTTPMethods []string
	// HTTPStatuses keeps these status codes.
	HTTPStatuses []int
	// Succeeded keeps statuses up to 399; with Failed, every status.
	Succeeded bool
	// Failed keeps statuses from 400.
	Failed bool
	// APIVersion keeps this version.
	APIVersion string
	// RequestPaths keeps paths matching any pattern, where "*" matches any
	// run of characters.
	RequestPaths []string
	// Clients keeps these user agents.
	Clients []string
}

// ActivityLogFilter narrows an activity log list (lago ActivityLogsQuery).
type ActivityLogFilter struct {
	// LoggedAt bounds the change instant.
	LoggedAt Range
	// IDs keeps these activity IDs.
	IDs []uuid.UUID
	// Types keeps these activity types.
	Types []string
	// Sources keeps these sources.
	Sources []ActivitySource
	// UserIDs keeps the activities of these users.
	UserIDs []core.UserID
	// APIKeyIDs keeps the activities of these API keys.
	APIKeyIDs []core.APIKeyID
	// ResourceIDs keeps the activities of these resources.
	ResourceIDs []uuid.UUID
	// ResourceTypes keeps these resource types.
	ResourceTypes []string
	// ExternalCustomerID keeps the activities of this customer.
	ExternalCustomerID string
	// ExternalSubscriptionID keeps the activities of this subscription.
	ExternalSubscriptionID string
}
