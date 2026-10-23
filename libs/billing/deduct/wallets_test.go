package deduct_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

type inboundSpec struct {
	status  wallet.TransactionStatus
	credits string
}

type walletSpec struct {
	name      string
	balance   string // credits, rate 1
	priority  int
	feeTypes  []core.FeeType
	metric    bool // targets metric 0
	ccy       string
	traceable bool
	inbound   []inboundSpec
}

type feeSpec struct {
	typ    core.FeeType
	metric int // charge fees: index of the metric
	amount int64
	tax    int64  // fee tax, booked as one tax row
	exact  string // precise tax, defaults to tax
	cn     int64  // credit-note share already applied
}

// Wallets of the lago specs: priority 49 is consumed before the default
// 50.
var (
	normalWallet       = walletSpec{name: "normal", balance: "10"}
	priorityWallet     = walletSpec{name: "priority", balance: "10", priority: 49}
	chargeWallet       = walletSpec{name: "limited charge", balance: "10", feeTypes: []core.FeeType{core.FeeTypeCharge}}
	priorityCharge     = walletSpec{name: "priority limited charge", balance: "10", priority: 49, feeTypes: []core.FeeType{core.FeeTypeCharge}}
	subscriptionWallet = walletSpec{name: "limited subscription", balance: "10", feeTypes: []core.FeeType{core.FeeTypeSubscription}}
	prioritySub        = walletSpec{name: "priority limited subscription", balance: "10", priority: 49, feeTypes: []core.FeeType{core.FeeTypeSubscription}}
	metricWallet       = walletSpec{name: "limited bm", balance: "10", metric: true}
	priorityMetric     = walletSpec{name: "priority limited bm", balance: "10", priority: 49, metric: true}
	sixWallets         = []walletSpec{normalWallet, priorityWallet, chargeWallet, priorityCharge, subscriptionWallet, prioritySub}
	eightWallets       = []walletSpec{normalWallet, subscriptionWallet, prioritySub, metricWallet, priorityMetric, priorityCharge, priorityWallet, chargeWallet}
)

type prepaidCase struct {
	name     string
	wallets  []walletSpec
	fees     []feeSpec
	total    int64 // invoice total left
	taxes    int64 // invoice taxes
	provider bool  // the taxes are provider taxes booked as one invoice tax
	want     map[string]int64
}

type prepaidScene struct {
	f       *fixture
	inv     *invoice.Invoice
	wallets map[string]*wallet.Wallet
	inbound map[string][]*wallet.Transaction
	applied []*invoice.AppliedTax
}

func newPrepaidScene(t *testing.T, tt prepaidCase) *prepaidScene {
	t.Helper()
	f := newFixture(t)
	h := f.h
	p := h.Plan(period.Monthly, 0)
	var charges []*catalog.Charge
	for i := range 10 {
		m := h.Metric("m"+string(rune('a'+i)), catalog.AggregationTypeCount, "")
		charges = append(charges, h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`))
	}
	sc := &prepaidScene{f: f, wallets: map[string]*wallet.Wallet{}, inbound: map[string][]*wallet.Transaction{}}
	for _, ws := range tt.wallets {
		w := h.Wallet(f.cust, ws.balance, func(w *wallet.Wallet) {
			w.Name = &ws.name
			if ws.priority != 0 {
				w.Priority = ws.priority
			}
			w.AllowedFeeTypes = ws.feeTypes
			if ws.metric {
				w.BillableMetricIDs = []core.BillableMetricID{charges[0].BillableMetricID}
			}
			if ws.ccy != "" {
				w.Currency = money.MustCurrency(ws.ccy)
			}
			w.Traceable = ws.traceable
		})
		for _, in := range ws.inbound {
			sc.inbound[ws.name] = append(sc.inbound[ws.name], h.InboundTransaction(w, in.status, in.credits))
			h.Clock.Advance(time.Second)
		}
		sc.wallets[ws.name] = w
		h.Clock.Advance(time.Minute)
	}
	sc.inv = h.StoredInvoice(f.cust)
	for _, fs := range tt.fees {
		h.StoredFee(sc.inv, fs.amount, func(fe *invoice.Fee) {
			fe.Type = fs.typ
			if fs.typ == core.FeeTypeCharge {
				fe.ChargeID = charges[fs.metric].ID
			}
			if fs.tax > 0 || fs.exact != "" {
				exact := money.NewFromInt(fs.tax)
				if fs.exact != "" {
					exact = money.MustParse(fs.exact)
				}
				row := invoice.FeeAppliedTax{
					ID: core.NewID[core.FeeAppliedTaxID](h.IDs), TaxCode: "vat", TaxName: "VAT",
					TaxRate: money.NewFromInt(10), Amount: fs.tax, PreciseAmount: exact,
				}
				if !tt.provider {
					row.TaxID = core.NewID[core.TaxID](h.IDs)
				}
				fe.AppliedTaxes = []invoice.FeeAppliedTax{row}
				fe.TaxesAmount, fe.TaxesPreciseAmount = fs.tax, exact
			}
			fe.PreciseCreditNotesAmount = money.NewFromInt(fs.cn)
		})
		h.Clock.Advance(time.Second)
	}
	sc.applied = []*invoice.AppliedTax{}
	if tt.provider {
		var base int64
		for _, fs := range tt.fees {
			base += fs.amount
		}
		sc.applied = append(sc.applied, &invoice.AppliedTax{
			ID: core.NewID[core.InvoiceAppliedTaxID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: sc.inv.ID,
			TaxCode: "vat", TaxName: "VAT", TaxRate: money.NewFromInt(10), FeesAmount: base, Amount: tt.taxes,
			PreciseAmount: money.NewFromInt(tt.taxes), Currency: sc.inv.Currency,
		})
	}
	sc.inv.TaxesAmount = tt.taxes
	sc.inv.SubTotalIncludingTaxesAmount = tt.total
	return sc
}

// apply runs ApplyAfterTaxes and saves the invoice.
func (sc *prepaidScene) apply(opts deduct.Options) (deduct.AfterTaxes, error) {
	var out deduct.AfterTaxes
	err := sc.f.h.Deps.Tx.InTx(sc.f.h.Ctx, func(ctx context.Context, r port.Repos) error {
		fees, err := r.Fees.ListByInvoice(ctx, sc.inv.OrganizationID, sc.inv.ID)
		if err != nil {
			return err
		}
		if out, err = sc.f.s.ApplyAfterTaxes(ctx, r, sc.f.env(), sc.inv, fees, sc.applied, opts); err != nil {
			return err
		}
		if opts.Preview {
			return nil
		}
		return save(ctx, r, sc.inv, fees)
	})
	return out, err
}

// allocate runs AllocatePrepaidCredits over the scene's wallets.
func (sc *prepaidScene) allocate(t *testing.T) map[string]int64 {
	t.Helper()
	got := map[string]int64{}
	sc.f.h.Do(func(ctx context.Context, r port.Repos) error {
		fees, err := r.Fees.ListByInvoice(ctx, sc.inv.OrganizationID, sc.inv.ID)
		if err != nil {
			return err
		}
		ws, err := r.Wallets.ListActiveForApplication(ctx, sc.inv.OrganizationID, sc.inv.CustomerID)
		if err != nil {
			return err
		}
		var usable []*wallet.Wallet
		for _, w := range ws {
			if w.Currency.Code() == sc.inv.Currency.Code() {
				usable = append(usable, w)
			}
		}
		out, err := deduct.AllocatePrepaidCredits(ctx, r, sc.inv, fees, sc.applied, usable, sc.inv.SubTotalIncludingTaxesAmount)
		if err != nil {
			return err
		}
		for _, wa := range out {
			n, _ := wa.Amount.Round(0, money.HalfUp).Int64()
			got[*wa.Wallet.Name] = n
		}
		return nil
	})
	return got
}

func prepaidCases() []prepaidCase {
	sub := core.FeeTypeSubscription
	charge := core.FeeTypeCharge
	var lastFee []feeSpec
	for i := range 10 {
		lastFee = append(lastFee, feeSpec{typ: charge, metric: (i + 1) % 10, amount: 60, tax: 8, exact: "8.456"})
	}
	return []prepaidCase{
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:68
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:67
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:84
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:94
		{
			name: "priority wallet", wallets: sixWallets, fees: []feeSpec{{typ: charge, metric: 1, amount: 100}},
			total: 100, want: map[string]int64{"priority": 100},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:91
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:74
		{name: "no wallets", fees: []feeSpec{{typ: charge, metric: 1, amount: 100}}, total: 100, want: map[string]int64{}},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:104
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:135
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:150
		{
			name: "priority wallet drained first", wallets: sixWallets, fees: []feeSpec{{typ: charge, metric: 1, amount: 1500}},
			total: 1500, want: map[string]int64{"priority": 1000, "priority limited charge": 500},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:122
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:186
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:196
		{
			name: "fee types single wallet", wallets: sixWallets,
			fees:  []feeSpec{{typ: sub, amount: 60, tax: 6}, {typ: charge, metric: 1, amount: 40, tax: 4}},
			total: 110, taxes: 10, want: map[string]int64{"priority": 110},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:133
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:219
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:240
		{
			name: "fee types split", wallets: sixWallets,
			fees:  []feeSpec{{typ: sub, amount: 3500, tax: 100}, {typ: charge, metric: 1, amount: 1500, tax: 50}},
			total: 5150, taxes: 150,
			want: map[string]int64{
				"priority": 1000, "priority limited charge": 1000, "priority limited subscription": 1000,
				"normal": 1000, "limited charge": 550, "limited subscription": 600,
			},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:177
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:281
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:296
		{
			name: "metric targets", wallets: eightWallets,
			fees:  []feeSpec{{typ: sub, amount: 60, tax: 6}, {typ: charge, metric: 0, amount: 40, tax: 4}},
			total: 110, taxes: 10, want: map[string]int64{"priority limited subscription": 66, "priority limited bm": 44},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:206
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:332
		{
			name: "precise fee decimals", wallets: eightWallets,
			fees:  []feeSpec{{typ: sub, amount: 60, tax: 6}, {typ: charge, metric: 0, amount: 44, tax: 4, exact: "4.4"}},
			total: 114, taxes: 10, want: map[string]int64{"priority limited subscription": 66, "priority limited bm": 48},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:345
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:365
		{
			name: "metric targets drained", wallets: eightWallets,
			fees:  []feeSpec{{typ: sub, amount: 2000, tax: 200}, {typ: charge, metric: 0, amount: 1000, tax: 100}},
			total: 3300, taxes: 300,
			want: map[string]int64{
				"priority limited subscription": 1000, "priority limited bm": 1000, "priority limited charge": 100,
				"priority": 1000, "normal": 200,
			},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:240
		{
			name: "wallet limited to the last fee", wallets: []walletSpec{{name: "bm", balance: "0.66", metric: true}},
			fees: lastFee, total: 680, taxes: 80, want: map[string]int64{"bm": 66},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:408
		{
			name: "wallet limited to the last fee, funded", wallets: []walletSpec{{name: "bm", balance: "66", metric: true}},
			fees: lastFee, total: 680, taxes: 80, want: map[string]int64{"bm": 68},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:271
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:646
		{
			name: "precise tax rounding", wallets: []walletSpec{{name: "normal", balance: "2000"}},
			fees: []feeSpec{
				{typ: charge, metric: 1, amount: 50_000, tax: 3413, exact: "3412.7"},
				{typ: charge, metric: 1, amount: 50_000, tax: 3413, exact: "3412.7"},
			},
			total: 106_826, taxes: 6826, want: map[string]int64{"normal": 106_826},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:306
		{
			name: "provider taxes in one bucket", wallets: []walletSpec{normalWallet}, provider: true,
			fees: []feeSpec{
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"},
			},
			total: 412, taxes: 12, want: map[string]int64{"normal": 412},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:315
		{
			name: "provider taxes with credit notes", wallets: []walletSpec{normalWallet}, provider: true,
			fees: []feeSpec{
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5", cn: 1},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5", cn: 1},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5", cn: 1},
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5", cn: 1},
			},
			total: 408, taxes: 12, want: map[string]int64{"normal": 408},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:323
		{
			name: "subscription wallet on charge fees", wallets: []walletSpec{subscriptionWallet}, provider: true,
			fees: []feeSpec{
				{typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"}, {typ: charge, metric: 1, amount: 100, tax: 3, exact: "2.5"},
			},
			total: 206, taxes: 6, want: map[string]int64{},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:330
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:660
		{
			name: "wallet currency", wallets: []walletSpec{{name: "eur", balance: "10", ccy: "EUR"}, {name: "usd", balance: "10"}},
			fees: []feeSpec{{typ: charge, metric: 1, amount: 100}}, total: 100, want: map[string]int64{"usd": 100},
		},
		// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:343
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:675
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:748
		{
			name: "no wallet in the currency", wallets: []walletSpec{{name: "eur", balance: "10", ccy: "EUR"}},
			fees: []feeSpec{{typ: charge, metric: 1, amount: 100}}, total: 100, want: map[string]int64{},
		},
	}
}

func TestAllocatePrepaidCredits(t *testing.T) {
	for _, tt := range prepaidCases() {
		t.Run(tt.name, func(t *testing.T) {
			sc := newPrepaidScene(t, tt)
			got := sc.allocate(t)
			eq(t, "wallets used", len(got), len(tt.want))
			for name, want := range tt.want {
				eq(t, "allocation of "+name, got[name], want)
			}
			// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:73
			for name, w := range sc.wallets {
				eq(t, "untouched balance of "+name, sc.f.h.ReloadWallet(w.ID).BalanceAmount, w.BalanceAmount)
			}
			eq(t, "webhooks", len(sc.f.h.Webhooks()), 0)
		})
	}
}

func TestApplyPrepaidCredits(t *testing.T) {
	for _, tt := range prepaidCases() {
		t.Run(tt.name, func(t *testing.T) {
			sc := newPrepaidScene(t, tt)
			out, err := sc.apply(deduct.Options{})
			mustOK(t, err)
			var sum int64
			for _, v := range tt.want {
				sum += v
			}
			eq(t, "prepaid credit", out.PrepaidCredit, sum)
			eq(t, "invoice prepaid credit", sc.inv.PrepaidCreditAmount, sum)
			eq(t, "total", sc.inv.TotalAmount, tt.total-sum)
			eq(t, "transactions", len(out.WalletTransactions), len(tt.want))
			stored := sc.f.h.WalletTransactions(sc.inv.ID)
			eq(t, "stored transactions", len(stored), len(tt.want))
			for _, tx := range out.WalletTransactions {
				eq(t, "type", tx.TransactionType, wallet.TransactionTypeOutbound)
				eq(t, "status", tx.TransactionStatus, wallet.TransactionStatusInvoiced)
				eqDec(t, "credit amount", tx.CreditAmount, kernelCredits(tx.Amount))
			}
			for name, w := range sc.wallets {
				got := sc.f.h.ReloadWallet(w.ID)
				eq(t, "balance of "+name, got.BalanceAmount, w.BalanceAmount-tt.want[name])
				eqDec(t, "credits balance of "+name, got.CreditsBalance, kernelCredits(w.BalanceAmount-tt.want[name]))
			}
			var created int
			for _, e := range sc.f.h.Webhooks() {
				if e == webhook.EventTypeWalletTransactionCreated {
					created++
				}
			}
			// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:116
			eq(t, "wallet_transaction.created", created, len(tt.want))
		})
	}
}

// kernelCredits returns the credits of a USD amount at rate 1.
func kernelCredits(minor int64) string {
	return money.NewFromInt(minor).Div(money.NewFromInt(100)).String()
}

// source: spec/services/credits/allocate_prepaid_credits_by_wallets_service_spec.rb:73
func TestApplyPrepaidCreditsPreview(t *testing.T) {
	tt := prepaidCases()[0]
	sc := newPrepaidScene(t, tt)
	out, err := sc.apply(deduct.Options{Preview: true})
	mustOK(t, err)
	eq(t, "prepaid credit", out.PrepaidCredit, 100)
	eq(t, "stored transactions", len(sc.f.h.WalletTransactions(sc.inv.ID)), 0)
	eq(t, "balance", sc.f.h.ReloadWallet(sc.wallets["priority"].ID).BalanceAmount, 1000)
	eq(t, "webhooks", len(sc.f.h.Webhooks()), 0)
}

// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:159
func TestApplyPrepaidCreditsAlreadyApplied(t *testing.T) {
	tt := prepaidCases()[0]
	sc := newPrepaidScene(t, tt)
	_, err := sc.apply(deduct.Options{})
	mustOK(t, err)
	sc.inv.PrepaidCreditAmount = 0
	_, err = sc.apply(deduct.Options{})
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != deduct.CodeAlreadyApplied {
		t.Fatalf("got error = %v, want = %s", err, deduct.CodeAlreadyApplied)
	}
}

func TestApplyPrepaidCreditsTraceable(t *testing.T) {
	granted, purchased := wallet.TransactionStatusGranted, wallet.TransactionStatusPurchased
	tests := []struct {
		name          string
		wallets       []walletSpec
		total         int64
		wantGranted   int64
		wantPurchased int64
		wantRemaining map[string][]int64
	}{
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:432
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:436
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:445
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:451
		{
			name:    "granted",
			wallets: []walletSpec{{name: "traceable", balance: "10", traceable: true, inbound: []inboundSpec{{granted, "10"}}}},
			total:   100, wantGranted: 100, wantRemaining: map[string][]int64{"traceable": {900}},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:473
		{
			name:    "purchased",
			wallets: []walletSpec{{name: "traceable", balance: "10", traceable: true, inbound: []inboundSpec{{purchased, "10"}}}},
			total:   100, wantPurchased: 100, wantRemaining: map[string][]int64{"traceable": {900}},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:514
		{
			name:    "granted and purchased",
			wallets: []walletSpec{{name: "traceable", balance: "10", traceable: true, inbound: []inboundSpec{{granted, "3"}, {purchased, "7"}}}},
			total:   500, wantGranted: 300, wantPurchased: 200, wantRemaining: map[string][]int64{"traceable": {0, 500}},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:529
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:533
		{
			name:    "not traceable",
			wallets: []walletSpec{{name: "plain", balance: "10"}},
			total:   100,
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:563
		{
			name: "traceable and not traceable",
			wallets: []walletSpec{
				{name: "traceable", balance: "5", traceable: true, inbound: []inboundSpec{{granted, "5"}}},
				{name: "plain", balance: "5"},
			},
			total: 100, wantRemaining: map[string][]int64{"traceable": {400}},
		},
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:609
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:613
		// source: spec/services/credits/applied_prepaid_credits_service_spec.rb:620
		{
			name: "two traceable wallets",
			wallets: []walletSpec{
				{name: "one", balance: "3", priority: 1, traceable: true, inbound: []inboundSpec{{granted, "3"}}},
				{name: "two", balance: "4", priority: 2, traceable: true, inbound: []inboundSpec{{purchased, "4"}}},
			},
			total: 500, wantGranted: 300, wantPurchased: 200, wantRemaining: map[string][]int64{"one": {0}, "two": {200}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := newPrepaidScene(t, prepaidCase{
				wallets: tt.wallets, fees: []feeSpec{{typ: core.FeeTypeCharge, metric: 1, amount: tt.total}}, total: tt.total,
			})
			out, err := sc.apply(deduct.Options{})
			mustOK(t, err)
			eq(t, "prepaid", out.PrepaidCredit, tt.total)
			eq(t, "granted", sc.inv.PrepaidGrantedCreditAmount, tt.wantGranted)
			eq(t, "purchased", sc.inv.PrepaidPurchasedCreditAmount, tt.wantPurchased)
			eq(t, "result granted", out.PrepaidGrantedCredit, tt.wantGranted)
			eq(t, "result purchased", out.PrepaidPurchasedCredit, tt.wantPurchased)
			for name, want := range tt.wantRemaining {
				for i, in := range sc.inbound[name] {
					got := sc.f.h.ReloadWalletTransaction(in.ID)
					if got.RemainingAmount == nil {
						t.Fatalf("remaining of %s: got = nil, want = %d", name, want[i])
					}
					eq(t, "remaining of "+name, *got.RemainingAmount, want[i])
				}
			}
			consumptions := 0
			sc.f.h.Do(func(ctx context.Context, r port.Repos) error {
				for _, tx := range out.WalletTransactions {
					cs, err := r.WalletTransactions.ListConsumptionsByOutbound(ctx, sc.f.h.OrgID(), tx.ID)
					if err != nil {
						return err
					}
					consumptions += len(cs)
				}
				return nil
			})
			var want int
			for _, w := range tt.wallets {
				if w.traceable {
					want += len(w.inbound)
				}
			}
			if tt.name == "traceable and not traceable" {
				want = 1
			}
			eq(t, "consumptions", consumptions, want)
		})
	}
}
