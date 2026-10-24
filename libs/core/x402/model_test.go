package x402_test

import (
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

const (
	evmPayout = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
	svmPayout = "2wKupLR9q6wXYppw8Gr2NvWxKBUqm4PPJKkQfoxHDBg4"
	evmPayer  = "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359"
	usdcBase  = "0x036CbD53842c5426634e7929541eC2318f3dCF7e"
)

var testOrg = core.NewID[core.OrganizationID](core.UUIDv7Generator{})

// connection is the connection factory.
//
// source: spec/factories/x402/connections.rb
func connection(networks []x402.Network, payouts map[x402.Family]string) *x402.Connection {
	return &x402.Connection{
		OrganizationID: testOrg, Code: "coinbase_cdp_1", Name: "Coinbase CDP", Facilitator: x402.FacilitatorCoinbaseCDP,
		Asset: x402.AssetUSDC, Networks: networks, PayoutAddresses: payouts, AutoCreateCustomers: true, Secrets: []byte("sealed"),
	}
}

func fieldCodes(err error, field string) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		if e.Field == field {
			out = append(out, e.Code)
		}
	}
	return out
}

func TestConnectionValidate(t *testing.T) {
	t.Parallel()
	evm := []x402.Network{"eip155:84532"}
	evmOnly := map[x402.Family]string{x402.FamilyEVM: evmPayout}
	for _, c := range []struct {
		name     string
		networks []x402.Network
		payouts  map[x402.Family]string
		field    string
		want     []string
	}{
		// source: spec/models/x402/connection_spec.rb:72-76
		{"unsupported network", []x402.Network{"eip155:1"}, evmOnly, "networks", []string{core.CodeInvalid}},
		// source: spec/models/x402/connection_spec.rb:78-82
		{"mixed environments", []x402.Network{"eip155:8453", "eip155:84532"}, evmOnly, "networks", []string{x402.CodeMixedEnvironments}},
		// source: spec/models/x402/connection_spec.rb:84-89
		{"mainnets of both families", []x402.Network{"eip155:8453", "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"}, map[x402.Family]string{x402.FamilyEVM: evmPayout, x402.FamilySVM: svmPayout}, "", nil},
		// source: spec/models/x402/connection_spec.rb:91-95
		{"no networks", nil, evmOnly, "networks", []string{core.CodeRequired}},
		// source: spec/models/x402/connection_spec.rb:101-105
		{"svm network without svm payout", []x402.Network{"eip155:84532", "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"}, evmOnly, "payout_addresses", []string{x402.CodeMissingSVMPayoutAddress}},
		// source: spec/models/x402/connection_spec.rb:107-111
		{"evm network without evm payout", evm, map[x402.Family]string{x402.FamilySVM: svmPayout}, "payout_addresses", []string{x402.CodeMissingEVMPayoutAddress}},
		// source: spec/models/x402/connection_spec.rb:113-117
		{"wrong checksum", evm, map[x402.Family]string{x402.FamilyEVM: "0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"}, "payout_addresses", []string{x402.CodeInvalidChecksum}},
		// source: spec/models/x402/connection_spec.rb:134-138
		{"truncated", evm, map[x402.Family]string{x402.FamilyEVM: "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeA"}, "payout_addresses", []string{core.CodeInvalidFormat}},
		// source: spec/models/x402/connection_spec.rb:140-145
		{"signature as svm address", []x402.Network{"solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1"}, map[x402.Family]string{x402.FamilySVM: x402.Base58Encode(sig(1))}, "payout_addresses", []string{core.CodeInvalidFormat}},
		// source: spec/models/x402/connection_spec.rb:147-151
		{"unknown family", evm, map[x402.Family]string{x402.FamilyEVM: evmPayout, "sui": evmPayout}, "payout_addresses", []string{core.CodeInvalid}},
		// source: spec/models/x402/connection_spec.rb:153-163
		{"no payouts", evm, nil, "payout_addresses", []string{core.CodeInvalid}},
		// source: spec/models/x402/connection_spec.rb:174-178
		{"payout of an unused family", evm, map[x402.Family]string{x402.FamilyEVM: evmPayout, x402.FamilySVM: svmPayout}, "", nil},
	} {
		conn := connection(c.networks, c.payouts)
		conn.Normalize()
		err := conn.Validate()
		if c.field == "" {
			if err != nil {
				t.Errorf("%s: got = %v, want = valid", c.name, err)
			}
			continue
		}
		if got := fieldCodes(err, c.field); !slices.Equal(got, c.want) {
			t.Errorf("%s: got = %v (%v), want = %v", c.name, got, err, c.want)
		}
	}
}

func TestConnectionNormalize(t *testing.T) {
	t.Parallel()
	// source: spec/models/x402/connection_spec.rb:119-132, 165-172
	for _, a := range []string{"0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed", "0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED"} {
		c := connection([]x402.Network{"eip155:84532"}, map[x402.Family]string{x402.FamilyEVM: a})
		c.Normalize()
		if err := c.Validate(); err != nil || c.PayoutAddresses[x402.FamilyEVM] != evmPayout {
			t.Errorf("%s: got = %s, %v, want = %s", a, c.PayoutAddresses[x402.FamilyEVM], err, evmPayout)
		}
	}
	// source: spec/models/x402/connection_spec.rb:40-46
	c := connection([]x402.Network{"eip155:84532"}, map[x402.Family]string{x402.FamilyEVM: evmPayout})
	c.Code, c.Name, c.Secrets = "", "", nil
	err := c.Validate()
	for _, f := range []string{"code", "name", "secrets"} {
		if got := fieldCodes(err, f); !slices.Equal(got, []string{core.CodeRequired}) {
			t.Errorf("%s: got = %v, want = [%s]", f, got, core.CodeRequired)
		}
	}
	if err := (x402.Credentials{}).Validate(); len(fieldCodes(err, "cdp_api_key_id")) != 1 || len(fieldCodes(err, "cdp_api_key_secret")) != 1 {
		t.Errorf("credentials: got = %v, want = both required", err)
	}
}

// settlement is the settlement factory.
//
// source: spec/factories/x402/settlements.rb
func settlement() *x402.Settlement {
	hash := "0x" + "ab"
	return &x402.Settlement{
		OrganizationID: testOrg, ConnectionID: core.NewID[core.X402ConnectionID](core.UUIDv7Generator{}),
		Kind: x402.SettlementKindCreditPurchase, Status: x402.SettlementStatusSettled, SettledBy: x402.SettledByTanvo,
		Network: "eip155:84532", Asset: usdcBase, PayerAddress: evmPayer, PayeeAddress: evmPayout,
		SettledAmountAtomic: big.NewInt(1_000_000), SettledAmountCents: 100, TransactionHash: &hash, PaymentDigest: "digest",
		PurchaseSettings: x402.Object{"plan_code": "agent_api", "wallet_code": "agent_credits"}, Payload: x402.Object{},
	}
}

func TestSettlementValidate(t *testing.T) {
	t.Parallel()
	soon := time.Now().Add(5 * time.Minute)
	type tc struct {
		name   string
		change func(*x402.Settlement)
		field  string
		want   string
	}
	for _, c := range []tc{
		// source: spec/models/x402/settlement_spec.rb:69-74
		{"no asset", func(s *x402.Settlement) { s.Asset = "" }, "asset", core.CodeRequired},
		{"no digest", func(s *x402.Settlement) { s.PaymentDigest = "" }, "payment_digest", core.CodeRequired},
		{"zero amount", func(s *x402.Settlement) { s.SettledAmountAtomic = big.NewInt(0) }, "settled_amount_atomic", core.CodeNotPositive},
		// source: spec/models/x402/settlement_spec.rb:76-90
		{"invoice payment without invoice", func(s *x402.Settlement) {
			s.Kind, s.PurchaseSettings = x402.SettlementKindInvoicePayment, nil
		}, "invoice", core.CodeRequired},
		// source: spec/models/x402/settlement_spec.rb:92-104
		{"no purchase settings", func(s *x402.Settlement) { s.PurchaseSettings = nil }, "purchase_settings", core.CodeRequired},
		{"empty purchase settings", func(s *x402.Settlement) { s.PurchaseSettings = x402.Object{} }, "purchase_settings", core.CodeRequired},
		// source: spec/models/x402/settlement_spec.rb:113-123
		{"unsupported network", func(s *x402.Settlement) { s.Network = "eip155:1" }, "network", core.CodeInvalid},
		// source: spec/models/x402/settlement_spec.rb:133-137
		{"malformed payer", func(s *x402.Settlement) { s.PayerAddress = "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d3" }, "payer_address", core.CodeInvalidFormat},
		// source: spec/models/x402/settlement_spec.rb:139-143
		{"svm payee on evm", func(s *x402.Settlement) { s.PayeeAddress = svmPayout }, "payee_address", core.CodeInvalidFormat},
		// source: spec/models/x402/settlement_spec.rb:229-233 (check_x402_settlements_settled_has_hash)
		{"settled without hash", func(s *x402.Settlement) { s.TransactionHash = nil }, "transaction_hash", core.CodeRequired},
		// source: spec/models/x402/settlement_spec.rb:235-239 (check_x402_settlements_pending_has_reconcile_after)
		{"pending without reconcile", func(s *x402.Settlement) { s.Status, s.TransactionHash = x402.SettlementStatusPending, nil }, "reconcile_after", core.CodeRequired},
		// source: spec/models/x402/settlement_spec.rb:253-257 (check_x402_settlements_merchant_settled_credit_purchase)
		{"pending merchant", func(s *x402.Settlement) {
			s.Status, s.TransactionHash, s.ReconcileAfter, s.SettledBy = x402.SettlementStatusPending, nil, &soon, x402.SettledByMerchant
		}, "settled_by", core.CodeForbidden},
		// source: spec/models/x402/settlement_spec.rb:259-263
		{"merchant invoice payment", func(s *x402.Settlement) {
			s.Kind, s.InvoiceID, s.SettledBy = x402.SettlementKindInvoicePayment, core.NewID[core.InvoiceID](core.UUIDv7Generator{}), x402.SettledByMerchant
		}, "settled_by", core.CodeForbidden},
		// source: spec/models/x402/settlement_spec.rb:271-275 (check_x402_settlements_granted_has_subscription)
		{"granted without subscription", func(s *x402.Settlement) {
			s.WalletTransactionID = core.NewID[core.WalletTransactionID](core.UUIDv7Generator{})
		}, "subscription", core.CodeRequired},
	} {
		s := settlement()
		c.change(s)
		s.Normalize()
		err := s.Validate()
		if got := fieldCodes(err, c.field); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s: got = %v (%v), want = [%s]", c.name, got, err, c.want)
		}
		// source: spec/models/x402/settlement_spec.rb:117-119
		if c.name == "unsupported network" && len(core.FieldErrors(err)) != 1 {
			t.Errorf("%s: got = %v, want = the network only", c.name, err)
		}
	}

	// source: spec/models/x402/settlement_spec.rb:145-154
	s := settlement()
	s.PayerAddress, s.PayeeAddress = "0xfb6916095ca1df60bb79ce92ce3ea74c37c5d359", "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"
	s.Normalize()
	if err := s.Validate(); err != nil || s.PayerAddress != evmPayer || s.PayeeAddress != evmPayout {
		t.Errorf("lowercase: got = %s, %s, %v", s.PayerAddress, s.PayeeAddress, err)
	}

	// source: spec/models/x402/settlement_spec.rb:157-166
	s = settlement()
	s.Network, s.Asset = "solana:EtWTRABZaYq6iMfeYKouRu166VU2xqa1", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU"
	s.PayerAddress, s.PayeeAddress = "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", svmPayout
	s.Normalize()
	if err := s.Validate(); err != nil || s.PayerAddress != "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA" {
		t.Errorf("solana: got = %s, %v", s.PayerAddress, err)
	}

	// source: spec/models/x402/settlement_spec.rb:265-269
	s = settlement()
	s.SettledBy = x402.SettledByMerchant
	if err := s.Validate(); err != nil {
		t.Errorf("merchant credit purchase: got = %v, want = valid", err)
	}
}

func TestSettlementValidateConnection(t *testing.T) {
	t.Parallel()
	conn := connection([]x402.Network{"eip155:84532"}, map[x402.Family]string{x402.FamilyEVM: evmPayout})
	for _, c := range []struct {
		name   string
		change func(*x402.Settlement)
		field  string
		want   string
	}{
		// source: spec/models/x402/settlement_spec.rb:174-180
		{"another organization", func(s *x402.Settlement) { s.OrganizationID = core.NewID[core.OrganizationID](core.UUIDv7Generator{}) }, "x402_connection", x402.CodeSameOrganization},
		// source: spec/models/x402/settlement_spec.rb:182-188
		{"network not offered", func(s *x402.Settlement) {
			s.Network, s.Asset = "eip155:8453", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
		}, "network", x402.CodeNotOfferedByConnection},
		// source: spec/models/x402/settlement_spec.rb:190-194
		{"other payee", func(s *x402.Settlement) { s.PayeeAddress = evmPayer }, "payee_address", x402.CodeNotConnectionPayoutAddress},
		// source: spec/models/x402/settlement_spec.rb:196-200
		{"other asset", func(s *x402.Settlement) { s.Asset = evmPayout }, "asset", x402.CodeNotConnectionAsset},
	} {
		s := settlement()
		c.change(s)
		s.Normalize()
		if got := fieldCodes(s.ValidateConnection(conn), c.field); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s: got = %v, want = [%s]", c.name, got, c.want)
		}
	}
	// source: spec/models/x402/settlement_spec.rb:202-209
	s := settlement()
	s.Asset = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	s.Normalize()
	if err := s.ValidateConnection(conn); err != nil || s.Asset != usdcBase {
		t.Errorf("lowercase asset: got = %s, %v", s.Asset, err)
	}
}

func TestSettlementSameFacts(t *testing.T) {
	t.Parallel()
	// source: spec/models/x402/settlement_spec.rb:229-246
	base := settlement()
	for name, change := range map[string]func(*x402.Settlement){
		"payer":    func(s *x402.Settlement) { s.PayerAddress = evmPayout },
		"payee":    func(s *x402.Settlement) { s.PayeeAddress = evmPayer },
		"digest":   func(s *x402.Settlement) { s.PaymentDigest = "other" },
		"settings": func(s *x402.Settlement) { s.PurchaseSettings = x402.Object{} },
		"invoice":  func(s *x402.Settlement) { s.InvoiceID = core.NewID[core.InvoiceID](core.UUIDv7Generator{}) },
	} {
		next := *base
		change(&next)
		if base.SameFacts(&next) {
			t.Errorf("%s: got = same facts, want = changed", name)
		}
	}
	next := *base
	reason := "settle_timeout"
	next.Status, next.ErrorReason = x402.SettlementStatusFailed, &reason
	if !base.SameFacts(&next) {
		t.Error("status and reason: got = changed facts, want = same")
	}
}
