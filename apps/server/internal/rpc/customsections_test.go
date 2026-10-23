package rpc_test

import (
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// restCustomSection is the JSON of an invoice custom section in a REST
// response.
type restCustomSection struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Details     string `json:"details"`
	Description string `json:"description"`
	SectionType string `json:"section_type"`
}

func icsDecode(t *testing.T, body []byte) restCustomSection {
	t.Helper()
	return decode[struct {
		Section restCustomSection `json:"invoice_custom_section"`
	}](t, body).Section
}

func icsCreate(t *testing.T, s *rpctest.Server, code, name string) restCustomSection {
	t.Helper()
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/invoice_custom_sections",
		map[string]any{
			"code": code, "name": name, "display_name": "Displayed " + name, "details": "Details of " + name,
			"description": "this invoice custom section will be added in the PDF",
		})
	if status != http.StatusOK {
		t.Fatalf("create %s: got status = %d (%s), want = 200", code, status, body)
	}
	return icsDecode(t, body)
}

func TestCreateInvoiceCustomSection(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)

	// source: spec/graphql/mutations/invoice_custom_sections/create_spec.rb:28
	got := icsCreate(t, s, "section_code", "First Invoice Custom Section")
	if got.ID == "" || got.Code != "section_code" || got.Name != "First Invoice Custom Section" ||
		got.DisplayName != "Displayed First Invoice Custom Section" || got.Details != "Details of First Invoice Custom Section" ||
		got.Description != "this invoice custom section will be added in the PDF" || got.SectionType != "INVOICE_CUSTOM_SECTION_TYPE_MANUAL" {
		t.Errorf("got %+v, want the created manual section", got)
	}

	tests := []struct {
		name   string
		source string
		body   string
		status int
	}{
		{"without code", "spec/graphql/mutations/invoice_custom_sections/create_spec.rb:56", `{"name":"No code"}`, http.StatusBadRequest},
		{"code taken", "app/models/invoice_custom_section.rb:26", `{"code":"section_code","name":"Again"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/invoice_custom_sections", tt.body)
			if status != tt.status {
				t.Errorf("status (%s): got = %d (%s), want = %d", tt.source, status, body, tt.status)
			}
		})
	}
}

func TestGetInvoiceCustomSection(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	sec := icsCreate(t, s, "sec", "Section")
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"by id", "spec/graphql/resolvers/invoice_custom_section_resolver_spec.rb:22", sec.ID, http.StatusOK},
		{"by code", "spec/graphql/resolvers/invoice_custom_section_resolver_spec.rb:22", "code%3Asec", http.StatusOK},
		{"not found", "spec/graphql/resolvers/invoice_custom_section_resolver_spec.rb:53", core.NewID[core.InvoiceCustomSectionID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/invoice_custom_sections/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("status (%s): got = %d (%s), want = %d", tt.source, status, body, tt.status)
			}
			if status == http.StatusOK && icsDecode(t, body).ID != sec.ID {
				t.Errorf("id: got = %s, want = %s", icsDecode(t, body).ID, sec.ID)
			}
		})
	}
}

func TestUpdateInvoiceCustomSection(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	sec := icsCreate(t, s, "sec", "Section")
	icsCreate(t, s, "other", "Other")

	// source: spec/graphql/mutations/invoice_custom_sections/update_spec.rb:34
	status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/invoice_custom_sections/"+sec.ID, map[string]any{
		"name": "First Invoice Custom Section", "details": "This is the exact information shown in the invoice",
		"display_name": "Section name displayed in the invoice", "description": "Updated description",
	})
	if status != http.StatusOK {
		t.Fatalf("update: got status = %d (%s), want = 200", status, body)
	}
	got := icsDecode(t, body)
	if got.Code != "sec" || got.Name != "First Invoice Custom Section" || got.DisplayName != "Section name displayed in the invoice" ||
		got.Details != "This is the exact information shown in the invoice" || got.Description != "Updated description" {
		t.Errorf("got %+v, want the updated section with its code", got)
	}

	// source: spec/graphql/mutations/invoice_custom_sections/update_spec.rb:67
	status, body = s.REST(t, s.APIKey, http.MethodPatch, "/v1/invoice_custom_sections/"+sec.ID, map[string]any{"code": "other"})
	if status != http.StatusBadRequest {
		t.Errorf("code taken: got status = %d (%s), want = 400", status, body)
	}
}

func TestDeleteInvoiceCustomSection(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	sec := icsCreate(t, s, "sec", "Section")

	// source: spec/graphql/mutations/invoice_custom_sections/destroy_spec.rb:35
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/invoice_custom_sections/"+sec.ID, nil); status != http.StatusOK {
		t.Fatalf("delete: got status = %d (%s), want = 200", status, body)
	}
	if status, _ := s.REST(t, s.APIKey, http.MethodGet, "/v1/invoice_custom_sections/"+sec.ID, nil); status != http.StatusNotFound {
		t.Errorf("get after delete: got status = %d, want = 404", status)
	}
	// source: spec/graphql/mutations/invoice_custom_sections/destroy_spec.rb:47
	if status, _ := s.REST(t, s.APIKey, http.MethodDelete, "/v1/invoice_custom_sections/"+sec.ID, nil); status != http.StatusNotFound {
		t.Errorf("delete again: got status = %d, want = 404", status)
	}
}

func TestListInvoiceCustomSections(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := icsCreate(t, s, "c", "c")
	a := icsCreate(t, s, "a", "a")
	x := icsCreate(t, s, "x", "x")

	// source: spec/graphql/resolvers/invoice_custom_sections_resolver_spec.rb:38
	var ids []string
	for token := ""; ; {
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/invoice_custom_sections?page_size=2&page_token="+token, nil)
		if status != http.StatusOK {
			t.Fatalf("list: got status = %d (%s), want = 200", status, body)
		}
		page := decode[struct {
			Sections []restCustomSection `json:"invoice_custom_sections"`
			Next     string              `json:"next_page_token"`
		}](t, body)
		for _, sec := range page.Sections {
			ids = append(ids, sec.ID)
		}
		if page.Next == "" {
			break
		}
		token = page.Next
	}
	if want := []string{a.ID, c.ID, x.ID}; len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Errorf("ids: got = %v, want = %v", ids, want)
	}

	for _, q := range []string{"filter=code%3D%22a%22", "search=a"} {
		if status, _ := s.REST(t, s.APIKey, http.MethodGet, "/v1/invoice_custom_sections?"+q, nil); status != http.StatusBadRequest {
			t.Errorf("%s: got status = %d, want = 400", q, status)
		}
	}
}
