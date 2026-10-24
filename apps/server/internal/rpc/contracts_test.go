package rpc_test

import (
	"net/http"
	"slices"
	"testing"
)

// restContract is the JSON of a contract in a REST response.
type restContract struct {
	ID                 string `json:"id"`
	ExternalID         string `json:"external_id"`
	ExternalCustomerID string `json:"external_customer_id"`
	Name               string `json:"name"`
	PlanCode           string `json:"plan_code"`
	Status             string `json:"status"`
	Currency           string `json:"currency"`
	RateCardsCount     int    `json:"rate_cards_count"`
}

// restContractRateCard is the JSON of a contract rate card in a REST
// response.
type restContractRateCard struct {
	ID                 string          `json:"id"`
	ExternalContractID string          `json:"external_contract_id"`
	RateCardCode       string          `json:"rate_card_code"`
	Units              string          `json:"units"`
	RatePhases         []restRatePhase `json:"rate_phases"`
	RatePhasesCount    int             `json:"rate_phases_count"`
}

type restContractBody struct {
	Contract restContract `json:"contract"`
}

type restContractRateCardBody struct {
	RateCard restContractRateCard `json:"rate_card"`
}

func TestContractsREST(t *testing.T) {
	t.Parallel()
	f := newCatalogFixture(t)
	cust := f.s.Fixtures.Customer()
	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:67")
	c := decode[restContractBody](t, f.do(t, http.MethodPost, "/v1/contracts",
		`{"external_customer_id":"`+cust.ExternalID+`","external_id":"contract-1","started_at":"2999-01-01T00:00:00Z"}`, http.StatusOK)).Contract
	if c.ID == "" || c.ExternalID != "contract-1" || c.ExternalCustomerID != cust.ExternalID || c.PlanCode != "" ||
		c.Status != "SUBSCRIPTION_STATUS_PENDING" || c.RateCardsCount != 0 {
		t.Errorf("got contract = %+v, want a pending plan-less contract-1", c)
	}
	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:79")
	f.fail(t, http.MethodPost, "/v1/contracts", `{"external_customer_id":"unknown","external_id":"contract-2"}`, http.StatusNotFound, "customer")
	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:89")
	f.fail(t, http.MethodPost, "/v1/contracts", `{"external_customer_id":"`+cust.ExternalID+`","external_id":"contract-1"}`,
		http.StatusBadRequest, "external_id", "value_already_exists")

	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:184")
	got := decode[restContractBody](t, f.do(t, http.MethodGet, "/v1/contracts/contract-1?status=SUBSCRIPTION_STATUS_PENDING", nil, http.StatusOK)).Contract
	if got.ID != c.ID {
		t.Errorf("got contract = %+v, want contract-1", got)
	}
	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:280")
	f.do(t, http.MethodGet, "/v1/contracts/unknown", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:296")
	got = decode[restContractBody](t, f.do(t, http.MethodPatch, "/v1/contracts/contract-1", `{"name":"Renamed"}`, http.StatusOK)).Contract
	if got.ExternalID != "contract-1" || got.Name != "Renamed" {
		t.Errorf("got contract = %+v, want contract-1 renamed", got)
	}

	f.do(t, http.MethodPost, "/v1/rate_cards",
		`{"code":"api_usd","name":"API","product_id":"code:api","currency":"USD","rates":[`+standardRate("launch", today, "1")+`]}`, http.StatusOK)
	cards := "/v1/contracts/contract-1/rate_cards"
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:19")
	card := decode[restContractRateCardBody](t, f.do(t, http.MethodPost, cards, `{"rate_card_code":"api_usd"}`, http.StatusOK)).RateCard
	if card.ID == "" || card.ExternalContractID != "contract-1" || card.RateCardCode != "api_usd" || card.RatePhasesCount != 1 ||
		!slices.Equal(phaseCodesOf(card.RatePhases), []string{"default"}) {
		t.Errorf("got card = %+v, want api_usd with a default phase", card)
	}
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:41")
	f.do(t, http.MethodPost, cards, `{"rate_card_code":"unknown"}`, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:81")
	list := decode[struct {
		RateCards []restContractRateCard `json:"rate_cards"`
	}](t, f.do(t, http.MethodGet, cards, nil, http.StatusOK)).RateCards
	if len(list) != 1 || list[0].ID != card.ID {
		t.Errorf("got cards = %+v, want api_usd only", list)
	}
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:116")
	if got := decode[restContractRateCardBody](t, f.do(t, http.MethodGet, cards+"/api_usd", nil, http.StatusOK)).RateCard; got.ID != card.ID {
		t.Errorf("got card = %+v, want api_usd", got)
	}
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:138")
	f.do(t, http.MethodGet, cards+"/unknown", nil, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:152")
	if got := decode[restContractRateCardBody](t, f.do(t, http.MethodPatch, cards+"/api_usd", `{"units":"5"}`, http.StatusOK)).RateCard; got.Units != "5" {
		t.Errorf("got units = %q, want = 5", got.Units)
	}
	t.Log("source: spec/requests/api/v2/contract_rate_cards_controller_spec.rb:167")
	f.do(t, http.MethodDelete, cards+"/api_usd", nil, http.StatusOK)
	f.do(t, http.MethodGet, cards+"/api_usd", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:410")
	got = decode[restContractBody](t, f.do(t, http.MethodDelete, "/v1/contracts/contract-1", nil, http.StatusOK)).Contract
	if got.Status != "SUBSCRIPTION_STATUS_CANCELED" {
		t.Errorf("got status = %s, want = canceled", got.Status)
	}
	t.Log("source: spec/requests/api/v2/contracts_controller_spec.rb:433")
	f.do(t, http.MethodDelete, "/v1/contracts/contract-1", nil, http.StatusNotFound)

	// Contracts are not served by the subscription API.
	f.do(t, http.MethodGet, "/v1/subscriptions/"+c.ID, nil, http.StatusNotFound)
}
