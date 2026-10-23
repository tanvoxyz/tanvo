package customers_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// fakeVIES answers every check with lookup or err.
type fakeVIES struct {
	lookup *tax.VIESLookup
	err    error
	calls  []string
}

func (f *fakeVIES) CheckVAT(_ context.Context, vat string) (*tax.VIESLookup, error) {
	f.calls = append(f.calls, vat)
	return f.lookup, f.err
}

// finalizer records the invoices it is asked to finalize.
type finalizer struct{ ids []core.InvoiceID }

func (f *finalizer) EnqueueFinalizePendingVIES(_ context.Context, _ port.Repos, inv *invoice.Invoice) error {
	f.ids = append(f.ids, inv.ID)
	return nil
}

func viesSetup(t *testing.T, eu bool) (*billingtest.Harness, *customers.Service, *fakeVIES, *finalizer) {
	t.Helper()
	h := billingtest.New(t, june22)
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.EUTaxManagement = eu; be.Address.Country = "FR" })
	v, f := &fakeVIES{}, &finalizer{}
	svc, err := customers.New(h.Deps, customers.Relations{
		Lifecycle: lifecycle.New(h.Deps),
		Coupons:   deduct.New(h.Deps),
		Wallets:   wallets.New(h.Deps),
	}, customers.WithVIES(v), customers.WithVIESInvoiceFinalizer(f))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, svc, v, f
}

func pendingVIES(h *billingtest.Harness, id core.CustomerID) *customer.PendingVIESCheck {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*customer.PendingVIESCheck, error) {
		p, err := r.PendingVIESChecks.Get(ctx, h.OrgID(), id)
		if errors.Is(err, core.ErrNotFound) {
			return nil, nil
		}
		return p, err
	})
}

func savePendingVIES(h *billingtest.Harness, c *customer.Customer, attempts int) {
	h.Do(func(ctx context.Context, r port.Repos) error {
		t := "unknown"
		return r.PendingVIESChecks.Save(ctx, &customer.PendingVIESCheck{
			OrganizationID: h.OrgID(), CustomerID: c.ID, BillingEntityID: c.BillingEntityID,
			TaxIdentificationNumber: c.TaxIdentificationNumber, AttemptsCount: attempts, LastErrorType: &t,
			CreatedAt: h.Now(), UpdatedAt: h.Now(),
		})
	})
}

// viesChecks returns the customer.vies_check payloads' vies_check.
func viesChecks(t *testing.T, h *billingtest.Harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range h.Emitted.Events() {
		if e.Type != webhook.EventTypeCustomerVIESCheck {
			continue
		}
		var p struct {
			VIESCheck map[string]any `json:"vies_check"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		out = append(out, p.VIESCheck)
	}
	return out
}

func runVIESJob(t *testing.T, h *billingtest.Harness, svc *customers.Service, id core.CustomerID) error {
	t.Helper()
	args, _ := json.Marshal(customers.VIESCheckArgs{CustomerID: id})
	ok, err := svc.HandleJob(h.Ctx, port.Job{Kind: customers.JobVIESCheck, OrganizationID: h.OrgID(), Args: args})
	if !ok {
		t.Fatalf("HandleJob = not handled, want = handled")
	}
	return err
}

func TestVIESScheduling(t *testing.T) {
	t.Run("a VAT number schedules a check", func(t *testing.T) {
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:83")
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:95")
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:101")
		h, svc, _, _ := viesSetup(t, true)
		h.Tax(tax.EUStandardTaxCode("FR"), "20")
		c, err := svc.Create(h.Ctx, h.OrgID(), "eu", customers.Input{
			TaxIdentificationNumber: core.Some(new("IT12345678901")),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(c.TaxIDs) != 0 {
			t.Errorf("got = %v, want = no tax until the check", c.TaxIDs)
		}
		p := pendingVIES(h, c.ID)
		if p == nil || p.AttemptsCount != 0 || *p.TaxIdentificationNumber != "IT12345678901" || p.BillingEntityID != c.BillingEntityID || p.LastErrorType != nil {
			t.Errorf("got = %+v, want = a fresh pending check", p)
		}
		js := jobs(h, customers.JobVIESCheck)
		if len(js) != 1 || js[0].Queue != port.QueueDefault || js[0].UniqueKey != "customer:"+c.ID.String() {
			t.Fatalf("got = %+v, want = one vies check on the default queue", js)
		}
		var a customers.VIESCheckArgs
		if err := json.Unmarshal(js[0].Args, &a); err != nil || a.CustomerID != c.ID {
			t.Errorf("got = %s, want = customer %s", js[0].Args, c.ID)
		}
	})

	t.Run("an existing check is reset", func(t *testing.T) {
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:111")
		h, svc, _, _ := viesSetup(t, true)
		c := h.Customer()
		savePendingVIES(h, c, 3)
		if _, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{TaxIdentificationNumber: core.Some(new("IT12345678901"))}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if p := pendingVIES(h, c.ID); p == nil || p.AttemptsCount != 0 || p.LastErrorType != nil {
			t.Errorf("got = %+v, want = attempts reset to 0", p)
		}
	})

	t.Run("no VAT number schedules nothing", func(t *testing.T) {
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:67")
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:73")
		h, svc, _, _ := viesSetup(t, true)
		de := h.Tax(tax.EUStandardTaxCode("DE"), "19")
		c, err := svc.Create(h.Ctx, h.OrgID(), "eu", customers.Input{BillingAddress: customers.Address{Country: core.Some("DE")}})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !slices.Equal(c.TaxIDs, []core.TaxID{de.ID}) || len(jobs(h, customers.JobVIESCheck)) != 0 {
			t.Errorf("got = %v and %d checks, want = [%s] and none", c.TaxIDs, len(jobs(h, customers.JobVIESCheck)), de.ID)
		}
	})

	t.Run("a territory cancels a pending check", func(t *testing.T) {
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:251")
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:256")
		t.Log("source: spec/services/customers/eu_auto_taxes_service_spec.rb:264")
		h, svc, _, _ := viesSetup(t, true)
		code := tax.ResolveEUTax(tax.EUTaxInput{
			BillingCountry: "FR",
			Customer:       tax.EUCustomer{Country: "ES", Zipcode: "35001", TaxIdentificationNumber: "IT12345678901"},
			At:             june22,
		}).TaxCode
		if code == "" {
			t.Fatalf("got = no territory tax for ES 35001, want = one")
		}
		h.Tax(code, "7")
		c := h.Customer()
		savePendingVIES(h, c, 1)
		_, err := svc.Update(h.Ctx, h.OrgID(), c.ID, customers.Input{
			TaxIdentificationNumber: core.Some(new("IT12345678901")),
			BillingAddress:          customers.Address{Country: core.Some("ES"), Zipcode: core.Some("35001")},
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if p := pendingVIES(h, c.ID); p != nil {
			t.Errorf("got = %+v, want = no pending check", p)
		}
		if n := len(jobs(h, customers.JobVIESCheck)); n != 0 || len(viesChecks(t, h)) != 0 {
			t.Errorf("got = %d checks, want = none and no customer.vies_check", n)
		}
	})
}

func TestVIESCheck(t *testing.T) {
	t.Run("VIES answers", func(t *testing.T) {
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:34")
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:58")
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:63")
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:68")
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:86")
		t.Log("source: spec/services/customers/vies_check_service_spec.rb:125")
		t.Log("source: spec/services/customers/vies_check_service_spec.rb:137")
		t.Log("source: spec/services/customers/vies_check_service_spec.rb:148")
		h, svc, v, f := viesSetup(t, true)
		fr := h.Tax(tax.EUStandardTaxCode("FR"), "20")
		other := h.Tax("other", "5")
		c := h.Customer(func(c *customer.Customer) {
			c.TaxIdentificationNumber = new("IE6388047V")
			c.TaxIDs = []core.TaxID{other.ID}
		})
		savePendingVIES(h, c, 2)
		d := draft(h, c, "2022-06-21", "2022-06-30")
		held := h.StoredInvoice(c, func(inv *invoice.Invoice) {
			inv.Status, inv.TaxStatus = invoice.StatusOpen, invoice.TaxStatusPending
		})
		h.StoredInvoice(c, func(inv *invoice.Invoice) {
			inv.Status, inv.TaxStatus = invoice.StatusOpen, invoice.TaxStatusSucceeded
		})
		h.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
		v.lookup = &tax.VIESLookup{CountryCode: "FR", VATNumber: "6388047V", Valid: true}
		if err := runVIESJob(t, h, svc, c.ID); err != nil {
			t.Fatalf("CheckVIES: %v", err)
		}
		if !slices.Equal(v.calls, []string{"IE6388047V"}) {
			t.Errorf("got = %v, want = one check of IE6388047V", v.calls)
		}
		if got := h.ReloadCustomer(c.ID).TaxIDs; !slices.Equal(got, []core.TaxID{fr.ID}) {
			t.Errorf("got = %v, want = [%s]", got, fr.ID)
		}
		if !reloadInvoice(h, d.ID).RefreshRequested {
			t.Errorf("got = not flagged, want = draft flagged for refresh")
		}
		if p := pendingVIES(h, c.ID); p != nil {
			t.Errorf("got = %+v, want = pending check deleted", p)
		}
		checks := viesChecks(t, h)
		if len(checks) != 1 || checks[0]["country_code"] != "FR" || checks[0]["valid"] != true {
			t.Errorf("got = %v, want = the VIES lookup", checks)
		}
		if !slices.Equal(f.ids, []core.InvoiceID{held.ID}) {
			t.Errorf("got = %v, want = [%s]", f.ids, held.ID)
		}
	})

	tests := []struct {
		name, source, country, zipcode, vatCountry string
		lookup                                     bool
		tin                                        string
		want                                       string
		wantCheck                                  map[string]any
	}{
		{
			name: "registered in another country", source: "spec/services/customers/vies_check_service_spec.rb:158",
			country: "DE", lookup: true, vatCountry: "DE", tin: "IT12345678901", want: tax.EUTaxCodeReverseCharge,
		},
		{
			name: "registered in a territory", source: "spec/services/customers/vies_check_service_spec.rb:179",
			country: "FR", zipcode: "97412", lookup: true, vatCountry: "FR", tin: "IT12345678901",
		},
		{
			name: "malformed number", source: "spec/services/customers/vies_check_service_spec.rb:108",
			country: "DE", tin: "invalid_vat_number", want: tax.EUStandardTaxCode("DE"),
			wantCheck: map[string]any{"valid": false, "valid_format": false},
		},
		{
			name: "unknown number without country", source: "spec/services/customers/vies_check_service_spec.rb:202",
			tin: "IT12345678901", want: tax.EUStandardTaxCode("FR"),
			wantCheck: map[string]any{"valid": false, "valid_format": true},
		},
		{
			name: "unknown number outside the EU", source: "spec/services/customers/vies_check_service_spec.rb:220",
			country: "US", tin: "IT12345678901", want: tax.EUTaxCodeExempt,
			wantCheck: map[string]any{"valid": false, "valid_format": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			h, svc, v, _ := viesSetup(t, true)
			want := tt.want
			if want == "" {
				want = tax.ResolveEUTaxAfterVIES(tax.EUTaxInput{
					BillingCountry: "FR", Customer: tax.EUCustomer{Country: tt.country, Zipcode: tt.zipcode}, At: june22,
				}, tax.VIESResult{Valid: true, CountryCode: tt.vatCountry})
				if want == tax.EUStandardTaxCode("FR") {
					t.Fatalf("got = the standard rate for %s %s, want = a territory", tt.country, tt.zipcode)
				}
			}
			wt := h.Tax(want, "1")
			c := h.Customer(func(c *customer.Customer) {
				c.TaxIdentificationNumber = &tt.tin
				c.BillingAddress.Country, c.BillingAddress.Zipcode = tt.country, tt.zipcode
			})
			if tt.lookup {
				v.lookup = &tax.VIESLookup{CountryCode: tt.vatCountry, Valid: true}
			}
			if err := runVIESJob(t, h, svc, c.ID); err != nil {
				t.Fatalf("CheckVIES: %v", err)
			}
			if got := h.ReloadCustomer(c.ID).TaxIDs; !slices.Equal(got, []core.TaxID{wt.ID}) {
				t.Errorf("got = %v, want = [%s] (%s)", got, wt.ID, want)
			}
			if tt.wantCheck != nil {
				if checks := viesChecks(t, h); len(checks) != 1 || !mapsEqual(checks[0], tt.wantCheck) {
					t.Errorf("got = %v, want = %v", checks, tt.wantCheck)
				}
			}
		})
	}

	t.Run("a missing EU tax leaves the taxes", func(t *testing.T) {
		h, svc, v, f := viesSetup(t, true)
		other := h.Tax("other", "5")
		c := h.Customer(func(c *customer.Customer) {
			c.TaxIdentificationNumber = new("IE6388047V")
			c.TaxIDs = []core.TaxID{other.ID}
		})
		held := h.StoredInvoice(c, func(inv *invoice.Invoice) {
			inv.Status, inv.TaxStatus = invoice.StatusOpen, invoice.TaxStatusPending
		})
		v.lookup = &tax.VIESLookup{CountryCode: "FR", Valid: true}
		if err := runVIESJob(t, h, svc, c.ID); err != nil {
			t.Fatalf("CheckVIES: %v", err)
		}
		if got := h.ReloadCustomer(c.ID).TaxIDs; !slices.Equal(got, []core.TaxID{other.ID}) {
			t.Errorf("got = %v, want = [%s]", got, other.ID)
		}
		if !slices.Equal(f.ids, []core.InvoiceID{held.ID}) {
			t.Errorf("got = %v, want = [%s]", f.ids, held.ID)
		}
	})

	t.Run("EU tax management disabled", func(t *testing.T) {
		t.Log("source: spec/services/customers/vies_check_service_spec.rb:92")
		h, svc, v, _ := viesSetup(t, false)
		c := h.Customer(func(c *customer.Customer) { c.TaxIdentificationNumber = new("IE6388047V") })
		if err := runVIESJob(t, h, svc, c.ID); err != nil {
			t.Fatalf("CheckVIES: %v", err)
		}
		if len(v.calls) != 0 || len(viesChecks(t, h)) != 0 || len(jobs(h, customers.JobVIESCheck)) != 0 {
			t.Errorf("got = %d calls, want = no check, webhook or retry", len(v.calls))
		}
	})
}

func TestVIESCheckFailure(t *testing.T) {
	tests := []struct {
		name, source string
		attempts     int
		errType      tax.VIESErrorType
		wantDelay    time.Duration
	}{
		{name: "first failure", source: "spec/services/customers/vies_check_service_spec.rb:43", errType: tax.VIESErrorTimeout, wantDelay: 5 * time.Minute},
		{name: "rate limit", source: "spec/services/customers/vies_check_service_spec.rb:230", errType: tax.VIESErrorRateLimit, wantDelay: 5 * time.Minute},
		{name: "HTTP error", source: "spec/services/customers/vies_check_service_spec.rb:245", errType: tax.VIESErrorServiceUnavailable, wantDelay: 5 * time.Minute},
		{name: "existing check", source: "spec/services/customers/vies_check_service_spec.rb:70", attempts: 2, errType: tax.VIESErrorBlocked, wantDelay: 10 * time.Minute},
		{name: "after five failures", attempts: 5, errType: tax.VIESErrorMemberStateUnavailable, wantDelay: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Log("source: " + tt.source)
			}
			t.Log("source: spec/services/customers/vies_check_service_spec.rb:28")
			t.Log("source: spec/services/customers/vies_check_service_spec.rb:37")
			t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:104")
			t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:108")
			t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:114")
			h, svc, v, f := viesSetup(t, true)
			other := h.Tax("other", "5")
			h.Tax(tax.EUStandardTaxCode("FR"), "20")
			c := h.Customer(func(c *customer.Customer) {
				c.TaxIdentificationNumber = new("IT12345678901")
				c.TaxIDs = []core.TaxID{other.ID}
			})
			if tt.attempts > 0 {
				savePendingVIES(h, c, tt.attempts)
			}
			h.StoredInvoice(c, func(inv *invoice.Invoice) {
				inv.Status, inv.TaxStatus = invoice.StatusOpen, invoice.TaxStatusPending
			})
			msg := "The VIES web service returned the error: SOMETHING"
			v.err = &tax.VIESError{Type: tt.errType, Message: msg}
			if err := runVIESJob(t, h, svc, c.ID); err != nil {
				t.Fatalf("CheckVIES: %v", err)
			}
			p := pendingVIES(h, c.ID)
			if p == nil || p.AttemptsCount != tt.attempts+1 || *p.LastErrorType != string(tt.errType) || *p.LastErrorMessage != msg ||
				p.LastAttemptAt == nil || *p.TaxIdentificationNumber != "IT12345678901" {
				t.Fatalf("got = %+v, want = attempt %d recorded", p, tt.attempts+1)
			}
			checks := viesChecks(t, h)
			want := map[string]any{"valid": false, "valid_format": true, "error": msg}
			if len(checks) != 1 || !mapsEqual(checks[0], want) {
				t.Errorf("got = %v, want = %v", checks, want)
			}
			js := jobs(h, customers.JobVIESCheck)
			if len(js) != 1 || !js[0].RunAt.Equal(h.Now().Add(tt.wantDelay)) {
				t.Fatalf("got = %+v, want = one retry in %s", js, tt.wantDelay)
			}
			if got := h.ReloadCustomer(c.ID).TaxIDs; !slices.Equal(got, []core.TaxID{other.ID}) {
				t.Errorf("got = %v, want = taxes kept", got)
			}
			if len(f.ids) != 0 {
				t.Errorf("got = %v, want = no finalization", f.ids)
			}
		})
	}

	t.Run("a retry is queued from the running check", func(t *testing.T) {
		t.Log("source: spec/jobs/customers/vies_check_job_spec.rb:142")
		h, svc, v, _ := viesSetup(t, true)
		c, err := svc.Create(h.Ctx, h.OrgID(), "eu", customers.Input{TaxIdentificationNumber: core.Some(new("IT12345678901"))})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		v.err = &tax.VIESError{Type: tax.VIESErrorTimeout, Message: "timeout"}
		if err := runVIESJob(t, h, svc, c.ID); err != nil {
			t.Fatalf("CheckVIES: %v", err)
		}
		if js := jobs(h, customers.JobVIESCheck); len(js) != 2 || js[1].UniqueKey == js[0].UniqueKey {
			t.Errorf("got = %+v, want = the first check and a retry with its own key", js)
		}
	})

	t.Run("an unknown fault fails the job", func(t *testing.T) {
		h, svc, v, _ := viesSetup(t, true)
		c := h.Customer(func(c *customer.Customer) { c.TaxIdentificationNumber = new("IT12345678901") })
		v.err = &tax.VIESError{Type: tax.VIESErrorUnknown, Message: "unknown"}
		if err := runVIESJob(t, h, svc, c.ID); err == nil {
			t.Fatalf("CheckVIES = nil, want = the VIES error")
		}
		if pendingVIES(h, c.ID) != nil || len(viesChecks(t, h)) != 0 {
			t.Errorf("got = a pending check or a webhook, want = neither")
		}
	})
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

// scenarioVIES knows every well-formed number, in the country of its
// prefix, or fails with err.
type scenarioVIES struct{ err error }

func (f *scenarioVIES) CheckVAT(_ context.Context, vat string) (*tax.VIESLookup, error) {
	if !tax.ValidVATNumberSyntax(vat) {
		return nil, nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return &tax.VIESLookup{CountryCode: vat[:2], VATNumber: vat, Valid: true}, nil
}

func TestEUTaxesScenario(t *testing.T) {
	h := billingtest.New(t, june22)
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.EUTaxManagement = true; be.Address.Country = "FR" })
	v := &scenarioVIES{}
	svc, err := customers.New(h.Deps, customers.Relations{}, customers.WithVIES(v))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	codes := map[string]core.TaxID{}
	for _, code := range []string{tax.EUTaxCodeExempt, tax.EUStandardTaxCode("FR"), tax.EUStandardTaxCode("IT"), tax.EUTaxCodeReverseCharge, "banking_rates"} {
		codes[code] = h.Tax(code, "1").ID
	}
	upsert := func(t *testing.T, externalID string, in customers.Input) *customer.Customer {
		t.Helper()
		c, _, err := svc.Upsert(h.Ctx, h.OrgID(), externalID, in)
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if p := pendingVIES(h, c.ID); p != nil {
			if err := runVIESJob(t, h, svc, c.ID); err != nil {
				t.Fatalf("CheckVIES: %v", err)
			}
		}
		return h.ReloadCustomer(c.ID)
	}
	address := func(country, zipcode string) customers.Address {
		return customers.Address{Country: core.Some(country), Zipcode: core.Some(zipcode)}
	}
	wantTax := func(t *testing.T, c *customer.Customer, code string) {
		t.Helper()
		if !slices.Equal(c.TaxIDs, []core.TaxID{codes[code]}) {
			t.Errorf("got = %v, want = [%s]", c.TaxIDs, code)
		}
	}
	lastCheck := func(t *testing.T) map[string]any {
		t.Helper()
		checks := viesChecks(t, h)
		if len(checks) == 0 {
			t.Fatalf("got = no customer.vies_check, want = one")
		}
		return checks[len(checks)-1]
	}

	t.Run("customers created after the feature was enabled", func(t *testing.T) {
		t.Log("source: spec/scenarios/customers/customer_eu_taxes_spec.rb:54")
		wantTax(t, upsert(t, "user_usa_123", customers.Input{BillingAddress: address("US", "80095")}), tax.EUTaxCodeExempt)
		fr := upsert(t, "user_fr_123", customers.Input{BillingAddress: address("FR", "75018")})
		wantTax(t, fr, tax.EUStandardTaxCode("FR"))
		it := upsert(t, "user_it_123", customers.Input{BillingAddress: address("IT", "00146")})
		wantTax(t, it, tax.EUStandardTaxCode("IT"))

		it = upsert(t, "user_it_123", customers.Input{TaxIdentificationNumber: core.Some(new("IT123"))})
		wantTax(t, it, tax.EUStandardTaxCode("IT"))
		if got, want := lastCheck(t), map[string]any{"valid": false, "valid_format": false}; !mapsEqual(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}

		it = upsert(t, "user_it_123", customers.Input{TaxIdentificationNumber: core.Some(new("IT12345678901"))})
		wantTax(t, it, tax.EUTaxCodeReverseCharge)
		if got := lastCheck(t); got["country_code"] != "IT" || got["vat_number"] != "IT12345678901" {
			t.Errorf("got = %v, want = the IT lookup", got)
		}

		fr = upsert(t, "user_fr_123", customers.Input{TaxIdentificationNumber: core.Some(new("FR12345678901"))})
		wantTax(t, fr, tax.EUStandardTaxCode("FR"))

		it = upsert(t, "user_it_123", customers.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("banking_rates")})})
		wantTax(t, it, "banking_rates")

		it = upsert(t, "user_it_123", customers.Input{TaxIdentificationNumber: core.Some[*string](nil)})
		wantTax(t, it, tax.EUStandardTaxCode("IT"))
	})

	t.Run("VIES returns an error", func(t *testing.T) {
		t.Log("source: spec/scenarios/customers/customer_eu_taxes_spec.rb:112")
		c := upsert(t, "user_fr_err", customers.Input{BillingAddress: address("FR", "75018")})
		wantTax(t, c, tax.EUStandardTaxCode("FR"))
		v.err = &tax.VIESError{Type: tax.VIESErrorRateLimit, Message: "The VIES web service returned the error: rate limit exceeded"}
		before := len(jobs(h, customers.JobVIESCheck))
		c = upsert(t, "user_fr_err", customers.Input{TaxIdentificationNumber: core.Some(new("FR12345678901"))})
		wantTax(t, c, tax.EUStandardTaxCode("FR"))
		if n := len(jobs(h, customers.JobVIESCheck)) - before; n != 2 {
			t.Errorf("got = %d checks queued, want = the check and its retry", n)
		}
		want := map[string]any{"valid": false, "valid_format": true, "error": "The VIES web service returned the error: rate limit exceeded"}
		if got := lastCheck(t); !mapsEqual(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
}
