package rpc

import (
	"encoding/json"
	"time"

	"google.golang.org/genproto/googleapis/type/date"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
)

var (
	invTypes = NewEnum(map[invoice.Type]invoicev1.InvoiceType{
		invoice.TypeSubscription:       invoicev1.InvoiceType_INVOICE_TYPE_SUBSCRIPTION,
		invoice.TypeCredit:             invoicev1.InvoiceType_INVOICE_TYPE_CREDIT,
		invoice.TypeOneOff:             invoicev1.InvoiceType_INVOICE_TYPE_ONE_OFF,
		invoice.TypeAdvanceCharges:     invoicev1.InvoiceType_INVOICE_TYPE_ADVANCE_CHARGES,
		invoice.TypeProgressiveBilling: invoicev1.InvoiceType_INVOICE_TYPE_PROGRESSIVE_BILLING,
	})
	invStatuses = NewEnum(map[invoice.Status]invoicev1.InvoiceStatus{
		invoice.StatusGenerating: invoicev1.InvoiceStatus_INVOICE_STATUS_GENERATING,
		invoice.StatusDraft:      invoicev1.InvoiceStatus_INVOICE_STATUS_DRAFT,
		invoice.StatusOpen:       invoicev1.InvoiceStatus_INVOICE_STATUS_OPEN,
		invoice.StatusFinalized:  invoicev1.InvoiceStatus_INVOICE_STATUS_FINALIZED,
		invoice.StatusClosed:     invoicev1.InvoiceStatus_INVOICE_STATUS_CLOSED,
		invoice.StatusVoided:     invoicev1.InvoiceStatus_INVOICE_STATUS_VOIDED,
		invoice.StatusFailed:     invoicev1.InvoiceStatus_INVOICE_STATUS_FAILED,
		invoice.StatusDeleted:    invoicev1.InvoiceStatus_INVOICE_STATUS_DELETED,
	})
	invTaxStatuses = NewEnum(map[invoice.TaxStatus]invoicev1.TaxStatus{
		invoice.TaxStatusPending:   invoicev1.TaxStatus_TAX_STATUS_PENDING,
		invoice.TaxStatusSucceeded: invoicev1.TaxStatus_TAX_STATUS_SUCCEEDED,
		invoice.TaxStatusFailed:    invoicev1.TaxStatus_TAX_STATUS_FAILED,
	})
	invReasons = NewEnum(map[invoice.InvoicingReason]invoicev1.InvoicingReason{
		invoice.InvoicingReasonSubscriptionStarting:    invoicev1.InvoicingReason_INVOICING_REASON_SUBSCRIPTION_STARTING,
		invoice.InvoicingReasonSubscriptionPeriodic:    invoicev1.InvoicingReason_INVOICING_REASON_SUBSCRIPTION_PERIODIC,
		invoice.InvoicingReasonSubscriptionTerminating: invoicev1.InvoicingReason_INVOICING_REASON_SUBSCRIPTION_TERMINATING,
		invoice.InvoicingReasonInAdvanceCharge:         invoicev1.InvoicingReason_INVOICING_REASON_IN_ADVANCE_CHARGE,
		invoice.InvoicingReasonInAdvanceChargePeriodic: invoicev1.InvoicingReason_INVOICING_REASON_IN_ADVANCE_CHARGE_PERIODIC,
		invoice.InvoicingReasonProgressiveBilling:      invoicev1.InvoicingReason_INVOICING_REASON_PROGRESSIVE_BILLING,
	})
	feePaymentStatuses = NewEnum(map[invoice.FeePaymentStatus]invoicev1.FeePaymentStatus{
		invoice.FeePaymentStatusPending:   invoicev1.FeePaymentStatus_FEE_PAYMENT_STATUS_PENDING,
		invoice.FeePaymentStatusSucceeded: invoicev1.FeePaymentStatus_FEE_PAYMENT_STATUS_SUCCEEDED,
		invoice.FeePaymentStatusFailed:    invoicev1.FeePaymentStatus_FEE_PAYMENT_STATUS_FAILED,
		invoice.FeePaymentStatusRefunded:  invoicev1.FeePaymentStatus_FEE_PAYMENT_STATUS_REFUNDED,
	})
	invSettlementTypes = NewEnum(map[invoice.SettlementType]invoicev1.SettlementType{
		invoice.SettlementTypePayment:    invoicev1.SettlementType_SETTLEMENT_TYPE_PAYMENT,
		invoice.SettlementTypeCreditNote: invoicev1.SettlementType_SETTLEMENT_TYPE_CREDIT_NOTE,
	})
	invErrorCodes = NewEnum(map[invoice.ErrorCode]invoicev1.InvoiceErrorCode{
		invoice.ErrorCodeNotProvided:            invoicev1.InvoiceErrorCode_INVOICE_ERROR_CODE_NOT_PROVIDED,
		invoice.ErrorCodeTaxError:               invoicev1.InvoiceErrorCode_INVOICE_ERROR_CODE_TAX_ERROR,
		invoice.ErrorCodeTaxVoidingError:        invoicev1.InvoiceErrorCode_INVOICE_ERROR_CODE_TAX_VOIDING_ERROR,
		invoice.ErrorCodeInvoiceGenerationError: invoicev1.InvoiceErrorCode_INVOICE_ERROR_CODE_INVOICE_GENERATION_ERROR,
	})
	feeAdjustmentModes = NewEnum(map[invoice.AdjustmentMode]invoicev1.FeeAdjustmentMode{
		invoice.AdjustmentModeDisplayName:       invoicev1.FeeAdjustmentMode_FEE_ADJUSTMENT_MODE_DISPLAY_NAME,
		invoice.AdjustmentModeUnits:             invoicev1.FeeAdjustmentMode_FEE_ADJUSTMENT_MODE_UNITS,
		invoice.AdjustmentModeUnitsAndUnitPrice: invoicev1.FeeAdjustmentMode_FEE_ADJUSTMENT_MODE_UNITS_AND_UNIT_PRICE,
	})
	// invFeeTypes maps fee types; walletFeeTypes is the same mapping.
	invFeeTypes = walletFeeTypes
	// invPaymentStatuses and invPaymentMethodTypes are the payment
	// mappings of payments.go.
	invPaymentStatuses    = payStatuses
	invPaymentMethodTypes = payMethodTypes
)

// dateTimeRange returns the range [from, to), nil when both are zero.
func dateTimeRange(from, to time.Time) *commonv1.DateTimeRange {
	if from.IsZero() && to.IsZero() {
		return nil
	}
	b := commonv1.DateTimeRange_builder{}
	if !from.IsZero() {
		b.From = Timestamp(from)
	}
	if !to.IsZero() {
		b.To = Timestamp(to)
	}
	return b.Build()
}

// optDate returns d, nil when zero.
func optDate(d core.Date) *date.Date {
	if d.IsZero() {
		return nil
	}
	return Date(d)
}

// feeTierDetails is the graduated breakdown kept in a fee's amount
// details (libs/pricing GraduatedDetails and GraduatedPercentageDetails).
type feeTierDetails struct {
	Graduated           []feeTierRange `json:"graduated_ranges"`
	GraduatedPercentage []feeTierRange `json:"graduated_percentage_ranges"`
}

type feeTierRange struct {
	From           *money.Decimal `json:"from_value"`
	To             *money.Decimal `json:"to_value"`
	FlatUnitAmount *money.Decimal `json:"flat_unit_amount"`
	PerUnitAmount  *money.Decimal `json:"per_unit_amount"`
	Rate           *money.Decimal `json:"rate"`
	Units          *money.Decimal `json:"units"`
	Total          *money.Decimal `json:"total_with_flat_amount"`
}

// feeTiers returns the tiers of a graduated fee's amount details.
func feeTiers(details json.RawMessage) []*invoicev1.FeeTier {
	if len(details) == 0 {
		return nil
	}
	var d feeTierDetails
	if json.Unmarshal(details, &d) != nil {
		return nil
	}
	ranges := d.Graduated
	if len(ranges) == 0 {
		ranges = d.GraduatedPercentage
	}
	str := func(p *money.Decimal) string {
		if p == nil {
			return ""
		}
		return p.String()
	}
	out := make([]*invoicev1.FeeTier, 0, len(ranges))
	for _, r := range ranges {
		per := r.PerUnitAmount
		if per == nil {
			per = r.Rate
		}
		out = append(out, invoicev1.FeeTier_builder{
			FromValue:      new(str(r.From)),
			ToValue:        new(str(r.To)),
			Units:          new(str(r.Units)),
			PerUnitAmount:  new(str(per)),
			FlatUnitAmount: new(str(r.FlatUnitAmount)),
			Amount:         new(str(r.Total)),
		}.Build())
	}
	return out
}

// feeProto converts a fee view.
func feeProto(v invoicing.FeeView) *invoicev1.Fee {
	f := v.Fee
	c := f.Currency
	b := invoicev1.Fee_builder{
		Id:                       new(f.ID.String()),
		InvoiceId:                idString(f.InvoiceID),
		SubscriptionId:           idString(f.SubscriptionID),
		CustomerId:               idString(v.CustomerID),
		FeeType:                  new(invFeeTypes.Proto(f.Type)),
		ChargeId:                 idString(f.ChargeID),
		ChargeFilterId:           idString(f.ChargeFilterID),
		FixedChargeId:            idString(f.FixedChargeID),
		AddOnId:                  idString(f.AddOnID),
		BillableMetricCode:       new(v.BillableMetricCode),
		CommitmentId:             idString(f.CommitmentID),
		WalletTransactionId:      idString(f.WalletTransactionID),
		GroupedBy:                f.GroupedBy,
		Amount:                   Money(f.Amount, c),
		PreciseAmount:            new(Decimal(f.PreciseAmount)),
		UnitAmount:               new(Decimal(f.PreciseUnitAmount)),
		Units:                    new(Decimal(f.Units)),
		TotalAggregatedUnits:     new(Decimal(f.TotalAggregatedUnits)),
		EventsCount:              new(f.EventsCount),
		TaxesAmount:              Money(f.TaxesAmount, c),
		TaxesRate:                new(Decimal(f.TaxesRate)),
		TotalAmount:              Money(f.Amount+f.TaxesAmount, c),
		PreciseCouponsAmount:     new(Decimal(f.PreciseCouponsAmount)),
		PreciseCreditNotesAmount: new(Decimal(f.PreciseCreditNotesAmount)),
		PayInAdvance:             new(f.PayInAdvance),
		Invoiceable:              new(f.Invoiceable),
		PaymentStatus:            new(feePaymentStatuses.Proto(f.PaymentStatus)),
		Period:                   dateTimeRange(f.Properties.From, f.Properties.To),
		ChargesPeriod:            dateTimeRange(f.Properties.ChargesFrom, f.Properties.ChargesTo),
		TrueUpParentFeeId:        idString(f.TrueUpParentFeeID),
		TrueUpFeeId:              idString(v.TrueUpFeeID),
		EventTransactionId:       new(f.PayInAdvanceEventTransactionID),
		Adjusted:                 new(v.Adjusted),
		Tiers:                    feeTiers(f.AmountDetails),
		SucceededAt:              TimestampPtr(f.SucceededAt),
		FailedAt:                 TimestampPtr(f.FailedAt),
		RefundedAt:               TimestampPtr(f.RefundedAt),
		CreatedAt:                Timestamp(f.CreatedAt),
		OriginalFeeId:            idString(f.OriginalFeeID),
		PresentationBreakdowns:   PresentationBreakdowns(v.PresentationBreakdowns),
	}
	if f.InvoiceDisplayName != nil {
		b.InvoiceDisplayName = new(*f.InvoiceDisplayName)
	}
	if f.Description != nil {
		b.Description = new(*f.Description)
	}
	if p := f.PricingUnitUsage; p != nil {
		b.PricingUnitUsage = invoicev1.PricingUnitUsage_builder{
			PricingUnitId:  idString(p.PricingUnitID),
			ShortName:      new(p.ShortName),
			ConversionRate: new(Decimal(p.ConversionRate)),
			Amount:         new(Decimal(p.PreciseAmount)),
			UnitAmount:     new(Decimal(p.PreciseUnitAmount)),
		}.Build()
	}
	for _, t := range f.AppliedTaxes {
		at := invoicev1.AppliedTax_builder{
			TaxId:         idString(t.TaxID),
			TaxCode:       new(t.TaxCode),
			TaxName:       new(t.TaxName),
			TaxRate:       new(Decimal(t.TaxRate)),
			TaxableAmount: Money(f.Amount, c),
			Amount:        Money(t.Amount, c),
			PreciseAmount: new(Decimal(t.PreciseAmount)),
		}
		if t.TaxDescription != nil {
			at.TaxDescription = new(*t.TaxDescription)
		}
		b.AppliedTaxes = append(b.AppliedTaxes, at.Build())
	}
	return b.Build()
}

// invoiceProto converts an invoice view.
func invoiceProto(v *invoicing.InvoiceView) *invoicev1.Invoice {
	inv := v.Invoice
	c := inv.Currency
	b := invoicev1.Invoice_builder{
		Id:                             new(inv.ID.String()),
		Number:                         new(inv.Number),
		CustomerId:                     idString(inv.CustomerID),
		BillingEntityId:                idString(inv.BillingEntityID),
		InvoiceType:                    new(invTypes.Proto(inv.Type)),
		Status:                         new(invStatuses.Proto(inv.Status)),
		PaymentStatus:                  new(invPaymentStatuses.Proto(inv.PaymentStatus)),
		TaxStatus:                      new(invTaxStatuses.Proto(inv.TaxStatus)),
		Currency:                       new(c.Code()),
		FeesAmount:                     Money(inv.FeesAmount, c),
		ProgressiveBillingCreditAmount: Money(inv.ProgressiveBillingCreditAmount, c),
		CouponsAmount:                  Money(inv.CouponsAmount, c),
		SubTotalExcludingTaxesAmount:   Money(inv.SubTotalExcludingTaxesAmount, c),
		TaxesAmount:                    Money(inv.TaxesAmount, c),
		TaxesRate:                      new(Decimal(inv.TaxesRate)),
		SubTotalIncludingTaxesAmount:   Money(inv.SubTotalIncludingTaxesAmount, c),
		CreditNotesAmount:              Money(inv.CreditNotesAmount, c),
		PrepaidCreditAmount:            Money(inv.PrepaidCreditAmount, c),
		PrepaidGrantedCreditAmount:     Money(inv.PrepaidGrantedCreditAmount, c),
		PrepaidPurchasedCreditAmount:   Money(inv.PrepaidPurchasedCreditAmount, c),
		TotalAmount:                    Money(inv.TotalAmount, c),
		TotalPaidAmount:                Money(inv.TotalPaidAmount, c),
		OffsetAmount:                   Money(inv.OffsettedAmount, c),
		TotalDueAmount:                 Money(inv.TotalDueAmount(), c),
		IssuingDate:                    optDate(inv.IssuingDate),
		PaymentDueDate:                 optDate(inv.PaymentDueDate),
		ExpectedFinalizationDate:       optDate(inv.ExpectedFinalizationDate),
		AppliedGracePeriodDays:         new(int32(inv.AppliedGracePeriodDays)),
		NetPaymentTermDays:             new(int32(inv.NetPaymentTermDays)),
		PaymentOverdue:                 new(inv.PaymentOverdue),
		PaymentDisputeLostAt:           TimestampPtr(inv.PaymentDisputeLostAt),
		SelfBilled:                     new(inv.SelfBilled),
		VoidedInvoiceId:                idString(inv.VoidedInvoiceID),
		RegeneratedInvoiceId:           idString(v.RegeneratedInvoiceID),
		PaymentMethodType:              new(invPaymentMethodTypes.Proto(inv.PaymentMethodType)),
		PaymentMethodId:                idString(inv.PaymentMethodID),
		Metadata:                       Metadata(inv.Metadata),
		FileAvailable:                  new(!inv.File.IsZero()),
		XmlFileAvailable:               new(!inv.XMLFile.IsZero()),
		FinalizedAt:                    TimestampPtr(inv.FinalizedAt),
		VoidedAt:                       TimestampPtr(inv.VoidedAt),
		CreatedAt:                      Timestamp(inv.CreatedAt),
		UpdatedAt:                      Timestamp(inv.UpdatedAt),
	}
	for _, s := range v.Subscriptions {
		is := s.Subscription
		b.Subscriptions = append(b.Subscriptions, invoicev1.InvoiceSubscription_builder{
			SubscriptionId:         idString(is.SubscriptionID),
			ExternalSubscriptionId: new(s.ExternalID),
			InvoicingReason:        new(invReasons.Proto(is.Reason)),
			Period:                 dateTimeRange(is.Period.From, is.Period.To),
			ChargesPeriod:          dateTimeRange(is.ChargesPeriod.From, is.ChargesPeriod.To),
			FixedChargesPeriod:     dateTimeRange(is.FixedChargesPeriod.From, is.FixedChargesPeriod.To),
		}.Build())
	}
	for _, f := range v.Fees {
		b.Fees = append(b.Fees, feeProto(f))
	}
	for _, cr := range v.Credits {
		k := cr.Credit
		b.Credits = append(b.Credits, invoicev1.InvoiceCredit_builder{
			AppliedCouponId:             idString(k.AppliedCouponID),
			CreditNoteId:                idString(k.CreditNoteID),
			ProgressiveBillingInvoiceId: idString(k.ProgressiveBillingInvoiceID),
			Amount:                      Money(k.Amount, c),
			Label:                       new(cr.Label),
		}.Build())
	}
	for _, t := range v.AppliedTaxes {
		at := invoicev1.AppliedTax_builder{
			TaxId:         idString(t.TaxID),
			TaxCode:       new(t.TaxCode),
			TaxName:       new(t.TaxName),
			TaxRate:       new(Decimal(t.TaxRate)),
			TaxableAmount: Money(t.TaxableBaseAmount, c),
			Amount:        Money(t.Amount, c),
			PreciseAmount: new(Decimal(t.PreciseAmount)),
		}
		if t.TaxDescription != nil {
			at.TaxDescription = new(*t.TaxDescription)
		}
		b.AppliedTaxes = append(b.AppliedTaxes, at.Build())
	}
	for _, s := range v.Settlements {
		b.Settlements = append(b.Settlements, invoicev1.InvoiceSettlement_builder{
			SettlementType: new(invSettlementTypes.Proto(s.Type)),
			PaymentId:      idString(s.PaymentID),
			CreditNoteId:   idString(s.CreditNoteID),
			Amount:         Money(s.Amount, c),
			CreatedAt:      Timestamp(s.CreatedAt),
		}.Build())
	}
	for _, e := range v.Errors {
		b.Errors = append(b.Errors, invoicev1.InvoiceError_builder{
			Code:      new(invErrorCodes.Proto(e.Code)),
			Message:   new(string(e.Details)),
			CreatedAt: Timestamp(e.CreatedAt),
		}.Build())
	}
	if inv.PurchaseOrderNumber != nil {
		b.PurchaseOrderNumber = new(*inv.PurchaseOrderNumber)
	}
	for _, cs := range v.CustomSections {
		as := invoicev1.AppliedInvoiceCustomSection_builder{
			Id:        new(cs.ID.String()),
			Code:      new(cs.Code),
			Name:      new(cs.Name),
			CreatedAt: Timestamp(cs.CreatedAt),
		}
		if cs.DisplayName != nil {
			as.DisplayName = new(*cs.DisplayName)
		}
		if cs.Details != nil {
			as.Details = new(*cs.Details)
		}
		b.AppliedInvoiceCustomSections = append(b.AppliedInvoiceCustomSections, as.Build())
	}
	return b.Build()
}
