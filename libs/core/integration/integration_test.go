package integration

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func ptr[T any](v T) *T { return &v }

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func TestIntegration(t *testing.T) {
	type tc struct {
		Name            string   `yaml:"name"`
		Source          string   `yaml:"source"`
		Code            string   `yaml:"code"`
		IntegrationName string   `yaml:"integration_name"`
		Provider        string   `yaml:"provider"`
		Category        string   `yaml:"category"`
		WantCategory    string   `yaml:"want_category"`
		Sync            bool     `yaml:"sync"`
		Errors          []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "integration.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			got, ok := CategoryOf(Provider(c.Provider))
			if string(got) != c.WantCategory || ok != (c.WantCategory != "") {
				t.Errorf("CategoryOf() = %q, %v, want = %q", got, ok, c.WantCategory)
			}
			i := &Integration{
				Code: c.Code, Name: c.IntegrationName,
				Provider: Provider(c.Provider), Category: Category(c.Category),
				SyncInvoices: c.Sync,
			}
			if errs := fieldErrors(i.Validate()); !slices.Equal(errs, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", errs, c.Errors)
			}
		})
	}
}

func TestSettings(t *testing.T) {
	type tc struct {
		Name   string `yaml:"name"`
		Source string `yaml:"source"`
		Key    string `yaml:"key"`
		Want   string `yaml:"want"`
		Found  bool   `yaml:"found"`
	}
	for _, c := range spectest.Load[tc](t, "settings.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			i := &Integration{}
			if err := i.SetSetting("key1", "val1"); err != nil {
				t.Fatal(err)
			}
			got, ok := i.Setting(c.Key)
			if got != c.Want || ok != c.Found {
				t.Errorf("Setting(%q) = %q, %v, want = %q, %v", c.Key, got, ok, c.Want, c.Found)
			}
		})
	}
}

func TestSetSettingKeepsOtherKeys(t *testing.T) {
	c := &IntegrationCustomer{Settings: []byte(`{"a":"1"}`)}
	if err := c.SetSetting("b", "2"); err != nil {
		t.Fatal(err)
	}
	a, _ := c.Setting("a")
	b, _ := c.Setting("b")
	if a != "1" || b != "2" {
		t.Errorf("settings = %s, want = a=1 and b=2", c.Settings)
	}
}

func TestCustomerValidate(t *testing.T) {
	ok := IntegrationCustomer{
		IntegrationID: core.MustParseID[core.IntegrationID]("0190a000-0000-7000-8000-000000000001"),
		CustomerID:    core.MustParseID[core.CustomerID]("0190a000-0000-7000-8000-000000000002"),
		Category:      CategoryAccounting, ExternalCustomerID: "cus_1",
	}
	tests := []struct {
		name string
		edit func(*IntegrationCustomer)
		want []string
	}{
		{"valid", func(*IntegrationCustomer) {}, []string{}},
		{"sync creates the external customer", func(c *IntegrationCustomer) { c.ExternalCustomerID, c.SyncWithProvider = "", true }, []string{}},
		{"external id required without sync", func(c *IntegrationCustomer) { c.ExternalCustomerID = "" }, []string{"external_customer_id: value_is_mandatory"}},
		{"payment customers live in the payment package", func(c *IntegrationCustomer) { c.Category = CategoryPayment }, []string{"category: value_not_allowed"}},
		{"auth has no customers", func(c *IntegrationCustomer) { c.Category = CategoryAuth }, []string{"category: value_not_allowed"}},
		{"settings must be an object", func(c *IntegrationCustomer) { c.Settings = []byte(`[]`) }, []string{"settings: invalid_format"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ok
			tt.edit(&c)
			if got := fieldErrors(c.Validate()); !slices.Equal(got, tt.want) {
				t.Errorf("Validate() = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestCheckCustomerLinks(t *testing.T) {
	cus := core.MustParseID[core.CustomerID]("0190a000-0000-7000-8000-000000000002")
	other := core.MustParseID[core.CustomerID]("0190a000-0000-7000-8000-000000000003")
	id := func(n byte) core.IntegrationCustomerID {
		return core.MustParseID[core.IntegrationCustomerID](uuid.UUID{15: n}.String())
	}
	tax := &IntegrationCustomer{ID: id(1), CustomerID: cus, Category: CategoryTax}
	crmDefault := &IntegrationCustomer{ID: id(2), CustomerID: cus, Category: CategoryCRM, IsDefault: true}
	existing := []*IntegrationCustomer{tax, crmDefault}
	tests := []struct {
		name      string
		candidate *IntegrationCustomer
		want      string
	}{
		// source: spec/models/integration_customers/base_customer_spec.rb:293
		// source: spec/models/integration_customers/base_customer_spec.rb:304
		{"second tax integration", &IntegrationCustomer{ID: id(3), CustomerID: cus, Category: CategoryTax}, CodeTaxIntegrationExists},
		// source: spec/models/integration_customers/base_customer_spec.rb:315
		{"updating the tax link itself", tax, ""},
		// source: spec/models/integration_customers/base_customer_spec.rb:279
		{"first tax integration of a customer", &IntegrationCustomer{ID: id(3), CustomerID: other, Category: CategoryTax}, ""},
		{"second default of a category", &IntegrationCustomer{ID: id(3), CustomerID: cus, Category: CategoryCRM, IsDefault: true}, "default_already_exists"},
		{"non-default of a category", &IntegrationCustomer{ID: id(3), CustomerID: cus, Category: CategoryCRM}, ""},
		{"default of another category", &IntegrationCustomer{ID: id(3), CustomerID: cus, Category: CategoryAccounting, IsDefault: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCustomerLinks(tt.candidate, existing)
			var got string
			if re, ok := errors.AsType[*core.RuleError](err); ok {
				got = re.Code
				if !errors.Is(err, core.ErrNotAllowed) {
					t.Errorf("errors.Is(%v, ErrNotAllowed) = false, want = true", err)
				}
			}
			if ve, ok := errors.AsType[*core.ValidationError](err); ok && len(ve.Errors) == 1 && ve.Errors[0].Field == "integration_id" {
				got = ve.Errors[0].Code
			}
			if got != tt.want || (err != nil) != (tt.want != "") {
				t.Errorf("CheckCustomerLinks() = %v, want = %q", err, tt.want)
			}
		})
	}
}

func TestDefaultConflict(t *testing.T) {
	org := core.MustParseID[core.OrganizationID]("0190a000-0000-7000-8000-0000000000aa")
	id := func(n byte) core.IntegrationID {
		return core.MustParseID[core.IntegrationID](uuid.UUID{15: n}.String())
	}
	existing := []*Integration{{ID: id(1), OrganizationID: org, Category: CategoryAccounting, IsDefault: true}}
	tests := []struct {
		name string
		c    *Integration
		want bool
	}{
		{"second default", &Integration{ID: id(2), OrganizationID: org, Category: CategoryAccounting, IsDefault: true}, true},
		{"the default itself", existing[0], false},
		{"not default", &Integration{ID: id(2), OrganizationID: org, Category: CategoryAccounting}, false},
		{"default of another category", &Integration{ID: id(2), OrganizationID: org, Category: CategoryCRM, IsDefault: true}, false},
	}
	for _, tt := range tests {
		if got := DefaultConflict(tt.c, existing); got != tt.want {
			t.Errorf("%s: DefaultConflict() = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

func TestResolve(t *testing.T) {
	be := core.MustParseID[core.BillingEntityID]("0190a000-0000-7000-8000-0000000000be")
	metric := uuid.UUID{15: 7}
	general := &Mapping{MappableType: MappableTypeBillableMetric, MappableID: metric, ExternalID: "item-general"}
	scoped := &Mapping{MappableType: MappableTypeBillableMetric, MappableID: metric, BillingEntityID: be, ExternalID: "item-be"}
	fee := &CollectionMapping{Type: CollectionMappingTypeSubscriptionFee, ExternalID: ptr("sub-fee")}
	fallback := &CollectionMapping{Type: CollectionMappingTypeFallbackItem, ExternalID: ptr("fallback")}
	fallbackBE := &CollectionMapping{Type: CollectionMappingTypeFallbackItem, BillingEntityID: be, ExternalID: ptr("fallback-be")}
	tests := []struct {
		name string
		in   ResolveInput
		want string
	}{
		{"own mapping", ResolveInput{Mappings: []*Mapping{general}, CollectionMappings: []*CollectionMapping{fallback}, MappableType: MappableTypeBillableMetric, MappableID: metric}, "item-general"},
		{"billing entity mapping wins", ResolveInput{Mappings: []*Mapping{scoped, general}, MappableType: MappableTypeBillableMetric, MappableID: metric, BillingEntityID: be}, "item-be"},
		{"general mapping when the entity has none", ResolveInput{Mappings: []*Mapping{general}, MappableType: MappableTypeBillableMetric, MappableID: metric, BillingEntityID: be}, "item-general"},
		{"mapping of another entity is ignored", ResolveInput{Mappings: []*Mapping{scoped}, CollectionMappings: []*CollectionMapping{fallback}, MappableType: MappableTypeBillableMetric, MappableID: metric}, "fallback"},
		{"collection of the type", ResolveInput{CollectionMappings: []*CollectionMapping{fallback, fee}, Collection: CollectionMappingTypeSubscriptionFee}, "sub-fee"},
		{"fallback item", ResolveInput{CollectionMappings: []*CollectionMapping{fallback, fee}, Collection: CollectionMappingTypeCoupon}, "fallback"},
		{"billing entity fallback wins", ResolveInput{CollectionMappings: []*CollectionMapping{fallback, fallbackBE}, BillingEntityID: be}, "fallback-be"},
		{"nothing maps", ResolveInput{CollectionMappings: []*CollectionMapping{fee}, Collection: CollectionMappingTypeCoupon}, ""},
		// Order of lago app/services/integrations/aggregator/base_payload.rb:81-106; no spec asserts it.
		{"billing entity fallback before the general mapping", ResolveInput{Mappings: []*Mapping{general}, CollectionMappings: []*CollectionMapping{fallback, fallbackBE}, MappableType: MappableTypeBillableMetric, MappableID: metric, BillingEntityID: be}, "fallback-be"},
		{"general fallback last", ResolveInput{CollectionMappings: []*CollectionMapping{fallback}, MappableType: MappableTypeBillableMetric, MappableID: metric, BillingEntityID: be}, "fallback"},
		{"billing entity fallback before the general collection", ResolveInput{CollectionMappings: []*CollectionMapping{fee, fallbackBE}, Collection: CollectionMappingTypeSubscriptionFee, BillingEntityID: be}, "fallback-be"},
		{"tax takes no fallback", ResolveInput{CollectionMappings: []*CollectionMapping{fallback}, Collection: CollectionMappingTypeTax}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.in)
			if tt.want == "" {
				if !errors.Is(err, ErrNoMapping) || !errors.Is(err, core.ErrNotFound) {
					t.Errorf("Resolve() error = %v, want = ErrNoMapping", err)
				}
				return
			}
			if err != nil || got.ExternalID != tt.want {
				t.Errorf("Resolve() = %q, %v, want = %q", got.ExternalID, err, tt.want)
			}
		})
	}
}

func TestItemValidate(t *testing.T) {
	integ := core.MustParseID[core.IntegrationID]("0190a000-0000-7000-8000-000000000001")
	for _, tt := range []struct {
		name   string
		source string
		item   Item
		want   []string
	}{
		{"valid", "", Item{IntegrationID: integ, ItemType: ItemTypeTax, ExternalID: "1"}, []string{}},
		{"no external id", "source: spec/models/integration_item_spec.rb:12", Item{IntegrationID: integ, ItemType: ItemTypeStandard}, []string{"external_id: value_is_mandatory"}},
		{"unknown item type", "source: spec/models/integration_item_spec.rb:13", Item{IntegrationID: integ, ItemType: "other", ExternalID: "1"}, []string{"item_type: invalid_value"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(tt.source)
			if got := fieldErrors(tt.item.Validate()); !slices.Equal(got, tt.want) {
				t.Errorf("Validate() = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestEnumValues(t *testing.T) {
	// source: spec/models/integration_collection_mappings/base_collection_mapping_spec.rb:18
	if got, want := CollectionMappingTypeValues(), []CollectionMappingType{
		"fallback_item", "coupon", "subscription_fee", "minimum_commitment", "tax", "prepaid_credit", "credit_note", "account", "currencies",
	}; !slices.Equal(got, want) {
		t.Errorf("collection mapping types: got = %v, want = %v", got, want)
	}
	if err := (&CollectionMapping{Type: "other"}).Validate(); !slices.Contains(fieldErrors(err), "mapping_type: invalid_value") {
		t.Errorf("unknown mapping type: got = %v, want = mapping_type invalid_value", err)
	}
	// sales_order_deprecated is left out: nothing records it.
	// source: spec/models/integration_resource_spec.rb:18
	if got, want := ResourceTypeValues(), []ResourceType{"invoice", "payment", "credit_note", "subscription"}; !slices.Equal(got, want) {
		t.Errorf("resource types: got = %v, want = %v", got, want)
	}
	// source: spec/models/integration_mappings/base_mapping_spec.rb:18
	if got, want := MappableTypeValues(), []MappableType{"add_on", "billable_metric"}; !slices.Equal(got, want) {
		t.Errorf("mappable types: got = %v, want = %v", got, want)
	}
	if err := (&Mapping{MappableType: "plan"}).Validate(); !slices.Contains(fieldErrors(err), "mappable_type: invalid_value") {
		t.Errorf("unknown mappable type: got = %v, want = mappable_type invalid_value", err)
	}
	// A connection's category is accounting, tax or CRM: payment
	// connections are provider customers.
	// source: spec/models/integration_customers/base_customer_spec.rb:19
	for cat, want := range map[Category]string{"other": "category: invalid_value", CategoryPayment: "category: value_not_allowed"} {
		if err := (&IntegrationCustomer{Category: cat}).Validate(); !slices.Contains(fieldErrors(err), want) {
			t.Errorf("category %s: got = %v, want = %s", cat, err, want)
		}
	}
	for _, cat := range []Category{CategoryAccounting, CategoryTax, CategoryCRM} {
		if err := (&IntegrationCustomer{Category: cat}).Validate(); slices.ContainsFunc(fieldErrors(err), func(s string) bool { return strings.HasPrefix(s, "category:") }) {
			t.Errorf("category %s: got = %v, want = valid", cat, err)
		}
	}
}
