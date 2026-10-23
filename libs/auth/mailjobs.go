package auth

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// JobKindMail is the kind of the jobs the services enqueue to send an
// email; libs/mail handles it. The arguments are a [MailJob].
const JobKindMail = "mail.send"

// Mail templates the services request (lago mailers).
const (
	// MailPasswordReset is PasswordResetMailer#requested; Data["token"]
	// is the reset token.
	MailPasswordReset = "password_reset.requested"
	// MailAPIKeyCreated is ApiKeyMailer#created.
	MailAPIKeyCreated = "api_key.created"
	// MailAPIKeyRotated is ApiKeyMailer#rotated.
	MailAPIKeyRotated = "api_key.rotated"
	// MailAPIKeyDestroyed is ApiKeyMailer#destroyed.
	MailAPIKeyDestroyed = "api_key.destroyed"
)

// MailJob is the argument of a [JobKindMail] job.
type MailJob struct {
	// Template names the email ([MailPasswordReset], …).
	Template string `json:"template"`
	// OrganizationID is the organization whose admins receive API key
	// emails, and the job's tenant for user emails.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// To is the recipient for user emails (password reset); empty means
	// the organization's admins.
	To string `json:"to,omitempty"`
	// Data are the template variables (reset token, key name and prefix,
	// instant).
	Data map[string]string `json:"data,omitempty"`
}

func enqueueMail(ctx context.Context, r port.Repos, m MailJob) error {
	args, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode mail job: %w", err)
	}
	if r.Jobs == nil {
		return nil
	}
	if err := r.Jobs.Enqueue(ctx, port.Job{
		Kind: JobKindMail, Queue: port.QueueDefault, OrganizationID: m.OrganizationID, Args: args,
	}); err != nil {
		return fmt.Errorf("enqueue mail %s: %w", m.Template, err)
	}
	return nil
}
