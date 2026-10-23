package rpc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestError(t *testing.T) {
	tests := []struct {
		err  error
		want connect.Code
	}{
		{&core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: "value_is_mandatory"}}}, connect.CodeInvalidArgument},
		{context.Canceled, connect.CodeCanceled},
		{fmt.Errorf("x: %w", auth.ErrUnauthenticated), connect.CodeUnauthenticated},
		{auth.ErrPermissionDenied, connect.CodePermissionDenied},
		{core.ErrNotAllowed, connect.CodeFailedPrecondition},
		{core.ErrNotFound, connect.CodeNotFound},
		{core.ErrConflict, connect.CodeAlreadyExists},
		{core.ErrInvalidTransition, connect.CodeFailedPrecondition},
		{core.ErrInvalidValue, connect.CodeInvalidArgument},
		{errors.New("boom"), connect.CodeInternal},
	}
	for _, tt := range tests {
		if got := connect.CodeOf(Error(t.Context(), tt.err)); got != tt.want {
			t.Errorf("Error(%v) code got = %v, want = %v", tt.err, got, tt.want)
		}
	}
	if Error(t.Context(), nil) != nil {
		t.Error("Error(nil) got = an error, want = nil")
	}
}

func TestBearer(t *testing.T) {
	if _, ok := bearer(nil); ok {
		t.Error("bearer(nil) got = ok, want = missing")
	}
}

func TestSplitProcedure(t *testing.T) {
	s, m := splitProcedure("/tanvo.event.v1.EventService/IngestEvent")
	if s != "tanvo.event.v1.EventService" || m != "IngestEvent" {
		t.Errorf("splitProcedure() got = %s %s, want = tanvo.event.v1.EventService IngestEvent", s, m)
	}
}
