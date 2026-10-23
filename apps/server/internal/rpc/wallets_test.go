package rpc_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1/walletv1connect"
)

// walletFixture is a server with a USD customer.
type walletFixture struct {
	s    *rpctest.Server
	c    walletv1connect.WalletServiceClient
	cust *customer.Customer
}

func newWalletFixture(t *testing.T) *walletFixture {
	t.Helper()
	s := rpctest.New(t)
	return &walletFixture{s: s, c: walletv1connect.NewWalletServiceClient(s.Connect(s.APIKey)), cust: s.Fixtures.Customer()}
}

// walletInput is a USD wallet of the fixture's customer at rate 1.
func (f *walletFixture) walletInput() *walletv1.Wallet {
	return walletv1.Wallet_builder{
		CustomerId: new(rpc.ExternalIDPrefix + f.cust.ExternalID),
		Name:       new("Prepaid"), Currency: new("USD"), RateAmount: new("1"),
	}.Build()
}

func (f *walletFixture) create(t *testing.T, w *walletv1.Wallet) (*walletv1.Wallet, error) {
	t.Helper()
	resp, err := f.c.CreateWallet(t.Context(), walletv1.CreateWalletRequest_builder{Wallet: w}.Build())
	return resp.GetWallet(), err
}

func walletWantViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	for _, v := range rpc.ErrorViolations(err) {
		if v.Field == field && v.Reason == reason {
			return
		}
	}
	t.Fatalf("got err = %v (violations %+v), want = %s %s", err, rpc.ErrorViolations(err), field, reason)
}

func walletWantNotFound(t *testing.T, err error, reason string) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeNotFound || rpc.ErrorReason(err) != reason {
		t.Fatalf("got err = %v (reason %q), want = NotFound %s", err, rpc.ErrorReason(err), reason)
	}
}

// source: spec/requests/api/v1/wallets_controller_spec.rb:15
// source: spec/requests/api/v1/wallets_controller_spec.rb:22
func TestCreateWallet(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	in := f.walletInput()
	in.SetGrantedCredits("10")
	in.SetPaidTopUpMinAmount("5.00")
	in.SetAllowedFeeTypes([]commonv1.FeeType{commonv1.FeeType_FEE_TYPE_CHARGE})
	in.SetRecurringTransactionRules([]*walletv1.RecurringTransactionRule{walletv1.RecurringTransactionRule_builder{
		Trigger:  new(walletv1.RecurringTrigger_RECURRING_TRIGGER_INTERVAL),
		Interval: new(commonv1.Interval_INTERVAL_MONTHLY), GrantedCredits: new("5"),
	}.Build()})
	w, err := f.create(t, in)
	if err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	if w.GetCustomerId() != f.cust.ID.String() || w.GetCurrency() != "USD" || w.GetStatus() != walletv1.WalletStatus_WALLET_STATUS_ACTIVE ||
		w.GetCode() != "prepaid" || w.GetPriority() != 50 {
		t.Errorf("got %v, want an active USD wallet prepaid of the customer at priority 50", w)
	}
	if w.GetCreditsBalance() != "10.00000" || w.GetBalanceAmount().GetAmount() != "10.00" || w.GetPaidTopUpMinAmount() != "5.00" {
		t.Errorf("got balance = %s %v, min = %s, want = 10 credits, 10.00, 5.00", w.GetCreditsBalance(), w.GetBalanceAmount(), w.GetPaidTopUpMinAmount())
	}
	if ft := w.GetAllowedFeeTypes(); len(ft) != 1 || ft[0] != commonv1.FeeType_FEE_TYPE_CHARGE {
		t.Errorf("got allowed_fee_types = %v, want = charge", ft)
	}
	if rs := w.GetRecurringTransactionRules(); len(rs) != 1 || rs[0].GetInterval() != commonv1.Interval_INTERVAL_MONTHLY || rs[0].GetGrantedCredits() != "5" {
		t.Errorf("got rules = %v, want one monthly rule granting 5", rs)
	}

	t.Run("customer not found", func(t *testing.T) {
		in := f.walletInput()
		in.SetCustomerId("external_id:missing")
		_, err := f.create(t, in)
		walletWantViolation(t, err, "wallet.customer_id", "customer_not_found")
	})
	t.Run("billing entity not found", func(t *testing.T) {
		in := f.walletInput()
		in.SetBillingEntityId("code:missing")
		_, err := f.create(t, in)
		walletWantNotFound(t, err, "billing_entity_not_found")
	})
	t.Run("unknown metric", func(t *testing.T) {
		in := f.walletInput()
		in.SetBillableMetricIds([]string{"code:missing"})
		_, err := f.create(t, in)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("got err = %v, want = InvalidArgument", err)
		}
	})
}

// source: spec/requests/api/v1/wallets_controller_spec.rb:53
// source: spec/requests/api/v1/wallets_controller_spec.rb:69
// source: spec/requests/api/v1/wallets_controller_spec.rb:79
// source: spec/requests/api/v1/wallets_controller_spec.rb:97
// source: spec/requests/api/v1/wallets_controller_spec.rb:114
func TestGetAndListWallets(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	usd := f.s.Fixtures.Wallet(f.cust, "10")
	eur := f.s.Fixtures.Wallet(f.cust, "0", func(w *wallet.Wallet) { w.Currency = money.MustCurrency("EUR") })
	other := f.s.Fixtures.Wallet(f.s.Fixtures.Customer(), "0")

	got, err := f.c.GetWallet(ctx, walletv1.GetWalletRequest_builder{WalletId: new(usd.ID.String())}.Build())
	if err != nil || got.GetWallet().GetId() != usd.ID.String() || got.GetWallet().GetCreditsBalance() != "10" {
		t.Errorf("GetWallet = %v, %v, want the USD wallet", got, err)
	}
	_, err = f.c.GetWallet(ctx, walletv1.GetWalletRequest_builder{WalletId: new(f.cust.ID.String())}.Build())
	walletWantNotFound(t, err, "wallet_not_found")

	list := func(customer, filter string) ([]string, error) {
		resp, err := f.c.ListWallets(ctx, walletv1.ListWalletsRequest_builder{CustomerId: &customer, Filter: &filter}.Build())
		var ids []string
		for _, w := range resp.GetWallets() {
			ids = append(ids, w.GetId())
		}
		return ids, err
	}
	ids, err := list(rpc.ExternalIDPrefix+f.cust.ExternalID, "")
	if err != nil || len(ids) != 2 || ids[0] != usd.ID.String() || ids[1] != eur.ID.String() {
		t.Errorf("got %v, %v, want = the customer's two wallets", ids, err)
	}
	ids, err = list(f.cust.ID.String(), `currency = "EUR"`)
	if err != nil || len(ids) != 1 || ids[0] != eur.ID.String() {
		t.Errorf("got %v, %v, want = the EUR wallet", ids, err)
	}
	be := `billing_entity_id = "code:` + f.s.Fixtures.BillingEntity.Code + `"`
	ids, err = list(f.cust.ID.String(), be)
	if err != nil || len(ids) != 2 {
		t.Errorf("got %v, %v, want = both wallets of the default billing entity", ids, err)
	}
	_, err = list(f.cust.ID.String(), `billing_entity_id = "code:missing"`)
	walletWantNotFound(t, err, "billing_entity_not_found")
	_, err = list("external_id:missing", "")
	walletWantNotFound(t, err, "customer_not_found")
	_ = other
}

// source: spec/requests/api/v1/wallets_controller_spec.rb:39
// source: spec/requests/api/v1/wallets_controller_spec.rb:61
func TestUpdateAndTerminateWallet(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	w := f.s.Fixtures.Wallet(f.cust, "10", func(w *wallet.Wallet) { w.Name = new("Old") })
	update := func(in *walletv1.Wallet, mask ...string) (*walletv1.Wallet, error) {
		req := walletv1.UpdateWalletRequest_builder{Wallet: in}.Build()
		if mask != nil {
			req.SetUpdateMask(&fieldmaskpb.FieldMask{Paths: mask})
		}
		resp, err := f.c.UpdateWallet(ctx, req)
		return resp.GetWallet(), err
	}
	got, err := update(walletv1.Wallet_builder{Id: new(w.ID.String()), Name: new("New"), Priority: new(int32(5)), PaidTopUpMaxAmount: new("100")}.Build())
	if err != nil {
		t.Fatalf("UpdateWallet: %v", err)
	}
	if got.GetName() != "New" || got.GetPriority() != 5 || got.GetPaidTopUpMaxAmount() != "100.00" {
		t.Errorf("got %v, want name New, priority 5, max 100.00", got)
	}
	got, err = update(walletv1.Wallet_builder{Id: new(w.ID.String())}.Build(), "paid_top_up_max_amount", "name")
	if err != nil || got.HasPaidTopUpMaxAmount() || got.HasName() {
		t.Errorf("got %v, %v, want the maximum and the name cleared", got, err)
	}
	_, err = update(walletv1.Wallet_builder{Id: new(f.cust.ID.String()), Name: new("x")}.Build())
	walletWantNotFound(t, err, "wallet_not_found")

	term, err := f.c.TerminateWallet(ctx, walletv1.TerminateWalletRequest_builder{WalletId: new(w.ID.String())}.Build())
	if err != nil || term.GetWallet().GetStatus() != walletv1.WalletStatus_WALLET_STATUS_TERMINATED || !term.GetWallet().HasTerminatedAt() {
		t.Fatalf("TerminateWallet = %v, %v, want a terminated wallet", term, err)
	}
	_, err = update(walletv1.Wallet_builder{Id: new(w.ID.String()), Name: new("x")}.Build())
	walletWantViolation(t, err, "wallet.wallet_id", "wallet_is_terminated")
}

// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:38
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:67
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:84
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:225
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:262
func TestCreateWalletTransaction(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	w := f.s.Fixtures.Wallet(f.cust, "20", func(w *wallet.Wallet) { w.PaidTopUpMinAmount = new(int64(1000)) })
	create := func(req *walletv1.CreateWalletTransactionRequest) ([]*walletv1.WalletTransaction, error) {
		resp, err := f.c.CreateWalletTransaction(ctx, req)
		return resp.GetWalletTransactions(), err
	}
	ts, err := create(walletv1.CreateWalletTransactionRequest_builder{
		WalletId: new(w.ID.String()), GrantedCredits: new("10"),
		Metadata: []*commonv1.MetadataItem{commonv1.MetadataItem_builder{Key: new("tag"), Value: new("promo")}.Build()},
	}.Build())
	if err != nil || len(ts) != 1 {
		t.Fatalf("got %v, %v, want = one transaction", ts, err)
	}
	if g := ts[0]; g.GetTransactionStatus() != walletv1.TransactionStatus_TRANSACTION_STATUS_GRANTED || g.GetStatus() != walletv1.SettlementStatus_SETTLEMENT_STATUS_SETTLED ||
		g.GetCreditAmount() != "10.00000" || g.GetAmount().GetAmount() != "10.00" || len(g.GetMetadata()) != 1 || g.GetSource() != walletv1.TransactionSource_TRANSACTION_SOURCE_MANUAL {
		t.Errorf("got %v, want a settled manual grant of 10 credits with its metadata", g)
	}

	ts, err = create(walletv1.CreateWalletTransactionRequest_builder{WalletId: new(w.ID.String()), VoidedCredits: new("5")}.Build())
	if err != nil || len(ts) != 1 || ts[0].GetTransactionStatus() != walletv1.TransactionStatus_TRANSACTION_STATUS_VOIDED || ts[0].GetTransactionType() != walletv1.TransactionType_TRANSACTION_TYPE_OUTBOUND {
		t.Errorf("got %v, %v, want = one outbound void", ts, err)
	}

	_, err = create(walletv1.CreateWalletTransactionRequest_builder{WalletId: new(w.ID.String()), PaidCredits: new("1")}.Build())
	walletWantViolation(t, err, "paid_credits", wallet.CodeBelowMinimum)

	_, err = create(walletv1.CreateWalletTransactionRequest_builder{WalletId: new(f.cust.ID.String()), GrantedCredits: new("1")}.Build())
	walletWantViolation(t, err, "wallet_id", "wallet_not_found")
}

// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:313
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:344
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:357
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:371
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:419
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:475
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:493
// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:515
func TestListWalletTransactions(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	w := f.s.Fixtures.Wallet(f.cust, "0")
	granted := f.s.Fixtures.InboundTransaction(w, wallet.TransactionStatusGranted, "10", func(t *wallet.Transaction) {
		t.Metadata = append(t.Metadata, core.MetadataItem{Key: "tag", Value: "promo"})
	})
	purchased := f.s.Fixtures.InboundTransaction(w, wallet.TransactionStatusPurchased, "5", func(t *wallet.Transaction) {
		t.Status = wallet.SettlementStatusPending
		t.SettledAt = nil
	})
	list := func(filter string) ([]string, error) {
		resp, err := f.c.ListWalletTransactions(ctx, walletv1.ListWalletTransactionsRequest_builder{WalletId: new(w.ID.String()), Filter: &filter}.Build())
		var ids []string
		for _, t := range resp.GetWalletTransactions() {
			ids = append(ids, t.GetId())
		}
		return ids, err
	}
	for _, c := range []struct {
		filter string
		want   []string
	}{
		{"", []string{granted.ID.String(), purchased.ID.String()}},
		{"status = PENDING", []string{purchased.ID.String()}},
		{"transaction_type = INBOUND", []string{granted.ID.String(), purchased.ID.String()}},
		{"transaction_status = GRANTED", []string{granted.ID.String()}},
		{`metadata.tag = "promo"`, []string{granted.ID.String()}},
		{`metadata.tag = "promo" AND transaction_status = PURCHASED`, nil},
	} {
		got, err := list(c.filter)
		if err != nil || len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("filter %q: got %v, %v, want = %v", c.filter, got, err, c.want)
		}
	}
	if _, err := list("status = BOGUS"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("got err = %v, want = InvalidArgument", err)
	}
	_, err := f.c.ListWalletTransactions(ctx, walletv1.ListWalletTransactionsRequest_builder{WalletId: new(f.cust.ID.String())}.Build())
	walletWantNotFound(t, err, "wallet_not_found")

	got, err := f.c.GetWalletTransaction(ctx, walletv1.GetWalletTransactionRequest_builder{WalletTransactionId: new(granted.ID.String())}.Build())
	if err != nil || got.GetWalletTransaction().GetId() != granted.ID.String() || got.GetWalletTransaction().GetWalletId() != w.ID.String() {
		t.Errorf("GetWalletTransaction = %v, %v, want the grant", got, err)
	}
	_, err = f.c.GetWalletTransaction(ctx, walletv1.GetWalletTransactionRequest_builder{WalletTransactionId: new(w.ID.String())}.Build())
	walletWantNotFound(t, err, "wallet_transaction_not_found")
}

// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb (consumptions, payment_url)
func TestWalletTransactionConsumptionsAndPaymentURL(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	w := f.s.Fixtures.Wallet(f.cust, "0")
	granted := f.s.Fixtures.InboundTransaction(w, wallet.TransactionStatusGranted, "10")
	_, err := f.c.ListWalletTransactionConsumptions(ctx, walletv1.ListWalletTransactionConsumptionsRequest_builder{WalletTransactionId: new(granted.ID.String())}.Build())
	walletWantViolation(t, err, "wallet", "not_traceable")

	tw := f.s.Fixtures.Wallet(f.cust, "0", func(w *wallet.Wallet) { w.Traceable = true })
	inbound := f.s.Fixtures.InboundTransaction(tw, wallet.TransactionStatusGranted, "10")
	resp, err := f.c.ListWalletTransactionConsumptions(ctx, walletv1.ListWalletTransactionConsumptionsRequest_builder{WalletTransactionId: new(inbound.ID.String())}.Build())
	if err != nil || len(resp.GetConsumptions()) != 0 {
		t.Errorf("got %v, %v, want = no consumptions", resp, err)
	}

	// source: spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:23
	_, err = f.c.GenerateWalletTransactionPaymentUrl(ctx, walletv1.GenerateWalletTransactionPaymentUrlRequest_builder{WalletTransactionId: new(granted.ID.String())}.Build())
	walletWantViolation(t, err, "base", "wallet_transaction_not_purchased")
	_, err = f.c.GenerateWalletTransactionPaymentUrl(ctx, walletv1.GenerateWalletTransactionPaymentUrlRequest_builder{WalletTransactionId: new(w.ID.String())}.Build())
	walletWantNotFound(t, err, "wallet_transaction_not_found")
}

// source: spec/requests/api/v1/wallets_controller_spec.rb:39 (recurring_transaction_rules)
func TestReplaceRecurringTransactionRules(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	ctx := t.Context()
	in := f.walletInput()
	in.SetRecurringTransactionRules([]*walletv1.RecurringTransactionRule{walletv1.RecurringTransactionRule_builder{
		Trigger: new(walletv1.RecurringTrigger_RECURRING_TRIGGER_INTERVAL), Interval: new(commonv1.Interval_INTERVAL_MONTHLY), GrantedCredits: new("5"),
	}.Build()})
	w, err := f.create(t, in)
	if err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	ruleID := w.GetRecurringTransactionRules()[0].GetId()
	replace := func(rules ...*walletv1.RecurringTransactionRule) ([]*walletv1.RecurringTransactionRule, error) {
		resp, err := f.c.ReplaceRecurringTransactionRules(ctx, walletv1.ReplaceRecurringTransactionRulesRequest_builder{
			WalletId: new(w.GetId()), RecurringTransactionRules: rules,
		}.Build())
		return resp.GetRecurringTransactionRules(), err
	}
	rules, err := replace(walletv1.RecurringTransactionRule_builder{
		Id: &ruleID, Trigger: new(walletv1.RecurringTrigger_RECURRING_TRIGGER_THRESHOLD), ThresholdCredits: new("2"), GrantedCredits: new("8"),
	}.Build())
	if err != nil || len(rules) != 1 || rules[0].GetId() != ruleID || rules[0].GetTrigger() != walletv1.RecurringTrigger_RECURRING_TRIGGER_THRESHOLD || rules[0].GetGrantedCredits() != "8" {
		t.Fatalf("got %v, %v, want the rule updated to a threshold rule granting 8", rules, err)
	}
	rules, err = replace()
	if err != nil || len(rules) != 0 {
		t.Errorf("got %v, %v, want = no active rule", rules, err)
	}
	_, err = replace(walletv1.RecurringTransactionRule_builder{Trigger: new(walletv1.RecurringTrigger_RECURRING_TRIGGER_INTERVAL)}.Build())
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("got err = %v, want = InvalidArgument (interval missing)", err)
	}
}

// TestWalletsREST checks the REST routes of the wallets.
func TestWalletsREST(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/customers/external_id:"+f.cust.ExternalID+"/wallets",
		`{"name":"Prepaid","currency":"USD","rate_amount":"1","granted_credits":"3"}`)
	if status != http.StatusOK {
		t.Fatalf("POST wallets = %d %s, want = 200", status, body)
	}
	w := decode[struct {
		Wallet struct {
			ID             string `json:"id"`
			CreditsBalance string `json:"credits_balance"`
		} `json:"wallet"`
	}](t, body).Wallet
	if w.CreditsBalance != "3.00000" {
		t.Errorf("got %+v, want 3 credits", w)
	}
	status, body = f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/wallets/"+w.ID+"/transactions", nil)
	if n := len(decode[struct {
		WalletTransactions []struct{} `json:"wallet_transactions"`
	}](t, body).WalletTransactions); status != http.StatusOK || n != 1 {
		t.Errorf("GET transactions = %d %s, want = 200 with the grant", status, body)
	}
	status, body = f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/wallets/"+w.ID+":terminate", "{}")
	if status != http.StatusOK {
		t.Errorf("POST :terminate = %d %s, want = 200", status, body)
	}
}
