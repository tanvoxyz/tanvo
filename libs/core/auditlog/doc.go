// Package auditlog defines the audit logs of an organization (parity
// §1.45, architecture §15): security logs of access changes, API logs of
// write requests made with API keys, and activity logs of resource
// changes. Logs are append-only and listed most recent first.
package auditlog
