package integration

import (
	"errors"
	"slices"
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
		{"second tax integration", &IntegrationCustomer{ID: id(3), CustomerID: cus, Category: CategoryTax}, CodeTaxCustomerExists},
		{"updating the tax link itself", tax, ""},
		{"tax integration of another customer", &IntegrationCustomer{ID: id(3), CustomerID: other, Category: CategoryTax}, ""},
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
			}
			if got != tt.want || (err != nil) != (tt.want != "") {
				t.Errorf("CheckCustomerLinks() = %v, want = %q", err, tt.want)
			}
			if err != nil && !errors.Is(err, core.ErrNotAllowed) {
				t.Errorf("errors.Is(%v, ErrNotAllowed) = false, want = true", err)
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
