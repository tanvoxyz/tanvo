package tax

// TaxSource names where a fee's taxes come from.
type TaxSource string

// Tax sources, in precedence order.
const (
	SourceExplicit      TaxSource = "explicit"
	SourceAddOn         TaxSource = "add_on"
	SourceCharge        TaxSource = "charge"
	SourceFixedCharge   TaxSource = "fixed_charge"
	SourceCommitment    TaxSource = "commitment"
	SourcePlan          TaxSource = "plan"
	SourceCustomer      TaxSource = "customer"
	SourceBillingEntity TaxSource = "billing_entity"
)

// TaxSources are the taxes attached to everything a fee relates to.
type TaxSources struct {
	// FeeType is the fee's type; the add-on, charge, fixed charge,
	// commitment and plan sources apply only to the matching fee types.
	FeeType FeeType
	// HasExplicitTaxes is set when the fee was created with explicit tax
	// codes (one-off invoices); ExplicitTaxes then wins even when empty
	// (codes matching no tax of the organization are dropped by the
	// caller).
	HasExplicitTaxes bool
	ExplicitTaxes    []Tax
	// AddOn, Charge, FixedCharge and Commitment are the taxes of the fee's
	// add-on, charge, fixed charge or commitment.
	AddOn       []Tax
	Charge      []Tax
	FixedCharge []Tax
	Commitment  []Tax
	// Plan are the taxes of the subscription's plan (or of the plan given
	// for the computation).
	Plan []Tax
	// Customer are the customer's taxes.
	Customer []Tax
	// BillingEntity are the default taxes of the customer's billing entity.
	BillingEntity []Tax
}

// ResolveFeeTaxes returns the taxes that apply to a fee and their source. The
// first non-empty source wins, in this order: explicit codes, add-on (add-on
// fees), charge (charge fees), fixed charge (fixed charge fees), commitment
// (commitment fees), plan (charge, subscription, commitment and fixed charge
// fees), customer, billing entity. With no source it returns the billing
// entity's (empty) taxes.
func ResolveFeeTaxes(s TaxSources) ([]Tax, TaxSource) {
	if s.HasExplicitTaxes {
		return s.ExplicitTaxes, SourceExplicit
	}
	switch {
	case s.FeeType == FeeAddOn && len(s.AddOn) > 0:
		return s.AddOn, SourceAddOn
	case s.FeeType == FeeCharge && len(s.Charge) > 0:
		return s.Charge, SourceCharge
	case s.FeeType == FeeFixedCharge && len(s.FixedCharge) > 0:
		return s.FixedCharge, SourceFixedCharge
	case s.FeeType == FeeCommitment && len(s.Commitment) > 0:
		return s.Commitment, SourceCommitment
	}
	switch s.FeeType {
	case FeeCharge, FeeSubscription, FeeCommitment, FeeFixedCharge:
		if len(s.Plan) > 0 {
			return s.Plan, SourcePlan
		}
	}
	if len(s.Customer) > 0 {
		return s.Customer, SourceCustomer
	}
	return s.BillingEntity, SourceBillingEntity
}
