package rpc

import (
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

var connectionBehaviors = NewEnum(map[integration.ConnectionBehavior]commonv1.ConnectionBehavior{
	integration.ConnectionBehaviorSpecific: commonv1.ConnectionBehavior_CONNECTION_BEHAVIOR_SPECIFIC,
	integration.ConnectionBehaviorSkip:     commonv1.ConnectionBehavior_CONNECTION_BEHAVIOR_SKIP,
	integration.ConnectionBehaviorInherit:  commonv1.ConnectionBehavior_CONNECTION_BEHAVIOR_INHERIT,
})

// connectionChoices converts the connections map of a request; nil when
// it is empty, which changes nothing.
func connectionChoices(m map[string]*commonv1.ConnectionChoice) map[string]kernel.ConnectionChoice {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]kernel.ConnectionChoice, len(m))
	for cat, c := range m {
		out[cat] = kernel.ConnectionChoice{Code: c.GetCode(), Behavior: string(connectionBehaviors.Core(c.GetBehavior()))}
	}
	return out
}

// connectionRoutesProto converts a billing object's connection routing
// (the connections of lago's subscription, wallet and recurring rule
// serializers).
func connectionRoutesProto(routes []kernel.ConnectionRoute) map[string]*commonv1.ConnectionChoice {
	out := make(map[string]*commonv1.ConnectionChoice, len(routes))
	for _, r := range routes {
		b := commonv1.ConnectionChoice_builder{Behavior: new(connectionBehaviors.Proto(r.Behavior))}
		if r.Code != "" {
			b.Code = new(r.Code)
		}
		out[string(r.Category)] = b.Build()
	}
	return out
}
