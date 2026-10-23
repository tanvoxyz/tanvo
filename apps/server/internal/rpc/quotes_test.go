package rpc_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
)

type restQuoteVersion struct {
	ID           string         `json:"id"`
	QuoteID      string         `json:"quote_id"`
	Version      int            `json:"version"`
	Status       string         `json:"status"`
	VoidReason   string         `json:"void_reason"`
	Currency     string         `json:"currency"`
	BillingItems map[string]any `json:"billing_items"`
}

type restQuote struct {
	ID             string           `json:"id"`
	Number         string           `json:"number"`
	CustomerID     string           `json:"customer_id"`
	OrderType      string           `json:"order_type"`
	CurrentVersion restQuoteVersion `json:"current_version"`
}

type restOrderForm struct {
	ID             string `json:"id"`
	QuoteID        string `json:"quote_id"`
	QuoteVersionID string `json:"quote_version_id"`
	Status         string `json:"status"`
	VoidReason     string `json:"void_reason"`
	ExpiresAt      string `json:"expires_at"`
}

type restOrder struct {
	ID              string `json:"id"`
	OrderFormID     string `json:"order_form_id"`
	Status          string `json:"status"`
	OrderType       string `json:"order_type"`
	ExecutionMode   string `json:"execution_mode"`
	ExecuteAt       string `json:"execute_at"`
	ExecutionRecord struct {
		InvoiceID string   `json:"invoice_id"`
		Errors    []string `json:"errors"`
	} `json:"execution_record"`
}

// quoteFixture is a customer with an approved one-off quote of a 100.00
// add-on and its generated order form.
type quoteFixture struct {
	s     *rpctest.Server
	cust  *customer.Customer
	quote restQuote
	form  restOrderForm
}

func (f *quoteFixture) do(t *testing.T, method, path string, body any, want int) []byte {
	t.Helper()
	status, out := f.s.REST(t, f.s.APIKey, method, path, body)
	if status != want {
		t.Fatalf("%s %s: got status = %d (%s), want = %d", method, path, status, out, want)
	}
	return out
}

// createQuote creates a draft one-off quote of a new add-on.
func (f *quoteFixture) createQuote(t *testing.T) restQuote {
	t.Helper()
	a := f.s.Fixtures.AddOn(10_000)
	body := fmt.Sprintf(`{"customer_id":%q,"order_type":"ORDER_TYPE_ONE_OFF","current_version":{"billing_items":{"addOns":[`+
		`{"id":%q,"localId":"3d08b2df-4e4c-4d58-b415-a525c1663735","type":"add_on","payload":{"code":%q,"units":1,"unitAmountCents":10000,"totalAmountCents":10000}}]}}}`,
		f.cust.ID, a.ID, a.Code)
	return decode[struct {
		Quote restQuote `json:"quote"`
	}](t, f.do(t, http.MethodPost, "/v1/quotes", body, http.StatusOK)).Quote
}

func newQuoteFixture(t *testing.T) *quoteFixture {
	t.Helper()
	s := rpctest.New(t)
	f := &quoteFixture{s: s, cust: s.Fixtures.Customer()}
	f.quote = f.createQuote(t)
	out := decode[struct {
		Version   restQuoteVersion `json:"version"`
		OrderForm restOrderForm    `json:"order_form"`
	}](t, f.do(t, http.MethodPost, f.versionPath()+":approve", `{}`, http.StatusOK))
	if out.Version.Status != "QUOTE_VERSION_STATUS_APPROVED" {
		t.Fatalf("got version status = %s, want approved", out.Version.Status)
	}
	f.form = out.OrderForm
	return f
}

func (f *quoteFixture) versionPath() string {
	return "/v1/quotes/" + f.quote.ID + "/versions/" + f.quote.CurrentVersion.ID
}

func (f *quoteFixture) sign(t *testing.T, body string) restOrder {
	t.Helper()
	return decode[struct {
		Order restOrder `json:"order"`
	}](t, f.do(t, http.MethodPost, "/v1/order_forms/"+f.form.ID+":sign", body, http.StatusOK)).Order
}

func TestQuotesREST(t *testing.T) {
	t.Parallel()
	f := newQuoteFixture(t)
	t.Log("source: spec/requests/api/v1/quotes_controller_spec.rb:211")
	got := decode[struct {
		Quote restQuote `json:"quote"`
	}](t, f.do(t, http.MethodGet, "/v1/quotes/"+f.quote.ID, nil, http.StatusOK)).Quote
	if got.Number != f.quote.Number || got.CustomerID != f.cust.ID.String() || got.OrderType != "ORDER_TYPE_ONE_OFF" ||
		got.CurrentVersion.ID != f.quote.CurrentVersion.ID || got.CurrentVersion.Currency != "USD" || got.CurrentVersion.BillingItems["addOns"] == nil {
		t.Errorf("got quote = %+v, want the one-off quote with its current version", got)
	}
	t.Log("source: spec/requests/api/v1/quotes_controller_spec.rb:232")
	f.do(t, http.MethodGet, "/v1/quotes/"+core.NewID[core.QuoteID](core.UUIDv7Generator{}).String(), nil, http.StatusNotFound)

	other := f.createQuote(t)
	tests := []struct {
		name, source, filter string
		want                 []string
	}{
		{"all", "spec/requests/api/v1/quotes_controller_spec.rb:18", "", []string{other.ID, f.quote.ID}},
		{"by status", "spec/requests/api/v1/quotes_controller_spec.rb:90", "status = QUOTE_VERSION_STATUS_DRAFT", []string{other.ID}},
		{"by number", "spec/requests/api/v1/quotes_controller_spec.rb:118", fmt.Sprintf("number = %q", f.quote.Number), []string{f.quote.ID}},
		{"by external customer", "spec/requests/api/v1/quotes_controller_spec.rb:132", fmt.Sprintf("external_customer_id = %q", f.cust.ExternalID), []string{other.ID, f.quote.ID}},
		{"by unknown external customer", "spec/requests/api/v1/quotes_controller_spec.rb:144", `external_customer_id = "unknown"`, nil},
		{"by order type", "spec/requests/api/v1/quotes_controller_spec.rb:104", "order_type = ORDER_TYPE_SUBSCRIPTION_CREATION", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			path := "/v1/quotes"
			if tt.filter != "" {
				path += "?filter=" + url.QueryEscape(tt.filter)
			}
			list := decode[struct {
				Quotes []restQuote `json:"quotes"`
			}](t, f.do(t, http.MethodGet, path, nil, http.StatusOK)).Quotes
			ids := make([]string, len(list))
			for i, q := range list {
				ids[i] = q.ID
			}
			slices.Sort(ids)
			want := slices.Sorted(slices.Values(tt.want))
			if !slices.Equal(ids, want) {
				t.Errorf("got quotes = %v, want = %v", ids, want)
			}
		})
	}

	t.Log("source: spec/requests/api/v1/quotes/versions_controller_spec.rb:20")
	versions := decode[struct {
		Versions []restQuoteVersion `json:"versions"`
	}](t, f.do(t, http.MethodGet, "/v1/quotes/"+f.quote.ID+"/versions", nil, http.StatusOK)).Versions
	if len(versions) != 1 || versions[0].ID != f.quote.CurrentVersion.ID {
		t.Errorf("got versions = %+v, want the approved version", versions)
	}
	t.Log("source: spec/requests/api/v1/quotes/versions_controller_spec.rb:62")
	f.do(t, http.MethodGet, "/v1/quotes/"+core.NewID[core.QuoteID](core.UUIDv7Generator{}).String()+"/versions", nil, http.StatusNotFound)
}

func TestQuoteVersionsREST(t *testing.T) {
	t.Parallel()
	f := newQuoteFixture(t)
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:20")
	v := decode[struct {
		Version restQuoteVersion `json:"version"`
	}](t, f.do(t, http.MethodGet, f.versionPath(), nil, http.StatusOK)).Version
	if v.Status != "QUOTE_VERSION_STATUS_APPROVED" || v.Version != 1 || v.QuoteID != f.quote.ID {
		t.Errorf("got version = %+v, want approved version 1 of the quote", v)
	}
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:35")
	f.do(t, http.MethodGet, "/v1/quotes/"+f.quote.ID+"/versions/"+core.NewID[core.QuoteVersionID](core.UUIDv7Generator{}).String(), nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:135")
	f.do(t, http.MethodPost, f.versionPath()+":approve", `{}`, http.StatusBadRequest)
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:192")
	f.do(t, http.MethodPost, f.versionPath()+":void", `{}`, http.StatusBadRequest)
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:270")
	f.do(t, http.MethodPost, f.versionPath()+":clone", `{}`, http.StatusBadRequest)

	q := f.createQuote(t)
	path := "/v1/quotes/" + q.ID + "/versions/" + q.CurrentVersion.ID
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:124")
	past := f.s.Clock.Now().Add(-time.Hour).Format(time.RFC3339)
	f.do(t, http.MethodPost, path+":approve", fmt.Sprintf(`{"expires_at":%q}`, past), http.StatusBadRequest)
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:238")
	clone := decode[struct {
		Version restQuoteVersion `json:"version"`
	}](t, f.do(t, http.MethodPost, path+":clone", `{}`, http.StatusOK)).Version
	if clone.Version != 2 || clone.Status != "QUOTE_VERSION_STATUS_DRAFT" {
		t.Errorf("got clone = %+v, want draft version 2", clone)
	}
	t.Log("source: spec/requests/api/v1/quote_versions_controller_spec.rb:181")
	voided := decode[struct {
		Version restQuoteVersion `json:"version"`
	}](t, f.do(t, http.MethodPost, "/v1/quotes/"+q.ID+"/versions/"+clone.ID+":void", `{}`, http.StatusOK)).Version
	if voided.Status != "QUOTE_VERSION_STATUS_VOIDED" || voided.VoidReason != "QUOTE_VERSION_VOID_REASON_MANUAL" {
		t.Errorf("got version = %+v, want voided manually", voided)
	}
}

func TestOrderFormsREST(t *testing.T) {
	t.Parallel()
	t.Run("get and list", func(t *testing.T) {
		t.Parallel()
		f := newQuoteFixture(t)
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:59")
		got := decode[struct {
			OrderForm restOrderForm `json:"order_form"`
		}](t, f.do(t, http.MethodGet, "/v1/order_forms/"+f.form.ID, nil, http.StatusOK)).OrderForm
		if got.Status != "ORDER_FORM_STATUS_GENERATED" || got.QuoteID != f.quote.ID || got.QuoteVersionID != f.quote.CurrentVersion.ID {
			t.Errorf("got order form = %+v, want the generated form of the quote", got)
		}
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:71")
		f.do(t, http.MethodGet, "/v1/order_forms/"+core.NewID[core.OrderFormID](core.UUIDv7Generator{}).String(), nil, http.StatusNotFound)
		for _, tt := range []struct {
			source, filter string
			want           int
		}{
			{"spec/requests/api/v1/order_forms_controller_spec.rb:21", "", 1},
			{"spec/requests/api/v1/order_forms_controller_spec.rb:31", "status = ORDER_FORM_STATUS_GENERATED", 1},
			{"spec/requests/api/v1/order_forms_controller_spec.rb:31", "status = ORDER_FORM_STATUS_SIGNED", 0},
		} {
			t.Logf("source: %s", tt.source)
			list := decode[struct {
				OrderForms []restOrderForm `json:"order_forms"`
			}](t, f.do(t, http.MethodGet, "/v1/order_forms?filter="+url.QueryEscape(tt.filter), nil, http.StatusOK)).OrderForms
			if len(list) != tt.want {
				t.Errorf("filter %q: got %d order forms, want = %d", tt.filter, len(list), tt.want)
			}
		}
	})
	t.Run("sign", func(t *testing.T) {
		t.Parallel()
		f := newQuoteFixture(t)
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:203")
		at := f.s.Clock.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		o := f.sign(t, fmt.Sprintf(`{"execution_mode":"EXECUTION_MODE_ORDER_ONLY","execute_at":%q}`, at))
		if o.Status != "ORDER_STATUS_CREATED" || o.OrderFormID != f.form.ID || o.ExecutionMode != "EXECUTION_MODE_ORDER_ONLY" || o.ExecuteAt != at {
			t.Errorf("got order = %+v, want a created order_only order at %s", o, at)
		}
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:114")
		f.do(t, http.MethodPost, "/v1/order_forms/"+f.form.ID+":sign", `{}`, http.StatusBadRequest)
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:131")
		f.do(t, http.MethodPost, "/v1/order_forms/"+core.NewID[core.OrderFormID](core.UUIDv7Generator{}).String()+":sign", `{}`, http.StatusNotFound)
	})
	tests := []struct {
		name, source, body string
	}{
		{"malformed signed document", "spec/requests/api/v1/order_forms_controller_spec.rb:186", `{"signed_document":"not a data uri"}`},
		{"execute_at without execution mode", "spec/requests/api/v1/order_forms_controller_spec.rb:237", `{"execute_at":"2099-01-01T00:00:00Z"}`},
		{"execute_at in the past", "spec/requests/api/v1/order_forms_controller_spec.rb:271", `{"execution_mode":"EXECUTION_MODE_EXECUTE","execute_at":"2000-01-01T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newQuoteFixture(t)
			f.do(t, http.MethodPost, "/v1/order_forms/"+f.form.ID+":sign", tt.body, http.StatusBadRequest)
		})
	}
	t.Run("void", func(t *testing.T) {
		t.Parallel()
		f := newQuoteFixture(t)
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:287")
		got := decode[struct {
			OrderForm restOrderForm `json:"order_form"`
		}](t, f.do(t, http.MethodPost, "/v1/order_forms/"+f.form.ID+":void", `{}`, http.StatusOK)).OrderForm
		if got.Status != "ORDER_FORM_STATUS_VOIDED" || got.VoidReason != "ORDER_FORM_VOID_REASON_MANUAL" {
			t.Errorf("got order form = %+v, want voided manually", got)
		}
		t.Log("source: spec/requests/api/v1/order_forms_controller_spec.rb:307")
		f.do(t, http.MethodPost, "/v1/order_forms/"+f.form.ID+":void", `{}`, http.StatusBadRequest)
	})
}

func TestOrdersREST(t *testing.T) {
	t.Parallel()
	t.Run("get, list and execute", func(t *testing.T) {
		t.Parallel()
		f := newQuoteFixture(t)
		o := f.sign(t, `{}`)
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:73")
		got := decode[struct {
			Order restOrder `json:"order"`
		}](t, f.do(t, http.MethodGet, "/v1/orders/"+o.ID, nil, http.StatusOK)).Order
		if got.Status != "ORDER_STATUS_CREATED" || got.OrderType != "ORDER_TYPE_ONE_OFF" {
			t.Errorf("got order = %+v, want a created one-off order", got)
		}
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:86")
		f.do(t, http.MethodGet, "/v1/orders/"+core.NewID[core.OrderID](core.UUIDv7Generator{}).String(), nil, http.StatusNotFound)
		for _, tt := range []struct {
			source, filter string
			want           int
		}{
			{"spec/requests/api/v1/orders_controller_spec.rb:23", "", 1},
			{"spec/requests/api/v1/orders_controller_spec.rb:34", "status = ORDER_STATUS_EXECUTED", 0},
			{"spec/requests/api/v1/orders_controller_spec.rb:45", "order_type = ORDER_TYPE_ONE_OFF", 1},
		} {
			t.Logf("source: %s", tt.source)
			list := decode[struct {
				Orders []restOrder `json:"orders"`
			}](t, f.do(t, http.MethodGet, "/v1/orders?filter="+url.QueryEscape(tt.filter), nil, http.StatusOK)).Orders
			if len(list) != tt.want {
				t.Errorf("filter %q: got %d orders, want = %d", tt.filter, len(list), tt.want)
			}
		}
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:173")
		f.do(t, http.MethodPost, "/v1/orders/"+o.ID+":execute", `{}`, http.StatusBadRequest)
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:135")
		ex := decode[struct {
			Order restOrder `json:"order"`
		}](t, f.do(t, http.MethodPost, "/v1/orders/"+o.ID+":execute", `{"execution_mode":"EXECUTION_MODE_EXECUTE"}`, http.StatusOK)).Order
		if ex.Status != "ORDER_STATUS_EXECUTED" || ex.ExecutionMode != "EXECUTION_MODE_EXECUTE" || ex.ExecutionRecord.InvoiceID == "" {
			t.Errorf("got order = %+v, want executed with an invoice", ex)
		}
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:147")
		again := decode[struct {
			Order restOrder `json:"order"`
		}](t, f.do(t, http.MethodPost, "/v1/orders/"+o.ID+":execute", `{}`, http.StatusOK)).Order
		if again.ExecutionRecord.InvoiceID != ex.ExecutionRecord.InvoiceID {
			t.Errorf("got invoice = %s, want = %s", again.ExecutionRecord.InvoiceID, ex.ExecutionRecord.InvoiceID)
		}
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:208")
		f.do(t, http.MethodPost, "/v1/orders/"+o.ID+":execute", `{"execution_mode":"EXECUTION_MODE_ORDER_ONLY"}`, http.StatusBadRequest)
		t.Log("source: spec/requests/api/v1/orders_controller_spec.rb:250")
		f.do(t, http.MethodPost, "/v1/orders/"+core.NewID[core.OrderID](core.UUIDv7Generator{}).String()+":execute", `{}`, http.StatusNotFound)
	})
	t.Run("update", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/update_service_spec.rb:86")
		f := newQuoteFixture(t)
		o := f.sign(t, `{}`)
		at := f.s.Clock.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		got := decode[struct {
			Order restOrder `json:"order"`
		}](t, f.do(t, http.MethodPatch, "/v1/orders/"+o.ID+"?update_mask=executionMode,executeAt",
			fmt.Sprintf(`{"execution_mode":"EXECUTION_MODE_ORDER_ONLY","execute_at":%q}`, at), http.StatusOK)).Order
		if got.ExecutionMode != "EXECUTION_MODE_ORDER_ONLY" || got.ExecuteAt != at {
			t.Errorf("got order = %+v, want order_only at %s", got, at)
		}
	})
}
