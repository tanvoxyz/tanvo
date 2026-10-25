package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/textproto"
	"os"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/docgen"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/mail"
)

// TestDocumentRetry checks how many times the document jobs run a failing
// call.
func TestDocumentRetry(t *testing.T) {
	refused := fmt.Errorf("send: %w", &textproto.Error{Code: 451, Msg: "busy"})
	rejected := fmt.Errorf("send: %w", &textproto.Error{Code: 550, Msg: "no such user"})
	timeout := fmt.Errorf("send: %w", os.ErrDeadlineExceeded)
	for _, tt := range []struct {
		source string
		rules  []jobs.RetryRule
		err    error
		want   int
	}{
		{"spec/jobs/invoices/generate_documents_job_spec.rb:21", renderRetry(), fmt.Errorf("typst: %w", io.ErrUnexpectedEOF), renderAttempts},
		{"spec/jobs/credit_notes/generate_documents_job_spec.rb:20", renderRetry(), errors.New("blob store unavailable"), renderAttempts},
		{"spec/jobs/payment_receipts/generate_documents_job_spec.rb:21", renderRetry(), timeout, renderAttempts},
		{"spec/jobs/invoices/generate_documents_job_spec.rb:21", renderRetry(), core.NotFound("invoice"), 1},
		{"spec/jobs/send_email_job_spec.rb:79", emailRetry(), fmt.Errorf("receipt: %w", mail.ErrFilesNotReady), 8},
		{"spec/jobs/send_email_job_spec.rb:125", emailRetry(), refused, 25},
		{"spec/jobs/send_email_job_spec.rb:125", emailRetry(), timeout, 6},
		{"spec/jobs/send_email_job_spec.rb:125", emailRetry(), io.EOF, 6},
		{"spec/jobs/send_email_job_spec.rb:125", emailRetry(), rejected, 1},
	} {
		t.Run(tt.err.Error(), func(t *testing.T) {
			t.Log("source:", tt.source)
			if got := attemptsOf(tt.rules, tt.err); got != tt.want {
				t.Errorf("attempts: got = %d, want = %d", got, tt.want)
			}
		})
	}
}

// sender records the messages and fails with err.
type sender struct {
	sent []*mail.Message
	err  error
}

func (s *sender) Send(_ context.Context, m *mail.Message) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, m)
	return nil
}

type docsEnv struct {
	*billingtest.Harness
	w      *docs
	sender *sender
	inv    *invoice.Invoice
}

func newDocsEnv(t *testing.T, fees int64) *docsEnv {
	t.Helper()
	h := billingtest.New(t, billingtest.Date(2025, 3, 10))
	h.LogActivities(auditlog.Actor{})
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.Email = new("billing@org.test") })
	gen, err := docgen.New(h.Deps, nil, nil, docgen.Options{})
	if err != nil {
		t.Fatal(err)
	}
	composer, err := mail.NewComposer(mail.Config{DefaultFrom: "noreply@tanvo.test", AppURL: "https://app.tanvo.test", DisablePDFs: true})
	if err != nil {
		t.Fatal(err)
	}
	s := &sender{}
	c := h.Customer(func(c *customer.Customer) { c.Email = new("ap@customer.test") })
	inv := h.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.FeesAmount, inv.TotalAmount = invoice.StatusFinalized, fees, fees
	})
	w := &docs{gen: gen, d: h.Deps, composer: composer, sender: s, log: slog.New(slog.DiscardHandler)}
	return &docsEnv{Harness: h, w: w, sender: s, inv: inv}
}

func (e *docsEnv) run(attempt int, resend bool) error {
	return e.w.sendEmail(e.Ctx, &jobs.Job[kernel.DocumentEmailArgs]{
		Kind: kernel.JobSendDocumentEmail, OrganizationID: e.OrgID(), Attempt: attempt,
		Args: kernel.DocumentEmailArgs{Type: kernel.DocumentInvoice, ID: e.inv.ID.UUID(), Resend: resend},
	})
}

// wantLog checks the email.sent logs: none, or one with status.
func (e *docsEnv) wantLog(status string, errClass string) {
	e.T.Helper()
	logs := e.Activities(auditlog.ActivityEmailSent)
	if status == "" {
		if len(logs) != 0 {
			e.T.Errorf("email.sent logs got = %d, want = 0", len(logs))
		}
		return
	}
	if len(logs) != 1 {
		e.T.Fatalf("email.sent logs got = %d, want = 1", len(logs))
	}
	l := logs[0]
	if l.ResourceType != string(auditlog.ResourceInvoice) || l.ResourceID != e.inv.ID.UUID() || l.Object["status"] != status {
		e.T.Errorf("log got = %s %s %v, want = invoice %s %s", l.ResourceType, l.ResourceID, l.Object["status"], e.inv.ID, status)
	}
	doc, _ := l.Object["document"].(map[string]any)
	if doc["number"] != e.inv.Number || doc["type"] != "invoice" {
		e.T.Errorf("log document got = %v, want = invoice %s", doc, e.inv.Number)
	}
	em, _ := l.Object["email"].(map[string]any)
	if fmt.Sprint(em["to"]) != "[ap@customer.test]" {
		e.T.Errorf("log to got = %v, want = [ap@customer.test]", em["to"])
	}
	er, _ := l.Object["error"].(map[string]any)
	if got, _ := er["class"].(string); got != errClass {
		e.T.Errorf("log error class got = %q, want = %q", got, errClass)
	}
}

// TestSendDocumentEmail ports SendEmailJob.
func TestSendDocumentEmail(t *testing.T) {
	// source: spec/jobs/send_email_job_spec.rb:19
	// source: spec/jobs/send_email_job_spec.rb:23
	t.Run("delivers and logs", func(t *testing.T) {
		e := newDocsEnv(t, 100)
		if err := e.run(1, false); err != nil {
			t.Fatal(err)
		}
		if len(e.sender.sent) != 1 {
			t.Fatalf("sent got = %d, want = 1", len(e.sender.sent))
		}
		e.wantLog(mail.ActivitySent, "")
	})

	// source: app/services/emails/resend_service.rb:44-47
	t.Run("resend", func(t *testing.T) {
		e := newDocsEnv(t, 100)
		if err := e.run(1, true); err != nil {
			t.Fatal(err)
		}
		e.wantLog(mail.ActivityResent, "")
	})

	// source: spec/jobs/send_email_job_spec.rb:64
	t.Run("not sent", func(t *testing.T) {
		e := newDocsEnv(t, 0)
		if err := e.run(1, false); err != nil {
			t.Fatal(err)
		}
		if len(e.sender.sent) != 0 {
			t.Errorf("sent got = %d, want = 0", len(e.sender.sent))
		}
		e.wantLog("", "")
	})

	// source: spec/jobs/send_email_job_spec.rb:85
	t.Run("retryable failure", func(t *testing.T) {
		busy := &textproto.Error{Code: 421, Msg: "busy"}
		e := newDocsEnv(t, 100)
		e.sender.err = busy
		if err := e.run(1, false); !errors.Is(err, busy) {
			t.Fatalf("error got = %v, want = %v", err, busy)
		}
		e.wantLog("", "")
		// source: spec/jobs/send_email_job_spec.rb:93
		if err := e.run(smtpBusyAttempts, false); !errors.Is(err, busy) {
			t.Fatalf("last attempt error got = %v, want = %v", err, busy)
		}
		e.wantLog(mail.ActivityFailed, "textproto.Error")
	})

	t.Run("document gone", func(t *testing.T) {
		e := newDocsEnv(t, 100)
		e.inv.ID = core.NewID[core.InvoiceID](e.IDs)
		if err := e.run(1, false); err != nil {
			t.Fatal(err)
		}
		if len(e.sender.sent) != 0 {
			t.Errorf("sent got = %d, want = 0", len(e.sender.sent))
		}
	})
}
