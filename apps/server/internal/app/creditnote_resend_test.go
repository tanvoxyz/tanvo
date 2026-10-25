package app

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// TestResendCreditNote ports the ResendCreditNoteEmail mutation through
// the document service behind CreditNoteService.ResendCreditNote.
func TestResendCreditNote(t *testing.T) {
	tests := []struct {
		name, source string
		to           rpc.Recipients
		missing      bool
		wantTo       []string
	}{
		{name: "resends the email", source: "spec/graphql/mutations/credit_notes/resend_email_spec.rb:35", wantTo: []string{"ap@customer.test"}},
		{
			name: "custom recipients", source: "spec/graphql/mutations/credit_notes/resend_email_spec.rb:50",
			to:     rpc.Recipients{To: []string{"custom@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"}},
			wantTo: []string{"custom@example.com"},
		},
		{name: "not found", source: "spec/graphql/mutations/credit_notes/resend_email_spec.rb:73", missing: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			e := newDocsEnv(t, 1000)
			id := e.StoredCreditNote(e.inv, 10).ID
			if tt.missing {
				id = core.NewID[core.CreditNoteID](e.IDs)
			}
			err := e.w.ResendCreditNote(e.Ctx, e.OrgID(), id, tt.to)
			if tt.missing {
				if !errors.Is(err, core.ErrNotFound) {
					t.Errorf("got = %v, want = not found", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var jobs []kernel.DocumentEmailArgs
			for _, j := range e.Store.Jobs() {
				if j.Kind != kernel.JobSendDocumentEmail {
					continue
				}
				var a kernel.DocumentEmailArgs
				if err := json.Unmarshal(j.Args, &a); err != nil {
					t.Fatal(err)
				}
				jobs = append(jobs, a)
			}
			if len(jobs) != 1 {
				t.Fatalf("got = %d email jobs, want = 1", len(jobs))
			}
			a := jobs[0]
			if a.Type != kernel.DocumentCreditNote || a.ID != id.UUID() || !a.Resend || !slices.Equal(a.To, tt.wantTo) ||
				!slices.Equal(a.Cc, tt.to.Cc) || !slices.Equal(a.Bcc, tt.to.Bcc) {
				t.Errorf("got = %+v, want a resend of credit note %s to %v", a, id, tt.wantTo)
			}
		})
	}
}
