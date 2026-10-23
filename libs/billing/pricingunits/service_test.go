package pricingunits_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/pricingunits"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *pricingunits.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := pricingunits.New(h.Deps)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

func create(t *testing.T, h *billingtest.Harness, s *pricingunits.Service, code, name string) *catalog.PricingUnit {
	t.Helper()
	p, err := s.Create(t.Context(), h.OrgID(), pricingunits.CreateInput{Code: code, Name: name, ShortName: "CT"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return p
}

func count(h *billingtest.Harness) int {
	return len(billingtest.Read(h, func(ctx context.Context, r port.Repos) (core.Page[*catalog.PricingUnit], error) {
		return r.PricingUnits.List(ctx, h.OrgID(), catalog.PricingUnitFilter{}, core.PageRequest{})
	}).Items)
}

func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) || len(ve.Errors) != 1 {
		t.Fatalf("errors = %v, want only %s %s", ve.Errors, field, code)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/pricing_units/create_service_spec.rb:31
	// source: spec/services/pricing_units/create_service_spec.rb:39
	t.Run("creates a pricing unit", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		p, err := s.Create(t.Context(), h.OrgID(), pricingunits.CreateInput{
			Code: "tokens", Name: "Cloud tokens", ShortName: "CT", Description: ptr("description"),
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, err := s.Get(t.Context(), h.OrgID(), p.ID)
		if err != nil || got.Code != "tokens" || got.Name != "Cloud tokens" || got.ShortName != "CT" || *got.Description != "description" {
			t.Fatalf("stored = %+v, %v, want the created pricing unit", got, err)
		}
		if n := count(h); n != 1 {
			t.Errorf("pricing units = %d, want = 1", n)
		}
	})
	// source: spec/services/pricing_units/create_service_spec.rb:47
	// source: spec/services/pricing_units/create_service_spec.rb:52
	t.Run("a used code is value_already_exist", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		create(t, h, s, "credits", "Credits")
		_, err := s.Create(t.Context(), h.OrgID(), pricingunits.CreateInput{Code: "credits", Name: "Cloud tokens", ShortName: "CT"})
		wantFieldError(t, err, "code", core.CodeAlreadyExists)
		if n := count(h); n != 1 {
			t.Errorf("pricing units = %d, want = 1", n)
		}
	})
	t.Run("a short name over 3 characters is value_is_too_long", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Create(t.Context(), h.OrgID(), pricingunits.CreateInput{Code: "tokens", Name: "Tokens", ShortName: "TOKS"})
		wantFieldError(t, err, "short_name", core.CodeTooLong)
	})
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	// source: spec/services/pricing_units/update_service_spec.rb:28
	// source: spec/services/pricing_units/update_service_spec.rb:36
	t.Run("updates the pricing unit", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		p := create(t, h, s, "tokens", "Tokens")
		got, err := s.Update(t.Context(), h.OrgID(), p.ID, pricingunits.UpdateInput{
			Name: core.Some("Cloud tokens"), ShortName: core.Some("CTK"), Description: core.Some(ptr("description")),
		})
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Name != "Cloud tokens" || got.ShortName != "CTK" || *got.Description != "description" || got.Code != "tokens" {
			t.Errorf("pricing unit = %+v, want the updated fields", got)
		}
	})
	// source: spec/services/pricing_units/update_service_spec.rb:44
	// source: spec/services/pricing_units/update_service_spec.rb:49
	t.Run("a description over 600 characters is value_is_too_long", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		p := create(t, h, s, "tokens", "Tokens")
		_, err := s.Update(t.Context(), h.OrgID(), p.ID, pricingunits.UpdateInput{Description: core.Some(ptr(strings.Repeat("a", 601)))})
		wantFieldError(t, err, "description", core.CodeTooLong)
		got, _ := s.Get(t.Context(), h.OrgID(), p.ID)
		if got.Description != nil {
			t.Errorf("description = %q, want unchanged", *got.Description)
		}
	})
	// source: spec/services/pricing_units/update_service_spec.rb:61
	// source: spec/services/pricing_units/update_service_spec.rb:70
	t.Run("a missing pricing unit is pricing_unit_not_found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Update(t.Context(), h.OrgID(), core.NewID[core.PricingUnitID](h.IDs), pricingunits.UpdateInput{Name: core.Some("")})
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Resource != pricingunits.Resource {
			t.Fatalf("error = %v, want pricing_unit_not_found", err)
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()
	// source: app/queries/pricing_units_query.rb:6-30
	t.Run("lists by name, then most recent first, searched by name or code", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		b := create(t, h, s, "b_code", "B")
		a1 := create(t, h, s, "a1_code", "A")
		h.Clock.Advance(time.Minute)
		a2 := create(t, h, s, "a2_code", "A")
		list := func(search string, size int) []core.PricingUnitID {
			var out []core.PricingUnitID
			req := core.PageRequest{Size: size}
			for {
				p, err := s.List(t.Context(), h.OrgID(), search, req)
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				for _, x := range p.Items {
					out = append(out, x.ID)
				}
				if p.NextToken == "" {
					return out
				}
				req.Token = p.NextToken
			}
		}
		if got := list("", 1); !slices.Equal(got, []core.PricingUnitID{a2.ID, a1.ID, b.ID}) {
			t.Errorf("List() = %v, want = A (newer), A, B", got)
		}
		if got := list("B_C", 0); !slices.Equal(got, []core.PricingUnitID{b.ID}) {
			t.Errorf("List(B_C) = %v, want = B", got)
		}
	})
}
