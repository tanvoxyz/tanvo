package rpc_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1/customerv1connect"
)

// restCustomer is the JSON of a customer in a REST response.
type restCustomer struct {
	ID                        string `json:"id"`
	ExternalID                string `json:"external_id"`
	BillingEntityID           string `json:"billing_entity_id"`
	CustomerType              string `json:"customer_type"`
	AccountType               string `json:"account_type"`
	Name                      string `json:"name"`
	Firstname                 string `json:"firstname"`
	Lastname                  string `json:"lastname"`
	Email                     string `json:"email"`
	Currency                  string `json:"currency"`
	Timezone                  string `json:"timezone"`
	ApplicableTimezone        string `json:"applicable_timezone"`
	FinalizeZeroAmountInvoice string `json:"finalize_zero_amount_invoice"`
	ExcludeFromDunning        bool   `json:"exclude_from_dunning_campaign"`
	Metadata                  []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"metadata"`
	Slug      string `json:"slug"`
	CreatedAt string `json:"created_at"`
	DeletedAt string `json:"deleted_at"`
}

func customerDecode(t *testing.T, body []byte) restCustomer {
	t.Helper()
	return decode[struct {
		Customer restCustomer `json:"customer"`
	}](t, body).Customer
}

func customersClient(s *rpctest.Server) customerv1connect.CustomerServiceClient {
	return customerv1connect.NewCustomerServiceClient(s.Connect(s.APIKey))
}

func customerWantViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	for _, v := range rpc.ErrorViolations(err) {
		if v.Field == field && v.Reason == reason {
			return
		}
	}
	t.Fatalf("got err = %v (violations %+v), want = %s %s", err, rpc.ErrorViolations(err), field, reason)
}

func TestCreateCustomer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		check  func(t *testing.T, c restCustomer)
	}{
		{
			name:   "creates",
			source: "spec/requests/api/v1/customers_controller_spec.rb:47",
			body: `{"external_id":"cus_1","name":"Foo Bar Inc.","firstname":"Foo","lastname":"Bar",
				"customer_type":"CUSTOMER_TYPE_COMPANY","currency":"EUR","timezone":"America/New_York"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.ID == "" || c.ExternalID != "cus_1" || c.Name != "Foo Bar Inc." || c.Firstname != "Foo" || c.Lastname != "Bar" ||
					c.CustomerType != "CUSTOMER_TYPE_COMPANY" || c.Currency != "EUR" || c.CreatedAt == "" {
					t.Errorf("got = %+v, want = the request's customer", c)
				}
				if c.AccountType != "ACCOUNT_TYPE_CUSTOMER" || c.Timezone != "America/New_York" || c.ApplicableTimezone != "America/New_York" {
					t.Errorf("got = %+v, want = a customer account in America/New_York", c)
				}
			},
		},
		{
			name:   "null bytes",
			source: "spec/requests/api/v1/customers_controller_spec.rb:37",
			body:   `{"external_id":"cus_1","name":"Foo\u0000Bar","firstname":"\u0000","lastname":"\u0000","currency":"EUR"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.Name != "FooBar" || c.Firstname != "" || c.Lastname != "" {
					t.Errorf("got = %q %q %q, want = FooBar and empty names", c.Name, c.Firstname, c.Lastname)
				}
			},
		},
		{
			name:   "finalize zero amount invoice",
			source: "spec/requests/api/v1/customers_controller_spec.rb:90",
			body:   `{"external_id":"cus_1","finalize_zero_amount_invoice":"ZERO_AMOUNT_FINALIZATION_SKIP"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.FinalizeZeroAmountInvoice != "ZERO_AMOUNT_FINALIZATION_SKIP" {
					t.Errorf("got = %q, want = skip", c.FinalizeZeroAmountInvoice)
				}
			},
		},
		{
			name:   "partner account",
			source: "spec/requests/api/v1/customers_controller_spec.rb:230",
			body:   `{"external_id":"cus_1","account_type":"ACCOUNT_TYPE_PARTNER"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.AccountType != "ACCOUNT_TYPE_PARTNER" || !c.ExcludeFromDunning {
					t.Errorf("got = %q exclude %v, want = a partner excluded from dunning", c.AccountType, c.ExcludeFromDunning)
				}
			},
		},
		{
			name:   "metadata",
			source: "spec/requests/api/v1/customers_controller_spec.rb:287",
			body:   `{"external_id":"cus_1","metadata":[{"key":"Hello","value":"Hi"}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if len(c.Metadata) != 1 || c.Metadata[0].Key != "Hello" || c.Metadata[0].Value != "Hi" {
					t.Errorf("got = %+v, want = Hello: Hi", c.Metadata)
				}
			},
		},
		{
			name:   "invisible characters in email",
			source: "spec/requests/api/v1/customers_controller_spec.rb:317",
			body:   `{"external_id":"cus_1","email":"foo\u200b@bar.com"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.Email != "foo@bar.com" {
					t.Errorf("got = %q, want = foo@bar.com", c.Email)
				}
			},
		},
		{
			name:   "unicode local part",
			source: "spec/requests/api/v1/customers_controller_spec.rb:353",
			body:   `{"external_id":"cus_1","email":"joão.silva@example.com"}`,
			status: http.StatusOK,
			check: func(t *testing.T, c restCustomer) {
				if c.Email != "joão.silva@example.com" {
					t.Errorf("got = %q, want = joão.silva@example.com", c.Email)
				}
			},
		},
		{
			name:   "malformed email",
			source: "spec/requests/api/v1/customers_controller_spec.rb:380",
			body:   `{"external_id":"cus_1","email":"joão.silva@"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid currency",
			source: "spec/requests/api/v1/customers_controller_spec.rb:416",
			body:   `{"external_id":"cus_1","currency":"invalid"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "missing external id",
			source: "spec/requests/api/v1/customers_controller_spec.rb:416",
			body:   `{"name":"Foo Bar"}`,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/customers", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, customerDecode(t, body))
			}
		})
	}
}

func TestCreateCustomerRelations(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := customersClient(s)
	vat := h.Tax("vat", "20")
	var section core.InvoiceCustomSectionID
	h.Do(func(ctx context.Context, r port.Repos) error {
		ics := &invoice.CustomSection{ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: "eu", Name: "EU", SectionType: invoice.CustomSectionTypeManual, CreatedAt: h.Now(), UpdatedAt: h.Now()}
		section = ics.ID
		return r.InvoiceCustomSections.Create(ctx, ics)
	})
	other := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", h.Now())
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, other) })

	t.Run("billing entity, taxes and sections", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:484")
		t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:449")
		resp, err := c.CreateCustomer(t.Context(), customerv1.CreateCustomerRequest_builder{Customer: customerv1.Customer_builder{
			ExternalId: new("cus_rel"), BillingEntityId: new("code:other"),
			TaxIds: []string{"code:vat"}, InvoiceCustomSectionIds: []string{"code:eu"},
		}.Build()}.Build())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		got := resp.GetCustomer()
		if got.GetBillingEntityId() != other.ID.String() || !slices.Equal(got.GetTaxIds(), []string{vat.ID.String()}) ||
			!slices.Equal(got.GetInvoiceCustomSectionIds(), []string{section.String()}) {
			t.Errorf("got = %v, want = entity other, tax vat, section eu", got)
		}
	})
	t.Run("skip sections with sections", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:466")
		_, err := c.CreateCustomer(t.Context(), customerv1.CreateCustomerRequest_builder{Customer: customerv1.Customer_builder{
			ExternalId: new("cus_skip"), SkipInvoiceCustomSections: new(true), InvoiceCustomSectionIds: []string{"code:eu"},
		}.Build()}.Build())
		customerWantViolation(t, err, "customer.invoice_custom_section_ids", customers.CodeSkipSectionsAndSelectedIDs)
	})
	t.Run("external id taken", func(t *testing.T) {
		t.Log("source: app/services/customers/upsert_from_api_service.rb:173")
		_, err := c.CreateCustomer(t.Context(), customerv1.CreateCustomerRequest_builder{Customer: customerv1.Customer_builder{
			ExternalId: new("cus_rel"),
		}.Build()}.Build())
		customerWantViolation(t, err, "customer.external_id", core.CodeAlreadyExists)
	})
	t.Run("x402 agent address", func(t *testing.T) {
		t.Log("source: spec/models/customer_spec.rb:369")
		got, err := c.CreateCustomer(t.Context(), customerv1.CreateCustomerRequest_builder{Customer: customerv1.Customer_builder{
			ExternalId: new("cus_x402"), X402AgentAddress: new("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"),
		}.Build()}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if a := got.GetCustomer().GetX402AgentAddress(); a != "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed" {
			t.Errorf("got x402_agent_address = %q, want the checksummed address", a)
		}
		_, err = c.CreateCustomer(t.Context(), customerv1.CreateCustomerRequest_builder{Customer: customerv1.Customer_builder{
			ExternalId: new("cus_x402_bad"), X402AgentAddress: new("0xabc"),
		}.Build()}.Build())
		customerWantViolation(t, err, "customer.x402_agent_address", core.CodeInvalidFormat)
	})
}

func TestUpdateCustomer(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := customersClient(s)
	old := h.Customer(func(c *customer.Customer) { c.Email = new("old@example.com"); c.Name = new("Old") })

	t.Run("upserts an existing customer", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:363")
		status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/customers/external_id%3A"+old.ExternalID+"?allow_missing=true",
			`{"email":"joão.silva@example.com"}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := customerDecode(t, body)
		if got.ID != old.ID.String() || got.Email != "joão.silva@example.com" || got.Name != "Old" {
			t.Errorf("got = %+v, want = the same customer with the new email", got)
		}
	})
	t.Run("upserts a new customer", func(t *testing.T) {
		t.Log("source: spec/services/customers/upsert_from_api_service_spec.rb:79")
		resp, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
			Customer:     customerv1.Customer_builder{Id: new("external_id:cus_new"), Name: new("New")}.Build(),
			AllowMissing: new(true),
		}.Build())
		if err != nil || resp.GetCustomer().GetExternalId() != "cus_new" || resp.GetCustomer().GetName() != "New" {
			t.Fatalf("got = %v, %v, want = new customer cus_new", resp.GetCustomer(), err)
		}
	})
	t.Run("missing without allow_missing", func(t *testing.T) {
		_, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
			Customer: customerv1.Customer_builder{Id: new("external_id:nope"), Name: new("New")}.Build(),
		}.Build())
		wantCode(t, err, connect.CodeNotFound, "customer_not_found")
	})
	t.Run("update mask clears an override", func(t *testing.T) {
		t.Log("source: spec/services/customers/update_service_spec.rb:76")
		if _, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
			Customer: customerv1.Customer_builder{Id: new(old.ID.String()), NetPaymentTermDays: new(int32(30))}.Build(),
		}.Build()); err != nil {
			t.Fatalf("set term: %v", err)
		}
		resp, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
			Customer:   customerv1.Customer_builder{Id: new(old.ID.String())}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"net_payment_term_days", "billing_address.country"}},
		}.Build())
		if err != nil || resp.GetCustomer().HasNetPaymentTermDays() || resp.GetCustomer().GetName() != "Old" {
			t.Fatalf("got = %v, %v, want = no payment term override", resp.GetCustomer(), err)
		}
	})
	t.Run("lower-case country codes", func(t *testing.T) {
		t.Log("source: app/services/customers/create_service.rb:41,48")
		t.Log("source: app/services/customers/update_service.rb:48,66")
		t.Log("source: app/services/customers/upsert_from_api_service.rb:56,68")
		addr := func(country string) *commonv1.Address { return commonv1.Address_builder{Country: new(country)}.Build() }
		for _, id := range []string{old.ID.String(), "external_id:cus_lower"} {
			resp, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
				Customer: customerv1.Customer_builder{
					Id: new(id), BillingAddress: addr("fr"), ShippingAddress: addr("es"),
				}.Build(),
				AllowMissing: new(true),
			}.Build())
			if err != nil || resp.GetCustomer().GetBillingAddress().GetCountry() != "FR" || resp.GetCustomer().GetShippingAddress().GetCountry() != "ES" {
				t.Errorf("%s: got = %v, %v, want = countries FR and ES", id, resp.GetCustomer(), err)
			}
		}
	})
	t.Run("unknown mask path", func(t *testing.T) {
		_, err := c.UpdateCustomer(t.Context(), customerv1.UpdateCustomerRequest_builder{
			Customer:   customerv1.Customer_builder{Id: new(old.ID.String())}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"slug"}},
		}.Build())
		customerWantViolation(t, err, "update_mask", core.CodeInvalid)
	})
}

func TestGetCustomer(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	live := h.Customer()
	gone := h.Customer(func(c *customer.Customer) { c.DeletedAt = new(h.Now()) })
	tests := []struct {
		name, source, ref string
		status            int
		want              string
	}{
		{name: "by external id", source: "spec/requests/api/v1/customers_controller_spec.rb:847", ref: "external_id%3A" + live.ExternalID, status: http.StatusOK, want: live.ID.String()},
		{name: "by id", ref: live.ID.String(), status: http.StatusOK, want: live.ID.String()},
		{name: "deleted by id", ref: gone.ID.String(), status: http.StatusOK, want: gone.ID.String()},
		{name: "deleted by external id", source: "app/controllers/api/v1/customers_controller.rb:85", ref: "external_id%3A" + gone.ExternalID, status: http.StatusNotFound},
		{name: "unknown", source: "spec/requests/api/v1/customers_controller_spec.rb:859", ref: "external_id%3Anope", status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/customers/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.want != "" {
				if got := customerDecode(t, body); got.ID != tt.want {
					t.Errorf("got id = %s, want = %s", got.ID, tt.want)
				}
			}
		})
	}
}

func TestListCustomers(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := customersClient(s)
	eur, _ := money.LookupCurrency("EUR")
	company := h.Customer(func(c *customer.Customer) {
		c.CustomerType = customer.TypeCompany
		c.Name = new("Acme Corp")
		c.BillingAddress.Country, c.BillingAddress.State, c.BillingAddress.Zipcode = "FR", "IDF", "75001"
		c.Currency = eur
		c.TaxIdentificationNumber = new("FR123")
		c.Metadata = core.Metadata{{Key: "tier", Value: "gold"}}
	})
	partner := h.Customer(func(c *customer.Customer) { c.AccountType = customer.AccountTypePartner; c.Name = new("Partner") })
	plain := h.Customer(func(c *customer.Customer) { c.CreatedAt = h.Now().Add(time.Minute) })
	h.Customer(func(c *customer.Customer) { c.DeletedAt = new(h.Now()) })

	ids := func(cs []*customerv1.Customer) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.GetId()
		}
		slices.Sort(out)
		return out
	}
	want := func(cs ...*customer.Customer) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.ID.String()
		}
		slices.Sort(out)
		return out
	}
	tests := []struct {
		name, source, filter, search string
		want                         []string
		code                         connect.Code
	}{
		{name: "all", source: "spec/requests/api/v1/customers_controller_spec.rb:529", want: want(company, partner, plain)},
		{name: "account type", source: "spec/requests/api/v1/customers_controller_spec.rb:546", filter: "account_type = PARTNER", want: want(partner)},
		{name: "customer type", source: "spec/requests/api/v1/customers_controller_spec.rb:563", filter: "customer_type = COMPANY", want: want(company)},
		{name: "has customer type", source: "spec/requests/api/v1/customers_controller_spec.rb:579", filter: "has_customer_type = true", want: want(company)},
		{name: "no customer type with a customer type", source: "spec/requests/api/v1/customers_controller_spec.rb:591", filter: "has_customer_type = false AND customer_type = COMPANY", code: connect.CodeInvalidArgument},
		{name: "unknown billing entity", source: "spec/requests/api/v1/customers_controller_spec.rb:622", filter: `billing_entity_id = "code:nope"`, code: connect.CodeNotFound},
		{name: "billing entity", source: "spec/requests/api/v1/customers_controller_spec.rb:611", filter: `billing_entity_id = "code:` + h.BillingEntity.Code + `"`, want: want(company, partner, plain)},
		{name: "external id", source: "spec/requests/api/v1/customers_controller_spec.rb:659", filter: `external_id = "` + plain.ExternalID + `"`, want: want(plain)},
		{name: "countries", source: "spec/requests/api/v1/customers_controller_spec.rb:674", filter: "billing_address.country = fr OR billing_address.country = DE", want: want(company)},
		{name: "states", source: "spec/requests/api/v1/customers_controller_spec.rb:702", filter: "billing_address.state = IDF", want: want(company)},
		{name: "zipcodes", source: "spec/requests/api/v1/customers_controller_spec.rb:715", filter: `billing_address.zipcode = "75001"`, want: want(company)},
		{name: "currencies", source: "spec/requests/api/v1/customers_controller_spec.rb:728", filter: "currency = EUR", want: want(company)},
		{name: "invalid currency", source: "spec/requests/api/v1/customers_controller_spec.rb:738", filter: "currency = ABC", code: connect.CodeInvalidArgument},
		{name: "tax identification number", source: "spec/requests/api/v1/customers_controller_spec.rb:755", filter: "has_tax_identification_number = true", want: want(company)},
		{name: "no tax identification number", source: "spec/requests/api/v1/customers_controller_spec.rb:765", filter: "has_tax_identification_number = false", want: want(partner, plain)},
		{name: "metadata", source: "spec/requests/api/v1/customers_controller_spec.rb:798", filter: "metadata.tier = gold", want: want(company)},
		{name: "search", source: "spec/requests/api/v1/customers_controller_spec.rb:826", search: "acme", want: want(company)},
		{name: "deleted", filter: "deleted = true", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			resp, err := c.ListCustomers(t.Context(), customerv1.ListCustomersRequest_builder{Filter: &tt.filter, Search: &tt.search}.Build())
			if tt.code != 0 {
				wantCode(t, err, tt.code, "")
				return
			}
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			got := ids(resp.GetCustomers())
			if tt.name == "deleted" {
				if len(got) != 4 {
					t.Errorf("got = %d customers, want = 4 with the deleted one", len(got))
				}
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
	t.Run("newest first", func(t *testing.T) {
		resp, err := c.ListCustomers(t.Context(), customerv1.ListCustomersRequest_builder{}.Build())
		if err != nil || len(resp.GetCustomers()) != 3 || resp.GetCustomers()[0].GetId() != plain.ID.String() {
			t.Errorf("got = %v, %v, want = plain first", resp.GetCustomers(), err)
		}
	})
}

func TestDeleteCustomer(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	cust := h.Customer()

	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:879")
	status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/customers/external_id%3A"+cust.ExternalID, nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	if got := customerDecode(t, body); got.ID != cust.ID.String() || got.ExternalID != cust.ExternalID || got.DeletedAt == "" {
		t.Errorf("got = %+v, want = the deleted customer", got)
	}
	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:875")
	if got := h.ReloadCustomer(cust.ID); got.DeletedAt == nil {
		t.Errorf("got = live customer, want = deleted")
	}
	var jobs int
	if err := s.Admin.QueryRow(t.Context(), `SELECT count(*) FROM job_outbox WHERE kind = $1`, customers.JobTerminateRelations).Scan(&jobs); err != nil || jobs != 1 {
		t.Errorf("got %s jobs = %d (%v), want = 1", customers.JobTerminateRelations, jobs, err)
	}
	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:891")
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/customers/external_id%3A"+cust.ExternalID, nil); status != http.StatusNotFound {
		t.Errorf("got status = %d (%s), want = 404", status, body)
	}
}

func TestGeneratePortalUrl(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	c := customersClient(s)

	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:502")
	resp, err := c.GeneratePortalUrl(t.Context(), customerv1.GeneratePortalUrlRequest_builder{CustomerId: new("external_id:" + cust.ExternalID)}.Build())
	if err != nil || !strings.Contains(resp.GetPortalUrl(), "/customer-portal/") {
		t.Fatalf("got = %v, %v, want = a portal URL", resp, err)
	}
	if got, want := resp.GetExpiresAt().AsTime(), rpctest.Now.Add(12*time.Hour); !got.Equal(want) {
		t.Errorf("got expires_at = %v, want = %v", got, want)
	}
	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:512")
	_, err = c.GeneratePortalUrl(t.Context(), customerv1.GeneratePortalUrlRequest_builder{CustomerId: new("external_id:nope")}.Build())
	wantCode(t, err, connect.CodeNotFound, "customer_not_found")
}

func TestCustomerConnections(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := customersClient(s)
	for _, in := range []*integration.Integration{
		{Code: "stripe", Category: integration.CategoryPayment, Provider: integration.ProviderStripe},
		{Code: "netsuite", Category: integration.CategoryAccounting, Provider: integration.ProviderNetsuite},
	} {
		in.ID, in.OrganizationID, in.Name, in.CreatedAt, in.UpdatedAt = core.NewID[core.IntegrationID](h.IDs), h.OrgID(), in.Code, h.Now(), h.Now()
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, in) })
	}
	cust := s.Fixtures.Customer()
	ref := new("external_id:" + cust.ExternalID)

	t.Log("source: spec/services/customers/generate_checkout_url_service_spec.rb:46")
	_, err := c.GenerateCheckoutUrl(t.Context(), customerv1.GenerateCheckoutUrlRequest_builder{CustomerId: ref}.Build())
	customerWantViolation(t, err, "base", customers.CodeNoLinkedPaymentProvider)

	t.Log("source: spec/services/payment_provider_customers/create_or_update_batch_service_spec.rb:27")
	up, err := c.UpsertCustomerConnection(t.Context(), customerv1.UpsertCustomerConnectionRequest_builder{
		CustomerId: ref,
		Connection: customerv1.CustomerConnection_builder{IntegrationId: new("code:stripe"), ExternalCustomerId: new("cus_stripe")}.Build(),
	}.Build())
	if err != nil || up.GetConnection().GetExternalCustomerId() != "cus_stripe" || !up.GetConnection().GetIsDefault() ||
		up.GetConnection().GetCategory() != customerv1.IntegrationCategory_INTEGRATION_CATEGORY_PAYMENT {
		t.Fatalf("got = %v, %v, want = default stripe payment connection", up.GetConnection(), err)
	}
	t.Log("source: spec/services/integration_customers/create_connection_service_spec.rb:117")
	if _, err := c.UpsertCustomerConnection(t.Context(), customerv1.UpsertCustomerConnectionRequest_builder{
		CustomerId: ref,
		Connection: customerv1.CustomerConnection_builder{IntegrationId: new("code:netsuite"), ExternalCustomerId: new("ns_1"), SubsidiaryId: new("1")}.Build(),
	}.Build()); err != nil {
		t.Fatalf("netsuite: %v", err)
	}
	// source: spec/serializers/v1/integration_customer_serializer_spec.rb:10, 31
	list, err := c.ListCustomerConnections(t.Context(), customerv1.ListCustomerConnectionsRequest_builder{CustomerId: ref}.Build())
	if err != nil || len(list.GetConnections()) != 2 || list.GetConnections()[0].GetProvider() != string(integration.ProviderStripe) ||
		list.GetConnections()[1].GetSubsidiaryId() != "1" || !list.GetConnections()[1].GetIsDefault() {
		t.Fatalf("got = %v, %v, want = stripe then the default netsuite", list.GetConnections(), err)
	}
	if ns := list.GetConnections()[1]; ns.GetProvider() != string(integration.ProviderNetsuite) || ns.GetExternalCustomerId() != "ns_1" ||
		ns.GetCategory() != customerv1.IntegrationCategory_INTEGRATION_CATEGORY_ACCOUNTING || ns.GetSyncWithProvider() {
		t.Errorf("netsuite: got = %v, want = a netsuite accounting connection to ns_1, not synced", ns)
	}
	got, err := c.GetCustomer(t.Context(), customerv1.GetCustomerRequest_builder{CustomerId: ref}.Build())
	if err != nil || len(got.GetCustomer().GetConnections()) != 2 {
		t.Errorf("got = %v, %v, want = the customer's two connections", got.GetCustomer().GetConnections(), err)
	}

	t.Log("source: spec/requests/api/v1/customers_controller_spec.rb:922")
	_, err = c.GenerateCheckoutUrl(t.Context(), customerv1.GenerateCheckoutUrlRequest_builder{CustomerId: ref}.Build())
	wantCode(t, err, connect.CodeUnimplemented, "")

	if _, err := c.DeleteCustomerConnection(t.Context(), customerv1.DeleteCustomerConnectionRequest_builder{CustomerId: ref, IntegrationId: new("code:netsuite")}.Build()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = c.DeleteCustomerConnection(t.Context(), customerv1.DeleteCustomerConnectionRequest_builder{CustomerId: ref, IntegrationId: new("code:netsuite")}.Build())
	wantCode(t, err, connect.CodeNotFound, "connection_not_found")
}

// TestListCustomersActiveSubscriptions ports the active subscription
// count filter of CustomersQuery (GraphQL customers resolver arguments
// active_subscriptions_count_from and active_subscriptions_count_to).
func TestListCustomersActiveSubscriptions(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := customersClient(s)
	p := h.Plan(period.Monthly, 1000)
	none := h.Customer()
	one, two, three := h.Customer(), h.Customer(), h.Customer()
	for i, cust := range []*customer.Customer{one, two, three} {
		for range i + 1 {
			h.Subscription(cust, p)
		}
	}
	want := func(cs ...*customer.Customer) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.ID.String()
		}
		slices.Sort(out)
		return out
	}
	tests := []struct {
		name, source, filter string
		want                 []string
		code                 connect.Code
	}{
		{name: "without subscriptions", source: "spec/queries/customers_query_spec.rb:384", filter: "active_subscriptions_count_from = 0 AND active_subscriptions_count_to = 0", want: want(none)},
		{name: "exact count", source: "spec/queries/customers_query_spec.rb:394", filter: "active_subscriptions_count_from = 2 AND active_subscriptions_count_to = 2", want: want(two)},
		{name: "more than", source: "spec/queries/customers_query_spec.rb:404", filter: "active_subscriptions_count_from = 1", want: want(two, three)},
		{name: "range", source: "spec/queries/customers_query_spec.rb:415", filter: "active_subscriptions_count_from = 1 AND active_subscriptions_count_to = 2", want: want(one, two)},
		{name: "less than", source: "spec/queries/customers_query_spec.rb:426", filter: "active_subscriptions_count_to = 2", want: want(none, one)},
		{name: "not a number", filter: "active_subscriptions_count_to = two", code: connect.CodeInvalidArgument},
		{name: "negative", filter: "active_subscriptions_count_from = -1", code: connect.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			resp, err := c.ListCustomers(t.Context(), customerv1.ListCustomersRequest_builder{Filter: &tt.filter}.Build())
			if tt.code != 0 {
				wantCode(t, err, tt.code, "")
				return
			}
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			got := make([]string, 0, len(resp.GetCustomers()))
			for _, c := range resp.GetCustomers() {
				got = append(got, c.GetId())
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
}
