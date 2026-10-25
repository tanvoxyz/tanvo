package cli_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// TestServeBilling runs every role over PostgreSQL with a billing clock
// in the past: a customer subscribed on March 1st to a monthly plan of
// $10 with an in-arrears charge of $2 per API call and an in-advance
// charge of $5 per seat sends events over the EventService; the seat is
// billed at once, and on April 1st the billing clock job bills March.
func TestServeBilling(t *testing.T) {
	dsn := startPostgres(t)
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server logs:\n%s", logs.String())
		}
	})
	environ := []string{
		"TANVO_DATABASE_URL=" + dsn,
		"TANVO_HTTP_ADDR=127.0.0.1:" + port,
		"TANVO_MIGRATE_ON_START=false",
		"TANVO_SHUTDOWN_TIMEOUT=10s",
		"TANVO_LOG_FORMAT=text",
		"OTEL_SDK_DISABLED=true",
	}
	ctx := t.Context()
	migrate(t, dsn, environ, logs)

	// The catalog and the subscription, through the store.
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Pool().Close)
	h := billingtest.NewOn(t, time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC), st, &webhooks.Emitter{Tx: st})
	cust := h.Customer()
	plan := h.Plan(period.Monthly, 1000)
	calls := h.Metric("api_calls", catalog.AggregationTypeCount, "")
	seats := h.Metric("seats", catalog.AggregationTypeCount, "")
	callsCharge := h.Charge(plan, calls, catalog.ChargeModelStandard, `{"amount":"2"}`)
	seatsCharge := h.Charge(plan, seats, catalog.ChargeModelStandard, `{"amount":"5"}`, func(c *catalog.Charge) { c.PayInAdvance = true })
	sub := h.Subscription(cust, plan, billingtest.SubscribedAt(time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)))
	keys := auth.NewAPIKeys(auth.Deps{Tx: st, Clock: core.SystemClock{}, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}, auth.NewUsageTracker())
	key, err := keys.Create(ctx, h.OrgID(), nil, nil)
	if err != nil {
		t.Fatalf("create API key: %v", err)
	}

	srv, base, stop := serveWith(t, environ, logs, h.Clock)

	// Three API calls and a seat, over the Connect EventService.
	var evs []string
	for i, code := range []string{"api_calls", "api_calls", "seats", "api_calls"} {
		at := time.Date(2024, 3, 5, 9, i, 0, 0, time.UTC)
		evs = append(evs, fmt.Sprintf(`{"transaction_id":"tx-%d","external_subscription_id":%q,"code":%q,"timestamp":%q}`, i, sub.ExternalID, code, at.Format(time.RFC3339)))
	}
	body := `{"events":[` + strings.Join(evs, ",") + `]}`
	if status, resp := post(t, base+"/tanvo.event.v1.EventService/IngestEvents", key.Secret, body); status != http.StatusOK {
		t.Fatalf("IngestEvents status got = %d, want = 200; body %s", status, resp)
	}

	// The seat's in-advance invoice is finalized at once.
	advance := waitInvoices(t, h, cust.ID, 1)
	checkInvoice(t, h, advance[0], invoice.TypeSubscription, 500, map[string]int64{"charge:" + seatsCharge.ID.String(): 500})

	// On April 1st the billing clock job bills March in arrears.
	h.Clock.Set(time.Date(2024, 4, 1, 1, 0, 0, 0, time.UTC))
	if err := srv.Jobs().Trigger(ctx, jobs.KindBillSubscriptions); err != nil {
		t.Fatalf("Trigger(%s) error = %v", jobs.KindBillSubscriptions, err)
	}
	all := waitInvoices(t, h, cust.ID, 2)
	checkInvoice(t, h, all[1], invoice.TypeSubscription, 1600, map[string]int64{"subscription": 1000, "charge:" + callsCharge.ID.String(): 600})

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The domain events of the webhooks.
	var events []string
	rows, err := pool.Query(ctx, "SELECT DISTINCT event_type FROM domain_events WHERE organization_id = $1 ORDER BY 1", h.OrgID().String())
	if err != nil {
		t.Fatalf("domain_events: %v", err)
	}
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fee.created", "invoice.created"} {
		if !slices.Contains(events, want) {
			t.Errorf("domain events got = %v, want = %s among them", events, want)
		}
	}
	// The jobs of the invoices: a payment and a document each, the
	// payments completed without a provider and the documents completed
	// without a blob store (nothing is rendered).
	for _, tt := range []struct {
		kind, state string
	}{
		{"payments.create_invoice_payment", "completed"},
		{"documents.generate_invoice", "completed"},
		{"billing.create_in_advance_charge_fees", "completed"},
	} {
		err := dockertest.Wait(ctx, 30*time.Second, func(ctx context.Context) error {
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM river.river_job WHERE kind = $1 AND state = $2", tt.kind, tt.state).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("no %s job %s", tt.kind, tt.state)
			}
			return nil
		})
		if err != nil {
			t.Errorf("river jobs: got = %v, want = a %s job %s", err, tt.kind, tt.state)
		}
	}

	stop()
}

// waitInvoices waits for the customer's n finalized invoices and returns
// them in creation order.
func waitInvoices(t *testing.T, h *billingtest.Harness, c core.CustomerID, n int) []*invoice.Invoice {
	t.Helper()
	var got []*invoice.Invoice
	err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
		got = got[:0]
		for _, inv := range h.Invoices(c) {
			if inv.Status == invoice.StatusFinalized {
				got = append(got, inv)
			}
		}
		if len(got) < n {
			return fmt.Errorf("%d finalized invoices", len(got))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("invoices: got = %v, want = %d finalized", err, n)
	}
	slices.SortFunc(got, func(a, b *invoice.Invoice) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	return got
}

// checkInvoice checks an invoice's type, total and fee amounts, keyed
// "subscription" or "charge:<charge ID>".
func checkInvoice(t *testing.T, h *billingtest.Harness, inv *invoice.Invoice, typ invoice.Type, total int64, fees map[string]int64) {
	t.Helper()
	if inv.Type != typ || inv.TotalAmount != total {
		t.Errorf("invoice %s: got = %s of %d, want = %s of %d", inv.ID, inv.Type, inv.TotalAmount, typ, total)
	}
	got := map[string]int64{}
	for _, f := range h.Fees(inv.ID) {
		key := string(f.Type)
		if f.Type == core.FeeTypeCharge {
			key += ":" + f.ChargeID.String()
		}
		got[key] += f.Amount
	}
	for k, v := range got {
		if v == 0 {
			delete(got, k)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(fees) {
		t.Errorf("invoice %s fees: got = %v, want = %v", inv.ID, got, fees)
	}
}
