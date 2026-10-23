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

// Error maps a service error to a Connect error: validation errors to
// InvalidArgument with their fields, the core sentinel errors to their
// codes, and anything else to Internal, logged here and not shown to the
// caller.
func Error(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	if v, ok := validationViolations("", err); ok {
		return InvalidArgument("invalid request", v)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, "canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, "deadline exceeded")
	case errors.Is(err, auth.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, "unauthenticated")
	case errors.Is(err, auth.ErrPermissionDenied), errors.Is(err, core.ErrNotAllowed):
		return connect.NewError(connect.CodePermissionDenied, "permission denied")
	case errors.Is(err, core.ErrInvalidValue):
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	case errors.Is(err, core.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "not found")
	case errors.Is(err, core.ErrInvalidTransition):
		return connect.NewError(connect.CodeFailedPrecondition, err.Error())
	case errors.Is(err, core.ErrConflict):
		return connect.NewError(connect.CodeAlreadyExists, "conflict")
	}
	slog.ErrorContext(ctx, "rpc: internal error", "error", err)
	return connect.NewError(connect.CodeInternal, "internal error")
}
