package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// props are the properties of a record; a nil value is sent as null.
type props = map[string]any

// codeNotDeployed is the failure of a record synced before its custom
// object exists.
const codeNotDeployed = "object_not_deployed"

// nullable is s, or nil when empty.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// compact sets the non-empty values of kv into p (Rails compact_blank).
func compact(p props, kv ...string) props {
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			p[kv[i]] = kv[i+1]
		}
	}
	return p
}

var scheme = regexp.MustCompile(`\Ahttps?://`)

// cleanURL is the host of a website (lago contacts/payloads/
// base_payload.rb#clean_url); "" when it has none.
func cleanURL(s string) string {
	if !scheme.MatchString(s) {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// day is a date as HubSpot's date properties take it ("2024-07-08"), or
// nil (lago base_payload.rb#formatted_date).
func day(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.DateOnly)
}

// major is minor units of c as a JSON number of major units (lago
// base_payload.rb#amount).
func major(c money.Currency, cents int64) json.Number {
	return json.Number(integration.Major(c, money.NewFromInt(cents)).Reduce().String())
}

// recordAnswer is a created or updated CRM record.
type recordAnswer struct {
	ID         string            `json:"id"`
	Properties map[string]string `json:"properties"`
}

// upsert creates a record of objectType with body, or updates record id
// when id is set, and returns the record.
func (g *Gateway) upsert(ctx context.Context, conn integration.Connection, op, objectType, id string, body any) (recordAnswer, error) {
	method, path := http.MethodPost, "/crm/v3/objects/"+url.PathEscape(objectType)
	if id != "" {
		method, path = http.MethodPatch, path+"/"+url.PathEscape(id)
	}
	resp, err := g.call(ctx, conn, op, method, path, body)
	if err != nil {
		return recordAnswer{}, err
	}
	var out recordAnswer
	if err := resp.Decode(&out); err != nil {
		return recordAnswer{}, fmt.Errorf("hubspot %s: %w", op, err)
	}
	if out.ID == "" {
		return recordAnswer{}, fmt.Errorf("hubspot %s: %w", op, errNoID)
	}
	return out, nil
}

// customerObject is the standard object of c's record.
func customerObject(c *integration.Contact) string {
	if c.TargetedObject == integration.TargetedObjectContacts {
		return objectTypeContacts
	}
	return objectTypeCompanies
}

// contactProperties are the properties of c's company or contact; an
// update of a contact leaves its Tanvo properties (lago companies/
// payloads/hubspot.rb:8-48, contacts/payloads/hubspot.rb:8-47).
func contactProperties(c *integration.Contact, update bool) props {
	if customerObject(c) == objectTypeCompanies {
		p := props{
			"tanvo_customer_id": c.CustomerID.String(), "tanvo_customer_external_id": c.ExternalID,
			"tanvo_billing_email": nullable(c.Email), "tanvo_tax_identification_number": nullable(c.TaxNumber),
			"tanvo_customer_link": c.Link,
		}
		return compact(p, "name", c.Name, "domain", cleanURL(c.URL))
	}
	p := props{}
	if !update {
		p = props{
			"tanvo_customer_id": c.CustomerID.String(), "tanvo_customer_external_id": c.ExternalID,
			"tanvo_billing_email": nullable(c.Email), "tanvo_customer_link": c.Link,
		}
	}
	return compact(p, "email", c.Email, "firstname", c.Firstname, "lastname", c.Lastname,
		"phone", c.Phone, "company", c.LegalName, "website", cleanURL(c.URL))
}

// CreateContact implements [integration.ContactGateway]: it creates the
// customer's company or contact by its targeted object (lago
// Integrations::Aggregator::Companies::CreateService and Contacts::
// CreateService).
func (g *Gateway) CreateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	rec, err := g.upsert(ctx, conn, "create_"+customerObject(c), customerObject(c), "", map[string]any{"properties": contactProperties(c, false)})
	if err != nil {
		return integration.ContactResult{}, err
	}
	return integration.ContactResult{ExternalID: rec.ID, Email: rec.Properties["email"]}, nil
}

// UpdateContact implements [integration.ContactGateway]: it updates the
// company or contact c.LinkedID (lago Companies::UpdateService and
// Contacts::UpdateService).
func (g *Gateway) UpdateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	if c.LinkedID == "" {
		return integration.ContactResult{}, fmt.Errorf("hubspot update_%s: no record: %w", customerObject(c), core.ErrInvalidValue)
	}
	rec, err := g.upsert(ctx, conn, "update_"+customerObject(c), customerObject(c), c.LinkedID, map[string]any{"properties": contactProperties(c, true)})
	if err != nil {
		return integration.ContactResult{}, err
	}
	return integration.ContactResult{ExternalID: rec.ID, Email: rec.Properties["email"]}, nil
}

// objectType returns the deployed custom object type of key.
func objectType(conn integration.Connection, key, op string) (string, error) {
	t := conn.Setting(key)
	if t == "" {
		return "", fmt.Errorf("hubspot %s: %w", op, &integration.ProviderError{
			Provider: Provider, Kind: integration.KindInvalidRequest, Code: codeNotDeployed, Message: "The custom object is not deployed.",
		})
	}
	return t, nil
}

// recordBody is the body of a record: a new record has no associations,
// which follow by [Gateway.Associate].
func recordBody(p props, create bool) map[string]any {
	if create {
		return map[string]any{"associations": []any{}, "properties": p}
	}
	return map[string]any{"properties": p}
}

// SyncInvoice implements [integration.CRMGateway] (lago invoices/payloads/
// hubspot.rb:7-76).
func (g *Gateway) SyncInvoice(ctx context.Context, conn integration.Connection, inv *integration.CRMInvoice) (string, error) {
	op := "create_invoice"
	if inv.RecordID != "" {
		op = "update_invoice"
	}
	t, err := objectType(conn, KeyInvoicesObjectTypeID, op)
	if err != nil {
		return "", err
	}
	c := inv.Currency
	p := props{
		"tanvo_invoice_id":                       inv.ID.String(),
		"tanvo_invoice_number":                   inv.Number,
		"tanvo_invoice_purchase_order_number":    nullable(inv.PurchaseOrderNumber),
		"tanvo_invoice_issuing_date":             nullableDate(inv.IssuingDate),
		"tanvo_invoice_payment_due_date":         nullableDate(inv.PaymentDueDate),
		"tanvo_invoice_payment_overdue":          inv.PaymentOverdue,
		"tanvo_invoice_type":                     inv.Type,
		"tanvo_invoice_status":                   inv.Status,
		"tanvo_invoice_payment_status":           inv.PaymentStatus,
		"tanvo_invoice_currency":                 c.Code(),
		"tanvo_invoice_total_amount":             major(c, inv.TotalAmountCents),
		"tanvo_invoice_total_due_amount":         major(c, inv.TotalDueAmountCents),
		"tanvo_invoice_subtotal_excluding_taxes": major(c, inv.SubTotalExcludingTaxesAmountCents),
		"tanvo_invoice_file_url":                 inv.FileURL,
		"tanvo_invoice_url":                      inv.URL,
	}
	rec, err := g.upsert(ctx, conn, op, t, inv.RecordID, recordBody(p, inv.RecordID == ""))
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

func nullableDate(d core.Date) any {
	if d.IsZero() {
		return nil
	}
	return d.String()
}

// SyncSubscription implements [integration.CRMGateway] (lago
// subscriptions/payloads/hubspot.rb:7-58).
func (g *Gateway) SyncSubscription(ctx context.Context, conn integration.Connection, sub *integration.CRMSubscription) (string, error) {
	op := "create_subscription"
	if sub.RecordID != "" {
		op = "update_subscription"
	}
	t, err := objectType(conn, KeySubscriptionsObjectTypeID, op)
	if err != nil {
		return "", err
	}
	p := props{
		"tanvo_subscription_id":             sub.ID.String(),
		"tanvo_external_subscription_id":    sub.ExternalID,
		"tanvo_billing_time":                sub.BillingTime,
		"tanvo_subscription_name":           nullable(sub.Name),
		"tanvo_subscription_plan_code":      sub.PlanCode,
		"tanvo_subscription_status":         sub.Status,
		"tanvo_subscription_created_at":     day(sub.CreatedAt),
		"tanvo_subscription_started_at":     day(sub.StartedAt),
		"tanvo_subscription_ending_at":      day(sub.EndingAt),
		"tanvo_subscription_at":             day(sub.SubscriptionAt),
		"tanvo_subscription_terminated_at":  day(sub.TerminatedAt),
		"tanvo_subscription_trial_ended_at": day(sub.TrialEndedAt),
		"tanvo_subscription_link":           sub.URL,
	}
	rec, err := g.upsert(ctx, conn, op, t, sub.RecordID, recordBody(p, sub.RecordID == ""))
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// Associate implements [integration.CRMGateway]: it links the invoice or
// subscription record to the customer's company or contact with the
// default association (lago invoices/payloads/hubspot.rb:78-86,
// subscriptions/payloads/hubspot.rb:60-68).
func (g *Gateway) Associate(ctx context.Context, conn integration.Connection, a integration.CRMAssociation) error {
	var key string
	switch a.Object {
	case integration.CRMObjectInvoices:
		key = KeyInvoicesObjectTypeID
	case integration.CRMObjectSubscriptions:
		key = KeySubscriptionsObjectTypeID
	default:
		return fmt.Errorf("hubspot associate %s: %w", a.Object, integration.ErrUnsupported)
	}
	op := "associate_" + string(a.Object)
	from, err := objectType(conn, key, op)
	if err != nil {
		return err
	}
	// lago IntegrationCustomers::HubspotCustomer#object_type.
	to := "company"
	if a.TargetedObject == integration.TargetedObjectContacts {
		to = "contact"
	}
	path := "/crm/v4/objects/" + url.PathEscape(from) + "/" + url.PathEscape(a.RecordID) +
		"/associations/default/" + to + "/" + url.PathEscape(a.CustomerRecordID)
	_, err = g.call(ctx, conn, op, http.MethodPut, path, nil)
	return err
}
