package customer

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

var (
	testOrg    = core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000001")
	testEntity = core.MustParseID[core.BillingEntityID]("0190a0a0-0000-7000-8000-000000000002")
	testID     = core.MustParseID[core.CustomerID]("0190a0a0-0000-7000-8000-000000000003")
	sectionA   = core.MustParseID[core.InvoiceCustomSectionID]("0190a0a0-0000-7000-8000-00000000000a")
	sectionB   = core.MustParseID[core.InvoiceCustomSectionID]("0190a0a0-0000-7000-8000-00000000000b")
	sectionSys = core.MustParseID[core.InvoiceCustomSectionID]("0190a0a0-0000-7000-8000-00000000000c")
)

func newTestCustomer() *Customer { return New(testOrg, testID, "cust-1", testEntity, testNow) }

func ptr[T any](v T) *T { return &v }

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func TestValidate(t *testing.T) {
	type tc struct {
		Name                  string     `yaml:"name"`
		Source                string     `yaml:"source"`
		ExternalID            *string    `yaml:"external_id"`
		DocumentLocale        *string    `yaml:"document_locale"`
		CustomerType          string     `yaml:"customer_type"`
		AccountType           string     `yaml:"account_type"`
		NameLength            int        `yaml:"name_length"`
		Email                 *string    `yaml:"email"`
		Country               string     `yaml:"country"`
		ShippingCountry       string     `yaml:"shipping_country"`
		IssuingDateAnchor     string     `yaml:"issuing_date_anchor"`
		IssuingDateAdjustment string     `yaml:"issuing_date_adjustment"`
		GracePeriodDays       *int       `yaml:"grace_period_days"`
		NetPaymentTermDays    *int       `yaml:"net_payment_term_days"`
		Metadata              [][]string `yaml:"metadata"`
		Errors                []string   `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "customer.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cu := newTestCustomer()
			if c.ExternalID != nil {
				cu.ExternalID = *c.ExternalID
			}
			cu.DocumentLocale = c.DocumentLocale
			cu.CustomerType = Type(c.CustomerType)
			if c.AccountType != "" {
				cu.AccountType = AccountType(c.AccountType)
			}
			if c.NameLength > 0 {
				cu.Name = ptr(strings.Repeat("a", c.NameLength))
			}
			cu.Email = c.Email
			cu.BillingAddress.Country = c.Country
			cu.ShippingAddress.Country = c.ShippingCountry
			cu.IssuingDateAnchor = core.IssuingDateAnchor(c.IssuingDateAnchor)
			cu.IssuingDateAdjustment = core.IssuingDateAdjustment(c.IssuingDateAdjustment)
			cu.InvoiceGracePeriodDays = c.GracePeriodDays
			cu.NetPaymentTermDays = c.NetPaymentTermDays
			for _, kv := range c.Metadata {
				v := kv[1]
				if n, ok := strings.CutPrefix(v, "a*"); ok {
					count, err := strconv.Atoi(n)
					if err != nil {
						t.Fatal(err)
					}
					v = strings.Repeat("a", count)
				}
				cu.Metadata = append(cu.Metadata, core.MetadataItem{Key: kv[0], Value: v})
			}
			if got := fieldErrors(cu.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	type tc struct {
		Name            string   `yaml:"name"`
		Source          string   `yaml:"source"`
		Fields          []string `yaml:"fields"`
		PreferLegalName bool     `yaml:"prefer_legal_name"`
		Want            string   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "display_name.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cu := newTestCustomer()
			for _, f := range c.Fields {
				switch f {
				case "name":
					cu.Name = ptr("ACME Inc")
				case "legal_name":
					cu.LegalName = ptr("ACME International Corporation")
				case "firstname":
					cu.Firstname = ptr("Thomas")
				case "lastname":
					cu.Lastname = ptr("Anderson")
				default:
					t.Fatalf("unknown field %q", f)
				}
			}
			if got := cu.DisplayName(c.PreferLegalName); got != c.Want {
				t.Errorf("DisplayName(%v) got = %q, want = %q (%s)", c.PreferLegalName, got, c.Want, c.Source)
			}
		})
	}
}

func TestApplicableSettings(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	london, _ := time.LoadLocation("Europe/London")
	tests := []struct {
		name, source string
		customer     func(*Customer)
		defaults     Defaults
		check        func(*Customer, Defaults) (got, want any)
	}{
		{
			"customer timezone", "spec/models/customer_spec.rb:811", func(c *Customer) { c.Timezone = paris },
			Defaults{Timezone: london},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableTimezone(d).String(), "Europe/Paris" },
		},
		{
			"billing entity timezone", "spec/models/customer_spec.rb:820", nil,
			Defaults{Timezone: london},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableTimezone(d).String(), "Europe/London" },
		},
		{
			"default timezone", "spec/models/customer_spec.rb:832", nil,
			Defaults{},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableTimezone(d).String(), "UTC" },
		},
		{
			"customer grace period", "spec/models/customer_spec.rb:842", func(c *Customer) { c.InvoiceGracePeriodDays = ptr(3) },
			Defaults{InvoiceGracePeriodDays: 5},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableGracePeriodDays(d), 3 },
		},
		{
			"billing entity grace period", "spec/models/customer_spec.rb:851", nil,
			Defaults{InvoiceGracePeriodDays: 5},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableGracePeriodDays(d), 5 },
		},
		{
			"default grace period", "spec/models/customer_spec.rb:863", nil,
			Defaults{},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableGracePeriodDays(d), 0 },
		},
		{
			"customer anchor", "spec/models/customer_spec.rb:878", func(c *Customer) { c.IssuingDateAnchor = core.IssuingDateAnchorCurrentPeriodEnd },
			Defaults{IssuingDateAnchor: core.IssuingDateAnchorNextPeriodStart},
			func(c *Customer, d Defaults) (any, any) {
				return c.ApplicableIssuingDateAnchor(d), core.IssuingDateAnchorCurrentPeriodEnd
			},
		},
		{
			"billing entity anchor", "spec/models/customer_spec.rb:886", nil,
			Defaults{IssuingDateAnchor: core.IssuingDateAnchorNextPeriodStart},
			func(c *Customer, d Defaults) (any, any) {
				return c.ApplicableIssuingDateAnchor(d), core.IssuingDateAnchorNextPeriodStart
			},
		},
		{
			"customer adjustment", "spec/models/customer_spec.rb:900", func(c *Customer) { c.IssuingDateAdjustment = core.IssuingDateAdjustmentKeepAnchor },
			Defaults{IssuingDateAdjustment: core.IssuingDateAdjustmentAlignWithFinalizationDate},
			func(c *Customer, d Defaults) (any, any) {
				return c.ApplicableIssuingDateAdjustment(d), core.IssuingDateAdjustmentKeepAnchor
			},
		},
		{
			"billing entity adjustment", "spec/models/customer_spec.rb:908", nil,
			Defaults{IssuingDateAdjustment: core.IssuingDateAdjustmentAlignWithFinalizationDate},
			func(c *Customer, d Defaults) (any, any) {
				return c.ApplicableIssuingDateAdjustment(d), core.IssuingDateAdjustmentAlignWithFinalizationDate
			},
		},
		{
			"customer net payment term", "spec/models/customer_spec.rb:917", func(c *Customer) { c.NetPaymentTermDays = ptr(15) },
			Defaults{NetPaymentTermDays: 30},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableNetPaymentTermDays(d), 15 },
		},
		{
			"billing entity net payment term", "spec/models/customer_spec.rb:928", nil,
			Defaults{NetPaymentTermDays: 30},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableNetPaymentTermDays(d), 30 },
		},
		{
			"customer document locale", "spec/models/customer_spec.rb:645", func(c *Customer) { c.DocumentLocale = ptr("en") },
			Defaults{DocumentLocale: "fr"},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableDocumentLocale(d), "en" },
		},
		{
			"billing entity document locale", "spec/models/customer_spec.rb:660", nil,
			Defaults{DocumentLocale: "fr"},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableDocumentLocale(d), "fr" },
		},
		{
			"finalize zero amount inherits", "tanvo", nil,
			Defaults{FinalizeZeroAmountInvoice: true},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableFinalizeZeroAmountInvoice(d), true },
		},
		{
			"finalize zero amount skip", "tanvo", func(c *Customer) { c.FinalizeZeroAmountInvoice = ZeroAmountFinalizationSkip },
			Defaults{FinalizeZeroAmountInvoice: true},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableFinalizeZeroAmountInvoice(d), false },
		},
		{
			"finalize zero amount finalize", "tanvo", func(c *Customer) { c.FinalizeZeroAmountInvoice = ZeroAmountFinalizationFinalize },
			Defaults{},
			func(c *Customer, d Defaults) (any, any) { return c.ApplicableFinalizeZeroAmountInvoice(d), true },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCustomer()
			if tt.customer != nil {
				tt.customer(c)
			}
			if got, want := tt.check(c, tt.defaults); got != want {
				t.Errorf("got = %v, want = %v (%s)", got, want, tt.source)
			}
		})
	}
}

func TestInvoiceCustomSections(t *testing.T) {
	ids := []core.InvoiceCustomSectionID(nil)
	tests := []struct {
		name, source string
		skip         bool
		customer     []core.InvoiceCustomSectionID
		system       []core.InvoiceCustomSectionID
		entity       []core.InvoiceCustomSectionID
		configurable []core.InvoiceCustomSectionID
		applicable   []core.InvoiceCustomSectionID
	}{
		{
			"skipped with system sections", "spec/models/customer_spec.rb:982", true, ids,
			[]core.InvoiceCustomSectionID{sectionSys},
			[]core.InvoiceCustomSectionID{sectionB},
			ids,
			[]core.InvoiceCustomSectionID{sectionSys},
		},
		{
			"customer manual and system", "spec/models/customer_spec.rb:993", false,
			[]core.InvoiceCustomSectionID{sectionA},
			[]core.InvoiceCustomSectionID{sectionSys},
			ids,
			[]core.InvoiceCustomSectionID{sectionA},
			[]core.InvoiceCustomSectionID{sectionA, sectionSys},
		},
		{
			"entity manual and customer system", "spec/models/customer_spec.rb:1004", false, ids,
			[]core.InvoiceCustomSectionID{sectionSys},
			[]core.InvoiceCustomSectionID{sectionB},
			[]core.InvoiceCustomSectionID{sectionB},
			[]core.InvoiceCustomSectionID{sectionB, sectionSys},
		},
		{
			"only entity manual", "spec/models/customer_spec.rb:1015", false, ids, ids,
			[]core.InvoiceCustomSectionID{sectionB},
			[]core.InvoiceCustomSectionID{sectionB},
			[]core.InvoiceCustomSectionID{sectionB},
		},
		{
			"only system", "spec/models/customer_spec.rb:1025", false, ids,
			[]core.InvoiceCustomSectionID{sectionSys},
			ids,
			ids,
			[]core.InvoiceCustomSectionID{sectionSys},
		},
		{"none", "spec/models/customer_spec.rb:1035", false, ids, ids, ids, ids, ids},
		{"configurable skipped", "spec/models/customer_spec.rb:1053", true, ids, ids, []core.InvoiceCustomSectionID{sectionA}, ids, ids},
		{
			"configurable from customer", "spec/models/customer_spec.rb:1064", false,
			[]core.InvoiceCustomSectionID{sectionB},
			ids,
			[]core.InvoiceCustomSectionID{sectionA},
			[]core.InvoiceCustomSectionID{sectionB},
			[]core.InvoiceCustomSectionID{sectionB},
		},
		{
			"configurable from entity", "spec/models/customer_spec.rb:1074", false, ids, ids,
			[]core.InvoiceCustomSectionID{sectionA},
			[]core.InvoiceCustomSectionID{sectionA},
			[]core.InvoiceCustomSectionID{sectionA},
		},
		{"configurable none", "spec/models/customer_spec.rb:1084", false, ids, ids, ids, ids, ids},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCustomer()
			c.SkipInvoiceCustomSections = tt.skip
			c.InvoiceCustomSectionIDs = tt.customer
			c.SystemInvoiceCustomSectionIDs = tt.system
			d := Defaults{InvoiceCustomSectionIDs: tt.entity}
			if got := c.ConfigurableInvoiceCustomSections(d); !slices.Equal(got, tt.configurable) {
				t.Errorf("ConfigurableInvoiceCustomSections() got = %v, want = %v (%s)", got, tt.configurable, tt.source)
			}
			if got := c.ApplicableInvoiceCustomSections(d); !slices.Equal(got, tt.applicable) {
				t.Errorf("ApplicableInvoiceCustomSections() got = %v, want = %v (%s)", got, tt.applicable, tt.source)
			}
		})
	}
}

func TestEditable(t *testing.T) {
	tests := []struct {
		name, source string
		docs         Documents
		want         bool
	}{
		{"with a wallet", "spec/models/customer_spec.rb:675", Documents{HasWallets: true}, false},
		{"with an applied coupon", "spec/models/customer_spec.rb:684", Documents{HasFixedAmountAppliedCoupons: true}, false},
		// Only applied coupons with an amount currency count (customer.rb:281).
		{"with an applied percentage coupon only", "tanvo", Documents{}, true},
		{"with an invoice", "spec/models/customer_spec.rb:702", Documents{HasInvoices: true}, false},
		{"with a subscription", "spec/models/customer_spec.rb:711", Documents{HasSubscriptions: true}, false},
		{"without documents", "spec/models/customer_spec.rb:720", Documents{}, true},
	}
	for _, tt := range tests {
		if got := Editable(tt.docs); got != tt.want {
			t.Errorf("%s: Editable() got = %v, want = %v (%s)", tt.name, got, tt.want, tt.source)
		}
	}
}

func TestCheckUpdate(t *testing.T) {
	usd, eur := money.MustCurrency("USD"), money.MustCurrency("EUR")
	locked := Documents{HasInvoices: true}
	tests := []struct {
		name   string
		source string
		docs   Documents
		change func(old, next *Customer)
		want   []string
	}{
		{"editable external id change", "spec/services/customers/update_service_spec.rb", Documents{}, func(_, n *Customer) { n.ExternalID = "other" }, []string{}},
		{"locked external id change", "spec/services/customers/update_service_spec.rb", locked, func(_, n *Customer) { n.ExternalID = "other" }, []string{"external_id: customer_not_editable"}},
		{"locked account type change", "tanvo", locked, func(_, n *Customer) { n.AccountType = AccountTypePartner }, []string{"account_type: customer_not_editable"}},
		{"set currency first time", "spec/services/customers/update_currency_service_spec.rb:59", locked, func(o, n *Customer) { o.Currency = money.Currency{}; n.Currency = eur }, []string{}},
		{"currency change with documents", "spec/services/customers/update_currency_service_spec.rb:84", locked, func(o, n *Customer) { o.Currency = usd; n.Currency = eur }, []string{}},
		{"currency change without documents", "spec/services/customers/update_currency_service_spec.rb:84", Documents{}, func(o, n *Customer) { o.Currency = usd; n.Currency = eur }, []string{}},
		{"currency unset", "tanvo", locked, func(o, n *Customer) { o.Currency = usd; n.Currency = money.Currency{} }, []string{}},
		{"name change while locked", "tanvo", locked, func(_, n *Customer) { n.Name = ptr("New") }, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old, next := newTestCustomer(), newTestCustomer()
			tt.change(old, next)
			if got := fieldErrors(CheckUpdate(old, next, tt.docs)); !slices.Equal(got, tt.want) {
				t.Errorf("CheckUpdate() errors got = %v, want = %v (%s)", got, tt.want, tt.source)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	// spec/models/customer_spec.rb:1115 and :1131.
	for _, tt := range []struct {
		prefix string
		seq    int64
		want   string
	}{{"LAG-ABCD", 1, "LAG-ABCD-001"}, {"ORG-55", 6, "ORG-55-006"}, {"ORG", 1234, "ORG-1234"}} {
		c := newTestCustomer()
		c.AssignSequence(tt.seq, tt.prefix)
		if c.SequentialID != tt.seq || c.Slug != tt.want {
			t.Errorf("AssignSequence(%d, %q) got = (%d, %q), want = (%d, %q)", tt.seq, tt.prefix, c.SequentialID, c.Slug, tt.seq, tt.want)
		}
	}
}

func TestAddresses(t *testing.T) {
	billing := core.Address{Line1: "Billing 1", Line2: "Billing 2", City: "Billing City", Zipcode: "11111", State: "Billing State", Country: "FR"}
	shipping := core.Address{Line1: "Ship 1", Line2: "Ship 2", City: "Ship City", Zipcode: "22222", State: "Ship State", Country: "DE"}
	tests := []struct {
		name, source      string
		billing, shipping core.Address
		same, empty       bool
		effective         core.Address
	}{
		{"different shipping", "spec/models/customer_spec.rb:1153", billing, shipping, false, false, shipping},
		{"same shipping", "spec/models/customer_spec.rb:1161", billing, billing, true, false, billing},
		{"no shipping", "spec/models/customer_spec.rb:1170", billing, core.Address{}, true, false, billing},
		{"shipping without billing", "spec/models/customer_spec.rb:1191", core.Address{}, shipping, false, false, shipping},
		{"no addresses", "spec/models/customer_spec.rb:1220", core.Address{}, core.Address{}, true, true, core.Address{}},
		{
			"partial shipping", "spec/models/customer_spec.rb:1296", billing,
			core.Address{Line1: "Shipping 1", City: "Shipping City"},
			false, false,
			core.Address{Line1: "Shipping 1", Line2: "Billing 2", City: "Shipping City", Zipcode: "11111", State: "Billing State", Country: "FR"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCustomer()
			c.BillingAddress, c.ShippingAddress = tt.billing, tt.shipping
			if got := c.SameBillingAndShippingAddress(); got != tt.same {
				t.Errorf("SameBillingAndShippingAddress() got = %v, want = %v (%s)", got, tt.same, tt.source)
			}
			if got := c.EmptyBillingAndShippingAddress(); got != tt.empty {
				t.Errorf("EmptyBillingAndShippingAddress() got = %v, want = %v (%s)", got, tt.empty, tt.source)
			}
			if got := c.EffectiveShippingAddress(); got != tt.effective {
				t.Errorf("EffectiveShippingAddress() got = %+v, want = %+v (%s)", got, tt.effective, tt.source)
			}
		})
	}
}

func TestArchive(t *testing.T) {
	c := newTestCustomer()
	if err := c.Archive(testNow); err != nil || !c.Deleted() {
		t.Fatalf("Archive() got = (%v, deleted %v), want = (nil, deleted true)", err, c.Deleted())
	}
	if err := c.Archive(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("second Archive() got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}
