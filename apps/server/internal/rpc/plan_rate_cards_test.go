package rpc_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// restRatePhase is the JSON of a rate phase in a REST response.
type restRatePhase struct {
	ID                        string `json:"id"`
	Code                      string `json:"code"`
	Position                  int    `json:"position"`
	BillingIntervalCycleCount *int   `json:"billing_interval_cycle_count"`
	RateOverride              *struct {
		RateModel  string `json:"rate_model"`
		MinAmount  string `json:"min_amount"`
		Properties struct {
			Standard *struct {
				Amount string `json:"amount"`
			} `json:"standard"`
		} `json:"properties"`
	} `json:"rate_override"`
}

// restPlanRateCard is the JSON of a plan rate card in a REST response.
type restPlanRateCard struct {
	ID              string          `json:"id"`
	PlanID          string          `json:"plan_id"`
	RateCardID      string          `json:"rate_card_id"`
	Units           string          `json:"units"`
	RatePhases      []restRatePhase `json:"rate_phases"`
	RatePhasesCount int             `json:"rate_phases_count"`
}

type restPlanRateCardBody struct {
	PlanRateCard restPlanRateCard `json:"plan_rate_card"`
}

type restRatePhaseBody struct {
	RatePhase restRatePhase `json:"rate_phase"`
}

func phaseCodesOf(phases []restRatePhase) []string {
	out := make([]string, len(phases))
	for i, p := range phases {
		out[i] = p.Code
	}
	return out
}

// planRateCardFixture is a catalog fixture with the rate card api_usd and
// a USD plan.
type planRateCardFixture struct {
	*catalogFixture
	card restRateCard
	plan *catalog.Plan
	path string
}

func newPlanRateCardFixture(t *testing.T) *planRateCardFixture {
	t.Helper()
	f := &planRateCardFixture{catalogFixture: newCatalogFixture(t)}
	f.card = decode[restRateCardBody](t, f.do(t, http.MethodPost, "/v1/rate_cards",
		`{"code":"api_usd","name":"API","product_id":"code:api","currency":"USD","rates":[`+standardRate("launch", today, "1")+`]}`, http.StatusOK)).RateCard
	f.plan = f.s.Fixtures.Plan(period.Monthly, 0)
	f.path = "/v1/plans/" + f.plan.ID.String() + "/rate_cards"
	return f
}

func TestPlanRateCardsREST(t *testing.T) {
	t.Parallel()
	f := newPlanRateCardFixture(t)
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:20")
	prc := decode[restPlanRateCardBody](t, f.do(t, http.MethodPost, f.path, `{"rate_card_id":"code:api_usd","units":"2"}`, http.StatusOK)).PlanRateCard
	if prc.ID == "" || prc.PlanID != f.plan.ID.String() || prc.RateCardID != f.card.ID || prc.Units != "2" || prc.RatePhasesCount != 1 ||
		!slices.Equal(phaseCodesOf(prc.RatePhases), []string{"default"}) || prc.RatePhases[0].BillingIntervalCycleCount != nil {
		t.Errorf("got plan rate card = %+v, want api_usd with a default indefinite phase", prc)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:33")
	f.do(t, http.MethodPost, "/v1/plans/"+unknownID[core.PlanID]()+"/rate_cards", `{"rate_card_id":"code:api_usd"}`, http.StatusNotFound)
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:43")
	f.do(t, http.MethodPost, f.path, `{"rate_card_id":"code:unknown"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:61")
	other := newPlanRateCardFixture(t)
	other.do(t, http.MethodPost, other.path, `{"rate_card_id":"code:api_usd"}`, http.StatusOK)
	list := decode[struct {
		PlanRateCards []restPlanRateCard `json:"plan_rate_cards"`
	}](t, f.do(t, http.MethodGet, f.path, nil, http.StatusOK)).PlanRateCards
	if len(list) != 1 || list[0].ID != prc.ID {
		t.Errorf("got plan rate cards = %+v, want the plan's only", list)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:90")
	f.do(t, http.MethodGet, "/v1/plans/"+unknownID[core.PlanID]()+"/rate_cards", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:160")
	got := decode[restPlanRateCardBody](t, f.do(t, http.MethodGet, f.path+"/code%3Aapi_usd", nil, http.StatusOK)).PlanRateCard
	if got.ID != prc.ID {
		t.Errorf("got plan rate card = %+v, want api_usd", got)
	}
	f.do(t, http.MethodGet, f.path+"/"+prc.ID, nil, http.StatusOK)
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:170")
	f.do(t, http.MethodGet, f.path+"/code%3Aunknown", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:185")
	got = decode[restPlanRateCardBody](t, f.do(t, http.MethodPatch, f.path+"/code%3Aapi_usd", `{"units":"5"}`, http.StatusOK)).PlanRateCard
	if got.Units != "5" {
		t.Errorf("got units = %q, want = 5", got.Units)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:205")
	f.do(t, http.MethodPatch, f.path+"/code%3Aunknown", `{"units":"5"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:220")
	deleted := decode[restPlanRateCardBody](t, f.do(t, http.MethodDelete, f.path+"/code%3Aapi_usd", nil, http.StatusOK)).PlanRateCard
	if deleted.ID != prc.ID || deleted.RatePhasesCount != 1 {
		t.Errorf("got deleted plan rate card = %+v, want api_usd with its phase", deleted)
	}
	f.do(t, http.MethodGet, f.path+"/code%3Aapi_usd", nil, http.StatusNotFound)
}

func TestPlanRateCardPhasesNestedREST(t *testing.T) {
	t.Parallel()
	f := newPlanRateCardFixture(t)
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:110")
	prc := decode[restPlanRateCardBody](t, f.do(t, http.MethodPost, f.path, `{"rate_card_id":"code:api_usd","rate_phases":[
		{"code":"intro","position":1,"billing_interval_cycle_count":3,"rate_override":{"rate_model":"CHARGE_MODEL_STANDARD","properties":{"standard":{"amount":"0.5"}},"min_amount":"10"}},
		{"code":"steady","position":2}]}`, http.StatusOK)).PlanRateCard
	if !slices.Equal(phaseCodesOf(prc.RatePhases), []string{"intro", "steady"}) || prc.RatePhases[0].Position != 1 ||
		prc.RatePhases[0].RateOverride == nil || prc.RatePhases[0].RateOverride.MinAmount != "10.00" ||
		prc.RatePhases[0].RateOverride.Properties.Standard.Amount != "0.5" || prc.RatePhases[1].RateOverride != nil {
		t.Errorf("got phases = %+v, want intro with its override then steady", prc.RatePhases)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards_controller_spec.rb:129")
	plan := f.s.Fixtures.Plan(period.Monthly, 0)
	f.fail(t, http.MethodPost, "/v1/plans/"+plan.ID.String()+"/rate_cards",
		`{"rate_card_id":"code:api_usd","rate_phases":[{"code":"a","position":1,"billing_interval_cycle_count":1}]}`, http.StatusBadRequest,
		"plan_rate_card.rate_phases", "last_phase_must_be_indefinite")
}

func TestRatePhasesREST(t *testing.T) {
	t.Parallel()
	f := newPlanRateCardFixture(t)
	f.do(t, http.MethodPost, f.path, `{"rate_card_id":"code:api_usd"}`, http.StatusOK)
	path := f.path + "/code%3Aapi_usd/rate_phases"
	phases := func(t *testing.T) []restRatePhase {
		t.Helper()
		return decode[struct {
			RatePhases []restRatePhase `json:"rate_phases"`
		}](t, f.do(t, http.MethodGet, path, nil, http.StatusOK)).RatePhases
	}

	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:18")
	if got := phaseCodesOf(phases(t)); !slices.Equal(got, []string{"default"}) {
		t.Errorf("got phases = %v, want = [default]", got)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:29")
	f.do(t, http.MethodGet, f.path+"/code%3Aunknown/rate_phases", nil, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:47")
	intro := decode[restRatePhaseBody](t, f.do(t, http.MethodPost, path, `{"code":"intro","billing_interval_cycle_count":2}`, http.StatusOK)).RatePhase
	if intro.Position != 1 || !slices.Equal(phaseCodesOf(phases(t)), []string{"intro", "default"}) {
		t.Errorf("got intro = %+v, want it inserted before the indefinite tail", intro)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:66")
	promo := decode[restRatePhaseBody](t, f.do(t, http.MethodPost, path, `{"code":"promo","billing_interval_cycle_count":1,
		"rate_override":{"rate_model":"CHARGE_MODEL_STANDARD","properties":{"standard":{"amount":"0.1"}}}}`, http.StatusOK)).RatePhase
	if promo.RateOverride == nil || promo.RateOverride.Properties.Standard.Amount != "0.1" {
		t.Errorf("got promo = %+v, want its override", promo)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:79")
	f.fail(t, http.MethodPost, path, `{"code":"open","position":1}`, http.StatusBadRequest,
		"rate_phase.billing_interval_cycle_count", "indefinite_phase_must_be_last")

	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:101")
	got := decode[restRatePhaseBody](t, f.do(t, http.MethodPatch, path+"/code%3Aintro", `{"name":"Intro","billing_interval_cycle_count":4}`, http.StatusOK)).RatePhase
	if got.BillingIntervalCycleCount == nil || *got.BillingIntervalCycleCount != 4 {
		t.Errorf("got phase = %+v, want 4 cycles", got)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:124")
	f.do(t, http.MethodPatch, path+"/code%3Apromo", `{"position":1}`, http.StatusOK)
	if got := phaseCodesOf(phases(t)); !slices.Equal(got, []string{"promo", "intro", "default"}) {
		t.Errorf("got phases = %v, want promo moved first", got)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:158")
	f.fail(t, http.MethodPatch, path+"/code%3Apromo", `{"position":3}`, http.StatusBadRequest, "rate_phase.position", "last_phase_must_be_indefinite")
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:179")
	f.fail(t, http.MethodPatch, path+"/code%3Adefault", `{"billing_interval_cycle_count":2}`, http.StatusBadRequest,
		"rate_phase.billing_interval_cycle_count", "last_phase_must_be_indefinite")
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:200")
	got = decode[restRatePhaseBody](t, f.do(t, http.MethodPatch, path+"/code%3Apromo?update_mask=rateOverride", `{}`, http.StatusOK)).RatePhase
	if got.RateOverride != nil {
		t.Errorf("got rate_override = %+v, want it cleared", got.RateOverride)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:280")
	f.do(t, http.MethodPatch, path+"/code%3Aunknown", `{"name":"x"}`, http.StatusNotFound)

	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:299")
	deleted := decode[restRatePhaseBody](t, f.do(t, http.MethodDelete, path+"/code%3Apromo", nil, http.StatusOK)).RatePhase
	if deleted.Code != "promo" {
		t.Errorf("got deleted phase = %+v, want promo", deleted)
	}
	if got := phases(t); !slices.Equal(phaseCodesOf(got), []string{"intro", "default"}) || got[0].Position != 1 || got[1].Position != 2 {
		t.Errorf("got phases = %+v, want the tail shifted up", got)
	}
	t.Log("source: spec/requests/api/v2/plan_rate_cards/rate_phases_controller_spec.rb:310")
	f.fail(t, http.MethodDelete, path+"/code%3Adefault", nil, http.StatusBadRequest, "indefinite_phase_not_deletable")
}
