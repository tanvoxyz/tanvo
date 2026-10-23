package pricing

import "slices"

// Chargeable is the kind of charge properties belong to.
type Chargeable int

const (
	// UsageCharge is a charge on a metric's usage; it supports every model.
	UsageCharge Chargeable = iota
	// FixedCharge is a charge on a fixed quantity (an add-on's units); it
	// supports the standard, graduated and volume models only.
	FixedCharge
)

// modelKeys returns the properties a model of chargeable uses.
func modelKeys(chargeable Chargeable, model ChargeModel) []string {
	switch model {
	case Standard:
		return []string{"amount"}
	case Graduated:
		return []string{"graduated_ranges"}
	case Volume:
		return []string{"volume_ranges"}
	}
	if chargeable != UsageCharge {
		return nil
	}
	switch model {
	case GraduatedPercentage:
		return []string{"graduated_percentage_ranges"}
	case Package:
		return []string{"amount", "free_units", "package_size"}
	case Percentage:
		return []string{
			"fixed_amount", "free_units_per_events", "free_units_per_total_aggregation",
			"per_transaction_max_amount", "per_transaction_min_amount", "rate",
		}
	default:
		return nil
	}
}

// FilterProperties returns the raw properties model of chargeable uses,
// dropping every other key: the model's own keys, and the grouping keys
// when they are present (blank pricing group keys are removed). An empty
// model keeps nothing.
func FilterProperties(chargeable Chargeable, model ChargeModel, raw map[string]any) map[string]any {
	out := map[string]any{}
	if model == "" {
		return out
	}
	keys := modelKeys(chargeable, model)
	if present(raw["pricing_group_keys"]) {
		keys = append(keys, "pricing_group_keys")
	}
	if present(raw["presentation_group_keys"]) {
		keys = append(keys, "presentation_group_keys")
	}
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	if list, ok := out["pricing_group_keys"].([]any); ok {
		out["pricing_group_keys"] = slices.DeleteFunc(slices.Clone(list), func(e any) bool {
			s, ok := e.(string)
			return ok && s == ""
		})
	}
	return out
}
