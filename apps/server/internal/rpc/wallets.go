package rpc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1/walletv1connect"
)

// WalletPaymentURLs makes the checkout URL of an invoice at its customer's
// payment provider.
type WalletPaymentURLs interface {
	// InvoicePaymentURL returns the payment URL of the invoice id.
	InvoicePaymentURL(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (string, error)
}

// Wallets serves tanvo.wallet.v1.WalletService over the billing library's
// wallet service (lago Api::V1::WalletsController,
// Api::V1::Customers::WalletsController and
// Api::V1::WalletTransactionsController). ListRecurringTransactionRules
// and TerminateRecurringTransactionRule are not served: the rules are
// read with their wallet and replaced as a whole.
type Wallets struct {
	walletv1connect.UnimplementedWalletServiceHandler

	svc  *wallets.Service
	urls WalletPaymentURLs
}

var _ walletv1connect.WalletServiceHandler = (*Wallets)(nil)

// NewWallets returns the service over svc; urls makes the payment URLs of
// the purchases, and without it GenerateWalletTransactionPaymentUrl is
// Unimplemented once the transaction is checked.
func NewWallets(svc *wallets.Service, urls WalletPaymentURLs) *Wallets {
	return &Wallets{svc: svc, urls: urls}
}

var (
	walletStatuses = NewEnum(map[wallet.Status]walletv1.WalletStatus{
		wallet.StatusActive:     walletv1.WalletStatus_WALLET_STATUS_ACTIVE,
		wallet.StatusTerminated: walletv1.WalletStatus_WALLET_STATUS_TERMINATED,
	})
	walletTransactionTypes = NewEnum(map[wallet.TransactionType]walletv1.TransactionType{
		wallet.TransactionTypeInbound:  walletv1.TransactionType_TRANSACTION_TYPE_INBOUND,
		wallet.TransactionTypeOutbound: walletv1.TransactionType_TRANSACTION_TYPE_OUTBOUND,
	})
	walletTransactionStatuses = NewEnum(map[wallet.TransactionStatus]walletv1.TransactionStatus{
		wallet.TransactionStatusPurchased: walletv1.TransactionStatus_TRANSACTION_STATUS_PURCHASED,
		wallet.TransactionStatusGranted:   walletv1.TransactionStatus_TRANSACTION_STATUS_GRANTED,
		wallet.TransactionStatusVoided:    walletv1.TransactionStatus_TRANSACTION_STATUS_VOIDED,
		wallet.TransactionStatusInvoiced:  walletv1.TransactionStatus_TRANSACTION_STATUS_INVOICED,
	})
	walletSettlementStatuses = NewEnum(map[wallet.SettlementStatus]walletv1.SettlementStatus{
		wallet.SettlementStatusPending: walletv1.SettlementStatus_SETTLEMENT_STATUS_PENDING,
		wallet.SettlementStatusSettled: walletv1.SettlementStatus_SETTLEMENT_STATUS_SETTLED,
		wallet.SettlementStatusFailed:  walletv1.SettlementStatus_SETTLEMENT_STATUS_FAILED,
	})
	walletTransactionSources = NewEnum(map[wallet.TransactionSource]walletv1.TransactionSource{
		wallet.TransactionSourceManual:    walletv1.TransactionSource_TRANSACTION_SOURCE_MANUAL,
		wallet.TransactionSourceInterval:  walletv1.TransactionSource_TRANSACTION_SOURCE_INTERVAL,
		wallet.TransactionSourceThreshold: walletv1.TransactionSource_TRANSACTION_SOURCE_THRESHOLD,
	})
	walletRuleTriggers = NewEnum(map[wallet.RecurringTrigger]walletv1.RecurringTrigger{
		wallet.RecurringTriggerInterval:  walletv1.RecurringTrigger_RECURRING_TRIGGER_INTERVAL,
		wallet.RecurringTriggerThreshold: walletv1.RecurringTrigger_RECURRING_TRIGGER_THRESHOLD,
	})
	walletRuleMethods = NewEnum(map[wallet.RecurringMethod]walletv1.RecurringMethod{
		wallet.RecurringMethodFixed:  walletv1.RecurringMethod_RECURRING_METHOD_FIXED,
		wallet.RecurringMethodTarget: walletv1.RecurringMethod_RECURRING_METHOD_TARGET,
	})
	walletRuleStatuses = NewEnum(map[wallet.RuleStatus]walletv1.RecurringRuleStatus{
		wallet.RuleStatusActive:     walletv1.RecurringRuleStatus_RECURRING_RULE_STATUS_ACTIVE,
		wallet.RuleStatusTerminated: walletv1.RecurringRuleStatus_RECURRING_RULE_STATUS_TERMINATED,
	})
	walletIntervals = NewEnum(map[period.Interval]commonv1.Interval{
		period.Weekly:     commonv1.Interval_INTERVAL_WEEKLY,
		period.Monthly:    commonv1.Interval_INTERVAL_MONTHLY,
		period.Quarterly:  commonv1.Interval_INTERVAL_QUARTERLY,
		period.Semiannual: commonv1.Interval_INTERVAL_SEMIANNUAL,
		period.Yearly:     commonv1.Interval_INTERVAL_YEARLY,
	})
	walletFeeTypes = NewEnum(map[core.FeeType]commonv1.FeeType{
		core.FeeTypeSubscription: commonv1.FeeType_FEE_TYPE_SUBSCRIPTION,
		core.FeeTypeCharge:       commonv1.FeeType_FEE_TYPE_CHARGE,
		core.FeeTypeFixedCharge:  commonv1.FeeType_FEE_TYPE_FIXED_CHARGE,
		core.FeeTypeAddOn:        commonv1.FeeType_FEE_TYPE_ADD_ON,
		core.FeeTypeCredit:       commonv1.FeeType_FEE_TYPE_CREDIT,
		core.FeeTypeCommitment:   commonv1.FeeType_FEE_TYPE_COMMITMENT,
		core.FeeTypeProduct:      commonv1.FeeType_FEE_TYPE_PRODUCT,
	})
	walletPaymentMethodTypes = NewEnum(map[core.PaymentMethodType]commonv1.PaymentMethodType{
		core.PaymentMethodTypeProvider: commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_PROVIDER,
		core.PaymentMethodTypeManual:   commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_MANUAL,
	})
)

// walletUpdateFields are the update_mask paths of UpdateWallet (lago
// WalletActions#update_params); the rules are replaced with
// ReplaceRecurringTransactionRules.
var walletUpdateFields = []string{
	"name", "code", "priority", "expires_at", "invoice_requires_successful_payment",
	"paid_top_up_min_amount", "paid_top_up_max_amount", "allowed_fee_types",
	"billable_metric_ids", "billing_entity_id", "payment_method_type", "payment_method_id", "connections",
	"metadata", "purchase_order_number", "skip_invoice_custom_sections", "invoice_custom_section_ids",
}

// CreateWallet implements WalletService.CreateWallet (lago
// Wallets::CreateService); wallet.customer_id is an ID or
// external_id:<id>.
func (s *Wallets) CreateWallet(ctx context.Context, req *walletv1.CreateWalletRequest) (*walletv1.CreateWalletResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	w := req.GetWallet()
	cust, err := s.walletCustomer(ctx, org, "wallet.customer_id", w.GetCustomerId())
	if err != nil {
		return nil, err
	}
	in := wallets.CreateInput{
		CustomerID:                       cust,
		Name:                             Opt(w.HasName() && w.GetName() != "", w.GetName()),
		Code:                             Opt(w.HasCode() && w.GetCode() != "", w.GetCode()),
		InvoiceRequiresSuccessfulPayment: w.GetInvoiceRequiresSuccessfulPayment(),
		ExpirationAt:                     TimePtr(w.GetExpiresAt()),
		PaymentMethod:                    walletPaymentMethod(w),
	}
	in.Metadata = createMetadata(w.GetMetadata())
	in.PurchaseOrderNumber = Opt(w.HasPurchaseOrderNumber(), w.GetPurchaseOrderNumber())
	in.TransactionName = Opt(w.HasTransactionName(), w.GetTransactionName())
	if w.HasTransactionPriority() {
		in.TransactionPriority = new(int(w.GetTransactionPriority()))
	}
	if len(w.GetTransactionMetadata()) > 0 {
		in.TransactionMetadata = FromMetadata(w.GetTransactionMetadata())
	}
	in.IgnorePaidTopUpLimitsOnCreation = w.GetIgnorePaidTopUpLimitsOnCreation()
	if in.InvoiceCustomSections, err = walletCustomSections("wallet.", w.HasSkipInvoiceCustomSections(), w.GetSkipInvoiceCustomSections(), w.GetInvoiceCustomSectionIds()); err != nil {
		return nil, err
	}
	if w.HasCurrency() {
		if in.Currency, err = walletCurrency("wallet.currency", w.GetCurrency()); err != nil {
			return nil, err
		}
	}
	if in.RateAmount, err = ParseDecimal("wallet.rate_amount", w.GetRateAmount()); err != nil {
		return nil, err
	}
	if w.HasPriority() {
		in.Priority = new(int(w.GetPriority()))
	}
	if in.PaidCredits, err = walletDecimal("wallet.paid_credits", w.HasPaidCredits(), w.GetPaidCredits()); err != nil {
		return nil, err
	}
	if in.GrantedCredits, err = walletDecimal("wallet.granted_credits", w.HasGrantedCredits(), w.GetGrantedCredits()); err != nil {
		return nil, err
	}
	if w.HasPaidTopUpMinAmount() || w.HasPaidTopUpMaxAmount() {
		if in.Currency.IsZero() {
			return nil, InvalidArgument("invalid wallet", []FieldViolation{{Field: "wallet.currency", Reason: core.CodeRequired}})
		}
		if in.PaidTopUpMinAmount, err = walletMinor("wallet.paid_top_up_min_amount", w.HasPaidTopUpMinAmount(), w.GetPaidTopUpMinAmount(), in.Currency); err != nil {
			return nil, err
		}
		if in.PaidTopUpMaxAmount, err = walletMinor("wallet.paid_top_up_max_amount", w.HasPaidTopUpMaxAmount(), w.GetPaidTopUpMaxAmount(), in.Currency); err != nil {
			return nil, err
		}
	}
	in.AllowedFeeTypes = walletFeeTypeList(w.GetAllowedFeeTypes())
	if in.BillableMetricIDs, err = s.walletMetrics(ctx, org, w.GetBillableMetricIds()); err != nil {
		return nil, err
	}
	if in.BillingEntityID, err = s.walletBillingEntity(ctx, org, w.GetBillingEntityId()); err != nil {
		return nil, err
	}
	for i, r := range w.GetRecurringTransactionRules() {
		rule, err := walletRuleInput(fmt.Sprintf("wallet.recurring_transaction_rules[%d].", i), r)
		if err != nil {
			return nil, err
		}
		in.RecurringTransactionRules = append(in.RecurringTransactionRules, rule)
	}
	in.Connections = connectionChoices(w.GetConnections())
	created, err := s.svc.Create(ctx, org, in)
	if err != nil {
		return nil, walletError(ctx, "wallet.", err)
	}
	out, err := s.walletProto(ctx, org, created)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.CreateWalletResponse_builder{Wallet: out}.Build(), nil
}

// GetWallet implements WalletService.GetWallet.
func (s *Wallets) GetWallet(ctx context.Context, req *walletv1.GetWalletRequest) (*walletv1.GetWalletResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.walletGet(ctx, p.OrganizationID, "wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	out, err := s.walletProto(ctx, p.OrganizationID, w)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.GetWalletResponse_builder{Wallet: out}.Build(), nil
}

// ListWallets implements WalletService.ListWallets (lago WalletsQuery):
// the wallets of customer_id (an ID or external_id:<id>), filtered by
// `currency = "EUR"` and `billing_entity_id = "code:acme"` (several joined
// by OR).
func (s *Wallets) ListWallets(ctx context.Context, req *walletv1.ListWalletsRequest) (*walletv1.ListWalletsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	f, err := s.walletListFilter(ctx, org, req.GetFilter())
	if err != nil {
		return nil, err
	}
	r, err := ParseRef("customer_id", req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	if f.CustomerID, err = s.walletCustomerRef(ctx, org, "customer_id", r); err != nil {
		return nil, Error(ctx, err)
	}
	page, err := s.svc.List(ctx, org, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*walletv1.Wallet, len(page.Items))
	for i, w := range page.Items {
		if out[i], err = s.walletProto(ctx, org, w); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return walletv1.ListWalletsResponse_builder{Wallets: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateWallet implements WalletService.UpdateWallet (lago
// Wallets::UpdateService). The fields updated are the update_mask paths
// or, without one, the fields set.
func (s *Wallets) UpdateWallet(ctx context.Context, req *walletv1.UpdateWalletRequest) (*walletv1.UpdateWalletResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	w := req.GetWallet()
	cur, err := s.walletGet(ctx, org, "wallet.id", w.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), walletUpdateFields, func(path string) bool {
		switch path {
		case "name":
			return w.HasName()
		case "code":
			return w.HasCode()
		case "priority":
			return w.HasPriority()
		case "expires_at":
			return w.HasExpiresAt()
		case "invoice_requires_successful_payment":
			return w.HasInvoiceRequiresSuccessfulPayment()
		case "paid_top_up_min_amount":
			return w.HasPaidTopUpMinAmount()
		case "paid_top_up_max_amount":
			return w.HasPaidTopUpMaxAmount()
		case "allowed_fee_types":
			return len(w.GetAllowedFeeTypes()) > 0
		case "billable_metric_ids":
			return len(w.GetBillableMetricIds()) > 0
		case "billing_entity_id":
			return w.HasBillingEntityId()
		case "payment_method_type":
			return w.HasPaymentMethodType()
		case "payment_method_id":
			return w.HasPaymentMethodId()
		case "connections":
			return len(w.GetConnections()) > 0
		case "metadata":
			return len(w.GetMetadata()) > 0
		case "purchase_order_number":
			return w.HasPurchaseOrderNumber()
		case "skip_invoice_custom_sections":
			return w.HasSkipInvoiceCustomSections()
		case "invoice_custom_section_ids":
			return len(w.GetInvoiceCustomSectionIds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in wallets.UpdateInput
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = wallets.To(Opt(w.GetName() != "", w.GetName()))
		case "code":
			in.Code = new(w.GetCode())
		case "priority":
			in.Priority = new(int(w.GetPriority()))
		case "expires_at":
			in.ExpirationAt = wallets.To(TimePtr(w.GetExpiresAt()))
		case "invoice_requires_successful_payment":
			in.InvoiceRequiresSuccessfulPayment = new(w.GetInvoiceRequiresSuccessfulPayment())
		case "paid_top_up_min_amount":
			v, err := walletMinor("wallet.paid_top_up_min_amount", w.GetPaidTopUpMinAmount() != "", w.GetPaidTopUpMinAmount(), cur.Currency)
			if err != nil {
				return nil, err
			}
			in.PaidTopUpMinAmount = wallets.To(v)
		case "paid_top_up_max_amount":
			v, err := walletMinor("wallet.paid_top_up_max_amount", w.GetPaidTopUpMaxAmount() != "", w.GetPaidTopUpMaxAmount(), cur.Currency)
			if err != nil {
				return nil, err
			}
			in.PaidTopUpMaxAmount = wallets.To(v)
		case "allowed_fee_types":
			in.AllowedFeeTypes = wallets.To(walletFeeTypeList(w.GetAllowedFeeTypes()))
		case "billable_metric_ids":
			ids, err := s.walletMetrics(ctx, org, w.GetBillableMetricIds())
			if err != nil {
				return nil, err
			}
			in.BillableMetricIDs = wallets.To(ids)
		case "billing_entity_id":
			id, err := s.walletBillingEntity(ctx, org, w.GetBillingEntityId())
			if err != nil {
				return nil, err
			}
			in.BillingEntityID = wallets.To(id)
		case "payment_method_type", "payment_method_id":
			if in.PaymentMethod == nil {
				in.PaymentMethod = &wallets.PaymentMethod{}
			}
			if path == "payment_method_type" {
				in.PaymentMethod.Type = walletPaymentMethodTypes.Core(w.GetPaymentMethodType())
			} else if in.PaymentMethod.ID, err = ParseID[core.PaymentMethodID]("wallet.payment_method_id", w.GetPaymentMethodId()); err != nil {
				return nil, err
			}
		case "connections":
			in.Connections = connectionChoices(w.GetConnections())
		case "metadata":
			in.Metadata = wallets.To(FromMetadata(w.GetMetadata()))
		case "purchase_order_number":
			in.PurchaseOrderNumber = wallets.To(Opt(w.GetPurchaseOrderNumber() != "", w.GetPurchaseOrderNumber()))
		case "skip_invoice_custom_sections", "invoice_custom_section_ids":
			if in.InvoiceCustomSections == nil {
				in.InvoiceCustomSections = &wallets.CustomSections{}
			}
			if path == "skip_invoice_custom_sections" {
				in.InvoiceCustomSections.Skip = new(w.GetSkipInvoiceCustomSections())
				continue
			}
			rs, err := CatalogRefs("wallet.invoice_custom_section_ids", w.GetInvoiceCustomSectionIds())
			if err != nil {
				return nil, err
			}
			// A masked empty list clears the sections.
			in.InvoiceCustomSections.Sections = append([]refs.Ref{}, rs...)
		}
	}
	updated, err := s.svc.Update(ctx, org, cur.ID, in)
	if err != nil {
		return nil, walletError(ctx, "wallet.", err)
	}
	out, err := s.walletProto(ctx, org, updated)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.UpdateWalletResponse_builder{Wallet: out}.Build(), nil
}

// TerminateWallet implements WalletService.TerminateWallet (lago
// Wallets::TerminateService); a terminated wallet is returned unchanged.
func (s *Wallets) TerminateWallet(ctx context.Context, req *walletv1.TerminateWalletRequest) (*walletv1.TerminateWalletResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.walletGet(ctx, p.OrganizationID, "wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	if w, err = s.svc.Terminate(ctx, p.OrganizationID, w.ID); err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.walletProto(ctx, p.OrganizationID, w)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.TerminateWalletResponse_builder{Wallet: out}.Build(), nil
}

// CreateWalletTransaction implements WalletService.CreateWalletTransaction
// (lago WalletTransactions::CreateFromParamsService).
func (s *Wallets) CreateWalletTransaction(ctx context.Context, req *walletv1.CreateWalletTransactionRequest) (*walletv1.CreateWalletTransactionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	id, err := ParseID[core.WalletID]("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	in := wallets.TransactionInput{
		WalletID:              id,
		Name:                  Opt(req.HasName(), req.GetName()),
		IgnorePaidTopUpLimits: req.GetIgnorePaidTopUpLimits(),
		PaymentMethod:         walletPaymentMethod(req),
		PurchaseOrderNumber:   Opt(req.HasPurchaseOrderNumber(), req.GetPurchaseOrderNumber()),
	}
	if req.HasPriority() {
		in.Priority = new(int(req.GetPriority()))
	}
	if in.VoidedTransactionID, err = ParseID[core.WalletTransactionID]("voided_transaction_id", req.GetVoidedTransactionId()); err != nil {
		return nil, err
	}
	if in.InvoiceCustomSections, err = walletCustomSections("", req.HasSkipInvoiceCustomSections(), req.GetSkipInvoiceCustomSections(), req.GetInvoiceCustomSectionIds()); err != nil {
		return nil, err
	}
	if len(req.GetMetadata()) > 0 {
		in.Metadata = FromMetadata(req.GetMetadata())
	}
	if req.HasInvoiceRequiresSuccessfulPayment() {
		in.InvoiceRequiresSuccessfulPayment = new(req.GetInvoiceRequiresSuccessfulPayment())
	}
	if in.PaidCredits, err = walletDecimal("paid_credits", req.HasPaidCredits(), req.GetPaidCredits()); err != nil {
		return nil, err
	}
	if in.GrantedCredits, err = walletDecimal("granted_credits", req.HasGrantedCredits(), req.GetGrantedCredits()); err != nil {
		return nil, err
	}
	if in.VoidedCredits, err = walletDecimal("voided_credits", req.HasVoidedCredits(), req.GetVoidedCredits()); err != nil {
		return nil, err
	}
	ts, err := s.svc.CreateTransactions(ctx, org, in)
	if err != nil {
		return nil, walletError(ctx, "", err)
	}
	w, err := s.svc.Get(ctx, org, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*walletv1.WalletTransaction, len(ts))
	for i, t := range ts {
		out[i] = walletTransactionProto(w, t)
	}
	return walletv1.CreateWalletTransactionResponse_builder{WalletTransactions: out}.Build(), nil
}

// GetWalletTransaction implements WalletService.GetWalletTransaction.
func (s *Wallets) GetWalletTransaction(ctx context.Context, req *walletv1.GetWalletTransactionRequest) (*walletv1.GetWalletTransactionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	w, t, err := s.walletTransaction(ctx, p.OrganizationID, req.GetWalletTransactionId())
	if err != nil {
		return nil, err
	}
	return walletv1.GetWalletTransactionResponse_builder{WalletTransaction: walletTransactionProto(w, t)}.Build(), nil
}

// ListWalletTransactions implements WalletService.ListWalletTransactions
// (lago WalletTransactionsQuery), filtered by transaction_type,
// transaction_status, status and `metadata.<key>` equalities.
func (s *Wallets) ListWalletTransactions(ctx context.Context, req *walletv1.ListWalletTransactionsRequest) (*walletv1.ListWalletTransactionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	f, err := walletTransactionFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	w, err := s.walletGet(ctx, org, "wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListTransactions(ctx, org, w.ID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.ListWalletTransactionsResponse_builder{
		WalletTransactions: Items(page, func(t *wallet.Transaction) *walletv1.WalletTransaction { return walletTransactionProto(w, t) }),
		NextPageToken:      new(page.NextToken),
	}.Build(), nil
}

// ListWalletTransactionConsumptions implements
// WalletService.ListWalletTransactionConsumptions (lago
// WalletTransactionConsumptionsQuery): the consumptions of an inbound
// transaction, or the fundings of an outbound one, newest first.
func (s *Wallets) ListWalletTransactionConsumptions(ctx context.Context, req *walletv1.ListWalletTransactionConsumptionsRequest) (*walletv1.ListWalletTransactionConsumptionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	_, t, err := s.walletTransaction(ctx, p.OrganizationID, req.GetWalletTransactionId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListConsumptions(ctx, p.OrganizationID, t.ID, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, walletError(ctx, "", err)
	}
	return walletv1.ListWalletTransactionConsumptionsResponse_builder{
		Consumptions: Items(page, func(c *wallet.Consumption) *walletv1.WalletTransactionConsumption {
			return walletv1.WalletTransactionConsumption_builder{
				InboundTransactionId:  new(c.InboundTransactionID.String()),
				OutboundTransactionId: new(c.OutboundTransactionID.String()),
				ConsumedAmount:        Money(c.ConsumedAmount, t.Currency),
				CreatedAt:             Timestamp(c.CreatedAt),
			}.Build()
		}),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// GenerateWalletTransactionPaymentUrl implements
// WalletService.GenerateWalletTransactionPaymentUrl (lago
// WalletTransactions::Payments::GeneratePaymentUrlService): the
// transaction must be a pending purchase with an invoice.
func (s *Wallets) GenerateWalletTransactionPaymentUrl(ctx context.Context, req *walletv1.GenerateWalletTransactionPaymentUrlRequest) (*walletv1.GenerateWalletTransactionPaymentUrlResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WalletTransactionID]("wallet_transaction_id", req.GetWalletTransactionId())
	if err != nil {
		return nil, err
	}
	inv, err := s.svc.PaymentURLInvoice(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, walletError(ctx, "", err)
	}
	if s.urls == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "payment URLs are not available")
	}
	url, err := s.urls.InvoicePaymentURL(ctx, p.OrganizationID, inv)
	if err != nil {
		return nil, walletError(ctx, "", err)
	}
	return walletv1.GenerateWalletTransactionPaymentUrlResponse_builder{PaymentUrl: new(url)}.Build(), nil
}

// ReplaceRecurringTransactionRules implements
// WalletService.ReplaceRecurringTransactionRules (lago
// Wallets::RecurringTransactionRules::UpdateService through
// Wallets::UpdateService): listed rules with an ID are updated, the others
// created, and the rules not listed terminated.
func (s *Wallets) ReplaceRecurringTransactionRules(ctx context.Context, req *walletv1.ReplaceRecurringTransactionRulesRequest) (*walletv1.ReplaceRecurringTransactionRulesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	w, err := s.walletGet(ctx, org, "wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	rules := []wallets.RuleInput{}
	for i, r := range req.GetRecurringTransactionRules() {
		rule, err := walletRuleInput(fmt.Sprintf("recurring_transaction_rules[%d].", i), r)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if _, err := s.svc.Update(ctx, org, w.ID, wallets.UpdateInput{RecurringTransactionRules: wallets.To(rules)}); err != nil {
		return nil, walletError(ctx, "", err)
	}
	out, err := s.walletRules(ctx, org, w)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.ReplaceRecurringTransactionRulesResponse_builder{RecurringTransactionRules: out}.Build(), nil
}

// walletGet returns the wallet of the ID s, or NotFound wallet_not_found.
func (s *Wallets) walletGet(ctx context.Context, org core.OrganizationID, field, id string) (*wallet.Wallet, error) {
	wid, err := ParseID[core.WalletID](field, id)
	if err != nil {
		return nil, err
	}
	w, err := s.svc.Get(ctx, org, wid)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return w, nil
}

// walletTransaction returns the transaction of the ID s with its wallet.
func (s *Wallets) walletTransaction(ctx context.Context, org core.OrganizationID, id string) (*wallet.Wallet, *wallet.Transaction, error) {
	tid, err := ParseID[core.WalletTransactionID]("wallet_transaction_id", id)
	if err != nil {
		return nil, nil, err
	}
	t, err := s.svc.GetTransaction(ctx, org, tid)
	if err != nil {
		return nil, nil, Error(ctx, err)
	}
	w, err := s.svc.Get(ctx, org, t.WalletID)
	if err != nil {
		return nil, nil, Error(ctx, err)
	}
	return w, t, nil
}

// walletCustomer resolves the customer reference of a wallet to create;
// a missing customer is the zero ID, which the wallet service reports as
// customer_id customer_not_found (lago Wallets::ValidateService).
func (s *Wallets) walletCustomer(ctx context.Context, org core.OrganizationID, field, ref string) (core.CustomerID, error) {
	r, err := ParseRef(field, ref)
	if err != nil {
		return core.CustomerID{}, err
	}
	id, err := s.walletCustomerRef(ctx, org, field, r)
	if errors.Is(err, core.ErrNotFound) {
		return core.CustomerID{}, nil
	}
	return id, err
}

func (s *Wallets) walletCustomerRef(ctx context.Context, org core.OrganizationID, field string, r Ref) (core.CustomerID, error) {
	if r.Code != "" {
		return core.CustomerID{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "this resource has no code",
		}})
	}
	var id core.CustomerID
	if r.ExternalID == "" {
		var err error
		if id, err = ParseID[core.CustomerID](field, r.ID.String()); err != nil {
			return core.CustomerID{}, err
		}
	}
	return s.svc.CustomerID(ctx, org, id, r.ExternalID)
}

// walletBillingEntity resolves a billing entity reference; empty is none.
func (s *Wallets) walletBillingEntity(ctx context.Context, org core.OrganizationID, ref string) (core.BillingEntityID, error) {
	r, err := CatalogRef("wallet.billing_entity_id", ref)
	if err != nil || r.IsZero() {
		return core.BillingEntityID{}, err
	}
	ids, err := s.svc.BillingEntityIDs(ctx, org, []refs.Ref{r})
	if err != nil {
		return core.BillingEntityID{}, Error(ctx, err)
	}
	return ids[0], nil
}

// walletMetrics resolves the billable metric references of the
// limitations; none is nil.
func (s *Wallets) walletMetrics(ctx context.Context, org core.OrganizationID, in []string) ([]core.BillableMetricID, error) {
	if len(in) == 0 {
		return nil, nil
	}
	rs, err := CatalogRefs("wallet.billable_metric_ids", in)
	if err != nil {
		return nil, err
	}
	ids, err := s.svc.MetricIDs(ctx, org, rs)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return ids, nil
}

// walletListFilter parses the filter of ListWallets (lago
// WalletActions#wallet_index: currency and billing entities).
func (s *Wallets) walletListFilter(ctx context.Context, org core.OrganizationID, filter string) (wallets.ListFilter, error) {
	var f wallets.ListFilter
	e, err := ParseFilter(filter)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, "currency", "billing_entity_id")
	if err != nil {
		return f, err
	}
	cur, err := FilterSingle("currency", eq["currency"])
	if err != nil {
		return f, err
	}
	if cur != "" {
		c, err := money.LookupCurrency(strings.ToUpper(cur))
		if err != nil {
			return f, filterError(fmt.Sprintf("field %q: unknown currency %q", "currency", cur))
		}
		f.Currency = c
	}
	if vs := eq["billing_entity_id"]; len(vs) > 0 {
		rs, err := CatalogRefs(FilterField, vs)
		if err != nil {
			return f, err
		}
		if f.BillingEntityIDs, err = s.svc.BillingEntityIDs(ctx, org, rs); err != nil {
			return f, Error(ctx, err)
		}
	}
	return f, nil
}

// walletTransactionFilter parses the filter of ListWalletTransactions:
// equalities on transaction_type, transaction_status and status, and on
// metadata.<key>.
func walletTransactionFilter(filter string) (wallets.TransactionListFilter, error) {
	var f wallets.TransactionListFilter
	e, err := ParseFilter(filter)
	if err != nil || e == nil {
		return f, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	var rest FilterAnd
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok || !strings.HasPrefix(r.Field, "metadata.") {
			rest = append(rest, t)
			continue
		}
		key := strings.TrimPrefix(r.Field, "metadata.")
		if r.Op != CmpEq || key == "" {
			return f, filterError("metadata takes `metadata.<key> = value` restrictions")
		}
		if f.Metadata == nil {
			f.Metadata = map[string]string{}
		}
		if _, dup := f.Metadata[key]; dup {
			return f, filterError(fmt.Sprintf("field %q is restricted twice", r.Field))
		}
		f.Metadata[key] = r.Value
	}
	var others FilterExpr
	if len(rest) > 0 {
		others = rest
	}
	eq, err := Equalities(others, "transaction_type", "transaction_status", "status")
	if err != nil {
		return f, err
	}
	typ, err := walletFilterEnum[walletv1.TransactionType]("transaction_type", eq["transaction_type"])
	if err != nil {
		return f, err
	}
	st, err := walletFilterEnum[walletv1.TransactionStatus]("transaction_status", eq["transaction_status"])
	if err != nil {
		return f, err
	}
	settlement, err := walletFilterEnum[walletv1.SettlementStatus]("status", eq["status"])
	if err != nil {
		return f, err
	}
	f.TransactionType = walletTransactionTypes.Core(typ)
	f.TransactionStatus = walletTransactionStatuses.Core(st)
	f.Status = walletSettlementStatuses.Core(settlement)
	return f, nil
}

// walletFilterEnum parses the single enumeration value of a filter field;
// unrestricted is 0.
func walletFilterEnum[P interface {
	~int32
	Descriptor() protoreflect.EnumDescriptor
}](field string, values []string) (P, error) {
	v, err := FilterSingle(field, values)
	if err != nil || v == "" {
		return 0, err
	}
	ps, err := FilterEnum[P](field, []string{v}, P(0).Descriptor())
	if err != nil {
		return 0, err
	}
	return ps[0], nil
}

func walletCurrency(field, s string) (money.Currency, error) {
	c, err := money.LookupCurrency(s)
	if err != nil {
		return money.Currency{}, InvalidArgument("invalid currency", []FieldViolation{{Field: field, Reason: core.CodeInvalidCurrency}})
	}
	return c, nil
}

// walletDecimal parses an optional decimal; unset is nil.
func walletDecimal(field string, has bool, s string) (*money.Decimal, error) {
	if !has {
		return nil, nil
	}
	d, err := ParseDecimal(field, s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// walletMinor parses an optional amount in major units of c into minor
// units; unset is nil.
func walletMinor(field string, has bool, s string, c money.Currency) (*int64, error) {
	if !has {
		return nil, nil
	}
	minor, _, err := FromMoney(field, commonv1.Money_builder{Currency: new(c.Code()), Amount: &s}.Build())
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			// FromMoney names the violation <field>.amount.
			return nil, InvalidArgument("invalid amount", []FieldViolation{{
				Field: field, Reason: core.CodeInvalid, Description: fmt.Sprintf("must be a decimal with at most %d decimals", c.Exponent()),
			}})
		}
		return nil, err
	}
	return &minor, nil
}

// walletMajor formats an optional amount in minor units of c in major
// units.
func walletMajor(minor *int64, c money.Currency) *string {
	if minor == nil {
		return nil
	}
	return new(c.FromMinor(*minor).StringFixed(c.Exponent()))
}

func walletFeeTypeList(in []commonv1.FeeType) []core.FeeType {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.FeeType, len(in))
	for i, t := range in {
		out[i] = walletFeeTypes.Core(t)
	}
	return out
}

// walletPaymentMethodSource is a message carrying a payment method.
type walletPaymentMethodSource interface {
	HasPaymentMethodType() bool
	GetPaymentMethodType() commonv1.PaymentMethodType
	HasPaymentMethodId() bool
	GetPaymentMethodId() string
}

// walletPaymentMethod returns the payment method of m; none is nil. The
// IDs are UUIDs (protovalidate).
func walletPaymentMethod(m walletPaymentMethodSource) *wallets.PaymentMethod {
	if !m.HasPaymentMethodType() && !m.HasPaymentMethodId() {
		return nil
	}
	pm := &wallets.PaymentMethod{Type: walletPaymentMethodTypes.Core(m.GetPaymentMethodType())}
	if id, err := core.ParseID[core.PaymentMethodID](m.GetPaymentMethodId()); err == nil {
		pm.ID = id
	}
	return pm
}

// walletRuleInput converts a recurring rule of a request; prefix is its
// path.
func walletRuleInput(prefix string, r *walletv1.RecurringTransactionRule) (wallets.RuleInput, error) {
	in := wallets.RuleInput{
		Trigger:       walletRuleTriggers.Core(r.GetTrigger()),
		Interval:      walletIntervals.Core(r.GetInterval()),
		Method:        walletRuleMethods.Core(r.GetMethod()),
		StartedAt:     TimePtr(r.GetStartedAt()),
		ExpirationAt:  TimePtr(r.GetExpiresAt()),
		PaymentMethod: walletPaymentMethod(r),
	}
	var err error
	if in.ID, err = ParseID[core.RecurringTransactionRuleID](prefix+"id", r.GetId()); err != nil {
		return in, err
	}
	for _, d := range []struct {
		field string
		has   bool
		value string
		to    **money.Decimal
	}{
		{"threshold_credits", r.HasThresholdCredits(), r.GetThresholdCredits(), &in.ThresholdCredits},
		{"paid_credits", r.HasPaidCredits(), r.GetPaidCredits(), &in.PaidCredits},
		{"granted_credits", r.HasGrantedCredits(), r.GetGrantedCredits(), &in.GrantedCredits},
		{"target_ongoing_balance", r.HasTargetOngoingBalance(), r.GetTargetOngoingBalance(), &in.TargetOngoingBalance},
	} {
		if *d.to, err = walletDecimal(prefix+d.field, d.has, d.value); err != nil {
			return in, err
		}
	}
	in.GrantsTargetTopUp = Opt(r.HasGrantsTargetTopUp(), r.GetGrantsTargetTopUp())
	in.InvoiceRequiresSuccessfulPayment = Opt(r.HasInvoiceRequiresSuccessfulPayment(), r.GetInvoiceRequiresSuccessfulPayment())
	in.IgnorePaidTopUpLimits = Opt(r.HasIgnorePaidTopUpLimits(), r.GetIgnorePaidTopUpLimits())
	in.TransactionName = Opt(r.HasTransactionName(), r.GetTransactionName())
	if len(r.GetTransactionMetadata()) > 0 {
		in.TransactionMetadata = FromMetadata(r.GetTransactionMetadata())
	}
	in.PurchaseOrderNumber = Opt(r.HasPurchaseOrderNumber(), r.GetPurchaseOrderNumber())
	if in.InvoiceCustomSections, err = walletCustomSections(prefix, r.HasSkipInvoiceCustomSections(), r.GetSkipInvoiceCustomSections(), r.GetInvoiceCustomSectionIds()); err != nil {
		return in, err
	}
	in.Connections = connectionChoices(r.GetConnections())
	return in, nil
}

// walletCustomSections converts the invoice custom section selection of a
// wallet, a rule or a transaction at prefix (lago invoice_custom_section);
// nil when the request selects nothing.
func walletCustomSections(prefix string, hasSkip, skip bool, ids []string) (*wallets.CustomSections, error) {
	if !hasSkip && len(ids) == 0 {
		return nil, nil
	}
	rs, err := CatalogRefs(prefix+"invoice_custom_section_ids", ids)
	if err != nil {
		return nil, err
	}
	return &wallets.CustomSections{Skip: Opt(hasSkip, skip), Sections: rs}, nil
}

// sectionIDs returns the IDs of a custom section selection.
func sectionIDs(cs wallet.CustomSections) []string {
	out := make([]string, len(cs.IDs))
	for i, id := range cs.IDs {
		out[i] = id.String()
	}
	return out
}

// walletError maps the service errors, whose field paths are the
// service's, onto the request at prefix.
func walletError(ctx context.Context, prefix string, err error) error {
	if v, ok := validationViolations(prefix, err); ok {
		return withReason(InvalidArgument("invalid wallet", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// walletProto converts w with its recurring rules (lago
// V1::WalletSerializer with recurring_transaction_rules and limitations).
func (s *Wallets) walletProto(ctx context.Context, org core.OrganizationID, w *wallet.Wallet) (*walletv1.Wallet, error) {
	rules, err := s.walletRules(ctx, org, w)
	if err != nil {
		return nil, err
	}
	routes, err := s.svc.Connections(ctx, org, w.CustomerID, integration.ObjectOwner{WalletID: w.ID})
	if err != nil {
		return nil, err
	}
	c := w.Currency
	b := walletv1.Wallet_builder{
		Id:                               new(w.ID.String()),
		CustomerId:                       new(w.CustomerID.String()),
		Code:                             w.Code,
		Name:                             w.Name,
		Currency:                         new(c.Code()),
		RateAmount:                       new(Decimal(w.RateAmount)),
		Priority:                         new(int32(w.Priority)),
		ExpiresAt:                        TimestampPtr(w.ExpirationAt),
		BalanceAmount:                    Money(w.BalanceAmount, c),
		CreditsBalance:                   new(Decimal(w.CreditsBalance)),
		OngoingBalanceAmount:             Money(w.OngoingBalanceAmount, c),
		CreditsOngoingBalance:            new(Decimal(w.CreditsOngoingBalance)),
		OngoingUsageBalanceAmount:        Money(w.OngoingUsageBalanceAmount, c),
		CreditsOngoingUsageBalance:       new(Decimal(w.CreditsOngoingUsageBalance)),
		ConsumedAmount:                   Money(w.ConsumedAmount, c),
		ConsumedCredits:                  new(Decimal(w.ConsumedCredits)),
		PaidTopUpMinAmount:               walletMajor(w.PaidTopUpMinAmount, c),
		PaidTopUpMaxAmount:               walletMajor(w.PaidTopUpMaxAmount, c),
		InvoiceRequiresSuccessfulPayment: new(w.InvoiceRequiresSuccessfulPayment),
		Traceable:                        new(w.Traceable),
		RecurringTransactionRules:        rules,
		Metadata:                         Metadata(w.Metadata),
		PurchaseOrderNumber:              w.PurchaseOrderNumber,
		SkipInvoiceCustomSections:        new(w.InvoiceCustomSections.Skip),
		InvoiceCustomSectionIds:          sectionIDs(w.InvoiceCustomSections),
		Connections:                      connectionRoutesProto(routes),
		DepletedOngoingBalance:           new(w.DepletedOngoingBalance),
		LastBalanceSyncAt:                TimestampPtr(w.LastBalanceSyncAt),
		LastConsumedCreditAt:             TimestampPtr(w.LastConsumedCreditAt),
		TerminatedAt:                     TimestampPtr(w.TerminatedAt),
		CreatedAt:                        Timestamp(w.CreatedAt),
		UpdatedAt:                        Timestamp(w.UpdatedAt),
	}
	if st := walletStatuses.Proto(w.Status); st != 0 {
		b.Status = &st
	}
	for _, t := range w.AllowedFeeTypes {
		b.AllowedFeeTypes = append(b.AllowedFeeTypes, walletFeeTypes.Proto(t))
	}
	for _, id := range w.BillableMetricIDs {
		b.BillableMetricIds = append(b.BillableMetricIds, id.String())
	}
	if !w.BillingEntityID.IsZero() {
		b.BillingEntityId = new(w.BillingEntityID.String())
	}
	if t := walletPaymentMethodTypes.Proto(w.PaymentMethodType); t != 0 {
		b.PaymentMethodType = &t
	}
	if !w.PaymentMethodID.IsZero() {
		b.PaymentMethodId = new(w.PaymentMethodID.String())
	}
	return b.Build(), nil
}

func (s *Wallets) walletRules(ctx context.Context, org core.OrganizationID, w *wallet.Wallet) ([]*walletv1.RecurringTransactionRule, error) {
	rules, err := s.svc.ListRules(ctx, org, w.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*walletv1.RecurringTransactionRule, len(rules))
	for i, r := range rules {
		routes, err := s.svc.Connections(ctx, org, w.CustomerID, integration.ObjectOwner{RecurringTransactionRuleID: r.ID})
		if err != nil {
			return nil, err
		}
		out[i] = walletRuleProto(r)
		out[i].SetConnections(connectionRoutesProto(routes))
	}
	return out, nil
}

func walletRuleProto(r *wallet.RecurringTransactionRule) *walletv1.RecurringTransactionRule {
	b := walletv1.RecurringTransactionRule_builder{
		Id:                               new(r.ID.String()),
		ThresholdCredits:                 DecimalPtr(r.ThresholdCredits),
		PaidCredits:                      new(Decimal(r.PaidCredits)),
		GrantedCredits:                   new(Decimal(r.GrantedCredits)),
		TargetOngoingBalance:             DecimalPtr(r.TargetOngoingBalance),
		GrantsTargetTopUp:                new(r.GrantsTargetTopUp),
		StartedAt:                        TimestampPtr(r.StartedAt),
		ExpiresAt:                        TimestampPtr(r.ExpirationAt),
		InvoiceRequiresSuccessfulPayment: new(r.InvoiceRequiresSuccessfulPayment),
		IgnorePaidTopUpLimits:            new(r.IgnorePaidTopUpLimits),
		TransactionName:                  r.TransactionName,
		TransactionMetadata:              Metadata(r.TransactionMetadata),
		PurchaseOrderNumber:              r.PurchaseOrderNumber,
		SkipInvoiceCustomSections:        new(r.InvoiceCustomSections.Skip),
		InvoiceCustomSectionIds:          sectionIDs(r.InvoiceCustomSections),
		TerminatedAt:                     TimestampPtr(r.TerminatedAt),
		CreatedAt:                        Timestamp(r.CreatedAt),
	}
	if v := walletRuleTriggers.Proto(r.Trigger); v != 0 {
		b.Trigger = &v
	}
	if v := walletIntervals.Proto(r.Interval); v != 0 {
		b.Interval = &v
	}
	if v := walletRuleMethods.Proto(r.Method); v != 0 {
		b.Method = &v
	}
	if v := walletRuleStatuses.Proto(r.Status); v != 0 {
		b.Status = &v
	}
	if v := walletPaymentMethodTypes.Proto(r.PaymentMethodType); v != 0 {
		b.PaymentMethodType = &v
	}
	if !r.PaymentMethodID.IsZero() {
		b.PaymentMethodId = new(r.PaymentMethodID.String())
	}
	return b.Build()
}

// walletTransactionProto converts t of w (lago
// V1::WalletTransactionSerializer).
func walletTransactionProto(w *wallet.Wallet, t *wallet.Transaction) *walletv1.WalletTransaction {
	b := walletv1.WalletTransaction_builder{
		Id:                               new(t.ID.String()),
		WalletId:                         new(t.WalletID.String()),
		Amount:                           Money(t.Amount, t.Currency),
		CreditAmount:                     new(Decimal(t.CreditAmount)),
		RemainingCreditAmount:            DecimalPtr(t.RemainingCredits(w.RateAmount)),
		Priority:                         new(int32(t.Priority)),
		Name:                             t.Name,
		Metadata:                         Metadata(t.Metadata),
		InvoiceRequiresSuccessfulPayment: new(t.InvoiceRequiresSuccessfulPayment),
		SettledAt:                        TimestampPtr(t.SettledAt),
		FailedAt:                         TimestampPtr(t.FailedAt),
		CreatedAt:                        Timestamp(t.CreatedAt),
		PurchaseOrderNumber:              wallet.ResolvePurchaseOrderNumber(t.PurchaseOrderNumber, w.PurchaseOrderNumber),
		SkipInvoiceCustomSections:        new(t.InvoiceCustomSections.Skip),
		InvoiceCustomSectionIds:          sectionIDs(t.InvoiceCustomSections),
	}
	if t.RemainingAmount != nil {
		b.RemainingAmount = Money(*t.RemainingAmount, t.Currency)
	}
	if !t.VoidedInvoiceID.IsZero() {
		b.VoidedInvoiceId = new(t.VoidedInvoiceID.String())
	}
	// lago V1::WalletTransactionSerializer#billing_entity_code.
	if be := cmp.Or(t.BillingEntityID, w.BillingEntityID); !be.IsZero() {
		b.BillingEntityId = new(be.String())
	}
	if v := walletPaymentMethodTypes.Proto(t.PaymentMethodType); v != 0 {
		b.PaymentMethodType = &v
	}
	if !t.PaymentMethodID.IsZero() {
		b.PaymentMethodId = new(t.PaymentMethodID.String())
	}
	if v := walletTransactionTypes.Proto(t.TransactionType); v != 0 {
		b.TransactionType = &v
	}
	if v := walletTransactionStatuses.Proto(t.TransactionStatus); v != 0 {
		b.TransactionStatus = &v
	}
	if v := walletSettlementStatuses.Proto(t.Status); v != 0 {
		b.Status = &v
	}
	if v := walletTransactionSources.Proto(t.Source); v != 0 {
		b.Source = &v
	}
	if !t.InvoiceID.IsZero() {
		b.InvoiceId = new(t.InvoiceID.String())
	}
	if !t.CreditNoteID.IsZero() {
		b.CreditNoteId = new(t.CreditNoteID.String())
	}
	if !t.RecurringTransactionRuleID.IsZero() {
		b.RecurringTransactionRuleId = new(t.RecurringTransactionRuleID.String())
	}
	return b.Build()
}
