package x402

import "fmt"

// CustomerExternalID is the external ID of the customer of the agent
// address in family f; false when address is not valid in f.
//
// source: app/services/x402/external_ids.rb:5-7
func CustomerExternalID(address string, f Family) (string, bool) {
	n, ok := normalizedAddress(address, f)
	if !ok {
		return "", false
	}
	return "x402_" + n, true
}

// SubscriptionExternalID is the external ID of the agent's subscription
// to planCode; false when address is not valid in f.
//
// source: app/services/x402/external_ids.rb:9-11
func SubscriptionExternalID(address, planCode string, f Family) (string, bool) {
	n, ok := normalizedAddress(address, f)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("x402_%s_%s", n, planCode), true
}

func normalizedAddress(address string, f Family) (string, bool) {
	if !ValidAddress(address, f) {
		return "", false
	}
	return NormalizeAddress(address, f), true
}
