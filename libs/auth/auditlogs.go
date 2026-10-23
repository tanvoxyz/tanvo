package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// AuditLogs reads the activity, API and security logs of an organization
// (lago ActivityLogsQuery, ApiLogsQuery and SecurityLogsQuery).
type AuditLogs struct{ d Deps }

// NewAuditLogs returns the service.
func NewAuditLogs(d Deps) *AuditLogs { return &AuditLogs{d: d} }

// ActivityLogView is an activity log with the email of its user.
type ActivityLogView struct {
	*auditlog.ActivityLog
	// UserEmail is the email of the user; "" when none.
	UserEmail string
}

// SecurityLogView is a security log with the email of its user.
type SecurityLogView struct {
	*auditlog.SecurityLog
	// UserEmail is the email of the user; "" when none.
	UserEmail string
}

// ActivityLogQuery filters activity logs.
type ActivityLogQuery struct {
	auditlog.ActivityLogFilter
	// UserEmails keeps the logs of the users with these emails (lago
	// ActivityLogsQuery#with_user_emails); an unknown email matches nothing.
	UserEmails []string
}

// GetActivityLog returns the activity log id; an unknown one is
// "activity_log_not_found".
func (s *AuditLogs) GetActivityLog(ctx context.Context, org core.OrganizationID, id uuid.UUID) (ActivityLogView, error) {
	var out ActivityLogView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		l, err := r.ActivityLogs.Get(ctx, org, id)
		if err != nil {
			return mapNotFound(err, "activity_log")
		}
		out = ActivityLogView{ActivityLog: l}
		out.UserEmail, err = userEmail(ctx, r, l.UserID)
		return err
	})
	return out, err
}

// ListActivityLogs returns the activity logs of org within its audit log
// retention, newest first (lago app/queries/activity_logs_query.rb).
func (s *AuditLogs) ListActivityLogs(ctx context.Context, org core.OrganizationID, q ActivityLogQuery, page core.PageRequest) (core.Page[ActivityLogView], error) {
	var out core.Page[ActivityLogView]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return mapNotFound(err, "organization")
		}
		f := q.ActivityLogFilter
		f.LoggedAt = s.retained(f.LoggedAt, retentionDays(o))
		if len(q.UserEmails) > 0 {
			ids, err := userIDs(ctx, r, q.UserEmails)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			f.UserIDs = append(f.UserIDs, ids...)
		}
		p, err := r.ActivityLogs.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list activity logs: %w", err)
		}
		out.NextToken = p.NextToken
		out.Items = make([]ActivityLogView, len(p.Items))
		for i, l := range p.Items {
			out.Items[i] = ActivityLogView{ActivityLog: l}
			if out.Items[i].UserEmail, err = userEmail(ctx, r, l.UserID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// GetAPILog returns the API log id; an unknown one is "api_log_not_found".
func (s *AuditLogs) GetAPILog(ctx context.Context, org core.OrganizationID, id uuid.UUID) (*auditlog.APILog, error) {
	var out *auditlog.APILog
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.APILogs.Get(ctx, org, id)
		if err != nil {
			return mapNotFound(err, "api_log")
		}
		return nil
	})
	return out, err
}

// ListAPILogs returns the API logs of org within its audit log retention,
// newest first (lago app/queries/api_logs_query.rb).
func (s *AuditLogs) ListAPILogs(ctx context.Context, org core.OrganizationID, f auditlog.APILogFilter, page core.PageRequest) (core.Page[*auditlog.APILog], error) {
	var out core.Page[*auditlog.APILog]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return mapNotFound(err, "organization")
		}
		f.LoggedAt = s.retained(f.LoggedAt, retentionDays(o))
		out, err = r.APILogs.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list api logs: %w", err)
		}
		return nil
	})
	return out, err
}

// GetSecurityLog returns the security log id; an unknown one, or one older
// than [auditlog.SecurityLogsRetention], is "security_log_not_found" (lago
// Clickhouse::SecurityLog default_scope).
func (s *AuditLogs) GetSecurityLog(ctx context.Context, org core.OrganizationID, id uuid.UUID) (SecurityLogView, error) {
	var out SecurityLogView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		l, err := r.SecurityLogs.Get(ctx, org, id)
		if err != nil {
			return mapNotFound(err, "security_log")
		}
		if l.LoggedAt.Before(s.d.now().Add(-auditlog.SecurityLogsRetention)) {
			return notFound("security_log")
		}
		out = SecurityLogView{SecurityLog: l}
		out.UserEmail, err = userEmail(ctx, r, l.UserID)
		return err
	})
	return out, err
}

// ListSecurityLogs returns the security logs of org of the last
// [auditlog.SecurityLogsRetention], newest first. The upper bound of
// f.LoggedAt is mandatory: without it the query fails with "to_date"
// value_is_mandatory (lago app/queries/security_logs_query.rb:17).
func (s *AuditLogs) ListSecurityLogs(ctx context.Context, org core.OrganizationID, f auditlog.SecurityLogFilter, page core.PageRequest) (core.Page[SecurityLogView], error) {
	if f.LoggedAt.To == nil {
		return core.Page[SecurityLogView]{}, fieldError("to_date", core.CodeRequired)
	}
	f.LoggedAt = s.retained(f.LoggedAt, int(auditlog.SecurityLogsRetention/(24*time.Hour)))
	var out core.Page[SecurityLogView]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.SecurityLogs.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list security logs: %w", err)
		}
		out.NextToken = p.NextToken
		out.Items = make([]SecurityLogView, len(p.Items))
		for i, l := range p.Items {
			out.Items[i] = SecurityLogView{SecurityLog: l}
			if out.Items[i].UserEmail, err = userEmail(ctx, r, l.UserID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// retained raises the lower bound of rg to the start of the retention of
// days days.
func (s *AuditLogs) retained(rg auditlog.Range, days int) auditlog.Range {
	start := s.d.now().AddDate(0, 0, -days)
	if rg.From == nil || rg.From.Before(start) {
		rg.From = &start
	}
	return rg
}

func retentionDays(o *organization.Organization) int {
	if o.AuditLogsRetentionDays > 0 {
		return o.AuditLogsRetentionDays
	}
	return organization.DefaultAuditLogsRetentionDays
}

// userEmail returns the email of the user id, "" for none or a deleted one.
func userEmail(ctx context.Context, r port.Repos, id core.UserID) (string, error) {
	if id.IsZero() {
		return "", nil
	}
	u, err := r.Users.Get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	return u.Email, nil
}

// userIDs returns the users with the emails; unknown emails are skipped.
func userIDs(ctx context.Context, r port.Repos, emails []string) ([]core.UserID, error) {
	var out []core.UserID
	for _, e := range emails {
		u, err := r.Users.GetByEmail(ctx, e)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get user: %w", err)
		}
		out = append(out, u.ID)
	}
	return out, nil
}
