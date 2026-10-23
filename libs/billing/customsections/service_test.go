package customsections_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customsections"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var now = billingtest.Date(2026, time.March, 10, 12)

func newService(t *testing.T) (*billingtest.Harness, *customsections.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, customsections.New(h.Deps)
}

func fieldCodes(err error) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		out = append(out, e.Field+":"+e.Code)
	}
	return out
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		source string
		in     customsections.CreateInput
		want   []string
	}{
		{
			name:   "valid params",
			source: "spec/services/invoice_custom_sections/create_service_spec.rb:22",
			in: customsections.CreateInput{
				Code: "test", Name: "my firsts section",
				Details: new("This text will be displayed in the invoice"), DisplayName: new("This will be the section title"),
				Description: new("this invoice custom section will be added in the PDF"),
			},
		},
		{
			name:   "invalid params",
			source: "spec/services/invoice_custom_sections/create_service_spec.rb:31",
			in:     customsections.CreateInput{Name: "section"},
			want:   []string{"code:value_is_mandatory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := newService(t)
			sec, err := svc.Create(ctx, h.OrgID(), tt.in)
			if got := fieldCodes(err); !slices.Equal(got, tt.want) {
				t.Fatalf("errors: got = %v (%v), want = %v", got, err, tt.want)
			}
			if tt.want != nil {
				return
			}
			got, err := svc.Get(ctx, h.OrgID(), sec.ID)
			if err != nil {
				t.Fatalf("get: got = %v, want = nil", err)
			}
			if got.Code != tt.in.Code || got.Name != tt.in.Name || *got.Details != *tt.in.Details ||
				*got.DisplayName != *tt.in.DisplayName || *got.Description != *tt.in.Description || got.SectionType != invoice.CustomSectionTypeManual {
				t.Errorf("got = %+v, want the created manual section", got)
			}
		})
	}

	t.Run("code taken", func(t *testing.T) {
		h, svc := newService(t)
		if _, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: "c", Name: "a"}); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: "c", Name: "b"})
		if got := fieldCodes(err); !slices.Equal(got, []string{"code:value_already_exist"}) {
			t.Errorf("got = %v, want = [code:value_already_exist]", got)
		}
	})
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		source string
		in     customsections.UpdateInput
		want   []string
	}{
		{"valid params", "spec/services/invoice_custom_sections/update_service_spec.rb:16", customsections.UpdateInput{Name: core.Some("Updated Name")}, nil},
		{"invalid params", "spec/services/invoice_custom_sections/update_service_spec.rb:27", customsections.UpdateInput{Name: core.Some("")}, []string{"name:value_is_mandatory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := newService(t)
			sec, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: "c", Name: "n"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := svc.Update(ctx, h.OrgID(), sec.ID, tt.in)
			if codes := fieldCodes(err); !slices.Equal(codes, tt.want) {
				t.Fatalf("errors: got = %v (%v), want = %v", codes, err, tt.want)
			}
			if tt.want == nil && got.Name != "Updated Name" {
				t.Errorf("name: got = %q, want = %q", got.Name, "Updated Name")
			}
		})
	}

	t.Run("not found", func(t *testing.T) {
		h, svc := newService(t)
		_, err := svc.Update(ctx, h.OrgID(), core.NewID[core.InvoiceCustomSectionID](h.IDs), customsections.UpdateInput{Name: core.Some("x")})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = not found", err)
		}
	})
}

func TestDelete(t *testing.T) {
	// source: spec/services/invoice_custom_sections/destroy_service_spec.rb:21
	ctx := context.Background()
	h, svc := newService(t)
	sec, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: "c", Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	del, err := svc.Delete(ctx, h.OrgID(), sec.ID)
	if err != nil {
		t.Fatalf("delete: got = %v, want = nil", err)
	}
	if del.DeletedAt == nil {
		t.Errorf("deleted_at: got = nil, want = set")
	}
	if _, err := svc.Get(ctx, h.OrgID(), sec.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get: got = %v, want = not found", err)
	}
	// Selections of the deleted section select nothing.
	h.Do(func(ctx context.Context, r port.Repos) error {
		got, err := r.InvoiceCustomSections.ListByIDs(ctx, h.OrgID(), []core.InvoiceCustomSectionID{sec.ID})
		if err != nil || len(got) != 0 {
			t.Errorf("selected: got = %v, %v, want none", got, err)
		}
		return nil
	})
	// source: spec/graphql/mutations/invoice_custom_sections/destroy_spec.rb:47
	if _, err := svc.Delete(ctx, h.OrgID(), sec.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("delete again: got = %v, want = not found", err)
	}
	// A discarded section frees its code (app/models/invoice_custom_section.rb:26-28).
	if _, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: "c", Name: "n"}); err != nil {
		t.Errorf("create with the deleted code: got = %v, want = nil", err)
	}
}

func TestList(t *testing.T) {
	// source: spec/graphql/resolvers/invoice_custom_sections_resolver_spec.rb:38
	ctx := context.Background()
	h, svc := newService(t)
	for _, n := range []string{"x", "a", "c"} {
		if _, err := svc.Create(ctx, h.OrgID(), customsections.CreateInput{Code: n, Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.InvoiceCustomSections.Create(ctx, &invoice.CustomSection{
			ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: "sys", Name: "not show",
			SectionType: invoice.CustomSectionTypeSystemGenerated, CreatedAt: now, UpdatedAt: now,
		})
	})
	var names []string
	page := core.PageRequest{Size: 2}
	for {
		p, err := svc.List(ctx, h.OrgID(), page)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range p.Items {
			names = append(names, s.Name)
		}
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	if !slices.Equal(names, []string{"a", "c", "x"}) {
		t.Errorf("got = %v, want = [a c x]", names)
	}
}
