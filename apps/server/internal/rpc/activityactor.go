package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
)

// ActivityActor returns the actor of the activity logs of ctx, for
// [auditlog.NewActivityProducer] (lago Utils::ActivityLog activity_source,
// api_key_id and user_id): an API key is the api source, a user session
// the front source with its user, a customer portal token the front
// source without a user, and anything else (jobs) the system.
func ActivityActor(ctx context.Context) auditlog.Actor {
	p, ok := Principal(ctx)
	if !ok {
		return auditlog.Actor{Source: auditlog.ActivitySourceSystem}
	}
	switch p.Kind {
	case auth.PrincipalAPIKey:
		a := auditlog.Actor{Source: auditlog.ActivitySourceAPI}
		if p.APIKey != nil {
			a.APIKeyID = p.APIKey.ID
		}
		return a
	case auth.PrincipalUser:
		a := auditlog.Actor{Source: auditlog.ActivitySourceFront}
		if p.User != nil {
			a.UserID = p.User.ID
		}
		return a
	case auth.PrincipalPortal:
		return auditlog.Actor{Source: auditlog.ActivitySourceFront}
	default:
		return auditlog.Actor{Source: auditlog.ActivitySourceSystem}
	}
}
