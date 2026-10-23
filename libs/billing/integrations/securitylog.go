package integrations

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Integration security log events (lago Utils::SecurityLog, log_type
// "integration").
const (
	// SecurityLogType is the log type of the integration events.
	SecurityLogType = "integration"
	// EventCreated: an integration was created (integrations/
	// create_service.rb:23-30, payment_providers/base_service.rb:36-55).
	EventCreated = "integration.created"
	// EventUpdated: an integration was updated; the resources also hold
	// each changed attribute as {deleted, added} (integrations/
	// update_service.rb:23-35).
	EventUpdated = "integration.updated"
	// EventDeleted: an integration was deleted (integrations/
	// destroy_service.rb:27-34, payment_providers/destroy_service.rb:37-47).
	EventDeleted = "integration.deleted"
)

// SecurityLogger records the integration security logs; apps/server adapts
// the security log store. The SSO connections, which libs/auth manages,
// record their own.
type SecurityLogger interface {
	// RecordIntegration records event of org with resources.
	RecordIntegration(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error
}

// WithSecurityLog makes s record the integration security logs through l
// in the transaction of each change, and returns s.
func (s *Service) WithSecurityLog(l SecurityLogger) *Service {
	s.log = l
	return s
}

// record writes event for i with the changed attributes diff; a nil
// logger records nothing.
func (s *Service) record(ctx context.Context, i *integration.Integration, event string, diff map[string]any) error {
	if s.log == nil {
		return nil
	}
	res := map[string]any{"integration_name": i.Name, "integration_type": string(i.Provider)}
	maps.Copy(res, diff)
	if err := s.log.RecordIntegration(ctx, i.OrganizationID, event, res); err != nil {
		return fmt.Errorf("record %s: %w", event, err)
	}
	return nil
}

// logState returns the attributes of i an update log compares: lago's
// columns except updated_at and secrets. The sync flags of the provider
// are in the settings, as lago stores them.
func logState(i *integration.Integration) (map[string]any, error) {
	settings, err := Settings(i)
	if err != nil {
		return nil, err
	}
	all := map[string]any{}
	for k, v := range settings {
		all[k] = v
	}
	flags := map[string]bool{
		syncInvoices: i.SyncInvoices, syncCreditNotes: i.SyncCreditNotes,
		syncPayments: i.SyncPayments, syncSubscriptions: i.SyncSubscriptions,
	}
	for _, f := range specs[i.Provider].sync {
		all[f] = flags[f]
	}
	return map[string]any{"code": i.Code, "name": i.Name, "settings": all}, nil
}

// logDiff returns the attributes that differ between before and after,
// each as {deleted, added} (lago previous_changes).
func logDiff(before, after map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(after)) {
		if !reflect.DeepEqual(before[k], after[k]) {
			out[k] = map[string]any{"deleted": before[k], "added": after[k]}
		}
	}
	return out
}
