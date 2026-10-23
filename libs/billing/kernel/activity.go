package kernel

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// LogActivity records the activity log of a in r, the repositories of the
// service's transaction, so that the log commits with the change and an
// error rolls the change back (lago Utils::ActivityLog.produce and the
// activity_loggable declarations of the services,
// app/services/base_service.rb:151). It records nothing without
// [Deps.ActivityLogs].
func (d *Deps) LogActivity(ctx context.Context, r port.Repos, a auditlog.Activity) error {
	if d.ActivityLogs == nil {
		return nil
	}
	return d.ActivityLogs.Produce(ctx, r.ActivityLogs, a)
}
