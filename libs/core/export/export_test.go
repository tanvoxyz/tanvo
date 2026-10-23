package export

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

var testNow = time.Date(2024, 3, 1, 9, 30, 15, 0, time.UTC)

var requester = core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-000000000001")

func newTestExport() *DataExport {
	return New(core.MustParseID[core.DataExportID]("0190a0a0-0000-7000-8000-000000000002"), core.OrganizationID{}, requester,
		ResourceInvoices, json.RawMessage(`{"status":"finalized"}`), testNow)
}

func TestValidateAndExpired(t *testing.T) {
	type tc struct {
		Name           string   `yaml:"name"`
		Source         string   `yaml:"source"`
		Status         string   `yaml:"status"`
		File           bool     `yaml:"file"`
		ExpiresInHours *int     `yaml:"expires_in_hours"`
		Resource       *string  `yaml:"resource"`
		Query          *string  `yaml:"query"`
		Errors         []string `yaml:"errors"`
		Expired        bool     `yaml:"expired"`
	}
	for _, c := range spectest.Load[tc](t, "data_export.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			e := newTestExport()
			e.Status = Status(c.Status)
			if c.File {
				e.File = "exports/1.csv"
			}
			if c.ExpiresInHours != nil {
				exp := testNow.Add(time.Duration(*c.ExpiresInHours) * time.Hour)
				e.ExpiresAt = &exp
			}
			if c.Resource != nil {
				e.ResourceType = Resource(*c.Resource)
			}
			if c.Query != nil {
				e.ResourceQuery = json.RawMessage(*c.Query)
			}
			got := []string{}
			for _, fe := range core.FieldErrors(e.Validate()) {
				got = append(got, fe.String())
			}
			if !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
			if got := e.Expired(testNow); got != c.Expired {
				t.Errorf("Expired() = %v, want = %v", got, c.Expired)
			}
		})
	}
}

func TestTransitionMatrix(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusPending, StatusProcessing}: true, {StatusPending, StatusFailed}: true,
		{StatusProcessing, StatusCompleted}: true, {StatusProcessing, StatusFailed}: true,
	}
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			if got := CanTransition(from, to); got != allowed[[2]Status{from, to}] {
				t.Errorf("CanTransition(%s, %s) = %v, want = %v", from, to, got, !got)
			}
		}
	}
}

// source: spec/models/data_export_spec.rb:47 and :61.
func TestLifecycle(t *testing.T) {
	e := newTestExport()
	if err := e.Complete("f.csv", testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Complete() while pending = %v, want = ErrInvalidTransition", err)
	}
	if err := e.Start(testNow); err != nil || e.Status != StatusProcessing || !e.StartedAt.Equal(testNow) {
		t.Fatalf("Start() = %v, status = %s, started_at = %v", err, e.Status, e.StartedAt)
	}
	if err := e.Complete("", testNow); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("Complete() without file = %v, want = ErrNotAllowed", err)
	}
	later := testNow.Add(time.Minute)
	if err := e.Complete("f.csv", later); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	if e.Status != StatusCompleted || !e.CompletedAt.Equal(later) || !e.ExpiresAt.Equal(later.Add(7*24*time.Hour)) {
		t.Errorf("status = %s, completed_at = %v, expires_at = %v, want = completed, %v, +7 days", e.Status, e.CompletedAt, e.ExpiresAt, later)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v, want = nil", err)
	}
	if err := e.Fail(later); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Fail() when completed = %v, want = ErrInvalidTransition", err)
	}
}

// source: spec/models/data_export_spec.rb:140.
func TestFilename(t *testing.T) {
	if got, want := newTestExport().Filename(), "20240301093015_invoices.csv"; got != want {
		t.Errorf("Filename() = %q, want = %q", got, want)
	}
}

func TestPart(t *testing.T) {
	p := &Part{DataExportID: newTestExport().ID, Index: 0, ObjectIDs: []uuid.UUID{uuid.New()}}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want = nil", err)
	}
	p.Finish("a,b\n", testNow)
	if !p.Completed || p.CSVLines != "a,b\n" {
		t.Errorf("Finish(): completed = %v, lines = %q", p.Completed, p.CSVLines)
	}
	if err := (&Part{Index: -1}).Validate(); len(core.FieldErrors(err)) != 3 {
		t.Errorf("Validate() of an empty part = %v, want = 3 errors", err)
	}
}
