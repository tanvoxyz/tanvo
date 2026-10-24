package hubspot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Standard object types of companies and contacts (lago
// app/models/integrations/hubspot_integration.rb:13-19).
const (
	objectTypeCompanies = "companies"
	objectTypeContacts  = "contacts"
)

// Names of the custom objects.
const (
	invoicesObject      = "TanvoInvoices"
	subscriptionsObject = "TanvoSubscriptions"
)

// codeAlreadyExists is HubSpot's error category of a property or object
// that exists.
const codeAlreadyExists = "OBJECT_ALREADY_EXISTS"

// option is a choice of an enumeration or boolean property.
type option struct {
	Label        string `json:"label"`
	Value        string `json:"value"`
	DisplayOrder int    `json:"displayOrder,omitempty"`
}

// property is a property definition of the properties and schemas APIs.
type property struct {
	GroupName                string   `json:"groupName,omitempty"`
	Name                     string   `json:"name"`
	Label                    string   `json:"label"`
	Type                     string   `json:"type"`
	FieldType                string   `json:"fieldType"`
	DisplayOrder             *int     `json:"displayOrder,omitempty"`
	HasUniqueValue           *bool    `json:"hasUniqueValue,omitempty"`
	SearchableInGlobalSearch bool     `json:"searchableInGlobalSearch,omitempty"`
	FormField                bool     `json:"formField,omitempty"`
	Options                  []option `json:"options,omitempty"`
}

// objectSchema is a custom object definition of the schemas API.
type objectSchema struct {
	Name                       string            `json:"name"`
	Description                string            `json:"description"`
	RequiredProperties         []string          `json:"requiredProperties"`
	Labels                     map[string]string `json:"labels"`
	PrimaryDisplayProperty     string            `json:"primaryDisplayProperty"`
	SecondaryDisplayProperties []string          `json:"secondaryDisplayProperties"`
	SearchableProperties       []string          `json:"searchableProperties"`
	Properties                 []property        `json:"properties"`
	AssociatedObjects          []string          `json:"associatedObjects"`
}

// schema is what [Gateway.Deploy] creates for a [integration.CRMObject]:
// a custom object, then properties of the object.
type schema struct {
	// object is the custom object; nil for a standard object.
	object *objectSchema
	// objectType is the standard object's type.
	objectType string
	// typeKey and objectVersionKey store the custom object's type ID and
	// deployed version.
	typeKey, objectVersionKey string
	objectVersion             int
	// properties are created once on the object.
	properties           []property
	propertiesVersionKey string
	propertiesVersion    int
}

func ptr[T any](v T) *T { return &v }

// customerProperties are the Tanvo properties of companies or contacts in
// group (lago hubspot/companies/deploy_properties_service.rb:45-96 and
// hubspot/contacts/deploy_properties_service.rb:45-87). Only companies
// carry the tax identification number, and only contacts have a unique
// external ID.
func customerProperties(group string, companies bool) []property {
	ps := []property{
		{
			GroupName: group, Name: "tanvo_customer_id", Label: "Tanvo Customer Id", Type: "string", FieldType: "text",
			DisplayOrder: ptr(-1), HasUniqueValue: ptr(true), SearchableInGlobalSearch: true, FormField: true,
		},
		{
			GroupName: group, Name: "tanvo_customer_external_id", Label: "Tanvo Customer External Id", Type: "string", FieldType: "text",
			DisplayOrder: ptr(-1), SearchableInGlobalSearch: true, FormField: true,
		},
		{
			GroupName: group, Name: "tanvo_billing_email", Label: "Tanvo Billing Email", Type: "string", FieldType: "text",
			SearchableInGlobalSearch: true, FormField: true,
		},
	}
	if companies {
		ps = append(ps, property{
			GroupName: group, Name: "tanvo_tax_identification_number", Label: "Tanvo Tax Identification Number",
			Type: "string", FieldType: "text", SearchableInGlobalSearch: true, FormField: true,
		})
	} else {
		ps[1].HasUniqueValue = ptr(true)
	}
	return append(ps, property{
		GroupName: group, Name: "tanvo_customer_link", Label: "Tanvo Customer Link", Type: "string", FieldType: "text",
		SearchableInGlobalSearch: true, FormField: true,
	})
}

func text(name, label string) property {
	return property{Name: name, Label: label, Type: "string", FieldType: "text"}
}

func date(name, label string) property {
	return property{Name: name, Label: label, Type: "date", FieldType: "date"}
}

func number(name, label string) property {
	return property{Name: name, Label: label, Type: "number", FieldType: "number"}
}

func searchable(p property) property {
	p.SearchableInGlobalSearch = true
	return p
}

func unique(p property) property {
	p.HasUniqueValue = ptr(true)
	return p
}

// schemas are the deployments of each object (lago hubspot/*/
// deploy_object_service.rb and deploy_properties_service.rb).
func schemas() map[integration.CRMObject]schema {
	return map[integration.CRMObject]schema{
		// hubspot/invoices/deploy_object_service.rb:60-172 and
		// deploy_properties_service.rb:45-58.
		integration.CRMObjectInvoices: {
			object: &objectSchema{
				Name: invoicesObject, Description: "Invoices issued by Tanvo billing engine",
				RequiredProperties:         []string{"tanvo_invoice_id"},
				Labels:                     map[string]string{"singular": "TanvoInvoice", "plural": "TanvoInvoices"},
				PrimaryDisplayProperty:     "tanvo_invoice_number",
				SecondaryDisplayProperties: []string{"tanvo_invoice_status", "tanvo_invoice_id"},
				SearchableProperties:       []string{"tanvo_invoice_number", "tanvo_invoice_id"},
				Properties: []property{
					unique(searchable(text("tanvo_invoice_id", "Tanvo Invoice Id"))),
					searchable(text("tanvo_invoice_number", "Tanvo Invoice Number")),
					searchable(text("tanvo_invoice_purchase_order_number", "Tanvo Purchase Order Number")),
					date("tanvo_invoice_issuing_date", "Tanvo Invoice Issuing Date"),
					date("tanvo_invoice_payment_due_date", "Tanvo Invoice Payment Due Date"),
					{
						Name: "tanvo_invoice_payment_overdue", Label: "Tanvo Invoice Payment Overdue", GroupName: invoicesObject,
						Type: "bool", FieldType: "booleancheckbox",
						Options: []option{{Label: "True", Value: "true"}, {Label: "False", Value: "false"}},
					},
					text("tanvo_invoice_type", "Tanvo Invoice Type"),
					text("tanvo_invoice_status", "Tanvo Invoice Status"),
					text("tanvo_invoice_payment_status", "Tanvo Invoice Payment Status"),
					text("tanvo_invoice_currency", "Tanvo Invoice Currency"),
					number("tanvo_invoice_total_amount", "Tanvo Invoice Total Amount"),
					number("tanvo_invoice_subtotal_excluding_taxes", "Tanvo Invoice Subtotal Excluding Taxes"),
					{Name: "tanvo_invoice_file_url", Label: "Tanvo Invoice File URL", Type: "string", FieldType: "file"},
					text("tanvo_invoice_url", "Tanvo Invoice URL"),
				},
				AssociatedObjects: []string{"COMPANY", "CONTACT"},
			},
			typeKey: KeyInvoicesObjectTypeID, objectVersionKey: KeyInvoicesObjectVersion, objectVersion: 1,
			properties: []property{{
				GroupName: "tanvoinvoices_information", Name: "tanvo_invoice_total_due_amount",
				Label: "Tanvo Invoice Total Due Amount", Type: "number", FieldType: "number",
			}},
			propertiesVersionKey: KeyInvoicesPropertiesVersion, propertiesVersion: 1,
		},
		// hubspot/subscriptions/deploy_object_service.rb:57-169 and
		// deploy_properties_service.rb:45-58. Lago's description is the
		// literal "string" (line 168), a placeholder copied from HubSpot's
		// API reference; Tanvo describes the object as it does invoices
		// (docs/parity.md §4).
		integration.CRMObjectSubscriptions: {
			object: &objectSchema{
				Name: subscriptionsObject, Description: "Subscriptions managed by Tanvo billing engine",
				RequiredProperties:         []string{"tanvo_subscription_id"},
				Labels:                     map[string]string{"singular": "TanvoSubscription", "plural": "TanvoSubscriptions"},
				PrimaryDisplayProperty:     "tanvo_subscription_id",
				SecondaryDisplayProperties: []string{"tanvo_external_subscription_id"},
				SearchableProperties:       []string{"tanvo_subscription_id", "tanvo_external_subscription_id"},
				Properties: []property{
					unique(searchable(text("tanvo_subscription_id", "Tanvo Subscription Id"))),
					searchable(text("tanvo_external_subscription_id", "Tanvo External Subscription Id")),
					text("tanvo_subscription_name", "Tanvo Subscription Name"),
					text("tanvo_subscription_plan_code", "Tanvo Subscription Plan Code"),
					text("tanvo_subscription_status", "Tanvo Subscription Status"),
					date("tanvo_subscription_created_at", "Tanvo Subscription Created At"),
					date("tanvo_subscription_started_at", "Tanvo Subscription Started At"),
					date("tanvo_subscription_ending_at", "Tanvo Subscription Ending At"),
					date("tanvo_subscription_at", "Tanvo Subscription At"),
					date("tanvo_subscription_terminated_at", "Tanvo Subscription Terminated At"),
					date("tanvo_subscription_trial_ended_at", "Tanvo Subscription Trial Ended At"),
					{
						Name: "tanvo_billing_time", Label: "Tanvo Billing Time", Type: "enumeration", FieldType: "radio",
						DisplayOrder: ptr(-1), HasUniqueValue: ptr(false), SearchableInGlobalSearch: true, FormField: true,
						Options: []option{
							{Label: "Calendar", Value: "calendar", DisplayOrder: 1},
							{Label: "Anniversary", Value: "anniversary", DisplayOrder: 2},
						},
					},
				},
				AssociatedObjects: []string{"COMPANY", "CONTACT"},
			},
			typeKey: KeySubscriptionsObjectTypeID, objectVersionKey: KeySubscriptionsObjectVersion, objectVersion: 1,
			properties: []property{{
				GroupName: "tanvosubscriptions_information", Name: "tanvo_subscription_link",
				Label: "Tanvo Subscription Link", Type: "string", FieldType: "text",
			}},
			propertiesVersionKey: KeySubscriptionsPropertiesVersion, propertiesVersion: 1,
		},
		integration.CRMObjectCompanies: {
			objectType: objectTypeCompanies, properties: customerProperties("companyinformation", true),
			propertiesVersionKey: KeyCompaniesPropertiesVersion, propertiesVersion: 1,
		},
		integration.CRMObjectContacts: {
			objectType: objectTypeContacts, properties: customerProperties("contactinformation", false),
			propertiesVersionKey: KeyContactsPropertiesVersion, propertiesVersion: 1,
		},
	}
}

// Deploy implements [integration.CRMGateway]. For [integration.CRMSchemaObject]
// it finds the custom object of object by name, else creates it, when its
// type ID is unknown or its deployed version older (lago
// hubspot/*/deploy_object_service.rb); for [integration.CRMSchemaProperties]
// it creates the object's extra properties when their deployed version is
// older (deploy_properties_service.rb). Properties that already exist count
// as created: HubSpot answers a conflict where lago's proxy answered success.
// The object and its properties keep separate versions, where lago shares
// one between them (see docs/parity.md §4).
func (g *Gateway) Deploy(ctx context.Context, conn integration.Connection, object integration.CRMObject, parts integration.CRMSchemaPart) (integration.ConnectionUpdate, error) {
	s, ok := g.schemas[object]
	if !ok {
		return integration.ConnectionUpdate{}, fmt.Errorf("hubspot deploy %s: %w", object, integration.ErrUnsupported)
	}
	settings := map[string]string{}
	objectType := s.objectType
	if s.object != nil {
		objectType = conn.Setting(s.typeKey)
		if parts&integration.CRMSchemaObject != 0 && (objectType == "" || conn.Setting(s.objectVersionKey) != strconv.Itoa(s.objectVersion)) {
			id, err := g.findObject(ctx, conn, s.object.Name)
			if err == nil && id == "" {
				id, err = g.createObject(ctx, conn, s.object)
			}
			if err != nil {
				return integration.ConnectionUpdate{}, err
			}
			objectType = id
			settings[s.typeKey], settings[s.objectVersionKey] = id, strconv.Itoa(s.objectVersion)
		}
	}
	if parts&integration.CRMSchemaProperties != 0 && len(s.properties) > 0 && conn.Setting(s.propertiesVersionKey) != strconv.Itoa(s.propertiesVersion) {
		if err := g.createProperties(ctx, conn, objectType, s.properties); err != nil {
			return integration.ConnectionUpdate{Settings: settings}, err
		}
		settings[s.propertiesVersionKey] = strconv.Itoa(s.propertiesVersion)
	}
	if len(settings) == 0 {
		return integration.ConnectionUpdate{}, nil
	}
	return integration.ConnectionUpdate{Settings: settings}, nil
}

// objectInfo is an entry of the schemas API.
type objectInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ObjectTypeID string `json:"objectTypeId"`
}

// findObject returns the type ID of the custom object name; "" when the
// portal has none (lago Integrations::Aggregator::CustomObjectService).
func (g *Gateway) findObject(ctx context.Context, conn integration.Connection, name string) (string, error) {
	resp, err := g.call(ctx, conn, "custom_object", http.MethodGet, "/crm/v3/schemas", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Results []objectInfo `json:"results"`
	}
	if err := resp.Decode(&out); err != nil {
		return "", fmt.Errorf("hubspot custom_object: %w", err)
	}
	for _, o := range out.Results {
		if o.Name == name {
			return o.ObjectTypeID, nil
		}
	}
	return "", nil
}

// createObject creates the custom object s and returns its type ID.
func (g *Gateway) createObject(ctx context.Context, conn integration.Connection, s *objectSchema) (string, error) {
	resp, err := g.call(ctx, conn, "deploy_object", http.MethodPost, "/crm/v3/schemas", s)
	if err != nil {
		return "", err
	}
	var out objectInfo
	if err := resp.Decode(&out); err != nil || out.ObjectTypeID == "" {
		return "", fmt.Errorf("hubspot deploy_object: no object type id in %s: %w", resp.Snippet(), errors.Join(err, errNoID))
	}
	return out.ObjectTypeID, nil
}

// errNoID is an answer without the created record's ID.
var errNoID = &integration.ProviderError{Provider: Provider, Kind: integration.KindPermanent, Code: codeUnexpected, Message: "no id in answer"}

// batchErrors is the answer of a batch call, with the errors of the
// inputs that failed.
type batchErrors struct {
	Errors []struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	} `json:"errors"`
}

// createProperties creates ps on objectType; properties that exist are
// kept (lago posts them again when their version changes).
func (g *Gateway) createProperties(ctx context.Context, conn integration.Connection, objectType string, ps []property) error {
	if objectType == "" {
		// The custom object is not deployed: lago posts a null objectType,
		// which its proxy refuses (an integration_error).
		return fmt.Errorf("hubspot deploy_properties: %w", &integration.ProviderError{
			Provider: Provider, Kind: integration.KindInvalidRequest, Code: codeNotDeployed, Message: "The custom object is not deployed.",
		})
	}
	resp, err := g.call(ctx, conn, "deploy_properties", http.MethodPost,
		"/crm/v3/properties/"+url.PathEscape(objectType)+"/batch/create", map[string]any{"inputs": ps})
	if pe, ok := integration.AsProviderError(err); ok && (pe.HTTPStatus == http.StatusConflict || pe.Code == codeAlreadyExists) {
		return nil
	}
	if err != nil {
		return err
	}
	var out batchErrors
	_ = json.Unmarshal(resp.Body, &out)
	for _, e := range out.Errors {
		if e.Category != codeAlreadyExists {
			return fmt.Errorf("hubspot deploy_properties: %w", &integration.ProviderError{
				Provider: Provider, Kind: integration.KindInvalidRequest, Code: e.Category, Message: e.Message, HTTPStatus: resp.Status,
			})
		}
	}
	return nil
}
