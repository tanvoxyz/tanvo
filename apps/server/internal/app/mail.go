package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/mail"
)

// mailAttempts bounds the attempts of an email failing temporarily
// (lago's SendEmailJob retries SMTP busy replies, timeouts and EOF).
const mailAttempts = 6

// newMailer returns the composer and the sender: SMTP when
// TANVO_SMTP_HOST is set, otherwise a sender that logs and drops the
// messages, so that an installation without mail still runs. The sender
// is traced.
func newMailer(cfg config.Config, log *slog.Logger, tp trace.TracerProvider) (*mail.Composer, mail.Sender, error) {
	composer, err := mail.NewComposer(mail.Config{
		DefaultFrom: cfg.MailFrom, AppURL: cfg.AppURL,
		// Without a blob store no PDF is rendered to attach or link.
		DisablePDFs: cfg.DisablePDFGeneration || cfg.BlobBackend == "",
	})
	if err != nil {
		return nil, nil, err
	}
	var sender mail.Sender = dropSender{log: log}
	if cfg.SMTPHost != "" {
		s, err := mail.NewSMTPSender(mail.SMTPConfig{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			Security: mail.Security(cfg.SMTPSecurity),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("mail: %w", err)
		}
		sender = s
	}
	return composer, tracedSender{next: sender, tracer: tp.Tracer(scopeName)}, nil
}

// dropSender logs the messages it is given instead of sending them.
type dropSender struct{ log *slog.Logger }

// Send implements [mail.Sender].
func (s dropSender) Send(ctx context.Context, m *mail.Message) error {
	s.log.WarnContext(ctx, "mail: no SMTP server configured (TANVO_SMTP_HOST); email dropped", "kind", string(m.Kind), "recipients", len(m.Recipients()))
	return nil
}

// tracedSender wraps a sender in a client span.
type tracedSender struct {
	next   mail.Sender
	tracer trace.Tracer
}

// Send implements [mail.Sender].
func (s tracedSender) Send(ctx context.Context, m *mail.Message) error {
	ctx, span := s.tracer.Start(ctx, "mail send", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("tanvo.mail.kind", string(m.Kind))))
	defer span.End()
	if err := s.next.Send(ctx, m); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// mailJobs runs the auth.JobKindMail jobs the access services enqueue.
type mailJobs struct {
	tx       port.Transactor
	composer *mail.Composer
	sender   mail.Sender
	// blobs holds the organization logos; nil shows none.
	blobs port.BlobStore
}

// register adds the mail kind to reg: temporary SMTP failures are retried
// up to [mailAttempts] times, other failures are not.
func (h mailJobs) register(reg *jobs.Registry) error {
	return jobs.Register(reg, jobs.Kind{
		Name:        auth.JobKindMail,
		MaxAttempts: 1,
		Retry:       []jobs.RetryRule{{Match: mail.IsTemporary, Attempts: mailAttempts}},
	}, jobs.HandlerFunc[auth.MailJob](h.handle))
}

func (h mailJobs) handle(ctx context.Context, j *jobs.Job[auth.MailJob]) error {
	m, err := h.compose(ctx, j.OrganizationID, j.Args)
	if errors.Is(err, mail.ErrSkipped) {
		slog.InfoContext(ctx, "mail: skipped", "template", j.Args.Template, "reason", err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	return h.sender.Send(ctx, m)
}

// compose builds the message of a mail job.
func (h mailJobs) compose(ctx context.Context, org core.OrganizationID, a auth.MailJob) (*mail.Message, error) {
	switch a.Template {
	case auth.MailPasswordReset:
		// auth hands the token; the composer builds the link.
		token := a.Data["token"]
		if token == "" {
			return nil, fmt.Errorf("mail: password reset without token: %w", core.ErrInvalidValue)
		}
		return h.composer.PasswordResetRequested(mail.PasswordResetEmail{Email: a.To, Token: token})
	case auth.MailAPIKeyCreated, auth.MailAPIKeyRotated, auth.MailAPIKeyDestroyed:
		in, err := h.admins(ctx, org)
		if err != nil {
			return nil, err
		}
		switch a.Template {
		case auth.MailAPIKeyCreated:
			return h.composer.APIKeyCreated(in)
		case auth.MailAPIKeyRotated:
			return h.composer.APIKeyRotated(in)
		default:
			return h.composer.APIKeyDestroyed(in)
		}
	case auth.MailAuthenticationMethodsUpdated:
		return h.authenticationMethodsUpdated(ctx, org, a)
	}
	return nil, fmt.Errorf("mail: unknown template %q: %w", a.Template, core.ErrInvalidValue)
}

// admins returns the organization's name and the addresses of its active
// admins.
func (h mailJobs) admins(ctx context.Context, org core.OrganizationID) (mail.APIKeyEmail, error) {
	var out mail.APIKeyEmail
	err := h.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return err
		}
		out.OrganizationName = o.Name
		f := access.MembershipFilter{Status: access.MembershipStatusActive, RoleID: access.BuiltInRoleID(permission.BuiltInRoleAdmin)}
		page := core.PageRequest{Size: core.MaxPageSize}
		for {
			p, err := r.Memberships.List(ctx, org, f, page)
			if err != nil {
				return err
			}
			for _, m := range p.Items {
				u, err := r.Users.Get(ctx, m.UserID)
				if err != nil {
					return err
				}
				out.AdminEmails = append(out.AdminEmails, u.Email)
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	if err != nil {
		return mail.APIKeyEmail{}, fmt.Errorf("mail: admins of %s: %w", org, err)
	}
	return out, nil
}
