package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/mail"
)

// authenticationMethodsUpdated builds the email of an
// [auth.MailAuthenticationMethodsUpdated] job: the organization's contact
// details, the default billing entity's document locale and the admins.
func (h mailJobs) authenticationMethodsUpdated(ctx context.Context, org core.OrganizationID, a auth.MailJob) (*mail.Message, error) {
	admins, err := h.admins(ctx, org)
	if err != nil {
		return nil, err
	}
	in := mail.AuthenticationMethodsEmail{
		OrganizationName: admins.OrganizationName,
		AdminEmails:      admins.AdminEmails,
		ActorEmail:       a.Data["actor_email"],
		Additions:        auth.SplitMethods(a.Data["additions"]),
		Deletions:        auth.SplitMethods(a.Data["deletions"]),
		Locale:           "en",
	}
	err = h.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return err
		}
		if o.Email != nil {
			in.OrganizationEmail = *o.Email
		}
		in.FromEmailEnabled = o.FromEmailEnabled
		// layouts/mailer.slim:50-53: the organization's logo_url.
		if h.blobs != nil && !o.Logo.IsZero() {
			u, err := h.blobs.URL(ctx, org, o.Logo)
			if err != nil && !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("link logo: %w", err)
			}
			in.OrganizationLogoURL = u
		}
		if be, err := r.BillingEntities.GetDefault(ctx, org); err == nil && be.DocumentLocale != "" {
			in.Locale = be.DocumentLocale
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mail: organization %s: %w", org, err)
	}
	return h.composer.AuthenticationMethodsUpdated(in)
}
