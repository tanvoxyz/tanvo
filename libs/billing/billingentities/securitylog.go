package billingentities

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Billing entity security log events (lago Utils::SecurityLog, log_type
// "billing_entity", clickhouse/security_log.rb).
const (
	// SecurityLogType is the log type of the billing entity events.
	SecurityLogType = "billing_entity"
	// EventCreated: a billing entity was created; the resources hold its
	// name and code (create_service.rb:113-120).
	EventCreated = "billing_entity.created"
	// EventUpdated: a billing entity was updated; the resources also hold
	// each changed attribute, the changed tax codes and the changed
	// dunning campaign as {deleted, added} (update_service.rb:98-121,
	// update_applied_dunning_campaign_service.rb:33-44).
	EventUpdated = "billing_entity.updated"
)

// SecurityLogger records the billing entity security logs; apps/server
// adapts the security log store.
type SecurityLogger interface {
	// RecordBillingEntity records event of org with resources. ctx
	// carries the change's transaction, which the entry joins.
	RecordBillingEntity(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error
}

// WithSecurityLog makes s record the billing entity security logs through
// l in the transaction of each change, and returns s.
func (s *Service) WithSecurityLog(l SecurityLogger) *Service {
	s.log = l
	return s
}

// recordSecurity writes event for be with the changed attributes diff; a
// nil logger records nothing.
func (s *Service) recordSecurity(ctx context.Context, be *organization.BillingEntity, event string, diff map[string]any) error {
	if s.log == nil {
		return nil
	}
	res := map[string]any{"billing_entity_name": be.Name, "billing_entity_code": be.Code}
	for k, v := range diff {
		res[k] = v
	}
	if err := s.log.RecordBillingEntity(ctx, be.OrganizationID, event, res); err != nil {
		return fmt.Errorf("record %s: %w", event, err)
	}
	return nil
}

// securityDiff returns the changes from prev to be that the update's
// security log carries: each changed column of lago's billing_entities
// table as {deleted, added} without a nil side (update_service.rb:99-103),
// tax_codes as the deleted and added codes (:105-113), and
// applied_dunning_campaign as the old and new campaign codes
// (update_applied_dunning_campaign_service.rb:40). Lago changes the
// dunning campaign in its own mutation, so its entry is logged alone;
// Tanvo's update takes it with the other attributes and logs one entry.
// The settings outside lago's columns (the invoice custom sections, the
// credit invoice payment delay) are not logged, as in lago.
func (s *Service) securityDiff(ctx context.Context, r port.Repos, prev, be *organization.BillingEntity) (map[string]any, error) {
	before, after := securityColumns(prev), securityColumns(be)
	diff := map[string]any{}
	for k, a := range after {
		if b := before[k]; !reflect.DeepEqual(b, a) {
			diff[k] = deletedAdded(b, a)
		}
	}
	if !sameIDs(prev.TaxIDs, be.TaxIDs) {
		old, err := taxCodes(ctx, r, prev)
		if err != nil {
			return nil, err
		}
		cur, err := taxCodes(ctx, r, be)
		if err != nil {
			return nil, err
		}
		entry := map[string]any{}
		if deleted := minus(old, cur); len(deleted) > 0 {
			entry["deleted"] = deleted
		}
		if added := minus(cur, old); len(added) > 0 {
			entry["added"] = added
		}
		if len(entry) > 0 {
			diff["tax_codes"] = entry
		}
	}
	if prev.AppliedDunningCampaignID != be.AppliedDunningCampaignID {
		old, err := campaignCode(ctx, r, prev)
		if err != nil {
			return nil, err
		}
		cur, err := campaignCode(ctx, r, be)
		if err != nil {
			return nil, err
		}
		diff["applied_dunning_campaign"] = deletedAdded(old, cur)
	}
	return diff, nil
}

// securityColumns returns the attributes of be under the names and in the
// representation of lago's billing_entities columns; an unset value is
// nil.
func securityColumns(be *organization.BillingEntity) map[string]any {
	str := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	ptr := func(v *string) any {
		if v == nil {
			return nil
		}
		return *v
	}
	tz := "UTC"
	if be.Timezone != nil {
		tz = be.Timezone.String()
	}
	settings := make([]string, len(be.EmailSettings))
	for i, e := range be.EmailSettings {
		settings[i] = string(e)
	}
	return map[string]any{
		"name":                         str(be.Name),
		"code":                         str(be.Code),
		"legal_name":                   ptr(be.LegalName),
		"legal_number":                 ptr(be.LegalNumber),
		"tax_identification_number":    ptr(be.TaxID),
		"email":                        ptr(be.Email),
		"phone":                        ptr(be.Phone),
		"address_line1":                str(be.Address.Line1),
		"address_line2":                str(be.Address.Line2),
		"city":                         str(be.Address.City),
		"state":                        str(be.Address.State),
		"zipcode":                      str(be.Address.Zipcode),
		"country":                      str(be.Address.Country),
		"timezone":                     tz,
		"default_currency":             str(be.DefaultCurrency.String()),
		"document_locale":              str(be.DocumentLocale),
		"document_numbering":           str(string(be.DocumentNumbering)),
		"document_number_prefix":       str(be.DocumentNumberPrefix),
		"invoice_footer":               ptr(be.InvoiceFooter),
		"invoice_grace_period":         be.InvoiceGracePeriodDays,
		"net_payment_term":             be.NetPaymentTermDays,
		"finalize_zero_amount_invoice": be.FinalizeZeroAmountInvoice,
		"email_settings":               settings,
		"eu_tax_management":            be.EUTaxManagement,
		"einvoicing":                   be.EInvoicing,
		"subscription_invoice_issuing_date_anchor":     str(string(be.IssuingDateAnchor)),
		"subscription_invoice_issuing_date_adjustment": str(string(be.IssuingDateAdjustment)),
	}
}

// campaignOnly reports whether in sets the applied dunning campaign and
// nothing else, as lago's UpdateAppliedDunningCampaign mutation.
func campaignOnly(in Input) bool {
	if !in.AppliedDunningCampaign.Set {
		return false
	}
	v := reflect.ValueOf(in)
	for i := range v.NumField() {
		if v.Type().Field(i).Name != "AppliedDunningCampaign" && v.Field(i).FieldByName("Set").Bool() {
			return false
		}
	}
	return true
}

// deletedAdded returns {deleted: old, added: cur} without a nil side
// (lago Hash#compact).
func deletedAdded(old, cur any) map[string]any {
	out := map[string]any{}
	if old != nil {
		out["deleted"] = old
	}
	if cur != nil {
		out["added"] = cur
	}
	return out
}

// taxCodes returns the codes of the taxes of be.
func taxCodes(ctx context.Context, r port.Repos, be *organization.BillingEntity) ([]string, error) {
	out := make([]string, 0, len(be.TaxIDs))
	for _, id := range be.TaxIDs {
		t, err := r.Taxes.Get(ctx, be.OrganizationID, id)
		if err != nil {
			return nil, fmt.Errorf("get tax %s: %w", id, err)
		}
		out = append(out, t.Code)
	}
	return out, nil
}

// campaignCode returns the code of the applied dunning campaign of be, or
// nil without one.
func campaignCode(ctx context.Context, r port.Repos, be *organization.BillingEntity) (any, error) {
	if be.AppliedDunningCampaignID.IsZero() {
		return nil, nil
	}
	c, err := r.DunningCampaigns.Get(ctx, be.OrganizationID, be.AppliedDunningCampaignID)
	if err != nil {
		return nil, fmt.Errorf("get dunning campaign %s: %w", be.AppliedDunningCampaignID, err)
	}
	return c.Code, nil
}

// sameIDs reports whether a and b hold the same IDs in any order.
func sameIDs[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// minus returns the elements of a not in b, in the order of a.
func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
