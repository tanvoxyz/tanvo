package rpc_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restRate is the JSON of a rate in a REST response.
type restRate struct {
	ID              string `json:"id"`
	Code            string `json:"code"`
	EffectiveFrom   string `json:"effective_from"`
	RateModel       string `json:"rate_model"`
	MinAmount       string `json:"min_amount"`
	Status          string `json:"status"`
	BillingInterval struct {
		Count int    `json:"count"`
		Unit  string `json:"unit"`
	} `json:"billing_interval"`
	Properties struct {
		Standard *struct {
			Amount string `json:"amount"`
		} `json:"standard"`
		Graduated *struct {
			Ranges []struct {
				FromValue *string `json:"from_value"`
				ToValue   *string `json:"to_value"`
			} `json:"ranges"`
		} `json:"graduated"`
	} `json:"properties"`
}

// restRateCard is the JSON of a rate card in a REST response.
type restRateCard struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	ProductID       string    `json:"product_id"`
	ProductFilterID string    `json:"product_filter_id"`
	Currency        string    `json:"currency"`
	BillingTiming   string    `json:"billing_timing"`
	TaxIDs          []string  `json:"tax_ids"`
	RatesCount      int       `json:"rates_count"`
	ActiveRate      *restRate `json:"active_rate"`
}

type restRateCardBody struct {
	RateCard restRateCard `json:"rate_card"`
}

// standardRate is the JSON of a monthly standard rate.
func standardRate(code, from, amount string) string {
	return fmt.Sprintf(`{"code":%q,"effective_from":%q,"rate_model":"CHARGE_MODEL_STANDARD",
		"properties":{"standard":{"amount":%q}},"billing_interval":{"count":1,"unit":"INTERVAL_UNIT_MONTH"}}`, code, from, amount)
}

const (
	today    = "2025-03-15T00:00:00Z"
	tomorrow = "2025-03-16T00:00:00Z"
)

func TestRateCardsREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	vat := f.s.Fixtures.Tax("vat", "20")
	f.s.Fixtures.Tax("gst", "10")
	us := f.createFilter(t, "us", "us")

	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:23")
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:40")
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:97")
	card := decode[restRateCardBody](t, f.do(t, http.MethodPost, "/v1/rate_cards", fmt.Sprintf(`{"code":"api_usd","name":"API",
		"product_id":"code:api","currency":"USD","tax_ids":["code:vat"],"rates":[%s,%s]}`,
		standardRate("launch", today, "1"), standardRate("later", tomorrow, "2")), http.StatusOK)).RateCard
	if card.ID == "" || card.ProductID != f.product.ID || card.Currency != "USD" || card.BillingTiming != "BILLING_TIMING_ARREARS" ||
		card.RatesCount != 2 || !slices.Equal(card.TaxIDs, []string{vat.ID.String()}) {
		t.Errorf("got card = %+v, want api_usd with its tax and 2 rates", card)
	}
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:303")
	if card.ActiveRate == nil || card.ActiveRate.Code != "launch" || card.ActiveRate.Status != "RATE_STATUS_ACTIVE" {
		t.Errorf("got active_rate = %+v, want launch", card.ActiveRate)
	}

	tests := []struct {
		name, source, body string
		status             int
		want               []string
	}{
		{
			"missing tax", "spec/requests/api/v2/rate_cards_controller_spec.rb:52",
			`{"code":"x","name":"x","product_id":"code:api","currency":"USD","tax_ids":["code:unknown"]}`, http.StatusNotFound, nil,
		},
		{
			"missing product", "spec/requests/api/v2/rate_cards_controller_spec.rb:63",
			`{"code":"x","name":"x","product_id":"code:unknown","currency":"USD"}`, http.StatusNotFound, nil,
		},
		{
			"invalid nested rate", "spec/requests/api/v2/rate_cards_controller_spec.rb:117",
			`{"code":"x","name":"x","product_id":"code:api","currency":"USD","rates":[` + standardRate("old", "2020-01-01T00:00:00Z", "1") + `]}`,
			http.StatusBadRequest,
			[]string{"rate_card.rates.effective_from", "must_not_be_before_today"},
		},
		{
			"missing filter", "spec/requests/api/v2/rate_cards_controller_spec.rb:157",
			`{"code":"x","name":"x","product_id":"code:api","product_filter_id":"code:unknown","currency":"USD"}`, http.StatusNotFound, nil,
		},
		{
			"invalid currency", "spec/requests/api/v2/rate_cards_controller_spec.rb:187",
			`{"code":"x","name":"x","product_id":"code:api","currency":"XXX"}`, http.StatusBadRequest,
			[]string{"rate_card.currency"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f.fail(t, http.MethodPost, "/v1/rate_cards", tt.body, tt.status, tt.want...)
		})
	}
	f.do(t, http.MethodGet, "/v1/rate_cards/code%3Ax", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:139")
	f.s.Clock.Advance(time.Minute)
	scoped := decode[restRateCardBody](t, f.do(t, http.MethodPost, "/v1/rate_cards",
		`{"code":"api_us","name":"API US","product_id":"code:api","product_filter_id":"`+us.ID+`","currency":"USD"}`, http.StatusOK)).RateCard
	if scoped.ProductFilterID != us.ID || scoped.ActiveRate != nil {
		t.Errorf("got card = %+v, want it scoped to the us filter", scoped)
	}

	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:229")
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:243")
	got := decode[restRateCardBody](t, f.do(t, http.MethodPatch, "/v1/rate_cards/code%3Aapi_us", `{"name":"US","tax_ids":["code:gst"]}`, http.StatusOK)).RateCard
	if got.Name != "US" || len(got.TaxIDs) != 1 || got.TaxIDs[0] == vat.ID.String() {
		t.Errorf("got card = %+v, want US with gst", got)
	}
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:264")
	got = decode[restRateCardBody](t, f.do(t, http.MethodPatch, "/v1/rate_cards/code%3Aapi_us", `{"name":"US2"}`, http.StatusOK)).RateCard
	if len(got.TaxIDs) != 1 {
		t.Errorf("got tax_ids = %v, want the tax kept", got.TaxIDs)
	}
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:276")
	f.do(t, http.MethodPatch, "/v1/rate_cards/code%3Aapi_us", `{"tax_ids":["code:unknown"]}`, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:253")
	got = decode[restRateCardBody](t, f.do(t, http.MethodPatch, "/v1/rate_cards/code%3Aapi_us?update_mask=taxIds", `{}`, http.StatusOK)).RateCard
	if len(got.TaxIDs) != 0 {
		t.Errorf("got tax_ids = %v, want none", got.TaxIDs)
	}
	t.Log("source: spec/services/rate_cards/update_service_spec.rb:117")
	f.fail(t, http.MethodPatch, "/v1/rate_cards/code%3Aapi_usd", `{"currency":"EUR"}`, http.StatusBadRequest, "rate_card.currency", "not_editable_with_rates")
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:288")
	f.do(t, http.MethodPatch, "/v1/rate_cards/code%3Aunknown", `{"name":"x"}`, http.StatusNotFound)

	lists := []struct {
		name, source, query string
		want                []string
	}{
		{"all", "spec/requests/api/v2/rate_cards_controller_spec.rb:337", "", []string{"api_us", "api_usd"}},
		{"by product", "spec/requests/api/v2/rate_cards_controller_spec.rb:352", "?filter=" + url.QueryEscape("product_id = "+f.product.ID), []string{"api_us", "api_usd"}},
		{"by product code", "spec/requests/api/v2/rate_cards_controller_spec.rb:362", "?filter=" + url.QueryEscape(`product_id = "code:api"`), []string{"api_us", "api_usd"}},
		{"by filter", "spec/queries/rate_cards_query_spec.rb:49", "?filter=" + url.QueryEscape("product_filter_id = "+us.ID), []string{"api_us"}},
		{"search", "spec/requests/api/v2/rate_cards_controller_spec.rb:372", "?search=api_usd", []string{"api_usd"}},
	}
	for _, tt := range lists {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			var codes []string
			for _, c := range decode[struct {
				RateCards []restRateCard `json:"rate_cards"`
			}](t, f.do(t, http.MethodGet, "/v1/rate_cards"+tt.query, nil, http.StatusOK)).RateCards {
				codes = append(codes, c.Code)
			}
			if !slices.Equal(codes, tt.want) {
				t.Errorf("got rate cards = %v, want = %v", codes, tt.want)
			}
		})
	}

	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:387")
	deleted := decode[restRateCardBody](t, f.do(t, http.MethodDelete, "/v1/rate_cards/code%3Aapi_us", nil, http.StatusOK)).RateCard
	if deleted.ID != scoped.ID {
		t.Errorf("got deleted card = %+v, want api_us", deleted)
	}
	f.do(t, http.MethodGet, "/v1/rate_cards/code%3Aapi_us", nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/rate_cards_controller_spec.rb:397")
	f.do(t, http.MethodDelete, "/v1/rate_cards/code%3Aapi_us", nil, http.StatusNotFound)

	t.Log("source: spec/services/rate_cards/destroy_service_spec.rb:37")
	plan := f.s.Fixtures.Plan(period.Monthly, 0)
	f.do(t, http.MethodPost, "/v1/plans/"+plan.ID.String()+"/rate_cards", `{"rate_card_id":"code:api_usd"}`, http.StatusOK)
	f.fail(t, http.MethodDelete, "/v1/rate_cards/code%3Aapi_usd", nil, http.StatusBadRequest, "attached_to_plan_or_subscription")
}

func TestRatesREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	f.do(t, http.MethodPost, "/v1/rate_cards", `{"code":"api_usd","name":"API","product_id":"code:api","currency":"USD"}`, http.StatusOK)
	f.do(t, http.MethodPost, "/v1/rate_cards", `{"code":"api_adv","name":"API","product_id":"code:api","currency":"USD","billing_timing":"BILLING_TIMING_ADVANCE"}`, http.StatusOK)
	path := "/v1/rate_cards/code%3Aapi_usd/rates"
	rate := func(t *testing.T, path, body string) restRate {
		t.Helper()
		return decode[struct {
			Rate restRate `json:"rate"`
		}](t, f.do(t, http.MethodPost, path, body, http.StatusOK)).Rate
	}

	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:24")
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:33")
	launch := rate(t, path, standardRate("launch", today, "1.5"))
	if launch.ID == "" || launch.EffectiveFrom != today || launch.Status != "RATE_STATUS_ACTIVE" ||
		launch.Properties.Standard == nil || launch.Properties.Standard.Amount != "1.5" || launch.MinAmount != "0.00" {
		t.Errorf("got rate = %+v, want the active launch rate", launch)
	}
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:42")
	later := rate(t, path, standardRate("later", "2025-03-20T15:30:00Z", "2"))
	if later.EffectiveFrom != "2025-03-20T00:00:00Z" || later.Status != "RATE_STATUS_PENDING" {
		t.Errorf("got rate = %+v, want the pending rate at midnight", later)
	}
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:52")
	f.fail(t, http.MethodPost, path, standardRate("same_day", "2025-03-20T08:00:00Z", "3"), http.StatusBadRequest, "rate.effective_from", core.CodeAlreadyExists)
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:66")
	adv := rate(t, "/v1/rate_cards/code%3Aapi_adv/rates", standardRate("adv", "2025-03-20T15:30:00Z", "2"))
	if adv.EffectiveFrom != "2025-03-20T15:30:00Z" {
		t.Errorf("got effective_from = %s, want the datetime in full", adv.EffectiveFrom)
	}
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:99")
	f.do(t, http.MethodPost, "/v1/rate_cards/code%3Aunknown/rates", standardRate("x", tomorrow, "1"), http.StatusNotFound)
	t.Log("source: spec/services/rate_card_rates/create_service_spec.rb:31")
	tiers := rate(t, path, `{"code":"tiers","effective_from":"2025-04-01T00:00:00Z","rate_model":"CHARGE_MODEL_GRADUATED",
		"properties":{"graduated":{"ranges":[{"to_value":"10","per_unit_amount":"1","flat_amount":"0"},{"per_unit_amount":"0.5","flat_amount":"0"}]}},
		"billing_interval":{"count":1,"unit":"INTERVAL_UNIT_MONTH"}}`)
	if g := tiers.Properties.Graduated; g == nil || len(g.Ranges) != 2 || g.Ranges[0].FromValue != nil || g.Ranges[0].ToValue == nil || *g.Ranges[0].ToValue != "10" || g.Ranges[1].ToValue != nil {
		t.Errorf("got properties = %+v, want two tiers by upper bound", tiers.Properties)
	}

	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:149")
	got := decode[struct {
		Rate restRate `json:"rate"`
	}](t, f.do(t, http.MethodGet, path+"/code%3Alater", nil, http.StatusOK)).Rate
	if got.ID != later.ID {
		t.Errorf("got rate = %+v, want later", got)
	}
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:116")
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:124")
	got = decode[struct {
		Rate restRate `json:"rate"`
	}](t, f.do(t, http.MethodPatch, path+"/code%3Alater", `{"code":"next","properties":{"standard":{"amount":"3"}},"min_amount":"5"}`, http.StatusOK)).Rate
	if got.Code != "next" || got.Properties.Standard.Amount != "3" || got.MinAmount != "5.00" {
		t.Errorf("got rate = %+v, want next at 3 with a 5.00 minimum", got)
	}
	t.Log("source: spec/services/rate_card_rates/update_service_spec.rb:99")
	f.fail(t, http.MethodPatch, path+"/code%3Alaunch", `{"billing_interval":{"count":6}}`, http.StatusBadRequest, "rate.billing_interval.count", "not_editable_on_active_rate")
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:134")
	f.do(t, http.MethodPatch, path+"/code%3Aunknown", `{"code":"x"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:166")
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:204")
	list := decode[struct {
		Rates []restRate `json:"rates"`
	}](t, f.do(t, http.MethodGet, path, nil, http.StatusOK)).Rates
	var codes []string
	for _, r := range list {
		codes = append(codes, r.Code)
	}
	if want := []string{"tiers", "next", "launch"}; !slices.Equal(codes, want) {
		t.Errorf("got rates = %v, want = %v", codes, want)
	}
	page := decode[struct {
		Rates         []restRate `json:"rates"`
		NextPageToken string     `json:"next_page_token"`
	}](t, f.do(t, http.MethodGet, path+"?page_size=2", nil, http.StatusOK))
	next := decode[struct {
		Rates []restRate `json:"rates"`
	}](t, f.do(t, http.MethodGet, path+"?page_size=2&page_token="+page.NextPageToken, nil, http.StatusOK)).Rates
	if len(page.Rates) != 2 || len(next) != 1 || next[0].Code != "launch" {
		t.Errorf("got pages = %+v / %+v, want 2 then launch", page.Rates, next)
	}

	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:219")
	deleted := decode[struct {
		Rate restRate `json:"rate"`
	}](t, f.do(t, http.MethodDelete, path+"/code%3Anext", nil, http.StatusOK)).Rate
	if deleted.ID != later.ID {
		t.Errorf("got deleted rate = %+v, want next", deleted)
	}
	t.Log("source: spec/requests/api/v2/rate_cards/rates_controller_spec.rb:228")
	f.fail(t, http.MethodDelete, path+"/code%3Alaunch", nil, http.StatusBadRequest, "only_pending_rates_can_be_deleted")
}
