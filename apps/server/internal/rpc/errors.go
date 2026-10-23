package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// FieldViolation is one invalid field of a request.
type FieldViolation struct {
	// Field is the snake_case path ("events[2].code").
	Field string
	// Reason is the stable code ("value_is_mandatory").
	Reason string
	// Description is a human-readable explanation; Reason when empty.
	Description string
}

// InvalidArgument returns an InvalidArgument error with a
// google.rpc.BadRequest detail listing violations.
func InvalidArgument(msg string, violations []FieldViolation) *connect.Error {
	e := connect.NewError(connect.CodeInvalidArgument, msg)
	if len(violations) == 0 {
		return e
	}
	br := &errdetails.BadRequest{}
	for _, v := range violations {
		desc := v.Description
		if desc == "" {
			desc = v.Reason
		}
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{Field: v.Field, Reason: v.Reason, Description: desc})
	}
	if d, err := connectproto.NewErrorDetail(br); err == nil {
		e = e.WithDetail(d)
	}
	return e
}

// ErrorDomain is the domain of the google.rpc.ErrorInfo details.
const ErrorDomain = "tanvo.xyz"

// Reasons of the ErrorInfo details that are not a resource or rule code.
const (
	// ReasonValidationErrors: the request has invalid fields, listed in a
	// BadRequest detail (lago "validation_errors").
	ReasonValidationErrors = "validation_errors"
	// ReasonNotFound: a resource that names no resource is missing.
	ReasonNotFound = "not_found"
	// ReasonUnauthenticated: the credential is missing or invalid.
	ReasonUnauthenticated = "unauthenticated"
	// ReasonForbidden: the principal lacks the permission.
	ReasonForbidden = "forbidden"
	// ReasonInvalidValue: an argument, such as a page token, is invalid.
	ReasonInvalidValue = "invalid_value"
	// ReasonInvalidTransition: the resource's status forbids the change.
	ReasonInvalidTransition = "invalid_transition"
	// ReasonConflict: the write conflicts with stored data.
	ReasonConflict = "conflict"
)

// withReason adds a google.rpc.ErrorInfo detail with the stable reason.
func withReason(e *connect.Error, reason string) *connect.Error {
	if d, err := connectproto.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain}); err == nil {
		e = e.WithDetail(d)
	}
	return e
}

// newError returns a Connect error with code, message and an ErrorInfo
// of reason.
func newError(code connect.Code, reason, msg string) *connect.Error {
	return withReason(connect.NewError(code, msg), reason)
}

// validationViolations returns the field errors of a core validation
// error, with prefix before each field path.
func validationViolations(prefix string, err error) ([]FieldViolation, bool) {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return nil, false
	}
	out := make([]FieldViolation, len(ve.Errors))
	for i, fe := range ve.Errors {
		out[i] = FieldViolation{Field: prefix + fe.Field, Reason: fe.Code}
	}
	return out, true
}

// Error maps a service error to a Connect error carrying a
// google.rpc.ErrorInfo whose reason is the stable code (architecture
// §4.3):
//
//   - *core.ValidationError: InvalidArgument, reason validation_errors,
//     with a BadRequest listing each field and its code;
//   - *core.NotFoundError and *auth.NotFoundError: NotFound, reason
//     "<resource>_not_found"; other core.ErrNotFound: reason not_found;
//   - *core.RuleError: FailedPrecondition, reason its code (lago's 405
//     method_not_allowed failures; the gRPC status model has no 405, and
//     validation failures are likewise InvalidArgument, HTTP 400, rather
//     than lago's 422);
//   - auth.ErrUnauthenticated: Unauthenticated; auth.ErrPermissionDenied:
//     PermissionDenied; core.ErrInvalidValue: InvalidArgument;
//     core.ErrInvalidTransition: FailedPrecondition; core.ErrConflict:
//     AlreadyExists; context errors: Canceled and DeadlineExceeded;
//   - anything else: Internal, logged here and not shown to the caller.
//
// A *connect.Error is returned as it is.
func Error(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	if v, ok := validationViolations("", err); ok {
		return withReason(InvalidArgument("invalid request", v), ReasonValidationErrors)
	}
	if nf, ok := errors.AsType[*core.NotFoundError](err); ok {
		return newError(connect.CodeNotFound, nf.Error(), nf.Error())
	}
	if nf, ok := errors.AsType[*auth.NotFoundError](err); ok {
		return newError(connect.CodeNotFound, nf.Error(), nf.Error())
	}
	if re, ok := errors.AsType[*core.RuleError](err); ok {
		return newError(connect.CodeFailedPrecondition, re.Code, re.Error())
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "deadline exceeded")
	case errors.Is(err, auth.ErrUnauthenticated):
		return newError(connect.CodeUnauthenticated, ReasonUnauthenticated, "unauthenticated")
	case errors.Is(err, auth.ErrPermissionDenied):
		return newError(connect.CodePermissionDenied, ReasonForbidden, "permission denied")
	case errors.Is(err, core.ErrNotAllowed):
		return newError(connect.CodeFailedPrecondition, ReasonForbidden, "not allowed")
	case errors.Is(err, core.ErrInvalidValue):
		return newError(connect.CodeInvalidArgument, ReasonInvalidValue, err.Error())
	case errors.Is(err, core.ErrNotFound):
		return newError(connect.CodeNotFound, ReasonNotFound, "not found")
	case errors.Is(err, core.ErrInvalidTransition):
		return newError(connect.CodeFailedPrecondition, ReasonInvalidTransition, err.Error())
	case errors.Is(err, core.ErrConflict):
		return newError(connect.CodeAlreadyExists, ReasonConflict, "conflict")
	}
	slog.ErrorContext(ctx, "rpc: internal error", "error", err)
	return connect.NewError(connect.CodeInternal, "internal error")
}

// ErrorReason returns the reason of the google.rpc.ErrorInfo detail of
// err, or "".
func ErrorReason(err error) string {
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		return ""
	}
	for _, d := range ce.Details() {
		v, err := connectproto.UnmarshalErrorDetail(d)
		if err != nil {
			continue
		}
		if info, ok := v.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// ErrorViolations returns the field violations of the google.rpc.BadRequest
// detail of err.
func ErrorViolations(err error) []FieldViolation {
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		return nil
	}
	var out []FieldViolation
	for _, d := range ce.Details() {
		v, err := connectproto.UnmarshalErrorDetail(d)
		if err != nil {
			continue
		}
		if br, ok := v.(*errdetails.BadRequest); ok {
			for _, fv := range br.GetFieldViolations() {
				out = append(out, FieldViolation{Field: fv.GetField(), Reason: fv.GetReason(), Description: fv.GetDescription()})
			}
		}
	}
	return out
}
