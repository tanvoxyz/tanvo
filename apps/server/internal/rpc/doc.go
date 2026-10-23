// Package rpc holds the Connect side of the API role: the server
// interceptors and the service handlers, which map requests to the
// libraries' domain services (architecture §4.6).
//
// # Interceptors
//
// [Telemetry], [Recover], [Auth], [Validate] and [Idempotency] run in this
// order on every RPC. [Auth] reads the bearer credential (an API key, or a
// session with the [OrganizationHeader] of the organization it acts in, or
// a portal token) and enforces the tanvo.common.v1.access option of the
// procedure; the handlers read the result with [Principal].
//
// # Writing a handler
//
// A handler is thin: it reads the principal, converts the request, calls
// one service method, converts the result and maps errors with [Error].
// Business rules stay in the service. BillableMetrics (metrics.go) is the
// reference implementation, and docs/development.md §8 the full playbook.
//
//   - References: [Lookup] resolves an ID, "code:<code>" or
//     "external_id:<id>" ([ParseRef]); [ParseID] parses fields that take
//     only IDs.
//   - Lists: [Page] and [Items] for AIP-158 pagination; [ParseFilter],
//     [Equalities], [FilterEnum], [FilterBool] and [FilterSingle] for
//     AIP-160 filters.
//   - Updates: [UpdatePaths] gives the fields an update writes, from the
//     update_mask or the fields present.
//   - Values: [Opt], [OptInt], [Timestamp], [Time], [Date], [FromDate],
//     [Money], [FromMoney], [Decimal], [ParseDecimal], [Metadata], [Struct]
//     and [NewEnum].
//   - Errors: [Error] maps core and auth errors to Connect codes with an
//     ErrorInfo reason and BadRequest violations; [InvalidArgument] builds
//     a validation error; [ErrorReason] and [ErrorViolations] read them in
//     tests.
//
// Register the handler in apps/server/internal/app (api.go) and port the
// request spec with apps/server/internal/rpctest, over REST and Connect.
package rpc
